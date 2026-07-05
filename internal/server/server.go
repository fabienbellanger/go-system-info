// Package server expose les métriques système via une API REST et sert
// l'interface web statique.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gosysteminfo/internal/sysinfo"
)

// DefaultRefresh est l'intervalle de rafraîchissement par défaut de l'application,
// source de vérité unique partagée entre ses deux usages : main l'emploie comme
// valeur par défaut du flag -r, et handleStream comme repli défensif si la
// configuration porte un intervalle nul ou négatif (time.NewTicker paniquerait
// sinon). En usage normal, parseFlags garantit déjà une valeur valide.
const DefaultRefresh = 3 * time.Second

// Délais appliqués au serveur HTTP pour se prémunir des connexions lentes
// (type Slowloris) et borner la durée d'arrêt gracieux.
const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 15 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 10 * time.Second

	// heartbeatInterval espace les commentaires SSE de maintien de connexion.
	heartbeatInterval = 20 * time.Second
)

// Config rassemble les paramètres d'exécution du serveur.
type Config struct {
	Host     string        // Adresse d'écoute ; vide = toutes les interfaces.
	Port     int           // Port d'écoute HTTP.
	Refresh  time.Duration // Intervalle de rafraîchissement exposé à l'interface.
	Static   fs.FS         // Système de fichiers du contenu statique (interface web).
	Version  string        // Version du binaire (injectée au build), exposée via /api/version.
	DiskPath string        // Volume à surveiller ; vide = défaut selon l'OS (voir sysinfo).
	// ReadOnly désactive la terminaison de processus (POST /api/processes/kill) :
	// l'interface reste consultable mais ne peut plus agir sur la machine, ce qui
	// rend défendable une écoute sur toutes les interfaces.
	ReadOnly bool
	// TrustedHosts liste, séparés par des virgules, des noms d'hôte de confiance
	// supplémentaires acceptés dans l'en-tête Host (au-delà de localhost, du nom
	// de la machine et des adresses IP littérales, toujours admis). Utile derrière
	// un reverse proxy exposant l'application sous un nom de domaine.
	TrustedHosts string
}

// systemCollector abstrait la source des métriques système. L'interface permet
// d'injecter un collecteur factice dans les tests, sans dépendre de la machine
// réelle (notamment pour couvrir le cas d'erreur de handleSystem).
type systemCollector interface {
	Start(ctx context.Context)
	Collect() (*sysinfo.Info, error)
	History() []sysinfo.HistorySample
	Kill(pid int32) error
	Details(pids []int32) []sysinfo.ProcessDetail
}

// Server encapsule la configuration et le routage HTTP.
type Server struct {
	cfg       Config
	collector systemCollector
	// allowedHosts est l'ensemble des noms d'hôte acceptés dans l'en-tête Host
	// (parade au DNS rebinding, cf. checkHost). Résolu une fois par New ; nil
	// lorsque le Server est construit directement (tests), auquel cas la
	// vérification est désactivée.
	allowedHosts map[string]bool
}

// New construit un serveur à partir de sa configuration.
func New(cfg Config) *Server {
	return &Server{
		cfg:          cfg,
		collector:    sysinfo.NewCollector(cfg.DiskPath),
		allowedHosts: allowedHosts(cfg),
	}
}

// allowedHosts calcule l'ensemble des valeurs d'en-tête Host acceptées, en plus
// des adresses IP littérales (toujours admises par hostAllowed). C'est la parade
// au DNS rebinding : un site tiers qui re-résout son domaine vers 127.0.0.1
// émettrait un Host de la forme « attaquant.tld », que l'on refuse faute de
// figurer ici. Les noms sont normalisés en minuscules.
func allowedHosts(cfg Config) map[string]bool {
	set := map[string]bool{"localhost": true}
	if h, err := os.Hostname(); err == nil && h != "" {
		h = strings.ToLower(h)
		set[h] = true
		// macOS/Bonjour expose souvent la machine sous « nom.local ».
		if !strings.Contains(h, ".") {
			set[h+".local"] = true
		}
	}
	// Hôte d'écoute explicite (-host) : légitime par construction.
	if cfg.Host != "" {
		set[strings.ToLower(cfg.Host)] = true
	}
	for h := range strings.SplitSeq(cfg.TrustedHosts, ",") {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			set[h] = true
		}
	}
	return set
}

