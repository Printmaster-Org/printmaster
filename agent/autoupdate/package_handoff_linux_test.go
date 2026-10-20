package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestPrivilegedRepositoryPackageIndependentTrustAndLock(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-side package intake requires root")
	}
	body := []byte("independently authorized package")
	sum := sha256.Sum256(body)
	manifest := &UpdateManifest{Component: "agent", Channel: "stable", Version: "1.1.0",
		Platform: "linux", Arch: runtime.GOARCH, Format: "deb",
		SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
	_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
	server := httptest.NewServer(http.FileServer(http.Dir(metadataDir)))
	defer server.Close()
	root, err := os.ReadFile(filepath.Join(metadataDir, "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "1.1.0",
		Platform: "linux", Arch: runtime.GOARCH, Format: "deb", SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes}
	sourcePath := filepath.Join(t.TempDir(), "package")
	if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	state := filepath.Join(t.TempDir(), "root-state")
	calls := 0
	consumer := func(path string, authorization updatetrust.Authorization) error {
		calls++
		if authorization.Target != target {
			t.Fatalf("wrong root-side authorization: %+v", authorization)
		}
		if err := os.WriteFile(sourcePath, []byte("changed service input"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(body) {
			t.Fatalf("root snapshot changed with service input: %q %v", got, err)
		}
		if err := WithPrivilegedRepositoryPackage(context.Background(), root, server.URL, state, source, target,
			func(string, updatetrust.Authorization) error {
				t.Fatal("concurrent handoff reached consumer")
				return nil
			}); err == nil {
			t.Fatal("concurrent privileged handoff accepted")
		}
		return nil
	}
	if err := WithPrivilegedRepositoryPackage(context.Background(), root, server.URL, state, source, target, consumer); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("root consumer calls=%d", calls)
	}
	if err := checkRootPackageTree(state); err != nil {
		t.Fatalf("root trust state permissions: %v", err)
	}
	if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	// Root trust loss must not silently bootstrap over an existing cache.
	if err := os.Remove(filepath.Join(state, "metadata.state.json")); err != nil {
		t.Fatal(err)
	}
	if err := WithPrivilegedRepositoryPackage(context.Background(), root, server.URL, state, source, target, consumer); err == nil {
		t.Fatal("missing root-side integrity record reset trusted metadata")
	}
	if calls != 1 {
		t.Fatal("corrupt root trust reached consumer")
	}
}

func TestPrivilegedRepositoryPackageRejectsUntrustedState(t *testing.T) {
	t.Parallel()
	state := filepath.Join(t.TempDir(), "state")
	target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "1.1.0",
		Platform: "linux", Arch: runtime.GOARCH, Format: "deb", SizeBytes: 7}
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	called := false
	consume := func(string, updatetrust.Authorization) error { called = true; return nil }
	if err := WithPrivilegedRepositoryPackage(context.Background(), nil, "https://invalid.example", state, file, target, consume); err == nil {
		t.Fatal("unprovisioned or non-root handoff accepted")
	}
	if called {
		t.Fatal("untrusted request reached consumer")
	}
	if os.Geteuid() != 0 {
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatalf("non-root request modified helper state: %v", err)
		}
	}
}

func TestRootPackageTreeRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("root-owned state qualification requires root; non-root rejection is tested separately")
	}
	for _, mode := range []string{"private", "public directory", "writable file", "symlink", "hardlink", "foreign owner"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "state")
			if err := os.WriteFile(file, []byte("state"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "public directory":
				err = os.Chmod(dir, 0o755)
			case "writable file":
				err = os.Chmod(file, 0o660)
			case "symlink":
				err = os.Symlink(file, filepath.Join(dir, "link"))
			case "hardlink":
				err = os.Link(file, filepath.Join(dir, "link"))
			case "foreign owner":
				err = os.Chown(file, 65534, 65534)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = checkRootPackageTree(dir)
			if (err == nil) != (mode == "private") {
				t.Fatalf("state %s: %v", mode, err)
			}
		})
	}
}
