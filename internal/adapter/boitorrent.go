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
	boitorrentName    = "boitorrent"
	boitorrentBaseURL = "https://boitorrent.com"
	// Realistic UA. Identifying as a bot to a Cloudflare-fronted site invites
	// an immediate block, see plan section 4 / Risks.
	boitorrentUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/124.0.0.0 Safari/537.36"
)

// Boitorrent is the adapter implementation for boitorrent.com.
//
// The site uses server-rendered HTML with infinite scroll for pagination, but
// the first page is enough for the homelab MVP — we expose only the static
// portion. Magnet links and seeders/leechers live on the per-item detail page
// and are not fetched in Phase 1; the DetailURL field carries the user there.
type Boitorrent struct {
	client *http.Client
}

// NewBoitorrent creates the adapter with sensible defaults. Pass a custom
// http.Client to inject FlareSolverr or test transports.
func NewBoitorrent(client *http.Client) *Boitorrent {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &Boitorrent{client: client}
}

// Name implements Adapter.
func (b *Boitorrent) Name() string { return boitorrentName }

// Search implements Adapter.
func (b *Boitorrent) Search(ctx context.Context, query string) ([]model.Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("boitorrent: empty query")
	}

	u, _ := url.Parse(boitorrentBaseURL + "/index.php")
	params := url.Values{}
	params.Set("campo1", q)
	params.Set("nome_campo1", "pesquisa")
	params.Set("categoria", "lista")
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("boitorrent: build request: %w", err)
	}
	req.Header.Set("User-Agent", boitorrentUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("boitorrent: http error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("boitorrent: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("boitorrent: read body: %w", err)
	}

	return ParseBoitorrent(body)
}

// HealthCheck implements Adapter. Cheap HEAD probe on the homepage.
func (b *Boitorrent) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, boitorrentBaseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", boitorrentUA)

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("boitorrent: healthcheck status %d", resp.StatusCode)
	}
	return nil
}

// ParseBoitorrent extracts normalized results from a search-page HTML payload.
// Exported (and pure) so it can be exercised against golden fixtures without
// hitting the network.
func ParseBoitorrent(htmlBytes []byte) ([]model.Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, fmt.Errorf("boitorrent: parse html: %w", err)
	}

	var out []model.Result

	// Each search hit is wrapped in <div class="row semelhantes">. The
	// canonical title and detail URL live in the inner <h2><a> element.
	doc.Find("div.row.semelhantes").Each(func(_ int, s *goquery.Selection) {
		anchor := s.Find("h2 a").First()
		title := strings.TrimSpace(anchor.Text())
		href, _ := anchor.Attr("href")

		if title == "" || href == "" {
			return
		}

		out = append(out, model.Result{
			Title:     title,
			Source:    boitorrentName,
			DetailURL: href,
		})
	})

	return out, nil
}
