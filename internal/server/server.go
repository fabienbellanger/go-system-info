// Package server expose les métriques système via une API REST et sert
// l'interface web statique.
package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"gosysteminfo/internal/sysinfo"
)

// Config rassemble les paramètres d'exécution du serveur.
type Config struct {
	Port    int           // Port d'écoute HTTP.
	Refresh time.Duration // Intervalle de rafraîchissement exposé à l'interface.
	Static  fs.FS         // Système de fichiers du contenu statique (interface web).
}

// Server encapsule la configuration et le routage HTTP.
type Server struct {
	cfg Config
}

// New construit un serveur à partir de sa configuration.
func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

// Handler assemble les routes et renvoie le gestionnaire HTTP racine.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system", s.handleSystem)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.Handle("/", http.FileServer(http.FS(s.cfg.Static)))
	return mux
}

// ListenAndServe démarre le serveur HTTP (appel bloquant).
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	log.Printf("Server started on http://localhost%s (refresh: %s)...", addr, s.cfg.Refresh)
	return http.ListenAndServe(addr, s.Handler())
}

// handleSystem renvoie les informations système au format JSON.
func (s *Server) handleSystem(w http.ResponseWriter, _ *http.Request) {
	info, err := sysinfo.Collect()
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
