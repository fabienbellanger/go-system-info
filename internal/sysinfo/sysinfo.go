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
	"github.com/shirou/gopsutil/v4/mem"
)

const giga = 1000 * 1000 * 1000

// cpuSampleInterval est la fenêtre de mesure de l'utilisation CPU.
const cpuSampleInterval = 500 * time.Millisecond

// Info regroupe l'ensemble des informations système renvoyées par l'API.
type Info struct {
	Timestamp time.Time `json:"timestamp"`
	Host      Host      `json:"host"`
	CPU       CPU       `json:"cpu"`
	Memory    Memory    `json:"memory"`
	Disk      Disk      `json:"disk"`
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
	return collect(used)
}

// collect assemble un Info à partir d'une mesure d'utilisation CPU déjà
// disponible, sans aucun appel bloquant.
func collect(cpuUsed float64) (*Info, error) {
	info := &Info{Timestamp: time.Now()}

	if err := info.collectHost(); err != nil {
		return nil, err
	}
	info.collectCPU(cpuUsed)
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
	cpu cpuSampler
}

// NewCollector construit un collecteur prêt à l'emploi.
func NewCollector() *Collector {
	return &Collector{}
}

// Start lance l'échantillonnage CPU en arrière-plan jusqu'à l'annulation de
// ctx. À appeler une seule fois avant de servir des requêtes.
func (c *Collector) Start(ctx context.Context) {
	go c.cpu.run(ctx)
}

// Collect renvoie les métriques courantes en réutilisant la dernière mesure
// CPU mise en cache (aucun appel bloquant).
func (c *Collector) Collect() (*Info, error) {
	return collect(c.cpu.get())
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

// run échantillonne le CPU à intervalle régulier jusqu'à l'annulation de ctx.
// cpu.Percent(0, …) est non bloquant : il renvoie l'utilisation depuis l'appel
// précédent, d'où l'appel initial qui sert de référence.
func (s *cpuSampler) run(ctx context.Context) {
	_, _ = cpu.Percent(0, false)

	ticker := time.NewTicker(cpuSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
				s.set(p[0])
			}
		}
	}
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
