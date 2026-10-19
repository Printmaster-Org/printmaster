package updatetrust

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type countingTransport struct {
	base     http.RoundTripper
	requests atomic.Int64
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests.Add(1)
	return c.base.RoundTrip(req)
}

func TestTrustStateLossFailsBeforeNetworkAfterRestart(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"root.json", "timestamp.json", "snapshot.json", "targets.json",
		"corrupt root", "corrupt timestamp", "corrupt snapshot", "corrupt targets",
		"missing record", "corrupt record", "unknown record", "cache cleanup",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			client, _ := repositoryClient(t, repo)
			if err := client.Verify(context.Background(), repo.target); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing record":
				if err := os.Remove(client.statePath()); err != nil {
					t.Fatal(err)
				}
			case "corrupt record", "unknown record":
				data := []byte("{")
				if mode == "unknown record" {
					data = []byte(`{"version":2,"sha256":{}}`)
				}
				if err := os.WriteFile(client.statePath(), data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "cache cleanup":
				// Delete only the exact metadata directory owned by this test.
				if err := os.RemoveAll(client.options.CacheDir); err != nil {
					t.Fatal(err)
				}
			case "corrupt root", "corrupt timestamp", "corrupt snapshot", "corrupt targets":
				name := mode[len("corrupt "):] + ".json"
				if err := os.WriteFile(filepath.Join(client.options.CacheDir, name), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Remove(filepath.Join(client.options.CacheDir, mode)); err != nil {
					t.Fatal(err)
				}
			}
			opts := client.options
			counter := &countingTransport{base: opts.HTTPClient.Transport}
			if counter.base == nil {
				counter.base = http.DefaultTransport
			}
			httpClient := *opts.HTTPClient
			httpClient.Transport = counter
			opts.HTTPClient = &httpClient
			restarted, err := NewClient(opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Verify(context.Background(), repo.target); !errors.Is(err, ErrTrustState) {
				t.Fatalf("expected explicit trust recovery error, got %v", err)
			}
			if requests := counter.requests.Load(); requests != 0 {
				t.Fatalf("damaged state caused %d network requests before rejection", requests)
			}
		})
	}
}

func TestTrustVersionsSurviveRestartAndTargetCacheCleanup(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	repo.timestamp.Signed.Version = 2
	repo.publish(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	targetCache := filepath.Join(client.options.CacheDir, "targets")
	if err := os.MkdirAll(targetCache, 0o700); err != nil {
		t.Fatal(err)
	}
	// Downloaded targets are disposable; trusted metadata is not.
	if err := os.RemoveAll(targetCache); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewClient(client.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("target cleanup damaged trust: %v", err)
	}
	repo.timestamp.Signed.Version = 1
	repo.publish(t)
	var rollback *metadata.ErrBadVersionNumber
	if err := restarted.Verify(context.Background(), repo.target); !errors.As(err, &rollback) {
		t.Fatalf("expected TUF rollback rejection with intact state, got %v", err)
	}
}

func TestFailedRefreshPreservesAdvancedTimestamp(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	repo.timestamp.Signed.Version = 2
	// The signed timestamp points to snapshot 2, but that versioned file is
	// unavailable. TUF must retain the newer timestamp despite this failure.
	repo.snapshot.Signed.Version = 2
	repo.publish(t)
	if err := client.Verify(context.Background(), repo.target); err == nil {
		t.Fatal("unavailable snapshot accepted")
	}
	repo.timestamp.Signed.Version = 1
	repo.snapshot.Signed.Version = 1
	repo.publish(t)
	restarted, err := NewClient(client.options)
	if err != nil {
		t.Fatal(err)
	}
	var rollback *metadata.ErrBadVersionNumber
	if err := restarted.Verify(context.Background(), repo.target); !errors.As(err, &rollback) {
		t.Fatalf("failed refresh lost TUF rollback state: %v", err)
	}
}

func TestInterruptedTrustStateCheckpointFailsClosed(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(client.statePath())
	if err != nil {
		t.Fatal(err)
	}
	repo.timestamp.Signed.Version++
	repo.publish(t)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after TUF persisted newer metadata but before the
	// integrity record was replaced. Do not silently adopt the mismatched cache.
	if err := os.WriteFile(client.statePath(), previous, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); !errors.Is(err, ErrTrustState) {
		t.Fatalf("interrupted checkpoint did not require recovery: %v", err)
	}
}

func TestFailedInitialRefreshCanRetryWithoutReset(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	timestampPath := filepath.Join(repo.dir, "timestamp.json")
	timestamp, err := os.ReadFile(timestampPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(timestampPath); err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), repo.target); err == nil {
		t.Fatal("missing timestamp accepted")
	}
	if err := os.WriteFile(timestampPath, timestamp, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewClient(client.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("ordinary network failure required insecure trust reset: %v", err)
	}
}

type blockingCheckpointTransport struct {
	base  http.RoundTripper
	state string
}

func (b *blockingCheckpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if filepath.Base(req.URL.Path) == "timestamp.json" {
		if err := os.Remove(b.state); err != nil {
			return nil, err
		}
		if err := os.Mkdir(b.state, 0o700); err != nil {
			return nil, err
		}
	}
	return b.base.RoundTrip(req)
}

func TestCheckpointFailureCannotAuthorizeTarget(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	client.options.HTTPClient.Transport = &blockingCheckpointTransport{
		base: client.options.HTTPClient.Transport, state: client.statePath(),
	}
	if err := client.Verify(context.Background(), repo.target); !errors.Is(err, ErrTrustState) {
		t.Fatalf("checkpoint write failure was not surfaced: %v", err)
	}
}
