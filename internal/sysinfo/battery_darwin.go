//go:build darwin

package sysinfo

// Lecture de l'état de la batterie sur macOS via le registre IOKit
// (service AppleSmartBattery), le même que celui qu'interrogent
// `ioreg -c AppleSmartBattery` et « Informations système ▸ Alimentation ».
//
// Pourquoi ce service plutôt que l'API IOPowerSources, plus simple : cette
// dernière ne donne que la charge et l'état (branché/en charge), alors que
// AppleSmartBattery expose aussi le nombre de cycles, les capacités (d'origine
// et actuelle, d'où la santé), la température et la puissance instantanée —
// c'est-à-dire l'essentiel de ce qu'affiche la carte.
//
// L'accès passe par purego (comme la lecture SMC des Mac Intel) : pas de cgo,
// `CGO_ENABLED=0` reste valable pour la compilation. Les bibliothèques IOKit et
// CoreFoundation sont ouvertes une seule fois pour la durée du processus et
// jamais refermées : gopsutil a constaté des SIGBUS/SIGSEGV lorsque le runtime Go
// (GC, timers) interagit avec des handles invalidés par un Dlclose.

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Constantes CoreFoundation / IOKit utilisées ici.
const (
	kCFStringEncodingUTF8 = 0x08000100
	kCFNumberSInt64Type   = 4 // CFNumberType
	kIOReturnSuccess      = 0
	kIOMainPortDefault    = 0
	ioServiceBattery      = "AppleSmartBattery"
)

// Clés du dictionnaire AppleSmartBattery. Les noms sont ceux du registre IOKit.
const (
	keyBatteryInstalled = "BatteryInstalled"
	keyCurrentCapacity  = "CurrentCapacity"
	keyMaxCapacity      = "MaxCapacity"
	keyDesignCapacity   = "DesignCapacity"
	keyNominalCapacity  = "NominalChargeCapacity"
	keyBatteryData      = "BatteryData"
	keyCycleCount       = "CycleCount"
	keyDesignCycleCount = "DesignCycleCount9C"
	keyIsCharging       = "IsCharging"
	keyExternal         = "ExternalConnected"
	keyFullyCharged     = "FullyCharged"
	keyTimeRemaining    = "TimeRemaining"
	keyAvgTimeToEmpty   = "AvgTimeToEmpty"
	keyAvgTimeToFull    = "AvgTimeToFull"
	keyTemperature      = "Temperature"
	keyVoltage          = "Voltage"
	keyAmperage         = "Amperage"
	keyFailureStatus    = "PermanentFailureStatus"
)

// batteryTimeUnknown est la valeur sentinelle que le contrôleur renvoie pour une
// estimation de temps indisponible (typiquement juste après un branchement).
const batteryTimeUnknown = 65535

