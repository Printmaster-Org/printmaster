package main

import (
	"io"
	"strings"
	"testing"
)

func TestDeliveryCLIRequiresExplicitOwnershipAndPins(t *testing.T) {
	args := []string{"-publication", "backup", "-root", "root.json", "-timestamp-sha256", strings.Repeat("a", 64),
		"-metadata-bucket", "public", "-recovery-bucket", "private", "-s3-endpoint", "https://s3.us-west-004.backblazeb2.com"}
	for _, extra := range [][]string{nil, {"-bootstrap"}, {"-externally-serialized"}, {"-externally-serialized", "-bootstrap", "-previous-timestamp-sha256", "old"}} {
		combined := append(append([]string(nil), args...), extra...)
		err := run(combined, func(string) string { t.Fatal("invalid CLI read credentials"); return "" }, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("got %v", err)
		}
	}
}
