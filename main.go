package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"time"

	"gosysteminfo/internal/logging"
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
	opts, err := parseFlags(os.Args[0], os.Args[1:], os.Stderr)
	if err != nil {
		// flag.ContinueOnError a déjà écrit l'erreur et l'usage.
		os.Exit(2)
	}

	if opts.LogPath != "" {
		closeLog := setupFileLogging(opts.LogPath)
		defer closeLog()
	}

	cfg := opts.Config
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

// options rassemble la configuration issue de la ligne de commande : celle du
// serveur, et ce qui ne concerne que le processus lui-même.
type options struct {
	server.Config

	// LogPath est le fichier de journal (flag -log). Vide = sortie d'erreur
	// standard, captée par le gestionnaire de service (launchd, systemd, Docker).
	LogPath string
}

// setupFileLogging redirige la journalisation vers le fichier tournant path et
// renvoie la fonction de fermeture.
//
// slog n'est pas reconfiguré : son handler par défaut écrit via le logger
// standard, donc rediriger celui-ci suffit et préserve le format des messages.
// Bonus : les paniques rattrapées par net/http dans un handler passent aussi par
// log.Printf, et atterrissent donc dans le même journal. Seules les paniques
// fatales du runtime restent écrites directement sur la sortie d'erreur.
func setupFileLogging(path string) func() {
	lf, err := logging.Open(path, logging.DefaultMaxBytes, logging.DefaultKeep)
	if err != nil {
		// Un journal inaccessible ne doit pas empêcher le service de tourner :
		// on reste sur la sortie d'erreur.
		slog.Warn("journalisation dans un fichier impossible, repli sur la sortie d'erreur",
			"chemin", path, "err", err)
		return func() {}
	}
	log.SetOutput(lf)
	slog.Info("journalisation dans un fichier",
		"chemin", lf.Path(), "taille_max", logging.DefaultMaxBytes, "archives", logging.DefaultKeep)
	return func() { _ = lf.Close() }
}

// parseFlags analyse les arguments de ligne de commande et renvoie la
// configuration correspondante. Isolé de main pour être testable sans toucher
// à l'état global de flag.CommandLine. out reçoit les messages d'erreur/usage.
func parseFlags(name string, args []string, out io.Writer) (options, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(out)

	var opts options
	cfg := &opts.Config
	flags.StringVar(&opts.LogPath, "log", "",
		"Fichier de journal (rotation automatique). Vide = sortie d'erreur standard")
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
		return options{}, err
	}

	// Validation : une configuration invalide doit échouer au démarrage plutôt
	// que de provoquer un comportement dégradé (ex. panique de time.NewTicker
	// sur un intervalle nul, cf. flux SSE) ou une adresse d'écoute absurde.
	if cfg.Port < 1 || cfg.Port > 65535 {
		return options{}, reportErr(out,
			fmt.Errorf("port invalide : %d (attendu entre 1 et 65535)", cfg.Port))
	}
	if cfg.Refresh < minRefreshInterval {
		return options{}, reportErr(out,
			fmt.Errorf("intervalle de rafraîchissement invalide : %s (minimum %s)", cfg.Refresh, minRefreshInterval))
	}
	return opts, nil
}

// reportErr écrit err sur out (comme le fait flag pour ses propres erreurs) puis
// la renvoie, afin que l'utilisateur voie le motif du refus avant l'os.Exit(2).
func reportErr(out io.Writer, err error) error {
	_, _ = fmt.Fprintln(out, err)
	return err
}