// readBattery lit le service AppleSmartBattery et en dérive l'état de la
// batterie. Renvoie nil sur un Mac sans batterie (iMac, Mac mini, Mac Studio…)
// ou si IOKit est inaccessible.
func readBattery() *Battery {
	lib, err := loadIOKit()
	if err != nil {
		return nil
	}
	props, ok := lib.batteryProperties()
	if !ok {
		return nil
	}
	defer lib.cfRelease(props)

	// Une batterie retirée ou en défaut peut laisser le service présent : on
	// s'assure qu'elle est bien installée avant d'exposer quoi que ce soit.
	if installed, ok := lib.boolValue(props, keyBatteryInstalled); ok && !installed {
		return nil
	}

	current, hasCurrent := lib.intValue(props, keyCurrentCapacity)
	maxCap, hasMax := lib.intValue(props, keyMaxCapacity)
	if !hasCurrent || !hasMax || maxCap <= 0 {
		return nil
	}

	charging, _ := lib.boolValue(props, keyIsCharging)
	plugged, _ := lib.boolValue(props, keyExternal)
	full, _ := lib.boolValue(props, keyFullyCharged)

	b := &Battery{
		// Rapport plutôt que valeur brute : selon le modèle, CurrentCapacity est
		// déjà un pourcentage (MaxCapacity = 100, Apple Silicon) ou une capacité en
		// mAh (Mac Intel). Le rapport couvre les deux cas.
		Percent: min(float64(current)/float64(maxCap)*100, 100),
		State:   batteryState(charging, plugged, full),
	}

	if cycles, ok := lib.intValue(props, keyCycleCount); ok {
		b.Cycles = int(cycles)
	}
	if designCycles, ok := lib.intValue(props, keyDesignCycleCount); ok && designCycles > 0 {
		b.DesignCycles = int(designCycles)
	}

	// Santé : capacité maximale actuelle / capacité d'origine. NominalChargeCapacity
	// est la valeur sur laquelle macOS s'appuie (« Capacité maximale » dans les
	// réglages) ; à défaut — Mac Intel, où la clé peut manquer — MaxCapacity est
	// déjà exprimée en mAh et joue le même rôle.
	design, hasDesign := lib.intValue(props, keyDesignCapacity)
	fullCap, hasFullCap := lib.intValue(props, keyNominalCapacity)
	if !hasFullCap && maxCap > 100 {
		fullCap, hasFullCap = maxCap, true // Intel : MaxCapacity est en mAh
	}
	if hasDesign && hasFullCap && design > 0 {
		b.DesignCapacityMAh = int(design)
		b.FullCapacityMAh = int(fullCap)
		b.HealthPercent = batteryHealth(float64(fullCap), float64(design))
	}
	// Sur Apple Silicon, macOS publie sa propre « capacité maximale » (celle des
	// Réglages ▸ Batterie ▸ État) dans BatteryData/MaxCapacity, déjà exprimée en
	// pourcentage et légèrement lissée : elle peut différer d'un point du rapport
	// brut ci-dessus (99 % calculés contre 100 % affichés). On la préfère quand
	// elle est présente, pour ne pas contredire le système. Le garde-fou « ≤ 100 »
	// écarte les modèles où cette même clé porte une capacité en mAh.
	if data, ok := lib.dictValue(props, keyBatteryData); ok {
		if health, ok := lib.intValue(data, keyMaxCapacity); ok && health > 0 && health <= 100 {
			b.HealthPercent = float64(health)
		}
	}

	b.TimeRemainingMinutes = int(lib.batteryTime(props, charging))

	// Température : centièmes de degré Celsius.
	if t, ok := lib.intValue(props, keyTemperature); ok && t > 0 {
		b.TempCelsius = float64(t) / 100
	}

	// Puissance instantanée : tension (mV) × intensité (mA, négative en décharge).
	// Le sens est déjà porté par State, on n'expose donc que la valeur absolue.
	if mv, ok := lib.intValue(props, keyVoltage); ok && mv > 0 {
		if ma, ok := lib.intValue(props, keyAmperage); ok && ma != 0 {
			if ma < 0 {
				ma = -ma
			}
			b.PowerWatts = float64(mv) * float64(ma) / 1e6
		}
	}

	b.Condition = batteryConditionNormal
	if failure, ok := lib.intValue(props, keyFailureStatus); ok && failure != 0 {
		b.Condition = batteryConditionService
	}
	return b
}

// batteryTime renvoie l'estimation en minutes du temps restant (autonomie en
// décharge, fin de charge sinon), ou 0 quand le contrôleur ne sait pas encore
// l'évaluer. TimeRemaining est la valeur générique ; les moyennes AvgTimeTo*
// prennent le relais quand elle manque.
func (l *ioKitLib) batteryTime(props uintptr, charging bool) int64 {
	keys := []string{keyTimeRemaining, keyAvgTimeToEmpty}
	if charging {
		keys = []string{keyAvgTimeToFull, keyTimeRemaining}
	}
	for _, key := range keys {
		if v, ok := l.intValue(props, key); ok && v > 0 && v != batteryTimeUnknown {
			return v
		}
	}
	return 0
}

// --- Accès bas niveau à IOKit / CoreFoundation via purego ---

// ioKitLib regroupe les fonctions IOKit et CoreFoundation résolues
// dynamiquement. Une seule instance vit pour la durée du processus (voir
// loadIOKit) : les bibliothèques ne sont jamais refermées.
type ioKitLib struct {
	ioServiceMatching                 func(name string) uintptr
	ioServiceGetMatchingService       func(mainPort uint32, matching uintptr) uint32
	ioRegistryEntryCreateCFProperties func(entry uint32, properties *uintptr, allocator uintptr, options uint32) int32
	ioObjectRelease                   func(object uint32) int32

	cfRelease                 func(cf uintptr)
	cfStringCreateWithCString func(alloc uintptr, cStr string, encoding uint32) uintptr
	cfDictionaryGetValue      func(dict, key uintptr) uintptr
	cfGetTypeID               func(cf uintptr) uintptr
	cfNumberGetTypeID         func() uintptr
	cfBooleanGetTypeID        func() uintptr
	cfDictionaryGetTypeID     func() uintptr
	cfNumberGetValue          func(number uintptr, theType int32, valuePtr unsafe.Pointer) bool
	cfBooleanGetValue         func(boolean uintptr) bool
}

var (
	ioKitOnce sync.Once
	ioKit     *ioKitLib
	ioKitErr  error
)

