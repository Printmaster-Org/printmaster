package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestPublishingCLIRecoversPinnedStateAndTransitionsRoutineKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keysDir, newKeysDir := filepath.Join(dir, "keys"), filepath.Join(dir, "new-keys")
	for _, path := range []string{keysDir, newKeysDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	makeKeys := func(directory string) (map[string]ed25519.PrivateKey, map[string]crypto.PublicKey) {
		t.Helper()
		keys := make(map[string]ed25519.PrivateKey)
		public := make(map[string]crypto.PublicKey)
		for _, role := range []string{"targets", "snapshot", "timestamp"} {
			pub, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			keys[role], public[role] = key, pub
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, role+".pem"),
				pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return keys, public
	}
	keys, public := makeKeys(keysDir)
	_, rootKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := updatetrust.InitializeRoot(rootKey, public, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(dir, "repository")
	if err := updatetrust.Publish(updatetrust.PublishOptions{Directory: repository, Root: root, Keys: keys}); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(repository, "1.root.json")
	stale, err := os.ReadFile(filepath.Join(repository, "timestamp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-repository", repository, "-root", rootPath, "-keys", keysDir,
		"-expected-timestamp-sha256", fmt.Sprintf("%x", sha256.Sum256(stale))}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-repository", repository, "-root", rootPath, "-keys", keysDir,
		"-expected-timestamp-sha256", fmt.Sprintf("%x", sha256.Sum256(stale))}); err == nil {
		t.Fatal("stale recovery checkpoint accepted by CLI")
	}
	before, err := os.ReadFile(filepath.Join(repository, "timestamp.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, nextPublic := makeKeys(newKeysDir)
	nextRoot, err := updatetrust.RotateRoutineKeys(root, rootKey, nextPublic, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	nextPath := filepath.Join(dir, "next-root.json")
	if err := os.WriteFile(nextPath, nextRoot, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-repository", repository, "-root", nextPath, "-keys", newKeysDir, "-previous-root", rootPath}
	if err := run(base); err == nil {
		t.Fatal("CLI accepted rotation without independent checkpoint")
	}
	if err := run(append(base, "-expected-timestamp-sha256", fmt.Sprintf("%x", sha256.Sum256(before)))); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-repository", repository, "-root", nextPath, "-keys", newKeysDir}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishingCLIValidatesInventory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		inventory string
		wantError bool
	}{
		{name: "freshness", inventory: "[]"},
		{name: "unknown field", inventory: `[{"unexpected":true}]`, wantError: true},
		{name: "trailing value", inventory: "[] {}", wantError: true},
		{name: "null", inventory: "null", wantError: true},
		{name: "invalid shape", inventory: "{}", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			public := make(map[string]crypto.PublicKey)
			var rootKey ed25519.PrivateKey
			for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
				pub, key, err := ed25519.GenerateKey(nil)
				if err != nil {
					t.Fatal(err)
				}

				if role == "root" {
					rootKey = key
					continue
				}
				public[role] = pub
				der, err := x509.MarshalPKCS8PrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, role+".pem"), pem.EncodeToMemory(&pem.Block{
					Type: "PRIVATE KEY", Bytes: der,
				}), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := updatetrust.InitializeRoot(rootKey, public, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			rootPath := filepath.Join(dir, "root.json")
			inventoryPath := filepath.Join(dir, "inventory.json")
			if err := os.WriteFile(rootPath, root, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inventoryPath, []byte(test.inventory), 0o600); err != nil {
				t.Fatal(err)
			}
			err = run([]string{"-repository", filepath.Join(dir, "metadata"), "-root", rootPath,
				"-keys", dir, "-inventory", inventoryPath})
			if (err != nil) != test.wantError {
				t.Fatalf("run() error = %v; expected error %v", err, test.wantError)
			}
		})
	}
}

func TestFreshnessCLIRequiresPreviousPublication(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := run([]string{"-repository", filepath.Join(dir, "metadata"),
		"-root", filepath.Join(dir, "root.json"), "-keys", dir})
	if err == nil || !strings.Contains(err.Error(), "existing publication workspace") {
		t.Fatalf("freshness job must not silently initialize a lost workspace: %v", err)
	}
}
