package main

import (
	"embed"
	"flag"
	"io"
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
	cfg, err := parseFlags(os.Args[0], os.Args[1:], os.Stderr)
	if err != nil {
		// flag.ContinueOnError a déjà écrit l'erreur et l'usage.
		os.Exit(2)
	}
	cfg.Version = version

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

// parseFlags analyse les arguments de ligne de commande et renvoie la
// configuration correspondante. Isolé de main pour être testable sans toucher
// à l'état global de flag.CommandLine. out reçoit les messages d'erreur/usage.
func parseFlags(name string, args []string, out io.Writer) (server.Config, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(out)

	var cfg server.Config
	flags.IntVar(&cfg.Port, "p", 8222, "Port d'écoute du serveur HTTP")
	flags.DurationVar(&cfg.Refresh, "r", 3*time.Second,
		"Intervalle de rafraîchissement de l'interface (ex. 5s, 30s, 1m)")

	if err := flags.Parse(args); err != nil {
		return server.Config{}, err
	}
	return cfg, nil
}
