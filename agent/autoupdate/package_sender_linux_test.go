package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageSender(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "wrong digest", "wrong length", "cancelled", "blocked", "closed reader"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			body := bytes.Repeat([]byte("package"), 64*1024)
			frame, target := packageRequestTestFrame(t, body)
			sourcePath := filepath.Join(t.TempDir(), "package")
			if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err := source.Seek(7, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			input, output, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			switch mode {
			case "wrong digest":
				target.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "wrong length":
				target.SizeBytes--
			case "cancelled":
				cancel()
			case "blocked":
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
				defer cancel()
			case "closed reader":
				if err := input.Close(); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- WriteRepositoryPackageRequest(ctx, output, source, target) }()
			var received []byte
			if mode != "blocked" && mode != "closed reader" {
				received, err = io.ReadAll(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			err = <-done
			if (err == nil) != (mode == "valid") {
				t.Fatalf("sender result: %v", err)
			}
			if mode == "valid" && !bytes.Equal(received, frame) {
				t.Fatal("sender did not produce exact canonical frame")
			}
			if (mode == "wrong digest" || mode == "wrong length" || mode == "cancelled") && len(received) != 0 {
				t.Fatal("invalid source sent bytes")
			}
			if mode == "blocked" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked writer did not honor deadline: %v", err)
			}
			offset, err := source.Seek(0, io.SeekCurrent)
			if err != nil || offset != 7 {
				t.Fatalf("sender changed source offset: %d %v", offset, err)
			}
			if _, err := output.Stat(); err == nil {
				t.Fatal("sender did not close owned pipe")
			}
		})
	}
}
