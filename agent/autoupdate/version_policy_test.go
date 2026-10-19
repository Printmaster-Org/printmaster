package autoupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

type countedInstallClient struct {
	forceInstallClient
	downloads int
}

func (c *countedInstallClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, path string, resume int64, callback DownloadProgressCallback) (int64, error) {
	c.downloads++
	return c.forceInstallClient.DownloadArtifactWithProgress(ctx, manifest, path, resume, callback)
}

func manifestForFile(t *testing.T, version, channel, path string) *UpdateManifest {
	t.Helper()
	identity, err := fileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureManifest(&UpdateManifest{Version: version, Channel: channel, SHA256: identity.SHA256, SizeBytes: identity.SizeBytes})
}

func TestApplicationPolicyCannotBeBypassedByEntryPoint(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"scheduled", "check", "force", "channel", "retry", "direct"} {
		for _, tc := range []struct {
			name, current, target string
			policy                updatepolicy.PolicySpec
		}{
			{name: "downgrade", current: "1.1.0", target: "1.0.0"},
			{name: "beta to older stable", current: "1.1.0-beta.2", target: "1.0.0"},
			{name: "unknown installed", current: "custom-build", target: "1.2.0"},
			{name: "unknown target", current: "1.0.0", target: "custom-build"},
			{name: "major pin", current: "1.0.0", target: "2.0.0", policy: updatepolicy.PolicySpec{VersionPinStrategy: updatepolicy.VersionPinMajor}},
			{name: "minor pin", current: "1.0.0", target: "1.1.0", policy: updatepolicy.PolicySpec{VersionPinStrategy: updatepolicy.VersionPinMinor}},
			{name: "patch pin", current: "1.0.0", target: "1.0.1", policy: updatepolicy.PolicySpec{VersionPinStrategy: updatepolicy.VersionPinPatch}},
			{name: "exact pin", current: "1.0.0", target: "1.2.0", policy: updatepolicy.PolicySpec{TargetVersion: "1.1.0"}},
			{name: "invalid pin", current: "1.0.0", target: "1.2.0", policy: updatepolicy.PolicySpec{TargetVersion: "oops"}},
			{name: "invalid pin strategy", current: "1.0.0", target: "1.2.0", policy: updatepolicy.PolicySpec{VersionPinStrategy: "oops"}},
		} {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				client := &countedInstallClient{forceInstallClient: forceInstallClient{manifest: fixtureManifest(&UpdateManifest{Version: tc.target})}}
				m := lifecycleManager(t, client, nil)
				m.currentVersion, m.currentSemver = tc.current, nil
				m.currentSemver, _ = parseSemverVersion(tc.current)
				m.policyProvider = &mockPolicyProvider{enabled: true, spec: tc.policy}
				var err error
				switch entry {
				case "scheduled":
					err = m.performCheck(context.Background())
				case "check":
					err = m.CheckNow(context.Background())
				case "force":
					err = m.ForceInstallLatest(context.Background(), "test")
				case "channel":
					err = m.ForceInstallLatestFromChannel(context.Background(), "test", "stable")
				case "retry":
					_ = m.ForceInstallLatest(context.Background(), "first attempt")
					err = m.ForceInstallLatest(context.Background(), "retry")
				case "direct":
					err = m.executeUpdate(context.Background(), client.manifest)
				}
				// Scheduled policy exclusions are no-op, not failed installations.
				scheduledExclusion := (entry == "scheduled" || entry == "check") &&
					(tc.name == "major pin" || tc.name == "minor pin" || tc.name == "patch pin" || tc.name == "exact pin")
				if !scheduledExclusion && !errors.Is(err, ErrApplicationPolicy) {
					t.Fatalf("candidate was not rejected by application policy: %v", err)
				}
				if scheduledExclusion && err != nil {
					t.Fatalf("scheduled exclusion: %v", err)
				}
				if client.downloads != 0 || m.Status().UpdateAvailable {
					t.Fatal("disallowed candidate was downloaded or advertised as applicable")
				}
				if body, err := os.ReadFile(m.binaryPath); err != nil || string(body) != "old binary" {
					t.Fatalf("rejection changed installed binary: %q %v", body, err)
				}
			})
		}
	}
}

func TestForceReinstallRequiresSameArtifactAndRespectsDisabledPolicyPins(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	manifest := manifestForFile(t, "1.0.0", "stable", m.binaryPath)
	if needed, err := m.applicationPolicy(context.Background(), manifest, false); err != nil || needed {
		t.Fatalf("ordinary equal-version check: %v %v", needed, err)
	}
	if needed, err := m.applicationPolicy(context.Background(), manifest, true); err != nil || !needed {
		t.Fatalf("exact manual reinstall rejected: %v %v", needed, err)
	}
	manifest.SizeBytes++
	if _, err := m.applicationPolicy(context.Background(), manifest, true); !errors.Is(err, ErrApplicationPolicy) {
		t.Fatalf("different same-version artifact accepted: %v", err)
	}
	m.policyProvider = &mockPolicyProvider{enabled: false, spec: updatepolicy.PolicySpec{TargetVersion: "1.0.0"}}
	manifest.Version = "1.1.0"
	if _, err := m.applicationPolicy(context.Background(), manifest, true); !errors.Is(err, ErrApplicationPolicy) {
		t.Fatalf("disabled schedule bypassed version pin: %v", err)
	}
}

