package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeCatalog struct {
	repos []string
	err   error
}

func (f *fakeCatalog) List(_ context.Context) ([]string, error) {
	return f.repos, f.err
}

type fakeTags struct {
	tags     []string
	err      error
	lastRepo string
}

func (f *fakeTags) List(_ context.Context, repo string) ([]string, error) {
	f.lastRepo = repo
	return f.tags, f.err
}

type recordingProxy struct {
	called bool
	path   string
}

func (p *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.called = true
	p.path = r.URL.Path
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"tags":["latest"]}`))
}

func TestHealth(t *testing.T) {
	h := New(&fakeCatalog{}, &fakeTags{}, &recordingProxy{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestV2Root(t *testing.T) {
	h := New(&fakeCatalog{}, &fakeTags{}, &recordingProxy{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestCatalogHandledLocally(t *testing.T) {
	proxy := &recordingProxy{}
	h := New(&fakeCatalog{repos: []string{"backend", "frontend"}}, &fakeTags{}, proxy)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/_catalog", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if proxy.called {
		t.Fatal("catalog request must never be forwarded to ECR")
	}

	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(body.Repositories) != 2 {
		t.Fatalf("expected 2 repositories, got %v", body.Repositories)
	}
}

func TestCatalogErrorDoesNotLeakDetails(t *testing.T) {
	h := New(&fakeCatalog{err: errors.New("secret aws details")}, &fakeTags{}, &recordingProxy{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/_catalog", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret aws details") {
		t.Fatal("error response must not leak internal error details")
	}
}

func TestTagsListHandledLocally(t *testing.T) {
	proxy := &recordingProxy{}
	tags := &fakeTags{tags: []string{"1.0", "latest"}}
	h := New(&fakeCatalog{}, tags, proxy)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/ns/backend/tags/list", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if proxy.called {
		t.Fatal("tags/list must be served from the cache, never forwarded raw to ECR")
	}
	if tags.lastRepo != "ns/backend" {
		t.Fatalf("expected repo %q, got %q", "ns/backend", tags.lastRepo)
	}

	var body struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body.Name != "ns/backend" || len(body.Tags) != 2 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestTagsListErrorDoesNotLeakDetails(t *testing.T) {
	h := New(&fakeCatalog{}, &fakeTags{err: errors.New("secret aws details")}, &recordingProxy{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/backend/tags/list", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret aws details") {
		t.Fatal("error response must not leak internal error details")
	}
}

func TestManifestsAndBlobsAreProxied(t *testing.T) {
	proxy := &recordingProxy{}
	h := New(&fakeCatalog{}, &fakeTags{}, proxy)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/backend/manifests/latest", nil))

	if !proxy.called {
		t.Fatal("expected manifest request to be forwarded to ECR")
	}
	if proxy.path != "/v2/backend/manifests/latest" {
		t.Fatalf("unexpected forwarded path: %s", proxy.path)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}
