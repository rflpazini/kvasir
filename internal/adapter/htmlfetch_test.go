package adapter

// fetchHTML is unexported, so the test lives in the package itself
// rather than the _test package next door.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchHTML_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.Header.Get("User-Agent") != "test-ua" {
			t.Errorf("UA = %q, want test-ua", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Accept") == "" {
			t.Error("Accept header missing")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>ok</html>"))
	}))
	defer srv.Close()

	body, err := fetchHTML(context.Background(), srv.Client(), srv.URL, "test-ua", "tester")
	if err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if string(body) != "<html>ok</html>" {
		t.Errorf("body = %q", body)
	}
}

func TestFetchHTML_NameInError(t *testing.T) {
	// Error messages must carry the adapter name as a prefix so logs can
	// distinguish which scraper hit the failure without inspecting stack.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	_, err := fetchHTML(context.Background(), srv.Client(), srv.URL, "ua", "myadapter")
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if !strings.HasPrefix(err.Error(), "myadapter:") {
		t.Errorf("error %q does not start with adapter name", err.Error())
	}
}

func TestFetchHTML_BadRequestURL(t *testing.T) {
	_, err := fetchHTML(context.Background(), http.DefaultClient, "://malformed", "ua", "adp")
	if err == nil {
		t.Fatal("expected error on malformed URL")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("expected build-request error, got %q", err.Error())
	}
}
