package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func nativeServiceTestOutput() string {
	return "Id=printmaster-agent.service\nLoadState=loaded\nActiveState=active\nSubState=running\nMainPID=1234\nInvocationID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nNeedDaemonReload=no\n"
}

func TestPrivilegedRepositoryPackageStartupReconciliation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned archive qualification requires root")
	}
	t.Parallel()
	for _, format := range []string{"deb", "rpm"} {
		for _, mode := range []string{"valid", "wrong run", "installing", "recovery-required", "previous version", "query error", "service stopped", "reload required", "wrong service", "bad invocation", "pid changed", "invocation changed", "wrong executable", "executable changed", "package changed", "cancelled", "cancel at confirmation", "archive conflict", "archive failure", "resume archive", "resume identical archive"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				journal := testPackageTransaction(t, dir)
				journal.Target.Format = format
				journal.Phase = "awaiting-startup"
				if mode == "installing" || mode == "recovery-required" {
					journal.Phase = mode
				}
				history := filepath.Join(dir, "transactions")
				archive := filepath.Join(history, journal.RunID+".json")
				if strings.HasPrefix(mode, "resume") {
					journal.Phase = "startup-confirmed"
					journal.Startup = &RepositoryPackageStartup{PID: 1234, InvocationID: strings.Repeat("a", 32)}
				}
				data, err := json.Marshal(journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal.path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if mode == "archive conflict" || mode == "resume identical archive" {
					if err := os.Mkdir(history, 0o700); err != nil {
						t.Fatal(err)
					}
					existing := data
					if mode == "archive conflict" {
						existing = []byte("{")
					}
					if err := os.WriteFile(archive, existing, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "archive failure" {
					if err := os.WriteFile(history, []byte("not a directory"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				queries, services, processes := 0, 0, 0
				run := func(_ context.Context, timeout time.Duration, gotDir string, env []string, tool string, args ...string) ([]byte, error) {
					if timeout != 30*time.Second || gotDir != dir || len(env) != 0 {
						t.Fatal("unsafe reconciliation command context")
					}
					if tool == "/usr/bin/systemctl" {
						services++
						want := []string{"--system", "--no-pager", "show",
							"--property=Id,LoadState,ActiveState,SubState,MainPID,InvocationID,NeedDaemonReload", "--", "printmaster-agent.service"}
						if !reflect.DeepEqual(args, want) {
							t.Fatalf("unexpected service command: %v", args)
						}
						output := nativeServiceTestOutput()
						switch mode {
						case "service stopped":
							output = strings.Replace(output, "ActiveState=active", "ActiveState=inactive", 1)
						case "reload required":
							output = strings.Replace(output, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1)
						case "wrong service":
							output = strings.Replace(output, "Id=printmaster-agent.service", "Id=other.service", 1)
						case "bad invocation":
							output = strings.Replace(output, strings.Repeat("a", 32), "invalid", 1)
						case "pid changed":
							if services == 2 {
								output = strings.Replace(output, "MainPID=1234", "MainPID=5678", 1)
							}
						case "invocation changed":
							if services == 2 {
								output = strings.Replace(output, strings.Repeat("a", 32), strings.Repeat("b", 32), 1)
							}
						}
						return []byte(output), nil
					}
					queries++
					wantTool := "/usr/bin/dpkg-query"
					wantArgs := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n${Status}\n", "printmaster-agent"}
					if format == "rpm" {
						wantTool = "/usr/bin/rpm"
						wantArgs = []string{"-q", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", "printmaster-agent"}
					}
					if tool != wantTool || !reflect.DeepEqual(args, wantArgs) {
						t.Fatalf("reconciliation attempted a modifying command: %s %v", tool, args)
					}
					if mode == "query error" {
						return nil, errors.New("database unavailable")
					}
					version := "1.1.0"
					if mode == "previous version" || mode == "package changed" && queries == 2 {
						version = "1.0.0"
					}
					arch, err := nativePackageArch(journal.Target.Arch, format)
					if err != nil {
						t.Fatal(err)
					}
					fields := []string{"printmaster-agent", version, arch, "install ok installed"}
					if format == "rpm" {
						fields = []string{"printmaster-agent", version, arch, "0", "1"}
					}
					return []byte(strings.Join(fields, "\n") + "\n"), nil
				}
				verify := func(pid int) error {
					processes++
					if pid != 1234 {
						t.Fatalf("wrong process inspected: %d", pid)
					}
					if mode == "wrong executable" || mode == "executable changed" && processes == 2 {
						return errors.New("wrong executable inode")
					}
					if mode == "cancel at confirmation" && processes == 2 {
						cancel()
					}
					return nil
				}
				runID := journal.RunID
				if mode == "wrong run" {
					runID = strings.Repeat("f", 32)
				}
				got, err := reconcilePackageStartup(ctx, dir, runID, run, verify)
				valid := mode == "valid" || strings.HasPrefix(mode, "resume")
				if (err == nil) != valid {
					t.Fatalf("reconciliation result: %+v %v", got, err)
				}
				if valid {
					if got.RunID != journal.RunID || got.Phase != "startup-confirmed" || got.Startup == nil || got.Startup.PID != 1234 {
						t.Fatalf("wrong startup confirmation: %+v", got)
					}
					if _, err := os.Lstat(journal.path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("active transaction was not retired: %v", err)
					}
					saved, err := readPackageTransactionFile(archive)
					if err != nil || saved.RunID != got.RunID || saved.Startup == nil || *saved.Startup != *got.Startup {
						t.Fatalf("missing durable archive: %+v %v", saved, err)
					}
					if _, err := newPackageTransaction(dir, journal.Target); err != nil {
						t.Fatalf("confirmed and archived run still blocks new intake: %v", err)
					}
					if mode == "valid" && (queries != 2 || services != 2 || processes != 2) {
						t.Fatalf("missing rechecks: queries=%d services=%d processes=%d", queries, services, processes)
					}
					if strings.HasPrefix(mode, "resume") && queries+services+processes != 0 {
						t.Fatal("archive resume reran native/service inspection")
					}
				} else {
					saved, readErr := os.ReadFile(journal.path)
					if readErr != nil {
						t.Fatalf("failed reconciliation removed active evidence: %v", readErr)
					}
					if mode != "archive conflict" && mode != "archive failure" && string(saved) != string(data) {
						t.Fatal("failed validation modified evidence")
					}
					if _, err := newPackageTransaction(dir, journal.Target); err == nil {
						t.Fatal("failed reconciliation unblocked a new installation")
					}
					if mode == "archive conflict" {
						savedArchive, readErr := os.ReadFile(archive)
						if readErr != nil || string(savedArchive) != "{" {
							t.Fatal("conflicting archive overwritten")
						}
					}
				}
			})
		}
	}
}

func TestPrivilegedRepositoryPackageServiceEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "duplicate", "missing", "extra", "malformed", "zero invocation", "invalid pid", "no main pid", "command failure"} {
		t.Run(mode, func(t *testing.T) {
			output := nativeServiceTestOutput()
			switch mode {
			case "duplicate":
				output += "MainPID=1\n"
			case "missing":
				output = strings.Replace(output, "SubState=running\n", "", 1)
			case "extra":
				output += "Unexpected=yes\n"
			case "malformed":
				output += "malformed\n"
			case "zero invocation":
				output = strings.Replace(output, strings.Repeat("a", 32), strings.Repeat("0", 32), 1)
			case "invalid pid":
				output = strings.Replace(output, "MainPID=1234", "MainPID=invalid", 1)
			case "no main pid":
				output = strings.Replace(output, "MainPID=1234", "MainPID=0", 1)
			}
			run := func(context.Context, time.Duration, string, []string, string, ...string) ([]byte, error) {
				if mode == "command failure" {
					return nil, errors.New("systemd unavailable")
				}
				return []byte(output), nil
			}
			got, err := queryNativeService(context.Background(), t.TempDir(), run)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("service evidence result: %+v %v", got, err)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageProcessExecutable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("protected executable identity qualification requires root")
	}
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("/usr/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	installed, other := filepath.Join(dir, "sleep"), filepath.Join(dir, "other")
	for _, path := range []string{installed, other} {
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(installed, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cmd.Process.Kill(); err != nil {
			t.Error(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Error("test child unexpectedly exited successfully")
		}
	}()
	if err := verifyNativeServiceExecutable(cmd.Process.Pid, installed); err != nil {
		t.Fatalf("matching kernel executable rejected: %v", err)
	}
	if err := verifyNativeServiceExecutable(cmd.Process.Pid, other); err == nil {
		t.Fatal("wrong executable accepted")
	}
	if err := os.Rename(other, installed); err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeServiceExecutable(cmd.Process.Pid, installed); err == nil {
		t.Fatal("old running executable accepted after installed file replacement")
	}
	if err := verifyNativeServiceExecutable(0, installed); err == nil {
		t.Fatal("invalid PID accepted")
	}
}
