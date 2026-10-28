package tenancy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

type enrollmentSubjectKey struct{}

func TestInstallInstructionsPreserveDownloadAndEscapeContent(t *testing.T) {
	code := "instructions-test-fixture"
	entry := installEntry{Script: "<script>alert('fixture')</script>", Filename: "install.ps1",
		ExpiresAt: time.Now().UTC().Add(time.Hour), OneTime: true,
		OneLiner: "irm https://example.invalid/install/fixture | iex", Platform: "windows",
		ServerURL: "https://example.invalid/<img src=x onerror=alert(1)>"}
	installStore.mu.Lock()
	installStore.m[code] = entry
	installStore.mu.Unlock()
	t.Cleanup(func() {
		installStore.mu.Lock()
		delete(installStore.m, code)
		installStore.mu.Unlock()
	})
	for _, request := range []struct{ method, suffix string }{
		{http.MethodGet, "/instructions"}, {http.MethodGet, "/instructions"},
		{http.MethodHead, "/instructions"}, {http.MethodHead, ""},
		{http.MethodPost, "/instructions"}, {http.MethodGet, "/unknown"},
	} {
		response := httptest.NewRecorder()
		handleInstall(response, httptest.NewRequest(request.method, "/install/"+code+request.suffix, nil))
		want := http.StatusOK
		if request.method == http.MethodPost {
			want = http.StatusMethodNotAllowed
		} else if request.suffix == "/unknown" {
			want = http.StatusNotFound
		}
		if response.Code != want {
			t.Fatalf("%s %s: status %d want %d", request.method, request.suffix, response.Code, want)
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("install response permits caching or referrer disclosure")
		}
		if request.method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("HEAD wrote a response body")
		}
		if request.method == http.MethodGet && request.suffix == "/instructions" {
			body := response.Body.String()
			for _, text := range []string{"&lt;script&gt;", "&lt;img", "Copy one-liner", "<details>", "Administrator PowerShell", "Keep this enrollment link private", "Initial enrollment trusts this Server"} {
				if !strings.Contains(body, text) {
					t.Fatalf("instructions missing %q", text)
				}
			}
			if strings.Contains(body, entry.Script) || strings.Contains(body, "<img src=x") {
				t.Fatal("instructions contains unescaped payload")
			}
			if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'none'") {
				t.Fatal("instructions missing restrictive CSP")
			}
		}
		installStore.mu.Lock()
		_, exists := installStore.m[code]
		installStore.mu.Unlock()
		if !exists {
			t.Fatal("preview, HEAD or invalid request consumed a one-time script")
		}
	}
	response := httptest.NewRecorder()
	handleInstall(response, httptest.NewRequest(http.MethodGet, "/install/"+code, nil))
	if response.Body.String() != entry.Script || !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("script download changed")
	}
	response = httptest.NewRecorder()
	handleInstall(response, httptest.NewRequest(http.MethodGet, "/install/"+code+"/instructions", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("used one-time link still exposes instructions")
	}
	entry.ExpiresAt = time.Now().UTC().Add(-time.Second)
	installStore.mu.Lock()
	installStore.m[code] = entry
	installStore.mu.Unlock()
	response = httptest.NewRecorder()
	handleInstall(response, httptest.NewRequest(http.MethodGet, "/install/"+code+"/instructions", nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), entry.OneLiner) {
		t.Fatal("expired link exposes installation data")
	}
}

func TestWindowsBootstrapConsole(t *testing.T) {
	available := false
	for _, name := range []string{"pwsh", "powershell"} {
		powershell, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		available = true
		t.Run(name, func(t *testing.T) {
			testWindowsBootstrapConsole(t, powershell)
		})
	}
	if !available {
		t.Skip("PowerShell required for bootstrap display runtime tests")
	}
}

