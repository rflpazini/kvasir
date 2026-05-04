// Package flaresolverr is a thin client for the FlareSolverr proxy
// (https://github.com/FlareSolverr/FlareSolverr). FlareSolverr fronts a real
// browser to solve Cloudflare interactive challenges and returns the rendered
// HTML, letting Go scrapers behind it bypass CF anti-bot pages without
// embedding Chrome themselves.
//
// Adapters use this client only when a direct HTTP fetch returns evidence of
// a CF challenge (status 403 with cf-mitigated header, or a body containing
// the Cloudflare interstitial markers).
package flaresolverr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client talks to FlareSolverr over its single /v1 JSON endpoint.
//
// A long-lived session is created lazily on the first request and reused for
// every subsequent fetch. FlareSolverr keeps the underlying browser context
// (and any solved Cloudflare cookies) tied to the session, so warm requests
// against the same host complete in 1–2s instead of re-solving the challenge
// every time (cold solves take 10–15s).
type Client struct {
	endpoint string
	http     *http.Client

	mu      sync.Mutex
	session string
}

// New creates a client. The endpoint must be the full /v1 URL, eg
// "http://flaresolverr:8191/v1". A nil http.Client falls back to a sensible
// default with a generous timeout (FlareSolverr can take ~10s on cold solves).
func New(endpoint string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 90 * time.Second}
	}
	return &Client{endpoint: endpoint, http: hc}
}

// Fetch issues a GET via FlareSolverr and returns the rendered HTML body.
// maxTimeoutMs is forwarded to FlareSolverr; pass 0 for the default 60s.
func (c *Client) Fetch(ctx context.Context, url string, maxTimeoutMs int) ([]byte, error) {
	if c == nil {
		return nil, errors.New("flaresolverr: nil client")
	}
	if maxTimeoutMs <= 0 {
		maxTimeoutMs = 60_000
	}

	session, err := c.ensureSession(ctx)
	if err != nil {
		// Session creation is an optimization; fall back to session-less
		// request rather than failing the search outright.
		session = ""
	}

	payload := map[string]any{
		"cmd":        "request.get",
		"url":        url,
		"maxTimeout": maxTimeoutMs,
	}
	if session != "" {
		payload["session"] = session
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: do: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flaresolverr: upstream status %d: %s", resp.StatusCode, snippet(raw))
	}

	var out struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status   int    `json:"status"`
			URL      string `json:"url"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("flaresolverr: decode: %w", err)
	}
	if out.Status != "ok" {
		// FlareSolverr drops sessions server-side when the headless browser
		// context expires; without invalidating our cached ID we'd loop on
		// the same dead session until the process restarts. Detect by
		// "session" appearing in the message and clear so the next Fetch
		// re-creates a fresh one.
		if strings.Contains(strings.ToLower(out.Message), "session") {
			c.invalidateSession(session)
		}
		return nil, fmt.Errorf("flaresolverr: %s: %s", out.Status, out.Message)
	}
	if out.Solution.Status >= 400 {
		return nil, fmt.Errorf("flaresolverr: solved page returned %d", out.Solution.Status)
	}
	return []byte(out.Solution.Response), nil
}

// Health returns nil if the FlareSolverr endpoint is reachable. Useful for /healthz.
func (c *Client) Health(ctx context.Context) error {
	if c == nil {
		return errors.New("flaresolverr: nil client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// FlareSolverr returns 405 on GET /v1 (only POST allowed). We accept
	// anything < 500 as proof the service is up.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("flaresolverr: status %d", resp.StatusCode)
	}
	return nil
}

// ensureSession returns the current session ID, creating one on first call.
// Concurrent callers share the same session; failure to create is non-fatal
// (Fetch falls back to a session-less request).
func (c *Client) ensureSession(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session != "" {
		return c.session, nil
	}

	body, _ := json.Marshal(map[string]any{"cmd": "sessions.create"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var out struct {
		Status  string `json:"status"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Status != "ok" || out.Session == "" {
		return "", fmt.Errorf("flaresolverr: session create returned %q", out.Status)
	}
	c.session = out.Session
	return c.session, nil
}

// invalidateSession drops the cached session ID, but only if it still
// matches `id` — guards against a concurrent caller having already
// rotated the session before we noticed it was stale.
func (c *Client) invalidateSession(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	if c.session == id {
		c.session = ""
	}
	c.mu.Unlock()
}

// CloseSession destroys the cached session on the FlareSolverr side. Best-effort.
func (c *Client) CloseSession(ctx context.Context) {
	c.mu.Lock()
	id := c.session
	c.session = ""
	c.mu.Unlock()
	if id == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"cmd": "sessions.destroy", "session": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

func snippet(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
