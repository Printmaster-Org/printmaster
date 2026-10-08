package autoupdate

import (
	"context"
	"errors"
	"testing"
)

type channelTestClient struct {
	mockUpdateClient
	channel string
}

func (c *channelTestClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel string) (*UpdateManifest, error) {
	c.channel = channel
	return c.manifest, c.err
}

func TestManualChannelDoesNotChangeScheduledChannel(t *testing.T) {
	t.Parallel()
	for _, channel := range []string{"stable", "beta", "dev"} {
		t.Run(channel, func(t *testing.T) {
			t.Parallel()
			client := &channelTestClient{mockUpdateClient: mockUpdateClient{err: errors.New("fixture stops before install")}}
			manager, err := NewManager(Options{Enabled: true, CurrentVersion: "1.0.0", Channel: "stable", DataDir: t.TempDir(), ServerClient: client})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.ForceInstallLatestFromChannel(context.Background(), "test", channel); err == nil {
				t.Fatal("expected fixture error")
			}
			if client.channel != channel {
				t.Fatalf("requested=%s want %s", client.channel, channel)
			}
			if manager.Status().Channel != "stable" {
				t.Fatal("manual update changed scheduled channel")
			}
			client.channel = ""
			if err := manager.ForceInstallLatest(context.Background(), "test"); err == nil {
				t.Fatal("expected fixture error")
			}
			if client.channel != "stable" {
				t.Fatal("operation lock/channel leaked")
			}
		})
	}
}

func TestManualChannelRejectsInvalidOrMismatchedManifest(t *testing.T) {
	t.Parallel()
	client := &channelTestClient{mockUpdateClient: mockUpdateClient{manifest: &UpdateManifest{Version: "1.1.0", Channel: "stable"}}}
	manager, err := NewManager(Options{Enabled: true, CurrentVersion: "1.0.0", Channel: "stable", DataDir: t.TempDir(), ServerClient: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ForceInstallLatestFromChannel(context.Background(), "test", "unknown"); err == nil {
		t.Fatal("invalid channel accepted")
	}
	if client.channel != "" {
		t.Fatal("invalid channel dispatched")
	}
	if err := manager.ForceInstallLatestFromChannel(context.Background(), "test", "beta"); err == nil {
		t.Fatal("wrong-channel manifest accepted")
	}
	if manager.Status().Status != StatusIdle {
		t.Fatal("failed selection kept operation busy")
	}
}

func TestManagedChannelUpdatesChecksAndStatusWithoutRestart(t *testing.T) {
	t.Parallel()
	managedChannel := "dev"
	client := &channelTestClient{mockUpdateClient: mockUpdateClient{err: errors.New("fixture stops before install")}}
	manager, err := NewManager(Options{Enabled: true, CurrentVersion: "0.31.1", Channel: "stable", ChannelProvider: func() string { return managedChannel }, DataDir: t.TempDir(), ServerClient: client})
	if err != nil {
		t.Fatal(err)
	}

	for _, channel := range []string{"dev", "beta", "stable"} {
		managedChannel = channel
		if manager.Status().Channel != channel {
			t.Fatal("managed channel status stale")
		}
		_ = manager.CheckNow(context.Background())
		if client.channel != channel {
			t.Fatalf("check used %s want %s", client.channel, channel)
		}
	}
	managedChannel = "dev"
	_ = manager.ForceInstallLatestFromChannel(context.Background(), "test", "stable")
	if client.channel != "stable" || manager.Status().Channel != "dev" {
		t.Fatal("explicit operation changed managed default")
	}
	managedChannel = ""
	if manager.Status().Channel != "stable" {
		t.Fatal("empty managed selection did not use Agent config")
	}
}

func TestBetaReleaseVersionOrdering(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		current string
		target  string
		newer   bool
	}{
		{"0.31.1", "0.32.0-beta.1", true},
		{"0.32.0-beta.1", "0.32.0-beta.2", true},
		{"0.32.0-beta.9", "0.32.0-beta.10", true},
		{"0.32.0-beta.2", "0.32.0", true},
		{"0.32.0", "0.32.0-beta.2", false},
	} {
		t.Run(tc.current+"->"+tc.target, func(t *testing.T) {
			client := &channelTestClient{}
			manager, err := NewManager(Options{Enabled: true, CurrentVersion: tc.current, Channel: "beta", DataDir: t.TempDir(), ServerClient: client})
			if err != nil {
				t.Fatal(err)
			}
			if got := manager.isUpdateNeeded(&UpdateManifest{Version: tc.target}); got != tc.newer {
				t.Fatalf("update needed = %v, want %v", got, tc.newer)
			}
		})
	}
}
