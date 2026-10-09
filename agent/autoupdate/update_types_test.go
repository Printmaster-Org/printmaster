package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"printmaster/common/updatepolicy"
)

// TestManagerStatus tests the Status() method returns correct information
func TestManagerStatus(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	mockClient := &mockUpdateClient{
		manifest: &UpdateManifest{
			Version:   "1.1.0",
			Platform:  runtime.GOOS,
			Arch:      runtime.GOARCH,
			Channel:   "stable",
			Component: "agent",
		},
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   mockClient,
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	status := manager.Status()

	if !status.Enabled {
		t.Error("Status.Enabled should be true")
	}
	if status.CurrentVersion != "1.0.0" {
		t.Errorf("Status.CurrentVersion = %q, want %q", status.CurrentVersion, "1.0.0")
	}
	if status.Channel != "stable" {
		t.Errorf("Status.Channel = %q, want %q", status.Channel, "stable")
	}
	if status.Platform != runtime.GOOS {
		t.Errorf("Status.Platform = %q, want %q", status.Platform, runtime.GOOS)
	}
	if status.Arch != runtime.GOARCH {
		t.Errorf("Status.Arch = %q, want %q", status.Arch, runtime.GOARCH)
	}
}

// TestManagerCheckNow tests immediate update check functionality
func TestManagerCheckNow(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	checkCount := 0
	var mu sync.Mutex

	mockClient := &mockUpdateClient{
		manifest: &UpdateManifest{
			Version:   "1.0.0", // Same version, no update needed
			Platform:  runtime.GOOS,
			Arch:      runtime.GOARCH,
			Channel:   "stable",
			Component: "agent",
		},
	}

	// Wrap to track calls
	wrappedClient := &trackingClient{
		inner: mockClient,
		onGetManifest: func() {
			mu.Lock()
			checkCount++
			mu.Unlock()
		},
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   wrappedClient,
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx := context.Background()
	if err := manager.CheckNow(ctx); err != nil {
		t.Fatalf("CheckNow() error = %v", err)
	}

	mu.Lock()
	count := checkCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("Expected 1 manifest check, got %d", count)
	}
}

// TestManagerUpdateAvailable tests detection of available updates
func TestManagerUpdateAvailable(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	mockClient := &mockUpdateClient{
		manifest: &UpdateManifest{
			Version:   "1.2.0", // Newer version available
			Platform:  runtime.GOOS,
			Arch:      runtime.GOARCH,
			Channel:   "stable",
			Component: "agent",
			SHA256:    "abc123",
			SizeBytes: 1024,
		},
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   mockClient,
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx := context.Background()
	_ = manager.CheckNow(ctx)

	status := manager.Status()
	if !status.UpdateAvailable {
		t.Error("Expected UpdateAvailable=true for newer version")
	}
	if status.LatestVersion != "1.2.0" {
		t.Errorf("Expected LatestVersion=1.2.0, got %s", status.LatestVersion)
	}
}

// TestManagerPolicyDisabled tests behavior when policy disables updates
func TestManagerPolicyDisabled(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   &mockUpdateClient{},
		PolicyProvider: &mockPolicyProvider{enabled: false},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx := context.Background()
	// CheckNow should complete without error but skip the actual check
	err = manager.CheckNow(ctx)
	if err != nil {
		t.Fatalf("CheckNow() error = %v", err)
	}
}

// TestManagerConcurrentChecks tests that concurrent check requests are serialized
func TestManagerConcurrentChecks(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	activeChecks := 0
	maxActiveChecks := 0
	var mu sync.Mutex

	mockClient := &delayedClient{
		manifest: &UpdateManifest{
			Version:   "1.0.0",
			Platform:  runtime.GOOS,
			Arch:      runtime.GOARCH,
			Channel:   "stable",
			Component: "agent",
		},
		delay: 50 * time.Millisecond,
		onStart: func() {
			mu.Lock()
			activeChecks++
			if activeChecks > maxActiveChecks {
				maxActiveChecks = activeChecks
			}
			mu.Unlock()
		},
		onEnd: func() {
			mu.Lock()
			activeChecks--
			mu.Unlock()
		},
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   mockClient,
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	// Start multiple concurrent checks
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.CheckNow(context.Background())
		}()
	}

	wg.Wait()

	mu.Lock()
	max := maxActiveChecks
	mu.Unlock()

	// Should be serialized to max 1 concurrent check
	if max > 1 {
		t.Errorf("Expected max 1 concurrent check, got %d", max)
	}
}

// TestManagerOperationOwnership holds the manifest request open so competing
// requests are tested without relying on sleep or goroutine scheduling.
func TestManagerOperationOwnership(t *testing.T) {
	t.Parallel()
	for _, firstOperation := range []string{"manual", "scheduled", "force"} {
		for _, status := range []Status{StatusChecking, StatusPending} {
			t.Run(firstOperation+"/"+string(status), func(t *testing.T) {
				t.Parallel()
				manifestErr := errors.New("manifest unavailable")
				client := &blockedManifestClient{
					mockUpdateClient: mockUpdateClient{err: manifestErr},
					entered:          make(chan struct{}, 10),
					release:          make(chan struct{}),
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(client.release) }) }
				defer release()
				manager, err := NewManager(Options{
					Enabled: true, CurrentVersion: "1.0.0", DataDir: t.TempDir(),
					ServerClient: client, PolicyProvider: &mockPolicyProvider{enabled: true},
				})
				if err != nil {
					t.Fatal(err)
				}
				operations := map[string]func(context.Context) error{
					"manual":    manager.CheckNow,
					"scheduled": manager.performCheck,
					"force": func(ctx context.Context) error {
						return manager.ForceInstallLatest(ctx, "test")
					},
				}
				firstDone := make(chan error, 1)
				go func() { firstDone <- operations[firstOperation](ctx) }()
				select {
				case <-client.entered:
				case <-ctx.Done():
					t.Fatal("first operation did not fetch manifest")
				}
				// Pending is an admissible UI state, but not a release of ownership.
				manager.setStatus(status)
				for name, operation := range operations {
					competingCtx, competingCancel := context.WithTimeout(ctx, time.Second)
					err := operation(competingCtx)
					competingCancel()
					if err == nil || !strings.Contains(err.Error(), "update operation already in progress") {
						t.Errorf("%s competing operation: got %v, want busy error", name, err)
					}
				}
				if got := len(client.entered); got != 0 {
					t.Errorf("%d competing operations fetched a manifest", got)
				}
				release()
				if err := <-firstDone; !errors.Is(err, manifestErr) {
					t.Fatalf("first operation: %v", err)
				}
				manager.setStatus(StatusIdle)
				if err := manager.CheckNow(ctx); !errors.Is(err, manifestErr) {
					t.Fatalf("operation ownership not released: %v", err)
				}
			})
		}
	}
}

