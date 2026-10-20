//go:build windows

package autoupdate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsPowerShellCommandUsesBuiltinModules(t *testing.T) {
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "Microsoft.PowerShell.Security")
	if err := os.MkdirAll(moduleDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(moduleDir, "Microsoft.PowerShell.Security.psm1"),
		[]byte("throw 'incompatible inherited module was loaded'"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PSModulePath", dir+string(os.PathListSeparator)+os.Getenv("PSModulePath"))
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	wantModules := filepath.Join(systemDir, "WindowsPowerShell", "v1.0", "Modules")
	var effectivePolicy string
	for _, fileMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy command", true: "helper file"}[fileMode], func(t *testing.T) {
			if fileMode {
				if err := validateWindowsHelperPolicy(effectivePolicy); err != nil {
					t.Skipf("machine policy blocks unsigned fixture: %v", err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			script := `$ErrorActionPreference = 'Stop'
Import-Module Microsoft.PowerShell.Security -ErrorAction Stop
$services = @(Get-Service -ErrorAction Stop)
if ($services.Count -eq 0) { throw 'No Windows services found' }
ConvertTo-Json -Compress @{
    policy = [string](Get-ExecutionPolicy)
    modules = $env:PSModulePath
    security = (Get-Module Microsoft.PowerShell.Security).ModuleBase
}
`
			args := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
			if fileMode {
				path := filepath.Join(dir, "helper.ps1")
				if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}
			}
			cmd, err := windowsPowerShellCommand(ctx, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(cmd.Path, filepath.Join(systemDir, "WindowsPowerShell", "v1.0", "powershell.exe")) {
				t.Fatalf("unexpected PowerShell executable: %s", cmd.Path)
			}
			moduleEntries := 0
			for _, entry := range cmd.Env {
				name, value, _ := strings.Cut(entry, "=")
				if strings.EqualFold(name, "PSModulePath") {
					moduleEntries++
					if !strings.EqualFold(value, wantModules) {
						t.Fatalf("unexpected child module environment: %s", entry)
					}
				}
			}
			if moduleEntries != 1 {
				t.Fatalf("expected exactly one child module path, got %d", moduleEntries)
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Windows PowerShell failed: %v\n%s", err, output)
			}
			var result struct {
				Policy   string `json:"policy"`
				Modules  string `json:"modules"`
				Security string `json:"security"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatalf("invalid output %q: %v", output, err)
			}
			// Windows PowerShell adds its standard installation paths at startup;
			// its binary security module reports PSHOME as its module base.
			if result.Policy == "" || strings.Contains(strings.ToLower(result.Modules), strings.ToLower(dir)) ||
				!strings.Contains(strings.ToLower(result.Modules), strings.ToLower(wantModules)) ||
				!strings.EqualFold(result.Security, filepath.Dir(wantModules)) {
				t.Fatalf("unexpected policy/module resolution: %+v", result)
			}
			effectivePolicy = result.Policy
		})
	}
}

func TestDetachedWindowsHelperStartsScript(t *testing.T) {
	const markerEnv = "PRINTMASTER_TEST_DETACHED_MARKER"
	if marker := os.Getenv(markerEnv); marker != "" {
		path := filepath.Join(filepath.Dir(marker), "helper.ps1")
		script := "$ErrorActionPreference = 'Stop'\n" +
			"$deadline = [DateTime]::UtcNow.AddSeconds(10)\n" +
			"while (Get-Process -Id " + strconv.Itoa(os.Getpid()) + " -ErrorAction SilentlyContinue) {\n" +
			"if ([DateTime]::UtcNow -ge $deadline) { exit 1 }; Start-Sleep -Milliseconds 100\n}\n" +
			"[IO.File]::WriteAllText(" + psLiteral(marker) + ", 'started')"
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := launchDetachedWindowsHelper(path); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestDetachedWindowsHelperStartsScript$", "-test.v")
	child.Env = append(os.Environ(), markerEnv+"="+marker)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("helper parent failed: %v\n%s", err, output)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			if string(data) != "started" {
				t.Fatalf("unexpected marker: %q", data)
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("detached Windows PowerShell exited without executing the harmless helper script")
}
