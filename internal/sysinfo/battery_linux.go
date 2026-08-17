//go:build linux

package sysinfo

// Lecture de l'état de la batterie sous Linux via sysfs
// (/sys/class/power_supply), l'interface standard du noyau — celle qu'utilisent
// upower, acpi et les indicateurs des environnements de bureau. Aucune
// dépendance ni appel externe : ce sont de simples fichiers texte.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// powerSupplyRoot est le répertoire sysfs listant batteries et alimentations.
const powerSupplyRoot = "/sys/class/power_supply"

func readBattery() *Battery {
	return readBatteryFrom(powerSupplyRoot)
}

// readBatteryFrom lit la première batterie trouvée sous root (racine sysfs
// injectable pour les tests). Renvoie nil si la machine n'en a pas — cas de la
// grande majorité des serveurs et des conteneurs.
func readBatteryFrom(root string) *Battery {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	// L'état du secteur est porté par une alimentation distincte (« Mains ») :
	// on le relève d'abord, car le statut de la batterie seul ne suffit pas à
	// distinguer « sur batterie » de « branché, charge en pause ».
	plugged := mainsOnline(root, entries)

	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if sysfsString(dir, "type") != "Battery" {
			continue
		}
		// « present » vaut 0 pour une baie de batterie vide.
		if present, ok := sysfsInt(dir, "present"); ok && present == 0 {
			continue
		}
		if b := readBatteryDir(dir, plugged); b != nil {
			return b
		}
	}
	return nil
}

// readBatteryDir construit l'état d'une batterie à partir de son répertoire
// sysfs. Renvoie nil si le niveau de charge n'est pas exploitable.
func readBatteryDir(dir string, plugged bool) *Battery {
	status := sysfsString(dir, "status") // Charging | Discharging | Full | Not charging | Unknown
	charging := status == "Charging"
	full := status == "Full"

	// Capacités : selon le pilote, exprimées en énergie (µWh) ou en charge (µAh).
	// Les deux jeux de clés sont équivalents pour nos calculs, pourvu qu'on ne les
	// mélange pas.
	now, hasNow := sysfsInt(dir, "energy_now")
	fullCap, hasFull := sysfsInt(dir, "energy_full")
	design, hasDesign := sysfsInt(dir, "energy_full_design")
	inMAh := false
	if !hasFull {
		now, hasNow = sysfsInt(dir, "charge_now")
		fullCap, hasFull = sysfsInt(dir, "charge_full")
		design, hasDesign = sysfsInt(dir, "charge_full_design")
		inMAh = true // µAh → convertibles en mAh, contrairement aux µWh
	}

	b := &Battery{State: batteryState(charging, plugged, full)}

	// Le niveau de charge : « capacity » (en %) quand il existe — c'est la valeur
	// qu'affichent les indicateurs de bureau —, sinon le rapport des capacités.
	switch {
	case sysfsHas(dir, "capacity"):
		pct, ok := sysfsInt(dir, "capacity")
		if !ok {
			return nil
		}
		b.Percent = min(float64(pct), 100)
	case hasNow && hasFull && fullCap > 0:
		b.Percent = min(float64(now)/float64(fullCap)*100, 100)
	default:
		return nil // niveau de charge indéterminable : batterie inexploitable
	}

	if cycles, ok := sysfsInt(dir, "cycle_count"); ok && cycles > 0 {
		b.Cycles = int(cycles)
	}
	if hasFull && hasDesign && design > 0 {
		b.HealthPercent = batteryHealth(float64(fullCap), float64(design))
		if inMAh {
			// µAh → mAh.
			b.FullCapacityMAh = int(fullCap / 1000)
			b.DesignCapacityMAh = int(design / 1000)
		}
	}

	// Débit instantané, exposé soit en puissance (µW), soit en intensité (µA)
	// selon le pilote — les deux se déduisent l'un de l'autre par la tension.
	power, hasPower := sysfsInt(dir, "power_now")       // µW
	current, hasCurrent := sysfsInt(dir, "current_now") // µA
	voltage, hasVoltage := sysfsInt(dir, "voltage_now") // µV
	if !hasPower && hasCurrent && hasVoltage {
		power, hasPower = current*voltage/1e6, true
	}
	if !hasCurrent && hasPower && hasVoltage && voltage > 0 {
		current, hasCurrent = power*1e6/voltage, true
	}
	if hasPower {
		b.PowerWatts = float64(abs64(power)) / 1e6
	}

	// Le temps restant se calcule dans l'unité des capacités : µWh ÷ µW, ou
	// µAh ÷ µA. Mélanger les deux (des mAh divisés par des watts) donnerait une
	// autonomie fantaisiste.
	flow, hasFlow := power, hasPower
	if inMAh {
		flow, hasFlow = current, hasCurrent
	}
	b.TimeRemainingMinutes = int(batteryTimeLinux(dir, charging, now, fullCap, abs64(flow), hasNow && hasFull && hasFlow))

	// Température en dixièmes de degré Celsius (rarement exposée).
	if t, ok := sysfsInt(dir, "temp"); ok && t > 0 {
		b.TempCelsius = float64(t) / 10
	}

	b.Condition = batteryConditionNormal
	if health := sysfsString(dir, "health"); health != "" && health != "Good" && health != "Unknown" {
		b.Condition = batteryConditionService
	}
	return b
}

// batteryTimeLinux estime le temps restant en minutes. Le noyau expose parfois
// directement time_to_empty_now/time_to_full_now (en minutes) ; sinon on divise
// la capacité restante (ou à fournir) par le débit instantané — les deux étant
// exprimés dans la même unité, cf. l'appelant. Renvoie 0 quand rien n'est
// calculable : débit nul, ou estimation encore instable après un branchement.
func batteryTimeLinux(dir string, charging bool, now, fullCap, flow int64, computable bool) int64 {
	key := "time_to_empty_now"
	if charging {
		key = "time_to_full_now"
	}
	if v, ok := sysfsInt(dir, key); ok && v > 0 {
		return v
	}
	if !computable || flow <= 0 {
		return 0
	}
	remaining := now
	if charging {
		remaining = fullCap - now
	}
	if remaining <= 0 {
		return 0
	}
	return remaining * 60 / flow
}

// abs64 renvoie la valeur absolue d'un entier signé : les compteurs sysfs sont
// négatifs en décharge sur certains pilotes, alors que le sens est déjà porté
// par l'état de la batterie.
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// sysfsHas indique si un attribut existe dans le répertoire d'une alimentation.
func sysfsHas(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// sysfsString lit un attribut texte, débarrassé de son saut de ligne final
// (chaîne vide si l'attribut est absent ou illisible).
func sysfsString(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// sysfsInt lit un attribut entier. Le second retour distingue un attribut absent
// ou non numérique d'une valeur nulle légitime.
func sysfsInt(dir, name string) (int64, bool) {
	s := sysfsString(dir, name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// mainsOnline indique si une alimentation secteur est branchée. Le type exact
// varie (« Mains » pour un chargeur classique, « USB » pour une alimentation
// USB-C) : toute alimentation qui n'est pas une batterie et se déclare « online »
// compte.
func mainsOnline(root string, entries []os.DirEntry) bool {
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if sysfsString(dir, "type") == "Battery" {
			continue
		}
		if online, ok := sysfsInt(dir, "online"); ok && online == 1 {
			return true
		}
	}
	return false
}
