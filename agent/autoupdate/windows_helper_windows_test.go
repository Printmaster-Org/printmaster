//go:build windows

package autoupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