func testWindowsBootstrapConsole(t *testing.T, powershell string) {
	t.Helper()
	script, _ := buildBootstrapScript("windows", "https://example.invalid", "disposable-test-token")
	helperEnd := strings.Index(script, "function Assert-Administrator")
	if helperEnd < 0 {
		t.Fatal("display helper boundary missing")
	}
	if strings.Contains(script[:helperEnd], "Clear-Host") {
		t.Fatal("bootstrap must not clear preexisting console output")
	}
	directory := t.TempDir()
	generatedPath := filepath.Join(directory, "generated.ps1")
	if err := os.WriteFile(generatedPath, append([]byte{0xef, 0xbb, 0xbf}, []byte(script)...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name        string
		interactive bool
		success     bool
	}{
		{"interactive success", true, true},
		{"interactive failure", true, false},
		{"redirected success", false, true},
		{"redirected failure", false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := script[:helperEnd] + fmt.Sprintf(`
$script:interactiveProgress = $%t
$ColorReset = $ColorRed = $ColorGreen = $ColorYellow = $ColorCyan = ""
$ColorWhite = $ColorBold = $ColorDim = ""
$script:messages = [System.Collections.Generic.List[string]]::new()
$script:updates = [System.Collections.Generic.List[int]]::new()
$script:completions = 0
function Write-Host {
    param([object]$Object)
    [void]$script:messages.Add([string]$Object)
}
function Write-Progress {
    param([int]$Id, [string]$Activity, [string]$Status, [int]$PercentComplete, [switch]$Completed)
    if ($Completed) { $script:completions++ }
    else { [void]$script:updates.Add($PercentComplete) }
}
$parseTokens = $null
$parseErrors = $null
$null = [System.Management.Automation.Language.Parser]::ParseFile(
    (Join-Path $PSScriptRoot 'generated.ps1'), [ref]$parseTokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Generated bootstrap does not parse' }
Show-Banner
Show-Progress -Percent -1 -Message 'Preparing'
Show-Progress -Percent 35 -Message 'Downloading'
Show-Info 'Download details'
Show-Success 'Download complete'
Show-Warning 'Warning remains visible'
Show-Error 'Error remains visible'
Show-Progress -Percent 101 -Message 'Finishing'
Show-CompletionBox -Success $%t -Message 'Finished fixture'
Write-Output (ConvertTo-Json -Compress -Depth 4 -InputObject @{
    messages = @($script:messages.ToArray())
    updates = @($script:updates.ToArray())
    completions = $script:completions
})
`, scenario.interactive, scenario.success)
			path := filepath.Join(directory, "display-test.ps1")
			if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, []byte(harness)...), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-File", path).CombinedOutput()
			if err != nil {
				t.Fatalf("display helpers failed: %v\n%s", err, output)
			}
			var result struct {
				Messages    []string `json:"messages"`
				Updates     []int    `json:"updates"`
				Completions int      `json:"completions"`
			}
			if err := json.Unmarshal(bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf}), &result); err != nil {
				t.Fatalf("invalid helper result: %v\n%s", err, output)
			}
			messages := strings.Join(result.Messages, "\n")
			for _, diagnostic := range []string{"Warning remains visible", "Error remains visible"} {
				if !strings.Contains(messages, diagnostic) {
					t.Fatalf("lost diagnostic %q: %s", diagnostic, messages)
				}
			}
			icon := "[X]"
			if scenario.success {
				icon = "[OK]"
			}
			if !strings.Contains(messages, icon+" Finished fixture") {
				t.Fatalf("completion summary missing: %s", messages)
			}
			if scenario.interactive {
				if strings.Contains(messages, "Download") || len(result.Updates) != 5 || result.Completions != 3 {
					t.Fatalf("interactive progress accumulated or was not completed: %+v", result)
				}
				if result.Updates[0] != 0 || result.Updates[len(result.Updates)-1] != 100 {
					t.Fatalf("progress not clamped: %+v", result.Updates)
				}
			} else if len(result.Updates) != 0 || result.Completions != 0 || !strings.Contains(messages, "[35/100] Downloading") ||
				!strings.Contains(messages, "[OK] Download complete") || !strings.Contains(messages, "[*] Download details") {
				t.Fatalf("redirected output lost readable status: %+v", result)
			}
		})
	}
}

func TestWindowsBootstrapFreshInstallOnly(t *testing.T) {
	script, _ := buildBootstrapScript("windows", "https://example.invalid", "disposable-test-token")
	guardStart := strings.Index(script, "function Assert-FreshAgentInstall")
	mainStart := strings.Index(script, "# ========== MAIN INSTALLATION ==========")
	if guardStart < 0 || mainStart <= guardStart {
		t.Fatal("fresh-install guard boundary missing")
	}
	main := script[mainStart:]
	guardCall := strings.Index(main, "\n\tAssert-FreshAgentInstall\n")
	if guardCall < 0 || !strings.Contains(main[:strings.Index(main, "# Step 2:")], "exit 1") {
		t.Fatal("bootstrap must exit nonzero when the fresh-install guard fails")
	}
	for _, mutation := range []string{"New-Item", "Set-Content", "Download-WithFallback", "Copy-Item", "Start-Process"} {
		if index := strings.Index(main, mutation); index < 0 || index <= guardCall {
			t.Fatalf("fresh-install check must precede %s", mutation)
		}
	}
	for _, removed := range []string{"Remove-ExistingInstallation", "Stop-Service", "Stop-Process", "msiexec.exe", "sc.exe delete", "Removing old binaries"} {
		if strings.Contains(script, removed) {
			t.Fatalf("bootstrap retains destructive existing-install behavior: %s", removed)
		}
	}
	harness := script[guardStart:mainStart] + `
$env:ProgramFiles = Join-Path $PSScriptRoot 'programfiles'
${env:ProgramFiles(x86)} = Join-Path $PSScriptRoot 'programfiles-x86'
$configDir = Join-Path $PSScriptRoot 'agent-data'
$installDir = Join-Path $PSScriptRoot 'agent-bin'
$ErrorActionPreference = 'Stop'
function Get-Service {
    if ($script:scenario -eq 'service query failure') { throw 'service inspection failed' }
    if ($script:scenario -in @('running service', 'stopped service')) {
        [pscustomobject]@{ Name = 'PrintMasterAgent'; Status = $script:scenario }
    }
}
function Get-Process {
    if ($script:scenario -eq 'process query failure') { throw 'process inspection failed' }
    if ($script:scenario -eq 'running process') { [pscustomobject]@{ ProcessName = 'printmaster-agent' } }
	if ($script:scenario -eq 'versioned process') { [pscustomobject]@{ ProcessName = 'printmaster-agent-v0.31.1-windows-amd64' } }
}
function Test-Path {
    param([string]$LiteralPath)
    if ($script:scenario -eq 'path query failure') { throw 'path inspection failed' }
    if ($LiteralPath -eq $configDir) { return $script:scenario -eq 'retained data' }
    if ($LiteralPath -eq $installDir) { return $script:scenario -eq 'legacy bin' }
    if ($LiteralPath -eq 'HKLM:\SYSTEM\CurrentControlSet\Services\PrintMasterAgent') { return $script:scenario -eq 'service registry' }
    if ($LiteralPath -like 'HKLM:*\Uninstall\PrintMasterAgent') { return $script:scenario -eq 'raw uninstall registry' }
    if ($LiteralPath -like 'HKLM:*\Uninstall') {
        if ($script:scenario -eq 'msi wow') { return $LiteralPath.Contains('WOW6432Node') }
        return $script:scenario -in @('msi native', 'registry query failure', 'server only')
    }
    if ($LiteralPath -eq (Join-Path $env:ProgramFiles 'PrintMaster\Agent')) { return $script:scenario -eq 'protected directory' }
    if ($LiteralPath -eq (Join-Path ${env:ProgramFiles(x86)} 'PrintMaster\Agent')) { return $script:scenario -eq 'x86 protected directory' }
    if ($LiteralPath -eq (Join-Path $env:ProgramFiles 'PrintMaster')) { return $script:scenario -in @('legacy executable', 'server only') }
    return $false
}
function Get-ItemProperty {
    param([string]$Path)
    if ($script:scenario -eq 'registry query failure') { throw 'registry inspection failed' }
    if ($script:scenario -in @('msi native', 'msi wow')) { [pscustomobject]@{ DisplayName = 'PrintMaster Agent'; PSChildName = '{TEST-PRODUCT}' } }
    if ($script:scenario -eq 'server only') { [pscustomobject]@{ DisplayName = 'PrintMaster Server' } }
}
function Get-ChildItem {
    if ($script:scenario -eq 'legacy executable') { [pscustomobject]@{ Name = 'printmaster-agent-v0.31.1.exe' } }
}
foreach ($command in @('Stop-Service', 'Stop-Process', 'Remove-Item', 'New-Item', 'Set-Content', 'Copy-Item', 'Start-Process', 'Invoke-WebRequest')) {
    Set-Item -Path "function:$command" -Value { throw 'MUTATION ATTEMPTED' }
}
$results = @()
foreach ($script:scenario in @('fresh', 'server only', 'running service', 'stopped service', 'service query failure',
	'process query failure', 'running process', 'versioned process', 'service registry', 'raw uninstall registry', 'msi native', 'msi wow',
    'registry query failure', 'path query failure', 'retained data', 'legacy bin', 'protected directory', 'x86 protected directory', 'legacy executable')) {
    $refused = $false
    $reason = ''
    try { Assert-FreshAgentInstall }
    catch { $refused = $true; $reason = $_.Exception.Message }
    $results += @{ scenario = $script:scenario; refused = $refused; reason = $reason }
}
Write-Output (ConvertTo-Json -Compress -Depth 4 -InputObject $results)
`
	available := false
	for _, name := range []string{"pwsh", "powershell"} {
		powershell, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		available = true
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh-install-test.ps1")
			if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, []byte(harness)...), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-File", path).CombinedOutput()
			if err != nil {
				t.Fatalf("guard helpers failed: %v\n%s", err, output)
			}
			var results []struct {
				Scenario string `json:"scenario"`
				Refused  bool   `json:"refused"`
				Reason   string `json:"reason"`
			}
			if err := json.Unmarshal(bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf}), &results); err != nil {
				t.Fatalf("invalid guard result: %v\n%s", err, output)
			}
			if len(results) != 19 {
				t.Fatalf("missing scenarios: %+v", results)
			}
			for _, result := range results {
				wantRefused := result.Scenario != "fresh" && result.Scenario != "server only"
				if result.Refused != wantRefused || strings.Contains(result.Reason, "MUTATION ATTEMPTED") {
					t.Errorf("scenario %q: refused=%t want=%t reason=%s", result.Scenario, result.Refused, wantRefused, result.Reason)
				}
			}
		})
	}
	if !available {
		t.Skip("PowerShell required for existing-install guard runtime tests")
	}
}

