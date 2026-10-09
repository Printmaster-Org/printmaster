package autoupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
)

// mockUpdateClient implements UpdateClient for testing.
type mockUpdateClient struct {
	manifest *UpdateManifest
	err      error
}

func (m *mockUpdateClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	return m.manifest, m.err
}

func (m *mockUpdateClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	return 0, m.err
}

func (m *mockUpdateClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	return 0, m.err
}

// mockPolicyProvider implements PolicyProvider for testing.
type mockPolicyProvider struct {
	spec    updatepolicy.PolicySpec
	source  updatepolicy.PolicySource
	enabled bool
}

func (m *mockPolicyProvider) EffectivePolicy() (updatepolicy.PolicySpec, updatepolicy.PolicySource, bool) {
	return m.spec, m.source, m.enabled
}

func TestNewManager(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       "linux",
		Arch:           "amd64",
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   &mockUpdateClient{},
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if manager == nil {
		t.Fatal("NewManager() returned nil manager")
	}

	status := manager.Status()
	if status.Status != StatusIdle {
		t.Errorf("Status.Status = %q, want %q", status.Status, StatusIdle)
	}
	if status.CurrentVersion != "1.0.0" {
		t.Errorf("Status.CurrentVersion = %q, want %q", status.CurrentVersion, "1.0.0")
	}
}

func TestNewManagerDisabled(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	opts := Options{
		Enabled:        false,
		CurrentVersion: "1.0.0",
		DataDir:        dataDir,
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if manager.enabled {
		t.Error("Manager should be disabled when Enabled=false")
	}
}

func TestManagerStartStop(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       "linux",
		Arch:           "amd64",
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   &mockUpdateClient{},
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)

	// Give the goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context and stop
	cancel()
	manager.Stop()
}

func TestPolicyAdapter(t *testing.T) {
	t.Parallel()

	configProvider := &mockConfigProvider{
		mode: updatepolicy.AgentOverrideLocal,
		spec: updatepolicy.PolicySpec{
			UpdateCheckDays:    7,
			VersionPinStrategy: updatepolicy.VersionPinMinor,
		},
	}

	adapter := NewPolicyAdapter(configProvider, nil)

	spec, source, enabled := adapter.EffectivePolicy()

	if !enabled {
		t.Error("EffectivePolicy should be enabled with valid local policy")
	}
	if source != updatepolicy.PolicySourceLocal {
		t.Errorf("Source = %v, want %v", source, updatepolicy.PolicySourceLocal)
	}
	if spec.UpdateCheckDays != 7 {
		t.Errorf("UpdateCheckDays = %d, want %d", spec.UpdateCheckDays, 7)
	}
}

func TestPolicyAdapterInheritWithFleet(t *testing.T) {
	t.Parallel()

	configProvider := &mockConfigProvider{
		mode: updatepolicy.AgentOverrideInherit,
		spec: updatepolicy.PolicySpec{UpdateCheckDays: 7},
	}

	fleetProvider := &mockFleetProvider{
		policy: &updatepolicy.FleetUpdatePolicy{
			PolicySpec: updatepolicy.PolicySpec{
				UpdateCheckDays:    14,
				VersionPinStrategy: updatepolicy.VersionPinMajor,
			},
		},
	}

	adapter := NewPolicyAdapter(configProvider, fleetProvider)

	spec, source, enabled := adapter.EffectivePolicy()

	if !enabled {
		t.Error("EffectivePolicy should be enabled with fleet policy")
	}
	if source != updatepolicy.PolicySourceFleet {
		t.Errorf("Source = %v, want %v", source, updatepolicy.PolicySourceFleet)
	}
	if spec.UpdateCheckDays != 14 {
		t.Errorf("UpdateCheckDays = %d, want %d", spec.UpdateCheckDays, 14)
	}
}

