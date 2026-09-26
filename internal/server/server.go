// Package server wires together the HTTP handlers that make the proxy
// look like a Docker Registry to VS Code Container Tools.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// CatalogLister returns the list of ECR repository names.
type CatalogLister interface {
	List(ctx context.Context) ([]string, error)
}

// TagLister returns the full, aggregated tag list for an ECR repository.
type TagLister interface {
	List(ctx context.Context, repo string) ([]string, error)
}

// New builds the HTTP handler for the proxy. Requests to /health, /v2/,
// /v2/_catalog and /v2/<repo>/tags/list are served locally; every other
// /v2/... request is forwarded to ecrProxy (the real ECR registry).
func New(cat CatalogLister, tags TagLister, ecrProxy http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			handleRegistryCheck(w, r)
		case r.URL.Path == "/v2/_catalog":
			handleCatalog(cat)(w, r)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
			if repo == "" {
				http.NotFound(w, r)
				return
			}
			handleTagsList(tags, repo)(w, r)
		default:
			ecrProxy.ServeHTTP(w, r)
		}
	})

	return mux
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleRegistryCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{})
}

func handleCatalog(cat CatalogLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repos, err := cat.List(r.Context())
		if err != nil {
			log.Printf("catalog error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"errors": []map[string]string{
					{"code": "ECR_ERROR", "message": "failed to list ECR repositories"},
				},
			})
			return
		}
		if repos == nil {
			repos = []string{}
		}
		writeJSON(w, http.StatusOK, map[string][]string{"repositories": repos})
	}
}

func handleTagsList(tags TagLister, repo string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := tags.List(r.Context(), repo)
		if err != nil {
			log.Printf("tags list error for %s: %v", repo, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"errors": []map[string]string{
					{"code": "ECR_ERROR", "message": "failed to list tags"},
				},
			})
			return
		}
		if list == nil {
			list = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": repo, "tags": list})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}
