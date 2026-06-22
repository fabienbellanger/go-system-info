package sysinfo

import (
	"runtime"
	"testing"
	"time"
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

	// Charge moyenne : non négative (peut valoir 0 selon la plateforme).
	if info.Load.One < 0 || info.Load.Five < 0 || info.Load.Fifteen < 0 {
		t.Errorf("Load négatif : %+v", info.Load)
	}

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

func TestNetRate(t *testing.T) {
	base := time.Unix(1000, 0)

	t.Run("débit nominal", func(t *testing.T) {
		prev := netTotals{recv: 1000, sent: 500, at: base}
		cur := netTotals{recv: 3000, sent: 1500, at: base.Add(2 * time.Second)}
		got := netRate(prev, cur)
		if got.RecvBytesPerSec != 1000 { // (3000-1000)/2
			t.Errorf("RecvBytesPerSec = %v, attendu 1000", got.RecvBytesPerSec)
		}
		if got.SentBytesPerSec != 500 { // (1500-500)/2
			t.Errorf("SentBytesPerSec = %v, attendu 500", got.SentBytesPerSec)
		}
	})

	t.Run("durée nulle", func(t *testing.T) {
		prev := netTotals{recv: 1000, at: base}
		cur := netTotals{recv: 3000, at: base}
		if got := netRate(prev, cur); got != (Net{}) {
			t.Errorf("attendu Net nul pour une durée nulle, obtenu %+v", got)
		}
	})

	t.Run("compteur réinitialisé", func(t *testing.T) {
		prev := netTotals{recv: 5000, sent: 5000, at: base}
		cur := netTotals{recv: 100, sent: 100, at: base.Add(time.Second)}
		got := netRate(prev, cur)
		if got.RecvBytesPerSec != 0 || got.SentBytesPerSec != 0 {
			t.Errorf("attendu 0 après réinitialisation, obtenu %+v", got)
		}
	})
}

func TestCollectDiskUnknownPath(t *testing.T) {
	var info Info
	if err := info.collectDisk("/chemin/inexistant/zzz"); err == nil {
		t.Error("collectDisk attendait une erreur pour un chemin inexistant")
	}
}

func TestHistoryRingBuffer(t *testing.T) {
	h := newHistory(3)

	if got := h.snapshot(); len(got) != 0 {
		t.Fatalf("snapshot initial = %d points, attendu 0", len(got))
	}

	// On insère plus d'échantillons que la capacité : seuls les 3 derniers
	// doivent subsister, dans l'ordre du plus ancien au plus récent.
	for i := 1; i <= 5; i++ {
		h.add(HistorySample{CPU: float64(i), Mem: float64(i * 10)})
	}

	got := h.snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, attendu 3", len(got))
	}
	want := []float64{3, 4, 5}
	for i, w := range want {
		if got[i].CPU != w {
			t.Errorf("got[%d].CPU = %v, attendu %v", i, got[i].CPU, w)
		}
		if got[i].Mem != w*10 {
			t.Errorf("got[%d].Mem = %v, attendu %v", i, got[i].Mem, w*10)
		}
	}
}
