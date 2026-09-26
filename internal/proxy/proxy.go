// Package proxy forwards Docker Registry API requests to the configured
// ECR registry, untouched, so ECR remains the source of truth for
// anything beyond the catalog compatibility shim.
package proxy

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"time"
)

// TokenSource returns a valid ECR Basic-auth token (base64 "AWS:<password>").
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type contextKey int

const (
	originalHostKey contextKey = iota
	originalSchemeKey
)

// linkURLPattern matches the "<url>" portion(s) of an RFC 5988 Link header.
var linkURLPattern = regexp.MustCompile(`<([^>]*)>`)

// NewReverseProxy returns a reverse proxy that forwards all requests to
// registryHost (e.g. "123456789012.dkr.ecr.eu-central-1.amazonaws.com").
// It never forwards anywhere else, regardless of request contents.
//
// Every forwarded request is authenticated with a token from tokens,
// replacing whatever Authorization the client sent: VS Code has no ECR
// credential helper wired up for this proxy's (fake) hostname, so it can
// never supply a valid ECR token itself.
func NewReverseProxy(registryHost string, tokens TokenSource) *httputil.ReverseProxy {
	target := &url.URL{
		Scheme: "https",
		Host:   registryHost,
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// All traffic goes to a single upstream host, and VS Code fires many
	// concurrent manifest/blob/tag requests; http.DefaultTransport's
	// MaxIdleConnsPerHost of 2 would force most of them to pay a fresh
	// TLS handshake instead of reusing a keep-alive connection.
	proxy.Transport = NewUpstreamTransport()

	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
			scheme = fwd
		}
		ctx := context.WithValue(r.Context(), originalHostKey, r.Host)
		ctx = context.WithValue(ctx, originalSchemeKey, scheme)
		*r = *r.WithContext(ctx)

		unwrapContinuationToken(r.URL)
		originalDirector(r)
		r.Host = registryHost

		token, err := tokens.Token(r.Context())
		if err != nil {
			// Deliberately omit the error's dynamic content: it may echo back
			// request details, never the token itself.
			log.Printf("failed to obtain ECR authorization token for %s", r.URL.Path)
			r.Header.Del("Authorization")
			return
		}
		r.Header.Set("Authorization", "Basic "+token)
	}

	// ECR paginates tags/list with an absolute Link: <https://<registryHost>/...>
	// rel="next" header; rewrite it back to this proxy so clients keep talking
	// to us instead of the real ECR host, which they have no credentials for.
	proxy.ModifyResponse = func(resp *http.Response) error {
		log.Printf("proxied %s %s -> %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode)

		links := resp.Header["Link"]
		if len(links) == 0 {
			return nil
		}
		host, _ := resp.Request.Context().Value(originalHostKey).(string)
		scheme, _ := resp.Request.Context().Value(originalSchemeKey).(string)
		if host == "" {
			return nil
		}
		rewritten := make([]string, len(links))
		for i, l := range links {
			rewritten[i] = rewriteLinkHeader(l, registryHost, scheme, host)
		}
		resp.Header["Link"] = rewritten
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Deliberately omit request details (headers may include auth tokens).
		log.Printf("proxy error for %s %s: %v", r.Method, r.URL.Path, err)
		w.WriteHeader(http.StatusBadGateway)
	}

	return proxy
}

// NewUpstreamTransport returns an http.Transport tuned for many concurrent
// requests to a single upstream host (the ECR registry): the default
// transport's MaxIdleConnsPerHost of 2 would force most concurrent requests
// to pay a fresh TLS handshake instead of reusing a keep-alive connection.
// Shared by the reverse proxy and tagscache's HTTP client, which both talk
// to the same ECR host.
func NewUpstreamTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	}
}

// rewriteLinkHeader replaces any "<...>" URL in value whose host matches
// targetHost with the same path/query under newScheme://newHost.
func rewriteLinkHeader(value, targetHost, newScheme, newHost string) string {
	return linkURLPattern.ReplaceAllStringFunc(value, func(match string) string {
		raw := match[1 : len(match)-1]
		u, err := url.Parse(raw)
		if err != nil || u.Host != targetHost {
			return match
		}
		u.Scheme = newScheme
		u.Host = newHost
		wrapContinuationToken(u)
		return "<" + u.String() + ">"
	})
}

// wrapContinuationToken re-encodes ECR's "last" pagination cursor with a
// URL-safe alphabet (no '+' or '/'). Some HTTP clients (observed: VS Code's
// Node-based registry client) decode a Link header's query string and
// reconstruct the next request without re-percent-encoding it, silently
// turning literal '+'/'/' into characters ECR then rejects as an invalid
// token. Base64url has no characters that need such escaping, so it
// survives that kind of naive round-trip untouched.
func wrapContinuationToken(u *url.URL) {
	q := u.Query()
	last := q.Get("last")
	if last == "" {
		return
	}
	q.Set("last", base64.RawURLEncoding.EncodeToString([]byte(last)))
	u.RawQuery = q.Encode()
}

// unwrapContinuationToken reverses wrapContinuationToken on an incoming
// request before it's forwarded to ECR. Values that aren't valid base64url
// (e.g. a client replaying ECR's original token directly) are left as-is.
func unwrapContinuationToken(u *url.URL) {
	if u.RawQuery == "" {
		return
	}
	q := u.Query()
	wrapped := q.Get("last")
	if wrapped == "" {
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(wrapped)
	if err != nil {
		return
	}
	q.Set("last", string(decoded))
	u.RawQuery = q.Encode()
}
