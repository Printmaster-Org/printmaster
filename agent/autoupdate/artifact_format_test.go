package autoupdate

import (
	"context"
	"errors"
	"testing"
)

// formatTestClient records the artifact format each manifest request asked for.
type formatTestClient struct {
	mockUpdateClient
	format string
}

func (c *formatTestClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	c.format = format
	return c.manifest, c.err
}

func TestManagerRequestsArtifactFormatForInstallMethod(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		useMSI bool
		want   string
	}{
		{"binary install", false, "binary"},
		{"msi install", true, "msi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &formatTestClient{mockUpdateClient: mockUpdateClient{err: errors.New("fixture stops after request")}}
			manager, err := NewManager(Options{Enabled: true, CurrentVersion: "1.0.0", Channel: "stable", DataDir: t.TempDir(), ServerClient: client})
			if err != nil {
				t.Fatal(err)
			}
			manager.useMSI = tc.useMSI
			_ = manager.CheckNow(context.Background())
			if client.format != tc.want {
				t.Fatalf("requested format %q, want %q", client.format, tc.want)
			}
		})
	}
}

func TestValidateManifestFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		manifest  string
		requested string
		ok        bool
	}{
		{"binary from legacy server", "", "binary", true},
		{"msi from legacy server", "", "msi", false},
		{"matching binary", "binary", "binary", true},
		{"matching msi", "MSI", "msi", true},
		{"binary for msi install", "binary", "msi", false},
		{"msi for binary install", "msi", "binary", false},
		{"unknown format", "zip", "binary", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{useMSI: tc.requested == "msi"}
			err := validateManifestFormat(&UpdateManifest{Format: tc.manifest}, manager.artifactFormat())
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && manifestErrorCode(err) != ErrCodeManifestError {
				t.Fatalf("format error reported as %s", manifestErrorCode(err))
			}
		})
	}
}
