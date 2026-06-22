// Package sysinfo collecte les métriques système (CPU, mémoire, disque, hôte).
package sysinfo

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

const giga = 1000 * 1000 * 1000

// cpuSampleInterval est la fenêtre de mesure de l'utilisation CPU.
const cpuSampleInterval = 500 * time.Millisecond

// netSampleInterval est l'intervalle entre deux relevés des compteurs réseau,
// servant au calcul du débit instantané.
const netSampleInterval = time.Second

// Paramètres de l'historique conservé côté serveur (anneau circulaire) :
// un point toutes les historyInterval, sur historySize points glissants.
const (
	historySize     = 120         // ≈ 2 min de profondeur à 1 point / s
	historyInterval = time.Second // résolution d'un point d'historique
)

// Info regroupe l'ensemble des informations système renvoyées par l'API.
type Info struct {
	Timestamp time.Time `json:"timestamp"`
	Host      Host      `json:"host"`
	CPU       CPU       `json:"cpu"`
	Load      Load      `json:"load"`
	Memory    Memory    `json:"memory"`
	Disk      Disk      `json:"disk"`
	Net       Net       `json:"net"`
}

// Host décrit la machine hôte.
type Host struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Platform      string `json:"platform"`
	KernelArch    string `json:"kernel_arch"`
	UptimeSeconds uint64 `json:"uptime_seconds"`
	GoVersion     string `json:"go_version"`
}

// CPU décrit l'utilisation du processeur.
type CPU struct {
	UsedPercent float64 `json:"used_percent"`
	Cores       int     `json:"cores"`
	ModelName   string  `json:"model_name"`
}

// Load décrit la charge système moyenne sur 1, 5 et 15 minutes.
type Load struct {
	One     float64 `json:"load1"`
	Five    float64 `json:"load5"`
	Fifteen float64 `json:"load15"`
}

// Net décrit le débit réseau instantané, agrégé sur toutes les interfaces.
type Net struct {
	RecvBytesPerSec float64 `json:"recv_bytes_per_sec"`
	SentBytesPerSec float64 `json:"sent_bytes_per_sec"`
}

// Memory décrit l'utilisation de la mémoire vive.
type Memory struct {
	UsedPercent float64 `json:"used_percent"`
	UsedGB      float64 `json:"used_gb"`
	FreeGB      float64 `json:"free_gb"`
	TotalGB     float64 `json:"total_gb"`
}

// Disk décrit l'utilisation d'une partition.
type Disk struct {
	UsedPercent float64 `json:"used_percent"`
	UsedGB      float64 `json:"used_gb"`
	TotalGB     float64 `json:"total_gb"`
	Path        string  `json:"path"`
}

// HistorySample est un point d'historique : utilisation CPU et mémoire (en
// pourcentage) relevée à un instant donné.
type HistorySample struct {
	CPU float64 `json:"cpu"`
	Mem float64 `json:"mem"`
}

// Collect récupère les métriques système courantes en mesurant le CPU de
// façon synchrone (appel bloquant pendant cpuSampleInterval). Pratique pour
// un relevé ponctuel ; un serveur lui préférera un Collector mis en cache.
func Collect() (*Info, error) {
	percent, err := cpu.Percent(cpuSampleInterval, false)
	if err != nil {
		return nil, err
	}
	var used float64
	if len(percent) > 0 {
		used = percent[0]
	}
	// Un relevé ponctuel ne dispose pas d'historique réseau : le débit
	// (qui se calcule sur deux mesures espacées) est laissé à zéro.
	return collect(used, Net{})
}

// collect assemble un Info à partir de mesures déjà disponibles (utilisation
// CPU et débit réseau), sans aucun appel bloquant.
func collect(cpuUsed float64, netRate Net) (*Info, error) {
	info := &Info{Timestamp: time.Now(), Net: netRate}

	if err := info.collectHost(); err != nil {
		return nil, err
	}
	info.collectCPU(cpuUsed)
	info.collectLoad()
	if err := info.collectMemory(); err != nil {
		return nil, err
	}
	if err := info.collectDisk("/"); err != nil {
		return nil, err
	}

	return info, nil
}

