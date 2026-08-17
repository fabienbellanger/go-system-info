package main

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFlags(t *testing.T) {
	t.Run("valeurs par défaut", func(t *testing.T) {
		cfg, err := parseFlags("test", nil, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.Port != 8222 {
			t.Errorf("Port = %d, attendu 8222", cfg.Port)
		}
		if cfg.Refresh != 3*time.Second {
			t.Errorf("Refresh = %v, attendu 3s", cfg.Refresh)
		}
	})

	t.Run("valeurs personnalisées", func(t *testing.T) {
		cfg, err := parseFlags("test", []string{"-p", "9090", "-r", "10s", "-d", "/data"}, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.Port != 9090 {
			t.Errorf("Port = %d, attendu 9090", cfg.Port)
		}
		if cfg.Refresh != 10*time.Second {
			t.Errorf("Refresh = %v, attendu 10s", cfg.Refresh)
		}
		if cfg.DiskPath != "/data" {
			t.Errorf("DiskPath = %q, attendu \"/data\"", cfg.DiskPath)
		}
	})

	t.Run("hôte d'écoute", func(t *testing.T) {
		cfg, err := parseFlags("test", []string{"-host", "127.0.0.1"}, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.Host != "127.0.0.1" {
			t.Errorf("Host = %q, attendu \"127.0.0.1\"", cfg.Host)
		}
	})

	t.Run("fichier de journal", func(t *testing.T) {
		cfg, err := parseFlags("test", nil, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.LogPath != "" {
			t.Errorf("LogPath = %q, attendu vide par défaut (sortie d'erreur)", cfg.LogPath)
		}

		cfg, err = parseFlags("test", []string{"-log", "/var/log/app.log"}, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.LogPath != "/var/log/app.log" {
			t.Errorf("LogPath = %q, attendu \"/var/log/app.log\"", cfg.LogPath)
		}
	})

	t.Run("port hors bornes rejeté", func(t *testing.T) {
		for _, p := range []string{"0", "-1", "70000"} {
			if _, err := parseFlags("test", []string{"-p", p}, io.Discard); err == nil {
				t.Errorf("port %q : attendu une erreur", p)
			}
		}
	})

	t.Run("intervalle nul ou trop court rejeté", func(t *testing.T) {
		for _, r := range []string{"0s", "-5s", "10ms"} {
			if _, err := parseFlags("test", []string{"-r", r}, io.Discard); err == nil {
				t.Errorf("intervalle %q : attendu une erreur", r)
			}
		}
	})

	t.Run("intervalle au plancher accepté", func(t *testing.T) {
		cfg, err := parseFlags("test", []string{"-r", "250ms"}, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.Refresh != minRefreshInterval {
			t.Errorf("Refresh = %v, attendu %v", cfg.Refresh, minRefreshInterval)
		}
	})

	t.Run("port non numérique", func(t *testing.T) {
		if _, err := parseFlags("test", []string{"-p", "abc"}, io.Discard); err == nil {
			t.Error("attendu une erreur pour un port non numérique")
		}
	})

	t.Run("flag inconnu", func(t *testing.T) {
		if _, err := parseFlags("test", []string{"-inconnu"}, io.Discard); err == nil {
			t.Error("attendu une erreur pour un flag inconnu")
		}
	})
}

func TestSetupFileLogging(t *testing.T) {
	t.Run("journalise dans le fichier", func(t *testing.T) {
		restoreLogOutput(t)
		path := filepath.Join(t.TempDir(), "logs", "app.log")

		closeLog := setupFileLogging(path)
		slog.Info("message de test")
		closeLog()

		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("lecture du journal : %v", err)
		}
		if got := string(b); !strings.Contains(got, "message de test") {
			t.Errorf("journal = %q, il devrait contenir le message", got)
		}
	})

	t.Run("repli sur la sortie d'erreur si le fichier est inaccessible", func(t *testing.T) {
		restoreLogOutput(t)
		// Un composant du chemin est un fichier : la création du répertoire échoue.
		barrier := filepath.Join(t.TempDir(), "fichier")
		if err := os.WriteFile(barrier, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		log.SetOutput(io.Discard) // l'avertissement attendu ne pollue pas la sortie du test

		closeLog := setupFileLogging(filepath.Join(barrier, "app.log"))
		defer closeLog()

		// Le service doit continuer de tourner : pas de panique, et la
		// journalisation reste utilisable.
		slog.Info("message après repli")
	})
}

// restoreLogOutput rétablit la sortie du logger standard à la fin du test, que
// setupFileLogging modifie globalement.
func restoreLogOutput(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
}
