//go:build linux

package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// writePowerSupply crée une entrée sysfs factice (un répertoire d'attributs
// texte, comme /sys/class/power_supply/BAT0).
func writePowerSupply(t *testing.T, root, name string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("création de %s : %v", dir, err)
	}
	for k, v := range attrs {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatalf("écriture de %s/%s : %v", dir, k, err)
		}
	}
}

// TestReadBatteryFromEnergy couvre le cas le plus répandu : un portable dont le
// pilote exprime les capacités en énergie (µWh), sur batterie.
func TestReadBatteryFromEnergy(t *testing.T) {
	root := t.TempDir()
	writePowerSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "0"})
	writePowerSupply(t, root, "BAT0", map[string]string{
		"type":               "Battery",
		"present":            "1",
		"status":             "Discharging",
		"capacity":           "72",
		"cycle_count":        "134",
		"energy_now":         "36000000", // 36 Wh
		"energy_full":        "50000000", // 50 Wh
		"energy_full_design": "60000000", // 60 Wh → santé 83,3 %
		"power_now":          "12000000", // 12 W → 3 h = 180 min
		"voltage_now":        "11500000",
		"health":             "Good",
	})

	b := readBatteryFrom(root)
	if b == nil {
		t.Fatal("readBatteryFrom = nil, attendu une batterie")
	}
	if b.Percent != 72 {
		t.Errorf("Percent = %v, attendu 72", b.Percent)
	}
	if b.State != batteryDischarging {
		t.Errorf("State = %q, attendu %q", b.State, batteryDischarging)
	}
	if b.Cycles != 134 {
		t.Errorf("Cycles = %d, attendu 134", b.Cycles)
	}
	if got := b.HealthPercent; got < 83.3 || got > 83.4 {
		t.Errorf("HealthPercent = %v, attendu ≈83,33", got)
	}
	// Capacités en µWh : non convertibles en mAh, les champs restent vides.
	if b.FullCapacityMAh != 0 || b.DesignCapacityMAh != 0 {
		t.Errorf("capacités en mAh renseignées à tort : %d / %d", b.FullCapacityMAh, b.DesignCapacityMAh)
	}
	if b.PowerWatts != 12 {
		t.Errorf("PowerWatts = %v, attendu 12", b.PowerWatts)
	}
	if b.TimeRemainingMinutes != 180 {
		t.Errorf("TimeRemainingMinutes = %d, attendu 180", b.TimeRemainingMinutes)
	}
	if b.Condition != batteryConditionNormal {
		t.Errorf("Condition = %q, attendu %q", b.Condition, batteryConditionNormal)
	}
}

// TestReadBatteryFromChargeCharging couvre l'autre famille de pilotes (µAh, d'où
// des capacités en mAh) et la charge en cours, secteur branché.
func TestReadBatteryFromChargeCharging(t *testing.T) {
	root := t.TempDir()
	writePowerSupply(t, root, "ADP1", map[string]string{"type": "Mains", "online": "1"})
	writePowerSupply(t, root, "BAT1", map[string]string{
		"type":               "Battery",
		"status":             "Charging",
		"capacity":           "40",
		"charge_now":         "2000000", // 2000 mAh
		"charge_full":        "4000000", // 4000 mAh
		"charge_full_design": "5000000", // 5000 mAh → santé 80 %
		"current_now":        "2000000", // 2 A
		"voltage_now":        "12000000",
		"temp":               "312",
	})

	b := readBatteryFrom(root)
	if b == nil {
		t.Fatal("readBatteryFrom = nil, attendu une batterie")
	}
	if b.State != batteryCharging {
		t.Errorf("State = %q, attendu %q", b.State, batteryCharging)
	}
	if b.HealthPercent != 80 {
		t.Errorf("HealthPercent = %v, attendu 80", b.HealthPercent)
	}
	if b.FullCapacityMAh != 4000 || b.DesignCapacityMAh != 5000 {
		t.Errorf("capacités = %d / %d mAh, attendu 4000 / 5000", b.FullCapacityMAh, b.DesignCapacityMAh)
	}
	if b.PowerWatts != 24 { // 2 A × 12 V
		t.Errorf("PowerWatts = %v, attendu 24", b.PowerWatts)
	}
	// Charge : (4000 − 2000) mAh à 2 A, soit une heure — le calcul doit se faire
	// en µAh ÷ µA, pas en mélangeant capacité et puissance.
	if b.TimeRemainingMinutes != 60 {
		t.Errorf("TimeRemainingMinutes = %d, attendu 60", b.TimeRemainingMinutes)
	}
	if b.TempCelsius != 31.2 {
		t.Errorf("TempCelsius = %v, attendu 31,2", b.TempCelsius)
	}
}

// TestReadBatteryFromStates vérifie les cas où aucune batterie exploitable n'est
// exposée : la carte doit alors disparaître de l'interface (relevé nil).
func TestReadBatteryFromStates(t *testing.T) {
	t.Run("racine absente", func(t *testing.T) {
		if b := readBatteryFrom(filepath.Join(t.TempDir(), "inexistant")); b != nil {
			t.Errorf("readBatteryFrom = %+v, attendu nil", b)
		}
	})

	t.Run("secteur seul (poste fixe)", func(t *testing.T) {
		root := t.TempDir()
		writePowerSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
		if b := readBatteryFrom(root); b != nil {
			t.Errorf("readBatteryFrom = %+v, attendu nil", b)
		}
	})

	t.Run("baie de batterie vide", func(t *testing.T) {
		root := t.TempDir()
		writePowerSupply(t, root, "BAT0", map[string]string{
			"type": "Battery", "present": "0", "capacity": "0",
		})
		if b := readBatteryFrom(root); b != nil {
			t.Errorf("readBatteryFrom = %+v, attendu nil", b)
		}
	})

	t.Run("batterie pleine sur secteur", func(t *testing.T) {
		root := t.TempDir()
		writePowerSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
		writePowerSupply(t, root, "BAT0", map[string]string{
			"type": "Battery", "status": "Full", "capacity": "100", "health": "Good",
		})
		b := readBatteryFrom(root)
		if b == nil || b.State != batteryCharged {
			t.Fatalf("readBatteryFrom = %+v, attendu l'état %q", b, batteryCharged)
		}
	})

	t.Run("batterie à remplacer", func(t *testing.T) {
		root := t.TempDir()
		writePowerSupply(t, root, "BAT0", map[string]string{
			"type": "Battery", "status": "Discharging", "capacity": "50", "health": "Dead",
		})
		b := readBatteryFrom(root)
		if b == nil || b.Condition != batteryConditionService {
			t.Fatalf("readBatteryFrom = %+v, attendu la condition %q", b, batteryConditionService)
		}
	})
}
