// Package sysinfo collecte les métriques système (CPU, mémoire, disque, hôte).
package sysinfo

import (
	"context"
	"fmt"
	"os/user"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

const giga = 1000 * 1000 * 1000

// cpuSampleInterval est la fenêtre de mesure de l'utilisation CPU.
const cpuSampleInterval = 500 * time.Millisecond

// netSampleInterval est l'intervalle entre deux relevés des compteurs réseau,
// servant au calcul du débit instantané.
const netSampleInterval = time.Second

// procSampleInterval est l'intervalle entre deux énumérations complètes des
// processus. Plus espacé que les autres relevés : parcourir tous les processus
// est coûteux, et une carte « consommateurs » n'a pas besoin de plus de fraîcheur.
const procSampleInterval = 3 * time.Second

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
	Net       Net       `json:"net"`
	// Processes est renseigné par le Collector mis en cache ; un relevé ponctuel
	// (fonction Collect libre) le laisse nil, auquel cas le champ est omis du JSON.
	Processes *Processes `json:"processes,omitempty"`
}

// ProcessInfo décrit un programme regroupant une ou plusieurs instances de même
// nom : ses consommations CPU et mémoire y sont sommées.
type ProcessInfo struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`       // nombre d'instances fusionnées
	User       string  `json:"user"`        // propriétaire (vide si instances de comptes différents)
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
	cpu         cpuSampler
	net         netSampler
	proc        procSampler
	history     *history
	currentUser string // propriétaire du serveur, résolu une fois au démarrage
}

// NewCollector construit un collecteur prêt à l'emploi.
func NewCollector() *Collector {
	return &Collector{history: newHistory(historySize), currentUser: currentUsername()}
}

// Start lance, en arrière-plan et jusqu'à l'annulation de ctx, l'échantillonnage
// CPU et l'enregistrement de l'historique. À appeler une seule fois avant de
// servir des requêtes.
func (c *Collector) Start(ctx context.Context) {
	go c.cpu.run(ctx)
	go c.net.run(ctx)
	go c.proc.run(ctx, c.currentUser)
	go c.recordHistory(ctx)
}

// Collect renvoie les métriques courantes en réutilisant les dernières mesures
// CPU et réseau mises en cache (aucun appel bloquant).
func (c *Collector) Collect() (*Info, error) {
	info, err := collect(c.cpu.get(), c.net.get())
	if err != nil {
		return nil, err
	}
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
// (instantané, relu à la demande). Délègue à processDetails.
func (c *Collector) Details(pids []int32) []ProcessDetail {
	return processDetails(pids)
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

// processDetails relit à la demande le détail de chaque PID. Les champs et les
// processus inaccessibles (disparus) sont ignorés silencieusement.
func processDetails(pids []int32) []ProcessDetail {
	details := make([]ProcessDetail, 0, len(pids))
	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue // processus disparu
		}
		d := ProcessDetail{PID: pid}
		if pp, err := p.Ppid(); err == nil {
			d.PPID = pp
		}
		if n, err := p.Name(); err == nil {
			d.Name = n
		}
		if u, err := p.Username(); err == nil {
			d.User = u
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
	prev, prevAt := readProcs(), time.Now()

	ticker := time.NewTicker(procSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, curAt := readProcs(), time.Now()
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

// readProcs énumère les processus et relève, pour chacun, son nom, son parent,
// son propriétaire, son temps CPU cumulé et sa mémoire résidente. Les processus
// disparus en cours de lecture (ou inaccessibles) sont silencieusement ignorés.
func readProcs() []procSample {
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
		user, _ := p.Username()
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
		return procs[:n]
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
