package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestRepositoryPackageHandoffLifetime(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "consumer failure", "invalid package", "nil consumer", "cancelled consumer"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("exact authenticated repository package")
			sum := sha256.Sum256(body)
			target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "1.1.0", Platform: "linux",
				Arch: runtime.GOARCH, Format: "deb", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			verifier, _ := runtimeTrustRepository(t, manifest, body, time.Now())
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			m.platform, m.arch, m.packageManager, m.packageName = "linux", target.Arch, "apt", "printmaster-agent"
			m.publisherVerifier = verifier
			var workspace string
			commands := 0
			arch, err := nativePackageArch(target.Arch, "deb")
			if err != nil {
				t.Fatal(err)
			}
			run := func(_ context.Context, _ time.Duration, dir string, _ []string, tool string, _ ...string) ([]byte, error) {
				commands++
				workspace = dir
				switch tool {
				case "apt-get":
					payload := body
					if mode == "invalid package" {
						payload = []byte("different bytes")
					}
					return nil, os.WriteFile(filepath.Join(dir, "candidate.deb"), payload, 0o600)
				case "dpkg-deb":
					return []byte("printmaster-agent\n1.1.0\n" + arch + "\n"), nil
				default:
					t.Fatalf("unexpected repository command: %s", tool)
					return nil, nil
				}
			}
			failure := errors.New("consumer could not finish")
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var retained *os.File
			var snapshot string
			consume := func(input *os.File, auth updatetrust.Authorization) error {
				calls++
				retained, snapshot = input, input.Name()
				if auth.Target != target || !strings.HasPrefix(snapshot, workspace+string(os.PathSeparator)) {
					t.Fatalf("wrong handoff identity or lifetime: %+v %s", auth.Target, snapshot)
				}
				got, err := io.ReadAll(input)
				if err != nil || string(got) != string(body) {
					t.Fatalf("handoff bytes differ: %q %v", got, err)
				}
				if mode == "consumer failure" {
					return failure
				}
				if mode == "cancelled consumer" {
					cancel()
				}
				return nil
			}
			if mode == "nil consumer" {
				consume = nil
			}
			auth, err := m.withRepositoryPackage(ctx, manifest, run, consume)
			if mode == "valid" {
				if err != nil || auth.Target != target || calls != 1 {
					t.Fatalf("handoff failed: %+v %v calls=%d", auth, err, calls)
				}
			} else if err == nil || auth != (updatetrust.Authorization{}) {
				t.Fatalf("failed handoff returned authority: %+v %v", auth, err)
			}
			if mode == "consumer failure" && (!errors.Is(err, failure) || calls != 1) {
				t.Fatalf("consumer error lost: calls=%d %v", calls, err)
			}
			if mode == "cancelled consumer" && (!errors.Is(err, context.Canceled) || calls != 1) {
				t.Fatalf("consumer cancellation lost: calls=%d %v", calls, err)
			}
			if (mode == "invalid package" || mode == "nil consumer") && calls != 0 {
				t.Fatal("unavailable/unauthorized consumer ran")
			}
			if mode == "nil consumer" && commands != 0 {
				t.Fatal("missing consumer reached repository")
			}
			if retained != nil {
				if _, err := retained.Stat(); err == nil {
					t.Fatal("handoff file remained open after consumer returned")
				}
			}
			for _, path := range []string{snapshot, workspace} {
				if path != "" {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("handoff data not cleaned: %s %v", path, err)
					}
				}
			}
		})
	}
}
