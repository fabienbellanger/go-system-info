package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gosysteminfo/internal/sysinfo"
)

// stubCollector est un collecteur factice pour les tests : il renvoie des
// valeurs prédéfinies sans interroger la machine.
type stubCollector struct {
	info    *sysinfo.Info
	err     error
	killErr error    // erreur renvoyée par Kill (nil = succès)
	killed  *[]int32 // PID effectivement passés à Kill (si non nil)
}

func (s stubCollector) Start(context.Context) {}

func (s stubCollector) Collect() (*sysinfo.Info, error) { return s.info, s.err }

func (s stubCollector) History() []sysinfo.HistorySample { return nil }

func (s stubCollector) Kill(pid int32) error {
	if s.killed != nil {
		*s.killed = append(*s.killed, pid)
	}
	return s.killErr
}

func (s stubCollector) Details(pids []int32) []sysinfo.ProcessDetail {
	details := make([]sysinfo.ProcessDetail, len(pids))
	for i, pid := range pids {
		details[i] = sysinfo.ProcessDetail{PID: pid}
	}
	return details
}

// newTestServer construit un serveur avec un contenu statique factice.
func newTestServer(refresh time.Duration) *Server {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>OK</h1>")},
	}
	// example.com est l'hôte par défaut des requêtes httptest.NewRequest : on le
	// déclare de confiance pour que la vérification d'en-tête Host (parade au DNS
	// rebinding) laisse passer ces requêtes de test. La vérification elle-même est
	// couverte par TestHostHeaderCheck.
	return New(Config{
		Port: 0, Refresh: refresh, Static: static, Version: "test-1.2.3",
		TrustedHosts: "example.com",
	})
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
		ReadOnly  bool  `json:"readonly"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("JSON invalide : %v", err)
	}
	if body.RefreshMS != 30000 {
		t.Errorf("refresh_ms = %d, attendu 30000", body.RefreshMS)
	}
	if body.ReadOnly {
		t.Error("readonly = true, attendu false pour un serveur de test non restreint")
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

func TestHandleSystemError(t *testing.T) {
	// Collecteur en échec injecté : handleSystem doit répondre 500.
	srv := &Server{
		cfg:       Config{Static: fstest.MapFS{}},
		collector: stubCollector{err: errors.New("collecte impossible")},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandleKill(t *testing.T) {
	t.Run("succès : transmet les PID au collecteur", func(t *testing.T) {
		var killed []int32
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{killed: &killed},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(`{"pids":[42,43]}`))
		req.Header.Set("Content-Type", "application/json")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
		}
		if len(killed) != 2 || killed[0] != 42 || killed[1] != 43 {
			t.Errorf("PID transmis = %v, attendu [42 43]", killed)
		}
		var body struct {
			Results []struct {
				PID int32 `json:"pid"`
				OK  bool  `json:"ok"`
			} `json:"results"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("décodage : %v", err)
		}
		if len(body.Results) != 2 || !body.Results[0].OK {
			t.Errorf("résultats inattendus : %+v", body.Results)
		}
	})

	t.Run("échec du collecteur : signalé par résultat", func(t *testing.T) {
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{killErr: errors.New("refusé")},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(`{"pids":[1]}`))
		req.Header.Set("Content-Type", "application/json")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
		}
		var body struct {
			Results []struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"results"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("décodage : %v", err)
		}
		if len(body.Results) != 1 || body.Results[0].OK || body.Results[0].Error == "" {
			t.Errorf("attendu un échec rapporté, obtenu %+v", body.Results)
		}
	})

	t.Run("corps vide : 400", func(t *testing.T) {
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(`{"pids":[]}`))
		req.Header.Set("Content-Type", "application/json")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("Content-Type non JSON refusé (anti-CSRF)", func(t *testing.T) {
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(`{"pids":[42]}`))
		req.Header.Set("Content-Type", "text/plain")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusUnsupportedMediaType)
		}
	})

	t.Run("requête cross-site refusée (anti-CSRF)", func(t *testing.T) {
		var killed []int32
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{killed: &killed},
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(`{"pids":[42]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "cross-site")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusForbidden)
		}
		if len(killed) != 0 {
			t.Errorf("aucun PID ne doit être terminé, obtenu %v", killed)
		}
	})

	t.Run("trop de PID refusés", func(t *testing.T) {
		srv := &Server{
			cfg:       Config{Static: fstest.MapFS{}},
			collector: stubCollector{},
		}
		var sb strings.Builder
		sb.WriteString(`{"pids":[`)
		for i := 0; i <= maxKillPIDs; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.Itoa(i + 1))
		}
		sb.WriteString(`]}`)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
			strings.NewReader(sb.String()))
		req.Header.Set("Content-Type", "application/json")

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("méthode GET refusée", func(t *testing.T) {
		srv := newTestServer(time.Second)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/processes/kill", nil)

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

func TestReadOnlyDisablesKill(t *testing.T) {
	var killed []int32
	srv := &Server{
		cfg:       Config{Static: fstest.MapFS{}, ReadOnly: true},
		collector: stubCollector{killed: &killed},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/processes/kill",
		strings.NewReader(`{"pids":[42]}`))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, attendu %d (mode lecture seule)", rec.Code, http.StatusForbidden)
	}
	if len(killed) != 0 {
		t.Errorf("aucun PID ne doit être terminé en lecture seule, obtenu %v", killed)
	}
}

func TestHostHeaderCheck(t *testing.T) {
	// Serveur construit via New SANS hôte de confiance supplémentaire : la
	// vérification d'en-tête Host est donc active. On cible /api/health, sans état.
	srv := New(Config{Refresh: time.Second, Static: fstest.MapFS{}})

	cases := []struct {
		host string
		want int
	}{
		{"localhost", http.StatusOK},
		{"localhost:8222", http.StatusOK},
		{"127.0.0.1:8222", http.StatusOK}, // IP littérale : jamais usurpable par rebinding
		{"[::1]:8222", http.StatusOK},
		{"evil.example.com", http.StatusForbidden},
		{"attaquant.tld:8222", http.StatusForbidden},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = tc.host
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Host %q : code = %d, attendu %d", tc.host, rec.Code, tc.want)
		}
	}
}

func TestHandleDetail(t *testing.T) {
	t.Run("renvoie les instances pour les PID demandés", func(t *testing.T) {
		srv := &Server{cfg: Config{Static: fstest.MapFS{}}, collector: stubCollector{}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/processes/detail?pids=10,20", nil)

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
		}
		var body struct {
			Instances []struct {
				PID int32 `json:"pid"`
			} `json:"instances"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("décodage : %v", err)
		}
		if len(body.Instances) != 2 || body.Instances[0].PID != 10 || body.Instances[1].PID != 20 {
			t.Errorf("instances inattendues : %+v", body.Instances)
		}
	})

	t.Run("pids manquant ou invalide : 400", func(t *testing.T) {
		srv := &Server{cfg: Config{Static: fstest.MapFS{}}, collector: stubCollector{}}
		for _, q := range []string{"", "?pids=", "?pids=abc"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/processes/detail"+q, nil)
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("query %q : code = %d, attendu %d", q, rec.Code, http.StatusBadRequest)
			}
		}
	})

	t.Run("au-delà de la borne : liste tronquée et signalée", func(t *testing.T) {
		srv := &Server{cfg: Config{Static: fstest.MapFS{}}, collector: stubCollector{}}
		ids := make([]string, maxDetailPIDs+10)
		for i := range ids {
			ids[i] = strconv.Itoa(i + 1)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/api/processes/detail?pids="+strings.Join(ids, ","), nil)

		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusOK)
		}
		var body struct {
			Instances []struct {
				PID int32 `json:"pid"`
			} `json:"instances"`
			Truncated bool `json:"truncated"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("décodage : %v", err)
		}
		if !body.Truncated {
			t.Error("truncated = false, attendu true")
		}
		if len(body.Instances) != maxDetailPIDs {
			t.Errorf("instances = %d, attendu %d", len(body.Instances), maxDetailPIDs)
		}
	})

	t.Run("dans la borne : pas de signal de troncature", func(t *testing.T) {
		srv := &Server{cfg: Config{Static: fstest.MapFS{}}, collector: stubCollector{}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/processes/detail?pids=10,20", nil)

		srv.Handler().ServeHTTP(rec, req)

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("décodage : %v", err)
		}
		if _, present := body["truncated"]; present {
			t.Error("truncated présent, attendu absent quand la liste tient dans la borne")
		}
	})
}

func TestParsePIDs(t *testing.T) {
	got := parsePIDs("10, 20 ,abc,-3,0,30")
	want := []int32{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("parsePIDs = %v, attendu %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parsePIDs[%d] = %d, attendu %d", i, got[i], want[i])
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
	defer func() { _ = res.Body.Close() }()

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

func TestHandleStreamCollectError(t *testing.T) {
	// Erreur de collecte dès le premier événement : handleStream doit se terminer
	// aussitôt (return), sans diffuser d'événement « data: ».
	srv := &Server{
		cfg:       Config{Refresh: 50 * time.Millisecond, Static: fstest.MapFS{}},
		collector: stubCollector{err: errors.New("collecte impossible")},
	}
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
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", res.StatusCode, http.StatusOK)
	}
	// Le handler retourne avant tout Write : le corps est vide et se clôt (EOF).
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("lecture du flux : %v", err)
	}
	if len(body) != 0 {
		t.Errorf("flux non vide malgré l'erreur de collecte : %q", body)
	}
}

func TestHandleStreamZeroRefreshFallback(t *testing.T) {
	// Server construit directement avec Refresh nul (hors parseFlags) : handleStream
	// doit retomber sur DefaultRefresh sans paniquer (NewTicker(0) paniquerait) et
	// émettre au moins le premier événement, immédiat.
	srv := &Server{
		cfg:       Config{Refresh: 0, Static: fstest.MapFS{}},
		collector: stubCollector{info: &sysinfo.Info{}},
	}
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
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu %d", res.StatusCode, http.StatusOK)
	}
	// Le premier événement (« data: … ») doit arriver malgré Refresh=0.
	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("aucun événement reçu avec Refresh=0 : %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return // premier événement émis : le repli a fonctionné
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
	// Les assets embarqués doivent être servis avec revalidation, sinon le
	// navigateur peut resservir un ancien bundle après reconstruction du binaire.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, attendu %q", cc, "no-cache")
	}
}

func TestMethodNotAllowedOnGetEndpoint(t *testing.T) {
	srv := newTestServer(time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, attendu %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("en-tête Allow = %q, attendu %q", allow, http.MethodGet)
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