func TestWindowsBootstrapStoresJoinTokenAfterServiceDirectoryProtection(t *testing.T) {
	script, _ := buildBootstrapScript("windows", "https://example.invalid", "disposable-test-token")
	if strings.Contains(script, `token = "$token"`) {
		t.Fatal("bootstrap must not store the enrollment credential as the Agent bearer token")
	}
	serviceInstall := strings.Index(script, `-ArgumentList "--service","install","--silent"`)
	tokenWrite := strings.Index(script, `WriteAllText($joinTokenPath, $token`)
	serviceStart := strings.Index(script, `-ArgumentList "--service","start","--silent"`)
	if serviceInstall < 0 || tokenWrite <= serviceInstall || serviceStart <= tokenWrite {
		t.Fatal("bootstrap must store server_join_token after protected service installation and before service start")
	}
	if !strings.Contains(script, `$joinTokenPath = Join-Path $configDir "server_join_token"`) ||
		!strings.Contains(script, `[System.Text.UTF8Encoding]::new($false)`) {
		t.Fatal("bootstrap join token path or BOM-free encoding is incorrect")
	}
}

func TestBootstrapTLSVerificationDefaultsSecureAndCanBeExplicitlySkipped(t *testing.T) {
	for _, platform := range []string{"windows", "linux"} {
		t.Run(platform, func(t *testing.T) {
			secure, _ := buildBootstrapScript(platform, "https://example.invalid", "fixture-token")
			insecure, _ := buildBootstrapScript(platform, "https://example.invalid", "fixture-token", true)
			if !strings.Contains(secure, "insecure_skip_verify = false") {
				t.Fatal("default bootstrap must verify server TLS certificates")
			}
			if !strings.Contains(insecure, "insecure_skip_verify = true") {
				t.Fatal("explicit TLS bypass option was not rendered")
			}
			if strings.Contains(secure, "token = \"$token\"") || !strings.Contains(secure, "server_join_token") {
				t.Fatal("bootstrap must persist the enrollment token where the Agent worker loads it")
			}
			if platform == "linux" {
				for _, required := range []string{
					"ensure_linux_service_user()",
					"getent group printmaster",
					"useradd --system --gid printmaster",
					"User=printmaster",
					"Group=printmaster",
					"chown printmaster:printmaster \"$DATA_DIR/server_join_token\"",
					"DATA_DIR=\"${HOME}/.local/share/printmaster/agent\"",
				} {
					if !strings.Contains(secure, required) {
						t.Errorf("Linux bootstrap missing service account setup %q", required)
					}
				}
				if strings.Contains(secure, "token = \"$TOKEN\"") {
					t.Fatal("Linux bootstrap must not write the enrollment token to config.toml")
				}
				serviceSection := strings.Index(secure, "[Service]\n")
				userDirective := strings.Index(secure, "User=printmaster")
				if serviceSection < 0 || userDirective <= serviceSection {
					t.Fatal("Linux bootstrap service directives must be inside the systemd Service section")
				}
			}
		})
	}
}

