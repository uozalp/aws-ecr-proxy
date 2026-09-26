// Package ecrauth fetches and caches the ECR Docker Registry authorization
// token used to authenticate proxied requests against the real ECR
// registry. This is distinct from the AWS SDK credentials used for the
// ECR management API (e.g. DescribeRepositories).
package ecrauth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
)

// ECRClient is the subset of the ECR SDK client used by TokenProvider.
type ECRClient interface {
	GetAuthorizationToken(ctx context.Context, params *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

// refreshMargin re-fetches the token this long before it actually expires.
const refreshMargin = 5 * time.Minute

// TokenProvider caches the ECR Basic-auth token (base64 "AWS:<password>"),
// refreshing it shortly before it expires. ECR tokens are valid 12 hours.
type TokenProvider struct {
	client ECRClient

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// New creates a TokenProvider backed by client.
func New(client ECRClient) *TokenProvider {
	return &TokenProvider{client: client}
}

// Token returns a valid base64-encoded "AWS:<password>" Basic-auth token,
// refreshing it from ECR if the cached one is missing or near expiry.
func (p *TokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Now().Before(p.expiresAt) {
		return p.token, nil
	}

	out, err := p.client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", fmt.Errorf("get authorization token: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil {
		return "", fmt.Errorf("get authorization token: no authorization data returned")
	}

	data := out.AuthorizationData[0]
	expiresAt := time.Now().Add(time.Hour)
	if data.ExpiresAt != nil {
		expiresAt = data.ExpiresAt.Add(-refreshMargin)
	}

	p.token = *data.AuthorizationToken
	p.expiresAt = expiresAt
	return p.token, nil
}
