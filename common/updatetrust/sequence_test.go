package updatetrust

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestReleaseSequencesAreSignedImmutableAndRetained(t *testing.T) {
	t.Parallel()
	repo := newTestRepository(t)
	root, err := os.ReadFile(filepath.Join(repo.dir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(file, []byte("official test artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, second := repo.target, repo.target
	first.Channel, first.Version = "dev", "0.32.0-dev.fff"
	second.Channel, second.Version = "dev", "0.32.0-dev.000"
	opts := PublishOptions{Directory: repo.dir, Root: root, Keys: repo.keys,
		Targets: []PublishTarget{{Target: first, File: file}, {Target: second, File: file}}}
	if err := Publish(opts); err != nil {
		t.Fatal(err)
	}
	client, server := repositoryClient(t, repo)
	assertSequence := func(client *Client, target Target, want int64) {
		t.Helper()
		got, err := client.Authorize(context.Background(), target)
		if err != nil || got.Target != target || got.ReleaseSequence != want {
			t.Fatalf("authorization=%+v want sequence=%d err=%v", got, want, err)
		}
	}
	assertSequence(client, first, 1)
	assertSequence(client, second, 2)
	// Duplicate inputs and freshness-only publications cannot promote an old build.
	opts.Targets = []PublishTarget{{Target: first, File: file}}
	if err := Publish(opts); err != nil {
		t.Fatal(err)
	}
	opts.Targets = nil
	if err := Publish(opts); err != nil {
		t.Fatal(err)
	}
	restored, err := NewClient(Options{TrustedRoot: root, MetadataURL: server.URL, CacheDir: client.options.CacheDir, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	assertSequence(restored, first, 1)
	assertSequence(restored, second, 2)
	assertSequence(restored, repo.target, 0) // Legacy inventory is not silently re-ordered.
}

func TestReleaseSequenceValidationAndScope(t *testing.T) {
	t.Parallel()
	infoWithSequence := func(value int64) *metadata.TargetFiles {
		t.Helper()
		data, err := json.Marshal(releaseCustom{ReleaseSequence: value})
		if err != nil {
			t.Fatal(err)
		}
		raw := json.RawMessage(data)
		return &metadata.TargetFiles{Custom: &raw}
	}
	targets := map[string]*metadata.TargetFiles{"agent/dev/1.0.0-dev.a/linux/amd64/binary": infoWithSequence(3)}
	for _, path := range []string{
		"agent/dev/1.0.0-dev.b/linux/amd64/binary",
		"agent/dev/1.0.0-dev.b/windows/amd64/binary",
		"agent/beta/1.0.0-beta.1/linux/amd64/binary",
	} {
		info := metadata.TargetFile()
		if err := assignReleaseSequence(targets, path, info); err != nil {
			t.Fatal(err)
		}
		got, err := releaseSequence(info)
		want := int64(1)
		if path == "agent/dev/1.0.0-dev.b/linux/amd64/binary" {
			want = 4
		}
		if err != nil || got != want {
			t.Fatalf("%s sequence=%d want=%d err=%v", path, got, want, err)
		}
	}
	for _, value := range []int64{-1, math.MaxInt64} {
		targets["agent/dev/1.0.0-dev.a/linux/amd64/binary"] = infoWithSequence(value)
		if err := assignReleaseSequence(targets, "agent/dev/1.0.0-dev.b/linux/amd64/binary", metadata.TargetFile()); err == nil {
			t.Fatalf("invalid/exhausted ordering %d accepted", value)
		}
	}
}
