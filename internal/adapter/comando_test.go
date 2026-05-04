package adapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rflpazini/kvasir/internal/adapter"
)

// TestComando_MagnetUnsupported locks the per-source contract: comando
// returns ErrMagnetUnsupported so the handler returns 404 and the
// frontend hides the magnet button. If a future contributor stubs
// Magnet to "" + nil error, the handler quietly serves "200 {magnet:''}"
// — this test catches that.
func TestComando_MagnetUnsupported(t *testing.T) {
	c := adapter.NewComando(nil)
	if _, err := c.Magnet(context.Background(), "https://comando.la/x"); !errors.Is(err, adapter.ErrMagnetUnsupported) {
		t.Errorf("err = %v, want ErrMagnetUnsupported", err)
	}
}

func TestComando_ParseSearch_Interstellar(t *testing.T) {
	html := loadFixture(t, "comando", "search_interstellar.html")

	results, err := adapter.ParseComando(html)
	if err != nil {
		t.Fatalf("ParseComando returned error: %v", err)
	}

	if got := len(results); got < 3 {
		t.Fatalf("expected at least 3 results from interstellar fixture, got %d", got)
	}

	// All results must be properly tagged.
	for i, r := range results {
		if r.Source != "comando" {
			t.Errorf("results[%d].Source = %q, want %q", i, r.Source, "comando")
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("results[%d].Title is empty", i)
		}
		if !strings.HasPrefix(r.DetailURL, "https://comando.la/") {
			t.Errorf("results[%d].DetailURL = %q, want https://comando.la/ prefix", i, r.DetailURL)
		}
	}

	// At least one result mentions Interestelar (PT-BR title).
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

func TestComando_ParseSearch_MultiResult(t *testing.T) {
	html := loadFixture(t, "comando", "search_matrix.html")

	results, err := adapter.ParseComando(html)
	if err != nil {
		t.Fatalf("ParseComando returned error: %v", err)
	}

	// matrix fixture has 12 article tags; parser must extract every one.
	if got := len(results); got < 8 {
		t.Fatalf("expected 8+ results from matrix fixture, got %d", got)
	}
}

func TestComando_ParseSearch_NoResults(t *testing.T) {
	results, err := adapter.ParseComando([]byte(`<html><body></body></html>`))
	if err != nil {
		t.Fatalf("ParseComando returned error on empty input: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestComando_IgnoresWidgetTitles(t *testing.T) {
	// The site renders sidebar widgets with <h2 class="widget-title">.
	// The parser must NOT pick those up — only article-scoped entry-titles.
	html := []byte(`
<html><body>
  <aside><h2 class="widget-title">Top Filmes</h2></aside>
  <article class="blog-view">
    <h2 class="entry-title"><a href="https://comando.la/legit-1/">Legit One</a></h2>
  </article>
  <h2 class="widget-title">Pesquisar</h2>
</body></html>`)
	results, err := adapter.ParseComando(html)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Legit One" {
		t.Errorf("expected exactly one 'Legit One' result, got %+v", results)
	}
}
