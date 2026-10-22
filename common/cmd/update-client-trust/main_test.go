package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientTrustCLIRejectsUnsafeModes(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil, {"reset"}, {"inspect"}, {"inspect", "--cache", t.TempDir(), "extra"},
		{"inspect", "--cache", t.TempDir(), "--externally-serialized"},
		{"repair", "--cache", t.TempDir()}, {"repair", "--cache", t.TempDir(), "--backup", t.TempDir()},
	} {
		if err := run(args, new(bytes.Buffer)); err == nil {
			t.Fatalf("unsafe invocation accepted: %v", args)
		}
	}
}

func TestClientTrustCLIInspectionDoesNotBootstrap(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "metadata")
	var output bytes.Buffer
	if err := run([]string{"inspect", "--cache", directory}, &output); err == nil {
		t.Fatal("missing checkpoint accepted")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("read-only inspection initialized state: %v", err)
	}
	if !strings.Contains(output.String(), `"integrity_ok":false`) {
		t.Fatalf("missing failed inspection report: %s", &output)
	}
}

func TestClientTrustCLIRestoresOnlyPinnedBytes(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cache, backup := filepath.Join(parent, "metadata"), filepath.Join(parent, "backup")
	for _, dir := range []string{cache, backup} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(`{"signed":{"version":1,"expires":"2099-01-01T00:00:00Z"}}`)
	hash := sha256.Sum256(body)
	checkpoint, err := json.Marshal(map[string]any{"version": 1, "sha256": map[string]string{"root.json": hex.EncodeToString(hash[:])}})
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		cache + ".state.json": checkpoint, filepath.Join(cache, "root.json"): []byte("damaged"),
		filepath.Join(backup, "root.json"): body,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := run([]string{"repair", "--cache", cache, "--backup", backup, "--externally-serialized"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "checkpoint unchanged") || !strings.Contains(output.String(), "Normal TUF refresh is still required") {
		t.Fatalf("repair claimed authorization success: %s", &output)
	}
	output.Reset()
	if err := run([]string{"inspect", "--cache", cache}, &output); err != nil || !strings.Contains(output.String(), `"integrity_ok":true`) {
		t.Fatalf("inspection failed after exact restore: %s %v", &output, err)
	}
}
