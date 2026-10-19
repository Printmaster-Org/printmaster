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
		{"-mode", "publish", "-state-repository", "owner/production", "-public-url", "https://example.com", "-mirror", "mirror", "-work", "work", "-externally-serialized", "-rehearsal-interrupt"},
	} {
		err := run(args, func(string) string { t.Fatal("invalid operation read credentials"); return "" }, io.Discard)
		if err == nil {
			t.Fatal("invalid publisher operation accepted")
		}
	}
}