// Handler assemble les routes et renvoie le gestionnaire HTTP racine,
// enveloppé par le middleware de journalisation des requêtes.
func (s *Server) Handler() http.Handler {
	// Chaque route d'API impose sa méthode via allow (405 sinon). On ne peut pas
	// s'en remettre aux motifs « GET /… » du ServeMux : le catch-all « / » (fichiers
	// statiques) matche toutes les méthodes et absorberait la requête (→ 404) avant
	// que le mux ne produise un 405.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system", allow(http.MethodGet, s.handleSystem))
	mux.HandleFunc("/api/history", allow(http.MethodGet, s.handleHistory))
	mux.HandleFunc("/api/stream", allow(http.MethodGet, s.handleStream))
	mux.HandleFunc("/api/config", allow(http.MethodGet, s.handleConfig))
	mux.HandleFunc("/api/health", allow(http.MethodGet, s.handleHealth))
	mux.HandleFunc("/api/version", allow(http.MethodGet, s.handleVersion))
	mux.HandleFunc("/api/processes/detail", allow(http.MethodGet, s.handleDetail))
	// La terminaison de processus n'est routée qu'hors mode lecture seule ; en
	// lecture seule, on répond explicitement 403 plutôt que de laisser la requête
	// filer vers le serveur de fichiers (404 trompeur).
	if s.cfg.ReadOnly {
		mux.HandleFunc("/api/processes/kill", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "mode lecture seule : terminaison désactivée", http.StatusForbidden)
		})
	} else {
		mux.HandleFunc("/api/processes/kill", allow(http.MethodPost, s.handleKill))
	}
	mux.Handle("/", staticCacheControl(http.FileServer(http.FS(s.cfg.Static))))
	// checkHost enveloppe tout le routage (parade au DNS rebinding) ; logRequests
	// reste le plus externe pour journaliser aussi les requêtes refusées.
	return logRequests(s.checkHost(mux))
}

