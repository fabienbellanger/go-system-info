// Package sysinfo collecte les métriques système (CPU, mémoire, disque, hôte).
package sysinfo

import (
	"runtime"
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

// Collect récupère les métriques système courantes.
func Collect() (*Info, error) {
	info := &Info{Timestamp: time.Now()}

	if err := info.collectHost(); err != nil {
		return nil, err
	}
	if err := info.collectCPU(); err != nil {
		return nil, err
	}
	if err := info.collectMemory(); err != nil {
		return nil, err
	}
	if err := info.collectDisk("/"); err != nil {
		return nil, err
	}

	return info, nil
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

func (i *Info) collectCPU() error {
	percent, err := cpu.Percent(cpuSampleInterval, false)
	if err != nil {
		return err
	}
	i.CPU.Cores = runtime.NumCPU()
	if len(percent) > 0 {
		i.CPU.UsedPercent = percent[0]
	}
	// Le modèle du CPU est optionnel : on ignore l'erreur sans échouer.
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		i.CPU.ModelName = infos[0].ModelName
	}
	return nil
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
