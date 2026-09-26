// Package tagscache fetches and caches the full tag list for an ECR
// repository, following ECR's Link-header pagination internally so the
// client only ever needs a single round trip instead of one per page.
package tagscache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
)

// TokenSource returns a valid ECR Basic-auth token (base64 "AWS:<password>").
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// maxBodyBytes caps how much of a single page response we'll read, as a
// defensive limit against a misbehaving upstream.
const maxBodyBytes = 10 << 20 // 10MiB

// nextLinkPattern extracts the URL of the rel="next" entry in a Link header.
var nextLinkPattern = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// Cache caches each repository's full tag list, refreshing per-repository
// (not globally) once its entry is older than ttl.
type Cache struct {
	registryHost string
	tokens       TokenSource
	client       *http.Client
	ttl          time.Duration

	mu      sync.Mutex
	entries map[string]*repoEntry
}

type repoEntry struct {
	mu        sync.Mutex // serializes refreshes for this one repository
	tags      []string
	fetchedAt time.Time
}

// New creates a Cache that fetches tags from registryHost, authenticating
// with tokens, caching each repository's result for ttl.
func New(registryHost string, tokens TokenSource, ttl time.Duration, client *http.Client) *Cache {
	if client == nil {
		client = http.DefaultClient
	}
	return &Cache{
		registryHost: registryHost,
		tokens:       tokens,
		client:       client,
		ttl:          ttl,
		entries:      make(map[string]*repoEntry),
	}
}

// List returns the full, aggregated tag list for repo, using the cache
// when it's still fresh.
func (c *Cache) List(ctx context.Context, repo string) ([]string, error) {
	e := c.entry(repo)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.tags != nil && c.ttl > 0 && time.Since(e.fetchedAt) < c.ttl {
		return e.tags, nil
	}

	tags, err := c.fetchAll(ctx, repo)
	if err != nil {
		return nil, err
	}

	e.tags = tags
	e.fetchedAt = time.Now()
	return e.tags, nil
}

func (c *Cache) entry(repo string) *repoEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[repo]
	if !ok {
		e = &repoEntry{}
		c.entries[repo] = e
	}
	return e
}

func (c *Cache) fetchAll(ctx context.Context, repo string) ([]string, error) {
	var tags []string

	next := (&url.URL{
		Scheme:   "https",
		Host:     c.registryHost,
		Path:     "/v2/" + repo + "/tags/list",
		RawQuery: "n=1000",
	}).String()

	for next != "" {
		page, pageNext, err := c.fetchPage(ctx, next)
		if err != nil {
			return nil, err
		}
		tags = append(tags, page...)
		next = pageNext
	}

	return tags, nil
}

func (c *Cache) fetchPage(ctx context.Context, pageURL string) (tags []string, nextURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build tags request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("get ecr token: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch tags: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read tags response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch tags: unexpected status %d", resp.StatusCode)
	}

	var page struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("decode tags response: %w", err)
	}

	return page.Tags, nextLinkURL(resp.Header.Get("Link")), nil
}

func nextLinkURL(linkHeader string) string {
	m := nextLinkPattern.FindStringSubmatch(linkHeader)
	if m == nil {
		return ""
	}
	return m[1]
}
