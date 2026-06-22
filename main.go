package main

import (
	"embed"
	"flag"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"gosysteminfo/internal/server"
)

// publicFS embarque l'interface web dans le binaire.
//
//go:embed public
var publicFS embed.FS

// version est la version du binaire, injectée au build via
// -ldflags "-X main.version=...". Vaut "dev" pour les exécutions locales.
var version = "dev"

func main() {
	cfg := server.Config{Version: version}
	flag.IntVar(&cfg.Port, "p", 8222, "Port d'écoute du serveur HTTP")
	flag.DurationVar(&cfg.Refresh, "r", 3*time.Second,
		"Intervalle de rafraîchissement de l'interface (ex. 5s, 30s, 1m)")
	flag.Parse()

	// Le contenu statique est servi depuis le sous-dossier "public" embarqué.
	static, err := fs.Sub(publicFS, "public")
	if err != nil {
		slog.Error("contenu statique introuvable", "err", err)
		os.Exit(1)
	}
	cfg.Static = static

	if err := server.New(cfg).ListenAndServe(); err != nil {
		slog.Error("échec du serveur", "err", err)
		os.Exit(1)
	}
}
