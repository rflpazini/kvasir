package adapter

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// fetchHTML issues a GET, sets the standard browser-emulation headers all
// our direct-HTTP adapters use, checks for a 200 status, and returns the
// fully read response body. The name argument prefixes errors so the
// resulting message reads "boitorrent: read body: ..." or
// "torrentdosfilmes: unexpected status 503" depending on the caller.
//
// FlareSolverr-fronted adapters (comando.la) bypass this helper because
// they need the browser-context plumbing of the solver.
func fetchHTML(ctx context.Context, client *http.Client, url, userAgent, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", name, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: http error: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: unexpected status %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", name, err)
	}
	return body, nil
}
