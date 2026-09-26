package tagscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type stubTokenSource struct {
	token string
	err   error
}

func (s *stubTokenSource) Token(_ context.Context) (string, error) {
	return s.token, s.err
}

func newTestServer(t *testing.T, pages [][]string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		if got := r.Header.Get("Authorization"); got != "Basic test-token" {
			t.Errorf("missing/incorrect Authorization header: %q", got)
		}

		pageIdx := 0
		if last := r.URL.Query().Get("last"); last != "" {
			n, err := fmt.Sscanf(last, "%d", &pageIdx)
			if err != nil || n != 1 {
				t.Fatalf("unexpected last param: %q", last)
			}
		}

		if pageIdx+1 < len(pages) {
			w.Header().Set("Link", fmt.Sprintf(`<https://%s/v2/repo/tags/list?last=%d>; rel="next"`, r.Host, pageIdx+1))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "repo", "tags": pages[pageIdx]})
	}))
	return srv, &calls
}

func TestCache_FetchesAllPages(t *testing.T) {
	srv, calls := newTestServer(t, [][]string{{"a", "b"}, {"c"}, {"d", "e"}})
	defer srv.Close()

	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	c := New(host.Host, &stubTokenSource{token: "test-token"}, time.Minute, srv.Client())

	tags, err := c.List(context.Background(), "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(tags) != len(want) {
		t.Fatalf("got %v, want %v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("got %v, want %v", tags, want)
		}
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("expected 3 upstream page requests, got %d", got)
	}
}

func TestCache_CachesPerRepository(t *testing.T) {
	srv, calls := newTestServer(t, [][]string{{"a"}})
	defer srv.Close()

	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	c := New(host.Host, &stubTokenSource{token: "test-token"}, time.Minute, srv.Client())

	for i := 0; i < 5; i++ {
		if _, err := c.List(context.Background(), "repo"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 upstream request due to caching, got %d", got)
	}
}

func TestCache_UpstreamError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	c := New(host.Host, &stubTokenSource{token: "test-token"}, time.Minute, srv.Client())
	if _, err := c.List(context.Background(), "repo"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCache_TokenError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when token fetch fails")
	}))
	defer srv.Close()

	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	c := New(host.Host, &stubTokenSource{err: errors.New("boom")}, time.Minute, srv.Client())
	if _, err := c.List(context.Background(), "repo"); err == nil {
		t.Fatal("expected error, got nil")
	}
}
