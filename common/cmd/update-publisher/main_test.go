package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"printmaster/common/updatetrust"
)

func TestPublisherRejectsInvalidOperationBeforeCredentials(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"-mode", "invalid"},
		{"-mode", "check", "-bootstrap"},
		{"-mode", "preflight", "-bootstrap", "-state-repository", "owner/repo", "-public-url", "https://example.com", "-externally-serialized"},
		{"-mode", "preflight", "-inventory", "inventory.json", "-state-repository", "owner/repo", "-public-url", "https://example.com", "-externally-serialized"},
		{"-mode", "publish", "-state-repository", "owner/production", "-public-url", "https://example.com", "-mirror", "mirror", "-work", "work", "-externally-serialized", "-rehearsal-interrupt"},
	} {
		err := run(args, func(string) string { t.Fatal("invalid operation read credentials"); return "" }, io.Discard)
		if err == nil {
			t.Fatal("invalid publisher operation accepted")
		}
	}
}

func TestReleaseOnlyRejectsBootstrapAndDevFlagsBeforeCredentials(t *testing.T) {
	base := []string{
		"-mode", "publish", "-release-only", "-release-channel", "beta", "-release-version", "0.32.0-beta.4",
		"-inventory", "inventory.json", "-work", "work", "-mirror", "mirror",
		"-state-repository", "owner/repo", "-public-url", "https://example.com", "-externally-serialized",
	}
	for _, extra := range [][]string{{"-bootstrap"}, {"-dev-only"}, {"-dev-source-sha", strings.Repeat("a", 40)}} {
		args := append(append([]string(nil), base...), extra...)
		err := run(args, func(string) string { t.Fatal("invalid release mode read credentials"); return "" }, io.Discard)
		if err == nil {
			t.Fatalf("release-only accepted conflicting flags %v", extra)
		}
	}
}

