package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// newTestServer construit un serveur avec un contenu statique factice.
func newTestServer(refresh time.Duration) *Server {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>OK</h1>")},
	}
	return New(Config{Port: 0, Refresh: refresh, Static: static, Version: "test-1.2.3"})
}

func TestHandleConfig(t *testing.T) {
	srv := newTestServer(30 * time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	var body struct {
		RefreshMS int64 `json:"refresh_ms"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	if body.RefreshMS != 30000 {
		t.Errorf("refresh_ms = %d, attendu 30000", body.RefreshMS)
	}
}

func TestHandleSystem(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}

	// Le corps doit être un JSON contenant les sections attendues.
	var body map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	for _, key := range []string{"timestamp", "host", "cpu", "memory", "disk"} {
		if _, ok := body[key]; !ok {
			t.Errorf("clé %q absente de la réponse", key)
		}
	}
}

func TestHandleHealth(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, attendu \"ok\"", body.Status)
	}
}

func TestHandleVersion(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	if body.Version != "test-1.2.3" {
		t.Errorf("version = %q, attendu \"test-1.2.3\"", body.Version)
	}
}

func TestHandleHistory(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}
	// Sans Start(), l'historique est vide mais doit rester un tableau JSON
	// valide (et non null).
	var body []struct {
		CPU float64 `json:"cpu"`
		Mem float64 `json:"mem"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	if body == nil {
		t.Error("réponse = null, attendu un tableau JSON")
	}
}

func TestHandleStream(t *testing.T) {
	srv := newTestServer(50 * time.Millisecond)

	// Un vrai serveur est nécessaire : httptest.NewRecorder n'implémente pas
	// le flush/streaming attendu par http.ResponseController.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("création requête : %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requête /api/stream : %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", res.StatusCode, http.StatusOK)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, attendu \"text/event-stream\"", ct)
	}

	// Lit le premier événement SSE (émis immédiatement) et vérifie sa forme.
	reader := bufio.NewReader(res.Body)
	var payload string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("lecture du flux : %v", err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			payload = strings.TrimSpace(data)
			break
		}
	}

	var state struct {
		System  map[string]json.RawMessage `json:"system"`
		History []map[string]float64       `json:"history"`
	}
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		t.Fatalf("JSON d'événement invalide : %v", err)
	}
	for _, key := range []string{"timestamp", "host", "cpu", "memory", "disk"} {
		if _, ok := state.System[key]; !ok {
			t.Errorf("clé %q absente de l'état système diffusé", key)
		}
	}
}

func TestServeStaticIndex(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "<h1>OK</h1>" {
		t.Errorf("corps = %q, attendu le contenu de index.html", got)
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/inexistant", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, attendu %d", rec.Code, http.StatusNotFound)
	}
}
