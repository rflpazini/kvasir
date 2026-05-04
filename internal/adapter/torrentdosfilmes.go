package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/rflpazini/kvasir/internal/model"
)

const (
	tdfName    = "torrentdosfilmes"
	tdfBaseURL = "https://torrentdosfilmes.se"
	tdfUA      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/124.0.0.0 Safari/537.36"
)

// TorrentDosFilmes is the adapter for torrentdosfilmes.se. The site issues a
// 301 to a sibling .xyz domain on every request; the standard http.Client
// follows it transparently, so the adapter does nothing special — but we keep
// queries pointed at the canonical .se entrypoint so the redirect history is
// auditable in logs.
type TorrentDosFilmes struct {
	client *http.Client
}

// NewTorrentDosFilmes builds the adapter with sensible HTTP defaults.
func NewTorrentDosFilmes(client *http.Client) *TorrentDosFilmes {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &TorrentDosFilmes{client: client}
}

// Name implements Adapter.
func (t *TorrentDosFilmes) Name() string { return tdfName }

// Search implements Adapter.
func (t *TorrentDosFilmes) Search(ctx context.Context, query string) ([]model.Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("torrentdosfilmes: empty query")
	}

	u, _ := url.Parse(tdfBaseURL + "/")
	params := url.Values{}
	params.Set("s", q)
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: build request: %w", err)
	}
	req.Header.Set("User-Agent", tdfUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: http error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrentdosfilmes: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: read body: %w", err)
	}
	return ParseTorrentDosFilmes(body)
}

// HealthCheck implements Adapter.
func (t *TorrentDosFilmes) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, tdfBaseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", tdfUA)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("torrentdosfilmes: healthcheck status %d", resp.StatusCode)
	}
	return nil
}

// ParseTorrentDosFilmes extracts normalized results from a search HTML payload.
//
// Markup contract: each result is a <div class="post ..."> (sometimes with a
// trailing color class like "green") containing <div class="title"><a>...</a>.
// goquery's Text() handles HTML entity decoding (eg &#8211; → en-dash).
func ParseTorrentDosFilmes(htmlBytes []byte) ([]model.Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: parse html: %w", err)
	}

	var out []model.Result

	doc.Find("div.post .title a").Each(func(_ int, s *goquery.Selection) {
		title := strings.TrimSpace(s.Text())
		href, _ := s.Attr("href")
		if title == "" || href == "" {
			return
		}
		out = append(out, model.Result{
			Title:     title,
			Source:    tdfName,
			DetailURL: href,
		})
	})

	return out, nil
}