func setupEnrollmentSecurity(t *testing.T, role storage.Role) (*storage.SQLiteStore, *http.ServeMux, string) {
	t.Helper()
	db, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousAuth, previousAuthorizer, previousEnabled := dbStore, AuthMiddleware, authorizer, tenancyEnabled
	SetEnabled(true)
	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := db.CreateTenant(context.Background(), &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	user := &storage.User{Username: "reviewer", Role: role, TenantIDs: []string{"tenant-a"}}
	if err := db.CreateUser(context.Background(), user, "test-only-password"); err != nil {
		t.Fatal(err)
	}
	session, err := db.CreateSession(context.Background(), user.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the route middleware hook against actual persisted sessions;
	// production main-package middleware cannot be imported by this package.
	AuthMiddleware = func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sess, err := db.GetSessionByToken(r.Context(), r.Header.Get("Authorization"))
			if err != nil || sess == nil || sess.ExpiresAt.Before(time.Now()) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			persisted, err := db.GetUserByID(r.Context(), sess.UserID)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			subject := authz.Subject{Role: persisted.Role, IsAdmin: persisted.Role == storage.RoleAdmin, AllowedTenantIDs: persisted.TenantIDs}
			next(w, r.WithContext(context.WithValue(r.Context(), enrollmentSubjectKey{}, subject)))
		}
	}
	SetAuthorizer(func(r *http.Request, action authz.Action, ref authz.ResourceRef) error {
		subject, ok := r.Context().Value(enrollmentSubjectKey{}).(authz.Subject)
		if !ok {
			return authz.ErrUnauthorized
		}
		return authz.Authorize(subject, action, ref)
	})
	mux := http.NewServeMux()
	RegisterRoutesOnMux(mux, db)
	t.Cleanup(func() {
		dbStore, AuthMiddleware, authorizer, tenancyEnabled = previousDB, previousAuth, previousAuthorizer, previousEnabled
		db.Close()
	})
	return db, mux, session.Token
}

func TestPendingRegistrationRequiresGlobalAdmin(t *testing.T) {
	for _, role := range []storage.Role{storage.RoleOperator, storage.RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			db, mux, sessionToken := setupEnrollmentSecurity(t, role)
			for _, tc := range []struct{ method, path, body string }{
				{http.MethodGet, "/api/v1/pending-registrations", ""},
				{http.MethodGet, "/api/v1/pending-registrations/999", ""},
				{http.MethodPost, "/api/v1/pending-registrations/999", `{"action":"approve","tenant_id":"tenant-b"}`},
				{http.MethodPost, "/api/v1/pending-registrations/999", `{"action":"reject"}`},
				{http.MethodDelete, "/api/v1/pending-registrations/999", ""},
				{http.MethodDelete, "/api/v1/agent-deployments/deployment-a", ""},
				{http.MethodPost, "/api/v1/agent-deployments/deployment-a/revoke", "{}"},
				{http.MethodPost, "/api/v1/agent-deployments/deployment-a/regenerate", `{"ttl_minutes":30}`},
			} {
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", sessionToken)
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Fatalf("%s %s: got %d, want 403", tc.method, tc.path, rr.Code)
				}
				rr = httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("anonymous %s %s: got %d, want 401", tc.method, tc.path, rr.Code)
				}
			}
			tokens, err := db.ListJoinTokens(context.Background(), "tenant-b")
			if err != nil || len(tokens) != 0 {
				t.Fatalf("unauthorized approval issued tokens: %+v, %v", tokens, err)
			}
		})
	}
}

