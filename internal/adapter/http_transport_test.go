package adapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/flaresolverr"
)

// captureTransport routes every adapter HTTP request to a test handler so we
// can exercise the Search/HealthCheck plumbing without hitting the live site.
type captureTransport struct {
	handler http.HandlerFunc
	calls   int
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls++
	rec := httptest.NewRecorder()
	c.handler(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func TestBoitorrent_SearchEndToEnd(t *testing.T) {
	html := loadFixture(t, "boitorrent", "search_interstellar.html")
	tr := &captureTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("campo1") == "" {
			t.Errorf("missing campo1 query param: %s", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write(html)
	}}

	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	results, err := a.Search(context.Background(), "interstellar")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results parsed end-to-end")
	}
	if a.Name() != "boitorrent" {
		t.Errorf("name: %s", a.Name())
	}
}

func TestBoitorrent_SearchEmptyQuery(t *testing.T) {
	a := adapter.NewBoitorrent(nil)
	if _, err := a.Search(context.Background(), "   "); err == nil {
		t.Error("expected error on empty query")
	}
}

func TestBoitorrent_SearchUpstream500(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}}
	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	if _, err := a.Search(context.Background(), "x"); err == nil {
		t.Error("expected error on upstream 500")
	}
}

func TestBoitorrent_HealthCheck(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		w.WriteHeader(200)
	}}
	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("health: %v", err)
	}
}

func TestBoitorrent_HealthCheck5xx(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}}
	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Error("expected error on 5xx")
	}
}

func TestTorrentDosFilmes_SearchEndToEnd(t *testing.T) {
	html := loadFixture(t, "torrentdosfilmes", "search_interstellar.html")
	tr := &captureTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("s") == "" {
			t.Errorf("missing s param: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write(html)
	}}
	a := adapter.NewTorrentDosFilmes(&http.Client{Transport: tr})
	results, err := a.Search(context.Background(), "x")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if a.Name() != "torrentdosfilmes" {
		t.Errorf("name: %s", a.Name())
	}
}

func TestTorrentDosFilmes_SearchEmptyQuery(t *testing.T) {
	a := adapter.NewTorrentDosFilmes(nil)
	if _, err := a.Search(context.Background(), ""); err == nil {
		t.Error("expected error on empty query")
	}
}

func TestTorrentDosFilmes_HealthCheck(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }}
	a := adapter.NewTorrentDosFilmes(&http.Client{Transport: tr})
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("health: %v", err)
	}
}

func TestComando_SearchEndToEnd(t *testing.T) {
	html := loadFixture(t, "comando", "search_interstellar.html")
	// FlareSolverr stub: minimal session-create + request.get pipeline that
	// returns the captured fixture as the solved page response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := readJSON(r)
		switch body["cmd"] {
		case "sessions.create":
			w.Write([]byte(`{"status":"ok","session":"s1"}`))
		case "request.get":
			w.Write([]byte(`{"status":"ok","solution":{"status":200,"response":` + jsonString(string(html)) + `}}`))
		}
	}))
	defer srv.Close()

	solver := flaresolverr.New(srv.URL, nil)
	a := adapter.NewComando(solver)
	results, err := a.Search(context.Background(), "interstellar")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results parsed end-to-end")
	}
	if a.Name() != "comando" {
		t.Errorf("name: %s", a.Name())
	}
}

func TestComando_SearchWithoutSolverFails(t *testing.T) {
	a := adapter.NewComando(nil)
	if _, err := a.Search(context.Background(), "x"); err == nil {
		t.Error("expected error when solver is nil")
	}
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Error("expected health error when solver is nil")
	}
}

func TestComando_SearchEmptyQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	a := adapter.NewComando(flaresolverr.New(srv.URL, nil))
	if _, err := a.Search(context.Background(), ""); err == nil {
		t.Error("expected error on empty query")
	}
}

func TestRegistry_Get(t *testing.T) {
	r := adapter.NewRegistry()
	a := adapter.NewBoitorrent(nil)
	r.Register(a)

	got, ok := r.Get("boitorrent")
	if !ok || got.Name() != "boitorrent" {
		t.Errorf("Get failed: ok=%v got=%v", ok, got)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get returned ok for missing adapter")
	}
}

func TestRegistry_DuplicateRegisterPanics(t *testing.T) {
	r := adapter.NewRegistry()
	r.Register(adapter.NewBoitorrent(nil))

	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()
	r.Register(adapter.NewBoitorrent(nil))
}