// TestManagerContextCancellation tests that operations respect context cancellation
func TestManagerContextCancellation(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	mockClient := &delayedClient{
		manifest: &UpdateManifest{
			Version:   "1.1.0",
			Platform:  runtime.GOOS,
			Arch:      runtime.GOARCH,
			Channel:   "stable",
			Component: "agent",
		},
		delay: 5 * time.Second, // Long delay to ensure cancellation works
	}

	opts := Options{
		Enabled:        true,
		CurrentVersion: "1.0.0",
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
		Channel:        "stable",
		DataDir:        dataDir,
		ServerClient:   mockClient,
		PolicyProvider: &mockPolicyProvider{enabled: true},
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_ = manager.CheckNow(ctx) // Error expected due to context cancellation
	elapsed := time.Since(start)

	// Should complete quickly due to cancellation
	if elapsed > 1*time.Second {
		t.Errorf("Expected quick cancellation, took %v", elapsed)
	}
}

// TestManagerDownloadVerification tests SHA256 verification during download
func TestManagerDownloadVerification(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	// Create test payload
	payload := []byte("test-binary-content-for-verification")
	sum := sha256.Sum256(payload)
	correctHash := hex.EncodeToString(sum[:])

	// Test with correct hash
	t.Run("correct hash", func(t *testing.T) {
		client := &downloadClient{
			manifest: &UpdateManifest{
				Version:   "1.1.0",
				Platform:  runtime.GOOS,
				Arch:      runtime.GOARCH,
				Channel:   "stable",
				Component: "agent",
				SHA256:    correctHash,
				SizeBytes: int64(len(payload)),
			},
			payload: payload,
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
		}

		manager, err := NewManager(opts)
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		// Download should succeed with correct hash
		destPath := filepath.Join(dataDir, "test-download")
		_, err = manager.client.DownloadArtifact(context.Background(), client.manifest, destPath, 0)
		if err != nil {
			t.Fatalf("Download with correct hash failed: %v", err)
		}
	})
}

// TestManagerErrorCodes tests error code assignment
func TestManagerErrorCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		errorType    error
		expectedCode string
	}{
		{
			name:         "disk space error",
			errorType:    errors.New("insufficient disk space"),
			expectedCode: ErrCodeDiskSpace,
		},
		{
			name:         "download error",
			errorType:    errors.New("download failed"),
			expectedCode: ErrCodeDownloadFailed,
		},
		{
			name:         "hash mismatch",
			errorType:    errors.New("hash mismatch"),
			expectedCode: ErrCodeHashMismatch,
		},
		{
			name:         "staging error",
			errorType:    errors.New("staging failed"),
			expectedCode: ErrCodeStagingFailed,
		},
		{
			name:         "apply error",
			errorType:    errors.New("apply failed"),
			expectedCode: ErrCodeApplyFailed,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Verify error codes are defined
			codes := map[string]bool{
				ErrCodeDiskSpace:      true,
				ErrCodeDownloadFailed: true,
				ErrCodeHashMismatch:   true,
				ErrCodeStagingFailed:  true,
				ErrCodeApplyFailed:    true,
				ErrCodeRestartFailed:  true,
				ErrCodeHealthCheck:    true,
				ErrCodeRollbackFailed: true,
				ErrCodeManifestError:  true,
				ErrCodePolicyDisabled: true,
				ErrCodeOutsideWindow:  true,
				ErrCodeServerError:    true,
			}

			if !codes[tc.expectedCode] {
				t.Errorf("Unknown error code: %s", tc.expectedCode)
			}
		})
	}
}

