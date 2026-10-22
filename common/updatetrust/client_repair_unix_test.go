//go:build !windows

package updatetrust

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClientTrustRepairWriteFailurePreservesEvidenceAndCanRetry(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary identity to enforce directory write denial")
	}
	repo := newTestRepository(t)
	client, _ := repositoryClient(t, repo)
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatal(err)
	}
	opts := ClientTrustRepairOptions{CacheDir: client.options.CacheDir, BackupDir: copyClientMetadata(t, client), ExternalSerialization: true}
	checkpoint, err := os.ReadFile(client.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.CacheDir, "timestamp.json"), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(opts.CacheDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(opts.CacheDir, 0o700); err != nil {
			t.Error(err)
		}
	}()
	evidence, err := RepairClientTrust(context.Background(), opts)
	if err == nil || evidence == "" {
		t.Fatalf("write denial did not report preserved evidence: %s %v", evidence, err)
	}
	saved, err := os.ReadFile(filepath.Join(evidence, "timestamp.json"))
	if err != nil || string(saved) != "damaged" {
		t.Fatalf("failed repair lost original evidence: %q %v", saved, err)
	}
	current, err := os.ReadFile(client.statePath())
	if err != nil || !bytes.Equal(checkpoint, current) {
		t.Fatalf("failed repair changed checkpoint: %v", err)
	}
	if err := os.Chmod(opts.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairClientTrust(context.Background(), opts); err != nil {
		t.Fatalf("retry of exact repair failed: %v", err)
	}
	if err := client.Verify(context.Background(), repo.target); err != nil {
		t.Fatalf("normal authorization after repair retry failed: %v", err)
	}
}
