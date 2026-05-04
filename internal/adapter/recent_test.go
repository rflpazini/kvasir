package adapter_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/model"
)

func TestParseBoitorrentHome(t *testing.T) {
	html := loadFixture(t, "boitorrent", "home.html")

	results, err := adapter.ParseBoitorrentHome(html)
	if err != nil {
		t.Fatalf("ParseBoitorrentHome returned error: %v", err)
	}

	if got := len(results); got < 5 {
		t.Fatalf("expected at least 5 recent items, got %d", got)
	}

	for i, r := range results {
		if r.Source != "boitorrent" {
			t.Errorf("results[%d].Source = %q, want %q", i, r.Source, "boitorrent")
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("results[%d].Title is empty", i)
		}
		if !strings.HasPrefix(r.DetailURL, "https://boitorrent.com/") {
			t.Errorf("results[%d].DetailURL = %q, want boitorrent.com prefix", i, r.DetailURL)
		}
		if r.Quality == "" {
			t.Errorf("results[%d].Quality unset", i)
		}
	}

	// At least one item must carry a quality marker (4K or 1080p) since the
	// homepage typically lists current releases that include resolution info.
	hasQuality := false
	for _, r := range results {
		if r.Quality == model.Quality4K || r.Quality == model.Quality1080p {
			hasQuality = true
			break
		}
	}
	if !hasQuality {
		t.Errorf("expected at least one 4K or 1080p item among %d results", len(results))
	}
}

func TestParseBoitorrentHome_Empty(t *testing.T) {
	results, err := adapter.ParseBoitorrentHome([]byte("<html><body></body></html>"))
	if err != nil {
		t.Fatalf("error on empty html: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestParseTorrentDosFilmesFeed(t *testing.T) {
	xml := loadFixture(t, "torrentdosfilmes", "feed.xml")

	results, err := adapter.ParseTorrentDosFilmesFeed(xml)
	if err != nil {
		t.Fatalf("ParseTorrentDosFilmesFeed returned error: %v", err)
	}

	if got := len(results); got < 3 {
		t.Fatalf("expected at least 3 RSS items, got %d", got)
	}

	withDate := 0
	for i, r := range results {
		if r.Source != "torrentdosfilmes" {
			t.Errorf("results[%d].Source = %q", i, r.Source)
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("results[%d].Title empty", i)
		}
		if !strings.HasPrefix(r.DetailURL, "https://torrentdosfilmes") {
			t.Errorf("results[%d].DetailURL = %q", i, r.DetailURL)
		}
		if r.Quality == "" {
			t.Errorf("results[%d].Quality unset", i)
		}
		if r.PublishedAt != nil {
			withDate++
		}
	}

	// Most WordPress feeds emit RFC1123Z pubDate; we expect the majority to
	// parse successfully even if a malformed entry slips through.
	if withDate < len(results)/2 {
		t.Errorf("only %d/%d results have PublishedAt; pubDate parsing is broken", withDate, len(results))
	}
}

func TestParseTorrentDosFilmesFeed_MalformedXML(t *testing.T) {
	_, err := adapter.ParseTorrentDosFilmesFeed([]byte("<not><valid><rss>"))
	if err == nil {
		t.Error("expected error on malformed XML")
	}
}

func TestBoitorrent_RecentEndToEnd(t *testing.T) {
	html := loadFixture(t, "boitorrent", "home.html")
	tr := &captureTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("path = %q, want /", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write(html)
	}}
	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	results, err := a.Recent(context.Background())
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected recent results, got 0")
	}
}

func TestBoitorrent_RecentUpstream500(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}}
	a := adapter.NewBoitorrent(&http.Client{Transport: tr})
	if _, err := a.Recent(context.Background()); err == nil {
		t.Error("expected error on upstream 500")
	}
}

func TestTorrentDosFilmes_RecentEndToEnd(t *testing.T) {
	xml := loadFixture(t, "torrentdosfilmes", "feed.xml")
	tr := &captureTransport{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed/" {
			t.Errorf("path = %q, want /feed/", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(200)
		w.Write(xml)
	}}
	a := adapter.NewTorrentDosFilmes(&http.Client{Transport: tr})
	results, err := a.Recent(context.Background())
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected RSS items, got 0")
	}
}

func TestTorrentDosFilmes_RecentFeedStatus500(t *testing.T) {
	tr := &captureTransport{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}}
	a := adapter.NewTorrentDosFilmes(&http.Client{Transport: tr})
	if _, err := a.Recent(context.Background()); err == nil {
		t.Error("expected error on feed 500")
	}
}

func TestComando_RecentRequiresSolver(t *testing.T) {
	a := adapter.NewComando(nil)
	if _, err := a.Recent(context.Background()); err == nil {
		t.Error("expected error when solver is nil")
	}
}

func TestParseTorrentDosFilmesFeed_MissingFields(t *testing.T) {
	// item with empty title must be skipped, not panic
	xml := []byte(`<?xml version="1.0"?><rss version="2.0"><channel>
		<item><title></title><link>https://x/a</link></item>
		<item><title>Valid 1080p</title><link>https://x/b</link><pubDate>Mon, 04 May 2026 14:32:05 +0000</pubDate></item>
	</channel></rss>`)
	results, err := adapter.ParseTorrentDosFilmesFeed(xml)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 valid result, got %d", len(results))
	}
	if results[0].Quality != model.Quality1080p {
		t.Errorf("quality = %q, want 1080p", results[0].Quality)
	}
	if results[0].PublishedAt == nil {
		t.Error("PublishedAt nil despite valid pubDate")
	}
}
