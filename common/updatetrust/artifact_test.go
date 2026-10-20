package updatetrust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestAuthorizeRepositoryPackageFile(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			repo := newTestRepository(t)
			// Retain the discovery binary as a separate signed identity.
			discovery := repo.target
			discovery.Platform = "linux"
			discoveryPath, err := discovery.Path()
			if err != nil {
				t.Fatal(err)
			}
			discoveryInfo, err := metadata.TargetFile().FromBytes(discoveryPath, []byte("official test artifact"), "sha256")
			if err != nil {
				t.Fatal(err)
			}
			repo.targets.Signed.Targets[discoveryPath] = discoveryInfo
			target := discovery
			target.Format = format
			path, err := target.Path()
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("actual repository package")
			sum := sha256.Sum256(body)
			target.SHA256, target.SizeBytes = hex.EncodeToString(sum[:]), int64(len(body))
			info, err := metadata.TargetFile().FromBytes(path, body, "sha256")
			if err != nil {
				t.Fatal(err)
			}
			repo.targets.Signed.Targets[path] = info
			repo.publish(t)
			client, _ := repositoryClient(t, repo)
			for _, mode := range []string{"valid", "short", "long", "same-length substitution", "discovery hash only", "wrong format", "wrong arch", "wrong version", "directory", "closed"} {
				t.Run(mode, func(t *testing.T) {
					candidate := target
					payload := append([]byte(nil), body...)
					switch mode {
					case "short":
						payload = payload[:len(payload)-1]
					case "long":
						payload = append(payload, 'x')
					case "same-length substitution":
						payload[0] ^= 1
					case "discovery hash only":
						candidate = discovery
					case "wrong format":
						candidate.Format = "msi"
					case "wrong arch":
						candidate.Arch = "arm64"
					case "wrong version":
						candidate.Version = "0.33.0"
					}
					filePath := filepath.Join(t.TempDir(), "candidate."+format)
					if err := os.WriteFile(filePath, payload, 0o600); err != nil {
						t.Fatal(err)
					}
					if mode == "directory" {
						filePath = filepath.Dir(filePath)
					}
					file, err := os.Open(filePath)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					if mode == "closed" {
						if err := file.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "valid" {
						if _, err := file.Seek(3, io.SeekStart); err != nil {
							t.Fatal(err)
						}
					}
					authorization, err := client.AuthorizeFile(context.Background(), candidate, file)
					if mode != "valid" {
						if err == nil || authorization != (Authorization{}) {
							t.Fatalf("invalid package returned authorization: %+v %v", authorization, err)
						}
						return
					}
					if err != nil || authorization.Target != target {
						t.Fatalf("exact package rejected: %+v %v", authorization, err)
					}
					offset, err := file.Seek(0, io.SeekCurrent)
					if err != nil || offset != 3 {
						t.Fatalf("verification changed caller offset: %d %v", offset, err)
					}
				})
			}
			t.Run("nil", func(t *testing.T) {
				if _, err := client.AuthorizeFile(context.Background(), target, nil); err == nil {
					t.Fatal("nil package file accepted")
				}
			})
			t.Run("cancelled", func(t *testing.T) {
				file, err := os.CreateTemp(t.TempDir(), "package")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				authorization, err := client.AuthorizeFile(ctx, target, file)
				if err == nil || authorization != (Authorization{}) {
					t.Fatalf("cancelled verification returned authorization: %+v %v", authorization, err)
				}
			})
		})
	}
}

func TestArtifactReaderHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &artifactReader{ctx: ctx, reader: strings.NewReader("package")}
	cancel()
	if _, err := reader.Read(make([]byte, 10)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled artifact read: %v", err)
	}
}
