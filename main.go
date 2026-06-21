package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"time"

	"gosysteminfo/internal/server"
)

// publicFS embarque l'interface web dans le binaire.
//
//go:embed public
var publicFS embed.FS

func main() {
	cfg := server.Config{}
	flag.IntVar(&cfg.Port, "p", 8080, "Port d'écoute du serveur HTTP")
	flag.DurationVar(&cfg.Refresh, "r", 3*time.Second,
		"Intervalle de rafraîchissement de l'interface (ex. 5s, 30s, 1m)")
	flag.Parse()

	// Le contenu statique est servi depuis le sous-dossier "public" embarqué.
	static, err := fs.Sub(publicFS, "public")
	if err != nil {
		log.Fatal(err)
	}
	cfg.Static = static

	if err := server.New(cfg).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
