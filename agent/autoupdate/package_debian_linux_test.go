package autoupdate

import (
	"bytes"
	"context"
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

// Test-only redirection: production arguments and tools remain unchanged.
// Fixtures have no maintainer scripts and never target the host package DB/log.
func isolatedDebianCommand(root string) repositoryCommand {
	return func(ctx context.Context, timeout time.Duration, dir string, env []string, tool string, args ...string) ([]byte, error) {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(root) != "package-root" || len(env) != 0 {
			return nil, errors.New("invalid isolated Debian fixture root")
		}
		switch tool {
		case "/usr/bin/dpkg":
			if len(args) != 4 || args[0] != "--refuse-downgrade" || args[1] != "--install" || args[2] != "--" {
				return nil, errors.New("unexpected isolated Debian installer arguments")
			}
			args = append([]string{"--root=" + root, "--log=" + filepath.Join(root, "var", "log", "dpkg.log")}, args...)
		case "/usr/bin/dpkg-query":
			args = append([]string{"--admindir=" + filepath.Join(root, "var", "lib", "dpkg")}, args...)
		case "/usr/bin/dpkg-deb":
		default:
			return nil, fmt.Errorf("unexpected isolated Debian tool: %s", tool)
		}
		return runPrivilegedPackageCommand(ctx, timeout, dir, nil, tool, args...)
	}
}

func TestPrivilegedRepositoryPackageDebianChild(t *testing.T) {
	if os.Getenv("PRINTMASTER_TEST_DEB_CHILD") != "1" {
		return
	}
	root, err := os.ReadFile(os.Getenv("PRINTMASTER_TEST_DEB_TRUST"))
	if err == nil {
		err = serveRepositoryPackageResponse(context.Background(), root, os.Getenv("PRINTMASTER_TEST_DEB_URL"),
			os.Getenv("PRINTMASTER_TEST_DEB_STATE"), os.Getenv("PRINTMASTER_TEST_DEB_POLICY"), os.Stdin, os.Stdout,
			isolatedDebianCommand(os.Getenv("PRINTMASTER_TEST_DEB_ROOT")))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func buildIsolatedDebianFixture(t *testing.T, dir, version string, missingDependency bool) string {
	t.Helper()
	tree := filepath.Join(dir, "fixture-"+version)
	for _, path := range []string{filepath.Join(tree, "DEBIAN"), filepath.Join(tree, "usr", "share", "printmaster-rehearsal")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	arch, err := nativePackageArch(runtime.GOARCH, "deb")
	if err != nil {
		t.Fatal(err)
	}
	control := "Package: printmaster-agent\nVersion: " + version + "\nArchitecture: " + arch +
		"\nMaintainer: PrintMaster Test <test@example.invalid>\nDescription: isolated updater fixture\n"
	if missingDependency {
		control += "Depends: printmaster-rehearsal-missing (= 99)\n"
	}
	if err := os.WriteFile(filepath.Join(tree, "DEBIAN", "control"), []byte(control), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "usr", "share", "printmaster-rehearsal", "version"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "fixture-"+version+".deb")
	data, err := runPrivilegedPackageCommand(context.Background(), 30*time.Second, dir, nil, "/usr/bin/dpkg-deb",
		"--build", "--root-owner-group", tree, output)
	if err != nil {
		t.Fatalf("build isolated Debian fixture: %v: %s", err, data)
	}
	return output
}

func TestPrivilegedRepositoryPackageIsolatedDebian(t *testing.T) {
	if os.Getenv("PRINTMASTER_TEST_ISOLATED_DEB") != "1" || os.Geteuid() != 0 {
		t.Skip("opt-in root rehearsal: PRINTMASTER_TEST_ISOLATED_DEB=1; temporary package root only")
	}
	for _, mode := range []string{"success", "missing dependency"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "package-root")
			admin := filepath.Join(root, "var", "lib", "dpkg")
			for _, path := range []string{admin, filepath.Join(root, "var", "log")} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(admin, "status"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			run := isolatedDebianCommand(root)
			baseline := buildIsolatedDebianFixture(t, dir, "1.0.0", false)
			if output, err := run(context.Background(), 30*time.Second, dir, nil, "/usr/bin/dpkg",
				"--refuse-downgrade", "--install", "--", baseline); err != nil {
				t.Fatalf("install baseline into isolated root: %v: %s", err, output)
			}
			candidate := buildIsolatedDebianFixture(t, dir, "1.1.0", mode == "missing dependency")
			body, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			_, target := packageRequestTestFrame(t, body)
			if current, err := queryNativePackageIdentity(context.Background(), target, dir, "", run); err != nil || current != "1.0.0" {
				t.Fatalf("wrong real baseline database: %q %v", current, err)
			}
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
			server := httptest.NewServer(http.FileServer(http.Dir(metadataDir)))
			defer server.Close()
			state, policy := filepath.Join(dir, "state"), filepath.Join(dir, "policy.json")
			if err := os.WriteFile(policy, []byte(`{"schema":1,"enabled":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(candidate)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPrivilegedRepositoryPackageDebianChild$")
				cmd.Env = append(packageCommandEnvironment(), "PRINTMASTER_TEST_DEB_CHILD=1",
					"PRINTMASTER_TEST_DEB_TRUST="+filepath.Join(metadataDir, "1.root.json"), "PRINTMASTER_TEST_DEB_URL="+server.URL,
					"PRINTMASTER_TEST_DEB_STATE="+state, "PRINTMASTER_TEST_DEB_POLICY="+policy, "PRINTMASTER_TEST_DEB_ROOT="+root)
				return cmd
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			got, installErr := launchRepositoryPackage(ctx, source, target, 25*time.Second, command)
			wantPhase := "awaiting-startup"
			if mode == "missing dependency" {
				wantPhase = "recovery-required"
				if installErr == nil || got.Status != "inspection-required" || !strings.Contains(installErr.Error(), "dependency") {
					t.Fatalf("native dependency failure lost: %+v %v", got, installErr)
				}
			} else if installErr != nil || got.Status != "awaiting-startup" {
				t.Fatalf("real native install failed: %+v %v", got, installErr)
			}
			journal, err := InspectPrivilegedRepositoryPackage(ctx, state)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Target != target || journal.RunID != got.RunID || journal.PreviousVersion != "1.0.0" || journal.Phase != wantPhase {
				t.Fatalf("real native transaction evidence differs: %+v outcome=%+v", journal, got)
			}
			payload, err := os.ReadFile(filepath.Join(root, "usr", "share", "printmaster-rehearsal", "version"))
			if err != nil || string(payload) != "1.1.0" {
				t.Fatalf("wrong installed payload: %q %v", payload, err)
			}
			if mode == "success" {
				if version, err := queryNativePackageIdentity(ctx, target, dir, "", run); err != nil || version != "1.1.0" {
					t.Fatalf("candidate not installed in real database: %q %v", version, err)
				}
			} else {
				output, err := run(ctx, 5*time.Second, dir, nil, "/usr/bin/dpkg-query",
					"--show", "--showformat=${Status}", "printmaster-agent")
				if err != nil || string(output) != "install ok unpacked" {
					t.Fatalf("dependency failure did not leave expected unpacked state: %q %v", output, err)
				}
				if _, err := queryNativePackageIdentity(ctx, target, dir, "", run); err == nil {
					t.Fatal("unconfigured native package was accepted as installed")
				}
			}
			before, err := os.ReadFile(filepath.Join(state, packageTransactionFile))
			if err != nil {
				t.Fatal(err)
			}
			retry, err := launchRepositoryPackage(ctx, source, target, 25*time.Second, command)
			if err == nil || retry.Status != "inspection-required" {
				t.Fatalf("native transaction was replayed without reconciliation: %+v %v", retry, err)
			}
			after, err := os.ReadFile(filepath.Join(state, packageTransactionFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("blocked replay changed durable evidence: %v", err)
			}
			entries, err := os.ReadDir(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".native-request-") || strings.HasPrefix(entry.Name(), "package-snapshot-") {
					t.Fatalf("native helper leaked package spool: %s", entry.Name())
				}
			}
			t.Logf("real isolated dpkg: %s; journal=%s; replay blocked", mode, journal.Phase)
		})
	}
}