func createEnrollmentDeploymentFixture(t *testing.T, db *storage.SQLiteStore) (*storage.JoinToken, *storage.AgentDeployment) {
	t.Helper()
	joinToken, _, err := db.CreateJoinToken(context.Background(), "tenant-a", 60, true)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &storage.AgentDeployment{JoinTokenID: joinToken.ID, TenantID: "tenant-a",
		Name: "Office Agent", Platform: "windows", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateAgentDeployment(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	return joinToken, deployment
}

func TestAgentDeploymentLifecycleEndpoints(t *testing.T) {
	for _, action := range []string{"delete", "revoke", "regenerate"} {
		t.Run(action, func(t *testing.T) {
			db, mux, sessionToken := setupEnrollmentSecurity(t, storage.RoleAdmin)
			oldToken, deployment := createEnrollmentDeploymentFixture(t, db)
			method, path, body := http.MethodDelete, "/api/v1/agent-deployments/"+deployment.ID, ""
			if action == "revoke" {
				method, path, body = http.MethodPost, path+"/revoke", `{}`
			} else if action == "regenerate" {
				method, path, body = http.MethodPost, path+"/regenerate", `{"ttl_minutes":30}`
			}
			request := httptest.NewRequest(method, path, strings.NewReader(body))
			request.Header.Set("Authorization", sessionToken)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("%s returned %d: %s", action, response.Code, response.Body.String())
			}
			tokens, err := db.ListJoinTokens(context.Background(), "tenant-a")
			oldRevoked := false
			for _, token := range tokens {
				if token.ID == oldToken.ID {
					oldRevoked = token.Revoked
				}
			}
			if err != nil || !oldRevoked {
				t.Fatalf("old invite was not revoked: tokens=%+v err=%v", tokens, err)
			}
			if action == "delete" {
				if _, err := db.GetAgentDeployment(context.Background(), deployment.ID); err != storage.ErrAgentDeploymentNotFound {
					t.Fatalf("removed deployment lookup err=%v", err)
				}
				return
			}
			if action == "revoke" {
				got, err := db.GetAgentDeployment(context.Background(), deployment.ID)
				if err != nil || got.Status != "revoked" {
					t.Fatalf("revoked deployment=%+v err=%v", got, err)
				}
				return
			}
			var result struct {
				DeploymentID string `json:"deployment_id"`
				Token        string `json:"token"`
				ExpiresAt    string `json:"expires_at"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.DeploymentID == "" || result.DeploymentID == deployment.ID || result.Token == "" || result.ExpiresAt == "" {
				t.Fatalf("regeneration response=%+v", result)
			}
			newToken, err := db.ValidateJoinToken(context.Background(), result.Token)
			if err != nil || newToken.ID == oldToken.ID || !newToken.OneTime {
				t.Fatalf("replacement token=%+v err=%v", newToken, err)
			}
		})
	}
}

func TestPendingApprovalRequiresExistingPendingRecord(t *testing.T) {
	for _, status := range []string{"missing", storage.PendingStatusPending, storage.PendingStatusApproved, storage.PendingStatusRejected} {
		t.Run(status, func(t *testing.T) {
			db, mux, sessionToken := setupEnrollmentSecurity(t, storage.RoleAdmin)
			ctx := context.Background()
			id := int64(999)
			if status != "missing" {
				var err error
				id, err = db.CreatePendingAgentRegistration(ctx, &storage.PendingAgentRegistration{AgentID: "new-agent", ExpiredTokenID: "expired-token", ExpiredTenantID: "tenant-a", Status: status})
				if err != nil {
					t.Fatal(err)
				}
			}
			review := func(action, tenant string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(map[string]string{"action": action, "tenant_id": tenant})
				req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/pending-registrations/%d", id), bytes.NewReader(body))
				req.Header.Set("Authorization", sessionToken)
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)
				return rr
			}
			want := http.StatusConflict
			if status == "missing" {
				want = http.StatusNotFound
			}
			if status == storage.PendingStatusPending {
				if rr := review("approve", "nonexistent-tenant"); rr.Code != http.StatusNotFound {
					t.Fatalf("missing tenant returned %d: %s", rr.Code, rr.Body.String())
				}
				want = http.StatusOK
			}
			rr := review("approve", "tenant-b")
			if rr.Code != want {
				t.Fatalf("got %d, want %d: %s", rr.Code, want, rr.Body.String())
			}
			if status == storage.PendingStatusPending {
				var response struct {
					JoinToken string `json:"join_token"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.JoinToken == "" {
					t.Fatalf("missing approval token: %s, %v", rr.Body.String(), err)
				}
				want = http.StatusConflict
				if rr := review("approve", "tenant-b"); rr.Code != want {
					t.Fatalf("repeat approval returned %d", rr.Code)
				}
			}
			if rr := review("reject", ""); rr.Code != want {
				t.Fatalf("review of missing/terminal record returned %d, want %d", rr.Code, want)
			}
			tokens, err := db.ListJoinTokens(ctx, "tenant-b")
			count := 0
			if status == storage.PendingStatusPending {
				count = 1
			}
			if err != nil || len(tokens) != count {
				t.Fatalf("got %d tokens, want %d: %v", len(tokens), count, err)
			}
		})
	}
}

func TestRegisterWithTokenRejectsExistingIdentity(t *testing.T) {
	for _, tenantID := range []string{"tenant-a", "tenant-b", ""} {
		t.Run("existing-"+tenantID, func(t *testing.T) {
			db, mux, _ := setupEnrollmentSecurity(t, storage.RoleAdmin)
			ctx := context.Background()
			agent := &storage.Agent{AgentID: "existing-agent", TenantID: tenantID, Token: "original-credential", Name: "Original", RegisteredAt: time.Now(), LastSeen: time.Now()}
			if err := db.RegisterAgent(ctx, agent); err != nil {
				t.Fatal(err)
			}
			_, token, err := db.CreateJoinToken(ctx, "tenant-b", 60, true)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"token": token, "agent_id": "existing-agent", "name": "Attacker"})
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", bytes.NewReader(body)))
			if rr.Code != http.StatusConflict || strings.Contains(rr.Body.String(), "agent_token") {
				t.Fatalf("collision returned %d: %s", rr.Code, rr.Body.String())
			}
			stored, err := db.GetAgent(ctx, agent.AgentID)
			if err != nil || stored.TenantID != tenantID || stored.Token != agent.Token || stored.Name != agent.Name {
				t.Fatalf("agent identity changed: %+v, %v", stored, err)
			}
			// The rejected collision must not consume a one-time token. A new
			// identity can still use it, without a user-session credential.
			body, _ = json.Marshal(map[string]string{"token": token, "agent_id": "new-agent"})
			rr = httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", bytes.NewReader(body)))
			if rr.Code != http.StatusOK {
				t.Fatalf("new enrollment returned %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestTrackedReplacementDeactivatesOldAgentAfterRegistration(t *testing.T) {
	db, mux, sessionToken := setupEnrollmentSecurity(t, storage.RoleAdmin)
	ctx := context.Background()
	oldAgent := &storage.Agent{
		AgentID: "replace-old", Name: "Old Agent", Hostname: "old-host", IP: "192.0.2.10",
		Platform: "windows", Version: "1.0.0", ProtocolVersion: "1", Token: "old-agent-secret",
		TenantID: "tenant-a", Status: "active",
	}
	if err := db.RegisterAgent(ctx, oldAgent); err != nil {
		t.Fatal(err)
	}

	requestBody, _ := json.Marshal(map[string]interface{}{
		"tenant_id": "tenant-a", "deployment_name": "New Office Agent",
		"platform": "windows", "replace_agent_id": oldAgent.AgentID,
		"ttl_minutes": 60, "one_time": true,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/join-token", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", sessionToken)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create tracked token returned %d: %s", response.Code, response.Body.String())
	}
	var tokenResponse struct {
		Token        string `json:"token"`
		DeploymentID string `json:"deployment_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatal(err)
	}
	if tokenResponse.Token == "" || tokenResponse.DeploymentID == "" {
		t.Fatalf("tracked token response missing token/deployment ID: %+v", tokenResponse)
	}
	if old, err := db.GetAgent(ctx, oldAgent.AgentID); err != nil || old.Status != "active" {
		t.Fatalf("old Agent changed before successor joined: agent=%+v err=%v", old, err)
	}

	events := make([]string, 0, 2)
	SetAgentEventSink(func(eventType string, _ map[string]interface{}) { events = append(events, eventType) })
	t.Cleanup(func() { SetAgentEventSink(nil) })
	registration, _ := json.Marshal(map[string]string{
		"token": tokenResponse.Token, "agent_id": "replace-new", "name": "installer name",
		"hostname": "new-host", "platform": "windows", "agent_version": "2.0.0",
	})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", bytes.NewReader(registration)))
	if response.Code != http.StatusOK {
		t.Fatalf("successor registration returned %d: %s", response.Code, response.Body.String())
	}

	registered, err := db.GetAgent(ctx, "replace-new")
	if err != nil || registered.Name != "New Office Agent" {
		t.Fatalf("successor record missing configured name: agent=%+v err=%v", registered, err)
	}
	retained, err := db.GetAgent(ctx, oldAgent.AgentID)
	if err != nil || retained.Status != "deactivated" || retained.Token != oldAgent.Token {
		t.Fatalf("old Agent should be retained and deactivated: agent=%+v err=%v", retained, err)
	}
	if _, err := db.GetAgentByToken(ctx, oldAgent.Token); err == nil {
		t.Fatal("old Agent credential still authenticates")
	}
	deployments, err := db.ListAgentDeployments(ctx, []string{"tenant-a"})
	if err != nil || len(deployments) != 1 || deployments[0].Status != "joined" || deployments[0].AgentID != "replace-new" {
		t.Fatalf("deployment not completed: deployments=%+v err=%v", deployments, err)
	}
	if len(events) != 2 || events[0] != "agent_deactivated" || events[1] != "agent_registered" {
		t.Fatalf("unexpected lifecycle event order: %v", events)
	}
}

func TestDeploymentEmailReturnsInstructionsURL(t *testing.T) {
	db, mux, sessionToken := setupEnrollmentSecurity(t, storage.RoleAdmin)
	_ = db
	originalSender := emailSender
	SetEmailSender(func(string, string, string, string) error { return nil })
	t.Cleanup(func() { SetEmailSender(originalSender) })
	requestBody, _ := json.Marshal(map[string]interface{}{
		"tenant_id": "tenant-a", "platform": "windows", "email": "operator@example.com", "ttl_minutes": 45,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/packages/send-email", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", sessionToken)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("send deployment email returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		InstructionsURL string `json:"instructions_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(payload.InstructionsURL, "/instructions") || strings.Contains(payload.InstructionsURL, "/install/") && !strings.HasSuffix(payload.InstructionsURL, "/instructions") {
		t.Fatalf("email response did not return the browser instructions URL: %q", payload.InstructionsURL)
	}
}

type enrollmentErrorStore struct {
	storage.Store
	lookupErr   error
	approvalErr error
}

func (s enrollmentErrorStore) GetAgent(context.Context, string) (*storage.Agent, error) {
	return nil, s.lookupErr
}

func (s enrollmentErrorStore) ApprovePendingRegistrationWithToken(context.Context, int64, string, string) (*storage.JoinToken, string, error) {
	return nil, "", s.approvalErr
}

func TestEnrollmentStorageFailuresFailClosed(t *testing.T) {
	db, mux, sessionToken := setupEnrollmentSecurity(t, storage.RoleAdmin)
	ctx := context.Background()
	id, err := db.CreatePendingAgentRegistration(ctx, &storage.PendingAgentRegistration{AgentID: "new-agent", ExpiredTokenID: "expired", ExpiredTenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	dbStore = enrollmentErrorStore{Store: db, lookupErr: errors.New("database unavailable"), approvalErr: sql.ErrNoRows}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", strings.NewReader(`{"token":"token","agent_id":"new-agent"}`)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failure returned %d", rr.Code)
	}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/pending-registrations/%d", id), strings.NewReader(`{"action":"approve","tenant_id":"tenant-a"}`))
	req.Header.Set("Authorization", sessionToken)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("lost approval race returned %d", rr.Code)
	}
	tokens, err := db.ListJoinTokens(ctx, "tenant-a")
	if err != nil || len(tokens) != 0 {
		t.Fatalf("failed approval minted tokens: %+v, %v", tokens, err)
	}
}

func enableTenancyForTest(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
}

func allowAllAuthorizer(t *testing.T) {
	SetAuthorizer(func(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
		return nil
	})
	t.Cleanup(func() { SetAuthorizer(nil) })
}

func setupTenancyServer(t *testing.T) *httptest.Server {
	enableTenancyForTest(t)
	store = NewInMemoryStore()
	AuthMiddleware = func(next http.HandlerFunc) http.HandlerFunc {
		return next
	}
	mux := http.NewServeMux()
	RegisterRoutesOnMux(mux, nil)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		AuthMiddleware = nil
		SetAuthorizer(nil)
	})
	return ts
}

func TestHandleTenantsAuthorizationFailures(t *testing.T) {
	enableTenancyForTest(t)
	SetAuthorizer(func(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
		return authz.ErrForbidden
	})
	t.Cleanup(func() { SetAuthorizer(nil) })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	rw := httptest.NewRecorder()
	handleTenants(rw, req)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 got %d", rw.Code)
	}

	SetAuthorizer(func(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
		return authz.ErrUnauthorized
	})
	rw = httptest.NewRecorder()
	handleTenants(rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 got %d", rw.Code)
	}
}