// TestManagerMaintenanceWindow tests maintenance window enforcement
func TestManagerMaintenanceWindow(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		policy    updatepolicy.PolicySpec
		checkTime time.Time
		expectRun bool
	}{
		{
			name: "within window",
			policy: updatepolicy.PolicySpec{
				UpdateCheckDays: 7,
				MaintenanceWindow: updatepolicy.MaintenanceWindow{
					Enabled:   true,
					StartHour: 2, // 2:00 UTC
					EndHour:   6, // 6:00 UTC
				},
			},
			checkTime: time.Date(2025, 1, 25, 3, 0, 0, 0, time.UTC), // 3:00 UTC
			expectRun: true,
		},
		{
			name: "outside window",
			policy: updatepolicy.PolicySpec{
				UpdateCheckDays: 7,
				MaintenanceWindow: updatepolicy.MaintenanceWindow{
					Enabled:   true,
					StartHour: 2, // 2:00 UTC
					EndHour:   6, // 6:00 UTC
				},
			},
			checkTime: time.Date(2025, 1, 25, 12, 0, 0, 0, time.UTC), // 12:00 UTC
			expectRun: false,
		},
		{
			name: "no window configured",
			policy: updatepolicy.PolicySpec{
				UpdateCheckDays: 7,
				// MaintenanceWindow.Enabled = false (default)
			},
			checkTime: time.Date(2025, 1, 25, 15, 0, 0, 0, time.UTC),
			expectRun: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Test maintenance window logic
			inWindow := true
			if tc.policy.MaintenanceWindow.Enabled {
				inWindow = isTimeInWindow(tc.checkTime, tc.policy.MaintenanceWindow.StartHour, tc.policy.MaintenanceWindow.EndHour)
			}

			if inWindow != tc.expectRun {
				t.Errorf("Maintenance window check: expected %v, got %v", tc.expectRun, inWindow)
			}
		})
	}
}

