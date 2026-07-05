package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"gosysteminfo/internal/server"
)

const (
	// defaultPort est le port par défaut sur lequel le serveur écoute.
	defaultPort = 8222

	// minRefreshInterval borne l'intervalle de rafraîchissement. En deçà, la
	// collecte et la sérialisation tourneraient en boucle trop serrée pour un
	// gain d'affichage nul ; surtout, time.NewTicker (utilisé par le flux SSE)
	// panique pour une durée ≤ 0. La borne rend cette valeur toujours valide.
	minRefreshInterval = 250 * time.Millisecond
)

// L'intervalle de rafraîchissement par défaut du flag -r provient de
// server.DefaultRefresh : une seule source de vérité, partagée avec le repli
// défensif du flux SSE (voir server.DefaultRefresh).

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
	flags.StringVar(&cfg.Host, "host", "",
		"Adresse d'écoute (ex. 127.0.0.1 pour la seule machine locale ; vide = toutes les interfaces)")
	flags.IntVar(&cfg.Port, "p", defaultPort, "Port d'écoute du serveur HTTP")
	flags.DurationVar(&cfg.Refresh, "r", server.DefaultRefresh,
		"Intervalle de rafraîchissement de l'interface (ex. 5s, 30s, 1m)")
	flags.StringVar(&cfg.DiskPath, "d", "",
		"Chemin du volume à surveiller (défaut : / sous Unix, C:\\ sous Windows)")
	flags.BoolVar(&cfg.ReadOnly, "readonly", false,
		"Mode lecture seule : désactive la terminaison de processus (POST /api/processes/kill)")
	flags.StringVar(&cfg.TrustedHosts, "trusted-host", "",
		"Noms d'hôte de confiance supplémentaires (séparés par des virgules) acceptés dans l'en-tête Host, en plus de localhost, du nom de la machine et des adresses IP")

	if err := flags.Parse(args); err != nil {
		return server.Config{}, err
	}

	// Validation : une configuration invalide doit échouer au démarrage plutôt
	// que de provoquer un comportement dégradé (ex. panique de time.NewTicker
	// sur un intervalle nul, cf. flux SSE) ou une adresse d'écoute absurde.
	if cfg.Port < 1 || cfg.Port > 65535 {
		return server.Config{}, reportErr(out,
			fmt.Errorf("port invalide : %d (attendu entre 1 et 65535)", cfg.Port))
	}
	if cfg.Refresh < minRefreshInterval {
		return server.Config{}, reportErr(out,
			fmt.Errorf("intervalle de rafraîchissement invalide : %s (minimum %s)", cfg.Refresh, minRefreshInterval))
	}
	return cfg, nil
}

// reportErr écrit err sur out (comme le fait flag pour ses propres erreurs) puis
// la renvoie, afin que l'utilisateur voie le motif du refus avant l'os.Exit(2).
func reportErr(out io.Writer, err error) error {
	fmt.Fprintln(out, err)
	return err
}
