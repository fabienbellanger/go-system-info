// Package sysinfo collecte les métriques système (CPU, mémoire, disque, hôte).
package sysinfo

import (
	"context"
	"fmt"
	"os/user"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"
)

const giga = 1000 * 1000 * 1000

// cpuSampleInterval est la fenêtre de mesure de l'utilisation CPU.
const cpuSampleInterval = 500 * time.Millisecond

// netSampleInterval est l'intervalle entre deux relevés des compteurs réseau,
// servant au calcul du débit instantané.
const netSampleInterval = time.Second

// diskIOSampleInterval est l'intervalle entre deux relevés des compteurs d'E/S
// disque (même cadence que le réseau : un débit doit rester frais).
const diskIOSampleInterval = time.Second

// procSampleInterval est l'intervalle entre deux énumérations complètes des
// processus. Plus espacé que les autres relevés : parcourir tous les processus
// est coûteux, et une carte « consommateurs » n'a pas besoin de plus de fraîcheur.
const procSampleInterval = 3 * time.Second

// diskSampleInterval espace l'énumération de tous les volumes montés (pour le
// sélecteur de disque de l'interface). L'occupation d'un disque évolue lentement
// et énumérer les partitions puis relever chaque usage est coûteux.
const diskSampleInterval = 5 * time.Second

// tempSampleInterval espace la lecture des capteurs de température. Elle évolue
// lentement ; inutile de solliciter les capteurs à chaque tick.
const tempSampleInterval = 5 * time.Second

// minVolumeBytes filtre les volumes trop petits du sélecteur de disque (tranches
// système de quelques Mo sur macOS, partitions techniques…) : seuls les volumes
// d'au moins cette taille sont proposés, sauf le volume surveillé par défaut, qui
// est toujours présent quelle que soit sa taille.
const minVolumeBytes = giga // 1 Go

// pseudoFstypes recense les systèmes de fichiers virtuels (sans espace de
// stockage réel) à exclure du sélecteur de volumes.
var pseudoFstypes = map[string]bool{
	"devfs": true, "autofs": true, "tmpfs": true, "devtmpfs": true,
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true,
	"overlay": true, "squashfs": true, "mqueue": true, "debugfs": true,
	"tracefs": true, "fusectl": true, "configfs": true, "securityfs": true,
	"pstore": true, "bpf": true, "nsfs": true, "ramfs": true, "binfmt_misc": true,
}

// procTopN borne le nombre de processus exposés dans chaque classement.
const procTopN = 10

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
	// Disks liste tous les volumes montés significatifs (pour le sélecteur de
	// l'interface) ; renseigné par le Collector, absent d'un relevé ponctuel.
	Disks  []Disk `json:"disks,omitempty"`
	DiskIO DiskIO `json:"disk_io"`
	Net    Net    `json:"net"`
	// Processes est renseigné par le Collector mis en cache ; un relevé ponctuel
	// (fonction Collect libre) le laisse nil, auquel cas le champ est omis du JSON.
	Processes *Processes `json:"processes,omitempty"`
}