// staticCacheControl force la revalidation des assets embarqués. embed.FS
// n'expose pas de date de modification, donc http.FileServer n'émet ni
// Last-Modified ni ETag : sans en-tête, le navigateur applique un cache
// heuristique et peut resservir un ancien app.js/styles.css après une
// reconstruction du binaire (bundle affiché désynchronisé de la version en
// cours d'exécution). « no-cache » impose une revalidation à chaque chargement.
func staticCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// checkHost refuse les requêtes dont l'en-tête Host n'est ni une adresse IP
// littérale ni un nom de confiance (cf. allowedHosts) : c'est la parade au DNS
// rebinding, qui permettrait sinon à une page tierce de dialoguer avec ce démon
// local en même origine. Désactivé quand aucun hôte n'a été résolu (Server
// construit sans New, notamment dans les tests).
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.allowedHosts) > 0 && !s.hostAllowed(r.Host) {
			http.Error(w, "en-tête Host non autorisé", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed indique si l'en-tête Host (« nom » ou « nom:port ») est acceptable.
// Une adresse IP littérale l'est toujours : un DNS rebinding s'appuie sur un nom
// de domaine, jamais sur une IP nue. Sinon le nom doit figurer dans allowedHosts.
func (s *Server) hostAllowed(host string) bool {
	if host == "" {
		return false
	}
	h := host
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		h = hostname
	}
	// Adresse IPv6 littérale sans port : « [::1] » → « ::1 ».
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if net.ParseIP(h) != nil {
		return true
	}
	return s.allowedHosts[strings.ToLower(h)]
}

// allow restreint un handler à une méthode HTTP (HEAD étant toléré pour GET),
// répondant 405 avec l'en-tête Allow sinon.
func allow(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
			w.Header().Set("Allow", method)
			http.Error(w, "méthode non autorisée", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// ListenAndServe démarre le serveur HTTP (appel bloquant). Il s'arrête
// proprement à la réception de SIGINT/SIGTERM en laissant aux requêtes en
// cours le temps de se terminer (dans la limite de shutdownTimeout).
func (s *Server) ListenAndServe() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Démarre la collecte CPU en arrière-plan : les requêtes deviennent
	// instantanées et la charge de mesure reste constante.
	s.collector.Start(ctx)

	// Host vide → « :port » = toutes les interfaces ; sinon « host:port » pour
	// restreindre l'écoute (ex. 127.0.0.1 pour la seule machine locale).
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
		// Rattache le contexte de chaque requête au contexte de signal : à la
		// réception de SIGINT/SIGTERM, les handlers longue durée (le flux SSE, dont
		// la boucle écoute r.Context().Done()) se terminent aussitôt, au lieu de
		// faire patienter Shutdown jusqu'à shutdownTimeout puis échouer.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// Hôte affiché dans l'URL : « localhost » quand on écoute sur toutes les
	// interfaces (Host vide), sinon l'adresse effectivement liée.
	displayHost := s.cfg.Host
	if displayHost == "" {
		displayHost = "localhost"
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("serveur démarré",
			"url", fmt.Sprintf("http://%s:%d", displayHost, s.cfg.Port),
			"refresh", s.cfg.Refresh,
			"version", s.cfg.Version,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("arrêt en cours...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// handleSystem renvoie les informations système au format JSON.
func (s *Server) handleSystem(w http.ResponseWriter, _ *http.Request) {
	info, err := s.collector.Collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

// handleHistory renvoie l'historique glissant des mesures CPU/mémoire,
// du plus ancien au plus récent, au format JSON.
func (s *Server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.collector.History())
}

// killResult rapporte l'issue d'une demande de terminaison pour un PID donné.
type killResult struct {
	PID   int32  `json:"pid"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Bornes de sécurité de handleKill.
const (
	// maxKillPIDs plafonne le nombre de PID traités en une requête.
	maxKillPIDs = 256
	// maxKillBody borne la taille du corps accepté (anti-abus mémoire).
	maxKillBody = 64 << 10 // 64 Kio
)

// handleKill termine les processus dont les PID sont fournis dans le corps JSON
// ({"pids":[…]}). La sécurité est déléguée au collecteur, qui refuse tout
// processus n'appartenant pas à l'utilisateur ayant lancé le serveur.
//
// L'endpoint ayant un effet destructeur, il est protégé du CSRF : on exige un
// Content-Type application/json (qu'une requête « simple » cross-site — la seule
// qu'un site tiers peut émettre sans preflight CORS — ne peut pas produire) et
// on refuse toute requête que le navigateur signale explicitement comme
// cross-site. La restriction « même utilisateur » reste le garde-fou de fond.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	// La méthode POST est déjà garantie par le motif de route (405 sinon).
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "requête cross-site refusée", http.StatusForbidden)
		return
	}
	if !hasJSONContentType(r) {
		http.Error(w, "Content-Type application/json requis", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxKillBody)
	var req struct {
		PIDs []int32 `json:"pids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "corps JSON invalide", http.StatusBadRequest)
		return
	}
	if len(req.PIDs) == 0 {
		http.Error(w, "aucun PID fourni", http.StatusBadRequest)
		return
	}
	if len(req.PIDs) > maxKillPIDs {
		http.Error(w, "trop de PID fournis", http.StatusBadRequest)
		return
	}

	results := make([]killResult, 0, len(req.PIDs))
	for _, pid := range req.PIDs {
		res := killResult{PID: pid, OK: true}
		if err := s.collector.Kill(pid); err != nil {
			res.OK = false
			res.Error = err.Error()
		}
		results = append(results, res)
	}
	writeJSON(w, map[string]any{"results": results})
}

// hasJSONContentType indique si l'en-tête Content-Type de la requête a pour type
// de média application/json (les paramètres comme « ; charset=utf-8 » sont
// tolérés). C'est le pivot de la protection CSRF de handleKill.
func hasJSONContentType(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// maxDetailPIDs borne le nombre de PID interrogeables en une requête de détail.
const maxDetailPIDs = 128

// handleDetail renvoie le détail courant des processus dont les PID sont passés
// dans le paramètre de requête `pids` (liste séparée par des virgules).
func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	pids := parsePIDs(r.URL.Query().Get("pids"))
	if len(pids) == 0 {
		http.Error(w, "paramètre pids manquant ou invalide", http.StatusBadRequest)
		return
	}
	if len(pids) > maxDetailPIDs {
		pids = pids[:maxDetailPIDs]
	}
	writeJSON(w, map[string]any{"instances": s.collector.Details(pids)})
}

// parsePIDs convertit une liste de PID séparés par des virgules ("12,34") en
// entiers, en ignorant silencieusement les entrées vides ou non numériques.
func parsePIDs(raw string) []int32 {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	pids := make([]int32, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			continue
		}
		pids = append(pids, int32(n))
	}
	return pids
}

// streamState est la charge utile poussée à chaque événement SSE : l'état
// instantané et l'historique glissant, regroupés pour éviter au client deux
// requêtes par cycle.
type streamState struct {
	System  *sysinfo.Info           `json:"system"`
	History []sysinfo.HistorySample `json:"history"`
}

// handleStream pousse l'état système en temps réel via Server-Sent Events,
// remplaçant le polling côté client. Un premier événement est émis aussitôt,
// puis un nouveau à chaque intervalle de rafraîchissement, jusqu'à la fermeture
// de la connexion.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	rc := http.NewResponseController(w)
	// Une connexion SSE est longue : on neutralise le WriteTimeout du serveur
	// pour cette requête, sinon elle serait coupée au bout de writeTimeout.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming non supporté", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	refresh := s.cfg.Refresh
	if refresh <= 0 {
		// Repli défensif : NewTicker panique pour une durée ≤ 0. parseFlags
		// écarte déjà ce cas, mais un Server construit directement pourrait ne pas.
		refresh = DefaultRefresh
	}
	ticker := time.NewTicker(refresh)
	defer ticker.Stop()

	// Battement de cœur indépendant : un commentaire SSE périodique garde la
	// connexion chaude derrière un proxy à timeout d'inactivité court quand
	// l'intervalle de données est long, sans polluer les événements.
	beat := time.NewTicker(heartbeatInterval)
	defer beat.Stop()

	// Premier événement immédiat, puis à chaque battement de l'un ou l'autre.
	if err := s.writeStreamEvent(w, rc); err != nil {
		return // client déconnecté ou erreur d'écriture/collecte
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.writeStreamEvent(w, rc); err != nil {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// writeStreamEvent sérialise l'état courant et l'écrit comme un événement SSE,
// puis vide le tampon pour une livraison immédiate.
func (s *Server) writeStreamEvent(w http.ResponseWriter, rc *http.ResponseController) error {
	info, err := s.collector.Collect()
	if err != nil {
		return err
	}
	data, err := json.Marshal(streamState{System: info, History: s.collector.History()})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return rc.Flush()
}

// handleConfig expose la configuration consommée par l'interface : l'intervalle
// de rafraîchissement et le mode lecture seule (pour masquer côté client les
// actions de terminaison quand elles sont désactivées côté serveur).
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"refresh_ms": s.cfg.Refresh.Milliseconds(),
		"readonly":   s.cfg.ReadOnly,
	})
}

// handleHealth répond aux sondes de santé des orchestrateurs.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleVersion expose la version du binaire injectée au build.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"version": s.cfg.Version})
}

// writeJSON sérialise v en JSON avec les en-têtes adéquats.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("erreur d'encodage JSON", "err", err)
	}
}

// statusRecorder enveloppe http.ResponseWriter pour mémoriser le code de
// statut HTTP écrit par le handler, afin de pouvoir le journaliser.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap expose le ResponseWriter sous-jacent pour que http.ResponseController
// (Flush, SetWriteDeadline) fonctionne à travers ce wrapper — indispensable au
// streaming SSE de /api/stream.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// logRequests journalise chaque requête (méthode, chemin, statut, durée)
// au format structuré via slog.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("requête HTTP",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
		)
	})
}
