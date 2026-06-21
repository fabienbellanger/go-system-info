package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

// newTestServer construit un serveur avec un contenu statique factice.
func newTestServer(refresh time.Duration) *Server {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>OK</h1>")},
	}
	return New(Config{Port: 0, Refresh: refresh, Static: static})
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