func TestDevInventoryRequiresPinnedBaselineAndCurrentBuild(t *testing.T) {
	const sourceSHA = "abcdef0123456789abcdef0123456789abcdef01"
	const version = "0.32.0-dev.abcdef0"
	registry, err := loadDevBaselineRegistry()
	if err != nil {
		t.Fatal(err)
	}
	platforms := []struct{ platform, arch string }{
		{"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
	}
	targets := make([]updatetrust.PublishTarget, 0, (len(registry.Releases)+1)*len(platforms))
	for _, release := range registry.Releases {
		for _, item := range platforms {
			targets = append(targets, updatetrust.PublishTarget{Target: updatetrust.Target{
				Component: "agent", Channel: "dev", Version: release.Version, Platform: item.platform,
				Arch: item.arch, Format: "binary", SHA256: release.Digests[item.platform+"/"+item.arch], SizeBytes: 1024,
			}})
		}
	}
	for _, item := range platforms {
		targets = append(targets, updatetrust.PublishTarget{Target: updatetrust.Target{
			Component: "agent", Channel: "dev", Version: version, Platform: item.platform,
			Arch: item.arch, Format: "binary", SHA256: strings.Repeat("a", 64), SizeBytes: 1024,
		}})
	}
	if err := validateDevInventory(targets, sourceSHA, version); err != nil {
		t.Fatalf("valid pinned baselines/current inventory rejected: %v", err)
	}

	wrongBaseline := append([]updatetrust.PublishTarget(nil), targets...)
	wrongBaseline[0].Target.SHA256 = strings.Repeat("b", 64)
	if err := validateDevInventory(wrongBaseline, sourceSHA, version); err == nil {
		t.Fatal("wrong pinned baseline digest accepted")
	}
	missing := targets[:len(targets)-1]
	if err := validateDevInventory(missing, sourceSHA, version); err == nil {
		t.Fatal("incomplete platform inventory accepted")
	}
	if err := validateDevInventory(targets, fmt.Sprintf("%040x", 1), version); err == nil {
		t.Fatal("current version not bound to source SHA accepted")
	}
}

func TestAgentReleaseInventoryRequiresExactFivePlatformBinaryMatrix(t *testing.T) {
	const version = "0.32.0-beta.4"
	platforms := []struct{ platform, arch string }{
		{"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
	}
	targets := make([]updatetrust.PublishTarget, 0, len(platforms))
	for _, item := range platforms {
		targets = append(targets, updatetrust.PublishTarget{Target: updatetrust.Target{
			Component: "agent", Channel: "beta", Version: version, Platform: item.platform,
			Arch: item.arch, Format: "binary", SHA256: strings.Repeat("a", 64), SizeBytes: 1024,
		}})
	}
	if err := validateAgentReleaseInventory(targets, "beta", version); err != nil {
		t.Fatalf("valid Beta inventory rejected: %v", err)
	}
	wrongChannel := append([]updatetrust.PublishTarget(nil), targets...)
	wrongChannel[0].Target.Channel = "dev"
	if err := validateAgentReleaseInventory(wrongChannel, "beta", version); err == nil {
		t.Fatal("Dev target accepted in Beta inventory")
	}
	wrongVersion := append([]updatetrust.PublishTarget(nil), targets...)
	wrongVersion[0].Target.Version = "0.32.0-beta.5"
	if err := validateAgentReleaseInventory(wrongVersion, "beta", version); err == nil {
		t.Fatal("different Beta version accepted")
	}
	if err := validateAgentReleaseInventory(targets[:len(targets)-1], "beta", version); err == nil {
		t.Fatal("incomplete Beta matrix accepted")
	}
	if err := validateAgentReleaseInventory(targets, "beta", "0.32.0-beta.04"); err == nil {
		t.Fatal("noncanonical Beta sequence accepted")
	}
	stableTargets := append([]updatetrust.PublishTarget(nil), targets...)
	for i := range stableTargets {
		stableTargets[i].Target.Channel = "stable"
		stableTargets[i].Target.Version = "0.32.0"
	}
	if err := validateAgentReleaseInventory(stableTargets, "stable", "0.32.0"); err != nil {
		t.Fatalf("valid Stable inventory rejected: %v", err)
	}
	if err := validateAgentReleaseInventory(stableTargets, "stable", version); err == nil {
		t.Fatal("Beta version accepted in Stable inventory")
	}
}

func TestPreflightRejectsInvalidRootBeforeRemoteCredentials(t *testing.T) {
	t.Parallel()
	err := run([]string{"-mode", "preflight", "-state-repository", "owner/repo",
		"-public-url", "https://example.com", "-externally-serialized"}, func(name string) string {
		if name != "UPDATE_ROOT_JSON" {
			t.Fatalf("invalid root accessed credential %s", name)
		}
		return "null"
	}, io.Discard)
	if err == nil {
		t.Fatal("preflight accepted an unprovisioned root")
	}
}

func TestDevOnlyRejectsRootChangeBeforePublisherCredentials(t *testing.T) {
	args := []string{
		"-mode", "publish", "-dev-only", "-dev-source-sha", "1234567890abcdef1234567890abcdef12345678",
		"-dev-version", "0.32.0-dev.1234567", "-inventory", "inventory.json",
		"-work", "work", "-mirror", "mirror", "-state-repository", "owner/repo",
		"-public-url", "https://metadata.example", "-externally-serialized",
	}
	err := run(args, func(name string) string {
		if name != "UPDATE_ROOT_JSON" {
			t.Fatalf("root mismatch accessed credential %s", name)
		}
		return "null"
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "root fingerprint changed") {
		t.Fatalf("root transition did not fail before credentials: %v", err)
	}
}

func TestReleaseOnlyRejectsRootChangeBeforePublisherCredentials(t *testing.T) {
	args := []string{
		"-mode", "publish", "-release-only", "-release-channel", "beta",
		"-release-version", "0.32.0-beta.4", "-inventory", "inventory.json",
		"-work", "work", "-mirror", "mirror", "-state-repository", "owner/repo",
		"-public-url", "https://metadata.example", "-externally-serialized",
	}
	err := run(args, func(name string) string {
		if name != "UPDATE_ROOT_JSON" {
			t.Fatalf("root mismatch accessed credential %s", name)
		}
		return "null"
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "publisher root fingerprint changed") {
		t.Fatalf("release publisher did not fail before credentials on root mismatch: %v", err)
	}
}
