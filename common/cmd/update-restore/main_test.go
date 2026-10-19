package main

import (
	"io"
	"strings"
	"testing"
)

func TestRestoreCLIRequiresIndependentPinsAndOwnership(t *testing.T) {
	args := []string{"-repository", "workspace", "-records", "records", "-root", "root.json",
		"-recovery-bucket", "private", "-s3-endpoint", "https://s3.us-west-004.backblazeb2.com"}
	for _, extra := range [][]string{nil, {"-externally-serialized"}, {"-externally-serialized", "-timestamp-sha256", strings.Repeat("a", 64)},
		{"-externally-serialized", "-state-repository", "owner/state", "-timestamp-sha256", strings.Repeat("a", 64), "-record-sha256", strings.Repeat("b", 64)}} {
		err := run(append(append([]string(nil), args...), extra...), func(string) string {
			t.Fatal("invalid command read credentials")
			return ""
		}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("got %v", err)
		}
	}
}