// ProcessInfo décrit un programme regroupant une ou plusieurs instances de même
// nom : ses consommations CPU et mémoire y sont sommées.
type ProcessInfo struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`       // nombre d'instances fusionnées
	User       string  `json:"user"`        // propriétaire de la racine du groupe (cf. Killable pour l'homogénéité du sous-arbre)
	CPUPercent float64 `json:"cpu_percent"` // % d'un cœur (style top/htop) ; peut dépasser 100 %
	// CPUPercentSystem est la même charge rapportée à la machine entière (CPUPercent
	// / nombre de cœurs) : sur la même base que la jauge CPU globale (0–100 %), la
	// somme des processus s'en approche.
	CPUPercentSystem float64 `json:"cpu_percent_system"`
	MemPercent       float64 `json:"mem_percent"` // RSS cumulé / mémoire totale
	MemBytes         uint64  `json:"mem_bytes"`   // RSS cumulé (octets)
	PIDs             []int32 `json:"pids"`        // PID des instances fusionnées (pour la terminaison)
	// Killable indique que toutes les instances appartiennent à l'utilisateur qui
	// a lancé le serveur : seuls ces processus peuvent être terminés via l'API.
	Killable bool `json:"killable"`
}

// Processes expose les deux classements des plus gros consommateurs. Les deux
// listes sont pré-calculées pour que l'interface puisse basculer instantanément
// entre les modes CPU et Mémoire sans rouvrir le flux SSE.
type Processes struct {
	TopCPU []ProcessInfo `json:"top_cpu"`
	TopMem []ProcessInfo `json:"top_mem"`
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
	// PerCore est l'occupation instantanée (0–100) de chaque cœur logique, pour la
	// grille par cœur de l'interface. Absent d'un relevé ponctuel (Collect libre).
	PerCore []float64 `json:"per_core,omitempty"`
	// TempCelsius est la température du capteur le plus chaud (best-effort) et
	// TempLabel son identifiant. Omis si aucun capteur n'est exploitable (fréquent
	// selon la plateforme et les droits).
	TempCelsius float64 `json:"temp_celsius,omitempty"`
	TempLabel   string  `json:"temp_label,omitempty"`
}

// Load décrit la charge système moyenne sur 1, 5 et 15 minutes.
type Load struct {
	One     float64 `json:"load1"`
	Five    float64 `json:"load5"`
	Fifteen float64 `json:"load15"`
}

// Net décrit l'activité réseau agrégée sur toutes les interfaces : débit
// instantané (octets/s) et volumes cumulés depuis le démarrage.
type Net struct {
	RecvBytesPerSec float64 `json:"recv_bytes_per_sec"`
	SentBytesPerSec float64 `json:"sent_bytes_per_sec"`
	RecvTotalBytes  uint64  `json:"recv_total_bytes"`
	SentTotalBytes  uint64  `json:"sent_total_bytes"`
}

// Memory décrit l'utilisation de la mémoire vive.
type Memory struct {
	UsedPercent float64 `json:"used_percent"`
	UsedGB      float64 `json:"used_gb"`
	FreeGB      float64 `json:"free_gb"`
	TotalGB     float64 `json:"total_gb"`
	// Swap (mémoire d'échange) : best-effort, peut être désactivé (total 0).
	SwapUsedPercent float64 `json:"swap_used_percent"`
	SwapUsedGB      float64 `json:"swap_used_gb"`
	SwapTotalGB     float64 `json:"swap_total_gb"`
}

// Disk décrit l'utilisation d'une partition.
type Disk struct {
	UsedPercent float64 `json:"used_percent"`
	UsedGB      float64 `json:"used_gb"`
	TotalGB     float64 `json:"total_gb"`
	Path        string  `json:"path"`
	Fstype      string  `json:"fstype,omitempty"` // système de fichiers (apfs, ext4…)
}

// DiskIO agrège le débit d'entrées/sorties disque (toutes unités confondues),
// calculé en différentiant les compteurs cumulés — comme le réseau.
type DiskIO struct {
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
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
	if err := info.collectDisk(defaultDiskPath()); err != nil {
		return nil, err
	}

	return info, nil
}

// Collector fournit les métriques système sans bloquer les requêtes : une
// goroutine échantillonne l'utilisation CPU en arrière-plan et la met en
// cache, de sorte que Collect renvoie instantanément la dernière mesure.
type Collector struct {
	cpu         cpuSampler
	core        coreSampler // occupation par cœur (grille de l'interface)
	net         netSampler
	diskIO      diskIOSampler // débit d'E/S disque global (carte Disque)
	proc        procSampler
	disks       diskSampler // liste des volumes montés (sélecteur de disque)
	temp        tempSampler // température (capteur le plus chaud, best-effort)
	history     *history
	currentUser string // propriétaire du serveur, résolu une fois au démarrage
	diskPath    string // volume surveillé, résolu une fois au démarrage

	// Métadonnées hôte immuables, résolues une fois au démarrage. Les rappeler à
	// chaque relevé (host.Info/cpu.Info) coûterait des syscalls pour des valeurs
	// constantes ; seul l'uptime, qui évolue, est relu à chaque Collect.
	staticHost Host
	cpuModel   string
	cpuCores   int

	// lastGood conserve le dernier Info complet réussi : sur défaillance
	// transitoire d'un relevé (mémoire/disque), Collect le réutilise au lieu de
	// renvoyer une erreur — ce qui, côté flux SSE, romprait la connexion.
	lastGood atomic.Pointer[Info]
}

// NewCollector construit un collecteur prêt à l'emploi. diskPath choisit le
// volume surveillé ; vide, il retombe sur le défaut de l'OS (defaultDiskPath).
func NewCollector(diskPath string) *Collector {
	if diskPath == "" {
		diskPath = defaultDiskPath()
	}
	c := &Collector{
		history:     newHistory(historySize),
		currentUser: currentUsername(),
		diskPath:    diskPath,
		cpuCores:    runtime.NumCPU(),
		staticHost:  Host{GoVersion: runtime.Version()},
	}
	// Best-effort : en cas d'échec, les champs restent vides — comme le reste du
	// code tolère déjà l'indisponibilité du modèle CPU ou de la charge moyenne.
	if h, err := host.Info(); err == nil {
		c.staticHost.Hostname = h.Hostname
		c.staticHost.OS = h.OS
		c.staticHost.Platform = h.Platform
		c.staticHost.KernelArch = h.KernelArch
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		c.cpuModel = infos[0].ModelName
	}
	return c
}

// defaultDiskPath renvoie le volume surveillé par défaut selon l'OS : la racine
// Unix « / », ou « C:\ » sous Windows où « / » ne désigne pas une racine de
// volume valide.
func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

// Start lance, en arrière-plan et jusqu'à l'annulation de ctx, l'échantillonnage
// CPU et l'enregistrement de l'historique. À appeler une seule fois avant de
// servir des requêtes.
func (c *Collector) Start(ctx context.Context) {
	go c.cpu.run(ctx)
	go c.core.run(ctx)
	go c.net.run(ctx)
	go c.diskIO.run(ctx)
	go c.proc.run(ctx, c.currentUser)
	go c.disks.run(ctx, c.diskPath)
	go c.temp.run(ctx)
	go c.recordHistory(ctx)
}

// Collect renvoie les métriques courantes en réutilisant les mesures CPU, réseau
// et processus déjà échantillonnées en arrière-plan (aucun appel bloquant), et
// les métadonnées hôte mises en cache. Sur défaillance transitoire d'un relevé
// dynamique, il retombe sur le dernier état complet connu.
func (c *Collector) Collect() (*Info, error) {
	info, err := c.assemble()
	if err != nil {
		// On a déjà servi un état complet : on le réémet plutôt que d'échouer.
		// Son horodatage (plus ancien) reflète honnêtement la fraîcheur réelle.
		if last := c.lastGood.Load(); last != nil {
			return last, nil
		}
		return nil, err
	}
	c.lastGood.Store(info)
	return info, nil
}

// assemble construit l'état courant à partir des métadonnées mises en cache
// (hôte, modèle CPU) et des relevés dynamiques : CPU/réseau/processus déjà
// échantillonnés en tâche de fond, uptime/charge/mémoire/disque lus ici. Renvoie
// une erreur si un relevé essentiel (mémoire ou disque) échoue.
func (c *Collector) assemble() (*Info, error) {
	tempC, tempLabel := c.temp.get()
	info := &Info{
		Timestamp: time.Now(),
		Net:       c.net.get(),
		Host:      c.staticHost,
		CPU: CPU{
			UsedPercent: c.cpu.get(),
			Cores:       c.cpuCores,
			ModelName:   c.cpuModel,
			PerCore:     c.core.get(),
			TempCelsius: tempC,
			TempLabel:   tempLabel,
		},
	}
	// L'uptime est la seule donnée hôte qui évolue : relevé seul (plus léger que
	// host.Info) à chaque collecte.
	if up, err := host.Uptime(); err == nil {
		info.Host.UptimeSeconds = up
	}
	info.collectLoad()
	if err := info.collectMemory(); err != nil {
		return nil, err
	}
	if err := info.collectDisk(c.diskPath); err != nil {
		return nil, err
	}
	info.Disks = c.disks.get()
	info.DiskIO = c.diskIO.get()
	info.Processes = c.proc.get()
	return info, nil
}

// History renvoie une copie ordonnée (du plus ancien au plus récent) des
// points d'historique conservés.
func (c *Collector) History() []HistorySample {
	return c.history.snapshot()
}

// Kill termine (SIGTERM) le processus de PID donné, à condition qu'il appartienne
// à l'utilisateur ayant lancé le serveur. Toute autre cible est refusée.
func (c *Collector) Kill(pid int32) error {
	return killOwnedProcess(pid, c.currentUser)
}

// Details renvoie le détail courant des processus dont les PID sont fournis
// (instantané, relu à la demande), restreint aux processus de l'utilisateur ayant
// lancé le serveur. Délègue à processDetails.
func (c *Collector) Details(pids []int32) []ProcessDetail {
	return processDetails(pids, c.currentUser)
}

// ProcessDetail décrit une instance (un PID) d'un groupe de processus : parent,
// nom, ligne de commande, état, threads, date de démarrage et mémoire résidente.
type ProcessDetail struct {
	PID        int32  `json:"pid"`
	PPID       int32  `json:"ppid"` // parent, pour reconstruire l'arbre côté client
	User       string `json:"user"`
	Name       string `json:"name"`
	Cmdline    string `json:"cmdline"`
	Status     string `json:"status"`
	Threads    int32  `json:"threads"`
	CreateTime int64  `json:"create_time"` // epoch en millisecondes
	MemBytes   uint64 `json:"mem_bytes"`   // RSS
}

// processDetails relit à la demande le détail de chaque PID appartenant à
// currentUser. Les processus d'un autre utilisateur sont ignorés : leur détail —
// dont la ligne de commande, susceptible de contenir des secrets (mots de passe,
// jetons passés en argument) — ne doit pas fuiter, au même titre que la
// terminaison est réservée à ses propres processus. Si currentUser est inconnu,
// aucun détail n'est renvoyé. Les champs et processus inaccessibles (disparus)
// sont ignorés silencieusement.
func processDetails(pids []int32, currentUser string) []ProcessDetail {
	if currentUser == "" {
		return nil
	}
	details := make([]ProcessDetail, 0, len(pids))
	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue // processus disparu
		}
		// Filtre de propriété : on relève le propriétaire d'abord et on écarte tout
		// ce qui n'appartient pas à l'utilisateur du serveur avant d'exposer quoi
		// que ce soit.
		owner, err := p.Username()
		if err != nil || owner != currentUser {
			continue
		}
		d := ProcessDetail{PID: pid, User: owner}
		if pp, err := p.Ppid(); err == nil {
			d.PPID = pp
		}
		if n, err := p.Name(); err == nil {
			d.Name = n
		}
		if c, err := p.Cmdline(); err == nil {
			d.Cmdline = c
		}
		if st, err := p.Status(); err == nil {
			d.Status = strings.Join(st, ", ")
		}
		if t, err := p.NumThreads(); err == nil {
			d.Threads = t
		}
		if ct, err := p.CreateTime(); err == nil {
			d.CreateTime = ct
		}
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			d.MemBytes = mi.RSS
		}
		details = append(details, d)
	}
	return details
}

// currentUsername renvoie le nom de l'utilisateur courant, ou une chaîne vide si
// indéterminable (auquel cas aucune terminaison ne sera autorisée).
func currentUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// killOwnedProcess envoie SIGTERM au processus pid, après avoir vérifié qu'il
// appartient bien à currentUser. C'est le garde-fou central : le serveur ne peut
// terminer que les processus de son propre utilisateur.
func killOwnedProcess(pid int32, currentUser string) error {
	if currentUser == "" {
		return fmt.Errorf("utilisateur courant inconnu : terminaison refusée")
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("processus %d introuvable : %w", pid, err)
	}
	owner, err := p.Username()
	if err != nil {
		return fmt.Errorf("propriétaire du processus %d inconnu : %w", pid, err)
	}
	if owner != currentUser {
		return fmt.Errorf("le processus %d appartient à %q, terminaison refusée", pid, owner)
	}
	return p.Terminate()
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

// cpuSmoothing est le facteur de la moyenne mobile exponentielle appliquée à la
// jauge CPU. Le sampler relève toutes les cpuSampleInterval (500 ms) ; un relevé
// brut sur une fenêtre aussi courte est très bruité (l'occupation instantanée
// saute typiquement de 5 % à 20 % d'un relevé à l'autre), ce qui donne une jauge
// erratique tombant souvent sur un creux non représentatif. On lisse donc la
// valeur publiée : à 500 ms, α = 0,25 correspond à une constante de temps ≈ 2 s,
// proche de la fenêtre que montre `top` — la jauge suit la tendance réelle au
// lieu d'un instantané.
const cpuSmoothing = 0.25

// cpuSampler maintient l'utilisation CPU lissée la plus récente, protégée par un
// mutex pour un accès concurrent sûr.
type cpuSampler struct {
	mu      sync.RWMutex
	percent float64
	seeded  bool // le premier relevé initialise la moyenne sans la lisser
}

func (s *cpuSampler) get() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.percent
}

// set intègre un nouveau relevé brut dans la moyenne mobile exponentielle. Le
// tout premier relevé l'initialise directement, pour ne pas démarrer la jauge à
// 0 % et converger lentement.
func (s *cpuSampler) set(p float64) {
	s.mu.Lock()
	if s.seeded {
		s.percent += cpuSmoothing * (p - s.percent)
	} else {
		s.percent = p
		s.seeded = true
	}
	s.mu.Unlock()
}

// run échantillonne l'utilisation CPU à intervalle régulier et met le résultat
// en cache, jusqu'à l'annulation de ctx. Plutôt que cpu.Percent — qui renvoie
// un 0 % indiscernable entre un CPU réellement au repos et un relevé fantôme —,
// on différencie nous-mêmes les temps CPU cumulés : si les compteurs n'ont pas
// progressé entre deux lectures (cas fréquent sur macOS, où host_statistics
// renvoie parfois des ticks identiques), on conserve la dernière valeur connue
// au lieu de publier un 0 % parasite alors que la machine est en réalité chargée.
func (s *cpuSampler) run(ctx context.Context) {
	prev, ok := cpuTimes()

	ticker := time.NewTicker(cpuSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, valid := cpuTimes()
			if !valid {
				continue
			}
			if !ok {
				prev, ok = cur, true
				continue
			}
			if pct, moved := cpuBusyPercent(prev, cur); moved {
				s.set(pct)
				prev = cur
			}
			// Compteurs immobiles (relevé fantôme) : on garde la dernière
			// valeur et on ne déplace pas `prev`, pour le comparer au prochain
			// relevé qui, lui, aura progressé.
		}
	}
}

// cpuTimes lit les temps CPU cumulés agrégés sur l'ensemble des cœurs.
func cpuTimes() (cpu.TimesStat, bool) {
	t, err := cpu.Times(false)
	if err != nil || len(t) == 0 {
		return cpu.TimesStat{}, false
	}
	return t[0], true
}

// cpuBusyPercent calcule le taux d'occupation CPU (0–100) entre deux relevés de
// temps cumulés. Le booléen vaut false lorsque le temps total n'a pas progressé :
// le relevé est alors un fantôme (compteurs figés) et doit être ignoré plutôt
// que de produire un 0 % trompeur.
func cpuBusyPercent(prev, cur cpu.TimesStat) (float64, bool) {
	prevAll, prevBusy := cpuAllBusy(prev)
	curAll, curBusy := cpuAllBusy(cur)
	totalDelta := curAll - prevAll
	if totalDelta <= 0 {
		return 0, false
	}
	pct := (curBusy - prevBusy) / totalDelta * 100
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	return pct, true
}

// cpuAllBusy renvoie le temps CPU total et le temps « occupé » (hors
// inactivité) d'un relevé, selon le même découpage que gopsutil.
func cpuAllBusy(t cpu.TimesStat) (all, busy float64) {
	all = t.User + t.System + t.Nice + t.Idle + t.Iowait + t.Irq +
		t.Softirq + t.Steal + t.Guest + t.GuestNice
	if runtime.GOOS == "linux" {
		// Sous Linux, User inclut déjà Guest et Nice inclut GuestNice : on les
		// retire du total pour ne pas les compter deux fois.
		all -= t.Guest + t.GuestNice
	}
	busy = all - t.Idle - t.Iowait
	return all, busy
}

// coreSampler maintient l'occupation instantanée (0–100) de chaque cœur logique,
// pour la grille par cœur de l'interface. Sampler distinct du cpuSampler global :
// ce dernier porte un correctif macOS délicat (lissage EMA, gestion des relevés
// fantômes) qu'on ne veut pas altérer. Ici, pas de lissage — une grille de barres
// n'en a pas besoin — mais on conserve la dernière valeur d'un cœur dont les
// compteurs n'ont pas bougé (même logique « fantôme » que cpuBusyPercent).
type coreSampler struct {
	mu      sync.RWMutex
	percent []float64
}

func (s *coreSampler) get() []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.percent == nil {
		return nil
	}
	out := make([]float64, len(s.percent))
	copy(out, s.percent)
	return out
}

func (s *coreSampler) set(v []float64) {
	s.mu.Lock()
	s.percent = v
	s.mu.Unlock()
}

// run échantillonne l'occupation par cœur à intervalle régulier (cpuSampleInterval)
// jusqu'à l'annulation de ctx.
func (s *coreSampler) run(ctx context.Context) {
	prev, ok := cpuTimesPerCore()

	ticker := time.NewTicker(cpuSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, valid := cpuTimesPerCore()
			if !valid {
				continue
			}
			// Nombre de cœurs changé (hotplug) ou premier relevé : on ré-amorce.
			if !ok || len(cur) != len(prev) {
				prev, ok = cur, true
				continue
			}
			s.set(perCoreBusy(prev, cur, s.get()))
			prev = cur
		}
	}
}

// cpuTimesPerCore lit les temps CPU cumulés de chaque cœur logique.
func cpuTimesPerCore() ([]cpu.TimesStat, bool) {
	t, err := cpu.Times(true)
	if err != nil || len(t) == 0 {
		return nil, false
	}
	return t, true
}

// perCoreBusy calcule l'occupation (0–100) de chaque cœur entre deux relevés. Un
// cœur dont les compteurs n'ont pas progressé (relevé fantôme) conserve sa
// dernière valeur connue (last) plutôt que de retomber à 0.
func perCoreBusy(prev, cur []cpu.TimesStat, last []float64) []float64 {
	out := make([]float64, len(cur))
	for i := range cur {
		if i >= len(prev) {
			continue
		}
		if pct, moved := cpuBusyPercent(prev[i], cur[i]); moved {
			out[i] = pct
		} else if i < len(last) {
			out[i] = last[i]
		}
	}
	return out
}

// tempSampler maintient la température du capteur le plus chaud (best-effort) et
// son identifiant. Nombre de plateformes n'exposent aucun capteur (ou exigent des
// droits) : le champ reste alors à zéro et l'interface ne l'affiche pas.
type tempSampler struct {
	mu      sync.RWMutex
	celsius float64
	label   string
}

func (s *tempSampler) get() (float64, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.celsius, s.label
}

func (s *tempSampler) set(c float64, label string) {
	s.mu.Lock()
	s.celsius, s.label = c, label
	s.mu.Unlock()
}

// run relève la température à intervalle régulier jusqu'à l'annulation de ctx. Si
// le premier relevé n'expose aucun capteur exploitable, la goroutine s'arrête :
// inutile de sonder en boucle une plateforme qui ne fournit rien.
func (s *tempSampler) run(ctx context.Context) {
	c, label := readHottestTemp()
	s.set(c, label)
	if c == 0 {
		return
	}

	ticker := time.NewTicker(tempSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.set(readHottestTemp())
		}
	}
}

// readHottestTemp lit les capteurs et renvoie la valeur la plus chaude. Best-effort :
// une erreur (capteurs indisponibles) renvoie une liste vide, donc 0.
func readHottestTemp() (float64, string) {
	temps, _ := sensors.SensorsTemperatures()
	return hottestTemp(temps)
}

// hottestTemp renvoie la température la plus élevée (et son capteur) parmi des
// relevés, en écartant les valeurs nulles ou aberrantes (> 130 °C).
func hottestTemp(temps []sensors.TemperatureStat) (float64, string) {
	var maxC float64
	var label string
	for _, t := range temps {
		if t.Temperature > maxC && t.Temperature < 130 {
			maxC, label = t.Temperature, t.SensorKey
		}
	}
	return maxC, label
}

// diskSampler maintient la liste des volumes montés significatifs (pour le
// sélecteur de disque de l'interface), rafraîchie en arrière-plan.
type diskSampler struct {
	mu   sync.RWMutex
	list []Disk
}

func (s *diskSampler) get() []Disk {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.list == nil {
		return nil
	}
	out := make([]Disk, len(s.list))
	copy(out, s.list)
	return out
}

func (s *diskSampler) set(v []Disk) {
	s.mu.Lock()
	s.list = v
	s.mu.Unlock()
}

// run énumère les volumes à intervalle régulier jusqu'à l'annulation de ctx. Le
// volume surveillé par défaut (diskPath) est toujours inclus, quelle que soit sa
// taille.
func (s *diskSampler) run(ctx context.Context, diskPath string) {
	s.set(listVolumes(diskPath))

	ticker := time.NewTicker(diskSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.set(listVolumes(diskPath))
		}
	}
}

// listVolumes énumère les partitions montées et relève l'occupation de chacune,
// en écartant les systèmes de fichiers virtuels et les volumes plus petits que
// minVolumeBytes (hors volume par défaut, toujours présent). Trié par chemin.
func listVolumes(diskPath string) []Disk {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	return selectVolumes(parts, disk.Usage, diskPath)
}

// selectVolumes filtre et déduplique les partitions puis relève l'occupation de
// chacune via usage (injecté pour la testabilité). Règles : on écarte les systèmes
// de fichiers virtuels et les volumes plus petits que minVolumeBytes ; on
// déduplique par occupation brute — plusieurs volumes d'un même conteneur APFS
// (macOS : /, /System/Volumes/Data…) remontent des chiffres identiques, alors que
// deux systèmes de fichiers réellement distincts diffèrent et restent séparés.
//
// Le volume surveillé par défaut (diskPath) est ajouté en premier et sa signature
// d'occupation pré-enregistrée : ses volumes-frères d'un même conteneur fusionnent
// donc vers lui quel que soit l'ordre de disk.Partitions (sinon un frère listé
// avant la racine survivait à la dédup, puis la racine était ajoutée en plus — une
// entrée redondante). Il est ainsi toujours présent, même filtré (petit) ou absent
// des partitions. Résultat trié par chemin.
func selectVolumes(parts []disk.PartitionStat, usage func(string) (*disk.UsageStat, error), diskPath string) []Disk {
	out := make([]Disk, 0, len(parts))
	seenMount := make(map[string]bool, len(parts))
	seenUsage := make(map[[2]uint64]bool, len(parts))

	if u, err := usage(diskPath); err == nil && u != nil && u.Total > 0 {
		seenMount[diskPath] = true
		seenUsage[[2]uint64{u.Total, u.Used}] = true
		out = append(out, diskFromUsage(u))
	}

	for _, p := range parts {
		if pseudoFstypes[strings.ToLower(p.Fstype)] || seenMount[p.Mountpoint] {
			continue
		}
		u, err := usage(p.Mountpoint)
		if err != nil || u == nil || u.Total == 0 {
			continue
		}
		usageKey := [2]uint64{u.Total, u.Used}
		if u.Total < minVolumeBytes || seenUsage[usageKey] {
			continue
		}
		seenMount[p.Mountpoint] = true
		seenUsage[usageKey] = true
		out = append(out, diskFromUsage(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// diskFromUsage convertit un relevé gopsutil d'occupation en Disk.
func diskFromUsage(u *disk.UsageStat) Disk {
	return Disk{
		UsedPercent: u.UsedPercent,
		UsedGB:      float64(u.Used) / giga,
		TotalGB:     float64(u.Total) / giga,
		Path:        u.Path,
		Fstype:      u.Fstype,
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
	// Publie d'emblée les volumes cumulés (le débit reste inconnu → 0 jusqu'au
	// second relevé) : sans cela, get() renverrait des totaux à zéro pendant tout
	// le premier intervalle, alors que ces compteurs sont déjà disponibles.
	if ok {
		s.set(Net{RecvTotalBytes: prev.recv, SentTotalBytes: prev.sent})
	}

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

// netRate calcule le débit (octets/s) entre deux relevés et reporte les
// volumes cumulés du relevé courant.
func netRate(prev, cur netTotals) Net {
	n := Net{RecvTotalBytes: cur.recv, SentTotalBytes: cur.sent}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return n
	}
	n.RecvBytesPerSec = perSec(prev.recv, cur.recv, elapsed)
	n.SentBytesPerSec = perSec(prev.sent, cur.sent, elapsed)
	return n
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

// diskIOSampler maintient le dernier débit d'E/S disque connu (agrégé sur toutes
// les unités), calculé en différentiant les compteurs cumulés — même principe
// que netSampler.
type diskIOSampler struct {
	mu   sync.RWMutex
	rate DiskIO
}

func (s *diskIOSampler) get() DiskIO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rate
}

func (s *diskIOSampler) set(r DiskIO) {
	s.mu.Lock()
	s.rate = r
	s.mu.Unlock()
}

// run relève les compteurs d'E/S disque à intervalle régulier et met en cache le
// débit instantané jusqu'à l'annulation de ctx.
func (s *diskIOSampler) run(ctx context.Context) {
	prev, ok := readDiskIOTotals()

	ticker := time.NewTicker(diskIOSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, valid := readDiskIOTotals()
			if ok && valid {
				s.set(diskIORate(prev, cur))
			}
			if valid {
				prev, ok = cur, true
			}
		}
	}
}

// diskIOTotals est un relevé instantané des compteurs d'E/S cumulés.
type diskIOTotals struct {
	read, write uint64
	at          time.Time
}

// readDiskIOTotals somme les compteurs de lecture/écriture de toutes les unités.
func readDiskIOTotals() (diskIOTotals, bool) {
	counters, err := disk.IOCounters()
	if err != nil || len(counters) == 0 {
		return diskIOTotals{}, false
	}
	var r, w uint64
	for _, c := range counters {
		r += c.ReadBytes
		w += c.WriteBytes
	}
	return diskIOTotals{read: r, write: w, at: time.Now()}, true
}

// diskIORate calcule le débit d'E/S (octets/s) entre deux relevés.
func diskIORate(prev, cur diskIOTotals) DiskIO {
	io := DiskIO{}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return io
	}
	io.ReadBytesPerSec = perSec(prev.read, cur.read, elapsed)
	io.WriteBytesPerSec = perSec(prev.write, cur.write, elapsed)
	return io
}

// procSampler maintient les deux classements (CPU, mémoire) des processus les
// plus consommateurs. L'utilisation CPU instantanée est calculée en différentiant
// les temps CPU cumulés entre deux énumérations espacées.
type procSampler struct {
	mu     sync.RWMutex
	result *Processes
}

func (s *procSampler) get() *Processes {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.result
}

func (s *procSampler) set(p *Processes) {
	s.mu.Lock()
	s.result = p
	s.mu.Unlock()
}

// run énumère les processus à intervalle régulier et met en cache les deux
// classements jusqu'à l'annulation de ctx. Le relevé précédent (prev) reste
// local à la goroutine : aucun verrou n'est nécessaire pour le manipuler.
func (s *procSampler) run(ctx context.Context, currentUser string) {
	// Cache uid → nom d'utilisateur, propre à cette goroutine (pas de verrou),
	// partagé entre tous les relevés pour ne résoudre chaque uid qu'une fois.
	users := make(usernameCache)
	prev, prevAt := readProcs(users), time.Now()

	ticker := time.NewTicker(procSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, curAt := readProcs(users), time.Now()
			elapsed := curAt.Sub(prevAt).Seconds()
			if elapsed <= 0 {
				continue
			}
			var totalMem uint64
			if vm, err := mem.VirtualMemory(); err == nil {
				totalMem = vm.Total
			}
			s.set(aggregateProcesses(prev, cur, elapsed, totalMem, runtime.NumCPU(), currentUser))
			prev, prevAt = cur, curAt
		}
	}
}

// procSample est un relevé brut, par processus, des compteurs cumulés servant au
// calcul (séparé de la lecture gopsutil pour rester testable).
type procSample struct {
	pid     int32
	ppid    int32 // PID du parent, pour le regroupement par arbre
	name    string
	user    string  // propriétaire du processus
	cpuTime float64 // temps CPU cumulé, en secondes
	rss     uint64  // mémoire résidente (octets)
}

// usernameCache mémoïse la résolution uid → nom d'utilisateur sur la durée de
// vie du sampler. Sur une machine, quelques uid seulement possèdent la plupart
// des processus : le cache évite autant d'appels à user.LookupId (lecture de la
// base passwd) à chaque relevé. Il est propre à la goroutine du sampler, donc
// sans verrou.
type usernameCache map[uint32]string

// resolve renvoie le propriétaire d'un processus. Sous Windows, le propriétaire
// se résout par jeton/SID (pas d'uid numérique) : on délègue à Username sans
// mise en cache. Ailleurs (POSIX), Username revient déjà à Uids + LookupId ; on
// fait donc l'Uids nous-mêmes et on met le nom en cache par uid.
func (c usernameCache) resolve(p *process.Process) string {
	if runtime.GOOS == "windows" {
		name, _ := p.Username()
		return name
	}
	uids, err := p.Uids()
	if err != nil || len(uids) == 0 {
		return ""
	}
	uid := uids[0]
	if name, ok := c[uid]; ok {
		return name
	}
	name := ""
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		name = u.Username
	}
	c[uid] = name
	return name
}

// readProcs énumère les processus et relève, pour chacun, son nom, son parent,
// son propriétaire, son temps CPU cumulé et sa mémoire résidente. Les processus
// disparus en cours de lecture (ou inaccessibles) sont silencieusement ignorés.
func readProcs(users usernameCache) []procSample {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	samples := make([]procSample, 0, len(procs))
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			continue
		}
		times, err := p.Times()
		if err != nil {
			continue
		}
		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		// Le parent est optionnel : à défaut, le processus formera sa propre racine.
		ppid, _ := p.Ppid()
		// Le propriétaire est optionnel : indisponible pour certains processus
		// système, on le laisse alors vide (le groupe ne sera pas « killable »).
		user := users.resolve(p)
		samples = append(samples, procSample{
			pid:  p.Pid,
			ppid: ppid,
			name: name,
			user: user,
			// Temps CPU consommé par le processus = temps utilisateur + système.
			// (Les autres champs de TimesStat, dont Idle, n'ont pas de sens ici.)
			cpuTime: times.User + times.System,
			rss:     rss,
		})
	}
	return samples
}

// aggregateProcesses regroupe les processus par **application** : chacun est
// rattaché à son ancêtre de plus haut niveau (cf. rootAncestor) et tout le
// sous-arbre est sommé sous le nom de cette racine. Le CPU est calculé par delta
// de temps CPU sur l'intervalle, en % d'un cœur (façon top/htop : un programme
// multi-thread peut dépasser 100 %). Un groupe est « killable » si toutes ses
// instances appartiennent à currentUser. Renvoie les deux classements bornés à
// procTopN.
func aggregateProcesses(prev, cur []procSample, elapsed float64, totalMem uint64, numCPU int, currentUser string) *Processes {
	if elapsed <= 0 {
		return &Processes{TopCPU: []ProcessInfo{}, TopMem: []ProcessInfo{}}
	}

	prevTime := make(map[int32]float64, len(prev))
	for _, s := range prev {
		prevTime[s.pid] = s.cpuTime
	}

	// Index des relevés courants par PID, pour remonter la chaîne des parents et
	// nommer chaque racine.
	ppidOf := make(map[int32]int32, len(cur))
	nameOf := make(map[int32]string, len(cur))
	userOf := make(map[int32]string, len(cur))
	for _, s := range cur {
		ppidOf[s.pid] = s.ppid
		nameOf[s.pid] = s.name
		userOf[s.pid] = s.user
	}

	// group porte l'accumulateur public et un drapeau interne (allOwned), non
	// exporté car propre au calcul.
	type group struct {
		info     *ProcessInfo
		allOwned bool // toutes les instances appartiennent à currentUser
	}
	groups := make(map[int32]*group)
	for _, s := range cur {
		root := rootAncestor(s.pid, ppidOf)

		// CPU : delta depuis le relevé précédent. Un processus neuf (absent de
		// prev) ou un PID recyclé (delta négatif) contribue 0, faute de référence.
		var cpuPct float64
		if pt, ok := prevTime[s.pid]; ok {
			if delta := s.cpuTime - pt; delta > 0 {
				cpuPct = delta / elapsed * 100
			}
		}

		g := groups[root]
		if g == nil {
			g = &group{info: &ProcessInfo{Name: nameOf[root], User: userOf[root]}, allOwned: true}
			groups[root] = g
		}
		g.info.Count++
		g.info.CPUPercent += cpuPct
		g.info.MemBytes += s.rss
		g.info.PIDs = append(g.info.PIDs, s.pid)
		g.allOwned = g.allOwned && s.user == currentUser
	}

	all := make([]ProcessInfo, 0, len(groups))
	for _, g := range groups {
		p := g.info
		// Terminable seulement si l'utilisateur courant est connu et que toutes
		// les instances du sous-arbre lui appartiennent.
		p.Killable = currentUser != "" && g.allOwned
		if totalMem > 0 {
			p.MemPercent = float64(p.MemBytes) / float64(totalMem) * 100
		}
		// Même charge ramenée à la machine entière, pour comparaison directe avec
		// la jauge CPU globale.
		if numCPU > 0 {
			p.CPUPercentSystem = p.CPUPercent / float64(numCPU)
		}
		all = append(all, *p)
	}

	return &Processes{
		TopCPU: topByCPU(all),
		TopMem: topByMem(all),
	}
}

// rootAncestor remonte la chaîne des parents jusqu'à l'ancêtre de plus haut
// niveau présent dans le relevé : celui dont le parent est launchd (pid ≤ 1) ou
// a disparu. Le nombre d'itérations est borné pour se prémunir d'un cycle.
func rootAncestor(pid int32, ppidOf map[int32]int32) int32 {
	cur := pid
	for range 64 {
		pp, ok := ppidOf[cur]
		if !ok || pp <= 1 {
			return cur
		}
		if _, present := ppidOf[pp]; !present {
			return cur // parent disparu : l'orphelin est sa propre racine
		}
		cur = pp
	}
	return cur
}

// topByCPU renvoie une copie de procs triée par utilisation CPU décroissante,
// bornée à procTopN.
func topByCPU(procs []ProcessInfo) []ProcessInfo {
	out := make([]ProcessInfo, len(procs))
	copy(out, procs)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CPUPercent > out[j].CPUPercent
	})
	return truncate(out, procTopN)
}

// topByMem renvoie une copie de procs triée par mémoire résidente décroissante,
// bornée à procTopN.
func topByMem(procs []ProcessInfo) []ProcessInfo {
	out := make([]ProcessInfo, len(procs))
	copy(out, procs)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].MemBytes > out[j].MemBytes
	})
	return truncate(out, procTopN)
}

func truncate(procs []ProcessInfo, n int) []ProcessInfo {
	if len(procs) > n {
		// Clip borne la capacité à n : le classement renvoyé est final, un append
		// ultérieur réallouerait plutôt que d'écraser le tableau source.
		return slices.Clip(procs[:n])
	}
	return procs
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
	// Swap : best-effort. Une erreur (ou l'absence de swap) ne compromet pas le
	// relevé mémoire principal — les champs restent simplement à zéro.
	if sw, err := mem.SwapMemory(); err == nil && sw != nil {
		i.Memory.SwapTotalGB = float64(sw.Total) / giga
		i.Memory.SwapUsedGB = float64(sw.Used) / giga
		i.Memory.SwapUsedPercent = sw.UsedPercent
	}
	return nil
}

func (i *Info) collectDisk(path string) error {
	usage, err := disk.Usage(path)
	if err != nil {
		return err
	}
	i.Disk = diskFromUsage(usage)
	i.Disk.Path = path // conserve le chemin demandé tel quel
	return nil
}