// Collector fournit les métriques système sans bloquer les requêtes : une
// goroutine échantillonne l'utilisation CPU en arrière-plan et la met en
// cache, de sorte que Collect renvoie instantanément la dernière mesure.
type Collector struct {
	cpu     cpuSampler
	net     netSampler
	history *history
}

// NewCollector construit un collecteur prêt à l'emploi.
func NewCollector() *Collector {
	return &Collector{history: newHistory(historySize)}
}

// Start lance, en arrière-plan et jusqu'à l'annulation de ctx, l'échantillonnage
// CPU et l'enregistrement de l'historique. À appeler une seule fois avant de
// servir des requêtes.
func (c *Collector) Start(ctx context.Context) {
	go c.cpu.run(ctx)
	go c.net.run(ctx)
	go c.recordHistory(ctx)
}

// Collect renvoie les métriques courantes en réutilisant les dernières mesures
// CPU et réseau mises en cache (aucun appel bloquant).
func (c *Collector) Collect() (*Info, error) {
	return collect(c.cpu.get(), c.net.get())
}

// History renvoie une copie ordonnée (du plus ancien au plus récent) des
// points d'historique conservés.
func (c *Collector) History() []HistorySample {
	return c.history.snapshot()
}

// recordHistory ajoute un point d'historique (CPU mis en cache + mémoire) à
// intervalle régulier jusqu'à l'annulation de ctx.
func (c *Collector) recordHistory(ctx context.Context) {
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var memPct float64
			if vm, err := mem.VirtualMemory(); err == nil {
				memPct = vm.UsedPercent
			}
			c.history.add(HistorySample{CPU: c.cpu.get(), Mem: memPct})
		}
	}
}

// history est un anneau circulaire thread-safe des derniers HistorySample.
type history struct {
	mu     sync.RWMutex
	buf    []HistorySample
	start  int // index du plus ancien échantillon
	length int // nombre d'échantillons valides
}

func newHistory(n int) *history {
	return &history{buf: make([]HistorySample, n)}
}

// add insère un échantillon, en écrasant le plus ancien lorsque l'anneau est
// plein.
func (h *history) add(s HistorySample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.length < len(h.buf) {
		h.buf[(h.start+h.length)%len(h.buf)] = s
		h.length++
		return
	}
	h.buf[h.start] = s
	h.start = (h.start + 1) % len(h.buf)
}

// snapshot renvoie les échantillons du plus ancien au plus récent.
func (h *history) snapshot() []HistorySample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]HistorySample, h.length)
	for i := 0; i < h.length; i++ {
		out[i] = h.buf[(h.start+i)%len(h.buf)]
	}
	return out
}

// cpuSampler maintient la dernière utilisation CPU connue, protégée par un
// mutex pour un accès concurrent sûr.
type cpuSampler struct {
	mu      sync.RWMutex
	percent float64
}

func (s *cpuSampler) get() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.percent
}

func (s *cpuSampler) set(p float64) {
	s.mu.Lock()
	s.percent = p
	s.mu.Unlock()
}

// run mesure l'utilisation CPU sur des fenêtres successives de
// cpuSampleInterval et met le résultat en cache, jusqu'à l'annulation de ctx.
// La mesure est volontairement bloquante : dans cette goroutine dédiée, elle
// n'affecte pas la latence des requêtes (qui lisent le cache) et, sur une
// fenêtre fixe et bien définie, elle évite les zéros parasites que renvoie
// l'appel non bloquant cpu.Percent(0, …) sur certaines plateformes (macOS
// notamment), où le compteur de ticks agrégé n'avance pas toujours entre deux
// lectures rapprochées.
func (s *cpuSampler) run(ctx context.Context) {
	for ctx.Err() == nil {
		p, err := cpu.Percent(cpuSampleInterval, false)
		if err != nil {
			// En cas d'erreur, cpu.Percent peut revenir sans temporiser :
			// on attend l'intervalle pour éviter une boucle active.
			select {
			case <-ctx.Done():
			case <-time.After(cpuSampleInterval):
			}
			continue
		}
		if len(p) > 0 {
			s.set(p[0])
		}
	}
}

