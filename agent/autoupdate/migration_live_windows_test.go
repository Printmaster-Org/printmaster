//go:build windows

package autoupdate

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	_ "modernc.org/sqlite"
)

func TestLiveLegacyWindowsMSIMigration(t *testing.T) {
	if os.Getenv("PRINTMASTER_LIVE_LEGACY_MSI") != "disposable-github-runner" {
		t.Skip("requires explicitly approved disposable hosted MSI migration")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("legacy MSI rehearsal must never execute on a persistent/local host")
	}
	work := os.Getenv("PRINTMASTER_LIVE_LEGACY_MSI_DIR")
	version := os.Getenv("PRINTMASTER_LIVE_LEGACY_MSI_VERSION")
	if !filepath.IsAbs(work) || (version != "0.31.0" && version != "0.31.1") {
		t.Fatal("invalid native MSI fixture configuration")
	}
	connection, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if existing, err := connection.OpenService("PrintMasterAgent"); err == nil {
		existing.Close()
		t.Fatal("existing service blocks destructive rehearsal")
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Fatal(err)
	}
	dataDir := filepath.Join(os.Getenv("ProgramData"), "PrintMaster", "agent")
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing/unreadable Agent data blocks rehearsal: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dataDir, "config.toml")
	dbPath := filepath.Join(dataDir, "devices.db")
	config := []byte("[server]\nenabled = false\n[auto_update]\nmode = \"disabled\"\n" +
		"[web]\nhttp_port = 8080\nenable_tls = false\n[database]\npath = '" + dbPath + "'\n")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	evidence := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, "evidence", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var transaction int
	runMSI := func(operation, name string, want int) {
		t.Helper()
		transaction++
		path := filepath.Join(work, name+".msi")
		logPath := filepath.Join(work, "evidence", fmt.Sprintf("%02d-%s-%s-msi.log", transaction, name, strings.TrimPrefix(operation, "/")))
		command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "msiexec.exe"),
			operation, path, "/qn", "/norestart", "REBOOT=ReallySuppress", "/l*v", logPath)
		output := new(bytes.Buffer)
		command.Stdout, command.Stderr = output, output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case err := <-done:
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			t.Logf("MSI %s returned %d (expected %d); log=%s", name, code, want, logPath)
			if code != want {
				t.Fatalf("MSI %s exit %d, expected %d; no retry/reboot permitted: %s", name, code, want, output)
			}
		case <-time.After(3 * time.Minute):
			t.Fatal("MSI observation deadline exceeded; leave transaction intact, no termination/retry/reboot")
		}
	}
	install := func(name string, want int) {
		t.Helper()
		runMSI("/i", name, want)
	}
	serviceConfig := func() mgr.Config {
		t.Helper()
		service, err := connection.OpenService("PrintMasterAgent")
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		config, err := service.Config()
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	checkServicePolicy := func() {
		t.Helper()
		service, err := connection.OpenService("PrintMasterAgent")
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		actions, err := service.RecoveryActions()
		if err != nil {
			t.Fatal(err)
		}
		reset, err := service.ResetPeriod()
		if err != nil {
			t.Fatal(err)
		}
		if len(actions) != 3 || reset != 0 {
			t.Fatalf("unexpected MSI recovery policy: actions=%+v reset=%d", actions, reset)
		}
		for _, action := range actions {
			if action.Type != mgr.ServiceRestart || action.Delay != 5*time.Second {
				t.Fatalf("unexpected MSI recovery action: %+v", action)
			}
		}
	}
	control := func(start bool) {
		t.Helper()
		service, err := connection.OpenService("PrintMasterAgent")
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		if !start {
			if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
				t.Fatal(err)
			}
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				status, err := service.Query()
				if err != nil {
					t.Fatal(err)
				}
				if status.State == svc.Stopped {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("service did not stop; refusing to edit live database")
		}
		if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			t.Fatal(err)
		}
	}
	waitVersion := func(want string) {
		t.Helper()
		client := &http.Client{Timeout: 2 * time.Second}
		var last error
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get("http://127.0.0.1:8080/api/version")
			if err == nil {
				var value struct{ Version string }
				err = json.NewDecoder(resp.Body).Decode(&value)
				resp.Body.Close()
				if err == nil && resp.StatusCode == http.StatusOK && value.Version == want {
					service, err := connection.OpenService("PrintMasterAgent")
					if err != nil {
						t.Fatal(err)
					}
					status, err := service.Query()
					service.Close()
					if err == nil && status.State == svc.Running {
						return
					}
				}
				if err == nil {
					err = fmt.Errorf("HTTP %d version %q, want %q", resp.StatusCode, value.Version, want)
				}
			}
			last = err
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("running service version %s not observed: %v", want, last)
	}
	install("baseline", 0)
	// Released MSI starts the service itself. The explicit start is idempotent
	// and tests owner readiness, not a claim about legacy startup reliability.
	control(true)
	waitVersion(version)
	original := serviceConfig()
	if !strings.EqualFold(original.ServiceStartName, "LocalSystem") {
		t.Fatalf("unexpected historical service account: %q", original.ServiceStartName)
	}
	legacyBinary := strings.TrimSpace(strings.Split(original.BinaryPathName, " --service")[0])
	legacyBinary = strings.Trim(legacyBinary, `"`)
	previous, err := fileIdentity(legacyBinary)
	if err != nil {
		t.Fatal(err)
	}
	control(false)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, seedErr := db.Exec(`INSERT INTO devices
		(serial, ip, manufacturer, model, last_seen, created_at, first_seen, is_saved, visible)
		VALUES ('MSI-MIGRATION-QUALIFICATION', '192.0.2.123', 'Fixture', 'Retained printer',
		CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 1)`)
	closeErr := db.Close()
	if seedErr != nil || closeErr != nil {
		t.Fatalf("seed real released database: %v %v", seedErr, closeErr)
	}
	preserved := map[string][]byte{
		configPath:                                  config,
		filepath.Join(dataDir, "agent_id"):          []byte("6e7f0958-425f-4dcc-ae7d-d8a59e5462c4"),
		filepath.Join(dataDir, "agent_token"):       []byte("disposable-msi-machine-token"),
		filepath.Join(dataDir, "server_join_token"): []byte("disposable-msi-join-token"),
	}
	for path, data := range preserved {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkPreserved := func() {
		t.Helper()
		for path, want := range preserved {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("retained file changed %s: %v", path, err)
			}
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		var model, ip, integrity string
		var saved int
		queryErr := db.QueryRow("SELECT model, ip, is_saved FROM devices WHERE serial = 'MSI-MIGRATION-QUALIFICATION'").
			Scan(&model, &ip, &saved)
		integrityErr := db.QueryRow("PRAGMA integrity_check").Scan(&integrity)
		closeErr := db.Close()
		if queryErr != nil || integrityErr != nil || closeErr != nil ||
			model != "Retained printer" || ip != "192.0.2.123" || saved != 1 || integrity != "ok" {
			t.Fatalf("legacy database not preserved: %q %q %d %q: %v %v %v", model, ip, saved, integrity,
				queryErr, integrityErr, closeErr)
		}
	}
	control(true)
	waitVersion(version)
	checkPreserved()
	// Real transaction failure after replacement exercises rollback, not merely
	// invalid-MSI rejection before any changes.
	install("failure", 1603)
	control(true)
	waitVersion(version)
	restored := serviceConfig()
	if restored.BinaryPathName != original.BinaryPathName || restored.ServiceStartName != original.ServiceStartName {
		t.Fatalf("failed MSI did not restore legacy service registration: %+v -> %+v", original, restored)
	}
	if err := previous.verify(legacyBinary); err != nil {
		t.Fatalf("rollback changed previous executable: %v", err)
	}
	checkPreserved()
	install("candidate", 0)
	// Do not manually repair/start the candidate: MSI must own its handoff.
	waitVersion("0.32.0")
	binary := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	current := serviceConfig()
	if !strings.EqualFold(current.BinaryPathName, `"`+binary+`" --service run`) ||
		!strings.EqualFold(current.ServiceStartName, "LocalSystem") || current.StartType != mgr.StartAutomatic ||
		!current.DelayedAutoStart {
		t.Fatalf("migration did not bind protected SYSTEM service: %+v", current)
	}
	checkServicePolicy()
	want, err := fileIdentity(filepath.Join(work, "candidate.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if err := want.verify(binary); err != nil {
		t.Fatal(err)
	}
	product, isMSI, err := checkMSIInstallation(binary)
	if err != nil || !isMSI || product == "" {
		t.Fatalf("candidate is not uniquely owned by Windows Installer: %q %v %v", product, isMSI, err)
	}
	checkPreserved()
	if err := validateInstallPath(binary); err != nil {
		t.Fatal(err)
	}
	// Fail closed on standard-user writes without repairing the tested ACLs.
	token := restrictedUsersToken(t)
	assertRestrictedTokenPositiveControl(t, token)
	for _, path := range []string{binary, configPath, filepath.Join(dataDir, "agent_token")} {
		if err := openWithRestrictedToken(token, path, windows.GENERIC_WRITE, windows.OPEN_EXISTING); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("standard Users can modify protected migrated path %s: %v", path, err)
		}
	}
	// Also cover the new native registration on fresh service creation, not
	// only reconfiguration of an existing legacy SCM record.
	runMSI("/x", "candidate", 0)
	if service, err := connection.OpenService("PrintMasterAgent"); err == nil {
		service.Close()
		t.Fatal("native MSI uninstall left the service registered")
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Fatal(err)
	}
	checkPreserved()
	install("candidate", 0)
	waitVersion("0.32.0")
	fresh := serviceConfig()
	if fresh.BinaryPathName != current.BinaryPathName || fresh.ServiceStartName != current.ServiceStartName ||
		fresh.StartType != current.StartType || fresh.DelayedAutoStart != current.DelayedAutoStart {
		t.Fatalf("fresh native service registration differs from migrated registration: %+v", fresh)
	}
	checkServicePolicy()
	checkPreserved()
	result, err := json.MarshalIndent(map[string]any{
		"legacy_version": version, "candidate_version": "0.32.0",
		"legacy_service_path": original.BinaryPathName, "service_path": current.BinaryPathName,
		"service_account": current.ServiceStartName, "product_code": product, "installed_sha256": want.SHA256,
		"service_policy": "automatic delayed start; three restart actions at five seconds; reset zero days",
		"rollback":       "real MSI exit 1603 restored legacy binary, service registration and retained data",
		"preserved":      []string{"configuration", "agent_id", "agent_token", "server_join_token", "saved printer", "SQLite integrity"},
		"reboot":         "all MSI operations /norestart REBOOT=ReallySuppress; only exact expected exits accepted",
		"trust":          "production root unchanged; owner-controlled installer rehearsal only",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	evidence("result.json", result)
}
