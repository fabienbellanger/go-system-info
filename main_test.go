package main

import (
	"io"
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