// loadIOKit ouvre IOKit et CoreFoundation au premier appel et mémorise le
// résultat (succès comme échec) : les relevés suivants réutilisent les mêmes
// handles.
func loadIOKit() (*ioKitLib, error) {
	ioKitOnce.Do(func() {
		iokit, err := purego.Dlopen(
			"/System/Library/Frameworks/IOKit.framework/IOKit",
			purego.RTLD_LAZY|purego.RTLD_GLOBAL,
		)
		if err != nil {
			ioKitErr = err
			return
		}
		cf, err := purego.Dlopen(
			"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			purego.RTLD_LAZY|purego.RTLD_GLOBAL,
		)
		if err != nil {
			ioKitErr = err
			return
		}

		l := &ioKitLib{}
		purego.RegisterLibFunc(&l.ioServiceMatching, iokit, "IOServiceMatching")
		purego.RegisterLibFunc(&l.ioServiceGetMatchingService, iokit, "IOServiceGetMatchingService")
		purego.RegisterLibFunc(&l.ioRegistryEntryCreateCFProperties, iokit, "IORegistryEntryCreateCFProperties")
		purego.RegisterLibFunc(&l.ioObjectRelease, iokit, "IOObjectRelease")
		purego.RegisterLibFunc(&l.cfRelease, cf, "CFRelease")
		purego.RegisterLibFunc(&l.cfStringCreateWithCString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&l.cfDictionaryGetValue, cf, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&l.cfGetTypeID, cf, "CFGetTypeID")
		purego.RegisterLibFunc(&l.cfNumberGetTypeID, cf, "CFNumberGetTypeID")
		purego.RegisterLibFunc(&l.cfBooleanGetTypeID, cf, "CFBooleanGetTypeID")
		purego.RegisterLibFunc(&l.cfDictionaryGetTypeID, cf, "CFDictionaryGetTypeID")
		purego.RegisterLibFunc(&l.cfNumberGetValue, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&l.cfBooleanGetValue, cf, "CFBooleanGetValue")
		ioKit = l
	})
	if ioKitErr != nil {
		return nil, ioKitErr
	}
	if ioKit == nil {
		return nil, errIOKitUnavailable
	}
	return ioKit, nil
}

// batteryProperties copie le dictionnaire de propriétés du service
// AppleSmartBattery. L'appelant doit le relâcher (cfRelease).
func (l *ioKitLib) batteryProperties() (uintptr, bool) {
	// IOServiceGetMatchingService consomme la référence du dictionnaire de
	// correspondance : rien à relâcher de ce côté.
	service := l.ioServiceGetMatchingService(kIOMainPortDefault, l.ioServiceMatching(ioServiceBattery))
	if service == 0 {
		return 0, false // Mac sans batterie
	}
	defer l.ioObjectRelease(service)

	var props uintptr
	if l.ioRegistryEntryCreateCFProperties(service, &props, 0, 0) != kIOReturnSuccess || props == 0 {
		return 0, false
	}
	return props, true
}

// intValue lit une clé numérique du dictionnaire. Le second retour distingue une
// clé absente (ou d'un autre type) d'une valeur nulle légitime.
func (l *ioKitLib) intValue(dict uintptr, key string) (int64, bool) {
	v := l.value(dict, key)
	if v == 0 || l.cfGetTypeID(v) != l.cfNumberGetTypeID() {
		return 0, false
	}
	var out int64
	if !l.cfNumberGetValue(v, kCFNumberSInt64Type, unsafe.Pointer(&out)) {
		return 0, false
	}
	return out, true
}

// dictValue lit une clé dont la valeur est elle-même un dictionnaire
// (AppleSmartBattery en imbrique plusieurs, dont BatteryData). La valeur suit la
// règle « Get » : elle vit aussi longtemps que le dictionnaire parent, rien à
// relâcher.
func (l *ioKitLib) dictValue(dict uintptr, key string) (uintptr, bool) {
	v := l.value(dict, key)
	if v == 0 || l.cfGetTypeID(v) != l.cfDictionaryGetTypeID() {
		return 0, false
	}
	return v, true
}

// boolValue lit une clé booléenne du dictionnaire.
func (l *ioKitLib) boolValue(dict uintptr, key string) (bool, bool) {
	v := l.value(dict, key)
	if v == 0 || l.cfGetTypeID(v) != l.cfBooleanGetTypeID() {
		return false, false
	}
	return l.cfBooleanGetValue(v), true
}

// value renvoie la valeur associée à une clé (0 si absente). La valeur suit la
// règle CoreFoundation « Get » : elle n'est pas retenue et reste valide tant que
// le dictionnaire vit.
func (l *ioKitLib) value(dict uintptr, key string) uintptr {
	cfKey := l.cfStringCreateWithCString(0, key, kCFStringEncodingUTF8)
	if cfKey == 0 {
		return 0
	}
	defer l.cfRelease(cfKey)
	return l.cfDictionaryGetValue(dict, cfKey)
}

type ioKitError string

func (e ioKitError) Error() string { return string(e) }

const errIOKitUnavailable ioKitError = "IOKit indisponible"
