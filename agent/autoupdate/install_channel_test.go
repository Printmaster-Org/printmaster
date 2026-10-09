package autoupdate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"printmaster/common/updatepolicy"
)

// installChannelClient records the channel and explicit flag of each manifest
// request, then stops the operation with its configured error.
type installChannelClient struct {
	mockUpdateClient
	calls    int
	channel  string
	explicit bool
}

func (c *installChannelClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	c.calls++
	c.channel = channel
	c.explicit = updatepolicy.HasExplicitChannel(ctx)
	return c.manifest, c.err
}

// telemetryRecorder captures reported telemetry payloads.
type telemetryRecorder struct {
	mu       sync.Mutex
	payloads []TelemetryPayload
	reported chan struct{}
}

func (r *telemetryRecorder) ReportUpdateStatus(ctx context.Context, payload TelemetryPayload) error {
	r.mu.Lock()
	r.payloads = append(r.payloads, payload)
	r.mu.Unlock()
	r.reported <- struct{}{}
	return nil
}

// installMethod applies a fixture install method to a manager.
type installMethod struct {
	name    string
	msi     bool
	pkgMgr  string
	stable  bool // true when the method can only follow Stable
	display string
}

var installMethods = []installMethod{
	{name: "binary", stable: false},
	{name: "msi", msi: true, stable: true, display: "MSI"},
	{name: "apt", pkgMgr: "apt", stable: true, display: "apt-managed package"},
}

func (im installMethod) apply(m *Manager) {
	m.useMSI = im.msi
	if im.pkgMgr != "" {
		m.usePackageManager = true
		m.packageManager = im.pkgMgr
		m.packageName = "printmaster-agent"
	}
}

func newChannelTestManager(t *testing.T, im installMethod, managedChannel string, client UpdateClient, telemetry TelemetrySink) *Manager {
	t.Helper()
	manager, err := NewManager(Options{
		Enabled:         true,
		CurrentVersion:  "1.0.0",
		Channel:         "stable",
		ChannelProvider: func() string { return managedChannel },
		DataDir:         t.TempDir(),
		ServerClient:    client,
		TelemetrySink:   telemetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	im.apply(manager)
	return manager
}

func TestStableOnlyInstallsPinScheduledChecksToStable(t *testing.T) {
	t.Parallel()
	for _, im := range installMethods {
		im := im
		t.Run(im.name, func(t *testing.T) {
			t.Parallel()
			client := &installChannelClient{mockUpdateClient: mockUpdateClient{err: errors.New("fixture stops after request")}}
			manager := newChannelTestManager(t, im, "beta", client, nil)

			_ = manager.CheckNow(context.Background())

			wantChannel := "beta"
			if im.stable {
				wantChannel = "stable"
			}
			if client.channel != wantChannel {
				t.Fatalf("requested channel %q, want %q", client.channel, wantChannel)
			}
			// Stable-only installs must mark Stable explicit so a Fleet-managed
			// Beta setting on the Server cannot override it.
			if client.explicit != im.stable {
				t.Fatalf("explicit = %v, want %v", client.explicit, im.stable)
			}

			status := manager.Status()
			if status.Channel != wantChannel {
				t.Fatalf("status channel %q, want %q", status.Channel, wantChannel)
			}
			if im.stable != (status.ChannelNote != "") {
				t.Fatalf("channel note %q, want note=%v", status.ChannelNote, im.stable)
			}
			if im.stable && !strings.Contains(status.ChannelNote, im.display) {
				t.Fatalf("channel note %q does not name install method %q", status.ChannelNote, im.display)
			}
		})
	}
}

func TestStableOnlyInstallHasNoNoteWhenStableSelected(t *testing.T) {
	t.Parallel()
	manager := newChannelTestManager(t, installMethods[1], "stable", &installChannelClient{}, nil)
	if note := manager.Status().ChannelNote; note != "" {
		t.Fatalf("unexpected channel note %q", note)
	}
}

func TestExplicitPrereleaseChannelRejectedForStableOnlyInstalls(t *testing.T) {
	t.Parallel()
	for _, im := range installMethods[1:] {
		im := im
		for _, channel := range []string{"beta", "dev"} {
			channel := channel
			t.Run(im.name+"/"+channel, func(t *testing.T) {
				t.Parallel()
				client := &installChannelClient{}
				telemetry := &telemetryRecorder{reported: make(chan struct{}, 1)}
				manager := newChannelTestManager(t, im, "stable", client, telemetry)

				err := manager.ForceInstallLatestFromChannel(context.Background(), "test", channel)
				if !errors.Is(err, errChannelUnsupported) {
					t.Fatalf("err = %v, want errChannelUnsupported", err)
				}
				if client.calls != 0 {
					t.Fatalf("manifest requested %d times; rejected channel must not reach the Server", client.calls)
				}
				<-telemetry.reported
				telemetry.mu.Lock()
				defer telemetry.mu.Unlock()
				if got := telemetry.payloads[0].ErrorCode; got != ErrCodeChannelUnsupported {
					t.Fatalf("telemetry error code %q, want %q", got, ErrCodeChannelUnsupported)
				}
			})
		}
	}
}

func TestExplicitStableAllowedForStableOnlyInstalls(t *testing.T) {
	t.Parallel()
	client := &installChannelClient{mockUpdateClient: mockUpdateClient{err: errors.New("fixture stops after request")}}
	manager := newChannelTestManager(t, installMethods[1], "beta", client, nil)

	err := manager.ForceInstallLatestFromChannel(context.Background(), "test", "stable")
	if errors.Is(err, errChannelUnsupported) {
		t.Fatalf("explicit Stable rejected: %v", err)
	}
	if client.calls != 1 || client.channel != "stable" {
		t.Fatalf("calls=%d channel=%q, want one Stable request", client.calls, client.channel)
	}
}

func TestPackageManagerUpdateRequiresExactVersion(t *testing.T) {
	t.Parallel()
	manager := &Manager{usePackageManager: true, packageManager: "dnf", packageName: "printmaster-agent"}
	if err := manager.applyUpdateViaPackageManager("  ", func(int, string) {}); err == nil {
		t.Fatal("versionless package manager update must be rejected")
	}
}

func TestApplyUpdateRefusesBinarySwapForPackageManagedInstall(t *testing.T) {
	t.Parallel()
	manager := &Manager{binaryPath: "/usr/bin/printmaster-agent", usePackageManager: true, packageManager: "dnf", packageName: "printmaster-agent"}
	err := manager.applyUpdate("/tmp/staged")
	if err == nil || !strings.Contains(err.Error(), "dnf") {
		t.Fatalf("err = %v, want refusal naming the package manager", err)
	}
}