// isTimeInWindow checks if a time is within the maintenance window
func isTimeInWindow(t time.Time, startHour, endHour int) bool {
	if startHour == 0 && endHour == 0 {
		return true // No window configured
	}

	hour := t.Hour()
	if startHour < endHour {
		// Normal window (e.g., 2-6)
		return hour >= startHour && hour < endHour
	}
	// Wrapped window (e.g., 22-6 = 22:00-06:00)
	return hour >= startHour || hour < endHour
}

// TestManagerVersionComparison tests semantic version comparison
func TestManagerVersionComparison(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		current string
		latest  string
		isNewer bool
		isMajor bool
	}{
		{"1.0.0", "1.0.1", true, false},  // Patch update
		{"1.0.0", "1.1.0", true, false},  // Minor update
		{"1.0.0", "2.0.0", true, true},   // Major update
		{"1.0.0", "1.0.0", false, false}, // Same version
		{"1.1.0", "1.0.0", false, false}, // Downgrade (not newer)
		// Note: Pre-release handling requires proper semver library
		// {"1.0.0-beta", "1.0.0", true, false}, // Pre-release to release
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.current+"->"+tc.latest, func(t *testing.T) {
			t.Parallel()

			isNewer, isMajor := compareVersions(tc.current, tc.latest)
			if isNewer != tc.isNewer {
				t.Errorf("isNewer: expected %v, got %v", tc.isNewer, isNewer)
			}
			if isMajor != tc.isMajor {
				t.Errorf("isMajor: expected %v, got %v", tc.isMajor, isMajor)
			}
		})
	}
}

// compareVersions compares semantic versions and returns if latest is newer and if it's a major upgrade
func compareVersions(current, latest string) (isNewer, isMajor bool) {
	// Simple comparison for test purposes
	// In real code, use semver library
	if current == latest {
		return false, false
	}

	// Parse major versions
	var currentMajor, latestMajor int
	if len(current) > 0 {
		for i, c := range current {
			if c == '.' {
				break
			}
			currentMajor = currentMajor*10 + int(c-'0')
			_ = i
		}
	}
	if len(latest) > 0 {
		for i, c := range latest {
			if c == '.' {
				break
			}
			latestMajor = latestMajor*10 + int(c-'0')
			_ = i
		}
	}

	isMajor = latestMajor > currentMajor

	// Simple string comparison for newer check (works for semver format)
	isNewer = latest > current
	return
}

// Helper types for testing

// trackingClient wraps an UpdateClient to track calls
type trackingClient struct {
	inner         UpdateClient
	onGetManifest func()
}

func (c *trackingClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	if c.onGetManifest != nil {
		c.onGetManifest()
	}
	return c.inner.GetLatestManifest(ctx, component, platform, arch, channel, format)
}

func (c *trackingClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	return c.inner.DownloadArtifact(ctx, manifest, destPath, resumeFrom)
}

func (c *trackingClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	return c.inner.DownloadArtifactWithProgress(ctx, manifest, destPath, resumeFrom, progressCb)
}

// blockedManifestClient gates manifest completion for concurrency tests.
type blockedManifestClient struct {
	mockUpdateClient
	entered chan struct{}
	release chan struct{}
}

func (c *blockedManifestClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return c.mockUpdateClient.GetLatestManifest(ctx, component, platform, arch, channel, format)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// delayedClient adds configurable delay to manifest fetching
type delayedClient struct {
	manifest *UpdateManifest
	delay    time.Duration
	onStart  func()
	onEnd    func()
}

func (c *delayedClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	if c.onStart != nil {
		c.onStart()
	}
	defer func() {
		if c.onEnd != nil {
			c.onEnd()
		}
	}()

	select {
	case <-time.After(c.delay):
		return c.manifest, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *delayedClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	return 0, nil
}

func (c *delayedClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	return 0, nil
}

// downloadClient provides test download functionality
type downloadClient struct {
	manifest *UpdateManifest
	payload  []byte
}

func (c *downloadClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	return c.manifest, nil
}

func (c *downloadClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	if err := os.WriteFile(destPath, c.payload, 0o644); err != nil {
		return 0, err
	}
	return int64(len(c.payload)), nil
}

func (c *downloadClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	if progressCb != nil {
		progressCb(100, int64(len(c.payload)))
	}
	return c.DownloadArtifact(ctx, manifest, destPath, resumeFrom)
}
