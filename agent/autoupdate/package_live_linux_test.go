package autoupdate

import (
	"context"
	"encoding/json"
	"encoding/pem"
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

	"printmaster/common/updatetrust"
)

// This fixture deliberately changes the installed package database and systemd
// services. It must never be selected by ordinary tests, builds or releases.
func TestLiveLinuxPackageServiceLifecycle(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_LINUX_PACKAGES") != "disposable-github-runner" {
		t.Skip("requires explicit disposable GitHub-hosted native package qualification")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" ||
		os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("live Linux package qualification is restricted to an approved disposable root CI environment")
	}
	work := os.Getenv("PRINTMASTER_LIVE_LINUX_PACKAGE_DIR")
	format := os.Getenv("PRINTMASTER_LIVE_LINUX_PACKAGE_FORMAT")
	if !filepath.IsAbs(work) || (format != "deb" && format != "rpm") {
		t.Fatal("invalid live package qualification configuration")
	}
	command := func(tool string, args ...string) []byte {
		t.Helper()
		output, err := runPrivilegedPackageCommand(context.Background(), time.Minute, "/", nil, tool, args...)
		if err != nil {
			t.Fatalf("%s %v: %v: %s", tool, args, err, output)
		}
		return output
	}
	findPackage := func(directory, version string) string {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(work, directory, "printmaster-agent*"+version+"*."+format))
		if err != nil || len(matches) != 1 {
			t.Fatalf("expected one %s %s fixture in %s: %v %v", format, version, directory, matches, err)
		}
		return matches[0]
	}
	for _, scenario := range []string{"upgrade", "disconnected-agent", "startup-failure", "missing-dependency"} {
		if !t.Run(scenario, func(t *testing.T) {
			// Repair is an explicit owner operation in this disposable fixture,
			// never an automatic downgrade or production journal reset.
			baseline := findPackage("packages", "1.0.0")
			if format == "deb" {
				command("/usr/bin/dpkg", "--install", "--", baseline)
			} else {
				command("/usr/bin/rpm", "--upgrade", "--oldpackage", "--replacepkgs", "--", baseline)
			}
			command("/usr/bin/systemctl", "stop", "printmaster-agent-update.socket")
			command("/usr/bin/systemctl", "daemon-reload")
			command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
			if _, err := queryNativeService(context.Background(), "/", runPrivilegedPackageCommand); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("/etc/sudoers.d/printmaster-agent",
				[]byte("# owner-modified historical fixture\nprintmaster ALL=(root) NOPASSWD: /usr/bin/true\n"), 0o440); err != nil {
				t.Fatal(err)
			}
			fixtureDir := "packages"
			if scenario == "missing-dependency" {
				fixtureDir = "dependency"
			}
			path := findPackage(fixtureDir, "1.1.0")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			target, err := packageTargetFromFile(context.Background(), file, updatetrust.Target{
				Component: "agent", Channel: "stable", Version: "1.1.0", Platform: "linux", Arch: runtime.GOARCH, Format: format,
			})
			if err != nil {
				t.Fatal(err)
			}
			manifest := &UpdateManifest{Component: target.Component, Channel: target.Channel, Version: target.Version,
				Platform: target.Platform, Arch: target.Arch, Format: target.Format, SHA256: target.SHA256, SizeBytes: target.SizeBytes}
			_, metadataDir := runtimeTrustRepository(t, manifest, body, time.Now())
			server := httptest.NewTLSServer(http.FileServer(http.Dir(metadataDir)))
			defer server.Close()
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.ReadFile(filepath.Join(metadataDir, "1.root.json"))
			if err != nil {
				t.Fatal(err)
			}
			certificate := filepath.Join(metadataDir, "qualification-ca.pem")
			write(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
			config, err := json.Marshal(map[string]any{"schema": 1, "metadata_url": server.URL})
			if err != nil {
				t.Fatal(err)
			}
			write(packageHelperConfigPath, config)
			write(packageHelperRootPath, root)
			write(packageHelperPolicyPath, []byte(`{"schema":1,"enabled":true}`))
			write("/etc/systemd/system/printmaster-agent-update@.service.d/qualification.conf",
				[]byte("[Service]\nEnvironment=SSL_CERT_FILE="+certificate+"\n"))
			broken := "/etc/systemd/system/printmaster-agent.service.d/qualification.conf"
			if scenario == "startup-failure" {
				write(broken, []byte("[Service]\nExecStart=\nExecStart=/usr/libexec/printmaster-qualification-nonexistent\n"))
			}
			command("/usr/bin/systemctl", "daemon-reload")
			command("/usr/bin/systemctl", "start", "printmaster-agent-update.socket")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			type result struct {
				outcome RepositoryPackageOutcome
				err     error
			}
			reply := make(chan result, 1)
			go func() {
				outcome, err := SubmitRepositoryPackage(ctx, file, target)
				reply <- result{outcome, err}
			}()
			if scenario == "disconnected-agent" {
				deadline := time.Now().Add(30 * time.Second)
				observed := false
				for time.Now().Before(deadline) {
					// A read-only observer must not acquire the intake lock
					// while the installer owns it. The journal is atomic.
					transaction, err := readPackageTransaction(packageHelperStateDir)
					if err == nil && transaction.Phase == "installing" {
						observed = true
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				cancel()
				if !observed {
					t.Fatal("did not observe an active native transaction before disconnect")
				}
			}
			got := <-reply
			wantPhase := "startup-confirmed"
			if scenario == "startup-failure" || scenario == "missing-dependency" {
				wantPhase = "recovery-required"
			}
			if scenario == "upgrade" && (got.err != nil || got.outcome.Status != wantPhase) {
				t.Fatalf("installed helper failed: %+v %v", got.outcome, got.err)
			}
			if scenario != "upgrade" && got.err == nil {
				t.Fatalf("failed/disconnected request reported success: %+v", got.outcome)
			}
			var transaction RepositoryPackageTransaction
			deadline := time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) {
				transaction, err = readPackageTransaction(packageHelperStateDir)
				if errors.Is(err, os.ErrNotExist) {
					matches, globErr := filepath.Glob(filepath.Join(packageHelperStateDir, "transactions", "*.json"))
					if globErr != nil {
						t.Fatal(globErr)
					}
					if len(matches) == 1 {
						transaction, err = readPackageTransactionFile(matches[0])
					}
				}
				if err == nil && transaction.Phase == wantPhase {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil || transaction.Phase != wantPhase || transaction.Target != target {
				t.Fatalf("wrong root transaction evidence: %+v %v", transaction, err)
			}
			if wantPhase == "startup-confirmed" {
				grants, err := os.ReadFile("/etc/sudoers.d/printmaster-agent")
				if err != nil || strings.Contains(string(grants), "NOPASSWD:") {
					t.Fatalf("upgrade retained historical active sudo grants: %s %v", grants, err)
				}
				if _, err := queryNativeService(context.Background(), "/", runPrivilegedPackageCommand); err != nil {
					t.Fatal(err)
				}
				if err := verifyNativeServiceExecutable(transaction.Startup.PID, "/usr/bin/printmaster-agent"); err != nil {
					t.Fatal(err)
				}
			} else {
				replayCtx, replayCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer replayCancel()
				if outcome, err := SubmitRepositoryPackage(replayCtx, file, target); err == nil || outcome.Status == "startup-confirmed" {
					t.Fatalf("failed transaction allowed replay: %+v %v", outcome, err)
				}
				if err := os.Remove(broken); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				if format == "deb" {
					command("/usr/bin/dpkg", "--install", "--", baseline)
				} else {
					command("/usr/bin/rpm", "--upgrade", "--oldpackage", "--replacepkgs", "--", baseline)
				}
				command("/usr/bin/systemctl", "daemon-reload")
				command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
				recoveryPath := "/usr/libexec/printmaster-agent-update-recovery"
				if err := checkRootPackageExecutable(recoveryPath); err != nil {
					t.Fatal(err)
				}
				repairCtx, repairCancel := context.WithTimeout(context.Background(), time.Minute)
				defer repairCancel()
				repair := exec.CommandContext(repairCtx, recoveryPath, "confirm-repair", transaction.RunID)
				repair.Env = packageCommandEnvironment()
				if output, err := repair.CombinedOutput(); err != nil {
					t.Fatalf("installed owner recovery command failed: %v: %s", err, output)
				}
				repaired, err := readRepositoryPackageObservation(packageHelperResultPath)
				if err != nil || repaired.Phase != "owner-repaired" || repaired.RunID != transaction.RunID ||
					repaired.RepairedVersion != "1.0.0" {
					t.Fatalf("owner recovery lost exact failed run: %+v %v", repaired, err)
				}
				if _, err := os.Stat(filepath.Join(packageHelperStateDir, packageTransactionFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("owner-confirmed repair did not retire active blocker: %v", err)
				}
			}
			command("/usr/bin/systemctl", "stop", "printmaster-agent-update.socket")
			for attempt := 0; attempt < 100; attempt++ {
				output := command("/usr/bin/systemctl", "list-units", "--state=running", "--plain", "--no-legend", "printmaster-agent-update@*.service")
				if strings.TrimSpace(string(output)) == "" {
					break
				}
				if attempt == 99 {
					t.Fatal("helper still running; refusing to move transaction evidence")
				}
				time.Sleep(100 * time.Millisecond)
			}
			evidence := filepath.Join(work, "evidence", scenario)
			if err := os.CopyFS(evidence, os.DirFS(packageHelperStateDir)); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(packageHelperStateDir, packageHelperStateDir+"-qualification-"+scenario); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(broken); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			t.Logf("real %s/systemd %s: run=%s phase=%s; evidence=%s", format, scenario, transaction.RunID, transaction.Phase, evidence)
		}) {
			t.FailNow()
		}
	}
	fmt.Fprintln(os.Stderr, "Native Linux package/systemd qualification completed; production activation remains gated.")
}
