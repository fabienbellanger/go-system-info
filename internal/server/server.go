// Package server expose les métriques système via une API REST et sert
// l'interface web statique.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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
}

// Server encapsule la configuration et le routage HTTP.
type Server struct {
	cfg       Config
	collector *sysinfo.Collector
}

// New construit un serveur à partir de sa configuration.
func New(cfg Config) *Server {
	return &Server{cfg: cfg, collector: sysinfo.NewCollector()}
}

// Handler assemble les routes et renvoie le gestionnaire HTTP racine.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system", s.handleSystem)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.Handle("/", http.FileServer(http.FS(s.cfg.Static)))
	return mux
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
		log.Printf("Server started on http://localhost%s (refresh: %s)...", addr, s.cfg.Refresh)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Println("Arrêt en cours...")
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

// handleConfig expose la configuration consommée par l'interface.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]int64{
		"refresh_ms": s.cfg.Refresh.Milliseconds(),
	})
}

// writeJSON sérialise v en JSON avec les en-têtes adéquats.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Println("Erreur d'encodage JSON :", err)
	}
}
