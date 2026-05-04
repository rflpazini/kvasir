package adapter_test

import (
	"strings"
	"testing"

	"github.com/rflpazini/kvasir/internal/adapter"
)

func TestTorrentDosFilmes_ParseSearch_Interstellar(t *testing.T) {
	html := loadFixture(t, "torrentdosfilmes", "search_interstellar.html")

	results, err := adapter.ParseTorrentDosFilmes(html)
	if err != nil {
		t.Fatalf("ParseTorrentDosFilmes returned error: %v", err)
	}

	if got := len(results); got < 3 {
		t.Fatalf("expected at least 3 results, got %d", got)
	}

	for i, r := range results {
		if r.Source != "torrentdosfilmes" {
			t.Errorf("results[%d].Source = %q, want %q", i, r.Source, "torrentdosfilmes")
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("results[%d].Title is empty", i)
		}
		if !strings.HasPrefix(r.DetailURL, "https://torrentdosfilmes") {
			t.Errorf("results[%d].DetailURL = %q, want torrentdosfilmes prefix", i, r.DetailURL)
		}
	}

	// At least one PT-BR-titled Interestelar result must be present.
	found := false
	for _, r := range results {
		if strings.Contains(strings.ToLower(r.Title), "interestelar") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no result mentioning Interestelar in %d results", len(results))
	}
}

func TestTorrentDosFilmes_ParseSearch_Matrix(t *testing.T) {
	html := loadFixture(t, "torrentdosfilmes", "search_matrix.html")

	results, err := adapter.ParseTorrentDosFilmes(html)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(results) < 5 {
		t.Fatalf("expected 5+ results from matrix fixture (8 .post blocks), got %d", len(results))
	}
}

func TestTorrentDosFilmes_ParseSearch_NoResults(t *testing.T) {
	results, err := adapter.ParseTorrentDosFilmes([]byte(`<html><body></body></html>`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestTorrentDosFilmes_HTMLEntitiesDecoded(t *testing.T) {
	// The site uses HTML entities (e.g. &#8211; for en-dash) in titles.
	// goquery's .Text() must decode these for us.
	html := []byte(`
<html><body>
  <div class="post">
    <div class="title">
      <a href="https://torrentdosfilmes-v2.xyz/foo/" title="Foo">Foo &#8211; Bar</a>
    </div>
  </div>
</body></html>`)
	results, err := adapter.ParseTorrentDosFilmes(html)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Title, "–") {
		t.Errorf("expected en-dash in decoded title, got %q", results[0].Title)
	}
}
