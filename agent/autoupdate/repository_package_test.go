package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

func TestRepositoryAcquisitionAuthenticatesExactPackage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ manager, goarch, debarch, rpmarch string }{
		{"apt", "amd64", "amd64", "x86_64"},
		{"dnf", "amd64", "amd64", "x86_64"},
		{"yum", "amd64", "amd64", "x86_64"},
		{"apt", "arm64", "arm64", "aarch64"},
		{"dnf", "arm64", "arm64", "aarch64"},
		{"apt", "arm", "armhf", "armv7hl"},
		{"dnf", "arm", "armhf", "armv7hl"},
	} {
		t.Run(tc.manager+"/"+tc.goarch, func(t *testing.T) {
			t.Parallel()
			body := []byte("publisher package fixture")
			sum := sha256.Sum256(body)
			format, arch, tool := "deb", tc.debarch, "apt-get"
			args := []string{"download", "printmaster-agent:" + arch + "=1.1.0"}
			if tc.manager != "apt" {
				format, arch, tool = "rpm", tc.rpmarch, "dnf"
				args = []string{"download", "--destdir=.", "printmaster-agent-1.1.0." + arch}
				if tc.manager == "yum" {
					tool, args = "yumdownloader", args[1:]
				}
			}
			manifest := &UpdateManifest{Component: "agent", Channel: "stable", Version: "1.1.0",
				Platform: "linux", Arch: tc.goarch, Format: format,
				SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
			verifier, _ := runtimeTrustRepository(t, manifest, body, time.Now())
			for _, mode := range []string{"valid", "substituted", "multiple", "missing", "directory", "wrong name", "wrong version", "wrong arch", "epoch", "release", "extra fields", "download failure", "query failure", "cancelled"} {
				t.Run(mode, func(t *testing.T) {
					m := lifecycleManager(t, &mockUpdateClient{}, nil)
					m.platform, m.arch, m.packageManager, m.packageName = "linux", tc.goarch, tc.manager, "printmaster-agent"
					m.publisherVerifier = verifier
					discovery := *manifest
					discovery.Format = "binary"
					// A binary discovery digest must never be used for package authorization.
					discovery.SHA256 = strings.Repeat("0", 64)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if mode == "cancelled" {
						cancel()
					}
					calls := 0
					var workspace string
					run := func(ctx context.Context, timeout time.Duration, dir string, env []string, name string, got ...string) ([]byte, error) {
						calls++
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						workspace = dir
						if calls == 1 {
							if name != tool || !reflect.DeepEqual(got, args) || len(env) != 0 || timeout != 10*time.Minute {
								t.Fatalf("unexpected acquisition: %s %v env=%v timeout=%s", name, got, env, timeout)
							}
							if mode == "download failure" {
								return []byte("download plugin missing"), errors.New("download failed")
							}
							if mode == "missing" {
								return nil, nil
							}
							path := filepath.Join(dir, "candidate."+format)
							if mode == "directory" {
								return nil, os.Mkdir(path, 0o700)
							}
							payload := body
							if mode == "substituted" {
								payload = []byte("attacker package fixture")
							}
							if err := os.WriteFile(path, payload, 0o600); err != nil {
								t.Fatal(err)
							}
							if mode == "multiple" {
								if err := os.WriteFile(filepath.Join(dir, "second."+format), body, 0o600); err != nil {
									t.Fatal(err)
								}
							}
							return nil, nil
						}
						if calls != 2 || (name != "dpkg-deb" && name != "rpm") {
							t.Fatalf("unexpected command (installation forbidden): %s %v", name, got)
						}
						wantQuery := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", filepath.Join(dir, "candidate."+format)}
						if format == "rpm" {
							wantQuery = []string{"-qp", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", filepath.Join(dir, "candidate."+format)}
						}
						if !reflect.DeepEqual(got, wantQuery) || timeout != 30*time.Second {
							t.Fatalf("unexpected native query: %s %v timeout=%s", name, got, timeout)
						}
						if mode == "query failure" {
							return nil, errors.New("native inspection failed")
						}
						fields := []string{"printmaster-agent", "1.1.0", arch}
						switch mode {
						case "wrong name":
							fields[0] = "another-package"
						case "wrong version":
							fields[1] = "1.2.0"
						case "wrong arch":
							fields[2] = "other"
						}
						if format == "rpm" {
							epoch := "0"
							release := "1.fc42"
							if mode == "epoch" {
								epoch = "1"
							}
							if mode == "release" {
								release = "2.fc42"
							}
							fields = append(fields, epoch, release)
						} else if mode == "epoch" {
							fields[1] = "1:1.1.0"
						} else if mode == "release" {
							fields[1] = "1.1.0-2"
						}
						if mode == "extra fields" {
							fields = append(fields, "unexpected")
						}
						return []byte(strings.Join(fields, "\n") + "\n"), nil
					}
					authorization, err := m.acquireRepositoryPackage(ctx, &discovery, run)
					if mode == "valid" {
						if err != nil || authorization.Target.Format != format || authorization.Target.SHA256 != manifest.SHA256 {
							t.Fatalf("package authorization: %+v %v", authorization, err)
						}
					} else if err == nil || authorization != (updatetrust.Authorization{}) {
						t.Fatalf("invalid acquisition authorized: %+v %v", authorization, err)
					}
					if workspace != "" {
						if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("workspace not cleaned: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestRepositoryAcquisitionRejectsUnsupportedIdentityBeforeCommand(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"version", "package", "arch", "platform", "manager", "channel"} {
		t.Run(mode, func(t *testing.T) {
			m := lifecycleManager(t, &mockUpdateClient{}, nil)
			m.platform, m.arch, m.packageManager, m.packageName = "linux", "amd64", "apt", "printmaster-agent"
			m.publisherVerifier = &repositoryRejectVerifier{}
			manifest := &UpdateManifest{Version: "1.1.0", Channel: "stable"}
			switch mode {
			case "version":
				manifest.Version = "--option"
			case "package":
				m.packageName = "other"
			case "arch":
				m.arch = "unknown"
			case "platform":
				m.platform = "windows"
			case "manager":
				m.packageManager = "unknown"
			case "channel":
				manifest.Channel = "beta"
			}
			run := func(context.Context, time.Duration, string, []string, string, ...string) ([]byte, error) {
				t.Fatal("unsupported identity reached repository command")
				return nil, nil
			}
			if _, err := m.acquireRepositoryPackage(context.Background(), manifest, run); !errors.Is(err, ErrPublisherTrust) {
				t.Fatalf("unsupported identity: %v", err)
			}
		})
	}
}

type repositoryRejectVerifier struct{}

func (*repositoryRejectVerifier) Authorize(context.Context, updatetrust.Target) (updatetrust.Authorization, error) {
	return updatetrust.Authorization{}, errors.New("not authorized")
}

func (*repositoryRejectVerifier) AuthorizeFile(context.Context, updatetrust.Target, *os.File) (updatetrust.Authorization, error) {
	return updatetrust.Authorization{}, errors.New("not authorized")
}