func TestHandleTenantUpdateScopesAuthorization(t *testing.T) {
	enableTenancyForTest(t)
	_, _ = store.CreateTenant(Tenant{ID: "scope-tenant", Name: "Scope"})
	called := false
	SetAuthorizer(func(_ *http.Request, action authz.Action, resource authz.ResourceRef) error {
		if action != authz.ActionTenantsWrite {
			t.Fatalf("unexpected action: %s", action)
		}
		if len(resource.TenantIDs) != 1 || resource.TenantIDs[0] != "scope-tenant" {
			t.Fatalf("unexpected tenant scope: %+v", resource.TenantIDs)
		}
		called = true
		return nil
	})
	t.Cleanup(func() { SetAuthorizer(nil) })
	payload := map[string]string{"name": "Scoped"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/scope-tenant", bytes.NewReader(body))
	rw := httptest.NewRecorder()
	handleTenantByID(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rw.Code)
	}
	if !called {
		t.Fatalf("authorizer not invoked")
	}
}

func TestHandleTenantsCreateAndList(t *testing.T) {
	enableTenancyForTest(t)
	allowAllAuthorizer(t)
	// POST create
	in := map[string]string{
		"id":            "httpt",
		"name":          "HTTP Tenant",
		"contact_email": "owner@example.com",
		"business_unit": "PS",
	}
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(b))
	rw := httptest.NewRecorder()
	handleTenants(rw, req)
	res := rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var out Tenant
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if out.ID != "httpt" {
		t.Fatalf("unexpected tenant id: %s", out.ID)
	}

	// GET list
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	rw = httptest.NewRecorder()
	handleTenants(rw, req)
	res = rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var list []Tenant
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode list failed: %v", err)
	}
	found := false
	for _, tenant := range list {
		if tenant.ID == "httpt" {
			if tenant.ContactEmail != "owner@example.com" {
				t.Fatalf("contact email not persisted")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created tenant not found in list")
	}
}

func TestHTTPRoutesEnforceAuthorization(t *testing.T) {
	ts := setupTenancyServer(t)
	SetAuthorizer(func(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
		return authz.ErrForbidden
	})
	resp, err := http.Get(ts.URL + "/api/v1/tenants")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 got %d", resp.StatusCode)
	}

	SetAuthorizer(func(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
		return nil
	})
	resp, err = http.Get(ts.URL + "/api/v1/tenants")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
}

