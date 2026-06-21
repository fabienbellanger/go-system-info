package sysinfo

import (
	"runtime"
	"testing"
)

func TestCollect(t *testing.T) {
	info, err := Collect()
	if err != nil {
		t.Fatalf("Collect() a renvoyé une erreur : %v", err)
	}
	if info == nil {
		t.Fatal("Collect() a renvoyé un Info nil")
	}

	if info.Timestamp.IsZero() {
		t.Error("Timestamp ne doit pas être nul")
	}

	// Hôte
	if info.Host.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, attendu %q", info.Host.GoVersion, runtime.Version())
	}
	if info.Host.OS == "" {
		t.Error("Host.OS ne doit pas être vide")
	}

	// CPU
	if want := runtime.NumCPU(); info.CPU.Cores != want {
		t.Errorf("CPU.Cores = %d, attendu %d", info.CPU.Cores, want)
	}
	assertPercent(t, "CPU.UsedPercent", info.CPU.UsedPercent)

	// Mémoire
	if info.Memory.TotalGB <= 0 {
		t.Errorf("Memory.TotalGB = %f, doit être > 0", info.Memory.TotalGB)
	}
	assertPercent(t, "Memory.UsedPercent", info.Memory.UsedPercent)

	// Disque
	if info.Disk.Path != "/" {
		t.Errorf("Disk.Path = %q, attendu \"/\"", info.Disk.Path)
	}
	if info.Disk.TotalGB <= 0 {
		t.Errorf("Disk.TotalGB = %f, doit être > 0", info.Disk.TotalGB)
	}
	assertPercent(t, "Disk.UsedPercent", info.Disk.UsedPercent)
}

// assertPercent vérifie qu'une valeur est un pourcentage valide (0–100).
func assertPercent(t *testing.T, name string, v float64) {
	t.Helper()
	if v < 0 || v > 100 {
		t.Errorf("%s = %f, doit être compris entre 0 et 100", name, v)
	}
}

func TestCollectDiskUnknownPath(t *testing.T) {
	var info Info
	if err := info.collectDisk("/chemin/inexistant/zzz"); err == nil {
		t.Error("collectDisk attendait une erreur pour un chemin inexistant")
	}
}