func TestPolicyAdapterDisabled(t *testing.T) {
	t.Parallel()

	configProvider := &mockConfigProvider{
		mode: updatepolicy.AgentOverrideNever,
	}

	adapter := NewPolicyAdapter(configProvider, nil)

	_, source, enabled := adapter.EffectivePolicy()

	if enabled {
		t.Error("EffectivePolicy should be disabled with AgentOverrideNever")
	}
	if source != updatepolicy.PolicySourceDisabled {
		t.Errorf("Source = %v, want %v", source, updatepolicy.PolicySourceDisabled)
	}
}

// mockConfigProvider implements AutoUpdateConfigProvider for testing.
type mockConfigProvider struct {
	mode updatepolicy.AgentOverrideMode
	spec updatepolicy.PolicySpec
}

func (m *mockConfigProvider) GetAutoUpdateMode() updatepolicy.AgentOverrideMode {
	return m.mode
}

func (m *mockConfigProvider) GetLocalPolicy() updatepolicy.PolicySpec {
	return m.spec
}

// mockFleetProvider implements FleetPolicyProvider for testing.
type mockFleetProvider struct {
	policy *updatepolicy.FleetUpdatePolicy
}

func (m *mockFleetProvider) GetFleetPolicy() *updatepolicy.FleetUpdatePolicy {
	return m.policy
}

func TestClientAdapter(t *testing.T) {
	t.Parallel()

	// This is a basic smoke test to ensure the adapter compiles and works
	// Full integration tests would require more mocking infrastructure
	adapter := NewClientAdapter(nil)
	if adapter == nil {
		t.Fatal("NewClientAdapter returned nil")
	}
}

func TestTelemetryAdapter(t *testing.T) {
	t.Parallel()

	// Basic smoke test
	adapter := NewTelemetryAdapter(nil)
	if adapter == nil {
		t.Fatal("NewTelemetryAdapter returned nil")
	}
}

func TestManagerForceInstallLatest(t *testing.T) {
	dataDir := t.TempDir()
	binaryName := "agent-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(dataDir, binaryName)

	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}

	payload := []byte("new-agent-binary")
	sum := sha256.Sum256(payload)
	manifest := &UpdateManifest{
		ManifestVersion: "1",
		Component:       "agent",
		Version:         "1.0.0",
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		Channel:         "stable",
		SizeBytes:       int64(len(payload)),
		SHA256:          hex.EncodeToString(sum[:]),
	}

	client := &forceInstallClient{
		manifest: manifest,
		payload:  payload,
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   client,
		PolicyProvider: &mockPolicyProvider{enabled: true},
		BinaryPath:     binaryPath,
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager error = %v", err)
	}
	manager.restartFn = func() error { return nil }
	// Mock the helper launch to avoid spawning a real process that holds files open
	manager.launchHelperFn = func(helperPath string) error { return nil }

	if err := manager.ForceInstallLatest(context.Background(), "test"); err != nil {
		t.Fatalf("ForceInstallLatest error = %v", err)
	}

	updated, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("failed to read updated binary: %v", err)
	}
	if runtime.GOOS == "windows" {
		helperPath := filepath.Join(dataDir, "autoupdate", "update_helper.bat")
		if _, err := os.Stat(helperPath); err != nil {
			t.Fatalf("expected update helper script on windows: %v", err)
		}
	} else if !bytes.Equal(updated, payload) {
		t.Fatalf("expected binary to be replaced with payload")
	}
}

type forceInstallClient struct {
	manifest *UpdateManifest
	payload  []byte
}

func (m *forceInstallClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	return m.manifest, nil
}

func (m *forceInstallClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	if err := os.WriteFile(destPath, m.payload, 0o644); err != nil {
		return 0, err
	}
	return int64(len(m.payload)), nil
}

func (m *forceInstallClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	// Call progress callback at 100% if provided
	if progressCb != nil {
		progressCb(100, int64(len(m.payload)))
	}
	if err := os.WriteFile(destPath, m.payload, 0o644); err != nil {
		return 0, err
	}
	return int64(len(m.payload)), nil
}
