package releases

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"printmaster/server/storage"
)

func TestReleaseChannelClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ version, channel string }{
		{"1.0.0", "stable"}, {"1.0.0+metadata", "stable"},
		{"1.0.0+metadata-with-hyphen", "stable"},
		{"1.1.0-dev.5", "dev"}, {"1.1.0-dev", "dev"},
		{"1.1.0-beta.1", "beta"}, {"1.1.0-rc.2", "beta"}, {"1.1.0-alpha.1", "beta"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if got := channelFromVersion(tc.version); got != tc.channel {
				t.Fatalf("got %s want %s", got, tc.channel)
			}
		})
	}
}

func TestBetaReleaseAssetNames(t *testing.T) {
	t.Parallel()
	for _, asset := range []string{
		"printmaster-agent-v0.32.0-beta.1-linux-amd64",
		"printmaster-agent_0.32.0-beta.1_amd64.deb",
		"printmaster-agent-0.32.0-beta.1-1.fc44.x86_64.rpm",
	} {
		t.Run(asset, func(t *testing.T) {
			desc, ok := buildDescriptor("agent", "0.32.0-beta.1", asset)
			if !ok || desc.version != "0.32.0-beta.1" || desc.platform != "linux" || desc.arch != "amd64" {
				t.Fatalf("unexpected Beta descriptor: %+v, matched=%v", desc, ok)
			}
		})
	}
}

func TestSyncRepairsCachedBetaManifestAndExcludesItFromStable(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "releases.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, nil, ManagerOptions{ManifestVersion: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	artifact := &storage.ReleaseArtifact{Component: "agent", Version: "1.1.0-beta.1", Platform: "linux", Arch: "amd64", Channel: "stable", SHA256: strings.Repeat("ab", 32), SizeBytes: 100, SourceURL: "https://example.com/agent"}
	if err := store.UpsertReleaseArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	before, err := manager.EnsureManifestForArtifact(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if manifest, err := manager.GetLatestManifest(ctx, "agent", "linux", "amd64", "stable", ""); err == nil || manifest != nil {
		t.Fatal("mislabeled beta escaped stable guard")
	}
	worker := &IntakeWorker{store: store, manifests: manager}
	worker.ensureManifest(ctx, artifact)
	after, err := manager.GetLatestManifest(ctx, "agent", "linux", "amd64", "beta", "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Channel != "beta" || after.Signature == before.Signature {
		t.Fatal("cached beta channel/signature not repaired")
	}
	stored, err := store.GetReleaseArtifact(ctx, "agent", artifact.Version, "linux", "amd64", "")
	if err != nil || stored.Channel != "beta" {
		t.Fatalf("artifact channel: %+v %v", stored, err)
	}
}
