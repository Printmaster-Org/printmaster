package autoupdate

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"printmaster/common/updatetrust"
)

type verifierFunc func(context.Context, updatetrust.Target) error

type rawManifestClient struct{ mockUpdateClient }

func (c *rawManifestClient) GetLatestManifest(context.Context, string, string, string, string, string) (*UpdateManifest, error) {
	return c.manifest, c.err
}

func (f verifierFunc) Verify(ctx context.Context, target updatetrust.Target) error {
	return f(ctx, target)
}

// Lifecycle fixtures test orchestration separately from publisher cryptography.
func fixtureVerifier() PublisherVerifier {
	return verifierFunc(func(context.Context, updatetrust.Target) error { return nil })
}

func fixtureManifest(manifest *UpdateManifest) *UpdateManifest {
	if manifest == nil {
		return nil
	}
	result := *manifest
	if result.Component == "" {
		result.Component = "agent"
	}
	if result.Platform == "" {
		result.Platform = runtime.GOOS
	}
	if result.Arch == "" {
		result.Arch = runtime.GOARCH
	}
	if result.Channel == "" {
		result.Channel = "stable"
	}
	return &result
}

func TestMissingPublisherTrustBlocksAllEntryPoints(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"scheduled", "manual", "force", "channel"} {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()
			m := lifecycleManager(t, &mockUpdateClient{manifest: &UpdateManifest{Version: "1.1.0"}}, nil)
			m.publisherVerifier = nil
			var err error
			switch entry {
			case "scheduled":
				err = m.performCheck(context.Background())
			case "manual":
				err = m.CheckNow(context.Background())
			case "force":
				err = m.ForceInstallLatest(context.Background(), "test")
			case "channel":
				err = m.ForceInstallLatestFromChannel(context.Background(), "test", "stable")
			}
			if !errors.Is(err, ErrPublisherTrust) {
				t.Fatalf("unprovisioned trust accepted: %v", err)
			}
			if p := m.Progress(); p.Status != "failed" || p.Error == "" {
				t.Fatalf("trust failure not durable/visible: %+v", p)
			}
			if m.latestManifest != nil {
				t.Fatal("unverified candidate published as available")
			}
		})
	}
}

func TestPublisherFailureCannotFallBackToLegacySignature(t *testing.T) {
	t.Parallel()
	manifest := signedTestManifest(t, newTestKey(t), "legacy")
	m := lifecycleManager(t, &mockUpdateClient{manifest: manifest}, nil)
	m.platform, m.arch = manifest.Platform, manifest.Arch
	rejected := errors.New("publisher metadata rejected")
	m.publisherVerifier = verifierFunc(func(context.Context, updatetrust.Target) error { return rejected })
	if err := m.ForceInstallLatest(context.Background(), "test"); !errors.Is(err, rejected) || !errors.Is(err, ErrPublisherTrust) {
		t.Fatalf("valid legacy signature bypassed publisher trust: %v", err)
	}
	if code := manifestErrorCode(ErrPublisherTrust); code != ErrCodeManifestError {
		t.Fatalf("wrong error classification: %s", code)
	}
}

func TestPublisherTargetUsesIntendedIdentity(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"component", "platform", "arch", "channel"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			manifest := fixtureManifest(&UpdateManifest{Version: "1.1.0"})
			switch field {
			case "component":
				manifest.Component = "server"
			case "platform":
				manifest.Platform = "wrong-os"
			case "arch":
				manifest.Arch = "wrong-arch"
			case "channel":
				manifest.Channel = "dev"
			}
			called := false
			m.publisherVerifier = verifierFunc(func(context.Context, updatetrust.Target) error {
				called = true
				return nil
			})
			if err := m.authorizeManifest(context.Background(), manifest); !errors.Is(err, ErrPublisherTrust) || called {
				t.Fatalf("wrong identity reached verification: err=%v called=%v", err, called)
			}
		})
	}
}

func TestPackageDiscoveryTrustDoesNotAuthorizePackageInstall(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	m.usePackageManager, m.packageName, m.packageManager = true, "printmaster-agent", "apt"
	if err := m.executeUpdate(context.Background(), fixtureManifest(&UpdateManifest{Version: "1.1.0"})); !errors.Is(err, ErrPublisherTrust) {
		t.Fatalf("unauthenticated package installation allowed: %v", err)
	}
}

func TestAuthorizationReceivesExactCandidateAndDoesNotAliasTransport(t *testing.T) {
	t.Parallel()
	manifest := fixtureManifest(&UpdateManifest{Version: "1.1.0", SizeBytes: 123, SHA256: "expected hash"})
	manager := lifecycleManager(t, &rawManifestClient{mockUpdateClient{manifest: manifest}}, nil)
	var actual updatetrust.Target
	manager.publisherVerifier = verifierFunc(func(_ context.Context, target updatetrust.Target) error {
		actual = target
		return nil
	})
	candidate, err := manager.fetchManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := updatetrust.Target{
		Component: "agent", Channel: "stable", Version: "1.1.0", Platform: manager.platform,
		Arch: manager.arch, Format: "binary", SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes,
	}
	if actual != expected {
		t.Fatalf("verified target = %+v, want %+v", actual, expected)
	}
	manifest.SHA256 = "transport mutated after return"
	if candidate.SHA256 != expected.SHA256 {
		t.Fatal("transport retained mutable candidate ownership")
	}
}

func TestExecutionCannotBypassPublisherAuthorization(t *testing.T) {
	t.Parallel()
	manager := lifecycleManager(t, &mockUpdateClient{}, nil)
	manager.publisherVerifier = nil
	if err := manager.executeUpdate(context.Background(), fixtureManifest(&UpdateManifest{Version: "1.1.0"})); !errors.Is(err, ErrPublisherTrust) {
		t.Fatalf("direct execution bypassed authorization: %v", err)
	}
}
