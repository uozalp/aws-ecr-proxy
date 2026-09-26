// Package catalog fetches and caches the list of ECR repositories in
// Docker Registry catalog format.
package catalog

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
)

// ECRClient is the subset of the ECR SDK client used by Catalog.
// Defined as an interface so tests can supply a mock implementation.
type ECRClient interface {
	DescribeRepositories(ctx context.Context, params *ecr.DescribeRepositoriesInput, optFns ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
}

// Catalog caches the sorted list of ECR repository names.
type Catalog struct {
	client   ECRClient
	ttl      time.Duration
	registry string

	mu        sync.Mutex
	repos     []string
	fetchedAt time.Time
}

// New creates a Catalog backed by client, caching results for ttl.
// registryID, if non-empty, is passed to DescribeRepositories to scope
// results to a specific ECR registry (AWS account).
func New(client ECRClient, ttl time.Duration, registryID string) *Catalog {
	return &Catalog{
		client:   client,
		ttl:      ttl,
		registry: registryID,
	}
}

// List returns the sorted list of ECR repository names, using the cache
// when it is still fresh. Concurrent callers are serialized so an expired
// cache only triggers a single ECR refresh.
func (c *Catalog) List(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.repos != nil && c.ttl > 0 && time.Since(c.fetchedAt) < c.ttl {
		return c.repos, nil
	}

	repos, err := c.fetchAll(ctx)
	if err != nil {
		return nil, err
	}

	sort.Strings(repos)
	c.repos = repos
	c.fetchedAt = time.Now()
	return c.repos, nil
}

func (c *Catalog) fetchAll(ctx context.Context) ([]string, error) {
	var names []string
	var nextToken *string
	maxResults := int32(1000) // ECR's max page size, to minimize round trips on large registries

	for {
		input := &ecr.DescribeRepositoriesInput{
			NextToken:  nextToken,
			MaxResults: &maxResults,
		}
		if c.registry != "" {
			input.RegistryId = &c.registry
		}

		out, err := c.client.DescribeRepositories(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("describe repositories: %w", err)
		}

		for _, repo := range out.Repositories {
			if repo.RepositoryName != nil {
				names = append(names, *repo.RepositoryName)
			}
		}

		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		nextToken = out.NextToken
	}

	return names, nil
}
