package autoupdate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"printmaster/common/updatetrust"
)

type livePackageManagerResult struct {
	Outcome RepositoryPackageOutcome `json:"outcome"`
	Error   string                   `json:"error,omitempty"`
}

func TestLiveLinuxPackageManagerSender(t *testing.T) {
	if os.Getenv("PRINTMASTER_TEST_LINUX_MANAGER_CHILD") == "" {
		return
	}
	if os.Geteuid() == 0 || os.Getenv("PRINTMASTER_LIVE_LINUX_PACKAGES") != "disposable-github-runner" {
		t.Fatal("native Manager sender fixture must run as the unprivileged service user")
	}
	dir := os.Getenv("PRINTMASTER_TEST_LINUX_MANAGER_CHILD")
	load := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var manifest UpdateManifest
	if err := json.Unmarshal(load("manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(load("ca.pem")) {
		t.Fatal("missing fixture HTTPS CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	verifier, err := updatetrust.NewClient(updatetrust.Options{TrustedRoot: load("root.json"),
		MetadataURL: string(load("url")), CacheDir: filepath.Join(dir, "data", "trust"),
		HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{Enabled: true, CurrentVersion: "1.0.0", DataDir: filepath.Join(dir, "data"),
		BinaryPath: "/usr/bin/printmaster-agent", ServerClient: &mockUpdateClient{}, PublisherVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.usePackageManager || !manager.enabled {
		t.Fatal("installed binary did not select native package updating")
	}
	if err := manager.beginOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := load("candidate")
	run := func(ctx context.Context, timeout time.Duration, dir string, env []string, tool string, args ...string) ([]byte, error) {
		// Only repository downloading is replaced; metadata inspection, both
		// TUF authorizations, the socket and all privileged commands are real.
		if tool == "apt-get" || tool == "dnf" || tool == "yumdownloader" {
			return nil, os.WriteFile(filepath.Join(dir, "candidate."+manifest.Format), body, 0o600)
		}
		return manager.runUpdateCommandIn(ctx, timeout, dir, env, tool, args...)
	}
	var result livePackageManagerResult
	err = manager.executeRepositoryPackageWith(context.Background(), &manifest, run,
		func(ctx context.Context, source *os.File, target updatetrust.Target) (RepositoryPackageOutcome, error) {
			var err error
			result.Outcome, err = SubmitRepositoryPackage(ctx, source, target)
			return result.Outcome, err
		})
	if err != nil {
		result.Error = err.Error()
	}
	if manager.Progress().Status == "succeeded" {
		t.Fatal("old Manager claimed replacement startup success")
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "result.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

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
	command := func(t *testing.T, tool string, args ...string) []byte {
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
				command(t, "/usr/bin/dpkg", "--install", "--", baseline)
			} else {
				command(t, "/usr/bin/rpm", "--upgrade", "--oldpackage", "--replacepkgs", "--", baseline)
			}
			command(t, "/usr/bin/systemctl", "stop", "printmaster-agent-update.socket")
			command(t, "/usr/bin/systemctl", "daemon-reload")
			// Independent scenarios intentionally perform several package and
			// owner restarts in quick succession; reset their fixture rate limit.
			command(t, "/usr/bin/systemctl", "reset-failed", "printmaster-agent.service")
			command(t, "/usr/bin/systemctl", "restart", "printmaster-agent.service")
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
			command(t, "/usr/bin/systemctl", "daemon-reload")
			command(t, "/usr/bin/systemctl", "start", "printmaster-agent-update.socket")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			type result struct {
				outcome RepositoryPackageOutcome
				err     error
			}
			reply := make(chan result, 1)
			clientDir, err := os.MkdirTemp("/tmp", "pm-native-manager-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(clientDir)
			if err := os.Chmod(clientDir, 0o755); err != nil {
				t.Fatal(err)
			}
			account, err := user.Lookup("printmaster")
			if err != nil {
				t.Fatal(err)
			}
			uid, err := strconv.ParseUint(account.Uid, 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			gid, err := strconv.ParseUint(account.Gid, 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(clientDir, "data")
			if err := os.Mkdir(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(dataDir, int(uid), int(gid)); err != nil {
				t.Fatal(err)
			}
			manifestData, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{"candidate": body, "root.json": root,
				"ca.pem":        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
				"manifest.json": manifestData, "url": []byte(server.URL)} {
				write(filepath.Join(clientDir, name), data)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			testBytes, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			childPath := filepath.Join(clientDir, "sender.test")
			if err := os.WriteFile(childPath, testBytes, 0o755); err != nil {
				t.Fatal(err)
			}
			go func() {
				child := exec.CommandContext(ctx, childPath, "-test.run=^TestLiveLinuxPackageManagerSender$")
				child.Env = append(os.Environ(), "PRINTMASTER_TEST_LINUX_MANAGER_CHILD="+clientDir)
				child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
				output, err := child.CombinedOutput()
				if err != nil {
					reply <- result{err: fmt.Errorf("unprivileged Manager exited: %w: %s", err, output)}
					return
				}
				data, err := os.ReadFile(filepath.Join(dataDir, "result.json"))
				var observed livePackageManagerResult
				if err == nil {
					err = json.Unmarshal(data, &observed)
				}
				if err == nil && observed.Error != "" {
					err = errors.New(observed.Error)
				}
				reply <- result{observed.Outcome, err}
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
			pendingData, err := os.ReadFile(filepath.Join(dataDir, updateDirName, pendingUpdateFile))
			if err != nil {
				t.Fatal(err)
			}
			var pending PendingUpdateState
			if err := json.Unmarshal(pendingData, &pending); err != nil {
				t.Fatal(err)
			}
			if pending.RepositoryTarget == nil || *pending.RepositoryTarget != target || pending.FromVersion != "1.0.0" {
				t.Fatalf("unprivileged Manager did not retain exact durable pending operation: %+v", pending)
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
					observation, observationErr := readRepositoryPackageObservation(packageHelperResultPath)
					if observationErr == nil && observation.Phase == wantPhase &&
						observation.RunID == transaction.RunID && observation.Target == target {
						break
					}
					err = fmt.Errorf("missing matching durable root observation: %+v: %v", observation, observationErr)
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
					command(t, "/usr/bin/dpkg", "--install", "--", baseline)
				} else {
					command(t, "/usr/bin/rpm", "--upgrade", "--oldpackage", "--replacepkgs", "--", baseline)
				}
				command(t, "/usr/bin/systemctl", "daemon-reload")
				command(t, "/usr/bin/systemctl", "reset-failed", "printmaster-agent.service")
				command(t, "/usr/bin/systemctl", "restart", "printmaster-agent.service")
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
			command(t, "/usr/bin/systemctl", "stop", "printmaster-agent-update.socket")
			for attempt := 0; attempt < 100; attempt++ {
				output := command(t, "/usr/bin/systemctl", "list-units", "--state=running", "--plain", "--no-legend", "printmaster-agent-update@*.service")
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
