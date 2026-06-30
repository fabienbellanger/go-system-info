package sysinfo

import (
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func TestCollect(t *testing.T) {
	info, err := Collect()
	if err != nil {
		t.Fatalf("Collect() a renvoyé une erreur : %v", err)
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

// BenchmarkCollect mesure l'assemblage d'un Info à partir de valeurs déjà
// échantillonnées — c'est le coût réel d'une requête GET /api/system servie
// par le Collector (lectures hôte/CPU/mémoire/disque, sans mesure CPU bloquante).
func BenchmarkCollect(b *testing.B) {
	for b.Loop() {
		if _, err := collect(0, Net{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHistorySnapshot mesure la copie de l'historique, effectuée à chaque
// événement SSE et requête GET /api/history.
func BenchmarkHistorySnapshot(b *testing.B) {
	h := newHistory(historySize)
	for i := range historySize {
		h.add(HistorySample{CPU: float64(i), Mem: float64(i)})
	}
	b.ResetTimer()
	for b.Loop() {
		_ = h.snapshot()
	}
}

func TestCPUBusyPercent(t *testing.T) {
	t.Run("relevé nominal", func(t *testing.T) {
		// total : 1000 → 2000 (delta 1000) ; occupé : 150 → 400 (delta 250) → 25 %.
		prev := cpu.TimesStat{User: 100, System: 50, Idle: 850}
		cur := cpu.TimesStat{User: 300, System: 100, Idle: 1600}
		pct, moved := cpuBusyPercent(prev, cur)
		if !moved {
			t.Fatal("attendu moved=true pour des compteurs qui progressent")
		}
		if pct != 25 {
			t.Errorf("pct = %v, attendu 25", pct)
		}
	})

	t.Run("compteurs figés (relevé fantôme)", func(t *testing.T) {
		// Deux lectures identiques : le total ne progresse pas. On doit signaler
		// l'absence de mesure (moved=false) plutôt que de renvoyer un 0 % faux.
		stat := cpu.TimesStat{User: 100, System: 50, Idle: 850}
		if _, moved := cpuBusyPercent(stat, stat); moved {
			t.Error("attendu moved=false pour des compteurs figés")
		}
	})

	t.Run("repos réel", func(t *testing.T) {
		// Le total progresse mais quasiment que de l'inactivité → ~0 %, valide.
		prev := cpu.TimesStat{User: 100, System: 50, Idle: 850}
		cur := cpu.TimesStat{User: 100, System: 50, Idle: 1850}
		pct, moved := cpuBusyPercent(prev, cur)
		if !moved {
			t.Fatal("attendu moved=true : les compteurs progressent")
		}
		if pct != 0 {
			t.Errorf("pct = %v, attendu 0", pct)
		}
	})
}

func TestCPUSamplerSmoothing(t *testing.T) {
	t.Run("le premier relevé initialise sans lisser", func(t *testing.T) {
		var s cpuSampler
		s.set(40)
		if s.get() != 40 {
			t.Errorf("get() = %v, attendu 40 (relevé initial non lissé)", s.get())
		}
	})

	t.Run("les relevés suivants sont lissés par EMA", func(t *testing.T) {
		var s cpuSampler
		s.set(10) // amorce
		s.set(20) // 10 + 0,25*(20-10) = 12,5
		if got := s.get(); got != 12.5 {
			t.Errorf("get() = %v, attendu 12.5", got)
		}
		s.set(20) // 12,5 + 0,25*(20-12,5) = 14,375
		if got := s.get(); got != 14.375 {
			t.Errorf("get() = %v, attendu 14.375", got)
		}
	})

	t.Run("un pic isolé ne déplace la valeur que partiellement", func(t *testing.T) {
		var s cpuSampler
		s.set(10)
		s.set(90) // un seul relevé extrême ne doit pas faire bondir la jauge
		if got := s.get(); got >= 90 || got <= 10 {
			t.Errorf("get() = %v, attendu une valeur lissée strictement entre 10 et 90", got)
		}
	})
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
		if got.RecvTotalBytes != 3000 || got.SentTotalBytes != 1500 {
			t.Errorf("totaux = %d/%d, attendus 3000/1500", got.RecvTotalBytes, got.SentTotalBytes)
		}
	})

	t.Run("durée nulle", func(t *testing.T) {
		prev := netTotals{recv: 1000, sent: 500, at: base}
		cur := netTotals{recv: 3000, sent: 1500, at: base}
		got := netRate(prev, cur)
		if got.RecvBytesPerSec != 0 || got.SentBytesPerSec != 0 {
			t.Errorf("attendu débit nul pour une durée nulle, obtenu %+v", got)
		}
		// Les totaux restent reportés même sans intervalle exploitable.
		if got.RecvTotalBytes != 3000 || got.SentTotalBytes != 1500 {
			t.Errorf("totaux = %d/%d, attendus 3000/1500", got.RecvTotalBytes, got.SentTotalBytes)
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

func TestAggregateProcesses(t *testing.T) {
	const totalMem = 1000 // octets, pour des pourcentages mémoire faciles à lire
	const numCPU = 8      // cœurs simulés, pour le % machine (CPUPercent / numCPU)
	const me = "moi"      // utilisateur courant simulé

	t.Run("regroupement par arbre (application) et CPU par cœur", func(t *testing.T) {
		// Arbre Helium : appli (ppid=1) ← helper ← renderer. Tout le sous-arbre est
		// sommé sous le nom de la racine. CPU = (5+3+0)/1*100 = 800 % d'un cœur.
		prev := []procSample{
			{pid: 100, ppid: 1, name: "Helium", user: me, cpuTime: 10, rss: 120},
			{pid: 101, ppid: 100, name: "Helium Helper", user: me, cpuTime: 20, rss: 220},
			{pid: 102, ppid: 101, name: "Helium Helper (Renderer)", user: me, cpuTime: 5, rss: 60},
		}
		cur := []procSample{
			{pid: 100, ppid: 1, name: "Helium", user: me, cpuTime: 15, rss: 120},                   // Δ5
			{pid: 101, ppid: 100, name: "Helium Helper", user: me, cpuTime: 23, rss: 220},          // Δ3
			{pid: 102, ppid: 101, name: "Helium Helper (Renderer)", user: me, cpuTime: 5, rss: 60}, // Δ0
		}
		got := aggregateProcesses(prev, cur, 1, totalMem, numCPU, me)

		h := findProc(t, got.TopCPU, "Helium")
		if h.Count != 3 {
			t.Errorf("Helium.Count = %d, attendu 3 (tout le sous-arbre)", h.Count)
		}
		if h.CPUPercent != 800 {
			t.Errorf("Helium.CPUPercent = %v, attendu 800", h.CPUPercent)
		}
		if h.CPUPercentSystem != 100 { // 800 % cœur / 8 cœurs = 100 % machine
			t.Errorf("Helium.CPUPercentSystem = %v, attendu 100", h.CPUPercentSystem)
		}
		if h.MemBytes != 400 {
			t.Errorf("Helium.MemBytes = %d, attendu 400", h.MemBytes)
		}
		if h.MemPercent != 40 {
			t.Errorf("Helium.MemPercent = %v, attendu 40", h.MemPercent)
		}
		if len(h.PIDs) != 3 {
			t.Errorf("Helium.PIDs = %v, attendu 3 PID", h.PIDs)
		}
		if !h.Killable {
			t.Error("Helium devrait être killable (tout le sous-arbre à l'utilisateur courant)")
		}
	})

	t.Run("racine = enfant de launchd ; un enfant rejoint l'arbre de sa racine", func(t *testing.T) {
		// Un « node » autonome (racine) et un « node » enfant de « zed » : le second
		// doit compter dans l'arbre « zed », pas dans le « node » autonome.
		cur := []procSample{
			{pid: 200, ppid: 1, name: "node", user: me, rss: 10},
			{pid: 201, ppid: 1, name: "zed", user: me, rss: 10},
			{pid: 202, ppid: 201, name: "node", user: me, rss: 10}, // enfant de zed
		}
		got := aggregateProcesses(nil, cur, 1, totalMem, numCPU, me)

		node := findProc(t, got.TopMem, "node")
		if node.Count != 1 {
			t.Errorf("node.Count = %d, attendu 1 (le node enfant compte dans zed)", node.Count)
		}
		zed := findProc(t, got.TopMem, "zed")
		if zed.Count != 2 {
			t.Errorf("zed.Count = %d, attendu 2 (zed + son node enfant)", zed.Count)
		}
	})

	t.Run("orphelin : parent absent → racine propre", func(t *testing.T) {
		cur := []procSample{{pid: 300, ppid: 999, name: "orphan", user: me, rss: 10}}
		got := aggregateProcesses(nil, cur, 1, totalMem, numCPU, me)
		findProc(t, got.TopMem, "orphan") // échoue si absent
	})

	t.Run("killable : un enfant d'un autre utilisateur rend l'arbre non killable", func(t *testing.T) {
		cur := []procSample{
			{pid: 400, ppid: 1, name: "app", user: me, rss: 10},
			{pid: 401, ppid: 400, name: "helper", user: "autre", rss: 10},
		}
		got := aggregateProcesses(nil, cur, 1, totalMem, numCPU, me)
		app := findProc(t, got.TopMem, "app")
		if app.User != me {
			t.Errorf("app.User = %q, attendu %q (propriétaire de la racine)", app.User, me)
		}
		if app.Killable {
			t.Error("app ne devrait pas être killable (un enfant appartient à un autre utilisateur)")
		}
	})

	t.Run("CPU par cœur : une seconde sur une seconde = 100 %", func(t *testing.T) {
		prev := []procSample{{pid: 1, ppid: 1, name: "worker", cpuTime: 0, rss: 10}}
		cur := []procSample{{pid: 1, ppid: 1, name: "worker", cpuTime: 1, rss: 10}}
		got := aggregateProcesses(prev, cur, 1, totalMem, numCPU, me)
		w := findProc(t, got.TopCPU, "worker")
		if w.CPUPercent != 100 {
			t.Errorf("worker.CPUPercent = %v, attendu 100", w.CPUPercent)
		}
	})

	t.Run("process neuf absent de prev → CPU 0", func(t *testing.T) {
		cur := []procSample{{pid: 99, ppid: 1, name: "neuf", cpuTime: 42, rss: 10}}
		got := aggregateProcesses(nil, cur, 1, totalMem, numCPU, me)
		p := findProc(t, got.TopCPU, "neuf")
		if p.CPUPercent != 0 {
			t.Errorf("CPUPercent = %v, attendu 0 pour un process neuf", p.CPUPercent)
		}
	})

	t.Run("delta CPU négatif (PID recyclé) → 0", func(t *testing.T) {
		prev := []procSample{{pid: 1, ppid: 1, name: "recycle", cpuTime: 100, rss: 10}}
		cur := []procSample{{pid: 1, ppid: 1, name: "recycle", cpuTime: 5, rss: 10}}
		got := aggregateProcesses(prev, cur, 1, totalMem, numCPU, me)
		p := findProc(t, got.TopCPU, "recycle")
		if p.CPUPercent != 0 {
			t.Errorf("CPUPercent = %v, attendu 0 pour un delta négatif", p.CPUPercent)
		}
	})

	t.Run("troncature à procTopN et tri des deux listes", func(t *testing.T) {
		// 15 racines distinctes (chacune enfant de launchd) : CPU croissant avec
		// l'indice, RSS décroissant.
		var cur []procSample
		for i := range 15 {
			cur = append(cur, procSample{
				pid:     int32(i + 10),
				ppid:    1,
				name:    procName(i),
				cpuTime: float64(i), // delta vs prev (cpuTime 0) = i
				rss:     uint64(100 - i),
			})
		}
		prev := make([]procSample, len(cur))
		copy(prev, cur)
		for i := range prev {
			prev[i].cpuTime = 0 // delta = cur.cpuTime
		}
		got := aggregateProcesses(prev, cur, 1, totalMem, numCPU, me)

		if len(got.TopCPU) != procTopN || len(got.TopMem) != procTopN {
			t.Fatalf("tailles = %d/%d, attendu %d/%d",
				len(got.TopCPU), len(got.TopMem), procTopN, procTopN)
		}
		// TopCPU : tri décroissant → l'indice 14 (plus gros CPU) en tête.
		if got.TopCPU[0].Name != procName(14) {
			t.Errorf("TopCPU[0] = %s, attendu %s", got.TopCPU[0].Name, procName(14))
		}
		for i := 1; i < len(got.TopCPU); i++ {
			if got.TopCPU[i-1].CPUPercent < got.TopCPU[i].CPUPercent {
				t.Errorf("TopCPU non trié décroissant à l'indice %d", i)
			}
		}
		// TopMem : RSS le plus élevé = indice 0 (rss 100).
		if got.TopMem[0].Name != procName(0) {
			t.Errorf("TopMem[0] = %s, attendu %s", got.TopMem[0].Name, procName(0))
		}
		for i := 1; i < len(got.TopMem); i++ {
			if got.TopMem[i-1].MemBytes < got.TopMem[i].MemBytes {
				t.Errorf("TopMem non trié décroissant à l'indice %d", i)
			}
		}
	})
}

// findProc retrouve un process par nom dans une liste, ou échoue le test.
func findProc(t *testing.T, list []ProcessInfo, name string) ProcessInfo {
	t.Helper()
	for _, p := range list {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("process %q absent de la liste", name)
	return ProcessInfo{}
}

// procName génère un nom de process déterministe pour les tests.
func procName(i int) string {
	return "proc" + string(rune('a'+i))
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
