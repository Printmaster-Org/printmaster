package updatetrust

import (
	"context"
	"errors"
	"testing"
)

func TestCancelledVerificationDoesNotWaitForConcurrentRefresh(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	client.refreshLock <- struct{}{}
	defer func() { <-client.refreshLock }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Verify(ctx, repo.target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification waited or lost cancellation: %v", err)
	}
}
