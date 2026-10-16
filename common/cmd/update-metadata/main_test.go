package main

import (
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

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
