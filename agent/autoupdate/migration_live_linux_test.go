package autoupdate

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// This is owner-controlled installation, not a legacy fleet self-update.
// Never select it in ordinary tests or on a persistent/local machine.
func TestLiveLegacyLinuxMigration(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_LEGACY_MIGRATION") != "disposable-github-runner" {
		t.Skip("requires explicit disposable hosted legacy migration qualification")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" ||
		os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("native legacy migration is restricted to disposable root CI")
	}
	work := os.Getenv("PRINTMASTER_LIVE_LEGACY_MIGRATION_DIR")
	format := os.Getenv("PRINTMASTER_LIVE_LEGACY_MIGRATION_FORMAT")
	version := os.Getenv("PRINTMASTER_LIVE_LEGACY_MIGRATION_VERSION")
	if !filepath.IsAbs(work) || (format != "deb" && format != "rpm") ||
		(version != "0.31.0" && version != "0.31.1") {
		t.Fatal("invalid native migration fixture configuration")
	}
	ctx := t.Context()
	command := func(tool string, args ...string) []byte {
		t.Helper()
		output, err := runPrivilegedPackageCommand(ctx, time.Minute, "/", nil, tool, args...)
		if err != nil {
			t.Fatalf("%s %v: %v: %s", tool, args, err, output)
		}
		return output
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	const dataDir = "/var/lib/printmaster"
	const configPath = "/etc/printmaster/agent.toml"
	config := []byte("[server]\nenabled = false\n[auto_update]\nmode = \"disabled\"\n" +
		"[web]\nhttp_port = 8080\nenable_tls = false\n[database]\npath = \"/var/lib/printmaster/devices.db\"\n")
	write(configPath, config, 0o640)
	// Explicit owner configuration, retained across both package versions.
	// Do not override ExecStart: qualify the shipped service and executable.
	write("/etc/systemd/system/printmaster-agent.service.d/migration-environment.conf",
		[]byte("[Service]\nEnvironment=AGENT_CONFIG=/etc/printmaster/agent.toml\n"+
			"Environment=PRINTMASTER_DATA_DIR=/var/lib/printmaster\n"), 0o644)
	install := func(path string) {
		t.Helper()
		if format == "deb" {
			command("/usr/bin/dpkg", "--install", "--", path)
		} else {
			command("/usr/bin/rpm", "--upgrade", "--", path)
		}
	}
	baseline := filepath.Join(work, "baseline."+format)
	candidate := filepath.Join(work, "candidate."+format)
	install(baseline)
	account, err := user.Lookup("printmaster")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configPath, 0, gid); err != nil {
		t.Fatal(err)
	}
	command("/usr/bin/systemctl", "daemon-reload")
	command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
	client := &http.Client{Timeout: 2 * time.Second}
	waitVersion := func(want string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		var last error
		for time.Now().Before(deadline) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/api/version", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err == nil {
				var value struct {
					Version string `json:"version"`
				}
				err = json.NewDecoder(resp.Body).Decode(&value)
				resp.Body.Close()
				if err == nil && resp.StatusCode == http.StatusOK && value.Version == want {
					state, serviceErr := queryNativeService(ctx, "/", runPrivilegedPackageCommand)
					if serviceErr == nil {
						if err := verifyNativeServiceExecutable(state.PID, "/usr/bin/printmaster-agent"); err != nil {
							t.Fatal(err)
						}
						return
					}
					err = serviceErr
				}
				if err == nil {
					err = fmt.Errorf("HTTP %d version %q, want %q", resp.StatusCode, value.Version, want)
				}
			}
			last = err
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("service did not become ready at version %s: %v", want, last)
	}
	waitVersion(version)
	command("/usr/bin/systemctl", "stop", "printmaster-agent.service")
	dbPath := filepath.Join(dataDir, "devices.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Use a real historical schema, not a new-schema database mislabeled old.
	_, seedErr := db.ExecContext(ctx, `INSERT INTO devices
		(serial, ip, manufacturer, model, last_seen, created_at, first_seen, is_saved, visible)
		VALUES ('MIGRATION-QUALIFICATION', '192.0.2.123', 'Fixture', 'Retained printer',
		CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 1)`)
	closeErr := db.Close()
	if seedErr != nil || closeErr != nil {
		t.Fatalf("seed released database: %v %v", seedErr, closeErr)
	}
	preserved := map[string][]byte{
		configPath:                                  config,
		filepath.Join(dataDir, "agent_id"):          []byte("bde9f16f-77e4-4cf3-aedf-01ad39aebaf6"),
		filepath.Join(dataDir, "agent_token"):       []byte("disposable-migration-machine-token"),
		filepath.Join(dataDir, "server_join_token"): []byte("disposable-migration-join-token"),
	}
	for path, data := range preserved {
		if path != configPath {
			write(path, data, 0o600)
			if err := os.Chown(path, uid, gid); err != nil {
				t.Fatal(err)
			}
		}
	}
	checkPreserved := func() {
		t.Helper()
		for path, want := range preserved {
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, want) {
				t.Fatalf("migration changed retained file %s: %v", path, err)
			}
		}
		current, err := user.Lookup("printmaster")
		if err != nil || current.Uid != account.Uid || current.Gid != account.Gid {
			t.Fatalf("migration changed service identity: %+v %v", current, err)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		var model, ip, integrity string
		var saved int
		queryErr := db.QueryRowContext(ctx, "SELECT model, ip, is_saved FROM devices WHERE serial = 'MIGRATION-QUALIFICATION'").
			Scan(&model, &ip, &saved)
		integrityErr := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
		closeErr := db.Close()
		if queryErr != nil || integrityErr != nil || closeErr != nil ||
			model != "Retained printer" || ip != "192.0.2.123" || saved != 1 || integrity != "ok" {
			t.Fatalf("database not preserved: model=%q ip=%q saved=%d integrity=%q: %v %v %v",
				model, ip, saved, integrity, queryErr, integrityErr, closeErr)
		}
	}
	command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
	waitVersion(version)
	checkPreserved()
	oldBinary, err := os.ReadFile("/usr/bin/printmaster-agent")
	if err != nil {
		t.Fatal(err)
	}
	// Invalid owner input must be rejected before stopping/replacing the old
	// service. No production updater or installer failure is simulated here.
	damaged := filepath.Join(work, "damaged."+format)
	write(damaged, []byte("not a native package"), 0o600)
	tool, args := "/usr/bin/dpkg", []string{"--install", "--", damaged}
	if format == "rpm" {
		tool, args = "/usr/bin/rpm", []string{"--upgrade", "--", damaged}
	}
	output, err := runPrivilegedPackageCommand(ctx, time.Minute, "/", nil, tool, args...)
	if err == nil {
		t.Fatal("invalid owner-supplied package was accepted")
	}
	write(filepath.Join(work, "evidence", "rejected-package.txt"), output, 0o644)
	stillInstalled, err := os.ReadFile("/usr/bin/printmaster-agent")
	if err != nil || !bytes.Equal(stillInstalled, oldBinary) {
		t.Fatalf("invalid package changed old binary: %v", err)
	}
	waitVersion(version)
	checkPreserved()
	install(candidate)
	command("/usr/bin/systemctl", "daemon-reload")
	command("/usr/bin/systemctl", "reset-failed", "printmaster-agent.service")
	command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
	waitVersion("0.32.0")
	checkPreserved()
	newBinary, err := os.ReadFile("/usr/bin/printmaster-agent")
	if err != nil {
		t.Fatal(err)
	}
	expectedBinary, err := os.ReadFile(filepath.Join(work, "agent"))
	if err != nil || !bytes.Equal(newBinary, expectedBinary) {
		t.Fatalf("package did not install exact candidate: %v", err)
	}
	grants, err := os.ReadFile("/etc/sudoers.d/printmaster-agent")
	if err != nil || strings.Contains(string(grants), "NOPASSWD:") {
		t.Fatalf("native upgrade retained historical sudo grants: %v", err)
	}
	for _, helper := range []string{"/usr/libexec/printmaster-agent-update", "/usr/libexec/printmaster-agent-update-recovery"} {
		if err := checkRootPackageExecutable(helper); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{packageHelperConfigPath, packageHelperRootPath, packageHelperPolicyPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owner migration unexpectedly provisioned production helper authority: %s %v", path, err)
		}
	}
	if output, err := runPrivilegedPackageCommand(ctx, 10*time.Second, "/", nil,
		"/usr/bin/systemctl", "is-active", "printmaster-agent-update.socket"); err == nil {
		t.Fatalf("unprovisioned helper socket activated: %s", output)
	}
	// A failed service start is not success or permission to downgrade a
	// migrated database. Owner repairs configuration and restarts the same build.
	broken := "/etc/systemd/system/printmaster-agent.service.d/migration-failure.conf"
	write(broken, []byte("[Service]\nExecStart=\nExecStart=/usr/libexec/nonexistent-migration-fixture\n"), 0o644)
	command("/usr/bin/systemctl", "stop", "printmaster-agent.service")
	command("/usr/bin/systemctl", "daemon-reload")
	startOutput, startErr := runPrivilegedPackageCommand(ctx, 15*time.Second, "/", nil,
		"/usr/bin/systemctl", "start", "printmaster-agent.service")
	t.Logf("deliberately broken service start: %v: %s", startErr, startOutput)
	time.Sleep(time.Second)
	if state, err := queryNativeService(ctx, "/", runPrivilegedPackageCommand); err == nil {
		t.Fatalf("broken replacement service claimed ready: %+v", state)
	}
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	command("/usr/bin/systemctl", "daemon-reload")
	command("/usr/bin/systemctl", "reset-failed", "printmaster-agent.service")
	command("/usr/bin/systemctl", "restart", "printmaster-agent.service")
	waitVersion("0.32.0")
	checkPreserved()
	hash := sha256.Sum256(newBinary)
	evidence, err := json.MarshalIndent(map[string]any{
		"legacy_version": version, "candidate_version": "0.32.0", "format": format,
		"installed_sha256": hex.EncodeToString(hash[:]), "service_uid": account.Uid,
		"preserved":       []string{"configuration", "agent_id", "agent_token", "server_join_token", "saved printer", "database integrity"},
		"invalid_package": "rejected; legacy service and data preserved",
		"startup_failure": "detected; owner repaired configuration and restarted same candidate",
		"activation":      "unprovisioned helper socket inactive",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(work, "evidence", "result.json"), evidence, 0o644)
	t.Logf("real %s -> 0.32.0 %s migration and owner service repair passed", version, format)
}