func TestTenantSubresourceDispatch(t *testing.T) {
	enableTenancyForTest(t)
	RegisterTenantSubresource("settings", func(w http.ResponseWriter, r *http.Request, tenantID string, rest string) {
		if tenantID != "sub-tenant" {
			t.Fatalf("unexpected tenant id: %s", tenantID)
		}
		if rest != "" {
			t.Fatalf("unexpected remainder path: %s", rest)
		}
		w.WriteHeader(http.StatusTeapot)
	})
	t.Cleanup(func() { RegisterTenantSubresource("settings", nil) })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/sub-tenant/settings", nil)
	rw := httptest.NewRecorder()
	handleTenantRoute(rw, req)
	if rw.Code != http.StatusTeapot {
		t.Fatalf("expected 418 got %d", rw.Code)
	}
}

func TestHTTPRoutesTenantScopedWrite(t *testing.T) {
	ts := setupTenancyServer(t)
	_, _ = store.CreateTenant(Tenant{ID: "route-scope", Name: "Route"})
	called := false
	SetAuthorizer(func(_ *http.Request, action authz.Action, resource authz.ResourceRef) error {
		if action != authz.ActionTenantsWrite {
			t.Fatalf("unexpected action: %s", action)
		}
		if len(resource.TenantIDs) != 1 || resource.TenantIDs[0] != "route-scope" {
			t.Fatalf("unexpected resource scope: %+v", resource.TenantIDs)
		}
		called = true
		return nil
	})
	payload := map[string]string{"name": "Updated"}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/tenants/route-scope", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	if !called {
		t.Fatalf("authorizer not invoked")
	}
}

func TestHandleTenantUpdate(t *testing.T) {
	enableTenancyForTest(t)
	allowAllAuthorizer(t)
	_, _ = store.CreateTenant(Tenant{ID: "update1", Name: "Original", ContactPhone: "123"})
	payload := map[string]string{
		"name":          "Updated Tenant",
		"contact_phone": "+18005551234",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/update1", bytes.NewReader(body))
	rw := httptest.NewRecorder()
	handleTenantByID(rw, req)
	res := rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var out Tenant
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if out.ContactPhone != "+18005551234" {
		t.Fatalf("contact phone not updated")
	}
}

func TestCreateJoinTokenAndRegister(t *testing.T) {
	enableTenancyForTest(t)
	allowAllAuthorizer(t)
	// Ensure tenant exists
	_, _ = store.CreateTenant(Tenant{ID: "regt", Name: "Reg Tenant"})

	// Create join token via handler
	in := map[string]interface{}{"tenant_id": "regt", "ttl_minutes": 5, "one_time": false}
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/join-token", bytes.NewReader(b))
	rw := httptest.NewRecorder()
	handleCreateJoinToken(rw, req)
	res := rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var tokenResp map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("decode token resp failed: %v", err)
	}
	token, ok := tokenResp["token"].(string)
	if !ok || token == "" {
		t.Fatalf("token missing in response")
	}

	// Now register with token
	reg := map[string]string{"token": token, "agent_id": "agent-x", "name": "agent x"}
	rb, _ := json.Marshal(reg)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", bytes.NewReader(rb))
	rw = httptest.NewRecorder()
	handleRegisterWithToken(rw, req)
	res = rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var regResp map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&regResp); err != nil {
		t.Fatalf("decode register resp failed: %v", err)
	}
	if regResp["tenant_id"] != "regt" {
		t.Fatalf("unexpected tenant_id in register response: %v", regResp["tenant_id"])
	}
	if regResp["agent_token"] == nil {
		t.Fatalf("agent_token missing in response")
	}
}

