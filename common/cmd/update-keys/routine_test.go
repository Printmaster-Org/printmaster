package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestRoutineKeyReplacementPreservesOfflineAuthority(t *testing.T) {
	t.Parallel()
	offline, routine, public := initialize(t)
	oldKey := filepath.Join(offline, "root.pem")
	oldBytes, err := os.ReadFile(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	nextRoutine, nextPublic := filepath.Join(parent, "routine"), filepath.Join(parent, "public")
	args := []string{"rotate-routine", "-root", filepath.Join(public, "1.root.json"), "-old-key", oldKey,
		"-routine", nextRoutine, "-public", nextPublic,
		"-expires", time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	nextRoot := filepath.Join(nextPublic, "2.root.json")
	if err := run([]string{"verify-keys", "-root", nextRoot, "-offline", offline, "-routine", nextRoutine}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify-keys", "-root", nextRoot, "-offline", offline, "-routine", routine}, &bytes.Buffer{}); err == nil {
		t.Fatal("old routine keys were not revoked")
	}
	if current, err := os.ReadFile(oldKey); err != nil || !bytes.Equal(current, oldBytes) {
		t.Fatal("offline authority changed")
	}
	files, err := os.ReadDir(nextRoutine)
	if err != nil || len(files) != 3 {
		t.Fatal("new routine output must contain only three role keys")
	}
	for _, file := range files {
		if file.Name() == "root.pem" {
			t.Fatal("offline authority copied into online keys")
		}
		old, err := os.ReadFile(filepath.Join(routine, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		next, err := os.ReadFile(filepath.Join(nextRoutine, file.Name()))
		if err != nil || bytes.Equal(next, old) {
			t.Fatalf("role key was not replaced: %s", file.Name())
		}
	}
	rootData, err := os.ReadFile(nextRoot)
	if err != nil {
		t.Fatal(err)
	}
	root, err := metadata.Root().FromBytes(rootData)
	if err != nil {
		t.Fatal(err)
	}
	if root.Signed.Version != 2 || len(root.Signatures) != 1 {
		t.Fatal("unchanged offline authority should sign exactly one successor root")
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("existing rotation output overwritten")
	}
	checkPrivateDirectory(t, nextRoutine)
	checkPrivateDirectory(t, nextPublic)
}

func TestRoutineKeyReplacementRejectsUnauthorizedOfflineKey(t *testing.T) {
	t.Parallel()
	_, _, public := initialize(t)
	unrelated, _, _ := initialize(t)
	parent := t.TempDir()
	output := filepath.Join(parent, "routine")
	if err := run([]string{"rotate-routine", "-root", filepath.Join(public, "1.root.json"),
		"-old-key", filepath.Join(unrelated, "root.pem"), "-routine", output, "-public", filepath.Join(parent, "public"),
		"-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, &bytes.Buffer{}); err == nil {
		t.Fatal("unrelated offline signer accepted")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("unauthorized rotation created output")
	}
}
