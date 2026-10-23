package main

import (
	"io"
	"testing"
)

func TestPublisherRejectsInvalidOperationBeforeCredentials(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"-mode", "invalid"},
		{"-mode", "check", "-bootstrap"},
		{"-mode", "preflight", "-bootstrap", "-state-repository", "owner/repo", "-public-url", "https://example.com", "-externally-serialized"},
		{"-mode", "preflight", "-inventory", "inventory.json", "-state-repository", "owner/repo", "-public-url", "https://example.com", "-externally-serialized"},
		{"-mode", "publish", "-state-repository", "owner/production", "-public-url", "https://example.com", "-mirror", "mirror", "-work", "work", "-externally-serialized", "-rehearsal-interrupt"},
	} {
		err := run(args, func(string) string { t.Fatal("invalid operation read credentials"); return "" }, io.Discard)
		if err == nil {
			t.Fatal("invalid publisher operation accepted")
		}
	}
}

func TestPreflightRejectsInvalidRootBeforeRemoteCredentials(t *testing.T) {
	t.Parallel()
	err := run([]string{"-mode", "preflight", "-state-repository", "owner/repo",
		"-public-url", "https://example.com", "-externally-serialized"}, func(name string) string {
		if name != "UPDATE_ROOT_JSON" {
			t.Fatalf("invalid root accessed credential %s", name)
		}
		return "null"
	}, io.Discard)
	if err == nil {
		t.Fatal("preflight accepted an unprovisioned root")
	}
}
