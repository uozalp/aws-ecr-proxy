package ecrauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

type mockClient struct {
	calls     int
	token     string
	expiresAt time.Time
	err       error
}

func (m *mockClient) GetAuthorizationToken(_ context.Context, _ *ecr.GetAuthorizationTokenInput, _ ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	expiresAt := m.expiresAt
	return &ecr.GetAuthorizationTokenOutput{
		AuthorizationData: []types.AuthorizationData{
			{AuthorizationToken: &m.token, ExpiresAt: &expiresAt},
		},
	}, nil
}

func TestTokenProvider_FetchesAndCaches(t *testing.T) {
	client := &mockClient{token: "dG9rZW4x", expiresAt: time.Now().Add(time.Hour)}
	p := New(client)

	for i := 0; i < 3; i++ {
		tok, err := p.Token(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tok != "dG9rZW4x" {
			t.Fatalf("unexpected token: %s", tok)
		}
	}

	if client.calls != 1 {
		t.Fatalf("expected 1 ECR call due to caching, got %d", client.calls)
	}
}

func TestTokenProvider_RefreshesNearExpiry(t *testing.T) {
	client := &mockClient{token: "first", expiresAt: time.Now().Add(refreshMargin - time.Second)}
	p := New(client)

	if _, err := p.Token(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client.token = "second"
	client.expiresAt = time.Now().Add(time.Hour)
	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "second" {
		t.Fatalf("expected refreshed token, got %s", tok)
	}
	if client.calls != 2 {
		t.Fatalf("expected 2 ECR calls, got %d", client.calls)
	}
}

func TestTokenProvider_Error(t *testing.T) {
	p := New(&mockClient{err: errors.New("boom")})
	if _, err := p.Token(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
}
