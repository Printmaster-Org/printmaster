package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageProcessChild(t *testing.T) {
	mode := os.Getenv("PRINTMASTER_PACKAGE_TEST_CHILD")
	if mode == "" {
		return
	}
	if mode == "exit" {
		os.Exit(7)
	}
	if mode == "stall" {
		time.Sleep(30 * time.Second)
		os.Exit(8)
	}
	if mode == "malformed" {
		if _, err := os.Stdout.Write([]byte{0, 0, 0, 1, '{'}); err != nil {
			os.Exit(9)
		}
		os.Exit(10)
	}
	root, err := os.ReadFile(os.Getenv("PRINTMASTER_PACKAGE_TEST_ROOT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(11)
	}
	installed := false
	run := func(_ context.Context, timeout time.Duration, dir string, env []string, tool string, args ...string) ([]byte, error) {
		if len(env) != 0 || timeout <= 0 {
			return nil, errors.New("invalid command context")
		}
		arch, err := nativePackageArch(runtime.GOARCH, "deb")
		if err != nil {
			return nil, err
		}
		switch tool {
		case "/usr/bin/dpkg-deb":
			return []byte("printmaster-agent\n1.1.0\n" + arch + "\n"), nil
		case "/usr/bin/dpkg-query":
			version := "1.0.0"
			if installed {
				version = "1.1.0"
			}
			return []byte("printmaster-agent\n" + version + "\n" + arch + "\ninstall ok installed\n"), nil
		case "/usr/bin/dpkg":
			if len(args) != 4 || args[0] != "--refuse-downgrade" || args[1] != "--install" || args[2] != "--" ||
				filepath.Dir(args[3]) != dir {
				return nil, errors.New("unsafe installer arguments")
			}
			if mode == "install failure" {
				return nil, errors.New("simulated installer dependency failure")
			}
			installed = true
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected simulated native tool: %s", tool)
		}
	}
	err = serveRepositoryPackageResponse(context.Background(), root, os.Getenv("PRINTMASTER_PACKAGE_TEST_URL"),
		os.Getenv("PRINTMASTER_PACKAGE_TEST_STATE"), os.Getenv("PRINTMASTER_PACKAGE_TEST_POLICY"), os.Stdin, os.Stdout, run)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(12)
	}
	if mode == "exit after reply" {
		os.Exit(13)
	}
	os.Exit(0)
}

func TestPrivilegedRepositoryPackageSubprocessExchange(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("real root trust/policy/journal subprocess rehearsal requires root")
	}
	for _, mode := range []string{"valid", "install failure", "disabled", "wrong trust", "exit", "malformed", "stall", "exit after reply"} {
		t.Run(mode, func(t *testing.T) {
			// Large enough to block the sender if the child rejects before receipt.
			body := bytes.Repeat([]byte("package"), 64*1024)
			_, target := packageRequestTestFrame(t, body)
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
			server := httptest.NewServer(http.FileServer(http.Dir(metadataDir)))
			defer server.Close()
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			rootPath := filepath.Join(metadataDir, "1.root.json")
			if mode == "wrong trust" {
				rootPath = filepath.Join(dir, "invalid-root")
				if err := os.WriteFile(rootPath, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			state, policy := filepath.Join(dir, "state"), filepath.Join(dir, "policy.json")
			policyBytes := `{"schema":1,"enabled":true}`
			if mode == "disabled" {
				policyBytes = `{"schema":1,"enabled":false}`
			}
			if err := os.WriteFile(policy, []byte(policyBytes), 0o600); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(dir, "input.deb")
			if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "stall" {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
			}
			childInput, request, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			response, childOutput, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPrivilegedRepositoryPackageProcessChild$")
			cmd.Env = append(os.Environ(), "PRINTMASTER_PACKAGE_TEST_CHILD="+mode, "PRINTMASTER_PACKAGE_TEST_ROOT="+rootPath,
				"PRINTMASTER_PACKAGE_TEST_URL="+server.URL, "PRINTMASTER_PACKAGE_TEST_STATE="+state, "PRINTMASTER_PACKAGE_TEST_POLICY="+policy)
			cmd.Stdin, cmd.Stdout = childInput, childOutput
			diagnostics := &commandOutput{}
			cmd.Stderr = diagnostics
			cmd.WaitDelay = time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(childInput.Close(), childOutput.Close()); err != nil {
				t.Fatal(err)
			}
			got, exchangeErr := ExchangeRepositoryPackage(ctx, request, response, source, target)
			exitErr := cmd.Wait()
			if mode == "valid" {
				if exchangeErr != nil || exitErr != nil || got.Status != "awaiting-startup" || got.RunID == "" {
					t.Fatalf("child exchange failed: %+v exchange=%v exit=%v diagnostics=%s", got, exchangeErr, exitErr, diagnostics.bytes())
				}
			} else if mode == "exit after reply" {
				if exchangeErr != nil || exitErr == nil || got.Status != "awaiting-startup" {
					t.Fatalf("reply/exit distinction lost: %+v exchange=%v exit=%v", got, exchangeErr, exitErr)
				}
			} else if exchangeErr == nil || exitErr == nil {
				t.Fatalf("failed child claimed completion: %+v exchange=%v exit=%v", got, exchangeErr, exitErr)
			}
			if mode == "valid" || mode == "install failure" || mode == "exit after reply" {
				journal, err := InspectPrivilegedRepositoryPackage(context.Background(), state)
				if err != nil {
					t.Fatal(err)
				}
				want := "awaiting-startup"
				if mode == "install failure" {
					want = "recovery-required"
				}
				if journal.Target != target || journal.Phase != want {
					t.Fatalf("wrong durable child outcome: %+v", journal)
				}
				if (mode == "valid" || mode == "exit after reply") && journal.RunID != got.RunID {
					t.Fatal("transport run differs from durable run")
				}

			} else if _, err := os.Lstat(filepath.Join(state, packageTransactionFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected/interrupted receipt wrote install intent: %v", err)
			}
			if entries, err := os.ReadDir(state); err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".native-request-") || strings.HasPrefix(entry.Name(), "package-snapshot-") {
						t.Fatalf("child leaked temporary package data: %s", entry.Name())
					}
				}
			}
			if mode == "install failure" && got.Status != "" && got.Status != "inspection-required" {
				data, _ := json.Marshal(got)
				t.Fatalf("installer failure reply is unsafe: %s", data)
			}
		})
	}
}

func TestPrivilegedRepositoryPackageExchangeSourceFailure(t *testing.T) {
	t.Parallel()
	_, target := packageRequestTestFrame(t, []byte("package"))
	helperInput, request, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer helperInput.Close()
	response, helperOutput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer helperOutput.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := ExchangeRepositoryPackage(ctx, request, response, nil, target)
	if err == nil || got.Status != "" {
		t.Fatalf("missing source was not rejected: %+v %v", got, err)
	}
	if ctx.Err() != nil {
		t.Fatal("source rejection waited for caller timeout instead of cancelling response read")
	}
	for _, pipe := range []*os.File{request, response} {
		if _, err := pipe.Stat(); err == nil {
			t.Fatal("failed exchange leaked owned pipe")
		}
	}
}