func TestRegisterWithTokenEmitsEvent(t *testing.T) {
	enableTenancyForTest(t)
	defer SetAgentEventSink(nil)

	_, _ = store.CreateTenant(Tenant{ID: "event", Name: "Event Tenant"})
	jt, err := store.CreateJoinToken("event", 5, false)
	if err != nil {
		t.Fatalf("CreateJoinToken failed: %v", err)
	}

	events := make(chan map[string]interface{}, 1)
	SetAgentEventSink(func(eventType string, data map[string]interface{}) {
		if eventType == "agent_registered" {
			events <- data
		}
	})

	reg := map[string]interface{}{
		"token":            jt.Token,
		"agent_id":         "agent-event",
		"name":             "Event Agent",
		"hostname":         "evt-host",
		"ip":               "10.0.0.55",
		"platform":         "linux",
		"agent_version":    "1.2.3",
		"protocol_version": "1",
	}
	body, _ := json.Marshal(reg)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register-with-token", bytes.NewReader(body))
	rw := httptest.NewRecorder()
	handleRegisterWithToken(rw, req)
	res := rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}

	select {
	case payload := <-events:
		if payload["agent_id"] != "agent-event" {
			t.Fatalf("unexpected agent_id payload: %v", payload["agent_id"])
		}
		if payload["connection_type"] != "none" {
			t.Fatalf("unexpected connection_type: %v", payload["connection_type"])
		}
	default:
		t.Fatalf("expected agent_registered event")
	}
}

func TestListAndRevokeJoinTokens(t *testing.T) {
	enableTenancyForTest(t)
	allowAllAuthorizer(t)
	// Ensure tenant exists and create token
	_, _ = store.CreateTenant(Tenant{ID: "admint", Name: "Admin Tenant"})
	jt, err := store.CreateJoinToken("admint", 5, false)
	if err != nil {
		t.Fatalf("CreateJoinToken failed: %v", err)
	}
	if jt == (JoinToken{}) {
		t.Fatalf("empty join token returned")
	}

	// List via handler (in-memory path)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/join-tokens?tenant_id=admint", nil)
	rw := httptest.NewRecorder()
	handleListJoinTokens(rw, req)
	res := rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	var list []JoinToken
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode list failed: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected non-empty list")
	}

	// Revoke via handler (attempt by id)
	// in-memory store uses token value as key; revoke by token
	b, _ := json.Marshal(map[string]string{"id": jt.Token})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/join-token/revoke", bytes.NewReader(b))
	rw = httptest.NewRecorder()
	handleRevokeJoinToken(rw, req)
	res = rw.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}

	// no raw for in-memory path
}

func TestHandleAgentDownloadLatestProxy(t *testing.T) {
	enableTenancyForTest(t)
	origVersion := serverVersion
	serverVersion = "1.2.3"
	t.Cleanup(func() { serverVersion = origVersion })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/agent-v1.2.3/printmaster-agent-v1.2.3-windows-amd64.exe"
		if r.URL.Path != expectedPath {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("bin"))
	}))
	t.Cleanup(func() { upstream.Close() })
	origBase := releaseAssetBaseURL
	releaseAssetBaseURL = upstream.URL
	t.Cleanup(func() { releaseAssetBaseURL = origBase })
	origClient := releaseDownloadClient
	releaseDownloadClient = upstream.Client()
	t.Cleanup(func() { releaseDownloadClient = origClient })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/download/latest?platform=windows&arch=amd64&proxy=1", nil)
	rw := httptest.NewRecorder()
	handleAgentDownloadLatest(rw, req)
	res := rw.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if string(body) != "bin" {
		t.Fatalf("unexpected body: %q", body)
	}
	if res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("unexpected content type: %s", res.Header.Get("Content-Type"))
	}
}

func TestHandleAgentDownloadLatestRedirect(t *testing.T) {
	enableTenancyForTest(t)
	origVersion := serverVersion
	serverVersion = "2.0.0"
	t.Cleanup(func() { serverVersion = origVersion })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/download/latest?platform=windows&arch=amd64", nil)
	rw := httptest.NewRecorder()
	handleAgentDownloadLatest(rw, req)
	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302 got %d", rw.Code)
	}
	loc := rw.Header().Get("Location")
	if !strings.Contains(loc, "/agent-v2.0.0/") {
		t.Fatalf("unexpected redirect location: %s", loc)
	}
	if !strings.Contains(loc, "printmaster-agent-v2.0.0-windows-amd64.exe") {
		t.Fatalf("missing expected asset in redirect: %s", loc)
	}
}

func TestHandleAgentDownloadLatestMSI(t *testing.T) {
	enableTenancyForTest(t)
	origVersion := serverVersion
	serverVersion = "2.0.0"
	t.Cleanup(func() { serverVersion = origVersion })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/download/latest?platform=windows&arch=amd64&format=msi", nil)
	rw := httptest.NewRecorder()
	handleAgentDownloadLatest(rw, req)
	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302 got %d", rw.Code)
	}
	loc := rw.Header().Get("Location")
	if !strings.Contains(loc, "/agent-v2.0.0/") {
		t.Fatalf("unexpected redirect location: %s", loc)
	}
	if !strings.Contains(loc, "printmaster-agent-v2.0.0-windows-amd64.msi") {
		t.Fatalf("expected MSI in redirect, got: %s", loc)
	}
}
