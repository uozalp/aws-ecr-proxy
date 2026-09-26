package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

// mockECRClient serves canned DescribeRepositories pages and counts calls.
type mockECRClient struct {
	mu    sync.Mutex
	pages [][]string // repository names per page, in NextToken order
	err   error
	calls int
}

func (m *mockECRClient) DescribeRepositories(_ context.Context, input *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++

	if m.err != nil {
		return nil, m.err
	}

	pageIdx := 0
	if input.NextToken != nil {
		idx, err := indexFromToken(*input.NextToken)
		if err != nil {
			return nil, err
		}
		pageIdx = idx
	}

	if pageIdx >= len(m.pages) {
		return &ecr.DescribeRepositoriesOutput{}, nil
	}

	var repos []types.Repository
	for _, name := range m.pages[pageIdx] {
		n := name
		repos = append(repos, types.Repository{RepositoryName: &n})
	}

	out := &ecr.DescribeRepositoriesOutput{Repositories: repos}
	if pageIdx+1 < len(m.pages) {
		tok := tokenFromIndex(pageIdx + 1)
		out.NextToken = &tok
	}
	return out, nil
}

func tokenFromIndex(i int) string {
	return string(rune('a' + i))
}

func indexFromToken(tok string) (int, error) {
	if len(tok) != 1 {
		return 0, errors.New("invalid token")
	}
	return int(tok[0] - 'a'), nil
}

func TestCatalog_EmptyRegistry(t *testing.T) {
	c := New(&mockECRClient{pages: [][]string{{}}}, time.Minute, "")
	repos, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("expected no repositories, got %v", repos)
	}
}

func TestCatalog_MultipleRepositories(t *testing.T) {
	c := New(&mockECRClient{pages: [][]string{{"frontend", "backend"}}}, time.Minute, "")
	repos, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"backend", "frontend"}
	assertEqual(t, repos, want)
}

func TestCatalog_Pagination(t *testing.T) {
	client := &mockECRClient{pages: [][]string{
		{"worker"},
		{"backend"},
		{"frontend"},
	}}
	c := New(client, time.Minute, "")
	repos, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, repos, []string{"backend", "frontend", "worker"})
	if client.calls != 3 {
		t.Fatalf("expected 3 ECR calls for 3 pages, got %d", client.calls)
	}
}

func TestCatalog_Sorting(t *testing.T) {
	c := New(&mockECRClient{pages: [][]string{{"worker", "backend", "frontend"}}}, time.Minute, "")
	repos, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, repos, []string{"backend", "frontend", "worker"})
}

func TestCatalog_ECRFailure(t *testing.T) {
	c := New(&mockECRClient{err: errors.New("boom")}, time.Minute, "")
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCatalog_CacheAvoidsRepeatedCalls(t *testing.T) {
	client := &mockECRClient{pages: [][]string{{"backend"}}}
	c := New(client, time.Minute, "")

	for i := 0; i < 5; i++ {
		if _, err := c.List(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if client.calls != 1 {
		t.Fatalf("expected 1 ECR call due to caching, got %d", client.calls)
	}
}

func TestCatalog_CacheExpires(t *testing.T) {
	client := &mockECRClient{pages: [][]string{{"backend"}}}
	c := New(client, time.Millisecond, "")

	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.calls != 2 {
		t.Fatalf("expected 2 ECR calls after cache expiry, got %d", client.calls)
	}
}

func assertEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
