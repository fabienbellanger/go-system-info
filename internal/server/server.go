// Package server expose les métriques système via une API REST et sert
// l'interface web statique.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gosysteminfo/internal/sysinfo"
)

// Délais appliqués au serveur HTTP pour se prémunir des connexions lentes
// (type Slowloris) et borner la durée d'arrêt gracieux.
const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 15 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 10 * time.Second
)

// Config rassemble les paramètres d'exécution du serveur.
type Config struct {
	Port    int           // Port d'écoute HTTP.
	Refresh time.Duration // Intervalle de rafraîchissement exposé à l'interface.
	Static  fs.FS         // Système de fichiers du contenu statique (interface web).
	Version string        // Version du binaire (injectée au build), exposée via /api/version.
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
}

// New construit un serveur à partir de sa configuration.
func New(cfg Config) *Server {
	return &Server{cfg: cfg, collector: sysinfo.NewCollector()}
}

// Handler assemble les routes et renvoie le gestionnaire HTTP racine,
// enveloppé par le middleware de journalisation des requêtes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system", s.handleSystem)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/processes/kill", s.handleKill)
	mux.HandleFunc("/api/processes/detail", s.handleDetail)
	mux.Handle("/", http.FileServer(http.FS(s.cfg.Static)))
	return logRequests(mux)
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

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("serveur démarré",
			"url", fmt.Sprintf("http://localhost%s", addr),
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

// handleKill termine les processus dont les PID sont fournis dans le corps JSON
// ({"pids":[…]}). La sécurité est déléguée au collecteur, qui refuse tout
// processus n'appartenant pas à l'utilisateur ayant lancé le serveur.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "méthode non autorisée", http.StatusMethodNotAllowed)
		return
	}
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
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	// Une connexion SSE est longue : on neutralise le WriteTimeout du serveur
	// pour cette requête, sinon elle serait coupée au bout de writeTimeout.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming non supporté", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	ticker := time.NewTicker(s.cfg.Refresh)
	defer ticker.Stop()

	for {
		if err := s.writeStreamEvent(w, rc); err != nil {
			return // client déconnecté ou erreur d'écriture/collecte
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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

// handleConfig expose la configuration consommée par l'interface.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]int64{
		"refresh_ms": s.cfg.Refresh.Milliseconds(),
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
