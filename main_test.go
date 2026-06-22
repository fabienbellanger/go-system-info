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
		cfg, err := parseFlags("test", []string{"-p", "9090", "-r", "10s"}, io.Discard)
		if err != nil {
			t.Fatalf("erreur inattendue : %v", err)
		}
		if cfg.Port != 9090 {
			t.Errorf("Port = %d, attendu 9090", cfg.Port)
		}
		if cfg.Refresh != 10*time.Second {
			t.Errorf("Refresh = %v, attendu 10s", cfg.Refresh)
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
