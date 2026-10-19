package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestExpiredRootMaintenanceIsNotAnExpiryBypass(t *testing.T) {
	t.Parallel()
	offline, routine, public := initialize(t)
	rootPath := filepath.Join(public, "1.root.json")
	data, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := metadata.Root().FromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	root.Signed.Expires = time.Now().Add(-time.Hour)
	root.Signatures = nil
	key, err := updatetrust.LoadSigningKey(filepath.Join(offline, "root.pem"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signature.LoadSigner(key, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Sign(signer); err != nil {
		t.Fatal(err)
	}
	expired, err := root.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	// Model a root that has aged naturally; never rewrite published roots.
	if err := os.WriteFile(rootPath, expired, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check-root", "-root", rootPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("expired root accepted by release guard")
	}
	if err := run([]string{"verify-keys", "-root", rootPath, "-offline", offline, "-routine", routine}, &bytes.Buffer{}); err != nil {
		t.Fatalf("expired root must still allow backup verification: %v", err)
	}
	dir := t.TempDir()
	nextPublic := filepath.Join(dir, "public")
	if err := run([]string{"rotate-root", "-root", rootPath, "-old-key", filepath.Join(offline, "root.pem"),
		"-offline", filepath.Join(dir, "offline"), "-public", nextPublic,
		"-expires", time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("expired root must still authorize maintenance: %v", err)
	}
	if err := run([]string{"check-root", "-root", filepath.Join(nextPublic, "2.root.json")}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func initialize(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	offline, routine, public := filepath.Join(dir, "offline"), filepath.Join(dir, "routine"), filepath.Join(dir, "public")
	var output bytes.Buffer
	if err := run([]string{"init", "-offline", offline, "-routine", routine, "-public", public,
		"-expires", time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "SHA256") || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatalf("must report public fingerprint only: %s", output.String())
	}
	return offline, routine, public
}

func TestProvisioningBackupAndRootRotation(t *testing.T) {
	t.Parallel()
	offline, routine, public := initialize(t)
	rootPath := filepath.Join(public, "1.root.json")
	root, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]int{offline: 1, routine: 3, public: 1} {
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != want {
			t.Fatalf("wrong role separation in %s: %v, %v", dir, files, err)
		}
	}
	for _, dir := range []string{offline, routine, public} {
		checkPrivateDirectory(t, dir)
	}
	for _, args := range [][]string{
		{"check-root", "-root", rootPath},
		{"verify-keys", "-root", rootPath, "-offline", offline, "-routine", routine},
	} {
		if err := run(args, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	// Restored bytes, not just the source workspace, must match every role.
	backup := t.TempDir()
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		source := routine
		if role == "root" {
			source = offline
		}
		data, err := os.ReadFile(filepath.Join(source, role+".pem"))
		if err != nil {
			t.Fatal(err)
		}
		if err := writeExclusive(filepath.Join(backup, role+".pem"), data); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"verify-keys", "-root", rootPath, "-offline", backup, "-routine", backup}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	badOffline, _, _ := initialize(t)
	if err := run([]string{"verify-keys", "-root", rootPath, "-offline", badOffline, "-routine", backup}, &bytes.Buffer{}); err == nil {
		t.Fatal("unrelated restored root key accepted")
	}
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		path := filepath.Join(backup, role+".pem")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		unrelated, err := os.ReadFile(filepath.Join(badOffline, "root.pem"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, unrelated, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"verify-keys", "-root", rootPath, "-offline", offline, "-routine", backup}, &bytes.Buffer{}); err == nil {
			t.Fatalf("unrelated restored %s key accepted", role)
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(backup, "timestamp.pem")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify-keys", "-root", rootPath, "-offline", offline, "-routine", backup}, &bytes.Buffer{}); err == nil {
		t.Fatal("incomplete restored backup accepted")
	}

	keys := make(map[string]ed25519.PrivateKey)
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		keys[role], err = updatetrust.LoadSigningKey(filepath.Join(routine, role+".pem"))
		if err != nil {
			t.Fatal(err)
		}
	}
	repository := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "artifact")
	data := []byte("disposable release")
	if err := os.WriteFile(artifact, data, 0o600); err != nil {
		t.Fatal(err)
	}
	target := updatetrust.Target{Component: "agent", Channel: "dev", Version: "0.32.0-dev.test",
		Platform: "windows", Arch: "amd64", Format: "binary", SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: int64(len(data))}
	if err := updatetrust.Publish(updatetrust.PublishOptions{Directory: repository, Root: root, Keys: keys,
		Targets: []updatetrust.PublishTarget{{Target: target, File: artifact}}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	options := updatetrust.Options{TrustedRoot: root, MetadataURL: server.URL, CacheDir: filepath.Join(t.TempDir(), "metadata")}
	client, err := updatetrust.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	nextDir := t.TempDir()
	nextOffline, nextPublic := filepath.Join(nextDir, "offline"), filepath.Join(nextDir, "public")
	args := []string{"rotate-root", "-root", rootPath, "-old-key", filepath.Join(offline, "root.pem"),
		"-offline", nextOffline, "-public", nextPublic, "-expires", time.Now().Add(730 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	wrongSigner := append([]string(nil), args...)
	wrongSigner[4] = filepath.Join(badOffline, "root.pem")
	if err := run(wrongSigner, &bytes.Buffer{}); err == nil {
		t.Fatal("rotation accepted an unauthorized previous signer")
	}
	if _, err := os.Lstat(nextOffline); !os.IsNotExist(err) {
		t.Fatal("failed authorization created rotation outputs")
	}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(filepath.Join(nextPublic, "2.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, err := metadata.Root().FromBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	nextRoot, err := metadata.Root().FromBytes(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldRoot.VerifyDelegate("root", nextRoot); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify-keys", "-root", filepath.Join(nextPublic, "2.root.json"),
		"-offline", nextOffline, "-routine", routine}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := updatetrust.Publish(updatetrust.PublishOptions{Directory: repository, Root: next, Keys: keys}); err != nil {
		t.Fatal(err)
	}
	client, err = updatetrust.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Verify(context.Background(), target); err != nil {
		t.Fatalf("restart with pinned root 1 must accept published root 2: %v", err)
	}
	cached, err := os.ReadFile(filepath.Join(options.CacheDir, "root.json"))
	if err != nil || !bytes.Equal(cached, next) {
		t.Fatalf("rotated root not persisted: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(offline, "root.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing ceremony output was overwritten")
	}
	after, err := os.ReadFile(filepath.Join(offline, "root.pem"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("original key changed")
	}
}

func TestKeyOperationRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"init", "-expires", "yesterday"},
		{"check-root"}, {"verify-keys"}, {"check-root", "extra"}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}

	}
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dirs := range [][]string{{existing}, {""}, {filepath.Join(parent, "same"), filepath.Join(parent, "same")},
		{filepath.Join(parent, "absent", "nested")}} {
		if err := validateNewDirectories(dirs); err == nil {
			t.Fatalf("accepted unsafe directories %v", dirs)
		}
	}
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateNewDirectories([]string{filepath.Join(parent, "keys")}); err == nil {
		t.Fatal("key output inside checkout accepted")
	}
	root := filepath.Join(t.TempDir(), "root.json")
	for _, data := range []string{"null", "{}", "not json"} {
		if err := os.WriteFile(root, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"check-root", "-root", root}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted unprovisioned root %q", data)
		}
	}
}

func TestExclusiveOutputsPreserveExistingBytesAndReportPartialFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "existing")
	if err := writeExclusive(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusive(path, []byte("replacement")); err == nil {
		t.Fatal("existing output overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("existing output changed")
	}
	parent := t.TempDir()
	created := filepath.Join(parent, "first")
	err = createOutputs([]string{created, path}, func() error {
		t.Fatal("must not write keys when output creation fails")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "partial outputs") {
		t.Fatalf("partial ceremony failure must be explicit: %v", err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("partial output should remain for owner review: %v", err)
	}
}