// netSampler maintient le dernier débit réseau connu, calculé en différentiant
// les compteurs cumulés d'octets entre deux relevés espacés.
type netSampler struct {
	mu   sync.RWMutex
	rate Net
}

func (s *netSampler) get() Net {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rate
}

func (s *netSampler) set(r Net) {
	s.mu.Lock()
	s.rate = r
	s.mu.Unlock()
}

// run relève les compteurs réseau à intervalle régulier et met en cache le
// débit instantané jusqu'à l'annulation de ctx.
func (s *netSampler) run(ctx context.Context) {
	prev, ok := readNetTotals()

	ticker := time.NewTicker(netSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, valid := readNetTotals()
			if ok && valid {
				s.set(netRate(prev, cur))
			}
			if valid {
				prev, ok = cur, true
			}
		}
	}
}

// netTotals est un relevé instantané des compteurs réseau cumulés.
type netTotals struct {
	recv, sent uint64
	at         time.Time
}

// readNetTotals lit les compteurs réseau agrégés (toutes interfaces).
func readNetTotals() (netTotals, bool) {
	counters, err := net.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return netTotals{}, false
	}
	return netTotals{recv: counters[0].BytesRecv, sent: counters[0].BytesSent, at: time.Now()}, true
}

// netRate calcule le débit (octets/s) entre deux relevés.
func netRate(prev, cur netTotals) Net {
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return Net{}
	}
	return Net{
		RecvBytesPerSec: perSec(prev.recv, cur.recv, elapsed),
		SentBytesPerSec: perSec(prev.sent, cur.sent, elapsed),
	}
}

// perSec renvoie le débit pour un compteur cumulé, en se prémunissant d'une
// réinitialisation du compteur (redémarrage d'interface, dépassement) qui
// donnerait une valeur négative.
func perSec(prev, cur uint64, elapsed float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / elapsed
}

func (i *Info) collectHost() error {
	h, err := host.Info()
	if err != nil {
		return err
	}
	i.Host = Host{
		Hostname:      h.Hostname,
		OS:            h.OS,
		Platform:      h.Platform,
		KernelArch:    h.KernelArch,
		UptimeSeconds: h.Uptime,
		GoVersion:     runtime.Version(),
	}
	return nil
}

func (i *Info) collectCPU(usedPercent float64) {
	i.CPU.UsedPercent = usedPercent
	i.CPU.Cores = runtime.NumCPU()
	// Le modèle du CPU est optionnel : on ignore l'erreur sans échouer.
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		i.CPU.ModelName = infos[0].ModelName
	}
}

func (i *Info) collectLoad() {
	// La charge moyenne est optionnelle (indisponible sur certaines
	// plateformes, ex. Windows) : on ignore l'erreur sans échouer.
	if avg, err := load.Avg(); err == nil {
		i.Load = Load{One: avg.Load1, Five: avg.Load5, Fifteen: avg.Load15}
	}
}

func (i *Info) collectMemory() error {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return err
	}
	i.Memory = Memory{
		UsedPercent: vm.UsedPercent,
		UsedGB:      float64(vm.Used) / giga,
		FreeGB:      float64(vm.Total-vm.Used) / giga,
		TotalGB:     float64(vm.Total) / giga,
	}
	return nil
}

func (i *Info) collectDisk(path string) error {
	usage, err := disk.Usage(path)
	if err != nil {
		return err
	}
	i.Disk = Disk{
		UsedPercent: usage.UsedPercent,
		UsedGB:      float64(usage.Used) / giga,
		TotalGB:     float64(usage.Total) / giga,
		Path:        path,
	}
	return nil
}
