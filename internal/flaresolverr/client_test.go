package flaresolverr_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rflpazini/kvasir/internal/flaresolverr"
)

// readReq decodes the JSON body of an incoming FlareSolverr request.
func readReq(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return m
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestFlareSolverr_FetchHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s-123"})
		case "request.get":
			if req["session"] != "s-123" {
				t.Errorf("expected session=s-123, got %v", req["session"])
			}
			if req["url"] != "https://example.com/" {
				t.Errorf("url = %v", req["url"])
			}
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{
					"status":   200,
					"url":      "https://example.com/",
					"response": "<html>hello</html>",
				},
			})
		default:
			t.Errorf("unexpected cmd: %v", req["cmd"])
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	body, err := c.Fetch(context.Background(), "https://example.com/", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(body) != "<html>hello</html>" {
		t.Errorf("body = %q", body)
	}
}

func TestFlareSolverr_SessionReuse(t *testing.T) {
	var sessionCalls atomic.Int32
	var fetchCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			sessionCalls.Add(1)
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s-once"})
		case "request.get":
			fetchCalls.Add(1)
			if req["session"] != "s-once" {
				t.Errorf("session reuse broken: got %v", req["session"])
			}
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{"status": 200, "response": "<p>ok</p>"},
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	for i := 0; i < 5; i++ {
		if _, err := c.Fetch(context.Background(), "https://example.com/", 0); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}

	if got := sessionCalls.Load(); got != 1 {
		t.Errorf("session creates = %d, want 1 (reused)", got)
	}
	if got := fetchCalls.Load(); got != 5 {
		t.Errorf("fetches = %d, want 5", got)
	}
}

func TestFlareSolverr_FallbackToSessionLessOnSessionCreateFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			// Simulate FlareSolverr returning failure on session creation.
			writeJSON(t, w, map[string]any{"status": "error", "message": "no browser"})
		case "request.get":
			if _, has := req["session"]; has {
				t.Errorf("expected session-less fallback, got session=%v", req["session"])
			}
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{"status": 200, "response": "ok"},
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	body, err := c.Fetch(context.Background(), "https://example.com/", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
}

func TestFlareSolverr_StaleSessionRecovery(t *testing.T) {
	// FlareSolverr emits "Session not found: <id>" when the browser context
	// expires server-side (see upstream sessions.py — SessionsService.get).
	// The client must invalidate the cached session AND retry the Fetch
	// once with a fresh one so the caller sees a single successful return,
	// not a failure followed by recovery on the next call.
	var sessionCreates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			n := sessionCreates.Add(1)
			writeJSON(t, w, map[string]any{"status": "ok", "session": fmt.Sprintf("s-%d", n)})
		case "request.get":
			// First session ID is dead — every request keyed on s-1 fails.
			// The second (and onwards) creates ok.
			if req["session"] == "s-1" {
				writeJSON(t, w, map[string]any{
					"status":  "error",
					"message": "Session not found: s-1",
				})
				return
			}
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{"status": 200, "response": "recovered"},
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	body, err := c.Fetch(context.Background(), "https://x/", 0)
	if err != nil {
		t.Fatalf("fetch must recover internally: %v", err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q, want recovered", body)
	}
	if got := sessionCreates.Load(); got != 2 {
		t.Errorf("session creates = %d, want 2 (recreated after stale)", got)
	}
}

// TestFlareSolverr_UnrelatedSessionMessageDoesNotInvalidate guards against
// the regression where a loose substring match on "session" would clear
// a healthy cached ID on any error that happened to mention the word.
func TestFlareSolverr_UnrelatedSessionMessageDoesNotInvalidate(t *testing.T) {
	var sessionCreates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			sessionCreates.Add(1)
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s-once"})
		case "request.get":
			writeJSON(t, w, map[string]any{
				"status":  "error",
				"message": "Session timeout exceeded waiting for cloudflare",
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	if _, err := c.Fetch(context.Background(), "https://x/", 0); err == nil {
		t.Fatal("expected timeout error to surface")
	}
	if _, err := c.Fetch(context.Background(), "https://x/", 0); err == nil {
		t.Fatal("expected timeout error to surface again")
	}
	if got := sessionCreates.Load(); got != 1 {
		t.Errorf("session creates = %d, want 1 (must NOT invalidate on unrelated error)", got)
	}
}

func TestFlareSolverr_UpstreamSolveFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s"})
		case "request.get":
			writeJSON(t, w, map[string]any{
				"status":  "error",
				"message": "Cloudflare challenge timed out",
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	_, err := c.Fetch(context.Background(), "https://blocked.example/", 0)
	if err == nil {
		t.Fatal("expected error from solve failure, got nil")
	}
	if !strings.Contains(err.Error(), "Cloudflare") {
		t.Errorf("error should surface upstream message, got: %v", err)
	}
}

func TestFlareSolverr_SolutionStatus4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s"})
		case "request.get":
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{"status": 403, "response": "<html>forbidden</html>"},
			})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	_, err := c.Fetch(context.Background(), "https://x/", 0)
	if err == nil {
		t.Fatal("expected error on solved-page 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got: %v", err)
	}
}

func TestFlareSolverr_HealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Real FlareSolverr returns 405 on GET /v1; we accept anything <500.
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	if err := c.Health(context.Background()); err != nil {
		t.Errorf("health: %v", err)
	}
}

func TestFlareSolverr_Health5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	if err := c.Health(context.Background()); err == nil {
		t.Error("expected health error on 500, got nil")
	}
}

func TestFlareSolverr_NilClientGuard(t *testing.T) {
	var c *flaresolverr.Client
	if _, err := c.Fetch(context.Background(), "https://x/", 0); err == nil {
		t.Error("expected error from nil-client Fetch")
	}
	if err := c.Health(context.Background()); err == nil {
		t.Error("expected error from nil-client Health")
	}
}

func TestFlareSolverr_CloseSession(t *testing.T) {
	var destroyed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := readReq(t, r)
		switch req["cmd"] {
		case "sessions.create":
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s-to-close"})
		case "request.get":
			writeJSON(t, w, map[string]any{
				"status": "ok",
				"solution": map[string]any{"status": 200, "response": ""},
			})
		case "sessions.destroy":
			if req["session"] != "s-to-close" {
				t.Errorf("destroy session = %v, want s-to-close", req["session"])
			}
			destroyed.Add(1)
			writeJSON(t, w, map[string]any{"status": "ok"})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)
	if _, err := c.Fetch(context.Background(), "https://x/", 0); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	c.CloseSession(context.Background())

	if got := destroyed.Load(); got != 1 {
		t.Errorf("destroy calls = %d, want 1", got)
	}

	// Calling CloseSession again is a no-op (no cached session).
	c.CloseSession(context.Background())
	if got := destroyed.Load(); got != 1 {
		t.Errorf("destroy calls after second close = %d, want still 1", got)
	}
}

func TestFlareSolverr_RespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the response until client cancels.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			writeJSON(t, w, map[string]any{"status": "ok", "session": "s"})
		}
	}))
	defer srv.Close()

	c := flaresolverr.New(srv.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Fetch(ctx, "https://x/", 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error on context timeout")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("did not cancel promptly: %v", elapsed)
	}
}
