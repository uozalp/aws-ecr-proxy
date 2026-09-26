package proxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type stubTokenSource struct {
	token string
	err   error
}

func (s *stubTokenSource) Token(_ context.Context) (string, error) {
	return s.token, s.err
}

func TestReverseProxy_InjectsTokenAndRewritesLink(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Basic dGVzdC10b2tlbg==" {
			t.Errorf("upstream did not receive injected token, got %q", got)
		}
		w.Header().Set("Link", `<https://`+r.Host+`/v2/backend/tags/list?last=abc>; rel="next"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"backend","tags":["latest"]}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}

	rp := NewReverseProxy(upstreamURL.Host, &stubTokenSource{token: "dGVzdC10b2tlbg=="})
	rp.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

	req := httptest.NewRequest(http.MethodGet, "/v2/backend/tags/list", nil)
	req.Host = "localhost:5000"
	req.Header.Set("Authorization", "whatever-the-client-sent")
	rr := httptest.NewRecorder()

	rp.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	link := rr.Header().Get("Link")
	if !strings.Contains(link, "localhost:5000") {
		t.Fatalf("expected Link header rewritten to proxy host, got %q", link)
	}
	if strings.Contains(link, upstreamURL.Host) {
		t.Fatalf("Link header still points at upstream host: %q", link)
	}
}

func TestReverseProxy_TokenErrorStripsAuthorization(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("expected no Authorization header forwarded, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}

	rp := NewReverseProxy(upstreamURL.Host, &stubTokenSource{err: context.DeadlineExceeded})
	rp.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

	req := httptest.NewRequest(http.MethodGet, "/v2/backend/tags/list", nil)
	req.Header.Set("Authorization", "client-supplied")
	rr := httptest.NewRecorder()

	rp.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestRewriteLinkHeader(t *testing.T) {
	in := `<https://456922045297.dkr.ecr.eu-west-1.amazonaws.com/v2/repo/tags/list?last=xyz>; rel="next"`
	got := rewriteLinkHeader(in, "456922045297.dkr.ecr.eu-west-1.amazonaws.com", "http", "localhost:5000")
	wantToken := base64.RawURLEncoding.EncodeToString([]byte("xyz"))
	want := `<http://localhost:5000/v2/repo/tags/list?last=` + wantToken + `>; rel="next"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRewriteLinkHeader_WrapsTokenWithSlashAndPlus(t *testing.T) {
	// The raw ECR token as it appears after percent-decoding: '+' and '/'
	// are exactly the characters a naive client fails to re-escape.
	rawToken := "ab+c/d=="
	in := `<https://456922045297.dkr.ecr.eu-west-1.amazonaws.com/v2/repo/tags/list?last=` + url.QueryEscape(rawToken) + `>; rel="next"`
	got := rewriteLinkHeader(in, "456922045297.dkr.ecr.eu-west-1.amazonaws.com", "http", "localhost:5000")

	rawURL := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(got, ">", 2)[0], "<"), "")
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("rewritten Link URL did not parse: %v (%q)", err, got)
	}
	wrapped := u.Query().Get("last")
	if strings.ContainsAny(wrapped, "+/") {
		t.Fatalf("wrapped token still contains '+' or '/', a naive client would corrupt it: %q", wrapped)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(wrapped)
	if err != nil {
		t.Fatalf("wrapped token is not valid base64url: %v", err)
	}
	if string(decoded) != rawToken {
		t.Fatalf("wrapped token does not decode back to original, got %q want %q", decoded, rawToken)
	}
}

func TestUnwrapContinuationToken_RecoversOriginalToken(t *testing.T) {
	rawToken := "ab+c/d=="
	wrapped := base64.RawURLEncoding.EncodeToString([]byte(rawToken))

	u, err := url.Parse("http://localhost:5000/v2/repo/tags/list?last=" + wrapped)
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}

	unwrapContinuationToken(u)

	if got := u.Query().Get("last"); got != rawToken {
		t.Fatalf("got %q, want %q", got, rawToken)
	}
}

func TestUnwrapContinuationToken_PassesThroughNonWrappedValue(t *testing.T) {
	u, err := url.Parse("http://localhost:5000/v2/repo/tags/list?last=not-a-wrapped-token!")
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}

	unwrapContinuationToken(u)

	if got := u.Query().Get("last"); got != "not-a-wrapped-token!" {
		t.Fatalf("expected unrecognized value left untouched, got %q", got)
	}
}

func TestRewriteLinkHeader_LeavesUnrelatedHostAlone(t *testing.T) {
	in := `<https://example.com/somewhere>; rel="next"`
	got := rewriteLinkHeader(in, "456922045297.dkr.ecr.eu-west-1.amazonaws.com", "http", "localhost:5000")
	if got != in {
		t.Fatalf("expected unrelated Link left untouched, got %q", got)
	}
}
