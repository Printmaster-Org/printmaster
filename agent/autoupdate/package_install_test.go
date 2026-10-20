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

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

func TestNativePackageConsumerEnforcesPolicyAndExactFile(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"deb", "rpm"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"valid", "valid exact pin", "valid major override", "valid minor override", "downgrade", "equal", "invalid pin", "invalid strategy", "major pin", "minor pin", "patch pin", "exact pin", "policy failure", "abort", "tampered", "cancelled", "query failure", "wrong name", "wrong version", "wrong arch", "wrong epoch", "wrong release", "broken installed", "install failure", "wrong post version", "post query failure", "missing policy"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, "printmaster-agent."+format)
					body := []byte("authenticated package")
					sum := sha256.Sum256(body)
					if err := os.WriteFile(path, body, 0o400); err != nil {
						t.Fatal(err)
					}
					target := updatetrust.Target{Component: "agent", Channel: "stable", Platform: "linux", Arch: "amd64",
						Version: "1.1.0", Format: format, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
					current := "1.0.0"
					spec := updatepolicy.PolicySpec{}
					switch mode {
					case "valid exact pin":
						spec.TargetVersion = target.Version
					case "valid major override":
						target.Version = "2.0.0"
						spec.VersionPinStrategy = updatepolicy.VersionPinMajor
						spec.AllowMajorUpgrade = true
					case "valid minor override":
						spec.VersionPinStrategy = updatepolicy.VersionPinMinor
						spec.AllowMajorUpgrade = true
					case "downgrade":
						current = "1.2.0"
					case "equal":
						current = target.Version
					case "invalid pin":
						spec.TargetVersion = "invalid"
					case "invalid strategy":
						spec.VersionPinStrategy = "invalid"
					case "major pin":
						target.Version = "2.0.0"
						spec.VersionPinStrategy = updatepolicy.VersionPinMajor
					case "minor pin":
						spec.VersionPinStrategy = updatepolicy.VersionPinMinor
					case "patch pin":
						spec.VersionPinStrategy = updatepolicy.VersionPinPatch
					case "exact pin":
						spec.TargetVersion = "1.0.1"
					case "abort":
						spec.RolloutControl.EmergencyAbort = true
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					policy := func() (updatepolicy.PolicySpec, error) {
						if mode == "policy failure" {
							return spec, errors.New("trusted policy unreadable")
						}
						if mode == "cancelled" {
							cancel()
						}
						if mode == "tampered" {
							if err := os.Chmod(path, 0o600); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(path, []byte("substituted package"), 0o600); err != nil {
								t.Fatal(err)
							}
						}
						return spec, nil
					}
					if mode == "missing policy" {
						policy = nil
					}
					calls, installs := 0, 0
					run := func(ctx context.Context, timeout time.Duration, gotDir string, env []string, name string, args ...string) ([]byte, error) {
						calls++
						if gotDir != dir || len(env) != 0 || !strings.HasPrefix(name, "/usr/bin/") {
							t.Fatalf("unsafe command context: %s %s %v", gotDir, name, env)
						}
						if calls == 3 {
							installs++
							wantTool, wantArgs := "/usr/bin/dpkg", []string{"--refuse-downgrade", "--install", "--", path}
							if format == "rpm" {
								wantTool, wantArgs = "/usr/bin/rpm", []string{"--upgrade", "--", path}
							}
							if name != wantTool || !reflect.DeepEqual(args, wantArgs) || timeout != 10*time.Minute {
								t.Fatalf("not an exact local-file install: %s %v", name, args)
							}
							if mode == "install failure" {
								return []byte("dependency missing"), errors.New("native installer failed")
							}
							return nil, nil
						}
						if calls > 4 || timeout != 30*time.Second {
							t.Fatalf("unexpected command: %s %v", name, args)
						}
						if mode == "query failure" || mode == "post query failure" && calls == 4 {
							return nil, errors.New("native query failed")
						}
						version := target.Version
						installed := calls == 2 || calls == 4
						wantTool := "/usr/bin/dpkg-deb"
						wantArgs := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", path}
						if installed {
							wantTool = "/usr/bin/dpkg-query"
							wantArgs = []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n${Status}\n", "printmaster-agent"}
						}
						if format == "rpm" {
							wantTool = "/usr/bin/rpm"
							flag, input := "-qp", path
							if installed {
								flag, input = "-q", "printmaster-agent"
							}
							wantArgs = []string{flag, "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", input}
						}
						if name != wantTool || !reflect.DeepEqual(args, wantArgs) {
							t.Fatalf("wrong native query: %s %v", name, args)
						}
						if calls == 2 || calls == 4 && mode == "wrong post version" {
							version = current
						}
						arch, err := nativePackageArch(target.Arch, format)
						if err != nil {
							t.Fatal(err)
						}
						fields := []string{"printmaster-agent", version, arch}
						if calls == 1 {
							switch mode {
							case "wrong name":
								fields[0] = "other-package"
							case "wrong version":
								fields[1] = "1.5.0"
							case "wrong arch":
								fields[2] = "other-arch"
							}
						}
						if format == "rpm" {
							epoch, release := "0", "1.fc42"
							if calls == 1 && mode == "wrong epoch" {
								epoch = "1"
							}
							if calls == 1 && mode == "wrong release" {
								release = "2"
							}
							if calls == 2 && mode == "broken installed" {
								fields[1] = "unorderable"
							}
							fields = append(fields, epoch, release)
						} else {
							if calls == 1 && mode == "wrong epoch" {
								fields[1] = "1:1.1.0"
							}
							if calls == 1 && mode == "wrong release" {
								fields[1] = "1.1.0-2"
							}
							if installed {
								status := "install ok installed"
								if calls == 2 && mode == "broken installed" {
									status = "install ok unpacked"
								}
								fields = append(fields, status)
							}
						}
						return []byte(strings.Join(fields, "\n") + "\n"), nil
					}
					err := consumeNativePackage(ctx, path, updatetrust.Authorization{Target: target}, policy, run)
					valid := strings.HasPrefix(mode, "valid")
					if (err == nil) != valid {
						t.Fatalf("unexpected native result: %v", err)
					}
					wantInstalls := 0
					if valid || mode == "install failure" || mode == "wrong post version" || mode == "post query failure" {
						wantInstalls = 1
					}
					if installs != wantInstalls {
						t.Fatalf("install count=%d want=%d error=%v", installs, wantInstalls, err)
					}
				})
			}
		})
	}
}
