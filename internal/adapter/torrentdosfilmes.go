package adapter

import (
	"bytes"
	"context"
	"encoding/xml"
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

	body, err := fetchHTML(ctx, t.client, u.String(), tdfUA, tdfName)
	if err != nil {
		return nil, err
	}
	return ParseTorrentDosFilmes(body)
}

// Recent implements Adapter — fetches the WordPress RSS feed at /feed/
// and parses items into normalized Results. RSS is preferred over HTML
// scraping because the schema is stable.
func (t *TorrentDosFilmes) Recent(ctx context.Context) ([]model.Result, error) {
	u := tdfBaseURL + "/feed/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: build feed request: %w", err)
	}
	req.Header.Set("User-Agent", tdfUA)
	req.Header.Set("Accept", "application/rss+xml,application/xml;q=0.9,*/*;q=0.5")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: feed http error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrentdosfilmes: feed status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: read feed: %w", err)
	}
	return ParseTorrentDosFilmesFeed(body)
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
			Quality:   model.ParseQuality(title),
			DetailURL: href,
		})
	})

	return out, nil
}

// rssChannel is the minimal subset of WordPress RSS 2.0 we care about.
// `media:content` lives in the http://search.yahoo.com/mrss/ namespace;
// Go's encoding/xml treats it as the local name "content" with that
// namespace, but in practice WordPress emits the prefixed form so the
// "media content" tuple in the struct tag matches.
type rssChannel struct {
	XMLName xml.Name `xml:"rss"`
	Items   []struct {
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		PubDate string `xml:"pubDate"`
		Media   []struct {
			URL    string `xml:"url,attr"`
			Medium string `xml:"medium,attr"`
		} `xml:"http://search.yahoo.com/mrss/ content"`
	} `xml:"channel>item"`
}

// ParseTorrentDosFilmesFeed extracts items from the WordPress RSS feed
// returned by /feed/. PubDate parsing is best-effort (RFC 1123 / RFC 822);
// failures leave PublishedAt nil so the rest of the result still renders.
func ParseTorrentDosFilmesFeed(xmlBytes []byte) ([]model.Result, error) {
	var ch rssChannel
	if err := xml.Unmarshal(xmlBytes, &ch); err != nil {
		return nil, fmt.Errorf("torrentdosfilmes: parse feed: %w", err)
	}
	out := make([]model.Result, 0, len(ch.Items))
	for _, it := range ch.Items {
		title := strings.TrimSpace(it.Title)
		link := strings.TrimSpace(it.Link)
		if title == "" || link == "" {
			continue
		}
		r := model.Result{
			Title:     title,
			Source:    tdfName,
			Quality:   model.ParseQuality(title),
			DetailURL: link,
		}
		// First image-type media:content entry wins. WordPress sometimes
		// emits multiple (thumbnail + full size); prefer the first.
		for _, m := range it.Media {
			if m.URL == "" {
				continue
			}
			if m.Medium == "" || m.Medium == "image" {
				r.PosterURL = m.URL
				break
			}
		}
		if t, err := parseRSSDate(it.PubDate); err == nil {
			r.PublishedAt = &t
		}
		out = append(out, r)
	}
	return out, nil
}

// parseRSSDate accepts the date formats WordPress emits in pubDate. Both
// RFC 1123 and RFC 1123Z appear in the wild depending on the timezone used.
func parseRSSDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty pubDate")
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized pubDate: %q", raw)
}
