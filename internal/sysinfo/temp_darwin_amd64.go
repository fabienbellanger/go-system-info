//go:build darwin && amd64

package sysinfo

// Lecture directe de la température CPU sur les Mac Intel via le System
// Management Controller (SMC).
//
// Pourquoi ne pas passer par gopsutil comme sur les autres plateformes : sur
// darwin/amd64, gopsutil décode mal les capteurs SMC. Son getTemperature
// renvoie 0 précisément pour le format « sp78 » — celui qu'utilisent justement
// les capteurs de température — de sorte que *tous* les capteurs ressortent à
// 0 °C et que l'interface masque le champ. On rouvre donc l'AppleSMC nous-mêmes
// (via purego, comme gopsutil en interne — pas de cgo, `CGO_ENABLED=0` reste
// valable) et on décode correctement les formats sp78 / flt.
//
// Sur Apple Silicon (darwin/arm64), gopsutil lit la température par l'API HID
// IOKit et fonctionne : ce fichier ne concerne donc que l'Intel.

import (
	"math"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Clés SMC des capteurs CPU, par ordre de préférence. On retient la première
// clé lisible et non aberrante — et non le maximum global. Les moniteurs grand
// public (CleanMyMac, iStat…) affichent la sonde de proximité/die du CPU
// (TC0P/TC0D) ; les sondes par cœur (TCxC) et le PECI (TCXC) lisent ~10-15 °C
// plus chaud sous charge. Prendre le max donnait donc une valeur nettement
// au-dessus des moniteurs usuels. On garde les sondes chaudes en dernier recours,
// pour les modèles qui n'exposent pas de proximité/die.
var cpuTempKeys = []string{
	"TC0P",         // CPU proximity — préféré (correspond aux moniteurs usuels)
	"TC0D",         // CPU die
	"TC0H",         // CPU heatsink
	"TCAD",         // CPU package
	"TCXC",         // PECI CPU (lit chaud)
	"TCXc",         // PECI CPU
	"TC0E", "TC0F", // CPU
	"TC1C", "TC2C", "TC3C", "TC4C", // cœurs (junction, lisent chaud)
	"TC5C", "TC6C", "TC7C", "TC8C",
}

// readHottestTemp ouvre l'AppleSMC et renvoie la température CPU de la première
// clé lisible dans cpuTempKeys (ordre de préférence), en écartant les valeurs
// nulles ou aberrantes (> 130 °C). Best-effort : tout échec (SMC indisponible)
// renvoie 0, et l'interface masque alors le champ.
func readHottestTemp() (float64, string) {
	smc, err := openSMC()
	if err != nil {
		return 0, ""
	}
	defer smc.close()

	for _, key := range cpuTempKeys {
		if c := smc.readTemp(key); c > 0 && c < 130 {
			return c, key
		}
	}
	return 0, ""
}

// --- Accès bas niveau à l'AppleSMC via IOKit (purego) ---

const (
	ioServiceSMC = "AppleSMC"

	kSMCHandleYPCEvent = 2
	kSMCReadKey        = 5
	kSMCGetKeyInfo     = 9

	kSMCSuccess = 0
)

// Structures d'échange avec le pilote SMC. La disposition doit correspondre
// exactement à celle attendue par IOConnectCallStructMethod (identique à celle
// de gopsutil, dont seul le *décodage* est fautif — pas la lecture brute).
type (
	smcVersion struct {
		major, minor, build, reserved byte
		release                       uint16
	}
	smcPLimitData struct {
		version, length                 uint16
		cpuPLimit, gpuPLimit, memPLimit uint32
	}
	smcKeyInfoData struct {
		dataSize, dataType uint32
		dataAttributes     uint8
	}
	smcParamStruct struct {
		key        uint32
		vers       smcVersion
		plimitData smcPLimitData
		keyInfo    smcKeyInfoData
		result     uint8
		status     uint8
		data8      uint8
		data32     uint32
		bytes      [32]byte
	}
)

// smc encapsule une connexion ouverte au pilote AppleSMC et les fonctions IOKit
// résolues dynamiquement.
type smc struct {
	iokit                     uintptr
	conn                      uint32
	ioConnectCallStructMethod func(conn, selector uint32, in, inCnt, out uintptr, outCnt *uintptr) int32
	ioServiceClose            func(conn uint32) int32
}

func openSMC() (*smc, error) {
	iokit, err := purego.Dlopen(
		"/System/Library/Frameworks/IOKit.framework/IOKit",
		purego.RTLD_LAZY|purego.RTLD_GLOBAL,
	)
	if err != nil {
		return nil, err
	}

	var (
		ioServiceMatching           func(name string) uintptr
		ioServiceGetMatchingService func(mainPort uint32, matching uintptr) uint32
		ioServiceOpen               func(service, owningTask, connType uint32, connect *uint32) int32
		ioObjectRelease             func(object uint32) int32
		machTaskSelf                func() uint32
	)
	purego.RegisterLibFunc(&ioServiceMatching, iokit, "IOServiceMatching")
	purego.RegisterLibFunc(&ioServiceGetMatchingService, iokit, "IOServiceGetMatchingService")
	purego.RegisterLibFunc(&ioServiceOpen, iokit, "IOServiceOpen")
	purego.RegisterLibFunc(&ioObjectRelease, iokit, "IOObjectRelease")
	// mach_task_self est résolu via IOKit ouvert en RTLD_GLOBAL (symbole global).
	purego.RegisterLibFunc(&machTaskSelf, iokit, "mach_task_self")

	s := &smc{iokit: iokit}
	purego.RegisterLibFunc(&s.ioConnectCallStructMethod, iokit, "IOConnectCallStructMethod")
	purego.RegisterLibFunc(&s.ioServiceClose, iokit, "IOServiceClose")

	service := ioServiceGetMatchingService(0, ioServiceMatching(ioServiceSMC))
	if service == 0 {
		purego.Dlclose(iokit)
		return nil, errSMCNotFound
	}

	var conn uint32
	if ioServiceOpen(service, machTaskSelf(), 0, &conn) != kSMCSuccess {
		ioObjectRelease(service)
		purego.Dlclose(iokit)
		return nil, errSMCOpen
	}
	ioObjectRelease(service)

	s.conn = conn
	return s, nil
}

func (s *smc) close() {
	s.ioServiceClose(s.conn)
	purego.Dlclose(s.iokit)
}

// readTemp lit la clé SMC donnée et renvoie la température en °C (0 si absente,
// illisible ou d'un format inattendu).
func (s *smc) readTemp(key string) float64 {
	// 1) Récupérer la taille et le type de la donnée. Seule cette réponse
	// renseigne keyInfo : l'appel de lecture (étape 2) le laisse à zéro, d'où la
	// conservation de dataType/dataSize ici.
	info, ok := s.call(&smcParamStruct{key: fourCC(key), data8: kSMCGetKeyInfo})
	if !ok {
		return 0
	}
	dataType, dataSize := info.keyInfo.dataType, info.keyInfo.dataSize

	// 2) Lire la valeur brute.
	read := &smcParamStruct{key: fourCC(key), data8: kSMCReadKey}
	read.keyInfo.dataSize = dataSize
	out, ok := s.call(read)
	if !ok {
		return 0
	}

	return decodeSMCTemp(dataType, dataSize, out.bytes[:])
}

func (s *smc) call(in *smcParamStruct) (*smcParamStruct, bool) {
	out := new(smcParamStruct)
	size := unsafe.Sizeof(*out)
	outSize := size
	res := s.ioConnectCallStructMethod(
		s.conn, kSMCHandleYPCEvent,
		uintptr(unsafe.Pointer(in)), size,
		uintptr(unsafe.Pointer(out)), &outSize,
	)
	if res != 0 || out.result != kSMCSuccess {
		return nil, false
	}
	return out, true
}

// decodeSMCTemp convertit une valeur SMC en degrés Celsius selon son type. Les
// capteurs de température exposent « sp78 » (point fixe signé 8.8) sur la
// plupart des Mac Intel, ou « flt » (float32) sur certains modèles récents.
func decodeSMCTemp(dataType, dataSize uint32, b []byte) float64 {
	switch dataType {
	case fourCC("sp78"):
		if dataSize >= 2 {
			// Grand-boutiste : octet de poids fort = partie entière signée.
			return float64(int16(uint16(b[0])<<8|uint16(b[1]))) / 256.0
		}
	case fourCC("flt "):
		if dataSize >= 4 {
			// Petit-boutiste sur Intel.
			bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
			return float64(math.Float32frombits(bits))
		}
	}
	return 0
}

// fourCC encode un code SMC de 4 caractères en uint32 grand-boutiste (comme le
// pilote AppleSMC l'attend).
func fourCC(code string) uint32 {
	if len(code) != 4 {
		return 0
	}
	return uint32(code[0])<<24 | uint32(code[1])<<16 | uint32(code[2])<<8 | uint32(code[3])
}

type smcError string

func (e smcError) Error() string { return string(e) }

const (
	errSMCNotFound smcError = "AppleSMC introuvable"
	errSMCOpen     smcError = "ouverture de l'AppleSMC impossible"
)
