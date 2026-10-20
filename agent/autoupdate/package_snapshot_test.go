package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestPackageSnapshotIsIndependentAndAuthenticated(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			body := []byte("publisher authenticated package")
			sum := sha256.Sum256(body)
			manifest := &UpdateManifest{Component: "agent", Channel: "stable", Version: "1.1.0",
				Platform: "linux", Arch: "amd64", Format: format,
				SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
			verifier, _ := runtimeTrustRepository(t, manifest, body, time.Now())
			target := updatetrust.Target{Component: "agent", Channel: "stable", Version: "1.1.0",
				Platform: "linux", Arch: "amd64", Format: format, SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes}
			for _, mode := range []string{"valid", "substitution", "wrong format", "wrong hash", "cancelled", "consumer failure", "source modified", "wrong length", "oversize", "directory", "wrong authorization", "cancel after authorization"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, "input")
					if err := os.WriteFile(path, body, 0o600); err != nil {
						t.Fatal(err)
					}
					sourcePath := path
					if mode == "directory" {
						sourcePath = dir
					}
					file, err := os.OpenFile(sourcePath, os.O_RDONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					candidate := target
					switch mode {
					case "substitution":
						if err := os.WriteFile(path, make([]byte, len(body)), 0o600); err != nil {
							t.Fatal(err)
						}
					case "wrong format":
						if format == "deb" {
							candidate.Format = "rpm"
						} else {
							candidate.Format = "deb"
						}
					case "wrong hash":
						candidate.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
					case "wrong length":
						candidate.SizeBytes++
					case "oversize":
						candidate.SizeBytes = maxRepositoryPackageSize + 1
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if mode == "cancelled" {
						cancel()
					}
					parent := t.TempDir()
					called := false
					consumerErr := errors.New("simulated consumer failure")
					var fileVerifier packageFileVerifier = verifier
					if mode == "wrong authorization" || mode == "cancel after authorization" {
						fileVerifier = packageVerifierFunc(func(ctx context.Context, target updatetrust.Target, file *os.File) (updatetrust.Authorization, error) {
							authorization, err := verifier.AuthorizeFile(ctx, target, file)
							if mode == "wrong authorization" {
								authorization.Target.Arch = "other"
							} else {
								cancel()
							}
							return authorization, err
						})
					}
					err = withAuthenticatedPackage(ctx, parent, file, candidate, fileVerifier, func(snapshot string, authorization updatetrust.Authorization) error {
						called = true
						if authorization.Target != target || filepath.Ext(snapshot) != "."+format || snapshot == path {
							t.Fatalf("consumer received invalid snapshot/authorization: %s %+v", snapshot, authorization)
						}
						if mode == "source modified" {
							if err := os.WriteFile(path, []byte("untrusted replacement"), 0o600); err != nil {
								t.Fatal(err)
							}
						}
						got, err := os.ReadFile(snapshot)
						if err != nil || string(got) != string(body) {
							t.Fatalf("snapshot changed with input: %q %v", got, err)
						}
						if mode == "consumer failure" {
							return consumerErr
						}
						return nil
					})
					wantCall := mode == "valid" || mode == "source modified" || mode == "consumer failure"
					if called != wantCall {
						t.Fatalf("consumer called=%v want=%v, error=%v", called, wantCall, err)
					}
					if mode == "consumer failure" {
						if !errors.Is(err, consumerErr) {
							t.Fatalf("consumer error lost: %v", err)
						}
					} else if (err == nil) != (mode == "valid" || mode == "source modified") {
						t.Fatalf("unexpected snapshot result: %v", err)
					}
					entries, err := os.ReadDir(parent)
					if err != nil || len(entries) != 0 {
						t.Fatalf("private snapshot leaked: %v %v", entries, err)
					}
				})
			}
		})
	}
}

type packageVerifierFunc func(context.Context, updatetrust.Target, *os.File) (updatetrust.Authorization, error)

func (f packageVerifierFunc) AuthorizeFile(ctx context.Context, target updatetrust.Target, file *os.File) (updatetrust.Authorization, error) {
	return f(ctx, target, file)
}

func TestPackageTargetHashPreservesOffsetAndRejectsCancellation(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "package")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("package"); err != nil {
		t.Fatal(err)
	}
	target, err := packageTargetFromFile(context.Background(), file, updatetrust.Target{Format: "deb"})
	if err != nil || target.SizeBytes != 7 {
		t.Fatalf("target hash: %+v %v", target, err)
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil || offset != 7 {
		t.Fatalf("source offset changed: %d %v", offset, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := packageTargetFromFile(ctx, file, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := file.Truncate(maxRepositoryPackageSize + 1); err != nil {
		t.Fatal(err)
	}
	if _, err := packageTargetFromFile(context.Background(), file, target); !errors.Is(err, ErrPublisherTrust) {
		t.Fatalf("oversized package accepted: %v", err)
	}
}
