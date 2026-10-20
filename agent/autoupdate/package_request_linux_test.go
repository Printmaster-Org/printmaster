package autoupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func packageRequestTestFrame(t *testing.T, body []byte) ([]byte, updatetrust.Target) {
	t.Helper()
	sum := sha256.Sum256(body)
	target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "1.1.0",
		Platform: "linux", Arch: runtime.GOARCH, Format: "deb", SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
	header, err := json.Marshal(RepositoryPackageRequest{Schema: 1, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, uint32(len(header)))
	frame = append(frame, header...)
	return append(frame, body...), target
}

func TestPrivilegedRepositoryPackageRequestValidation(t *testing.T) {
	t.Parallel()
	body := []byte("package bytes")
	frame, _ := packageRequestTestFrame(t, body)
	valid := frame[4 : len(frame)-len(body)]
	for _, mode := range []string{"valid", "schema", "command", "path", "policy", "root", "duplicate", "nested duplicate", "null", "trailing", "wrong arch", "bad digest", "oversized package", "wrong channel"} {
		t.Run(mode, func(t *testing.T) {
			header := append([]byte(nil), valid...)
			switch mode {
			case "schema":
				header = bytes.Replace(header, []byte(`"schema":1`), []byte(`"schema":2`), 1)
			case "command", "path", "policy", "root":
				header = append([]byte(`{"`+mode+`":"untrusted",`), header[1:]...)
			case "duplicate":
				header = append([]byte(`{"schema":2,`), header[1:]...)
			case "nested duplicate":
				header = bytes.Replace(header, []byte(`"target":{`), []byte(`"target":{"channel":"dev",`), 1)
			case "null":
				header = bytes.Replace(header, []byte(`"size_bytes":13`), []byte(`"size_bytes":null`), 1)
			case "trailing":
				header = append(header, []byte("{}")...)
			case "wrong arch":
				header = bytes.Replace(header, []byte(`"arch":"`+runtime.GOARCH+`"`), []byte(`"arch":"unsupported"`), 1)
			case "bad digest":
				header = bytes.Replace(header, []byte(`"sha256":"`), []byte(`"sha256":"x`), 1)
			case "oversized package":
				header = bytes.Replace(header, []byte(`"size_bytes":13`), []byte(`"size_bytes":536870913`), 1)
			case "wrong channel":
				header = bytes.Replace(header, []byte(`"channel":"stable"`), []byte(`"channel":"dev"`), 1)
			}
			_, err := decodeRepositoryPackageRequest(header)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("request validation: %v", err)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageRequestPipe(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "suffix", "short prefix", "short header", "zero length", "oversized header", "short package", "cancelled", "stalled writer", "consumer failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			body := []byte("package bytes")
			frame, target := packageRequestTestFrame(t, body)
			switch mode {
			case "suffix":
				frame = append(frame, []byte("arbitrary ignored second frame")...)
			case "short prefix":
				frame = frame[:2]
			case "short header":
				frame = frame[:10]
			case "zero length":
				frame = make([]byte, 4)
			case "oversized header":
				frame = make([]byte, 4)
				binary.BigEndian.PutUint32(frame, 4097)
			case "short package":
				frame = frame[:len(frame)-1]
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			if mode != "stalled writer" {
				if _, err := writer.Write(frame); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if mode == "stalled writer" {
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
			}
			dir := t.TempDir()
			calls := 0
			consumerErr := errors.New("consumer rejected package")
			err = receiveRepositoryPackage(ctx, input, dir, func(source *os.File, got updatetrust.Target) error {
				calls++
				if got != target {
					t.Fatalf("wrong request target: %+v", got)
				}
				data := make([]byte, len(body))
				if _, err := source.ReadAt(data, 0); err != nil || !bytes.Equal(data, body) {
					t.Fatalf("wrong private package input: %q %v", data, err)
				}
				info, err := source.Stat()
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("unsafe spool permissions: %v %v", info, err)
				}
				if mode == "consumer failure" {
					return consumerErr
				}
				return nil
			})
			valid := mode == "valid" || mode == "suffix"
			if (err == nil) != valid {
				t.Fatalf("pipe handling result: %v", err)
			}
			if mode == "consumer failure" && !errors.Is(err, consumerErr) {
				t.Fatalf("consumer error lost: %v", err)
			}
			if mode == "stalled writer" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stalled input did not honor cancellation: %v", err)
			}
			wantCalls := 0
			if valid || mode == "consumer failure" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("consumer calls=%d want=%d", calls, wantCalls)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("request input leaked: %v %v", entries, readErr)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageStreamAuthorization(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("independent privileged stream verification requires root")
	}
	for _, substituted := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "substituted"}[substituted], func(t *testing.T) {
			body := []byte("package bytes")
			frame, target := packageRequestTestFrame(t, body)
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
			server := httptest.NewServer(http.FileServer(http.Dir(metadataDir)))
			defer server.Close()
			root, err := os.ReadFile(filepath.Join(metadataDir, "1.root.json"))
			if err != nil {
				t.Fatal(err)
			}
			if substituted {
				frame[len(frame)-1] ^= 1
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := writer.Write(frame); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state")
			calls := 0
			err = withRootPackageState(state, true, func() error {
				return receiveRepositoryPackage(context.Background(), input, state, func(source *os.File, got updatetrust.Target) error {
					return authenticateRootPackage(context.Background(), root, server.URL, state, source, got,
						func(path string, authorization updatetrust.Authorization) error {
							calls++
							data, err := os.ReadFile(path)
							if err != nil || !bytes.Equal(data, body) || authorization.Target != target {
								t.Fatalf("wrong authorized snapshot: %q %+v %v", data, authorization, err)
							}
							return nil
						})
				})
			})
			if (err == nil) == substituted || calls != map[bool]int{false: 1, true: 0}[substituted] {
				t.Fatalf("stream authorization result: calls=%d error=%v", calls, err)
			}
			entries, err := os.ReadDir(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".native-request-") || strings.HasPrefix(entry.Name(), "package-snapshot-") {
					t.Fatalf("temporary package data leaked: %s", entry.Name())
				}
			}
		})
	}
}

func TestPrivilegedRepositoryPackageRequestRejectsRegularInput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = receiveRepositoryPackage(context.Background(), file, t.TempDir(), func(*os.File, updatetrust.Target) error {
		t.Fatal("regular input reached consumer")
		return nil
	})
	if err == nil {
		t.Fatal("non-pipe request accepted")
	}
}

func TestPrivilegedRepositoryPackageRequestFailsClosed(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing policy", "disabled policy", "unprovisioned trust"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			policyPath := filepath.Join(dir, "policy.json")
			if mode != "missing policy" {
				enabled := "false"
				if mode == "unprovisioned trust" {
					enabled = "true"
				}
				if err := os.WriteFile(policyPath, []byte(`{"schema":1,"enabled":`+enabled+`}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			state := filepath.Join(dir, "state")
			frame, _ := packageRequestTestFrame(t, []byte("package bytes"))
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(frame); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			err = ServePrivilegedRepositoryPackage(context.Background(), nil, "https://invalid.example", state, policyPath, input)
			if err == nil {
				t.Fatal("unconfigured privileged request accepted")
			}
			if _, err := input.Stat(); err == nil {
				t.Fatal("owned request pipe was not closed on rejection")
			}
			if _, err := os.Lstat(filepath.Join(state, packageTransactionFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected request created a transaction: %v", err)
			}
			if os.Geteuid() == 0 {
				entries, err := os.ReadDir(state)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".native-request-") {
						t.Fatal("failed request leaked spool")
					}
				}
			}
		})
	}
}