func TestOperationChannelIsFrozenUntilNextOperation(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	channel := "beta"
	m.channelProvider = func() string { return channel }
	ctx, finish := m.operationContext(context.Background())
	channel = "dev"
	if got := m.operationChannel(ctx); got != "beta" {
		t.Fatalf("in-flight channel changed: %s", got)
	}
	if !updatepolicy.HasExplicitChannel(m.manifestContext(ctx)) {
		t.Fatal("Server can override operation channel")
	}
	finish()
	ctx, finish = m.operationContext(context.Background())
	defer finish()
	if got := m.operationChannel(ctx); got != "dev" {
		t.Fatalf("next operation did not pick up changed channel: %s", got)
	}
}

func TestSignedDevOrderingControlsInstallation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, current, target string
		older                 bool
	}{
		{name: "lower lexical hash is newer", current: "1.1.0-dev.fffffff", target: "1.1.0-dev.0000123"},
		{name: "higher lexical hash is newer", current: "1.1.0-dev.0000123", target: "1.1.0-dev.fffffff"},
		{name: "higher lexical hash is older", current: "1.1.0-dev.0000123", target: "1.1.0-dev.fffffff", older: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte("new Dev binary")
			file := filepath.Join(t.TempDir(), "new")
			if err := os.WriteFile(file, body, 0o600); err != nil {
				t.Fatal(err)
			}
			candidate := manifestForFile(t, tc.target, "dev", file)
			client := &countedInstallClient{forceInstallClient: forceInstallClient{manifest: candidate, payload: body}}
			m := lifecycleManager(t, client, nil)
			m.currentVersion, m.currentSemver = tc.current, nil
			m.currentSemver, _ = parseSemverVersion(tc.current)
			m.channel = "dev"
			baseline := manifestForFile(t, tc.current, "dev", m.binaryPath)
			baselineTarget := updatetrust.PublishTarget{Target: m.manifestTarget(context.Background(), baseline), File: m.binaryPath}
			if tc.older {
				candidateTarget := updatetrust.PublishTarget{Target: m.manifestTarget(context.Background(), candidate), File: file}
				m.publisherVerifier, _ = runtimeTrustRepository(t, baseline, []byte("old binary"), time.Now(), candidateTarget)
			} else {
				m.publisherVerifier, _ = runtimeTrustRepository(t, candidate, body, time.Now(), baselineTarget)
			}
			m.launchHelperFn = func(string) error { return nil }
			m.restartFn = func() error { return nil }
			err := m.ForceInstallLatest(context.Background(), "test")
			if tc.older {
				if !errors.Is(err, ErrApplicationPolicy) || client.downloads != 0 {
					t.Fatalf("signed older Dev build installed: %v downloads=%d", err, client.downloads)
				}
			} else if err != nil || client.downloads != 1 {
				t.Fatalf("newer signed Dev build not installed: %v downloads=%d", err, client.downloads)
			}
			if !tc.older && runtime.GOOS != "windows" {
				if got, err := os.ReadFile(m.binaryPath); err != nil || string(got) != string(body) {
					t.Fatalf("wrong installed Dev artifact: %q %v", got, err)
				}
			}
		})
	}
}

func TestDevOrderingFailsClosedWithoutAuthenticatedBaseline(t *testing.T) {
	t.Parallel()
	m := lifecycleManager(t, &mockUpdateClient{}, nil)
	m.currentVersion = "1.1.0-dev.fff"
	m.currentSemver, _ = parseSemverVersion(m.currentVersion)
	m.channel = "dev"
	m.publisherVerifier = nil
	manifest := fixtureManifest(&UpdateManifest{Version: "1.1.0-dev.0000123", Channel: "dev"})
	for _, manual := range []bool{false, true} {
		if _, err := m.applicationPolicy(context.Background(), manifest, manual); !errors.Is(err, ErrApplicationPolicy) ||
			!strings.Contains(err.Error(), "publisher-authenticated Dev release ordering is unavailable") {
			t.Fatalf("missing authenticated Dev ordering accepted: %v", err)
		}
	}

}

type sequenceVerifier map[string]int64

func (v sequenceVerifier) Authorize(_ context.Context, target updatetrust.Target) (updatetrust.Authorization, error) {
	sequence, exists := v[target.Version]
	if !exists {
		return updatetrust.Authorization{}, errors.New("installed baseline not in publisher inventory")
	}
	return updatetrust.Authorization{Target: target, ReleaseSequence: sequence}, nil
}

func TestDevReleaseSequencesMustBePresentAndStrictlyIncreasing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		sequences sequenceVerifier
	}{
		{name: "missing baseline", sequences: sequenceVerifier{"1.1.0-dev.bbbbbbb": 3}},
		{name: "legacy baseline", sequences: sequenceVerifier{"1.1.0-dev.aaaaaaa": 0, "1.1.0-dev.bbbbbbb": 3}},
		{name: "legacy candidate", sequences: sequenceVerifier{"1.1.0-dev.aaaaaaa": 2, "1.1.0-dev.bbbbbbb": 0}},
		{name: "same sequence", sequences: sequenceVerifier{"1.1.0-dev.aaaaaaa": 2, "1.1.0-dev.bbbbbbb": 2}},
		{name: "older sequence", sequences: sequenceVerifier{"1.1.0-dev.aaaaaaa": 2, "1.1.0-dev.bbbbbbb": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			m.currentVersion, m.channel = "1.1.0-dev.aaaaaaa", "dev"
			m.currentSemver, _ = parseSemverVersion(m.currentVersion)
			m.publisherVerifier = tc.sequences
			manifest := manifestForFile(t, "1.1.0-dev.bbbbbbb", "dev", m.binaryPath)
			for _, manual := range []bool{false, true} {
				if allowed, err := m.applicationPolicy(context.Background(), manifest, manual); allowed || !errors.Is(err, ErrApplicationPolicy) {
					t.Fatalf("invalid release ordering accepted: %v %v", allowed, err)
				}
			}
		})
	}
}
