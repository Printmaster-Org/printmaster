package main

import (
	"bytes"
	"testing"
)

func TestInvalidArgumentsFailBeforeNetwork(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"-repository", "owner/state", "extra"}, {"-repository", "https://example.invalid/state"}} {
		if err := run(args, func(string) string { return "" }, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid authority selection accepted")
		}
	}
}
