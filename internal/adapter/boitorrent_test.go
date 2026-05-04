package adapter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rflpazini/kvasir/internal/adapter"
	"github.com/rflpazini/kvasir/internal/model"
)

func TestBoitorrent_ParseSearch_Interstellar(t *testing.T) {
	html := loadFixture(t, "boitorrent", "search_interstellar.html")

	results, err := adapter.ParseBoitorrent(html)
	if err != nil {
		t.Fatalf("ParseBoitorrent returned error: %v", err)
	}

	if got := len(results); got < 5 {
		t.Fatalf("expected at least 5 results, got %d", got)
	}

	// Every result must carry the source tag and a non-empty title and URL.
	for i, r := range results {
		if r.Source != "boitorrent" {
			t.Errorf("results[%d].Source = %q, want %q", i, r.Source, "boitorrent")
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("results[%d].Title is empty", i)
		}
		if !strings.HasPrefix(r.DetailURL, "https://boitorrent.com/") {
			t.Errorf("results[%d].DetailURL = %q, want prefix https://boitorrent.com/", i, r.DetailURL)
		}
	}

	// At least one result for "interstellar" should mention the title (the
	// site uses both "interestelar" and "interstellar" forms).
	found := false
	for _, r := range results {
		title := strings.ToLower(r.Title)
		if strings.Contains(title, "interestelar") || strings.Contains(title, "interstellar") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one result mentioning interestelar/interstellar; got %d results", len(results))
	}
}

func TestBoitorrent_ParseSearch_SingleResult(t *testing.T) {
	html := loadFixture(t, "boitorrent", "search_matrix_1080p.html")

	results, err := adapter.ParseBoitorrent(html)
	if err != nil {
		t.Fatalf("ParseBoitorrent returned error: %v", err)
	}

	if got := len(results); got != 1 {
		t.Fatalf("expected exactly 1 result for matrix 1080p fixture, got %d", got)
	}

	r := results[0]
	if !strings.Contains(strings.ToLower(r.Title), "matrix") {
		t.Errorf("result title %q does not mention matrix", r.Title)
	}
	if r.DetailURL != "https://boitorrent.com/arquivo-matrix-tri-audio-torrent" {
		t.Errorf("DetailURL = %q, want canonical detail URL", r.DetailURL)
	}
	if r.Source != "boitorrent" {
		t.Errorf("Source = %q, want %q", r.Source, "boitorrent")
	}
	// Fixture title lists both 4K and 1080P; 4K wins per precedence rule.
	if r.Quality != model.Quality4K {
		t.Errorf("Quality = %q, want %q (title carries 4K marker)", r.Quality, model.Quality4K)
	}
}

func TestBoitorrent_ParseSearch_NoResults(t *testing.T) {
	// Empty HTML must not panic and must return zero results without error.
	results, err := adapter.ParseBoitorrent([]byte(`<html><body></body></html>`))
	if err != nil {
		t.Fatalf("ParseBoitorrent returned error on empty input: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestBoitorrent_Result_Shape(t *testing.T) {
	// Sanity check that the returned slice element is the canonical model.
	var _ []model.Result = mustParse(t, "boitorrent", "search_matrix_1080p.html")
}

// helpers

func loadFixture(t *testing.T, adapterName, file string) []byte {
	t.Helper()
	path := filepath.Join("testdata", adapterName, file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("loadFixture(%q): %v", path, err)
	}
	return data
}

func mustParse(t *testing.T, adapterName, file string) []model.Result {
	t.Helper()
	html := loadFixture(t, adapterName, file)
	results, err := adapter.ParseBoitorrent(html)
	if err != nil {
		t.Fatalf("mustParse: %v", err)
	}
	return results
}
