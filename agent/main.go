// Printer/Copier Fleet Management Agent in Go
// Cross-platform agent for SNMP printer discovery and reporting
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"printmaster/agent/agent"
	"printmaster/agent/autoupdate"
	"printmaster/agent/featureflags"
	"printmaster/agent/proxy"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
	"printmaster/common/config"
	"printmaster/common/logger"
	"printmaster/common/report"
	pmsettings "printmaster/common/settings"
	commonutil "printmaster/common/util"
	sharedweb "printmaster/common/web"
	wscommon "printmaster/common/ws"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/kardianos/service"
)

// Version information (set at build time via -ldflags)
var (
	Version   = "dev"     // Semantic version (e.g., "1.0.0")
	BuildTime = "unknown" // Build timestamp
	GitCommit = "unknown" // Git commit hash
	BuildType = "dev"     // "dev" or "release"
)

//go:embed web
var webFS embed.FS

// loggingResponseWriter captures status code and byte count for diagnostics
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := lrw.ResponseWriter.Write(b)
	lrw.bytes += n
	return n, err
}

// Flush proxies Flush to the underlying writer when supported
func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom ensures io.Copy can use an optimized path while still counting bytes
func (lrw *loggingResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	// Use io.Copy which will call lrw.Write, preserving the byte counter
	return io.Copy(lrw, r)
}

// basicAuth returns base64 of user:pass per RFC7617
func basicAuth(userpass string) string {
	return base64.StdEncoding.EncodeToString([]byte(userpass))
}

// rewriteExistingBaseTag finds a device-supplied <base href="..."> tag and
// rewrites it to stay under proxyPrefix. Some vendor UIs (e.g. Kyocera
// Command Center RX) ship their own <base href="/"> so relative asset
// requests always resolve from the device's web root; left untouched, those
// requests escape the proxy prefix and hit the server's own top-level routes
// (404s / X-Frame-Options-blocked root page) instead of the printer. Returns
// the rewritten content and whether a tag was found and modified.
func rewriteExistingBaseTag(content, proxyPrefix, targetHost string) (string, bool) {
	contentLower := strings.ToLower(content)
	baseIdx := strings.Index(contentLower, "<base")
	if baseIdx == -1 {
		return content, false
	}
	tagEnd := strings.Index(content[baseIdx:], ">")
	if tagEnd == -1 {
		return content, false
	}
	tagEnd += baseIdx + 1 // position just after '>'
	tag := content[baseIdx:tagEnd]
	tagLower := strings.ToLower(tag)

	quote := byte('"')
	hrefIdx := strings.Index(tagLower, `href="`)
	if hrefIdx == -1 {
		hrefIdx = strings.Index(tagLower, `href='`)
		quote = '\''
	}
	if hrefIdx == -1 {
		return content, false
	}
	valueStart := hrefIdx + len(`href="`)
	valueEnd := strings.IndexByte(tag[valueStart:], quote)
	if valueEnd == -1 {
		return content, false
	}
	valueEnd += valueStart
	href := tag[valueStart:valueEnd]

	// Strip scheme+host if the href is a full same-host URL
	hrefPath := href
	if u, err := url.Parse(href); err == nil && u.Host != "" {
		if !strings.EqualFold(u.Host, targetHost) {
			return content, false // different host, leave alone
		}
		hrefPath = u.Path
		if hrefPath == "" {
			hrefPath = "/"
		}
	}

	if !strings.HasPrefix(hrefPath, "/") || strings.HasPrefix(hrefPath, proxyPrefix) {
		return content, false // relative or already rewritten
	}

	newHref := proxyPrefix + hrefPath
	newTag := tag[:valueStart] + newHref + tag[valueEnd:]
	return content[:baseIdx] + newTag + content[tagEnd:], true
}

func isKyoceraModelScript(targetPath string) bool {
	path := strings.ToLower(targetPath)
	return strings.HasPrefix(path, "/js/jssrc/model/") && strings.HasSuffix(path, ".model.htm")
}

// Global session cache for form-based logins
var proxySessionCache = proxy.NewSessionCache()

var agentSessions = newAgentSessionManager()
var agentAuth *agentAuthManager

// globalLocalPrinterStore holds reference to the local printer store for runtime settings changes
var globalLocalPrinterStore storage.LocalPrinterStore

// AgentPrincipal represents an authenticated UI context (placeholder for future auth)
type AgentPrincipal struct {
	Username  string   `json:"username"`
	Role      string   `json:"role"`
	Source    string   `json:"source"`
	TenantIDs []string `json:"tenant_ids,omitempty"`
}

type contextKey string

const (
	isHTTPSContextKey        contextKey = "isHTTPS"
	agentPrincipalContextKey contextKey = "agentPrincipal"
)

const (
	agentSessionCookieName = "pm_agent_session"
	defaultAgentSessionTTL = 24 * time.Hour
	serverAuthTimeout      = 15 * time.Second
)

type agentSession struct {
	ID          string
	Principal   *AgentPrincipal
	ServerToken string
	ExpiresAt   time.Time
}

type agentSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*agentSession
}

func newAgentSessionManager() *agentSessionManager {
	return &agentSessionManager{sessions: make(map[string]*agentSession)}
}

func (m *agentSessionManager) Create(principal *AgentPrincipal, serverToken string, expiresAt time.Time) string {
	if principal == nil {
		return ""
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(24 * time.Hour)
	}
	token := randomSessionToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	m.sessions[token] = &agentSession{
		ID:          token,
		Principal:   principal,
		ServerToken: serverToken,
		ExpiresAt:   expiresAt,
	}
	return token
}

func (m *agentSessionManager) Get(token string) (*agentSession, bool) {
	if token == "" {
		return nil, false
	}
	m.mu.RLock()
	sess, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		m.Delete(token)
		return nil, false
	}
	return sess, true
}

func (m *agentSessionManager) Delete(token string) {
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *agentSessionManager) cleanupLocked() {
	now := time.Now()
	for key, sess := range m.sessions {
		if now.After(sess.ExpiresAt) {
			delete(m.sessions, key)
		}
	}
}

func randomSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

var (
	errInvalidCredentials = errors.New("invalid credentials")
)

type agentAuthManager struct {
	config           *AgentConfig // Read the persisted machine identity, including post-start enrollment.
	mode             string
	allowLocalAdmin  bool
	serverURL        string
	serverCAPath     string
	serverSkipVerify bool
	sessions         *agentSessionManager
	publicExact      map[string]struct{}
	publicPrefixes   []string
}

type agentAuthOptions struct {
	Mode            string `json:"mode"`
	AllowLocalAdmin bool   `json:"allow_local_admin"`
	ServerURL       string `json:"server_url,omitempty"`
	ServerAuthURL   string `json:"server_auth_url,omitempty"` // URL to redirect for server auth
	LoginSupported  bool   `json:"login_supported"`
}

func newAgentAuthManager(cfg *AgentConfig, sessions *agentSessionManager) *agentAuthManager {
	mode := "local"
	allowLocal := true
	serverURL := ""
	serverCA := ""
	serverSkip := false
	if cfg != nil {
		if cfg.Web.Auth.Mode != "" {
			mode = strings.ToLower(strings.TrimSpace(cfg.Web.Auth.Mode))
		}
		allowLocal = cfg.Web.Auth.AllowLocalAdmin
		serverURL = strings.TrimSpace(cfg.Server.URL)
		serverCA = strings.TrimSpace(cfg.Server.CAPath)
		serverSkip = cfg.Server.InsecureSkipVerify

		// Auto-enable server mode if server URL is configured and mode not explicitly set
		if serverURL != "" && cfg.Web.Auth.Mode == "" {
			mode = "server"
		}
	}
	return &agentAuthManager{
		config:           cfg,
		mode:             mode,
		allowLocalAdmin:  allowLocal,
		serverURL:        serverURL,
		serverCAPath:     serverCA,
		serverSkipVerify: serverSkip,
		sessions:         sessions,
		publicExact: map[string]struct{}{
			"/login":                {},
			"/favicon.ico":          {},
			"/health":               {},
			"/api/version":          {},
			"/api/v1/auth/options":  {},
			"/api/v1/auth/login":    {},
			"/api/v1/auth/logout":   {},
			"/api/v1/auth/me":       {},
			"/api/v1/auth/callback": {}, // Server auth callback
		},
		publicPrefixes: []string{
			"/static/",
			"/api/usb-printers/", // USB printer metrics (public for testing)
		},
	}
}

func (a *agentAuthManager) optionsPayload() agentAuthOptions {
	if a == nil {
		return agentAuthOptions{Mode: "disabled", AllowLocalAdmin: true, LoginSupported: false}
	}
	serverURL := strings.TrimSpace(a.serverURL)
	hasServer := serverURL != ""
	loginSupported := hasServer && a.mode == "server"
	opts := agentAuthOptions{
		Mode:            a.mode,
		AllowLocalAdmin: a.allowLocalAdmin,
		LoginSupported:  loginSupported,
	}
	if hasServer {
		opts.ServerURL = serverURL
		// Always provide the server auth URL when server is configured
		// This enables redirect-based auth even when direct login isn't supported
		opts.ServerAuthURL = strings.TrimRight(serverURL, "/") + "/login"
	}
	return opts
}

func (a *agentAuthManager) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler := next
		if handler == nil {
			handler = http.DefaultServeMux
		}
		if a == nil || a.shouldBypass(r) {
			handler.ServeHTTP(w, r)
			return
		}
		principal, ok := a.authenticate(r)
		if !ok {
			a.respondUnauthorized(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), agentPrincipalContextKey, principal)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *agentAuthManager) shouldBypass(r *http.Request) bool {
	if a == nil || a.mode == "disabled" {
		return true
	}
	// Bypass auth for server-proxied requests (header is set by ws_client.go handleProxyRequest)
	// This is safe because only the internal proxy code path can set this header
	if r.Header.Get("X-PrintMaster-Proxy") == "server" {
		return true
	}
	path := r.URL.Path
	if _, ok := a.publicExact[path]; ok {
		return true
	}
	for _, prefix := range a.publicPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (a *agentAuthManager) PrincipalForRequest(r *http.Request) (*AgentPrincipal, bool) {
	return a.authenticate(r)
}

func (a *agentAuthManager) authenticate(r *http.Request) (*AgentPrincipal, bool) {
	if a == nil || a.mode == "disabled" {
		return &AgentPrincipal{Username: "system", Role: "admin", Source: "disabled"}, true
	}
	if sess := a.sessionFromRequest(r); sess != nil {
		return sess.Principal, true
	}
	if a.allowLocalAdmin && requestIsLoopback(r) {
		return &AgentPrincipal{Username: "local-admin", Role: "admin", Source: "loopback"}, true
	}
	switch a.mode {
	case "local":
		return nil, false
	case "server":
		return nil, false
	default:
		return nil, false
	}
}

func (a *agentAuthManager) respondUnauthorized(w http.ResponseWriter, r *http.Request) {
	if a != nil && a.mode == "server" && strings.TrimSpace(a.serverURL) != "" && acceptsHTML(r) {
		http.Redirect(w, r, a.serverLoginURL(r), http.StatusFound)
		return
	}
	if acceptsHTML(r) {
		redirectTo := "/login"
		if r.URL != nil && r.URL.Path != "/login" {
			redirectTo = redirectTo + "?return_to=" + url.QueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (a *agentAuthManager) sessionFromRequest(r *http.Request) *agentSession {
	if a == nil || a.sessions == nil {
		return nil
	}
	cookie, err := r.Cookie(agentSessionCookieName)
	if err != nil {
		return nil
	}
	sess, ok := a.sessions.Get(cookie.Value)
	if !ok {
		return nil
	}
	return sess
}

func requestIsLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	checkHost := func(value string) bool {
		if value == "" {
			return false
		}
		host := value
		if strings.Contains(host, ":") {
			if parsedHost, _, err := net.SplitHostPort(value); err == nil {
				host = parsedHost
			}
		}
		ip := net.ParseIP(strings.TrimSpace(host))
		return ip != nil && ip.IsLoopback()
	}
	if checkHost(r.RemoteAddr) {
		return true
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 && checkHost(parts[0]) {
			return true
		}
	}
	return false
}

func requestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if v := r.Context().Value(isHTTPSContextKey); v != nil {
		if flag, ok := v.(bool); ok && flag {
			return true
		}
	}
	proto := strings.TrimSpace(strings.ToLower(r.Header.Get("X-Forwarded-Proto")))
	return proto == "https"
}

// isSafeReturnPath validates that a "return_to" redirect target is a local,
// same-origin path (prevents open-redirect attacks). It rejects absolute
// URLs, protocol-relative URLs (//host or /\host), and control characters
// (tab/CR/LF/etc.) that some browsers strip or normalize before navigating,
// which could otherwise be used to smuggle "//host" past a naive prefix check
// (e.g. "/\t/evil.com" is stripped to "//evil.com" by some browsers).
func isSafeReturnPath(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return false
	}
	if strings.Contains(p, "://") {
		return false
	}
	for _, r := range p {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}

// handleHealth responds with a simple JSON payload indicating the agent is alive.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
	})
}

type agentHealthAttempt struct {
	url      string
	insecure bool
}

// runAgentHealthCheck probes the local /health endpoint using configured ports.
// Returns nil when healthy; otherwise an error summarizing failures.
func runAgentHealthCheck(configFlag string) error {
	cfg := DefaultAgentConfig()

	if resolved := config.ResolveConfigPath("AGENT", configFlag); resolved != "" {
		if _, err := os.Stat(resolved); err == nil {
			if loaded, loadErr := LoadAgentConfig(resolved); loadErr == nil {
				cfg = loaded
			}
		}
	}

	attempts := make([]agentHealthAttempt, 0, 2)
	if cfg.Web.HTTPPort > 0 {
		attempts = append(attempts, agentHealthAttempt{url: fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Web.HTTPPort)})
	}
	if cfg.Web.HTTPSPort > 0 {
		attempts = append(attempts, agentHealthAttempt{url: fmt.Sprintf("https://127.0.0.1:%d/health", cfg.Web.HTTPSPort), insecure: true})
	}

	if len(attempts) == 0 {
		attempts = append(attempts, agentHealthAttempt{url: "http://127.0.0.1:8080/health"})
	}

	var errs []string
	for _, attempt := range attempts {
		if err := probeAgentHealth(attempt.url, attempt.insecure); err != nil {
			errMsg := fmt.Sprintf("%s: %v", attempt.url, err)
			errs = append(errs, errMsg)
			continue
		}
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("no health endpoints to probe")
	}

	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func probeAgentHealth(endpoint string, insecure bool) error {
	client := &http.Client{Timeout: 5 * time.Second}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if strings.ToLower(strings.TrimSpace(payload.Status)) != "healthy" {
		return fmt.Errorf("status=%s", payload.Status)
	}

	return nil
}

func acceptsHTML(r *http.Request) bool {
	if r == nil {
		return false
	}
	header := r.Header.Get("Accept")
	if strings.Contains(header, "text/html") {
		return true
	}
	return r.URL != nil && r.URL.Path == "/"
}

func (a *agentAuthManager) issueSessionCookie(w http.ResponseWriter, r *http.Request, principal *AgentPrincipal, serverToken string, expiresAt time.Time) (string, error) {
	if a == nil || a.sessions == nil || principal == nil {
		return "", errors.New("authentication disabled")
	}
	if expiresAt.IsZero() || expiresAt.Before(time.Now()) {
		expiresAt = time.Now().Add(defaultAgentSessionTTL)
	}
	sessionID := a.sessions.Create(principal, serverToken, expiresAt)
	cookie := &http.Cookie{
		Name:     agentSessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	}
	if expiresAt.After(time.Now()) {
		cookie.MaxAge = int(time.Until(expiresAt).Seconds())
	}
	http.SetCookie(w, cookie)
	return sessionID, nil
}

func (a *agentAuthManager) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     agentSessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *agentAuthManager) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// When accessed through server proxy, the server injects principal info in headers
	// Return that info so the proxied UI can display the correct role/permissions
	if r.Header.Get("X-PrintMaster-Proxy") == "server" {
		user := r.Header.Get("X-PrintMaster-User")
		role := r.Header.Get("X-PrintMaster-Role")
		if user != "" && role != "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(&AgentPrincipal{
				Username: user,
				Role:     role,
				Source:   "server-proxy",
			})
			return
		}
	}

	if principal, ok := a.authenticate(r); ok && principal != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(principal)
		return
	}
	http.Error(w, "unauthenticated", http.StatusUnauthorized)
}

func (a *agentAuthManager) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a == nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	if username == "" || password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}
	switch a.mode {
	case "server":
		principal, serverToken, expiresAt, err := a.serverLogin(r.Context(), username, password)
		if err != nil {
			if errors.Is(err, errInvalidCredentials) {
				http.Error(w, "invalid credentials", http.StatusUnauthorized)
				return
			}
			if appLogger != nil {
				appLogger.Warn("Server login via agent failed", "error", err.Error())
			}
			http.Error(w, "login failed", http.StatusBadGateway)
			return
		}
		if _, err := a.issueSessionCookie(w, r, principal, serverToken, expiresAt); err != nil {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    principal,
		})
		return
	case "disabled":
		http.Error(w, "authentication disabled", http.StatusForbidden)
		return
	default:
		http.Error(w, "login mode not supported", http.StatusNotImplemented)
		return
	}
}

func (a *agentAuthManager) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}
	var serverToken string
	cookie, err := r.Cookie(agentSessionCookieName)
	if err == nil && cookie.Value != "" {
		if sess, ok := a.sessions.Get(cookie.Value); ok {
			serverToken = sess.ServerToken
		}
		a.sessions.Delete(cookie.Value)
	}
	a.clearSessionCookie(w, r)
	if serverToken != "" && a.mode == "server" {
		if err := a.serverLogout(r.Context(), serverToken); err != nil && appLogger != nil {
			appLogger.Warn("Failed to log out from server", "error", err.Error())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handleAuthCallback handles GET /api/v1/auth/callback
// This is called when the server redirects back to the agent after authentication.
// The server includes a short-lived callback token that we validate to create a local session.
func (a *agentAuthManager) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}

	// Get the callback token from query params
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	returnTo := strings.TrimSpace(r.URL.Query().Get("return_to"))
	if returnTo == "" || !isSafeReturnPath(returnTo) {
		returnTo = "/"
	}

	if token == "" {
		if appLogger != nil {
			appLogger.Warn("Auth callback missing token")
		}
		// Redirect to login with error
		http.Redirect(w, r, "/login?error=missing_token&return_to="+url.QueryEscape(returnTo), http.StatusFound)
		return
	}

	// Validate the token with the server
	principal, serverToken, expiresAt, err := a.validateServerCallbackToken(r.Context(), token)
	if err != nil {
		if appLogger != nil {
			appLogger.Warn("Auth callback token validation failed", "error", err.Error())
		}
		http.Redirect(w, r, "/login?error=invalid_token&return_to="+url.QueryEscape(returnTo), http.StatusFound)
		return
	}

	// Create a local session
	if _, err := a.issueSessionCookie(w, r, principal, serverToken, expiresAt); err != nil {
		if appLogger != nil {
			appLogger.Error("Failed to create session after callback", "error", err.Error())
		}
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	if appLogger != nil {
		appLogger.Info("Auth callback successful", "username", principal.Username, "return_to", returnTo)
	}

	// Redirect to the original destination
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// validateServerCallbackToken validates a callback token with the server and returns user info.
func (a *agentAuthManager) validateServerCallbackToken(ctx context.Context, token string) (*AgentPrincipal, string, time.Time, error) {
	if a == nil || a.mode != "server" || strings.TrimSpace(a.serverURL) == "" {
		return nil, "", time.Time{}, fmt.Errorf("server validation unavailable")
	}
	agentID, err := a.serverAuthAgentID()
	if err != nil || strings.TrimSpace(token) == "" {
		return nil, "", time.Time{}, fmt.Errorf("callback requires machine identity and token")
	}

	if appLogger != nil {
		appLogger.Debug("Validating callback token with server", "server_url", a.serverURL)
	}

	client, err := a.newServerHTTPClient()
	if err != nil {
		if appLogger != nil {
			appLogger.Debug("Failed to create HTTP client for token validation", "error", err.Error())
		}
		return nil, "", time.Time{}, err
	}

	payload := map[string]string{"token": token, "agent_id": agentID}
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		return nil, "", time.Time{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverAPIURL("/api/v1/auth/agent-callback/validate"), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("PrintMaster-Agent/%s", Version))

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", time.Time{}, err
	}

	if resp.StatusCode != http.StatusOK {
		if appLogger != nil {
			appLogger.Debug("Server returned non-OK status for token validation", "status", resp.StatusCode)
		}
		return nil, "", time.Time{}, fmt.Errorf("token validation failed: status %d", resp.StatusCode)
	}

	var result struct {
		Authorized    bool     `json:"authorized"`
		AgentID       string   `json:"agent_id"`
		AgentTenantID string   `json:"agent_tenant_id"`
		Valid         bool     `json:"valid"`
		UserID        int64    `json:"user_id"`
		Username      string   `json:"username"`
		Role          string   `json:"role"`
		TenantID      string   `json:"tenant_id"`
		TenantIDs     []string `json:"tenant_ids"`
		ExpiresAt     string   `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", time.Time{}, err
	}

	if !result.Valid || !result.Authorized || result.AgentID != agentID || !serverPrincipalAllowsAgent(result.Username, result.Role, result.TenantID, result.TenantIDs, result.AgentTenantID) {
		if appLogger != nil {
			appLogger.Debug("Server reported token as invalid")
		}
		return nil, "", time.Time{}, fmt.Errorf("token invalid")
	}

	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		return nil, "", time.Time{}, fmt.Errorf("callback expiration invalid")
	}

	principal := &AgentPrincipal{
		Username:  result.Username,
		Role:      result.Role,
		Source:    "server-callback",
		TenantIDs: result.TenantIDs,
	}
	if len(principal.TenantIDs) == 0 && result.TenantID != "" {
		principal.TenantIDs = []string{result.TenantID}
	}

	if appLogger != nil {
		appLogger.Debug("Token validation successful", "username", result.Username, "role", result.Role, "expires_at", expiresAt.Format(time.RFC3339))
	}

	// Return the token itself as the "server token" for logout purposes
	// In a full implementation, you might want to create a proper server session
	return principal, token, expiresAt, nil
}

func (a *agentAuthManager) serverLogin(ctx context.Context, username, password string) (*AgentPrincipal, string, time.Time, error) {
	if a == nil || strings.TrimSpace(a.serverURL) == "" {
		return nil, "", time.Time{}, fmt.Errorf("server login unavailable")
	}
	if _, err := a.serverAuthAgentID(); err != nil {
		return nil, "", time.Time{}, err
	}
	client, err := a.newServerHTTPClient()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	payload := map[string]string{"username": username, "password": password}
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		return nil, "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverAPIURL("/api/v1/auth/login"), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("PrintMaster-Agent/%s", Version))
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", time.Time{}, errInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", time.Time{}, fmt.Errorf("server login failed: status %d", resp.StatusCode)
	}
	var loginResp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &loginResp); err != nil {
		return nil, "", time.Time{}, err
	}
	if loginResp.Token == "" {
		return nil, "", time.Time{}, fmt.Errorf("server login failed: missing token")
	}
	expiresAt := time.Now().Add(defaultAgentSessionTTL)
	if loginResp.ExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, loginResp.ExpiresAt); err == nil {
			expiresAt = parsed
		}
	}
	principal, err := a.fetchServerPrincipal(ctx, client, loginResp.Token)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return principal, loginResp.Token, expiresAt, nil
}

func (a *agentAuthManager) serverLogout(ctx context.Context, serverToken string) error {
	if a == nil || serverToken == "" {
		return nil
	}
	client, err := a.newServerHTTPClient()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverAPIURL("/api/v1/auth/logout"), nil)
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "pm_session", Value: serverToken})
	req.Header.Set("User-Agent", fmt.Sprintf("PrintMaster-Agent/%s", Version))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server logout failed: status %d", resp.StatusCode)
	}
	return nil
}

func (a *agentAuthManager) fetchServerPrincipal(ctx context.Context, client *http.Client, serverToken string) (*AgentPrincipal, error) {
	agentID, err := a.serverAuthAgentID()
	if err != nil || serverToken == "" {
		return nil, fmt.Errorf("agent authorization unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.serverAPIURL("/api/v1/auth/me"), nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "pm_session", Value: serverToken})
	req.Header.Set("User-Agent", fmt.Sprintf("PrintMaster-Agent/%s", Version))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth verification failed: status %d", resp.StatusCode)
	}
	var payload struct {
		Username  string   `json:"username"`
		Role      string   `json:"role"`
		TenantID  string   `json:"tenant_id"`
		TenantIDs []string `json:"tenant_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	ids := payload.TenantIDs
	if len(ids) == 0 && payload.TenantID != "" {
		ids = []string{payload.TenantID}
	}
	// Authentication alone is insufficient: the server must authorize this
	// session against this machine's persisted agent record.
	targetReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.serverAPIURL("/api/v1/agents/"+url.PathEscape(agentID)), nil)
	if err != nil {
		return nil, err
	}
	targetReq.AddCookie(&http.Cookie{Name: "pm_session", Value: serverToken})
	targetReq.Header.Set("User-Agent", fmt.Sprintf("PrintMaster-Agent/%s", Version))
	targetResp, err := client.Do(targetReq)
	if err != nil {
		return nil, err
	}
	defer targetResp.Body.Close()
	if targetResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent authorization failed: status %d", targetResp.StatusCode)
	}
	var target struct {
		AgentID  string `json:"agent_id"`
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(io.LimitReader(targetResp.Body, 1<<20)).Decode(&target); err != nil {
		return nil, err
	}
	if target.AgentID != agentID || !serverPrincipalAllowsAgent(payload.Username, payload.Role, payload.TenantID, ids, target.TenantID) {
		return nil, fmt.Errorf("agent identity or tenant authorization mismatch")
	}
	return &AgentPrincipal{
		Username:  payload.Username,
		Role:      payload.Role,
		Source:    "server",
		TenantIDs: ids,
	}, nil
}

func (a *agentAuthManager) serverAuthAgentID() (string, error) {
	if a == nil || a.config == nil || strings.TrimSpace(a.config.Server.AgentID) == "" {
		return "", fmt.Errorf("persisted agent ID unavailable")
	}
	return strings.TrimSpace(a.config.Server.AgentID), nil
}

func serverPrincipalAllowsAgent(username, role, tenantID string, tenantIDs []string, agentTenantID string) bool {
	if strings.TrimSpace(username) == "" {
		return false
	}
	if role == "admin" {
		return true
	}
	if (role != "operator" && role != "viewer") || agentTenantID == "" {
		return false
	}
	if len(tenantIDs) == 0 {
		return tenantID == agentTenantID
	}
	for _, id := range tenantIDs {
		if id == agentTenantID {
			return true
		}
	}
	return false
}

func (a *agentAuthManager) newServerHTTPClient() (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: a.serverSkipVerify}
	if a.serverCAPath != "" {
		pemData, err := os.ReadFile(a.serverCAPath)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("failed to parse server CA certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	return &http.Client{Timeout: serverAuthTimeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}, nil
}

func (a *agentAuthManager) serverAPIURL(p string) string {
	base := strings.TrimRight(a.serverURL, "/")
	if base == "" {
		return p
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

func (a *agentAuthManager) serverLoginURL(r *http.Request) string {
	if a == nil || strings.TrimSpace(a.serverURL) == "" {
		return "/login"
	}
	// Determine what URL the user originally wanted
	returnTo := "/"
	if r != nil && r.URL != nil {
		if uri := r.URL.RequestURI(); uri != "" {
			returnTo = uri
		}
	}

	// Build the agent callback URL that the server will redirect to after auth
	// We need to determine the agent's external URL
	agentCallbackURL := buildAgentCallbackURL(r, returnTo)
	agentID, err := a.serverAuthAgentID()
	if err != nil {
		return "/login?error=missing_agent_id"
	}
	agentCallbackURL += "&agent_id=" + url.QueryEscape(agentID)

	// Use 'redirect' parameter for external redirects (server login page convention)
	return strings.TrimRight(a.serverURL, "/") + "/login?agent_id=" + url.QueryEscape(agentID) + "&redirect=" + url.QueryEscape(agentCallbackURL)
}

// buildAgentCallbackURL constructs the callback URL that the server should redirect to after auth
func buildAgentCallbackURL(r *http.Request, returnTo string) string {
	// Try to determine the agent's base URL from the request
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	// Check for X-Forwarded-Proto header
	if r != nil {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = strings.ToLower(strings.TrimSpace(proto))
		}
	}

	host := "localhost:8080" // default fallback
	if r != nil && r.Host != "" {
		host = r.Host
	}

	// Build the callback URL
	callbackURL := fmt.Sprintf("%s://%s/api/v1/auth/callback?return_to=%s", scheme, host, url.QueryEscape(returnTo))
	return callbackURL
}

type staticResourceCache struct {
	sync.RWMutex
	items map[string]cachedResource
}

type cachedResource struct {
	data        []byte
	contentType string
	headers     http.Header
	expiry      time.Time
}

func newStaticResourceCache() *staticResourceCache {
	return &staticResourceCache{items: make(map[string]cachedResource)}
}

func (c *staticResourceCache) Get(key string) ([]byte, string, http.Header, bool) {
	c.RLock()
	defer c.RUnlock()
	item, ok := c.items[key]
	if !ok || time.Now().After(item.expiry) {
		return nil, "", nil, false
	}
	return item.data, item.contentType, item.headers, true
}

func (c *staticResourceCache) Set(key string, data []byte, contentType string, headers http.Header, ttl time.Duration) {
	c.Lock()
	defer c.Unlock()
	c.items[key] = cachedResource{
		data:        data,
		contentType: contentType,
		headers:     headers,
		expiry:      time.Now().Add(ttl),
	}
}

var (
	staticCache    = newStaticResourceCache()
	uploadWorkerMu sync.RWMutex
	uploadWorker   *UploadWorker
	// localProxyHandler is the root HTTP handler for direct proxy invocation
	// This is set when the web server starts and used by WebSocket proxy requests
	localProxyHandler   http.Handler
	localProxyHandlerMu sync.RWMutex
	// deviceStore is shared across the agent for persistence access
	deviceStore storage.DeviceStore
	// agentConfigStore stores user-configurable settings/ranges
	agentConfigStore storage.AgentConfigStore
	settingsManager  *SettingsManager
	// applyDiscoveryEffectsFunc allows deferred wiring of discovery settings hooks
	applyDiscoveryEffectsFunc func(map[string]interface{})
	// configEpsonRemoteModeEnabled tracks global feature flag state
	configEpsonRemoteModeEnabled bool
	// Global structured logger instance
	appLogger *logger.Logger
	// autoUpdateManagerMu protects access to autoUpdateManager
	autoUpdateManagerMu sync.RWMutex
	// autoUpdateManager handles agent self-update operations
	autoUpdateManager *autoupdate.Manager
	// scannerConfig centralizes runtime-adjustable scanner parameters
	scannerConfig struct {
		sync.RWMutex
		SNMPTimeoutMs       int
		SNMPRetries         int
		DiscoverConcurrency int
	}
)

// setLocalProxyHandler sets the global HTTP handler for direct proxy invocation.
// Called when the web server starts.
func setLocalProxyHandler(h http.Handler) {
	localProxyHandlerMu.Lock()
	localProxyHandler = h
	localProxyHandlerMu.Unlock()

	// Also set it on the upload worker if it exists
	uploadWorkerMu.RLock()
	w := uploadWorker
	uploadWorkerMu.RUnlock()
	if w != nil {
		w.SetLocalHandler(h)
	}
}

// getLocalProxyHandler returns the global HTTP handler for direct proxy invocation.
func getLocalProxyHandler() http.Handler {
	localProxyHandlerMu.RLock()
	defer localProxyHandlerMu.RUnlock()
	return localProxyHandler
}

// notifyServerDeviceDeleted sends a device_deleted notification to the server via WebSocket.
// This is called after a device is deleted locally so the server can also remove it.
// Runs as best-effort - does not block or fail if the server is unreachable.
func notifyServerDeviceDeleted(serial string) {
	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()

	if worker == nil {
		return
	}

	wsClient := worker.WSClient()
	if wsClient == nil || !wsClient.IsConnected() {
		return
	}

	msg := wscommon.Message{
		Type: wscommon.MessageTypeDeviceDeleted,
		Data: map[string]interface{}{
			"serial": serial,
		},
		Timestamp: time.Now(),
	}

	if err := wsClient.SendMessage(msg); err != nil {
		// Log but don't fail - notification is best-effort
		appLogger.Debug("Failed to notify server about device deletion", "serial", serial, "error", err)
	} else {
		appLogger.Info("Notified server about device deletion", "serial", serial)
	}
}

func runGarbageCollection(ctx context.Context, store storage.DeviceStore, config *agent.RetentionConfig) {
	ticker := time.NewTicker(24 * time.Hour) // Run daily
	defer ticker.Stop()

	// Check if context is already cancelled before running
	select {
	case <-ctx.Done():
		return
	default:
		// Run immediately on startup
		doGarbageCollection(store, config)
	}

	for {
		select {
		case <-ticker.C:
			doGarbageCollection(store, config)
		case <-ctx.Done():
			return
		}
	}
}

// doGarbageCollection performs the actual cleanup work
func doGarbageCollection(store storage.DeviceStore, config *agent.RetentionConfig) {
	ctx := context.Background()

	// Calculate cutoff timestamps
	scanHistoryCutoff := time.Now().AddDate(0, 0, -config.ScanHistoryDays).Unix()
	hiddenDevicesCutoff := time.Now().AddDate(0, 0, -config.HiddenDevicesDays).Unix()

	// Delete old scan history
	if scansDeleted, err := store.DeleteOldScans(ctx, scanHistoryCutoff); err != nil {
		appLogger.Error("Garbage collection: Failed to delete old scans", "error", err, "cutoff_days", config.ScanHistoryDays)
	} else if scansDeleted > 0 {
		appLogger.Info("Garbage collection: Deleted old scan history", "count", scansDeleted, "age_days", config.ScanHistoryDays)
	}

	// Delete old hidden devices
	if devicesDeleted, err := store.DeleteOldHiddenDevices(ctx, hiddenDevicesCutoff); err != nil {
		appLogger.Error("Garbage collection: Failed to delete old hidden devices", "error", err, "cutoff_days", config.HiddenDevicesDays)
	} else if devicesDeleted > 0 {
		appLogger.Info("Garbage collection: Deleted old hidden devices", "count", devicesDeleted, "age_days", config.HiddenDevicesDays)
	}
}

// runMetricsDownsampler runs periodic downsampling of metrics data
// This implements Netdata-style tiered storage: raw → hourly → daily → monthly
func runMetricsDownsampler(ctx context.Context, store storage.DeviceStore) {
	// Run every 6 hours (4 times per day)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	// Run immediately on startup (with a small delay to let the app initialize)
	select {
	case <-time.After(30 * time.Second):
		doMetricsDownsampling(store)
	case <-ctx.Done():
		return
	}

	for {
		select {
		case <-ticker.C:
			doMetricsDownsampling(store)
		case <-ctx.Done():
			return
		}
	}
}

// doMetricsDownsampling performs the actual downsampling work
func doMetricsDownsampling(store storage.DeviceStore) {
	ctx := context.Background()

	appLogger.Info("Metrics downsampling: Starting tiered aggregation")

	// Perform full downsampling: raw→hourly, hourly→daily, daily→monthly, cleanup
	if err := store.PerformFullDownsampling(ctx); err != nil {
		appLogger.Error("Metrics downsampling: Failed", "error", err)
	} else {
		appLogger.Info("Metrics downsampling: Completed successfully")
	}
}

// downsampleAgentMetrics reduces the number of data points while preserving first, last,
// and representative samples throughout the range. Uses LTTB-inspired algorithm
// for visually significant point selection.
func downsampleAgentMetrics(data []*storage.MetricsSnapshot, targetPoints int) []*storage.MetricsSnapshot {
	n := len(data)
	if n <= targetPoints || targetPoints < 3 {
		return data
	}

	// Always keep first and last points
	result := make([]*storage.MetricsSnapshot, 0, targetPoints)
	result = append(result, data[0])

	// Calculate bucket size for middle points
	bucketSize := float64(n-2) / float64(targetPoints-2)

	// Select representative points from each bucket
	// Simple approach: pick the point with the most change (to preserve peaks/valleys)
	for i := 0; i < targetPoints-2; i++ {
		bucketStart := int(float64(i)*bucketSize) + 1
		bucketEnd := int(float64(i+1)*bucketSize) + 1
		if bucketEnd > n-1 {
			bucketEnd = n - 1
		}
		if bucketStart >= bucketEnd {
			continue
		}

		// Find the point with max delta from linear interpolation (preserves peaks)
		bestIdx := bucketStart
		maxDelta := 0.0
		prevValue := float64(data[bucketStart-1].PageCount)
		nextValue := float64(data[bucketEnd].PageCount)

		for j := bucketStart; j < bucketEnd; j++ {
			// Expected value via linear interpolation
			t := float64(j-bucketStart+1) / float64(bucketEnd-bucketStart+1)
			expected := prevValue + t*(nextValue-prevValue)
			actual := float64(data[j].PageCount)
			delta := expected - actual
			if delta < 0 {
				delta = -delta
			}
			if delta > maxDelta {
				maxDelta = delta
				bestIdx = j
			}
		}

		result = append(result, data[bestIdx])
	}

	result = append(result, data[n-1])
	return result
}

// ensureTLSCertificates generates or loads TLS certificates for HTTPS
// If customCertPath and customKeyPath are provided, uses those instead
func ensureTLSCertificates(customCertPath, customKeyPath string) (certFile, keyFile string, err error) {
	// If custom cert paths provided, validate and use them
	if customCertPath != "" && customKeyPath != "" {
		if _, err := os.Stat(customCertPath); err == nil {
			if _, err := os.Stat(customKeyPath); err == nil {
				appLogger.Info("Using custom TLS certificates", "cert", customCertPath, "key", customKeyPath)
				return customCertPath, customKeyPath, nil
			}
		}
		appLogger.Warn("Custom TLS certificate paths invalid, falling back to auto-generated", "cert", customCertPath, "key", customKeyPath)
	}

	// Get data directory
	dataDir, err := storage.GetDataDir("PrintMaster")
	if err != nil {
		return "", "", fmt.Errorf("failed to get data directory: %w", err)
	}

	certFile = filepath.Join(dataDir, "server.crt")
	keyFile = filepath.Join(dataDir, "server.key")

	// Check if certificates already exist
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			// Both files exist
			return certFile, keyFile, nil
		}
	}

	// Generate new self-signed certificate
	appLogger.Info("Generating self-signed TLS certificate")

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour * 10) // 10 years

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"PrintMaster"},
			CommonName:   "PrintMaster Agent",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// Create self-signed certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate: %w", err)
	}

	// Write certificate file
	certOut, err := os.Create(certFile)
	if err != nil {
		return "", "", fmt.Errorf("failed to create cert file: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		certOut.Close()
		return "", "", fmt.Errorf("failed to write cert: %w", err)
	}
	certOut.Close()

	// Write private key file
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return "", "", fmt.Errorf("failed to create key file: %w", err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		keyOut.Close()
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		keyOut.Close()
		return "", "", fmt.Errorf("failed to write key: %w", err)
	}
	keyOut.Close()

	appLogger.Info("Generated self-signed TLS certificate", "cert", certFile, "key", keyFile)
	return certFile, keyFile, nil
}

// deviceStorageAdapter implements agent.DeviceStorage interface
type deviceStorageAdapter struct {
	store storage.DeviceStore
}

func (a *deviceStorageAdapter) StoreDiscoveredDevice(ctx context.Context, pi agent.PrinterInfo) error {
	// Convert PrinterInfo to Device
	device := storage.PrinterInfoToDevice(pi, false)
	device.Visible = true

	snapshot := storage.PrinterInfoToScanSnapshot(pi)
	metrics := storage.PrinterInfoToMetricsSnapshot(pi)
	if err := a.store.StoreDiscoveryAtomic(ctx, device, snapshot, metrics); err != nil {
		return fmt.Errorf("failed to persist discovery atomically: %w", err)
	}

	// Broadcast device update via SSE
	if sseHub != nil {
		isNew := device.FirstSeen.Equal(device.LastSeen) || time.Since(device.FirstSeen) < time.Second
		eventType := "device_updated"
		if isNew {
			eventType = "device_discovered"
		}
		sseHub.Broadcast(SSEEvent{
			Type: eventType,
			Data: map[string]interface{}{
				"serial": device.Serial,
				"ip":     device.IP,
				"make":   device.Manufacturer,
				"model":  device.Model,
			},
		})
	}

	return nil
}

// SSE (Server-Sent Events) Hub for real-time UI updates
type SSEEvent struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

type SSEClient struct {
	id     string
	events chan SSEEvent
}

type SSEHub struct {
	clients    map[string]*SSEClient
	broadcast  chan SSEEvent
	register   chan *SSEClient
	unregister chan *SSEClient
	shutdown   chan struct{}
	mu         sync.RWMutex
}

func NewSSEHub() *SSEHub {
	hub := &SSEHub{
		clients:    make(map[string]*SSEClient),
		broadcast:  make(chan SSEEvent, 100),
		register:   make(chan *SSEClient),
		unregister: make(chan *SSEClient),
		shutdown:   make(chan struct{}),
	}
	go hub.run()
	return hub
}

func (h *SSEHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client.id] = client
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client.id]; ok {
				delete(h.clients, client.id)
				close(client.events)
			}
			h.mu.Unlock()
		case event := <-h.broadcast:
			h.mu.RLock()
			for _, client := range h.clients {
				select {
				case client.events <- event:
				default:
					// Client's buffer is full, skip
				}
			}
			h.mu.RUnlock()
		case <-h.shutdown:
			// Close all client connections
			h.mu.Lock()
			for _, client := range h.clients {
				close(client.events)
			}
			h.clients = make(map[string]*SSEClient)
			h.mu.Unlock()
			return
		}
	}
}

func (h *SSEHub) Stop() {
	close(h.shutdown)
}

func (h *SSEHub) Broadcast(event SSEEvent) {
	select {
	case h.broadcast <- event:
	default:
		// Broadcast buffer full, skip event
	}
}

func (h *SSEHub) NewClient() *SSEClient {
	client := &SSEClient{
		id:     fmt.Sprintf("client_%d", time.Now().UnixNano()),
		events: make(chan SSEEvent, 10),
	}
	h.register <- client
	return client
}

func (h *SSEHub) RemoveClient(client *SSEClient) {
	h.unregister <- client
}

var sseHub *SSEHub

func mapIntoStruct(src map[string]interface{}, dst interface{}) {
	if src == nil || dst == nil {
		return
	}
	data, err := json.Marshal(src)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, dst)
}

func structToMap(src interface{}) map[string]interface{} {
	if src == nil {
		return map[string]interface{}{}
	}
	data, err := json.Marshal(src)
	if err != nil {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}

func applyFeaturesSettingsEffects(feat *pmsettings.FeaturesSettings) {
	if feat == nil {
		return
	}
	configEnabled := configEpsonRemoteModeEnabled
	effective := feat.EpsonRemoteModeEnabled || configEnabled
	previous := featureflags.EpsonRemoteModeEnabled()
	featureflags.SetEpsonRemoteMode(effective)
	if configEnabled {
		feat.EpsonRemoteModeEnabled = effective
	}
	if appLogger != nil && effective != previous {
		appLogger.Info("Epson remote mode updated", "enabled", effective, "config_override", configEnabled)
	}
}

func applySpoolerSettings(spooler *pmsettings.SpoolerSettings) {
	if spooler == nil {
		return
	}
	// Stop the current worker if running
	StopSpoolerWorker()

	if !spooler.Enabled {
		if appLogger != nil {
			appLogger.Info("Spooler tracking disabled")
		}
		return
	}

	// Start with new config if enabled and we have a store
	if globalLocalPrinterStore == nil {
		if appLogger != nil {
			appLogger.Warn("Cannot start spooler worker: no local printer store available")
		}
		return
	}

	pollInterval := time.Duration(spooler.PollIntervalSeconds) * time.Second
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}
	if pollInterval > 5*time.Minute {
		pollInterval = 5 * time.Minute
	}

	config := SpoolerWorkerConfig{
		PollInterval:           pollInterval,
		IncludeNetworkPrinters: spooler.IncludeNetworkPrinters,
		IncludeVirtualPrinters: spooler.IncludeVirtualPrinters,
		AutoTrackUSB:           true,
		AutoTrackLocal:         false,
	}

	if err := StartSpoolerWorker(globalLocalPrinterStore, config, appLogger); err != nil {
		if appLogger != nil {
			appLogger.Warn("Failed to start spooler worker with new settings", "error", err)
		}
	} else if appLogger != nil {
		appLogger.Info("Spooler worker restarted with new settings",
			"poll_interval", pollInterval,
			"include_network", spooler.IncludeNetworkPrinters,
			"include_virtual", spooler.IncludeVirtualPrinters)
	}
}

func applyEffectiveSettingsSnapshot(cfg pmsettings.Settings) {
	if applyDiscoveryEffectsFunc != nil {
		discMap := structToMap(cfg.Discovery)
		delete(discMap, "ranges_text")
		delete(discMap, "detected_subnet")
		applyDiscoveryEffectsFunc(discMap)
	}
	applyFeaturesSettingsEffects(&cfg.Features)
}

func loadUnifiedSettings(store storage.AgentConfigStore) pmsettings.Settings {
	base := pmsettings.DefaultSettings()
	managed := false
	if settingsManager != nil {
		base, managed = settingsManager.baseSettings()
	}
	if store == nil {
		pmsettings.Sanitize(&base)
		return base
	}
	if !managed {
		var disc map[string]interface{}
		if err := store.GetConfigValue("discovery_settings", &disc); err == nil && disc != nil {
			mapIntoStruct(disc, &base.Discovery)
		}
	}
	if txt, err := store.GetRanges(); err == nil {
		base.Discovery.RangesText = txt
	}
	if ipnets, err := agent.GetLocalSubnets(); err == nil && len(ipnets) > 0 {
		base.Discovery.DetectedSubnet = ipnets[0].String()
	}
	// Load unified settings structure
	var unified map[string]interface{}
	if err := store.GetConfigValue("settings", &unified); err == nil && unified != nil {
		// SNMP settings (fleet-managed, don't allow local override when managed)
		if snmpRaw, ok := unified["snmp"].(map[string]interface{}); ok && !managed {
			mapIntoStruct(snmpRaw, &base.SNMP)
		}
		// Features settings (fleet-managed, don't allow local override when managed)
		if featRaw, ok := unified["features"].(map[string]interface{}); ok && !managed {
			mapIntoStruct(featRaw, &base.Features)
		}
		// Logging settings (agent-local, always allow local override)
		if logRaw, ok := unified["logging"].(map[string]interface{}); ok {
			mapIntoStruct(logRaw, &base.Logging)
		}
		// Web settings (agent-local, always allow local override)
		if webRaw, ok := unified["web"].(map[string]interface{}); ok {
			mapIntoStruct(webRaw, &base.Web)
		}
	}
	pmsettings.Sanitize(&base)
	applyFeaturesSettingsEffects(&base.Features)
	return base
}

// applyServerConfigFromStore merges persisted server connection settings from the
// agent config database into the in-memory configuration so that UI-driven join
// flows can enable uploads without editing config.toml manually.
// Note: Environment variables take precedence over stored config values.
func applyServerConfigFromStore(agentCfg *AgentConfig, store storage.AgentConfigStore, log *logger.Logger) {
	if agentCfg == nil || store == nil {
		return
	}

	var persisted ServerConnectionConfig
	if err := store.GetConfigValue("server", &persisted); err != nil {
		if log != nil {
			log.Warn("Failed to load server settings from config store", "error", err)
		}
		return
	}

	if strings.TrimSpace(persisted.URL) == "" {
		return
	}

	// Only apply stored URL if no environment variable is set
	// This allows SERVER_URL env var to override stored config (Docker Compose scenario)
	envURL := os.Getenv("SERVER_URL")
	if envURL == "" {
		agentCfg.Server.URL = strings.TrimSpace(persisted.URL)
	} else if log != nil {
		log.Debug("SERVER_URL environment variable set; ignoring stored URL",
			"env_url", envURL,
			"stored_url", persisted.URL)
	}
	if persisted.Name != "" {
		agentCfg.Server.Name = persisted.Name
	}
	agentCfg.Server.CAPath = persisted.CAPath
	// Only apply InsecureSkipVerify from store if env var not set
	if os.Getenv("SERVER_INSECURE_SKIP_VERIFY") == "" {
		agentCfg.Server.InsecureSkipVerify = persisted.InsecureSkipVerify
	}
	if persisted.UploadInterval > 0 {
		agentCfg.Server.UploadInterval = persisted.UploadInterval
	}
	if persisted.HeartbeatInterval > 0 {
		agentCfg.Server.HeartbeatInterval = persisted.HeartbeatInterval
	}
	if persisted.AgentID != "" {
		agentCfg.Server.AgentID = persisted.AgentID
	}
	if persisted.Token != "" {
		agentCfg.Server.Token = persisted.Token
	}
	if persisted.Enabled {
		agentCfg.Server.Enabled = true
	} else if agentCfg.Server.URL != "" {
		// Default to enabled when a URL is present but legacy data omitted the flag
		agentCfg.Server.Enabled = true
	}

	if log != nil {
		log.Info("Loaded server configuration from agent database",
			"url", agentCfg.Server.URL,
			"enabled", agentCfg.Server.Enabled,
			"insecure_skip_verify", agentCfg.Server.InsecureSkipVerify)
	}
}

type serverConnectionStatus struct {
	Enabled            bool       `json:"enabled"`
	URL                string     `json:"url"`
	Name               string     `json:"name"`
	AgentID            string     `json:"agent_id"`
	InsecureSkipVerify bool       `json:"insecure_skip_verify"`
	CAPath             string     `json:"ca_path"`
	UploadInterval     int        `json:"upload_interval"`
	HeartbeatInterval  int        `json:"heartbeat_interval"`
	Connected          bool       `json:"connected"`
	ConnectionMode     string     `json:"connection_mode"`
	LastHeartbeat      *time.Time `json:"last_heartbeat,omitempty"`
	LastDeviceUpload   *time.Time `json:"last_device_upload,omitempty"`
	LastMetricsUpload  *time.Time `json:"last_metrics_upload,omitempty"`
	HasAgentToken      bool       `json:"has_agent_token"`
	HasJoinToken       bool       `json:"has_join_token"`
	WebSocketEnabled   bool       `json:"websocket_enabled"`
	WebSocketConnected bool       `json:"websocket_connected"`
}

var (
	serverStatusMu          sync.Mutex
	serverStatusFingerprint string
)

func snapshotServerConnectionStatus(agentCfg *AgentConfig, dataDir string) serverConnectionStatus {
	status := serverConnectionStatus{}
	if agentCfg != nil {
		status.Enabled = agentCfg.Server.Enabled
		status.URL = agentCfg.Server.URL
		status.Name = agentCfg.Server.Name
		status.AgentID = agentCfg.Server.AgentID
		status.InsecureSkipVerify = agentCfg.Server.InsecureSkipVerify
		status.CAPath = agentCfg.Server.CAPath
		status.UploadInterval = agentCfg.Server.UploadInterval
		status.HeartbeatInterval = agentCfg.Server.HeartbeatInterval
	}

	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()
	if worker != nil {
		wStatus := worker.Status()
		status.Connected = status.Enabled && wStatus.Running
		status.LastHeartbeat = timePtr(wStatus.LastHeartbeat)
		status.LastDeviceUpload = timePtr(wStatus.LastDeviceUpload)
		status.LastMetricsUpload = timePtr(wStatus.LastMetricsUpload)
		status.WebSocketEnabled = wStatus.WebSocketEnabled
		status.WebSocketConnected = wStatus.WebSocketConnected
	} else {
		status.Connected = false
	}
	mode := "disconnected"
	if status.Enabled && status.URL != "" {
		if status.Connected {
			mode = "connected"
			if status.WebSocketEnabled && status.WebSocketConnected {
				mode = "live"
			}
		}
	}
	status.ConnectionMode = mode

	if dataDir != "" {
		status.HasAgentToken = LoadServerToken(dataDir) != ""
		status.HasJoinToken = LoadServerJoinToken(dataDir) != ""
	}

	return status
}

func serverStatusHash(status serverConnectionStatus) string {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Sprintf("fallback:%v:%v", status.Enabled, time.Now().UnixNano())
	}
	return string(data)
}

func setServerStatusFingerprint(status serverConnectionStatus) {
	serverStatusMu.Lock()
	defer serverStatusMu.Unlock()
	serverStatusFingerprint = serverStatusHash(status)
}

func markServerStatusFingerprint(status serverConnectionStatus) bool {
	hash := serverStatusHash(status)
	serverStatusMu.Lock()
	defer serverStatusMu.Unlock()
	if hash == serverStatusFingerprint {
		return false
	}
	serverStatusFingerprint = hash
	return true
}

func broadcastServerStatusSnapshot(status serverConnectionStatus, reason string) {
	if sseHub == nil {
		return
	}
	payload := map[string]interface{}{
		"status": status,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	sseHub.Broadcast(SSEEvent{Type: "server_status", Data: payload})
}

func broadcastServerStatus(agentCfg *AgentConfig, dataDir string, reason string, force bool) {
	if sseHub == nil {
		return
	}
	status := snapshotServerConnectionStatus(agentCfg, dataDir)
	if force {
		setServerStatusFingerprint(status)
		broadcastServerStatusSnapshot(status, reason)
		return
	}
	if markServerStatusFingerprint(status) {
		broadcastServerStatusSnapshot(status, reason)
	}
}

func startServerStatusMonitor(ctx context.Context, agentCfg *AgentConfig, dataDir string, interval time.Duration) {
	if agentCfg == nil || dataDir == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				broadcastServerStatus(agentCfg, dataDir, "", false)
			}
		}
	}()
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t
	return &value
}

func disconnectFromServer(agentCfg *AgentConfig, dataDir string) error {
	if appLogger != nil {
		appLogger.Info("Disconnecting agent from server")
	}
	uploadWorkerMu.Lock()
	if uploadWorker != nil {
		uploadWorker.Stop()
		uploadWorker = nil
	}
	uploadWorkerMu.Unlock()

	if strings.TrimSpace(dataDir) != "" {
		if err := DeleteServerToken(dataDir); err != nil {
			return fmt.Errorf("failed to remove server token: %w", err)
		}
		if err := SaveServerJoinToken(dataDir, ""); err != nil {
			return fmt.Errorf("failed to clear join token: %w", err)
		}
	}

	if agentCfg != nil {
		agentCfg.Server.Enabled = false
		agentCfg.Server.URL = ""
		agentCfg.Server.Name = ""
		agentCfg.Server.CAPath = ""
		agentCfg.Server.InsecureSkipVerify = false
		agentCfg.Server.Token = ""
	}

	if agentConfigStore != nil {
		persisted := ServerConnectionConfig{}
		if agentCfg != nil {
			persisted.AgentID = agentCfg.Server.AgentID
		}
		if err := agentConfigStore.SetConfigValue("server", persisted); err != nil {
			return fmt.Errorf("failed to persist server disconnect: %w", err)
		}
	}

	// Clear server-managed settings snapshot so settings become editable again
	if settingsManager != nil {
		if err := settingsManager.ClearManagedSnapshot(); err != nil {
			if appLogger != nil {
				appLogger.Warn("Failed to clear server-managed settings", "error", err)
			}
		} else if appLogger != nil {
			appLogger.Info("Cleared server-managed settings, local editing now unlocked")
		}
	}

	broadcastServerStatus(agentCfg, dataDir, "disconnected", true)
	return nil
}

// startServerUploadWorker encapsulates upload worker bootstrap (agent
// registration, token persistence, WebSocket setup) so it can be invoked at
// startup and again after a join event without duplicating logic.
func startServerUploadWorker(
	ctx context.Context,
	agentCfg *AgentConfig,
	dataDir string,
	deviceStore storage.DeviceStore,
	settings *SettingsManager,
	workerLogger Logger,
) (*UploadWorker, error) {
	if agentCfg == nil {
		return nil, fmt.Errorf("agent configuration unavailable")
	}
	if strings.TrimSpace(agentCfg.Server.URL) == "" {
		return nil, fmt.Errorf("server URL not configured")
	}

	agentID := agentCfg.Server.AgentID
	if agentID == "" {
		var err error
		agentID, err = LoadOrGenerateAgentID(dataDir)
		if err != nil {
			return nil, fmt.Errorf("failed to load or generate agent ID: %w", err)
		}
		agentCfg.Server.AgentID = agentID
		workerLogger.Info("Generated new agent ID", "agent_id", agentID)
	}

	agentName := agentCfg.Server.Name
	if agentName == "" {
		if hostname, err := os.Hostname(); err == nil {
			agentName = hostname
		}
	}

	workerLogger.Info("Server integration enabled",
		"url", agentCfg.Server.URL,
		"agent_id", agentID,
		"agent_name", agentName,
		"ca_path", agentCfg.Server.CAPath,
		"upload_interval", agentCfg.Server.UploadInterval,
		"heartbeat_interval", agentCfg.Server.HeartbeatInterval)

	token := LoadServerToken(dataDir)
	if token == "" {
		workerLogger.Debug("No saved server token found")
	}

	serverClient := agent.NewServerClientWithName(
		agentCfg.Server.URL,
		agentID,
		agentName,
		token,
		agentCfg.Server.CAPath,
		agentCfg.Server.InsecureSkipVerify,
	)

	workerConfig := UploadWorkerConfig{
		HeartbeatInterval: time.Duration(agentCfg.Server.HeartbeatInterval) * time.Second,
		UploadInterval:    time.Duration(agentCfg.Server.UploadInterval) * time.Second,
		RetryAttempts:     3,
		RetryBackoff:      2 * time.Second,
		UseWebSocket:      true,
	}
	if workerConfig.HeartbeatInterval <= 0 {
		workerConfig.HeartbeatInterval = 60 * time.Second
	}
	if workerConfig.UploadInterval <= 0 {
		workerConfig.UploadInterval = 5 * time.Minute
	}

	uploadWorker := NewUploadWorker(serverClient, deviceStore, workerLogger, settings, workerConfig, dataDir)

	// Set local handler if web server has already started
	if h := getLocalProxyHandler(); h != nil {
		uploadWorker.SetLocalHandler(h)
	}

	// Build version info for heartbeats
	versionInfo := &agent.AgentVersionInfo{
		Version:         Version,
		ProtocolVersion: "1",
		BuildType:       BuildType,
		GitCommit:       GitCommit,
	}

	if err := uploadWorker.StartWithVersionInfo(ctx, Version, versionInfo); err != nil {
		return nil, err
	}

	if newToken := serverClient.GetToken(); newToken != "" && newToken != token {
		if err := SaveServerToken(dataDir, newToken); err != nil {
			workerLogger.Error("Failed to save server token", "error", err)
		} else {
			workerLogger.Info("Server token saved")
		}
	}

	broadcastServerStatus(agentCfg, dataDir, "upload_worker_started", true)
	return uploadWorker, nil
}

type serverJoinParams struct {
	ServerURL string
	Token     string
	CAPath    string
	Insecure  bool
	AgentName string
}

type serverJoinResult struct {
	TenantID   string
	AgentToken string
	AgentName  string
	AgentID    string
}

type joinError struct {
	status int
	err    error
}

func (e *joinError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *joinError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newJoinError(status int, err error) error {
	if err == nil {
		err = fmt.Errorf("unknown join error")
	}
	return &joinError{status: status, err: err}
}

func joinErrorStatus(err error) int {
	var je *joinError
	if errors.As(err, &je) {
		return je.status
	}
	return http.StatusInternalServerError
}

func resolveAgentDisplayName(agentCfg *AgentConfig, candidate string) string {
	if name := strings.TrimSpace(candidate); name != "" {
		return name
	}
	if agentCfg != nil {
		if cfgName := strings.TrimSpace(agentCfg.Server.Name); cfgName != "" {
			return cfgName
		}
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "PrintMaster Agent"
}

func performServerJoin(
	reqCtx context.Context,
	appCtx context.Context,
	params serverJoinParams,
	agentCfg *AgentConfig,
	cfgStore storage.AgentConfigStore,
	deviceStore storage.DeviceStore,
	settings *SettingsManager,
	logger *logger.Logger,
	isSvc bool,
) (*serverJoinResult, error) {
	serverURL := strings.TrimSpace(params.ServerURL)
	joinToken := strings.TrimSpace(params.Token)
	if serverURL == "" {
		return nil, newJoinError(http.StatusBadRequest, fmt.Errorf("server_url required"))
	}
	if joinToken == "" {
		return nil, newJoinError(http.StatusBadRequest, fmt.Errorf("token required"))
	}
	dataDir, err := config.GetDataDirectory("agent", isSvc)
	if err != nil {
		return nil, newJoinError(http.StatusInternalServerError, fmt.Errorf("failed to determine data directory: %w", err))
	}
	agentID, err := LoadOrGenerateAgentID(dataDir)
	if err != nil {
		return nil, newJoinError(http.StatusInternalServerError, fmt.Errorf("failed to load or generate agent id: %w", err))
	}
	caPath := strings.TrimSpace(params.CAPath)
	agentName := resolveAgentDisplayName(agentCfg, params.AgentName)
	client := agent.NewServerClientWithName(serverURL, agentID, agentName, "", caPath, params.Insecure)
	agentToken, tenantID, err := client.RegisterWithToken(reqCtx, joinToken, Version)
	if err != nil {
		return nil, newJoinError(http.StatusBadGateway, err)
	}
	if agentCfg != nil {
		agentCfg.Server.Enabled = true
		agentCfg.Server.URL = serverURL
		agentCfg.Server.Name = agentName
		agentCfg.Server.CAPath = caPath
		agentCfg.Server.InsecureSkipVerify = params.Insecure
		agentCfg.Server.AgentID = agentID
	}
	if cfgStore != nil {
		uploadInterval := 0
		heartbeatInterval := 0
		if agentCfg != nil {
			uploadInterval = agentCfg.Server.UploadInterval
			heartbeatInterval = agentCfg.Server.HeartbeatInterval
		}
		persisted := ServerConnectionConfig{
			Enabled:            true,
			URL:                serverURL,
			Name:               agentName,
			CAPath:             caPath,
			InsecureSkipVerify: params.Insecure,
			UploadInterval:     uploadInterval,
			HeartbeatInterval:  heartbeatInterval,
			AgentID:            agentID,
		}
		if err := cfgStore.SetConfigValue("server", persisted); err != nil {
			if logger != nil {
				logger.Warn("Failed to persist server settings", "error", err)
			}
		}
	}
	if err := SaveServerToken(dataDir, agentToken); err != nil {
		return nil, newJoinError(http.StatusInternalServerError, fmt.Errorf("failed to save server token: %w", err))
	}
	if err := SaveServerJoinToken(dataDir, joinToken); err != nil {
		return nil, newJoinError(http.StatusInternalServerError, fmt.Errorf("failed to save join token: %w", err))
	}
	uploadWorkerMu.RLock()
	existingWorker := uploadWorker
	uploadWorkerMu.RUnlock()
	if existingWorker != nil && existingWorker.client != nil {
		existingWorker.client.SetToken(agentToken)
		existingWorker.client.BaseURL = serverURL
		maybeStartAutoUpdateWorker(appCtx, agentCfg, dataDir, isSvc, logger)
	} else {
		go func() {
			worker, err := startServerUploadWorker(appCtx, agentCfg, dataDir, deviceStore, settings, logger)
			if err != nil {
				if logger != nil {
					logger.Error("Failed to start upload worker after join", "error", err)
				}
				return
			}
			uploadWorkerMu.Lock()
			uploadWorker = worker
			uploadWorkerMu.Unlock()

			// Check if local proxy handler was set while we were starting up
			if h := getLocalProxyHandler(); h != nil {
				worker.SetLocalHandler(h)
			}

			maybeStartAutoUpdateWorker(appCtx, agentCfg, dataDir, isSvc, logger)
		}()
	}
	broadcastServerStatus(agentCfg, dataDir, "joined", true)
	return &serverJoinResult{
		TenantID:   tenantID,
		AgentToken: agentToken,
		AgentName:  agentName,
		AgentID:    agentID,
	}, nil
}

func maybeStartAutoUpdateWorker(appCtx context.Context, agentCfg *AgentConfig, dataDir string, isService bool, log *logger.Logger) {
	autoUpdateManagerMu.RLock()
	alreadyRunning := autoUpdateManager != nil
	autoUpdateManagerMu.RUnlock()
	if alreadyRunning {
		return
	}
	go initAutoUpdateWorker(appCtx, agentCfg, dataDir, isService, log)
}

// tryLearnOIDForValue performs an SNMP walk to find an OID that returns the specified value
// Returns the OID if found, empty string otherwise
func tryLearnOIDForValue(ctx context.Context, ip string, vendorHint string, fieldName string, targetValue interface{}) string {
	if ip == "" {
		return ""
	}

	// Convert target value to string for comparison
	targetStr := fmt.Sprintf("%v", targetValue)
	if targetStr == "" {
		return ""
	}

	// Perform a targeted SNMP walk on common MIB roots
	appLogger.Info("Attempting to learn OID for locked field", "ip", ip, "field", fieldName, "target_value", targetStr)

	result, err := scanner.QueryDevice(ctx, ip, scanner.QueryFull, vendorHint, 10)
	if err != nil {
		appLogger.Warn("Failed to query device for OID learning", "ip", ip, "error", err)
		return ""
	}

	if result == nil || len(result.PDUs) == 0 {
		return ""
	}

	// Search through PDUs for matching value
	for _, pdu := range result.PDUs {
		var pduValueStr string

		// Convert PDU value to string based on type
		switch pdu.Type {
		case gosnmp.OctetString:
			if bytes, ok := pdu.Value.([]byte); ok {
				pduValueStr = string(bytes)
			} else {
				pduValueStr = fmt.Sprintf("%v", pdu.Value)
			}
		case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.Counter64:
			pduValueStr = fmt.Sprintf("%v", pdu.Value)
		default:
			pduValueStr = fmt.Sprintf("%v", pdu.Value)
		}

		// Check for exact match or numeric match
		if pduValueStr == targetStr {
			appLogger.Info("Found matching OID for field", "ip", ip, "field", fieldName, "oid", pdu.Name, "value", pduValueStr)
			return pdu.Name
		}

		// For numeric fields, try parsing and comparing as integers
		if strings.Contains(strings.ToLower(fieldName), "page") || strings.Contains(strings.ToLower(fieldName), "count") {
			targetInt, targetErr := strconv.ParseInt(targetStr, 10, 64)
			pduInt, pduErr := strconv.ParseInt(pduValueStr, 10, 64)
			if targetErr == nil && pduErr == nil && targetInt == pduInt {
				appLogger.Info("Found matching OID for numeric field", "ip", ip, "field", fieldName, "oid", pdu.Name, "value", pduInt)
				return pdu.Name
			}
		}
	}

	appLogger.Info("No matching OID found for field", "ip", ip, "field", fieldName, "target_value", targetStr, "pdus_checked", len(result.PDUs))
	return ""
}

func main() {
	// Parse command-line flags for service management
	configPath := flag.String("config", "config.toml", "Configuration file path")
	generateConfig := flag.Bool("generate-config", false, "Generate default config file and exit")
	serviceCmd := flag.String("service", "", "Service control: install, uninstall, start, stop, run")
	showVersion := flag.Bool("version", false, "Show version information and exit")
	quiet := flag.Bool("quiet", false, "Suppress informational output (errors/warnings still shown)")
	flag.BoolVar(quiet, "q", false, "Shorthand for --quiet")
	silent := flag.Bool("silent", false, "Suppress ALL output (complete silence)")
	flag.BoolVar(silent, "s", false, "Shorthand for --silent")
	healthCheck := flag.Bool("health", false, "Perform local health check against /health and exit")
	flag.Parse()

	// Set quiet/silent mode globally for util functions
	if *silent {
		commonutil.SetSilentMode(true)
	} else {
		commonutil.SetQuietMode(*quiet)
	}

	// Show version if requested
	if *showVersion {
		fmt.Printf("PrintMaster Agent %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		fmt.Printf("Git Commit: %s\n", GitCommit)
		fmt.Printf("Build Type: %s\n", BuildType)
		fmt.Printf("Go Version: %s\n", runtime.Version())
		fmt.Printf("OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return
	}

	// Generate default config if requested
	if *generateConfig {
		if err := WriteDefaultAgentConfig(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Generated default configuration at %s\n", *configPath)
		return
	}

	// Lightweight health probe for Docker/monitoring: call local /health and exit.
	if *healthCheck {
		if err := runAgentHealthCheck(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("healthy")
		return
	}

	// Handle service commands
	if *serviceCmd != "" {
		handleServiceCommand(*serviceCmd)
		return
	}

	// Check if running as service and start appropriately
	if !service.Interactive() {
		// Running as service, use service wrapper
		runAsService()
		return
	}

	// Running interactively, start normally (no context means run forever)
	runInteractive(context.Background(), *configPath)
}

// handleServiceCommand processes service install/uninstall/start/stop commands
func handleServiceCommand(cmd string) {
	svcConfig := getServiceConfig()
	prg := &program{}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create service: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "install":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		// Check if service already exists and handle gracefully
		status, _ := s.Status()
		if status != service.StatusUnknown {
			commonutil.ShowWarning("Service already exists, removing first...")

			// Stop if running
			if status == service.StatusRunning {
				commonutil.ShowInfo("Stopping existing service...")
				_ = s.Stop()
				time.Sleep(2 * time.Second)
				commonutil.ShowSuccess("Service stopped")
			}

			// Uninstall existing
			commonutil.ShowInfo("Removing existing service...")
			if err := s.Uninstall(); err != nil {
				// Ignore "marked for deletion" errors - we can still install over it
				if !strings.Contains(err.Error(), "marked for deletion") {
					commonutil.ShowError(fmt.Sprintf("Failed to remove existing service: %v", err))
					commonutil.ShowCompletionScreen(false, "Installation Failed")
					os.Exit(1)
				}
				commonutil.ShowWarning("Service marked for deletion, will install anyway")
			} else {
				commonutil.ShowSuccess("Existing service removed")
			}
			time.Sleep(500 * time.Millisecond)
		}

		// Create service directories first
		commonutil.ShowInfo("Setting up directories...")
		time.Sleep(300 * time.Millisecond)
		if err := setupServiceDirectories(); err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to setup service directories: %v", err))
			commonutil.ShowCompletionScreen(false, "Installation Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Directories ready")

		commonutil.ShowInfo("Installing service...")
		time.Sleep(500 * time.Millisecond)
		err = s.Install()
		if err != nil {
			// If service already exists, that's actually okay for install
			if strings.Contains(err.Error(), "already exists") {
				commonutil.ShowWarning("Service already exists (this is normal)")
			} else {
				commonutil.ShowError(fmt.Sprintf("Failed to install service: %v", err))
				commonutil.ShowCompletionScreen(false, "Installation Failed")
				os.Exit(1)
			}
		}
		commonutil.ShowSuccess("Service installed")

		commonutil.ShowCompletionScreen(true, "Service Installed!")
		fmt.Println()
		commonutil.ShowInfo("Use '--service start' to start the service")

	case "uninstall":
		err = s.Uninstall()
		if err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to uninstall service: %v", err))
			os.Exit(1)
		}
		commonutil.ShowInfo("PrintMaster Agent service uninstalled successfully")

	case "start":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		commonutil.ShowInfo("Starting service...")
		err = s.Start()
		if err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to start service: %v", err))
			commonutil.ShowCompletionScreen(false, "Start Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service started")

		commonutil.ShowCompletionScreen(true, "Service Started!")

	case "stop":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		commonutil.ShowInfo("Stopping service...")
		done := make(chan bool)
		go commonutil.AnimateProgress(0, "Stopping service (may take up to 30 seconds)", done)
		err = s.Stop()
		done <- true

		if err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to stop service: %v", err))
			commonutil.ShowCompletionScreen(false, "Stop Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service stopped")

		commonutil.ShowCompletionScreen(true, "Service Stopped!")

	case "status":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		// Get service status
		status, statusErr := s.Status()

		fmt.Println()
		commonutil.ShowInfo("Service Status Information")
		fmt.Println()

		// Service state
		var statusText, statusColor string
		switch status { //nolint:exhaustive
		case service.StatusRunning:
			statusText = "RUNNING"
			statusColor = commonutil.ColorGreen
		case service.StatusStopped:
			statusText = "STOPPED"
			statusColor = commonutil.ColorYellow
		case service.StatusUnknown:
			statusText = "NOT INSTALLED"
			statusColor = commonutil.ColorRed
		default:
			statusText = "UNKNOWN"
			statusColor = commonutil.ColorDim
		}

		if statusErr != nil {
			fmt.Printf("  %sService State:%s %s%s%s (%v)\n",
				commonutil.ColorDim, commonutil.ColorReset,
				statusColor, statusText, commonutil.ColorReset,
				statusErr)
		} else {
			fmt.Printf("  %sService State:%s %s%s%s\n",
				commonutil.ColorDim, commonutil.ColorReset,
				statusColor, commonutil.ColorBold+statusText, commonutil.ColorReset)
		}

		// Service configuration
		cfg := getServiceConfig()
		fmt.Printf("  %sService Name:%s  %s\n", commonutil.ColorDim, commonutil.ColorReset, cfg.Name)
		fmt.Printf("  %sDisplay Name:%s  %s\n", commonutil.ColorDim, commonutil.ColorReset, cfg.DisplayName)
		fmt.Printf("  %sDescription:%s   %s\n", commonutil.ColorDim, commonutil.ColorReset, cfg.Description)
		fmt.Printf("  %sData Directory:%s %s\n", commonutil.ColorDim, commonutil.ColorReset, cfg.WorkingDirectory)

		// Try to get more details on Windows
		if runtime.GOOS == "windows" && status == service.StatusRunning {
			fmt.Println()
			commonutil.ShowInfo("Checking service details...")

			// Use sc.exe to query service for more info
			cmd := exec.Command("sc", "query", cfg.Name)
			output, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if strings.Contains(line, "PID") {
						fmt.Printf("  %s%s%s\n", commonutil.ColorDim, line, commonutil.ColorReset)
					}
				}
			}

			// Try to get uptime via wmic
			cmd = exec.Command("wmic", "service", "where", fmt.Sprintf("name='%s'", cfg.Name), "get", "ProcessId,Started", "/value")
			output, err = cmd.Output()
			if err == nil {
				fmt.Printf("  %s%s%s\n", commonutil.ColorDim, strings.TrimSpace(string(output)), commonutil.ColorReset)
			}
		}

		fmt.Println()

		// Show helpful next steps based on status
		switch status {
		case service.StatusRunning:
			commonutil.ShowInfo("Service is running normally")
			fmt.Println()
			fmt.Printf("  %sWeb UI:%s http://localhost:8080 or https://localhost:8443\n", commonutil.ColorDim, commonutil.ColorReset)
		case service.StatusStopped:
			commonutil.ShowWarning("Service is installed but not running - Use '--service start' to start the service")
		default:
			commonutil.ShowWarning("Service is not installed - Use '--service install' to install the service")
		}

		fmt.Println()
		commonutil.PromptToContinue()

	case "restart":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		commonutil.ShowInfo("Stopping service...")
		if err := s.Stop(); err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to stop service: %v", err))
			commonutil.ShowCompletionScreen(false, "Restart Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service stopped")

		time.Sleep(1 * time.Second)

		commonutil.ShowInfo("Starting service...")
		if err := s.Start(); err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to start service: %v", err))
			commonutil.ShowCompletionScreen(false, "Restart Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service started")

		commonutil.ShowCompletionScreen(true, "Service Restarted!")

	case "update":
		// Show banner
		commonutil.ShowBanner(Version, GitCommit, BuildTime, "Fleet Management Agent")

		// Stop service if running
		commonutil.ShowInfo("Stopping service...")
		done := make(chan bool)
		go commonutil.AnimateProgress(0, "Stopping service (may take up to 30 seconds)", done)

		stopErr := s.Stop()
		if stopErr != nil {
			done <- true
			commonutil.ShowWarning("Service not running or already stopped")
		} else {
			// Wait for service to fully stop (max 30 seconds)
			for i := 0; i < 30; i++ {
				time.Sleep(1 * time.Second)

				// Check service status (Windows-specific check)
				if runtime.GOOS == "windows" {
					status, _ := s.Status()
					if status == service.StatusStopped {
						break
					}
				}
			}
			done <- true
			commonutil.ShowSuccess("Service stopped")
		}

		// Uninstall existing service
		commonutil.ShowInfo("Uninstalling old service...")
		time.Sleep(500 * time.Millisecond)
		if err := s.Uninstall(); err != nil {
			commonutil.ShowWarning("Service not installed or already removed")
		} else {
			commonutil.ShowSuccess("Service uninstalled")
		}

		// Setup directories
		commonutil.ShowInfo("Setting up directories...")
		time.Sleep(300 * time.Millisecond)
		if err := setupServiceDirectories(); err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to setup service directories: %v", err))
			commonutil.ShowCompletionScreen(false, "Update Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Directories ready")

		// Reinstall service
		commonutil.ShowInfo("Installing updated service...")
		time.Sleep(500 * time.Millisecond)
		err = s.Install()
		if err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to install service: %v", err))
			commonutil.ShowCompletionScreen(false, "Update Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service installed")

		// Start service
		commonutil.ShowInfo("Starting service...")
		time.Sleep(500 * time.Millisecond)
		err = s.Start()
		if err != nil {
			commonutil.ShowError(fmt.Sprintf("Failed to start service: %v", err))
			commonutil.ShowCompletionScreen(false, "Update Failed")
			os.Exit(1)
		}
		commonutil.ShowSuccess("Service started")

		// Show completion screen
		commonutil.ShowCompletionScreen(true, "Service Updated Successfully!")

	case "run":
		// Run as service (called by service manager)
		err = s.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Service run failed: %v\n", err)
			os.Exit(1)
		}

	case "help", "":
		// Show help for service commands
		fmt.Println("PrintMaster Agent - Service Management")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  printmaster-agent --service <command>")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  install    Install PrintMaster Agent as a system service")
		fmt.Println("  uninstall  Remove the PrintMaster Agent service")
		fmt.Println("  start      Start the PrintMaster Agent service")
		fmt.Println("  stop       Stop the PrintMaster Agent service")
		fmt.Println("  restart    Restart the PrintMaster Agent service")
		fmt.Println("  status     Show service status and information")
		fmt.Println("  update     Full reinstall cycle (stop, remove, install, start)")
		fmt.Println("  run        Run as service (used by service manager)")
		fmt.Println("  help       Show this help message")
		fmt.Println()
		fmt.Println("Service Details:")
		fmt.Println("  Name:         PrintMasterAgent")
		fmt.Println("  Display Name: PrintMaster Agent")
		fmt.Println("  Description:  Printer and copier fleet management agent")
		fmt.Println()
		fmt.Println("Platform-Specific Paths:")
		switch runtime.GOOS {
		case "windows":
			fmt.Println("  Data Directory: C:\\ProgramData\\PrintMaster\\")
			fmt.Println("  Log Directory:  C:\\ProgramData\\PrintMaster\\logs\\")
		case "darwin":
			fmt.Println("  Data Directory: /Library/Application Support/PrintMaster/")
			fmt.Println("  Log Directory:  /var/log/printmaster/")
		default: // Linux
			fmt.Println("  Data Directory: /var/lib/printmaster/")
			fmt.Println("  Log Directory:  /var/log/printmaster/")
			fmt.Println("  Config:         /etc/printmaster/")
		}
		fmt.Println()
		fmt.Println("Examples:")
		if runtime.GOOS == "windows" {
			fmt.Println("  # Install and start (requires Administrator)")
			fmt.Println("  .\\printmaster-agent.exe --service install")
			fmt.Println("  .\\printmaster-agent.exe --service start")
			fmt.Println()
			fmt.Println("  # Update running service")
			fmt.Println("  .\\printmaster-agent.exe --service update")
			fmt.Println()
			fmt.Println("  # Check service status")
			fmt.Println("  Get-Service PrintMasterAgent")
		} else {
			fmt.Println("  # Install and start (requires root)")
			fmt.Println("  sudo ./printmaster-agent --service install")
			fmt.Println("  sudo systemctl start PrintMasterAgent")
		}
		fmt.Println()

	default:
		fmt.Fprintf(os.Stderr, "Unknown service command: %s\n", cmd)
		fmt.Println()
		fmt.Println("Valid commands: install, uninstall, start, stop, restart, status, update, run, help")
		fmt.Println("Run 'printmaster-agent --service help' for more information")
		os.Exit(1)
	}
}

// runAsService starts the agent under service manager control
func runAsService() {
	svcConfig := getServiceConfig()
	prg := &program{}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		os.Exit(1)
	}

	err = s.Run()
	if err != nil {
		os.Exit(1)
	}
}

// runInteractive starts the agent in foreground mode (normal operation)
func runInteractive(ctx context.Context, configFlag string) {
	// Initialize SSE hub for real-time UI updates
	sseHub = NewSSEHub()

	// Initialize structured logger (DEBUG level for proxy diagnostics, 1000 entries in buffer)
	// Determine log directory based on whether we're running as a service
	var logDir string
	if !service.Interactive() {
		// Running as service - use platform-specific system directory
		logPath := getServiceLogPath()
		logDir = filepath.Dir(logPath)
	} else {
		logDir = "logs"
	}

	if err := os.MkdirAll(logDir, 0755); err == nil {
		appLogger = logger.New(logger.DEBUG, logDir, 1000)
		// Expose logger globally for scanner/vendor packages
		logger.SetGlobal(appLogger)
		appLogger.SetRotationPolicy(logger.RotationPolicy{
			Enabled:    true,
			MaxSizeMB:  10,
			MaxAgeDays: 7,
			MaxFiles:   5,
		})
		// Disable console output when running as service to avoid flooding syslog/journal.
		// The agent already writes to its own rotated log files in logDir.
		if !service.Interactive() {
			appLogger.SetConsoleOutput(false)
		}
		// Set up SSE broadcasting for log entries
		appLogger.SetOnLogCallback(func(entry logger.LogEntry) {
			if sseHub != nil {
				// Broadcast log entry via SSE
				sseHub.Broadcast(SSEEvent{
					Type: "log_entry",
					Data: map[string]interface{}{
						"timestamp": entry.Timestamp.Format(time.RFC3339),
						"level":     logger.LevelToString(entry.Level),
						"message":   entry.Message,
						"context":   entry.Context,
					},
				})
			}
		})
		defer appLogger.Close()
	} else {
		// Fallback: if log directory creation fails, use a logger with empty directory
		appLogger = logger.New(logger.DEBUG, "", 1000)
		logger.SetGlobal(appLogger)
	}

	if appLogger != nil {
		appLogger.Info("Printer Fleet Agent starting",
			"startup_scan", "disabled",
			"version", Version,
			"build_time", BuildTime,
			"git_commit", GitCommit,
			"build_type", BuildType)
	}

	// Provide the app logger to the agent package so internal logs are structured
	agent.SetLogger(appLogger)

	// Load TOML configuration
	// Try to find config.toml in multiple locations
	// Service mode: ProgramData/agent > ProgramData (legacy)
	// Interactive mode: executable dir > current dir
	var agentConfig *AgentConfig

	isService := !service.Interactive()
	var configPaths []string

	if isService {
		// Running as service - check ProgramData locations only
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = "C:\\ProgramData"
		}
		configPaths = []string{
			filepath.Join(programData, "PrintMaster", "agent", "config.toml"),
			filepath.Join(programData, "PrintMaster", "config.toml"), // Legacy location
		}
	} else {
		// Running interactively - check local locations only
		configPaths = []string{
			filepath.Join(filepath.Dir(os.Args[0]), "config.toml"),
			"config.toml",
		}
	}

	// Resolve config path using shared helper which checks AGENT_CONFIG/AGENT_CONFIG_PATH,
	// generic CONFIG/CONFIG_PATH, then the provided flag value.
	configLoaded := false
	resolved := config.ResolveConfigPath("AGENT", configFlag)
	if resolved != "" {
		if _, statErr := os.Stat(resolved); statErr == nil {
			if cfg, err := LoadAgentConfig(resolved); err == nil {
				agentConfig = cfg
				appLogger.Info("Loaded configuration", "path", resolved)
				configLoaded = true
			} else {
				appLogger.Warn("Config path set but failed to parse", "path", resolved, "error", err)
			}
		} else {
			appLogger.Warn("Config path set but file not found", "path", resolved)
		}
	}

	// If not loaded via env/flag, fall back to default search paths
	for _, cfgPath := range configPaths {
		if configLoaded {
			break
		}
		if cfg, err := LoadAgentConfig(cfgPath); err == nil {
			agentConfig = cfg
			appLogger.Info("Loaded configuration", "path", cfgPath)
			configLoaded = true
			break
		}
	}

	if !configLoaded {
		appLogger.Warn("No config.toml found, using defaults")
		agentConfig = DefaultAgentConfig()
		ApplyEnvironmentOverrides(agentConfig) // Apply env overrides even when no config file
	}
	configEpsonRemoteModeEnabled = agentConfig != nil && agentConfig.EpsonRemoteModeEnabled
	featureflags.SetEpsonRemoteMode(configEpsonRemoteModeEnabled)
	agentAuth = newAgentAuthManager(agentConfig, agentSessions)

	// Always apply environment overrides for database path (supports AGENT_DB_PATH and DB_PATH)
	// even when using default configuration (no config file present).
	config.ApplyDatabaseEnvOverrides(&agentConfig.Database, "AGENT")
	if agentConfig.Database.Path != "" {
		// If env var points to a directory, append default filename (devices.db)
		dbPath := agentConfig.Database.Path
		if strings.HasSuffix(dbPath, string(os.PathSeparator)) || strings.HasSuffix(dbPath, "/") {
			dbPath = filepath.Join(dbPath, "devices.db")
		} else {
			if fi, err := os.Stat(dbPath); err == nil && fi.IsDir() {
				dbPath = filepath.Join(dbPath, "devices.db")
			}
		}

		parent := filepath.Dir(dbPath)
		if err := os.MkdirAll(parent, 0755); err != nil {
			appLogger.Warn("Could not create DB parent directory, falling back", "parent", parent, "error", err)
			agentConfig.Database.Path = ""
		} else {
			// Probe write access
			f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0644)
			if err != nil {
				appLogger.Warn("Cannot write to DB path, falling back", "path", dbPath, "error", err)
				agentConfig.Database.Path = ""
			} else {
				if err := f.Close(); err != nil {
					appLogger.Warn("Failed to close DB probe file", "path", dbPath, "error", err)
				}
				agentConfig.Database.Path = dbPath
				appLogger.Info("Database path overridden by environment", "path", agentConfig.Database.Path)
			}
		}
	}

	// Apply logging level from config
	if level := logger.LevelFromString(agentConfig.Logging.Level); level >= 0 {
		appLogger.SetLevel(level)
		appLogger.Info("Log level set from config", "level", agentConfig.Logging.Level)
	}

	// Initialize device storage
	// Use config-specified path or detect proper data directory for service
	var dbPath string
	var err error
	if agentConfig != nil && agentConfig.Database.Path != "" {
		dbPath = agentConfig.Database.Path
		appLogger.Info("Using configured database path", "path", dbPath)
	} else {
		// Detect if running as service and use appropriate directory
		dataDir, dirErr := config.GetDataDirectory("agent", isService)
		if dirErr != nil {
			appLogger.Warn("Could not get data directory, using in-memory storage", "error", dirErr)
			dbPath = ":memory:"
		} else {
			dbPath = filepath.Join(dataDir, "devices.db")
			appLogger.Info("Using device database", "path", dbPath)
		}
	}

	// Set logger for storage package
	storage.SetLogger(appLogger)

	// Initialize agent config storage first (needed for rotation tracking)
	agentDBPath := filepath.Join(filepath.Dir(dbPath), "agent.db")
	if dbPath == ":memory:" {
		agentDBPath = ":memory:"
	}
	agentConfigStore, err = storage.NewAgentConfigStore(agentDBPath)
	if err != nil {
		appLogger.Error("Failed to initialize agent config storage", "error", err, "path", agentDBPath)
		os.Exit(1)
	}
	defer agentConfigStore.Close()
	appLogger.Info("Agent config database initialized", "path", agentDBPath)
	settingsManager = NewSettingsManager(agentConfigStore)
	applyServerConfigFromStore(agentConfig, agentConfigStore, appLogger)

	// Migration: consolidate legacy dev_settings / developer_settings / security_settings into unified "settings" key
	// Also migrates from old Developer/Security structure to new SNMP/Features/Logging/Web structure.
	// This is idempotent and creates a timestamped backup of legacy data before deleting it.
	func() {
		if agentConfigStore == nil {
			return
		}
		var settings map[string]interface{}
		_ = agentConfigStore.GetConfigValue("settings", &settings)
		if settings == nil {
			settings = map[string]interface{}{}
		}

		migrated := false
		timestamp := time.Now().Format(time.RFC3339)

		// Migrate legacy "developer" section to new structure
		if devRaw, ok := settings["developer"].(map[string]interface{}); ok {
			bkKey := "backup.developer." + timestamp
			_ = agentConfigStore.SetConfigValue(bkKey, devRaw)

			// Extract SNMP settings
			snmp := map[string]interface{}{}
			if v, ok := devRaw["snmp_community"]; ok {
				snmp["community"] = v
			}
			if v, ok := devRaw["snmp_timeout_ms"]; ok {
				snmp["timeout_ms"] = v
			}
			if v, ok := devRaw["snmp_retries"]; ok {
				snmp["retries"] = v
			}
			if len(snmp) > 0 {
				settings["snmp"] = snmp
			}

			// Extract Features settings
			features := map[string]interface{}{}
			if v, ok := devRaw["epson_remote_mode_enabled"]; ok {
				features["epson_remote_mode_enabled"] = v
			}
			if v, ok := devRaw["asset_id_regex"]; ok {
				features["asset_id_regex"] = v
			}
			if len(features) > 0 {
				settings["features"] = features
			}

			// Extract Logging settings
			logging := map[string]interface{}{}
			if v, ok := devRaw["log_level"]; ok {
				logging["level"] = v
			}
			if v, ok := devRaw["dump_parse_debug"]; ok {
				logging["dump_parse_debug"] = v
			}
			if len(logging) > 0 {
				settings["logging"] = logging
			}

			// Move discover_concurrency to discovery
			if v, ok := devRaw["discover_concurrency"]; ok {
				var disc map[string]interface{}
				_ = agentConfigStore.GetConfigValue("discovery_settings", &disc)
				if disc == nil {
					disc = map[string]interface{}{}
				}
				disc["concurrency"] = v
				_ = agentConfigStore.SetConfigValue("discovery_settings", disc)
			}

			delete(settings, "developer")
			migrated = true
			appLogger.Info("Migrated legacy developer settings to new structure", "backup_key", bkKey)
		}

		// Migrate legacy security_settings to web section
		var security map[string]interface{}
		if err := agentConfigStore.GetConfigValue("security_settings", &security); err == nil && security != nil {
			bkKey := "backup.security_settings." + timestamp
			_ = agentConfigStore.SetConfigValue(bkKey, security)

			web := map[string]interface{}{}
			if v, ok := security["enable_http"]; ok {
				web["enable_http"] = v
			}
			if v, ok := security["enable_https"]; ok {
				web["enable_https"] = v
			}
			if v, ok := security["http_port"]; ok {
				web["http_port"] = v
			}
			if v, ok := security["https_port"]; ok {
				web["https_port"] = v
			}
			if v, ok := security["redirect_http_to_https"]; ok {
				web["redirect_http_to_https"] = v
			}
			if v, ok := security["custom_cert_path"]; ok {
				web["custom_cert_path"] = v
			}
			if v, ok := security["custom_key_path"]; ok {
				web["custom_key_path"] = v
			}
			if len(web) > 0 {
				settings["web"] = web
			}

			// Move credentials_enabled to features
			if v, ok := security["credentials_enabled"]; ok {
				feat, _ := settings["features"].(map[string]interface{})
				if feat == nil {
					feat = map[string]interface{}{}
				}
				feat["credentials_enabled"] = v
				settings["features"] = feat
			}

			_ = agentConfigStore.DeleteConfigValue("security_settings")
			migrated = true
			appLogger.Info("Migrated legacy security_settings to web/features", "backup_key", bkKey)
		}

		// Migrate legacy dev_settings if present
		var legacy map[string]interface{}
		if err := agentConfigStore.GetConfigValue("dev_settings", &legacy); err == nil && legacy != nil {
			bkKey := "backup.dev_settings." + timestamp
			_ = agentConfigStore.SetConfigValue(bkKey, legacy)
			_ = agentConfigStore.DeleteConfigValue("dev_settings")
			migrated = true
			appLogger.Info("Cleaned up legacy dev_settings", "backup_key", bkKey)
		}

		// Migrate legacy developer_settings if present
		if err := agentConfigStore.GetConfigValue("developer_settings", &legacy); err == nil && legacy != nil {
			bkKey := "backup.developer_settings." + timestamp
			_ = agentConfigStore.SetConfigValue(bkKey, legacy)
			_ = agentConfigStore.DeleteConfigValue("developer_settings")
			migrated = true
			appLogger.Info("Cleaned up legacy developer_settings", "backup_key", bkKey)
		}

		if migrated {
			_ = agentConfigStore.SetConfigValue("settings", settings)
		}
	}()

	// Prime runtime settings so feature flags reflect stored values before services start.
	loadUnifiedSettings(agentConfigStore)

	// Clean up old database backups (keep 10 most recent)
	if err := storage.CleanupOldBackups(dbPath, 10); err != nil {
		appLogger.Warn("Failed to cleanup old database backups", "error", err)
	}

	// Initialize device storage with config store for rotation tracking
	deviceStore, err = storage.NewSQLiteStoreWithConfig(dbPath, agentConfigStore)
	if err != nil {
		appLogger.Error("Failed to initialize device storage", "error", err, "path", dbPath)
		os.Exit(1)
	}
	defer deviceStore.Close()

	// Load and restore trace tags from config
	var savedTraceTags map[string]bool
	if err := agentConfigStore.GetConfigValue("trace_tags", &savedTraceTags); err == nil && len(savedTraceTags) > 0 {
		appLogger.SetTraceTags(savedTraceTags)
		appLogger.Info("Restored trace tags from config", "count", len(savedTraceTags))
	}

	// Secret key for encrypting local credentials
	dataDir := filepath.Dir(dbPath)
	broadcastServerStatus(agentConfig, dataDir, "initial", true)
	startServerStatusMonitor(ctx, agentConfig, dataDir, 5*time.Second)
	secretPath := filepath.Join(dataDir, "agent_secret.key")
	secretKey, skErr := commonutil.LoadOrCreateKey(secretPath)
	if skErr != nil {
		appLogger.Warn("Could not prepare local secret key", "error", skErr, "path", secretPath)
	} else {
		appLogger.Debug("Secret key loaded", "path", secretPath)
	}

	// Helpers for WebUI credential storage
	type credRecord struct {
		Username  string `json:"username"`
		Password  string `json:"password_enc"` // encrypted base64 (local) or plaintext (from server)
		AuthType  string `json:"auth_type"`    // "basic" | "form"
		AutoLogin bool   `json:"auto_login"`
	}

	// getCreds fetches device credentials. When connected to a server, credentials are
	// fetched from the server (stateless agent model). When standalone, local storage is used.
	getCreds := func(serial string) (*credRecord, error) {
		// Try server first if connected (agents should be stateless when server-controlled)
		uploadWorkerMu.RLock()
		worker := uploadWorker
		uploadWorkerMu.RUnlock()

		if worker != nil {
			if client := worker.Client(); client != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if serverCreds, err := client.GetDeviceCredentials(ctx, serial); err == nil {
					appLogger.Debug("Got credentials from server", "serial", serial, "has_password", serverCreds.Password != "")
					return &credRecord{
						Username:  serverCreds.Username,
						Password:  serverCreds.Password, // plaintext from server (already decrypted)
						AuthType:  serverCreds.AuthType,
						AutoLogin: serverCreds.AutoLogin,
					}, nil
				}
				// Server fetch failed - fall through to local storage for standalone compatibility
				appLogger.Debug("Server credentials fetch failed, trying local", "serial", serial)
			}
		}

		// Fallback to local storage (standalone mode)
		if agentConfigStore == nil {
			return nil, fmt.Errorf("no config store")
		}
		var all map[string]credRecord
		if err := agentConfigStore.GetConfigValue("webui_credentials", &all); err != nil {
			all = map[string]credRecord{}
		}
		c, ok := all[serial]
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		// Decrypt local password
		if c.Password != "" && len(secretKey) == 32 {
			if decrypted, err := commonutil.DecryptFromB64(secretKey, c.Password); err == nil {
				c.Password = decrypted
			}
		}
		return &c, nil
	}

	saveCreds := func(serial string, c credRecord) error {
		if agentConfigStore == nil {
			return fmt.Errorf("no config store")
		}
		var all map[string]credRecord
		if err := agentConfigStore.GetConfigValue("webui_credentials", &all); err != nil || all == nil {
			all = map[string]credRecord{}
		}
		all[serial] = c
		return agentConfigStore.SetConfigValue("webui_credentials", all)
	}

	// Create storage adapter that implements agent.DeviceStorage interface
	storageAdapter := &deviceStorageAdapter{store: deviceStore}
	agent.SetDeviceStorage(storageAdapter)
	appLogger.Info("Device storage connected", "mode", "auto_persist")
	startIdentityRefreshForVersionChange(ctx, agentConfigStore, deviceStore, appLogger)

	// Start garbage collection goroutine
	retentionConfig := agent.GetRetentionConfig()
	go runGarbageCollection(ctx, deviceStore, retentionConfig)

	// Start metrics downsampler goroutine (runs every 6 hours)
	go runMetricsDownsampler(ctx, deviceStore)

	// Auto-discovery management (periodic scanning + optional live discovery methods)
	// Controlled by discovery setting: auto_discover_enabled (bool) - master switch
	// Individual live discovery methods can be enabled/disabled independently
	var (
		autoDiscoverMu       sync.Mutex
		autoDiscoverCancel   context.CancelFunc
		autoDiscoverRunning  bool
		autoDiscoverInterval = 15 * time.Minute // Configurable via settings

		liveMDNSMu      sync.Mutex
		liveMDNSCancel  context.CancelFunc
		liveMDNSRunning bool
		liveMDNSSeen    = map[string]time.Time{}

		liveWSDiscoveryMu      sync.Mutex
		liveWSDiscoveryCancel  context.CancelFunc
		liveWSDiscoveryRunning bool
		liveWSDiscoverySeen    = map[string]time.Time{}

		liveSSDPMu      sync.Mutex
		liveSSDPCancel  context.CancelFunc
		liveSSDPRunning bool
		liveSSDPSeen    = map[string]time.Time{}

		metricsRescanMu       sync.Mutex
		metricsRescanCancel   context.CancelFunc
		metricsRescanRunning  bool
		metricsRescanInterval = 60 * time.Minute // Configurable via settings

		snmpTrapMu      sync.Mutex
		snmpTrapCancel  context.CancelFunc
		snmpTrapRunning bool
		snmpTrapSeen    = map[string]time.Time{}

		llmnrMu      sync.Mutex
		llmnrCancel  context.CancelFunc
		llmnrRunning bool
		llmnrSeen    = map[string]time.Time{}
	)

	// Periodic discovery worker
	startAutoDiscover := func() {
		autoDiscoverMu.Lock()
		defer autoDiscoverMu.Unlock()
		if autoDiscoverRunning {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		autoDiscoverCancel = cancel
		autoDiscoverRunning = true
		appLogger.Info("Auto Discover: starting periodic scanner", "interval", autoDiscoverInterval.String())

		go func() {
			ticker := time.NewTicker(autoDiscoverInterval)
			defer ticker.Stop()

			// Run immediately on start
			runPeriodicScan := func() {
				appLogger.Debug("Auto Discover: running periodic scan")

				// Load discovery settings
				var discoverySettings = map[string]interface{}{
					"subnet_scan":   true,
					"manual_ranges": true,
					"arp_enabled":   true,
					"icmp_enabled":  true,
					"tcp_enabled":   true,
					"snmp_enabled":  true,
					"mdns_enabled":  false,
				}
				if agentConfigStore != nil {
					var stored map[string]interface{}
					if err := agentConfigStore.GetConfigValue("discovery_settings", &stored); err == nil && stored != nil {
						for k, v := range stored {
							discoverySettings[k] = v
						}
					}
				}

				// Get saved ranges
				var ranges []string
				if agentConfigStore != nil {
					savedRanges, _ := agentConfigStore.GetRangesList()
					ranges = savedRanges
				}

				// Build DiscoveryConfig from settings
				discoveryCfg := &agent.DiscoveryConfig{
					ARPEnabled:  discoverySettings["arp_enabled"] == true,
					ICMPEnabled: discoverySettings["icmp_enabled"] == true,
					TCPEnabled:  discoverySettings["tcp_enabled"] == true,
					SNMPEnabled: discoverySettings["snmp_enabled"] == true,
					MDNSEnabled: discoverySettings["mdns_enabled"] == true,
				}

				// Use new scanner for periodic discovery (full mode)
				_, err := Discover(ctx, ranges, "full", discoveryCfg, deviceStore, 50, 10)
				if err != nil && ctx.Err() == nil {
					appLogger.Error("Auto Discover scan error", "error", err, "ranges", len(ranges))
				}
			}
			runPeriodicScan()

			for {
				select {
				case <-ctx.Done():
					autoDiscoverMu.Lock()
					autoDiscoverRunning = false
					autoDiscoverCancel = nil
					autoDiscoverMu.Unlock()
					appLogger.Info("Auto Discover: stopped")
					return
				case <-ticker.C:
					runPeriodicScan()
				}
			}
		}()
	}

	stopAutoDiscover := func() {
		autoDiscoverMu.Lock()
		defer autoDiscoverMu.Unlock()
		if autoDiscoverCancel != nil {
			appLogger.Info("Auto Discover: stopping periodic scanner")
			autoDiscoverCancel()
			autoDiscoverCancel = nil
		}
	}

	// getSNMPTimeoutSeconds returns the configured SNMP timeout in seconds
	getSNMPTimeoutSeconds := func() int {
		scannerConfig.RLock()
		defer scannerConfig.RUnlock()
		timeoutSec := scannerConfig.SNMPTimeoutMs / 1000
		if timeoutSec < 1 {
			timeoutSec = 2 // Minimum 2 seconds
		}
		return timeoutSec
	}

	// handleLiveDiscovery processes a single IP from live discovery (mDNS, SSDP, WS-Discovery)
	// Uses the new scanner to detect and store the device
	handleLiveDiscovery := func(ip string, discoveryMethod string) {
		ctx := context.Background()

		// Check if we already know this IP from a saved device
		// If so, do a quick refresh instead of full detection
		if deviceStore != nil {
			visibleTrue := true
			devices, err := deviceStore.List(ctx, storage.DeviceFilter{
				Visible: &visibleTrue,
			})
			if err == nil {
				for _, device := range devices {
					if device.IP == ip {
						// Known device - liveness confirmed, do quick refresh
						appLogger.Debug(discoveryMethod+": known device liveness confirmed, refreshing",
							"ip", ip, "serial", device.Serial)

						// Perform quick SNMP query to get updated metrics
						pi, err := LiveDiscoveryDetect(ctx, ip, getSNMPTimeoutSeconds())
						if err != nil {
							appLogger.Debug(discoveryMethod+": refresh failed, updating last_seen only",
								"ip", ip, "serial", device.Serial, "error", err)
							// Just update last seen time even if SNMP fails
							device.LastSeen = time.Now()
							deviceStore.Update(ctx, device)
							return
						}

						// Update device with fresh data
						device.LastSeen = time.Now()
						if pi.Serial != "" && pi.Serial == device.Serial {
							// Serials match, update other fields if not locked
							if device.LockedFields == nil {
								device.LockedFields = []storage.FieldLock{}
							}
							isLocked := func(field string) bool {
								for _, lf := range device.LockedFields {
									if strings.EqualFold(lf.Field, field) {
										return true
									}
								}
								return false
							}

							if !isLocked("manufacturer") && pi.Manufacturer != "" {
								device.Manufacturer = pi.Manufacturer
							}
							if !isLocked("model") && pi.Model != "" {
								device.Model = pi.Model
							}
							if !isLocked("hostname") && pi.Hostname != "" {
								device.Hostname = pi.Hostname
							}

							deviceStore.Update(ctx, device)

							// Broadcast SSE update
							sseHub.Broadcast(SSEEvent{
								Type: "device_updated",
								Data: map[string]interface{}{
									"serial":       device.Serial,
									"ip":           ip,
									"manufacturer": device.Manufacturer,
									"model":        device.Model,
									"last_seen":    device.LastSeen.Format(time.RFC3339),
									"method":       discoveryMethod,
								},
							})
						}
						return
					}
				}
			}
		}

		// Not a known device - do full detection
		// Use new scanner for live discovery detection
		pi, err := LiveDiscoveryDetect(ctx, ip, getSNMPTimeoutSeconds())
		if err != nil {
			appLogger.WarnRateLimited(discoveryMethod+"_detect_"+ip, 5*time.Minute,
				discoveryMethod+" detection failed", "ip", ip, "error", err)
			// Don't store device without serial - it will just create errors
			return
		}

		// If lightweight query didn't get a serial, try a full deep scan
		// We already have proof of life from live discovery, so it's worth the extra query
		if pi.Serial == "" {
			appLogger.Debug(discoveryMethod+": no serial from quick scan, trying deep scan",
				"ip", ip, "manufacturer", pi.Manufacturer, "model", pi.Model)

			deepPi, deepErr := LiveDiscoveryDeepScan(ctx, ip, 30)
			if deepErr != nil {
				appLogger.Debug(discoveryMethod+": deep scan failed",
					"ip", ip, "error", deepErr)
				return
			}

			// Use deep scan result if it has a serial
			if deepPi != nil && deepPi.Serial != "" {
				pi = deepPi
				appLogger.Info(discoveryMethod+": deep scan found device",
					"ip", ip, "serial", pi.Serial, "manufacturer", pi.Manufacturer, "model", pi.Model)
			} else {
				appLogger.Debug(discoveryMethod+": deep scan completed but no serial found",
					"ip", ip)
				return
			}
		}

		// Add discovery method
		pi.DiscoveryMethods = append(pi.DiscoveryMethods, discoveryMethod)

		// Check if this is a known device
		if pi.Serial != "" {
			existing, err := deviceStore.Get(ctx, pi.Serial)
			if err == nil && existing != nil {
				// Known device - broadcast SSE update immediately
				existing.LastSeen = time.Now()
				existing.IP = ip
				if updateErr := deviceStore.Update(ctx, existing); updateErr == nil {
					sseHub.Broadcast(SSEEvent{
						Type: "device_updated",
						Data: map[string]interface{}{
							"serial":       pi.Serial,
							"ip":           ip,
							"manufacturer": pi.Manufacturer,
							"model":        pi.Model,
							"last_seen":    existing.LastSeen.Format(time.RFC3339),
							"method":       discoveryMethod,
						},
					})
					appLogger.Debug(discoveryMethod+": known device updated",
						"ip", ip, "serial", pi.Serial)
				}
			} else {
				// New device - broadcast discovery event
				sseHub.Broadcast(SSEEvent{
					Type: "device_discovered",
					Data: map[string]interface{}{
						"ip":           ip,
						"serial":       pi.Serial,
						"manufacturer": pi.Manufacturer,
						"model":        pi.Model,
						"method":       discoveryMethod,
					},
				})
				appLogger.Debug(discoveryMethod+": new device discovered",
					"ip", ip, "serial", pi.Serial)
			}
		}

		// Store/update the device
		agent.UpsertDiscoveredPrinter(*pi)
	}

	// Live mDNS discovery worker (only works when auto discover is enabled)
	startLiveMDNS := func() {
		liveMDNSMu.Lock()
		defer liveMDNSMu.Unlock()
		if liveMDNSRunning {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		liveMDNSCancel = cancel
		liveMDNSRunning = true
		appLogger.Info("Live mDNS discovery: starting background browser")

		go func() {
			h := func(ip string) bool {
				ip = strings.TrimSpace(ip)
				if ip == "" {
					return false
				}
				liveMDNSMu.Lock()
				last, ok := liveMDNSSeen[ip]
				if ok && time.Since(last) < 10*time.Minute {
					liveMDNSMu.Unlock()
					return false
				}
				liveMDNSSeen[ip] = time.Now()
				liveMDNSMu.Unlock()
				agent.AppendScanEvent("LIVE MDNS: discovered " + ip)

				// Call LiveDiscoveryDetect directly
				go handleLiveDiscovery(ip, "mdns")
				return true
			}
			agent.StartMDNSBrowser(ctx, h)
			liveMDNSMu.Lock()
			liveMDNSRunning = false
			liveMDNSCancel = nil
			liveMDNSMu.Unlock()
			appLogger.Info("Live mDNS discovery: stopped")
		}()
	}

	stopLiveMDNS := func() {
		liveMDNSMu.Lock()
		defer liveMDNSMu.Unlock()
		if liveMDNSCancel != nil {
			appLogger.Info("Live mDNS discovery: stopping background browser")
			liveMDNSCancel()
			liveMDNSCancel = nil
		}
	}

	// Live WS-Discovery worker (Windows network printer discovery)
	startLiveWSDiscovery := func() {
		liveWSDiscoveryMu.Lock()
		defer liveWSDiscoveryMu.Unlock()
		if liveWSDiscoveryRunning {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		liveWSDiscoveryCancel = cancel
		liveWSDiscoveryRunning = true
		appLogger.Info("Live WS-Discovery: starting background listener")

		go func() {
			h := func(ip string) bool {
				ip = strings.TrimSpace(ip)
				if ip == "" {
					return false
				}
				liveWSDiscoveryMu.Lock()
				last, ok := liveWSDiscoverySeen[ip]
				if ok && time.Since(last) < 10*time.Minute {
					liveWSDiscoveryMu.Unlock()
					return false
				}
				liveWSDiscoverySeen[ip] = time.Now()
				liveWSDiscoveryMu.Unlock()
				agent.AppendScanEvent("LIVE WS-DISCOVERY: discovered " + ip)

				// Call LiveDiscoveryDetect directly
				go handleLiveDiscovery(ip, "wsdiscovery")
				return true
			}
			agent.StartWSDiscoveryBrowser(ctx, h)
			liveWSDiscoveryMu.Lock()
			liveWSDiscoveryRunning = false
			liveWSDiscoveryCancel = nil
			liveWSDiscoveryMu.Unlock()
			appLogger.Info("Live WS-Discovery: stopped")
		}()
	}

	stopLiveWSDiscovery := func() {
		liveWSDiscoveryMu.Lock()
		defer liveWSDiscoveryMu.Unlock()
		if liveWSDiscoveryCancel != nil {
			appLogger.Info("Live WS-Discovery: stopping background listener")
			liveWSDiscoveryCancel()
			liveWSDiscoveryCancel = nil
		}
	}

	// Live SSDP/UPnP discovery worker
	startLiveSSDP := func() {
		liveSSDPMu.Lock()
		defer liveSSDPMu.Unlock()
		if liveSSDPRunning {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		liveSSDPCancel = cancel
		liveSSDPRunning = true
		appLogger.Info("Live SSDP: starting background listener")

		go func() {
			h := func(ip string) bool {
				ip = strings.TrimSpace(ip)
				if ip == "" {
					return false
				}
				liveSSDPMu.Lock()
				last, ok := liveSSDPSeen[ip]
				if ok && time.Since(last) < 10*time.Minute {
					liveSSDPMu.Unlock()
					return false
				}
				liveSSDPSeen[ip] = time.Now()
				liveSSDPMu.Unlock()
				agent.AppendScanEvent("LIVE SSDP: discovered " + ip)

				// Call LiveDiscoveryDetect directly
				go handleLiveDiscovery(ip, "ssdp")
				return true
			}
			agent.StartSSDPBrowser(ctx, h)
			liveSSDPMu.Lock()
			liveSSDPRunning = false
			liveSSDPCancel = nil
			liveSSDPMu.Unlock()
			appLogger.Info("Live SSDP: stopped")
		}()
	}

	stopLiveSSDP := func() {
		liveSSDPMu.Lock()
		defer liveSSDPMu.Unlock()
		if liveSSDPCancel != nil {
			appLogger.Info("Live SSDP: stopping background listener")
			liveSSDPCancel()
			liveSSDPCancel = nil
		}
	}

	// SNMP Trap Listener: Event-driven discovery via trap notifications
	startSNMPTrap := func() {
		snmpTrapMu.Lock()
		defer snmpTrapMu.Unlock()

		if snmpTrapRunning {
			appLogger.Debug("SNMP Trap listener already running")
			return
		}

		ctx, cancel := context.WithCancel(context.Background())
		snmpTrapCancel = cancel
		snmpTrapRunning = true

		appLogger.Info("SNMP Trap: starting listener", "port", 162, "requires_admin", true)

		go func() {
			h := func(ip string) bool {
				// Async SNMP enrichment + metrics collection
				go func(ip string) {
					// Use new scanner for trap handling
					ctx := context.Background()
					pi, err := LiveDiscoveryDetect(ctx, ip, getSNMPTimeoutSeconds())
					if err != nil {
						appLogger.WarnRateLimited("trap_enrich_"+ip, 5*time.Minute, "SNMP Trap: enrichment failed", "ip", ip, "error", err)
						return
					}

					serial := pi.Serial
					if serial == "" {
						appLogger.Debug("SNMP Trap: no serial found for device", "ip", ip)
						return
					}

					// Check if device exists in DB
					existing, err := deviceStore.Get(ctx, serial)
					if err == nil && existing != nil {
						// Known device - update LastSeen
						existing.LastSeen = time.Now()
						existing.IP = ip
						if updateErr := deviceStore.Update(ctx, existing); updateErr == nil {
							appLogger.Debug("SNMP Trap: known device updated", "ip", ip, "serial", serial)
						}
					} else {
						// New device
						appLogger.Debug("SNMP Trap: new device discovered", "ip", ip, "serial", serial)
					}

					// Store/update the device
					agent.UpsertDiscoveredPrinter(*pi)
					appLogger.Info("SNMP Trap: discovered device", "ip", ip, "serial", serial)

					// If metrics monitoring is enabled and device is saved, collect metrics immediately
					metricsRescanMu.Lock()
					metricsEnabled := metricsRescanRunning
					metricsRescanMu.Unlock()

					if metricsEnabled && deviceStore != nil && serial != "" {
						// Check if device is saved
						ctx := context.Background()
						device, err := deviceStore.Get(ctx, serial)
						if err == nil && device != nil && device.IsSaved {
							// Extract learned OIDs from device for efficient metrics collection
							pi := storage.DeviceToPrinterInfo(device)
							learnedOIDs := &pi.LearnedOIDs

							// Collect metrics for this device using learned OIDs if available
							agentSnapshot, err := CollectMetricsWithOIDs(ctx, ip, serial, device.Manufacturer, 10, learnedOIDs)
							if err != nil {
								appLogger.WarnRateLimited("trap_metrics_"+serial, 5*time.Minute, "SNMP Trap: metrics collection failed", "serial", serial, "error", err)
							} else {
								// Convert to storage format
								storageSnapshot := &storage.MetricsSnapshot{}
								storageSnapshot.Serial = agentSnapshot.Serial
								storageSnapshot.Timestamp = time.Now()
								storageSnapshot.PageCount = agentSnapshot.PageCount
								storageSnapshot.ColorPages = agentSnapshot.ColorPages
								storageSnapshot.MonoPages = agentSnapshot.MonoPages
								storageSnapshot.ScanCount = agentSnapshot.ScanCount
								storageSnapshot.TonerLevels = agentSnapshot.TonerLevels
								storageSnapshot.FaxPages = agentSnapshot.FaxPages
								storageSnapshot.CopyPages = agentSnapshot.CopyPages
								storageSnapshot.OtherPages = agentSnapshot.OtherPages
								storageSnapshot.CopyMonoPages = agentSnapshot.CopyMonoPages
								storageSnapshot.CopyFlatbedScans = agentSnapshot.CopyFlatbedScans
								storageSnapshot.CopyADFScans = agentSnapshot.CopyADFScans
								storageSnapshot.FaxFlatbedScans = agentSnapshot.FaxFlatbedScans
								storageSnapshot.FaxADFScans = agentSnapshot.FaxADFScans
								storageSnapshot.ScanToHostFlatbed = agentSnapshot.ScanToHostFlatbed
								storageSnapshot.ScanToHostADF = agentSnapshot.ScanToHostADF
								storageSnapshot.DuplexSheets = agentSnapshot.DuplexSheets
								storageSnapshot.JamEvents = agentSnapshot.JamEvents
								storageSnapshot.ScannerJamEvents = agentSnapshot.ScannerJamEvents

								// Save to database (error already logged in storage layer)
								if err := deviceStore.SaveMetricsSnapshot(ctx, storageSnapshot); err == nil {
									appLogger.Debug("SNMP Trap: collected metrics", "serial", serial)
								}
							}
						}
					}
				}(ip)
				return true
			} // Call browser with 10-minute throttle window
			agent.StartSNMPTrapBrowser(ctx, h, snmpTrapSeen, 10*time.Minute)

			snmpTrapMu.Lock()
			snmpTrapRunning = false
			snmpTrapCancel = nil
			snmpTrapMu.Unlock()
			appLogger.Info("SNMP Trap: stopped")
		}()
	}

	stopSNMPTrap := func() {
		snmpTrapMu.Lock()
		defer snmpTrapMu.Unlock()
		if snmpTrapCancel != nil {
			appLogger.Info("SNMP Trap: stopping listener")
			snmpTrapCancel()
			snmpTrapCancel = nil
		}
	}

	// LLMNR: Windows hostname resolution for printer discovery
	startLLMNR := func() {
		llmnrMu.Lock()
		defer llmnrMu.Unlock()

		if llmnrRunning {
			appLogger.Debug("LLMNR listener already running")
			return
		}

		ctx, cancel := context.WithCancel(context.Background())
		llmnrCancel = cancel
		llmnrRunning = true

		appLogger.Info("LLMNR: starting listener")

		go func() {
			h := func(job scanner.ScanJob) bool {
				ip := job.IP
				hostname := ""
				if job.Meta != nil {
					if meta, ok := job.Meta.(map[string]interface{}); ok {
						if hn, ok := meta["hostname"].(string); ok {
							hostname = hn
						}
					}
				}

				// Async SNMP enrichment
				go func(ip, hostname string) {
					ctx := context.Background()
					pi, err := LiveDiscoveryDetect(ctx, ip, getSNMPTimeoutSeconds())
					if err != nil {
						appLogger.WarnRateLimited("llmnr_enrich_"+ip, 5*time.Minute, "LLMNR: enrichment failed", "ip", ip, "hostname", hostname, "error", err)
					} else {
						agent.UpsertDiscoveredPrinter(*pi)
						appLogger.Info("LLMNR: discovered device", "ip", ip, "hostname", hostname, "serial", pi.Serial)
					}
				}(ip, hostname)
				return true
			}

			// Call browser with 10-minute throttle window
			agent.StartLLMNRBrowser(ctx, h, llmnrSeen, 10*time.Minute)

			llmnrMu.Lock()
			llmnrRunning = false
			llmnrCancel = nil
			llmnrMu.Unlock()
			appLogger.Info("LLMNR: stopped")
		}()
	}

	stopLLMNR := func() {
		llmnrMu.Lock()
		defer llmnrMu.Unlock()
		if llmnrCancel != nil {
			appLogger.Info("LLMNR: stopping listener")
			llmnrCancel()
			llmnrCancel = nil
		}
	}

	// Declare collectMetricsForSavedDevices first so it can be used in startMetricsRescan
	var collectMetricsForSavedDevices func()

	// Metrics Rescan: Periodically collect metrics from saved devices
	// intervalMinutes: legacy minutes-based interval (min 1, max 1440)
	// intervalSeconds: sub-minute interval (min 15, max 300) - takes precedence if > 0
	startMetricsRescan := func(intervalMinutes, intervalSeconds int) {
		metricsRescanMu.Lock()
		defer metricsRescanMu.Unlock()

		if metricsRescanRunning {
			appLogger.Debug("Metrics rescan already running")
			return
		}

		var interval time.Duration
		if intervalSeconds > 0 {
			// Use seconds-based interval for sub-minute precision
			if intervalSeconds < 15 {
				intervalSeconds = 15 // minimum 15 seconds
			}
			if intervalSeconds > 300 {
				intervalSeconds = 300 // maximum 5 minutes for seconds mode
			}
			interval = time.Duration(intervalSeconds) * time.Second
			appLogger.Info("Metrics rescan: starting with sub-minute interval", "interval_seconds", intervalSeconds)
		} else {
			// Use minutes-based interval
			if intervalMinutes < 1 {
				intervalMinutes = 1 // minimum 1 minute
			}
			if intervalMinutes > 1440 {
				intervalMinutes = 1440 // maximum 24 hours
			}
			interval = time.Duration(intervalMinutes) * time.Minute
			appLogger.Info("Metrics rescan: starting", "interval_minutes", intervalMinutes)
		}

		metricsRescanInterval = interval
		ctx, cancel := context.WithCancel(context.Background())
		metricsRescanCancel = cancel
		metricsRescanRunning = true

		appLogger.Info("Metrics rescan: starting", "interval_minutes", intervalMinutes)

		go func() {
			defer func() {
				metricsRescanMu.Lock()
				metricsRescanRunning = false
				metricsRescanCancel = nil
				metricsRescanMu.Unlock()
			}()

			// Run immediately on start
			collectMetricsForSavedDevices()

			ticker := time.NewTicker(metricsRescanInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					appLogger.Info("Metrics rescan: stopped")
					return
				case <-ticker.C:
					collectMetricsForSavedDevices()
				}
			}
		}()
	}

	stopMetricsRescan := func() {
		metricsRescanMu.Lock()
		defer metricsRescanMu.Unlock()
		if metricsRescanCancel != nil {
			appLogger.Info("Metrics rescan: stopping")
			metricsRescanCancel()
			metricsRescanCancel = nil
		}
	}

	// Define the collection function
	// Collect metrics from ALL devices (saved + discovered) for tiered storage
	collectMetricsForSavedDevices = func() {
		appLogger.Debug("Metrics rescan: collecting snapshots from all devices")
		ctx := context.Background()

		// Get all devices (no IsSaved filter - collect from discovered devices too)
		devices, err := deviceStore.List(ctx, storage.DeviceFilter{})
		if err != nil {
			appLogger.Error("Metrics rescan: failed to list devices", "error", err)
			return
		}

		count := 0
		for _, device := range devices {
			// Extract learned OIDs from device for efficient metrics collection
			pi := storage.DeviceToPrinterInfo(device)
			learnedOIDs := &pi.LearnedOIDs

			// Collect metrics snapshot using learned OIDs if available
			agentSnapshot, err := CollectMetricsWithOIDs(ctx, device.IP, device.Serial, device.Manufacturer, 10, learnedOIDs)
			if err != nil {
				appLogger.WarnRateLimited("metrics_collect_"+device.Serial, 5*time.Minute, "Metrics rescan: collection failed", "serial", device.Serial, "ip", device.IP, "error", err)
				continue
			}

			// Convert to storage type
			storageSnapshot := &storage.MetricsSnapshot{}
			storageSnapshot.Serial = agentSnapshot.Serial
			storageSnapshot.PageCount = agentSnapshot.PageCount
			storageSnapshot.ColorPages = agentSnapshot.ColorPages
			storageSnapshot.MonoPages = agentSnapshot.MonoPages
			storageSnapshot.ScanCount = agentSnapshot.ScanCount
			storageSnapshot.TonerLevels = agentSnapshot.TonerLevels
			storageSnapshot.FaxPages = agentSnapshot.FaxPages
			storageSnapshot.CopyPages = agentSnapshot.CopyPages
			storageSnapshot.OtherPages = agentSnapshot.OtherPages
			storageSnapshot.CopyMonoPages = agentSnapshot.CopyMonoPages
			storageSnapshot.CopyFlatbedScans = agentSnapshot.CopyFlatbedScans
			storageSnapshot.CopyADFScans = agentSnapshot.CopyADFScans
			storageSnapshot.FaxFlatbedScans = agentSnapshot.FaxFlatbedScans
			storageSnapshot.FaxADFScans = agentSnapshot.FaxADFScans
			storageSnapshot.ScanToHostFlatbed = agentSnapshot.ScanToHostFlatbed
			storageSnapshot.ScanToHostADF = agentSnapshot.ScanToHostADF
			storageSnapshot.DuplexSheets = agentSnapshot.DuplexSheets
			storageSnapshot.JamEvents = agentSnapshot.JamEvents
			storageSnapshot.ScannerJamEvents = agentSnapshot.ScannerJamEvents

			// Save to database (error already logged in storage layer)
			if err := deviceStore.SaveMetricsSnapshot(ctx, storageSnapshot); err != nil {
				continue
			}

			count++
		}

		appLogger.Info("Metrics rescan: completed", "device_count", count)
	}

	// Helpers to apply runtime effects for settings (closures to access local start/stop functions)
	applyDiscoveryEffects := func(req map[string]interface{}) {
		if req == nil {
			return
		}
		autoDiscoverEnabled := false
		if v, ok := req["auto_discover_enabled"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				autoDiscoverEnabled = vb
				if vb {
					startAutoDiscover()
					appLogger.Info("Auto Discover enabled via settings")
				} else {
					stopAutoDiscover()
					stopLiveMDNS()
					stopLiveWSDiscovery()
					stopLiveSSDP()
					stopSNMPTrap()
					stopLLMNR()
					appLogger.Info("Auto Discover disabled via settings")
				}
			}
		}

		// Master IP scanning toggle (controls subnet/manual IP scanning)
		if v, ok := req["ip_scanning_enabled"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if !vb {
					// Stop any periodic per-IP scanning
					stopAutoDiscover()
					appLogger.Info("IP scanning disabled via settings: periodic and manual per-IP scans will be blocked")
				} else {
					// If enabling, only start auto-discover if auto_discover_enabled is true in the provided map
					if ad, ok := req["auto_discover_enabled"]; ok {
						if adb, ok2 := ad.(bool); ok2 && adb {
							startAutoDiscover()
							appLogger.Info("IP scanning enabled via settings: starting periodic scans")
						}
					} else {
						// No auto_discover change provided; do not automatically start periodic scans here
						appLogger.Info("IP scanning enabled via settings")
					}
				}
			}
		}
		if v, ok := req["auto_discover_live_mdns"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb && autoDiscoverEnabled {
					startLiveMDNS()
					appLogger.Info("Live mDNS discovery enabled via settings")
				} else {
					stopLiveMDNS()
					appLogger.Info("Live mDNS discovery disabled via settings")
				}
			}
		}
		if v, ok := req["auto_discover_live_wsd"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb && autoDiscoverEnabled {
					startLiveWSDiscovery()
					appLogger.Info("Live WS-Discovery enabled via settings")
				} else {
					stopLiveWSDiscovery()
					appLogger.Info("Live WS-Discovery disabled via settings")
				}
			}
		}
		if v, ok := req["auto_discover_live_ssdp"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb && autoDiscoverEnabled {
					startLiveSSDP()
					appLogger.Info("Live SSDP discovery enabled via settings")
				} else {
					stopLiveSSDP()
					appLogger.Info("Live SSDP discovery disabled via settings")
				}
			}
		}
		if v, ok := req["auto_discover_live_snmptrap"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb && autoDiscoverEnabled {
					startSNMPTrap()
					appLogger.Info("SNMP Trap listener enabled via settings")
				} else {
					stopSNMPTrap()
					appLogger.Info("SNMP Trap listener disabled via settings")
				}
			}
		}
		if v, ok := req["auto_discover_live_llmnr"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb && autoDiscoverEnabled {
					startLLMNR()
					appLogger.Info("LLMNR listener enabled via settings")
				} else {
					stopLLMNR()
					appLogger.Info("LLMNR listener disabled via settings")
				}
			}
		}
		if v, ok := req["metrics_rescan_enabled"]; ok {
			if vb, ok2 := v.(bool); ok2 {
				if vb {
					intervalMinutes := 60
					intervalSeconds := 0
					if iv, ok := req["metrics_rescan_interval_minutes"]; ok {
						if ivf, ok2 := iv.(float64); ok2 {
							intervalMinutes = int(ivf)
						}
					}
					if iv, ok := req["metrics_rescan_interval_seconds"]; ok {
						if ivf, ok2 := iv.(float64); ok2 {
							intervalSeconds = int(ivf)
						}
					}
					startMetricsRescan(intervalMinutes, intervalSeconds)
					if intervalSeconds > 0 {
						appLogger.Info("Metrics monitoring enabled", "interval_seconds", intervalSeconds)
					} else {
						appLogger.Info("Metrics monitoring enabled", "interval_minutes", intervalMinutes)
					}
				} else {
					stopMetricsRescan()
					appLogger.Info("Metrics monitoring disabled")
				}
			}
		}
	}
	applyDiscoveryEffectsFunc = applyDiscoveryEffects
	if settingsManager != nil && settingsManager.HasManagedSnapshot() {
		cfg := loadUnifiedSettings(agentConfigStore)
		applyEffectiveSettingsSnapshot(cfg)
	}

	// Load saved ranges from database
	rangesText, err := agentConfigStore.GetRanges()
	if err != nil {
		appLogger.Error("Failed to load ranges from database", "error", err.Error())
	} else if rangesText != "" {
		appLogger.Info("Loaded saved ranges (preview)")
		// show a short preview
		lines := strings.Split(rangesText, "\n")
		previewLines := len(lines)
		if previewLines > 5 {
			previewLines = 5
		}
		for i := 0; i < previewLines; i++ {
			appLogger.Debug("Range preview", "line", strings.TrimSpace(lines[i]))
		}
		// Validate the ranges
		res, err := agent.ParseRangeText(rangesText, 4096)
		if err != nil {
			appLogger.Error("Failed to parse saved ranges", "error", err.Error())
		} else if len(res.Errors) > 0 {
			for _, pe := range res.Errors {
				appLogger.Warn("Saved range parse error", "line", pe.Line, "error", pe.Msg)
			}
		} else {
			appLogger.Info("Validated saved addresses", "count", len(res.IPs))
		}
	}

	// Apply configuration from TOML
	if agentConfig != nil {
		// Apply asset ID regex from config
		if agentConfig.AssetIDRegex != "" {
			agent.SetAssetIDRegex(agentConfig.AssetIDRegex)
			appLogger.Info("AssetIDRegex configured from TOML", "pattern", agentConfig.AssetIDRegex)
		} else {
			// reasonable default: five digit numeric asset tags
			agent.SetAssetIDRegex(`\b\d{5}\b`)
			appLogger.Info("Using default AssetIDRegex", "pattern", "five-digit")
		}

		// Apply SNMP settings to environment for sub-packages
		if agentConfig.SNMP.Version != "" {
			_ = os.Setenv("SNMP_VERSION", agentConfig.SNMP.Version)
		}
		if agentConfig.SNMP.Community != "" {
			_ = os.Setenv("SNMP_COMMUNITY", agentConfig.SNMP.Community)
		}
		// SNMPv3 settings
		if agentConfig.SNMP.SecurityLevel != "" {
			_ = os.Setenv("SNMP_SECURITY_LEVEL", agentConfig.SNMP.SecurityLevel)
		}
		if agentConfig.SNMP.Username != "" {
			_ = os.Setenv("SNMP_USERNAME", agentConfig.SNMP.Username)
		}
		if agentConfig.SNMP.AuthProtocol != "" {
			_ = os.Setenv("SNMP_AUTH_PROTOCOL", agentConfig.SNMP.AuthProtocol)
		}
		if agentConfig.SNMP.AuthPassword != "" {
			_ = os.Setenv("SNMP_AUTH_PASSWORD", agentConfig.SNMP.AuthPassword)
		}
		if agentConfig.SNMP.PrivProtocol != "" {
			_ = os.Setenv("SNMP_PRIV_PROTOCOL", agentConfig.SNMP.PrivProtocol)
		}
		if agentConfig.SNMP.PrivPassword != "" {
			_ = os.Setenv("SNMP_PRIV_PASSWORD", agentConfig.SNMP.PrivPassword)
		}
		if agentConfig.SNMP.ContextName != "" {
			_ = os.Setenv("SNMP_CONTEXT_NAME", agentConfig.SNMP.ContextName)
		}
		appLogger.Info("SNMP configured from TOML",
			"version", agentConfig.SNMP.Version,
			"has_community", agentConfig.SNMP.Community != "",
			"has_v3_user", agentConfig.SNMP.Username != "")

		// Apply SNMP timeout and retries settings
		scannerConfig.Lock()
		scannerConfig.SNMPTimeoutMs = agentConfig.SNMP.TimeoutMs
		scannerConfig.SNMPRetries = agentConfig.SNMP.Retries
		scannerConfig.DiscoverConcurrency = agentConfig.Concurrency
		appLogger.Info("Scanner config applied from TOML",
			"timeout_ms", scannerConfig.SNMPTimeoutMs,
			"retries", scannerConfig.SNMPRetries,
			"concurrency", scannerConfig.DiscoverConcurrency)
		scannerConfig.Unlock()
	}

	// Load server configuration from TOML and start upload worker
	if agentConfig != nil && agentConfig.Server.Enabled {
		dataDir, err := config.GetDataDirectory("agent", isService)
		if err != nil {
			appLogger.Error("Failed to get data directory", "error", err)
			return
		}

		go func() {
			worker, err := startServerUploadWorker(ctx, agentConfig, dataDir, deviceStore, settingsManager, appLogger)
			if err != nil {
				appLogger.Error("Failed to start upload worker", "error", err)
				return
			}
			uploadWorkerMu.Lock()
			uploadWorker = worker
			uploadWorkerMu.Unlock()

			// Check if local proxy handler was set while we were starting up
			// This handles the race condition where web server starts before upload worker finishes
			if h := getLocalProxyHandler(); h != nil {
				worker.SetLocalHandler(h)
			}

			// Start auto-update worker after upload worker is ready
			go initAutoUpdateWorker(ctx, agentConfig, dataDir, isService, appLogger)
		}()
	}

	// Load discovery settings from database (user-configurable via web UI)
	{
		var discoverySettings map[string]interface{}
		if agentConfigStore != nil {
			_ = agentConfigStore.GetConfigValue("discovery_settings", &discoverySettings)
		}
		if discoverySettings != nil {
			applyDiscoveryEffects(discoverySettings)
		}
	}

	// Start only after SNMP/environment settings are initialized. Reachability
	// is independent of optional heavy metrics collection and auto-discovery.
	stopLiveness := startDeviceLivenessMonitor(ctx, deviceStore.(deviceLivenessStore), func() bool {
		base := pmsettings.DefaultSettings()
		managed := false
		if settingsManager != nil {
			base, managed = settingsManager.baseSettings()
		}
		if !managed {
			var discovery map[string]interface{}
			if err := agentConfigStore.GetConfigValue("discovery_settings", &discovery); err != nil {
				return false
			}
			mapIntoStruct(discovery, &base.Discovery)
		}
		return base.Discovery.IPScanningEnabled && base.Discovery.SNMPEnabled
	})
	defer stopLiveness()

	// Ensure key handlers are registered (register sandbox explicitly so it's
	// always present regardless of init ordering in other files). Use a
	// Start web UI

	// Lightweight health endpoint for Docker/monitoring (public).
	http.HandleFunc("/health", handleHealth)

	// Serve the UI only for the exact root path and GET method. This prevents
	// the UI HTML from being returned as a fallback for other endpoints (e.g.
	// POST /sandbox_simulate) when a handler is missing or not registered.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "only GET allowed", http.StatusMethodNotAllowed)
			return
		}
		// Serve the HTML from embedded filesystem
		tmpl, err := template.ParseFS(webFS, "web/index.html")
		if err != nil {
			http.Error(w, "Template error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmpl.Execute(w, nil)

	})

	// Serve static assets (CSS, JS) from embedded filesystem
	http.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		// Strip /static/ prefix to get the filename
		fileName := strings.TrimPrefix(r.URL.Path, "/static/")

		// Serve shared assets from common/web package
		if fileName == "shared.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Write([]byte(sharedweb.SharedCSS))
			return
		}
		if fileName == "shared.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write([]byte(sharedweb.SharedJS))
			return
		}
		if fileName == "metrics.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write([]byte(sharedweb.MetricsJS))
			return
		}
		if fileName == "cards.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write([]byte(sharedweb.CardsJS))
			return
		}
		if fileName == "report.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write([]byte(sharedweb.ReportJS))
			return
		}
		// Serve vendored flatpickr files from the embedded common/web package so
		// they are served with correct MIME types and avoid CDN/CSP issues.
		if fileName == "flatpickr/flatpickr.min.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write([]byte(sharedweb.FlatpickrJS))
			return
		}
		if fileName == "flatpickr/flatpickr.min.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Write([]byte(sharedweb.FlatpickrCSS))
			return
		}
		if fileName == "flatpickr/LICENSE.md" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write([]byte(sharedweb.FlatpickrLicense))
			return
		}

		// Serve other files from embedded filesystem
		filePath := "web/" + fileName
		content, err := webFS.ReadFile(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// Set appropriate content type
		if strings.HasSuffix(filePath, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else if strings.HasSuffix(filePath, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}

		w.Write(content)
	})

	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "only GET allowed", http.StatusMethodNotAllowed)
			return
		}
		if data, err := webFS.ReadFile("web/login.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><html><head><title>Login</title></head><body><h1>Authentication Required</h1><p>The agent login interface has not been installed. Please access this agent through the central server or install the latest web assets.</p></body></html>")
	})

	// Helper function to create bool pointer
	boolPtr := func(b bool) *bool {
		return &b
	}

	// SSE endpoint for real-time UI updates
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Create client and register with hub
		client := sseHub.NewClient()
		defer sseHub.RemoveClient(client)

		// Send initial connection event
		fmt.Fprintf(w, "event: connected\ndata: {\"message\":\"Connected to event stream\"}\n\n")
		flusher.Flush()

		// Send periodic keepalive comments to prevent idle timeouts in proxies
		// and intermediaries that may close connections when no data flows.
		// The comment format ": <text>\n\n" is ignored by EventSource but keeps the TCP/TLS
		// session active. Use a 20s interval which is commonly safe.
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()

		// Stream events to client
		for {
			select {
			case event := <-client.events:
				// Marshal event data
				data, err := json.Marshal(event.Data)
				if err != nil {
					continue
				}

				// Send SSE formatted event
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, string(data))
				flusher.Flush()

			case <-ticker.C:
				// Keepalive comment for EventSource; ignored by client but prevents
				// idle connection timeouts in proxies and network middleboxes.
				// Format: comment line starting with ':' followed by a blank line.
				_, _ = fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case <-r.Context().Done():
				// Client disconnected
				return
			}
		}
	})

	// cancel currently running scan (if any)
	// Discovery endpoint - scans saved IP ranges and/or local subnet using discovery pipeline
	// Respects discovery_settings from database (manual_ranges, subnet_scan, method toggles)
	http.HandleFunc("/discover", func(w http.ResponseWriter, r *http.Request) {
		conc := 50
		timeoutSeconds := 5

		// Check for mode parameter (quick vs full)
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "full" // default to full scan
		}

		// Load discovery settings
		var discoverySettings = map[string]interface{}{
			"subnet_scan":   true,
			"manual_ranges": true,
			"arp_enabled":   true,
			"icmp_enabled":  true,
			"tcp_enabled":   true,
			"snmp_enabled":  true,
			"mdns_enabled":  false,
		}
		if agentConfigStore != nil {
			var stored map[string]interface{}
			if err := agentConfigStore.GetConfigValue("discovery_settings", &stored); err == nil && stored != nil {
				for k, v := range stored {
					discoverySettings[k] = v
				}
			}
		}

		// If IP scanning master toggle is explicitly disabled, skip discovery
		if discoverySettings["ip_scanning_enabled"] == false {
			agent.Info("Discovery skipped: IP scanning disabled in settings")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "discovery skipped: IP scanning is disabled in settings")
			return
		}

		// Get saved ranges from database (if manual ranges enabled)
		var ranges []string
		manualRangesEnabled := discoverySettings["manual_ranges"] == true
		if manualRangesEnabled && agentConfigStore != nil {
			savedRanges, err := agentConfigStore.GetRangesList()
			if err == nil {
				ranges = savedRanges
			}
		}

		// Check if local subnet scanning is enabled
		scanLocalSubnet := discoverySettings["subnet_scan"] == true

		// Determine what to scan
		if len(ranges) > 0 {
			agent.Info(fmt.Sprintf("Starting Discover with %d saved addresses", len(ranges)))
		} else if scanLocalSubnet {
			agent.Info("Starting Auto Discover (local subnet)")
			// Empty ranges will trigger auto subnet detection
		} else {
			agent.Info("Discovery skipped: no saved ranges and subnet scan disabled")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "discovery skipped: enable subnet scanning or configure ranges in Settings")
			return
		}

		// Build DiscoveryConfig from settings
		discoveryCfg := &agent.DiscoveryConfig{
			ARPEnabled:  discoverySettings["arp_enabled"] == true,
			ICMPEnabled: discoverySettings["icmp_enabled"] == true,
			TCPEnabled:  discoverySettings["tcp_enabled"] == true,
			SNMPEnabled: discoverySettings["snmp_enabled"] == true,
			MDNSEnabled: discoverySettings["mdns_enabled"] == true,
		}

		// Build saved device IP map for bypass when detection is disabled
		savedDeviceIPs := make(map[string]bool)
		if deviceStore != nil {
			ctx := context.Background()
			saved := true
			savedDevices, err := deviceStore.List(ctx, storage.DeviceFilter{IsSaved: &saved})
			if err == nil {
				for _, dev := range savedDevices {
					savedDeviceIPs[dev.IP] = true
				}
				if len(savedDeviceIPs) > 0 {
					appLogger.Info("Discovery will bypass detection for saved devices", "count", len(savedDeviceIPs))
				}
			}
		}

		// Use new scanner for all discovery
		ctx := context.Background()
		printers, err := Discover(ctx, ranges, mode, discoveryCfg, deviceStore, conc, timeoutSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(printers)
	})

	// Synchronous discovery endpoint (quick Phase A scan) backed by discover.go
	http.HandleFunc("/discover_now", handleDiscover)

	// Removed /saved_ranges, /ranges, and /clear_ranges in favor of unified /settings

	// GET /devices/discovered - List discovered devices with optional filters
	// Query params:
	//   - minutes: only show devices discovered in last X minutes (default: no filter)
	//   - include_known: include already saved/known devices (default: false)
	http.HandleFunc("/devices/discovered", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Parse query parameters
		minutesStr := r.URL.Query().Get("minutes")
		includeKnown := r.URL.Query().Get("include_known") == "true"

		// Query discovered devices from database
		ctx := context.Background()

		// Build filter
		filter := storage.DeviceFilter{
			Visible: boolPtr(true), // Only visible devices
		}

		// Filter by save status unless include_known is true
		if !includeKnown {
			filter.IsSaved = boolPtr(false) // Only unsaved (new) devices
		}

		// Filter by time if minutes parameter provided
		if minutesStr != "" {
			if minutes, err := strconv.Atoi(minutesStr); err == nil && minutes > 0 {
				cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
				filter.LastSeenAfter = &cutoff
			}
		}

		devices, err := deviceStore.List(ctx, filter)
		if err != nil {
			appLogger.Error("Error listing discovered devices", "error", err.Error())
			json.NewEncoder(w).Encode([]agent.PrinterInfo{})
			return
		}

		// Convert devices to PrinterInfo and enrich with latest metrics
		printers := make([]agent.PrinterInfo, len(devices))
		for i, dev := range devices {
			printers[i] = storage.DeviceToPrinterInfo(dev)

			// Fetch latest metrics for this device
			if dev.Serial != "" {
				if snapshot, err := deviceStore.GetLatestMetrics(ctx, dev.Serial); err == nil && snapshot != nil {
					printers[i].PageCount = snapshot.PageCount
					// Convert TonerLevels from map[string]interface{} to map[string]int
					if snapshot.TonerLevels != nil {
						toner := make(map[string]int)
						for k, v := range snapshot.TonerLevels {
							if level, ok := v.(float64); ok {
								toner[k] = int(level)
							} else if level, ok := v.(int); ok {
								toner[k] = level
							}
						}
						printers[i].TonerLevels = toner
					}
				}
			}
		}

		json.NewEncoder(w).Encode(printers)
	})

	// POST /devices/clear_discovered - Delete discovered devices (hard delete)
	// This endpoint removes devices that are not saved (is_saved = 0) from the local DB.
	http.HandleFunc("/devices/clear_discovered", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		// Delete discovered (unsaved) devices from database
		ctx := context.Background()
		isSaved := false
		filter := storage.DeviceFilter{IsSaved: &isSaved}
		count, err := deviceStore.DeleteAll(ctx, filter)
		if err != nil {
			appLogger.Error("Error deleting discovered devices", "error", err.Error())
			http.Error(w, "failed to delete discovered", http.StatusInternalServerError)
			return
		}
		appLogger.Info("Deleted discovered devices", "count", count)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "deleted %d devices", count)
	})

	// POST /database/clear - Backup current database and start fresh
	http.HandleFunc("/database/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		appLogger.Info("Database clear requested - backing up and resetting")

		// Get the SQLiteStore to call backupAndReset
		if sqliteStore, ok := deviceStore.(*storage.SQLiteStore); ok {
			if err := sqliteStore.BackupAndReset(); err != nil {
				appLogger.Error("Failed to backup and reset database", "error", err)
				http.Error(w, fmt.Sprintf("failed to reset database: %v", err), http.StatusInternalServerError)
				return
			}

			appLogger.Info("Database backed up and reset successfully")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "Database backed up and reset successfully",
				"reload":  true, // Signal UI to reload
			})
		} else {
			http.Error(w, "database type does not support reset", http.StatusBadRequest)
		}
	})

	// Use /devices/get?serial=X for device details by serial
	// Use /devices/list with filters for querying by IP

	// Use /devices/metrics/history for metrics data

	http.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		// Optional query params: level=ERROR|WARN|INFO|DEBUG|TRACE, tail=N
		q := r.URL.Query()
		levelStr := strings.ToUpper(strings.TrimSpace(q.Get("level")))
		tailStr := strings.TrimSpace(q.Get("tail"))

		// Map for level parsing local to this handler
		levelMap := map[string]int{
			"ERROR": 0,
			"WARN":  1,
			"INFO":  2,
			"DEBUG": 3,
			"TRACE": 4,
		}
		minLevel, haveLevel := levelMap[levelStr]

		// Parse tail count
		tail := 0
		if tailStr != "" {
			if n, err := strconv.Atoi(tailStr); err == nil && n > 0 {
				tail = n
			}
		}

		// Get buffered entries from app logger
		entries := appLogger.GetBuffer()

		// Filter by level if requested (include entries with level <= minLevel)
		if haveLevel {
			filtered := make([]logger.LogEntry, 0, len(entries))
			for _, e := range entries {
				if int(e.Level) <= minLevel {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
		}

		// Tail if requested
		if tail > 0 && len(entries) > tail {
			entries = entries[len(entries)-tail:]
		}

		// Write as plain text compatible with prior behavior
		w.Header().Set("Content-Type", "text/plain")
		var b strings.Builder
		for i, e := range entries {
			// Format similar to logger package
			ts := e.Timestamp.Format("2006-01-02T15:04:05-07:00")
			// Best-effort level name mapping
			levelName := "INFO"
			switch e.Level {
			case 0:
				levelName = "ERROR"
			case 1:
				levelName = "WARN"
			case 2:
				levelName = "INFO"
			case 3:
				levelName = "DEBUG"
			case 4:
				levelName = "TRACE"
			}
			b.WriteString(fmt.Sprintf("%s [%s] %s", ts, levelName, e.Message))
			if len(e.Context) > 0 {
				for k, v := range e.Context {
					b.WriteString(fmt.Sprintf(" %s=%v", k, v))
				}
			}
			if i < len(entries)-1 {
				b.WriteString("\n")
			}
		}
		fmt.Fprint(w, b.String())
	})

	// Download a zip archive of the entire logs directory
	http.HandleFunc("/logs/archive", func(w http.ResponseWriter, r *http.Request) {
		logDir := filepath.Join(".", "logs")
		if st, err := os.Stat(logDir); err != nil || !st.IsDir() {
			http.Error(w, "logs directory not found", http.StatusNotFound)
			return
		}
		fname := fmt.Sprintf("logs_%s.zip", time.Now().Format("20060102_150405"))
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fname))

		zw := zip.NewWriter(w)
		defer zw.Close()

		// Walk logs directory and add files
		_ = filepath.Walk(logDir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip problematic entries
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(logDir, p)
			if err != nil {
				rel = info.Name()
			}
			// Normalize to forward slashes for zip entries
			zipName := strings.ReplaceAll(rel, "\\", "/")
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			wtr, err := zw.Create(zipName)
			if err != nil {
				return nil
			}
			_, _ = io.Copy(wtr, f)
			return nil
		})
	})

	// Clear logs by rotating the current log file and clearing the buffer
	http.HandleFunc("/logs/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Force rotation to archive current log and start fresh
		appLogger.ForceRotate()
		// Clear the in-memory buffer
		appLogger.ClearBuffer()
		appLogger.Info("Logs cleared and rotated by user request")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success": true, "message": "Logs cleared and rotated"}`)
	})

	// Endpoint to return unknown manufacturer log entries (if present)
	http.HandleFunc("/unknown_manufacturers", func(w http.ResponseWriter, r *http.Request) {
		logDir := filepath.Join(".", "logs")
		fpath := filepath.Join(logDir, "unknown_mfg.log")
		data, err := os.ReadFile(fpath)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode([]string{})
			return
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lines)
	})

	// Endpoint to fetch parse debug for an IP (returns in-memory snapshot or persisted JSON)
	http.HandleFunc("/parse_debug", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ip := q.Get("ip")
		if ip == "" {
			http.Error(w, "ip parameter required", http.StatusBadRequest)
			return
		}
		// try in-memory snapshot first
		if d, ok := agent.GetParseDebug(ip); ok {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(d)
			return
		}
		// fallback to persisted file
		logDir := filepath.Join(".", "logs")
		fpath := filepath.Join(logDir, fmt.Sprintf("parse_debug_%s.json", strings.ReplaceAll(ip, ".", "_")))
		data, err := os.ReadFile(fpath)
		if err != nil {
			http.Error(w, "no diagnostics found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	// POST /api/report - Submit a device data report to the proxy service
	// This endpoint collects diagnostic data and forwards it to the cloud proxy
	// which creates a GitHub Gist and returns URLs for the pre-filled issue.
	http.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		var req agent.ReportRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}

		// Validate required fields
		if req.IssueType == "" {
			http.Error(w, "issue_type is required", http.StatusBadRequest)
			return
		}

		// Create the report submitter with a salt derived from agent ID
		agentID := agentConfig.Server.AgentID
		submitter := agent.NewReportSubmitter(Version, agentID, appLogger)

		// Get parse debug data if available
		var parseDebug *agent.ParseDebug
		if req.DeviceIP != "" {
			if pd, ok := agent.GetParseDebug(req.DeviceIP); ok {
				parseDebug = &pd
			}
		}

		// If FullWalk is requested, perform a complete SNMP walk to capture all OIDs
		// This helps debug vendor-specific issues where standard OIDs don't work
		if req.FullWalk && req.DeviceIP != "" {
			if appLogger != nil {
				appLogger.Info("Performing full SNMP walk for report", "ip", req.DeviceIP)
			}
			cfg, err := agent.GetSNMPConfig()
			if err == nil {
				client, err := agent.NewSNMPClient(cfg, req.DeviceIP, 10)
				if err == nil {
					defer client.Close()
					// Walk standard MIB-2, Printer-MIB, and enterprise OIDs
					cols := agent.FullDiagnosticWalk(client, nil, []string{
						"1.3.6.1.2.1",    // MIB-2 (system, interfaces, etc.)
						"1.3.6.1.2.1.43", // Printer-MIB
						"1.3.6.1.4.1",    // Enterprise MIBs (vendor-specific)
					}, 5000) // Cap at 5000 OIDs to keep report size reasonable

					// Convert PDUs to RawPDU format for JSON serialization
					fullWalkData := make([]agent.RawPDU, 0, len(cols))
					for _, pdu := range cols {
						fullWalkData = append(fullWalkData, agent.PDUToRawPDU(pdu))
					}

					// Create or update parseDebug with full walk data
					if parseDebug == nil {
						parseDebug = &agent.ParseDebug{
							IP:        req.DeviceIP,
							Timestamp: time.Now().Format(time.RFC3339),
						}
					}
					parseDebug.FullWalkData = fullWalkData

					if appLogger != nil {
						appLogger.Info("Full SNMP walk complete", "ip", req.DeviceIP, "oids_collected", len(fullWalkData))
					}
				} else if appLogger != nil {
					appLogger.Warn("Failed to create SNMP client for full walk", "ip", req.DeviceIP, "error", err)
				}
			} else if appLogger != nil {
				appLogger.Warn("Failed to get SNMP config for full walk", "error", err)
			}
		}

		// Get recent logs for context (last 50 lines, sanitized)
		var recentLogs []string
		logPath := filepath.Join(".", "logs", "agent.log")
		var allLogLines []string
		if logData, err := os.ReadFile(logPath); err == nil {
			allLogLines = strings.Split(string(logData), "\n")
			start := len(allLogLines) - 50
			if start < 0 {
				start = 0
			}
			recentLogs = allLogLines[start:]
		}

		// Build the report
		issueType := agent.ParseIssueType(req.IssueType)
		rpt := submitter.BuildReport(
			issueType,
			req.ExpectedValue,
			req.UserMessage,
			req.DeviceIP,
			req.DeviceSerial,
			req.DeviceModel,
			req.DeviceMAC,
			req.CurrentManufacturer,
			req.CurrentHostname,
			req.CurrentPageCount,
			parseDebug,
			recentLogs,
		)

		// Supplement with any client-provided SNMP data (if parse debug wasn't on server)
		if len(rpt.SNMPResponses) == 0 && len(req.SNMPResponses) > 0 {
			rpt.SNMPResponses = req.SNMPResponses
		}
		if rpt.DetectedVendor == "" && req.DetectedVendor != "" {
			rpt.DetectedVendor = req.DetectedVendor
		}
		if len(rpt.DetectionSteps) == 0 && len(req.DetectionSteps) > 0 {
			rpt.DetectionSteps = req.DetectionSteps
		}

		// Populate extended device info from request
		rpt.Firmware = req.Firmware
		rpt.SubnetMask = req.SubnetMask
		rpt.Gateway = req.Gateway
		rpt.Consumables = req.Consumables
		rpt.StatusMessages = req.StatusMessages
		rpt.DiscoveryMethod = req.DiscoveryMethod
		rpt.WebUIURL = req.WebUIURL
		rpt.DeviceType = req.DeviceType
		rpt.SourceType = req.SourceType
		rpt.IsUSB = req.IsUSB
		rpt.PortName = req.PortName
		rpt.DriverName = req.DriverName
		rpt.IsDefault = req.IsDefault
		rpt.IsShared = req.IsShared
		rpt.SpoolerStatus = req.SpoolerStatus

		// Populate metrics data
		rpt.ColorPages = req.ColorPages
		rpt.MonoPages = req.MonoPages
		rpt.ScanCount = req.ScanCount
		rpt.TonerLevels = req.TonerLevels
		rpt.RawData = req.RawData

		// Fetch full device record and metrics history from storage for diagnostics
		if deviceStore != nil && req.DeviceSerial != "" {
			// Get full device record
			if device, err := deviceStore.Get(r.Context(), req.DeviceSerial); err == nil && device != nil {
				deviceMap := map[string]interface{}{
					"serial":             device.Serial,
					"ip":                 device.IP,
					"mac_address":        device.MACAddress,
					"hostname":           device.Hostname,
					"manufacturer":       device.Manufacturer,
					"model":              device.Model,
					"firmware":           device.Firmware,
					"subnet_mask":        device.SubnetMask,
					"gateway":            device.Gateway,
					"consumables":        device.Consumables,
					"status_messages":    device.StatusMessages,
					"device_type":        device.DeviceType,
					"source_type":        device.SourceType,
					"discovery_method":   device.DiscoveryMethod,
					"web_ui_url":         device.WebUIURL,
					"asset_number":       device.AssetNumber,
					"location":           device.Location,
					"description":        device.Description,
					"is_usb":             device.IsUSB,
					"port_name":          device.PortName,
					"driver_name":        device.DriverName,
					"is_default":         device.IsDefault,
					"is_shared":          device.IsShared,
					"spooler_status":     device.SpoolerStatus,
					"initial_page_count": device.InitialPageCount,
					"is_saved":           device.IsSaved,
					"visible":            device.Visible,
					"first_seen":         device.FirstSeen,
					"last_seen":          device.LastSeen,
					"created_at":         device.CreatedAt,
					"raw_data":           device.RawData,
				}
				rpt.DeviceRecord = deviceMap
				if appLogger != nil {
					appLogger.Info("Included device record in report", "serial", req.DeviceSerial)
				}
			}

			// Get last 100 metrics snapshots
			since := time.Now().AddDate(0, -6, 0) // 6 months back
			until := time.Now()
			if metricsHistory, err := deviceStore.GetMetricsHistory(r.Context(), req.DeviceSerial, since, until); err == nil {
				// Limit to last 100
				limit := 100
				if len(metricsHistory) > limit {
					metricsHistory = metricsHistory[:limit]
				}

				// Convert to generic maps for JSON serialization
				metricsSlice := make([]map[string]interface{}, 0, len(metricsHistory))
				for _, m := range metricsHistory {
					metricsMap := map[string]interface{}{
						"id":           m.ID,
						"serial":       m.Serial,
						"timestamp":    m.Timestamp,
						"page_count":   m.PageCount,
						"color_pages":  m.ColorPages,
						"mono_pages":   m.MonoPages,
						"scan_count":   m.ScanCount,
						"toner_levels": m.TonerLevels,
						"paper_trays":  m.PaperTrays,
						// HP-specific extended counters (agent only)
						"fax_pages":            m.FaxPages,
						"copy_pages":           m.CopyPages,
						"other_pages":          m.OtherPages,
						"copy_mono_pages":      m.CopyMonoPages,
						"copy_flatbed_scans":   m.CopyFlatbedScans,
						"copy_adf_scans":       m.CopyADFScans,
						"fax_flatbed_scans":    m.FaxFlatbedScans,
						"fax_adf_scans":        m.FaxADFScans,
						"scan_to_host_flatbed": m.ScanToHostFlatbed,
						"scan_to_host_adf":     m.ScanToHostADF,
						"duplex_sheets":        m.DuplexSheets,
						"jam_events":           m.JamEvents,
						"scanner_jam_events":   m.ScannerJamEvents,
						"tier":                 m.Tier,
					}
					metricsSlice = append(metricsSlice, metricsMap)
				}
				rpt.MetricsHistory = metricsSlice
				if appLogger != nil {
					appLogger.Info("Included metrics history in report", "serial", req.DeviceSerial, "count", len(metricsSlice))
				}
			}
		}

		// Include extended agent logs (last 200 lines) for detailed debugging
		if len(allLogLines) > 0 {
			start := len(allLogLines) - 200
			if start < 0 {
				start = 0
			}
			rpt.AgentLogs = allLogLines[start:]
		}

		// Collect USB proxy info if this is a USB device
		if req.IsUSB && req.DeviceSerial != "" {
			rpt.USBProxyInfo = collectUSBProxyInfo(r.Context(), req.DeviceSerial, appLogger)
		}

		// Try to submit to proxy
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		proxyResp, err := submitter.SubmitToProxy(ctx, rpt)
		if err != nil {
			if appLogger != nil {
				appLogger.Warn("Failed to submit report to proxy", "error", err)
			}
			// Return the report data so frontend can use fallback
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":     err.Error(),
				"report":    rpt,
				"issue_url": report.BuildGitHubIssueURL(rpt, ""),
				"fallback":  true,
			})
			return
		}

		if appLogger != nil {
			appLogger.Info("Report submitted successfully",
				"report_id", rpt.ReportID,
				"gist_url", proxyResp.GistURL,
				"issue_type", issueType.String())
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(proxyResp)
	})

	// POST /api/report/stream - Submit a device report with SSE progress streaming
	// This endpoint performs the same work as /api/report but streams progress updates
	// during the SNMP walk phase via Server-Sent Events.
	http.HandleFunc("/api/report/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}

		// Set up SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		// Helper to send SSE events
		sendEvent := func(eventType string, data interface{}) {
			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, jsonData)
			flusher.Flush()
		}

		var req agent.ReportRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			sendEvent("error", map[string]string{"error": fmt.Sprintf("invalid request: %v", err)})
			return
		}

		// Validate required fields
		if req.IssueType == "" {
			sendEvent("error", map[string]string{"error": "issue_type is required"})
			return
		}

		// Send initial progress
		sendEvent("progress", map[string]interface{}{
			"stage":   "starting",
			"percent": 0,
			"message": "Preparing report...",
		})

		// Create the report submitter
		agentID := agentConfig.Server.AgentID
		submitter := agent.NewReportSubmitter(Version, agentID, appLogger)

		// Get parse debug data if available
		var parseDebug *agent.ParseDebug
		if req.DeviceIP != "" {
			if pd, ok := agent.GetParseDebug(req.DeviceIP); ok {
				parseDebug = &pd
			}
		}

		// If FullWalk is requested, perform a complete SNMP walk with progress
		if req.FullWalk && req.DeviceIP != "" {
			sendEvent("progress", map[string]interface{}{
				"stage":   "connecting",
				"percent": 5,
				"message": "Connecting to device...",
			})

			if appLogger != nil {
				appLogger.Info("Performing full SNMP walk with progress for report", "ip", req.DeviceIP)
			}

			cfg, err := agent.GetSNMPConfig()
			if err == nil {
				client, err := agent.NewSNMPClient(cfg, req.DeviceIP, 10)
				if err == nil {
					defer client.Close()

					sendEvent("progress", map[string]interface{}{
						"stage":   "walking",
						"percent": 10,
						"message": "Starting SNMP walk...",
					})

					// Walk with progress callback
					progressFn := func(p agent.WalkProgress) {
						// Map progress to 10-90% range (reserve 0-10 for setup, 90-100 for submission)
						adjustedPercent := 10 + (p.Percent * 80 / 100)
						sendEvent("progress", map[string]interface{}{
							"stage":      p.Stage,
							"percent":    adjustedPercent,
							"message":    p.Message,
							"oids_found": p.OIDsFound,
							"root_oid":   p.RootOID,
						})
					}

					cols := agent.FullDiagnosticWalkWithProgress(client, nil, []string{
						"1.3.6.1.2.1",    // MIB-2
						"1.3.6.1.2.1.43", // Printer-MIB
						"1.3.6.1.4.1",    // Enterprise MIBs
					}, 5000, progressFn)

					// Convert PDUs to RawPDU format
					fullWalkData := make([]agent.RawPDU, 0, len(cols))
					for _, pdu := range cols {
						fullWalkData = append(fullWalkData, agent.PDUToRawPDU(pdu))
					}

					if parseDebug == nil {
						parseDebug = &agent.ParseDebug{
							IP:        req.DeviceIP,
							Timestamp: time.Now().Format(time.RFC3339),
						}
					}
					parseDebug.FullWalkData = fullWalkData

					if appLogger != nil {
						appLogger.Info("Full SNMP walk complete", "ip", req.DeviceIP, "oids_collected", len(fullWalkData))
					}
				} else if appLogger != nil {
					appLogger.Warn("Failed to create SNMP client for full walk", "ip", req.DeviceIP, "error", err)
					sendEvent("progress", map[string]interface{}{
						"stage":   "warning",
						"percent": 90,
						"message": "Could not connect to device for SNMP walk",
					})
				}
			} else if appLogger != nil {
				appLogger.Warn("Failed to get SNMP config for full walk", "error", err)
			}
		}

		sendEvent("progress", map[string]interface{}{
			"stage":   "building",
			"percent": 90,
			"message": "Building report...",
		})

		// Get recent logs for context
		var recentLogs []string
		logPath := filepath.Join(".", "logs", "agent.log")
		var allLogLines []string
		if logData, err := os.ReadFile(logPath); err == nil {
			allLogLines = strings.Split(string(logData), "\n")
			start := len(allLogLines) - 50
			if start < 0 {
				start = 0
			}
			recentLogs = allLogLines[start:]
		}

		// Build the report
		issueType := agent.ParseIssueType(req.IssueType)
		rpt := submitter.BuildReport(
			issueType,
			req.ExpectedValue,
			req.UserMessage,
			req.DeviceIP,
			req.DeviceSerial,
			req.DeviceModel,
			req.DeviceMAC,
			req.CurrentManufacturer,
			req.CurrentHostname,
			req.CurrentPageCount,
			parseDebug,
			recentLogs,
		)

		// Supplement with client-provided data
		if len(rpt.SNMPResponses) == 0 && len(req.SNMPResponses) > 0 {
			rpt.SNMPResponses = req.SNMPResponses
		}
		if rpt.DetectedVendor == "" && req.DetectedVendor != "" {
			rpt.DetectedVendor = req.DetectedVendor
		}
		if len(rpt.DetectionSteps) == 0 && len(req.DetectionSteps) > 0 {
			rpt.DetectionSteps = req.DetectionSteps
		}

		// Populate extended device info
		rpt.Firmware = req.Firmware
		rpt.SubnetMask = req.SubnetMask
		rpt.Gateway = req.Gateway
		rpt.Consumables = req.Consumables
		rpt.StatusMessages = req.StatusMessages
		rpt.DiscoveryMethod = req.DiscoveryMethod
		rpt.WebUIURL = req.WebUIURL
		rpt.DeviceType = req.DeviceType
		rpt.SourceType = req.SourceType
		rpt.IsUSB = req.IsUSB
		rpt.PortName = req.PortName
		rpt.DriverName = req.DriverName
		rpt.IsDefault = req.IsDefault
		rpt.IsShared = req.IsShared
		rpt.SpoolerStatus = req.SpoolerStatus
		rpt.ColorPages = req.ColorPages
		rpt.MonoPages = req.MonoPages
		rpt.ScanCount = req.ScanCount
		rpt.TonerLevels = req.TonerLevels
		rpt.RawData = req.RawData

		// Fetch device record and metrics from storage
		if deviceStore != nil && req.DeviceSerial != "" {
			if device, err := deviceStore.Get(r.Context(), req.DeviceSerial); err == nil && device != nil {
				deviceMap := map[string]interface{}{
					"serial": device.Serial, "ip": device.IP, "mac_address": device.MACAddress,
					"hostname": device.Hostname, "manufacturer": device.Manufacturer, "model": device.Model,
					"firmware": device.Firmware, "device_type": device.DeviceType, "source_type": device.SourceType,
				}
				rpt.DeviceRecord = deviceMap
			}

			since := time.Now().AddDate(0, -6, 0)
			until := time.Now()
			if metricsHistory, err := deviceStore.GetMetricsHistory(r.Context(), req.DeviceSerial, since, until); err == nil {
				limit := 100
				if len(metricsHistory) > limit {
					metricsHistory = metricsHistory[:limit]
				}
				metricsSlice := make([]map[string]interface{}, 0, len(metricsHistory))
				for _, m := range metricsHistory {
					metricsSlice = append(metricsSlice, map[string]interface{}{
						"timestamp": m.Timestamp, "page_count": m.PageCount,
						"color_pages": m.ColorPages, "mono_pages": m.MonoPages,
						"toner_levels": m.TonerLevels,
					})
				}
				rpt.MetricsHistory = metricsSlice
			}
		}

		// Extended logs
		if len(allLogLines) > 0 {
			start := len(allLogLines) - 200
			if start < 0 {
				start = 0
			}
			rpt.AgentLogs = allLogLines[start:]
		}

		sendEvent("progress", map[string]interface{}{
			"stage":   "submitting",
			"percent": 95,
			"message": "Submitting to GitHub...",
		})

		// Submit to proxy
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		proxyResp, err := submitter.SubmitToProxy(ctx, rpt)
		if err != nil {
			if appLogger != nil {
				appLogger.Warn("Failed to submit report to proxy", "error", err)
			}
			sendEvent("error", map[string]interface{}{
				"error":     err.Error(),
				"report":    rpt,
				"issue_url": report.BuildGitHubIssueURL(rpt, ""),
				"fallback":  true,
			})
			return
		}

		if appLogger != nil {
			appLogger.Info("Report submitted successfully (streamed)",
				"report_id", rpt.ReportID,
				"gist_url", proxyResp.GistURL,
				"issue_type", issueType.String())
		}

		sendEvent("complete", proxyResp)
	})

	// Endpoint to return current scan metrics snapshot
	// TODO(deprecate): Remove /scan_metrics endpoint - superseded by metrics API
	// Still used by UI metrics display, needs replacement before removal
	http.HandleFunc("/scan_metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agent.GetMetricsSnapshot())
	})

	// Serve the on-disk logfile for easier inspection
	http.HandleFunc("/logfile", func(w http.ResponseWriter, r *http.Request) {
		fpath := filepath.Join(".", "logs", "agent.log")
		data, err := os.ReadFile(fpath)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "logfile not found")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	})

	// Check for database rotation event (GET) or clear the warning (POST)
	http.HandleFunc("/database/rotation_warning", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case "GET":
			// Check if rotation flag is set
			var rotationInfo map[string]interface{}
			err := agentConfigStore.GetConfigValue("database_rotation", &rotationInfo)
			if err != nil || rotationInfo == nil {
				// No rotation event
				json.NewEncoder(w).Encode(map[string]interface{}{
					"rotated":     false,
					"rotated_at":  nil,
					"backup_path": nil,
				})
				return
			}

			// Rotation event found
			json.NewEncoder(w).Encode(map[string]interface{}{
				"rotated":     true,
				"rotated_at":  rotationInfo["rotated_at"],
				"backup_path": rotationInfo["backup_path"],
			})
		case "POST":
			// Clear the rotation warning flag
			if err := agentConfigStore.SetConfigValue("database_rotation", nil); err != nil {
				appLogger.Error("Failed to clear rotation warning", "error", err)
				http.Error(w, "Failed to clear warning", http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "Rotation warning cleared",
			})
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	appLogger.Info("Web UI running", "url", "http://localhost:8080")

	// Refresh device profile by serial (or IP). POST JSON { "serial": "...", "ip": "optional ip" }
	http.HandleFunc("/devices/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Serial string `json:"serial"`
			IP     string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		req.Serial = strings.TrimSpace(req.Serial)
		if req.Serial == "" && req.IP == "" {
			http.Error(w, "serial or ip required", http.StatusBadRequest)
			return
		}
		// if serial provided but no IP, try load existing device to get IP
		targetIP := strings.TrimSpace(req.IP)
		if targetIP == "" && req.Serial != "" {
			// Sanitize serial to prevent path traversal attacks
			safeSerial := filepath.Base(req.Serial)
			if safeSerial == "." || safeSerial == ".." || safeSerial != req.Serial {
				http.Error(w, "invalid serial number", http.StatusBadRequest)
				return
			}
			devPath := filepath.Join(".", "logs", "devices", safeSerial+".json")
			if b, err := os.ReadFile(devPath); err == nil {
				var doc map[string]interface{}
				if json.Unmarshal(b, &doc) == nil {
					if pi, ok := doc["printer_info"].(map[string]interface{}); ok {
						if ipval, ok2 := pi["ip"].(string); ok2 {
							targetIP = strings.TrimSpace(ipval)
						}
						if targetIP == "" {
							if ipval2, ok3 := pi["IP"].(string); ok3 {
								targetIP = strings.TrimSpace(ipval2)
							}
						}
					}
				}
			}
		}
		if targetIP == "" {
			http.Error(w, "unable to determine target ip for refresh", http.StatusBadRequest)
			return
		}
		ctx := context.Background()
		pi, err := LiveDiscoveryDetect(ctx, targetIP, getSNMPTimeoutSeconds())
		if err != nil {
			appLogger.Error("Device refresh failed", "ip", targetIP, "error", err)
			http.Error(w, "refresh failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		agent.UpsertDiscoveredPrinter(*pi)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "serial": pi.Serial})
	})

	// Update device fields (now supports many fields; respects locked fields at the UI level)
	http.HandleFunc("/devices/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Serial       string    `json:"serial"`
			Manufacturer *string   `json:"manufacturer,omitempty"`
			Model        *string   `json:"model,omitempty"`
			Hostname     *string   `json:"hostname,omitempty"`
			Firmware     *string   `json:"firmware,omitempty"`
			IP           *string   `json:"ip,omitempty"`
			SubnetMask   *string   `json:"subnet_mask,omitempty"`
			Gateway      *string   `json:"gateway,omitempty"`
			DNSServers   *[]string `json:"dns_servers,omitempty"`
			DHCPServer   *string   `json:"dhcp_server,omitempty"`
			AssetNumber  *string   `json:"asset_number,omitempty"`
			Location     *string   `json:"location,omitempty"`
			Description  *string   `json:"description,omitempty"`
			WebUIURL     *string   `json:"web_ui_url,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		req.Serial = strings.TrimSpace(req.Serial)
		if req.Serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}

		// Get existing device
		ctx := context.Background()
		device, err := deviceStore.Get(ctx, req.Serial)
		if err != nil {
			http.Error(w, "device not found: "+err.Error(), http.StatusNotFound)
			return
		}

		// Helper to check if field is locked
		isFieldLocked := func(fieldName string) bool {
			if device.LockedFields == nil {
				return false
			}
			for _, lf := range device.LockedFields {
				if strings.EqualFold(lf.Field, fieldName) {
					return true
				}
			}
			return false
		}

		// Update only provided fields (skip locked fields)
		if req.Manufacturer != nil && !isFieldLocked("manufacturer") {
			device.Manufacturer = *req.Manufacturer
		}
		if req.Model != nil && !isFieldLocked("model") {
			device.Model = *req.Model
		}
		if req.Hostname != nil && !isFieldLocked("hostname") {
			device.Hostname = *req.Hostname
		}
		if req.Firmware != nil && !isFieldLocked("firmware") {
			device.Firmware = *req.Firmware
		}
		if req.IP != nil && !isFieldLocked("ip") {
			device.IP = *req.IP
		}
		if req.SubnetMask != nil && !isFieldLocked("subnet_mask") {
			device.SubnetMask = *req.SubnetMask
		}
		if req.Gateway != nil && !isFieldLocked("gateway") {
			device.Gateway = *req.Gateway
		}
		if req.DNSServers != nil && !isFieldLocked("dns_servers") {
			device.DNSServers = *req.DNSServers
		}
		if req.DHCPServer != nil && !isFieldLocked("dhcp_server") {
			device.DHCPServer = *req.DHCPServer
		}
		if req.AssetNumber != nil && !isFieldLocked("asset_number") {
			device.AssetNumber = *req.AssetNumber
		}
		if req.Location != nil && !isFieldLocked("location") {
			device.Location = *req.Location
		}
		if req.Description != nil && !isFieldLocked("description") {
			device.Description = *req.Description
		}
		if req.WebUIURL != nil && !isFieldLocked("web_ui_url") {
			device.WebUIURL = *req.WebUIURL
		}

		// Save updated device
		if err := deviceStore.Update(ctx, device); err != nil {
			appLogger.Error("Device update failed", "serial", device.Serial, "error", err)
			http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "serial": device.Serial})
	})

	// Preview device updates: perform a live walk+parse but DO NOT write to DB; returns proposed fields
	http.HandleFunc("/devices/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct{ Serial, IP string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		req.Serial = strings.TrimSpace(req.Serial)
		// If IP not provided, try to load from DB
		if strings.TrimSpace(req.IP) == "" && req.Serial != "" {
			if dev, err := deviceStore.Get(context.Background(), req.Serial); err == nil {
				req.IP = dev.IP
			}
		}
		if strings.TrimSpace(req.IP) == "" {
			http.Error(w, "ip required", http.StatusBadRequest)
			return
		}

		// Build SNMP client and perform a full diagnostic walk (no stop keywords)
		cfg, err := agent.GetSNMPConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client, err := agent.NewSNMPClient(cfg, req.IP, 5)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer client.Close()

		cols := agent.FullDiagnosticWalk(client, nil, []string{"1.3.6.1.2.1", "1.3.6.1.2.1.43", "1.3.6.1.4.1"}, 10000)
		pi, _ := agent.ParsePDUs(req.IP, cols, nil, func(string) {})
		// Merge vendor-specific metrics (ICE-style OIDs)
		agent.MergeVendorMetrics(&pi, cols, "")

		// Return only the fields relevant for device details
		proposed := map[string]interface{}{
			"ip":           pi.IP,
			"manufacturer": pi.Manufacturer,
			"model":        pi.Model,
			"hostname":     pi.Hostname,
			"firmware":     pi.Firmware,
			"subnet_mask":  pi.SubnetMask,
			"gateway":      pi.Gateway,
			"dns_servers":  pi.DNSServers,
			"dhcp_server":  pi.DHCPServer,
			"asset_number": pi.AssetID,
			"location":     pi.Location,
			"description":  pi.Description,
			"web_ui_url":   pi.WebUIURL,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"proposed": proposed})
	})

	// Toggle a field lock on a device
	http.HandleFunc("/devices/lock", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Serial       string      `json:"serial"`
			Field        string      `json:"field"`
			Lock         bool        `json:"lock"`
			CurrentValue interface{} `json:"current_value,omitempty"` // Value to search for when locking
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Serial == "" || req.Field == "" {
			http.Error(w, "serial and field required", http.StatusBadRequest)
			return
		}
		ctx := context.Background()
		device, err := deviceStore.Get(ctx, req.Serial)
		if err != nil {
			http.Error(w, "device not found: "+err.Error(), http.StatusNotFound)
			return
		}

		// Ensure LockedFields slice exists
		if device.LockedFields == nil {
			device.LockedFields = []storage.FieldLock{}
		}
		// helper to check presence
		has := -1
		for i, lf := range device.LockedFields {
			if strings.EqualFold(lf.Field, req.Field) {
				has = i
				break
			}
		}

		// When locking a field, try to learn the OID for the current value
		var foundOID string
		if req.Lock && has == -1 && req.CurrentValue != nil {
			// Perform SNMP walk to find OID matching the locked value
			foundOID = tryLearnOIDForValue(ctx, device.IP, device.Manufacturer, req.Field, req.CurrentValue)
			if foundOID != "" {
				appLogger.Info("FIELD_LOCK_OID_LEARNED",
					"ip", device.IP,
					"manufacturer", device.Manufacturer,
					"model", device.Model,
					"serial", device.Serial,
					"field", req.Field,
					"value", req.CurrentValue,
					"found_oid", foundOID)

				// Store the learned OID in device RawData
				pi := storage.DeviceToPrinterInfo(device)
				switch strings.ToLower(req.Field) {
				case "page_count", "total_pages":
					pi.LearnedOIDs.PageCountOID = foundOID
				case "mono_pages", "mono_impressions":
					pi.LearnedOIDs.MonoPagesOID = foundOID
				case "color_pages", "color_impressions":
					pi.LearnedOIDs.ColorPagesOID = foundOID
				case "serial":
					pi.LearnedOIDs.SerialOID = foundOID
				case "model":
					pi.LearnedOIDs.ModelOID = foundOID
				default:
					// Store in vendor-specific OIDs
					if pi.LearnedOIDs.VendorSpecificOIDs == nil {
						pi.LearnedOIDs.VendorSpecificOIDs = make(map[string]string)
					}
					pi.LearnedOIDs.VendorSpecificOIDs[req.Field] = foundOID
				}
				// Update device with learned OIDs
				if device.RawData == nil {
					device.RawData = make(map[string]interface{})
				}
				device.RawData["learned_oids"] = pi.LearnedOIDs
			}
		}

		if req.Lock {
			if has == -1 {
				device.LockedFields = append(device.LockedFields, storage.FieldLock{Field: req.Field, LockedAt: time.Now(), Reason: "user_locked"})
			}
		} else {
			if has >= 0 {
				device.LockedFields = append(device.LockedFields[:has], device.LockedFields[has+1:]...)
			}
		}
		if err := deviceStore.Update(ctx, device); err != nil {
			http.Error(w, "failed to update locks: "+err.Error(), http.StatusInternalServerError)
			return
		}

		response := map[string]interface{}{
			"status": "ok",
		}
		if foundOID != "" {
			response["learned_oid"] = foundOID
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	// Endpoint: Save Web UI credentials (moved out of proxy response modifier)
	http.HandleFunc("/device/webui-credentials", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method { //nolint:exhaustive
		case http.MethodGet:
			serial := r.URL.Query().Get("serial")
			if serial == "" {
				http.Error(w, "serial required", http.StatusBadRequest)
				return
			}
			cr, err := getCreds(serial)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"exists": false})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"exists": true, "username": cr.Username, "auth_type": cr.AuthType, "auto_login": cr.AutoLogin})
		case http.MethodPost:
			var req struct {
				Serial    string `json:"serial"`
				Username  string `json:"username"`
				Password  string `json:"password"`
				AuthType  string `json:"auth_type"`
				AutoLogin bool   `json:"auto_login"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			if req.Serial == "" {
				http.Error(w, "serial required", http.StatusBadRequest)
				return
			}
			enc := ""
			if req.Password != "" && len(secretKey) == 32 {
				if v, err := commonutil.EncryptToB64(secretKey, req.Password); err == nil {
					enc = v
				}
			}
			cr := credRecord{Username: req.Username, Password: enc, AuthType: strings.ToLower(req.AuthType), AutoLogin: req.AutoLogin}
			if err := saveCreds(req.Serial, cr); err != nil {
				http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// checkAndFallbackProtocol does a quick TCP connectivity check on the target URL.
	// If the connection fails, it tries the alternative protocol (https↔http).
	// This handles cases where web_ui_url is incorrectly set (e.g., HTTPS when device only supports HTTP).
	checkAndFallbackProtocol := func(ctx context.Context, targetURL, deviceIP, serial string, log *logger.Logger) string {
		parsed, err := url.Parse(targetURL)
		if err != nil {
			return targetURL
		}

		// Determine host:port to check
		host := parsed.Host
		if host == "" {
			return targetURL
		}

		// Quick TCP probe with short timeout (don't block the user)
		checkCtx, checkCancel := context.WithTimeout(ctx, 3*time.Second)
		defer checkCancel()

		d := net.Dialer{}
		conn, err := d.DialContext(checkCtx, "tcp", host)
		if err == nil {
			conn.Close()
			return targetURL // Primary URL works
		}

		// Connection failed - try alternative protocol
		log.Debug("Proxy: primary URL unreachable, trying fallback", "url", targetURL, "error", err.Error())

		var altURL string
		if parsed.Scheme == "https" {
			// Try HTTP on port 80
			altURL = "http://" + deviceIP
		} else {
			// Try HTTPS on port 443
			altURL = "https://" + deviceIP
		}

		altParsed, err := url.Parse(altURL)
		if err != nil {
			return targetURL
		}

		altConn, err := d.DialContext(checkCtx, "tcp", altParsed.Host)
		if err == nil {
			altConn.Close()
			log.Info("Proxy: using fallback protocol", "serial", serial, "original", targetURL, "fallback", altURL)
			return altURL
		}

		// Both failed - return original and let the proxy handler report the error
		log.Warn("Proxy: both protocols unreachable", "serial", serial, "primary", targetURL, "fallback", altURL)
		return targetURL
	}

	// Proxy printer web UI - /proxy/<serial>/<path...>
	http.HandleFunc("/proxy/", func(w http.ResponseWriter, r *http.Request) {
		// Determine if request is over HTTPS
		isHTTPS := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

		// Set a timeout for the entire proxy request and store HTTPS status
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, isHTTPSContextKey, isHTTPS)
		r = r.WithContext(ctx)

		// Extract serial from path: /proxy/SERIAL123/remaining/path
		pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/")
		if len(pathParts) == 0 || pathParts[0] == "" {
			http.Error(w, "serial required in path", http.StatusBadRequest)
			return
		}
		serial := pathParts[0]

		// Check if this looks like a resource path without a valid serial (e.g., /proxy/js/... or /proxy/css/...)
		// This happens when relative URLs like "../js/file.js" escape the serial directory
		// Common resource directories that shouldn't be treated as serials
		resourceDirs := []string{"js", "css", "images", "strings", "lib", "fonts", "assets", "static", "startwlm", "wlmeng"}
		for _, dir := range resourceDirs {
			if serial == dir {
				appLogger.Debug("Proxy: detected resource path without serial", "path", r.URL.Path, "referer", r.Header.Get("Referer"))
				// Try to extract serial from Referer header
				if referer := r.Header.Get("Referer"); referer != "" {
					if refURL, err := url.Parse(referer); err == nil && strings.HasPrefix(refURL.Path, "/proxy/") {
						refParts := strings.Split(strings.TrimPrefix(refURL.Path, "/proxy/"), "/")
						if len(refParts) > 0 && refParts[0] != "" {
							// Check if the referer's serial is valid
							refSerial := refParts[0]
							isValidSerial := true
							for _, resDir := range resourceDirs {
								if refSerial == resDir {
									isValidSerial = false
									break
								}
							}
							if isValidSerial {
								// Redirect to the correct path with serial
								correctPath := "/proxy/" + refSerial + strings.TrimPrefix(r.URL.Path, "/proxy")
								appLogger.Debug("Proxy: redirecting resource to correct serial path", "from", r.URL.Path, "to", correctPath)
								http.Redirect(w, r, correctPath, http.StatusFound)
								return
							}
						}
					}
				}
				http.Error(w, "Invalid proxy path - serial number required. Resource paths must include device serial.", http.StatusBadRequest)
				return
			}
		}

		// Check if this is a USB printer - if so, we'll use USB transport with the standard proxy flow
		// This ensures all URL rewriting logic is applied consistently
		var usbTransport http.RoundTripper
		isUSBDevice := CanUSBProxySerial(serial) && usbProxySupported()
		if isUSBDevice {
			var ok bool
			usbTransport, ok = usbProxyTransportForSerial(serial)
			if !ok {
				appLogger.Warn("Proxy: USB transport unavailable", "serial", serial)
				http.Error(w, "USB printer connection failed", http.StatusBadGateway)
				return
			}
			appLogger.Debug("Proxy: using USB transport", "serial", serial, "path", r.URL.Path)
		}

		// For USB devices, we don't need to look up network device info
		// Create a virtual target URL for USB (localhost is standard for IPP-USB)
		var device *storage.Device
		var targetURL string
		if isUSBDevice {
			// USB devices use localhost as the virtual target (IPP-USB spec)
			targetURL = "http://localhost"
		} else {
			// Look up network device (use the timeout context from above)
			var err error
			device, err = deviceStore.Get(ctx, serial)
			if err != nil {
				appLogger.Warn("Proxy: device lookup failed", "serial", serial, "error", err.Error(), "path", r.URL.Path)
				http.Error(w, "device not found: "+err.Error(), http.StatusNotFound)
				return
			}
			appLogger.Debug("Proxy: device found", "serial", serial, "ip", device.IP, "manufacturer", device.Manufacturer)

			// Determine target URL (prefer web_ui_url, fallback to http://<ip>)
			targetURL = device.WebUIURL
			if targetURL == "" {
				targetURL = "http://" + device.IP
			}

			// Quick connectivity check with automatic HTTP/HTTPS fallback
			// This helps when web_ui_url is incorrectly set (common with self-signed HTTPS)
			targetURL = checkAndFallbackProtocol(ctx, targetURL, device.IP, serial, appLogger)
		}

		target, err := url.Parse(targetURL)
		if err != nil {
			http.Error(w, "invalid target URL", http.StatusInternalServerError)
			return
		}

		// Build target path early for fast static resource detection
		targetPath := "/"
		if len(pathParts) > 1 {
			targetPath = "/" + strings.Join(pathParts[1:], "/")
		}
		targetPath = strings.ReplaceAll(targetPath, "//", "/")

		// Fast path for static resources - skip all auth logic for performance
		// These resources don't need authentication and checking on every request is slow
		staticExtensions := []string{".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot"}
		isStaticResource := false
		lowerPath := strings.ToLower(targetPath)
		for _, ext := range staticExtensions {
			if strings.HasSuffix(lowerPath, ext) {
				isStaticResource = true
				break
			}
		}

		// Check cache for static resources first
		if isStaticResource {
			cacheKey := serial + ":" + targetPath
			if data, contentType, headers, ok := staticCache.Get(cacheKey); ok {
				appLogger.Debug("Proxy: serving from cache", "serial", serial, "path", targetPath, "size", len(data))
				// Copy cached headers
				for key, values := range headers {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				if contentType != "" {
					w.Header().Set("Content-Type", contentType)
				}
				w.Header().Set("Cache-Control", "public, max-age=3600") // Browser can cache for 1 hour
				w.WriteHeader(http.StatusOK)
				w.Write(data)
				return
			}
		}

		// Pre-authenticate for main page requests when auto-login is enabled
		// This ensures cookies are available before the page loads (important for Kyocera)
		if !isStaticResource && (targetPath == "/" || targetPath == "") {
			cr, err := getCreds(serial)
			if err == nil && cr != nil && cr.AuthType == "form" && cr.AutoLogin {
				// Check if we have a valid session cached
				cachedJar := proxySessionCache.Get(serial)
				hasValidSession := false
				if cachedJar != nil {
					// Verify the cached jar is valid
					func() {
						defer func() {
							if r := recover(); r != nil {
								appLogger.Debug("Proxy: cached session invalid on pre-auth check", "serial", serial)
								proxySessionCache.Clear(serial)
								cachedJar = nil
							}
						}()
						if targetParsed, err := url.Parse(targetURL); err == nil {
							cookies := cachedJar.Cookies(targetParsed)
							hasValidSession = len(cookies) > 0
							appLogger.Debug("Proxy: pre-auth session check", "serial", serial, "has_session", hasValidSession, "cookie_count", len(cookies))
						}
					}()
				}

				// If no valid session, perform login now before serving the page
				// cr.Password is already plaintext (fetched from server or decrypted locally)
				if !hasValidSession && cr.Password != "" {
					appLogger.Info("Proxy: pre-authenticating for main page request", "serial", serial, "manufacturer", device.Manufacturer)
					if adapter := proxy.GetAdapterForManufacturer(device.Manufacturer); adapter != nil {
						if jar, err := adapter.Login(targetURL, cr.Username, cr.Password, appLogger); err == nil {
							proxySessionCache.Set(serial, jar)
							appLogger.Info("Proxy: pre-auth successful, cookies ready", "serial", serial, "manufacturer", device.Manufacturer)

							// Send cookies to browser and redirect to same URL to reload with auth
							if targetParsed, err := url.Parse(targetURL); err == nil {
								cookies := jar.Cookies(targetParsed)
								// Get HTTPS status from context
								isHTTPS := false
								if v := r.Context().Value(isHTTPSContextKey); v != nil {
									isHTTPS = v.(bool)
								}
								for _, cookie := range cookies {
									// Rewrite cookie path for proxy
									if cookie.Path == "" || cookie.Path == "/" {
										cookie.Path = "/proxy/" + serial + "/"
									} else if !strings.HasPrefix(cookie.Path, "/proxy/"+serial) {
										cookie.Path = "/proxy/" + serial + cookie.Path
									}
									cookie.Domain = ""
									// Set Secure flag based on current connection type
									// If agent is accessed via HTTPS, keep Secure=true; if HTTP, clear it
									cookie.Secure = isHTTPS
									if cookie.SameSite == 0 {
										cookie.SameSite = http.SameSiteLaxMode
									}
									http.SetCookie(w, cookie)
									appLogger.Debug("Proxy: pre-auth set cookie", "serial", serial, "name", cookie.Name, "path", cookie.Path, "secure", cookie.Secure)
								}
							}

							// Redirect to same URL to reload with cookies
							w.Header().Set("Location", r.URL.Path)
							w.WriteHeader(http.StatusFound)
							return
						} else {
							appLogger.Warn("Proxy: pre-auth login failed", "serial", serial, "error", err.Error())
						}
					}
				}
			}
		}

		// Create reverse proxy. Director is intentionally left unset because
		// the Rewrite hook below (set after credential/session setup) fully
		// replaces it - ReverseProxy panics if both Director and Rewrite are set.
		rproxy := &httputil.ReverseProxy{}

		// Handle form-based login if configured (skip for static resources)
		var sessionJar http.CookieJar
		var cr *credRecord
		if !isStaticResource {
			var err error
			cr, err = getCreds(serial)
			appLogger.Debug("Proxy: checking credentials", "serial", serial, "has_creds", cr != nil, "get_error", err)
			if err == nil && cr != nil {
				appLogger.Debug("Proxy: credentials found", "serial", serial, "auth_type", cr.AuthType, "auto_login", cr.AutoLogin, "username", cr.Username)
			}
			if err == nil && cr != nil && cr.AuthType == "form" && cr.AutoLogin {
				appLogger.Info("Proxy: form auth configured for device", "serial", serial, "manufacturer", device.Manufacturer)
				// Check session cache first - Get returns *cookiejar.Jar which can be nil
				cachedJar := proxySessionCache.Get(serial)
				if cachedJar != nil {
					sessionJar = cachedJar
					// Test if jar is actually usable by trying to get cookies
					func() {
						defer func() {
							if r := recover(); r != nil {
								appLogger.Warn("Proxy: cached jar is invalid, clearing", "serial", serial, "error", fmt.Sprintf("%v", r))
								sessionJar = nil
								proxySessionCache.Clear(serial)
							}
						}()
						if targetParsed, err := url.Parse(targetURL); err == nil {
							_ = sessionJar.Cookies(targetParsed)
							appLogger.Debug("Proxy: session cache check - jar is valid", "serial", serial)
						}
					}()
				}
				appLogger.Debug("Proxy: session cache check", "serial", serial, "cached", sessionJar != nil)
				// cr.Password is already plaintext (fetched from server or decrypted locally)
				if sessionJar == nil && cr.Password != "" {
					appLogger.Debug("Proxy: attempting fresh login", "serial", serial, "manufacturer", device.Manufacturer)
					// Attempt vendor-specific login
					if adapter := proxy.GetAdapterForManufacturer(device.Manufacturer); adapter != nil {
						appLogger.Debug("Proxy: attempting vendor login", "manufacturer", device.Manufacturer, "serial", serial, "adapter", adapter.Name())
						if jar, err := adapter.Login(targetURL, cr.Username, cr.Password, appLogger); err == nil {
							sessionJar = jar
							proxySessionCache.Set(serial, jar)
							// Log cookies that were received
							if targetParsed, err := url.Parse(targetURL); err == nil {
								cookies := jar.Cookies(targetParsed)
								appLogger.Info("Proxy: logged into device", "manufacturer", device.Manufacturer, "serial", serial, "adapter", adapter.Name(), "cookies_received", len(cookies))
								for i, c := range cookies {
									appLogger.Debug("Proxy: received cookie", "index", i, "name", c.Name, "value_length", len(c.Value), "path", c.Path, "domain", c.Domain)
								}
							} else {
								appLogger.Info("Proxy: logged into device", "manufacturer", device.Manufacturer, "serial", serial, "adapter", adapter.Name())
							}
						} else {
							appLogger.WarnRateLimited("proxy_login_"+serial, 5*time.Minute, "Proxy: login failed", "serial", serial, "error", err.Error())
						}
					} else {
						appLogger.Debug("Proxy: no adapter found for manufacturer", "manufacturer", device.Manufacturer, "serial", serial)
					}
				}

				// If we have valid session cookies and user is accessing a login/password page,
				// redirect them to the home page instead (autologin bypass)
				if sessionJar != nil {
					// Build target path first to check it
					targetPath := "/"
					if len(pathParts) > 1 {
						targetPath = "/" + strings.Join(pathParts[1:], "/")
					}
					targetPath = strings.ReplaceAll(targetPath, "//", "/")

					loginPaths := []string{
						"/PRESENTATION/ADVANCED/PASSWORD",
						"/login",
						"/auth",
					}
					for _, loginPath := range loginPaths {
						if strings.HasPrefix(strings.ToUpper(targetPath), strings.ToUpper(loginPath)) {
							appLogger.Info("Proxy: redirecting authenticated user from login page to home", "serial", serial, "original_path", targetPath)

							// Send Set-Cookie headers to browser so it stores the session cookies
							if targetParsed, err := url.Parse(targetURL); err == nil {
								cookies := sessionJar.Cookies(targetParsed)
								proxyPrefix := "/proxy/" + serial
								// Determine if we're on HTTPS to set Secure flag appropriately
								isSecure := false
								if v := r.Context().Value(isHTTPSContextKey); v != nil {
									isSecure = v.(bool)
								}
								for _, cookie := range cookies {
									// Clone the cookie and rewrite path for proxy
									browserCookie := &http.Cookie{
										Name:     cookie.Name,
										Value:    cookie.Value,
										Path:     proxyPrefix + "/",
										Domain:   "",
										MaxAge:   cookie.MaxAge,
										Secure:   isSecure,
										HttpOnly: cookie.HttpOnly,
										SameSite: http.SameSiteLaxMode,
									}
									http.SetCookie(w, browserCookie)
									appLogger.Debug("Proxy: sending Set-Cookie to browser", "name", cookie.Name, "path", browserCookie.Path)
								}
							}

							// Send HTML that redirects the top-level frame (not just iframe)
							// This ensures the entire page reloads with cookies, not just the iframe
							w.Header().Set("Content-Type", "text/html; charset=utf-8")
							w.WriteHeader(http.StatusOK)
							// Escape serial for safe HTML embedding to prevent XSS
							safeSerial := html.EscapeString(serial)
							redirectHTML := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Logged In - Redirecting...</title>
<script>
// Redirect the top-level window to ensure full page reload with cookies
window.top.location.href = '/proxy/%s/';
</script>
</head>
<body>
<p>Login successful. Redirecting...</p>
<noscript>
<p>Please enable JavaScript or <a href="/proxy/%s/">click here</a> to continue.</p>
</noscript>
</body>
</html>`, safeSerial, safeSerial)
							fmt.Fprint(w, redirectHTML)
							return
						}
					}
				}
			}
		} // End if !isStaticResource

		// Rewrite request path to remove /proxy/<serial> prefix BEFORE setting Director
		originalPath := r.URL.Path
		// Reuse targetPath if already computed, otherwise calculate it
		if targetPath == "/" && len(pathParts) > 1 {
			targetPath = "/" + strings.Join(pathParts[1:], "/")
		}
		// Clean up double slashes
		targetPath = strings.ReplaceAll(targetPath, "//", "/")

		// Proxy prefix for this device's serial, used for URL rewriting and header adjustments
		proxyPrefix := "/proxy/" + serial

		// Capture sessionJar for safe closure access (avoid races)
		capturedJar := sessionJar

		// Rewrite requests using the modern ReverseProxy hook.
		rproxy.Rewrite = func(pr *httputil.ProxyRequest) {
			req := pr.Out

			// Base rewrite behavior to set URL/Host/Path
			pr.SetURL(target)
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = targetPath
			req.Host = target.Host
			// Force identity encoding so upstream doesn't gzip; simplifies any content rewriting
			// and avoids mismatched Content-Encoding headers when we replace bodies.
			req.Header.Del("Accept-Encoding")

			// Rewrite Referer and Origin headers to the upstream origin so vendor UIs that enforce
			// CSRF/host checks don't reject proxied form posts or XHR requests.
			if ref := pr.In.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil {
					if strings.HasPrefix(u.Path, proxyPrefix) {
						upPath := strings.TrimPrefix(u.Path, proxyPrefix)
						if upPath == "" {
							upPath = "/"
						}
						newRef := target.Scheme + "://" + target.Host + upPath
						if u.RawQuery != "" {
							newRef += "?" + u.RawQuery
						}
						if u.Fragment != "" {
							newRef += "#" + u.Fragment
						}
						appLogger.TraceTag("proxy_rewrite", "Rewriting Referer header", "original", ref, "rewritten", newRef)
						req.Header.Set("Referer", newRef)
					}
				}
			}

			if origOrigin := pr.In.Header.Get("Origin"); origOrigin != "" {
				newOrigin := target.Scheme + "://" + target.Host
				appLogger.TraceTag("proxy_rewrite", "Rewriting Origin header", "original", origOrigin, "rewritten", newOrigin)
				req.Header.Set("Origin", newOrigin)
			}

			// Add Authorization for Basic auth
			// cr.Password is already plaintext (fetched from server or decrypted locally)
			if cr, err := getCreds(serial); err == nil && cr != nil && cr.AuthType == "basic" && cr.AutoLogin && cr.Password != "" {
				userpass := cr.Username + ":" + cr.Password
				req.Header.Set("Authorization", "Basic "+basicAuth(userpass))
			}

			// Attach cookies for form auth
			// Double-check jar is valid to prevent race conditions
			if capturedJar != nil && target != nil {
				// Safely get cookies with nil check
				func() {
					defer func() {
						if r := recover(); r != nil {
							appLogger.Warn("Proxy: panic getting cookies", "error", fmt.Sprintf("%v", r))
						}
					}()
					if cookies := capturedJar.Cookies(target); len(cookies) > 0 {
						appLogger.Debug("Proxy Rewrite: attaching cookies", "path", req.URL.Path, "cookie_count", len(cookies))
						for _, c := range cookies {
							appLogger.Debug("Proxy Rewrite: adding cookie", "name", c.Name, "value_length", len(c.Value))
							req.AddCookie(c)
						}
					} else {
						appLogger.Debug("Proxy Rewrite: no cookies to attach", "path", req.URL.Path, "has_jar", capturedJar != nil)
					}
				}()
			} else {
				appLogger.Debug("Proxy Rewrite: skipping cookies", "has_jar", capturedJar != nil, "has_target", target != nil, "path", req.URL.Path)
			}
		}

		// Configure transport - use USB transport for USB devices, HTTP transport for network
		if usbTransport != nil {
			rproxy.Transport = usbTransport
		} else {
			rproxy.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{
					// #nosec G402 -- InsecureSkipVerify intentionally enabled:
					// Network printers commonly use self-signed SSL certificates.
					// This reverse proxy connects to printer web interfaces on local networks.
					InsecureSkipVerify: true,
				},
				MaxIdleConns:          10,
				IdleConnTimeout:       60 * time.Second,
				DisableCompression:    false,
				DisableKeepAlives:     false,
				ResponseHeaderTimeout: 30 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   15 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			}
		}

		appLogger.TraceTag("proxy_request", "Proxy request", "method", r.Method, "path", originalPath, "prefix", proxyPrefix, "target", target.String(), "target_path", targetPath)

		// Modify response to rewrite URLs in content and headers
		rproxy.ModifyResponse = func(resp *http.Response) error {
			if isKyoceraModelScript(targetPath) {
				resp.Header.Set("Content-Type", "application/javascript")
			}

			// Rewrite Set-Cookie headers to include the proxy path
			// This ensures the browser stores cookies and includes them in iframe requests
			if cookies := resp.Cookies(); len(cookies) > 0 {
				resp.Header.Del("Set-Cookie")
				// Get HTTPS status from context
				isHTTPS := false
				if resp.Request != nil && resp.Request.Context() != nil {
					if v := resp.Request.Context().Value(isHTTPSContextKey); v != nil {
						isHTTPS = v.(bool)
					}
				}
				for _, cookie := range cookies {
					// Rewrite cookie path to be relative to proxy prefix
					if cookie.Path == "" || cookie.Path == "/" {
						cookie.Path = proxyPrefix + "/"
					} else if !strings.HasPrefix(cookie.Path, proxyPrefix) {
						cookie.Path = proxyPrefix + cookie.Path
					}
					// Clear domain since we're proxying to a different host
					cookie.Domain = ""
					// Set Secure flag based on connection type
					// If agent is accessed via HTTPS, keep Secure=true; if HTTP, clear it
					cookie.Secure = isHTTPS
					// Set SameSite to Lax to allow iframe requests
					if cookie.SameSite == 0 {
						cookie.SameSite = http.SameSiteLaxMode
					}
					resp.Header.Add("Set-Cookie", cookie.String())
					appLogger.Debug("Proxy: rewriting Set-Cookie for browser", "name", cookie.Name, "path", cookie.Path, "secure", cookie.Secure)
				}
			}

			// Rewrite Location header for redirects to stay within proxy path
			if loc := resp.Header.Get("Location"); loc != "" {
				if locURL, err := url.Parse(loc); err == nil {
					// Rewrite relative or same-host absolute URLs
					if locURL.Host == "" || locURL.Host == target.Host {
						newPath := locURL.Path
						if newPath == "" {
							newPath = "/"
						}
						newLoc := proxyPrefix + newPath
						if locURL.RawQuery != "" {
							newLoc += "?" + locURL.RawQuery
						}
						if locURL.Fragment != "" {
							newLoc += "#" + locURL.Fragment
						}
						resp.Header.Set("Location", newLoc)
					}
				}
			}

			// Strip headers that prevent iframe embedding
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("Content-Security-Policy")

			// Rewrite HTML/CSS/JS content to fix relative URLs
			contentType := resp.Header.Get("Content-Type")
			shouldRewrite := strings.Contains(contentType, "text/html") ||
				strings.Contains(contentType, "text/css") ||
				strings.Contains(contentType, "application/javascript") ||
				strings.Contains(contentType, "text/javascript") ||
				strings.Contains(contentType, "application/x-javascript")

			if shouldRewrite {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				resp.Body.Close()

				content := string(body)
				appLogger.TraceTag("proxy_body_rewrite", "Rewriting response body", "content_type", contentType, "original_size", len(body), "path", targetPath)
				isHTML := strings.Contains(contentType, "text/html")
				isCSS := strings.Contains(contentType, "text/css")

				// Rewrite common URL patterns in HTML/CSS/JS
				// Fix absolute paths: href="/path" -> href="/proxy/SERIAL/path"
				content = strings.ReplaceAll(content, `href="/"`, `href="`+proxyPrefix+`/"`)
				content = strings.ReplaceAll(content, `href='/'`, `href='`+proxyPrefix+`/'`)
				content = strings.ReplaceAll(content, `src="/"`, `src="`+proxyPrefix+`/"`)
				content = strings.ReplaceAll(content, `src='/'`, `src='`+proxyPrefix+`/'`)
				content = strings.ReplaceAll(content, `action="/"`, `action="`+proxyPrefix+`/"`)
				content = strings.ReplaceAll(content, `action='/'`, `action='`+proxyPrefix+`/'`)

				// Rewrite absolute-path attributes to stay under /proxy/<serial>
				content = strings.ReplaceAll(content, `href="/`, `href="`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `href='/`, `href='`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `src="/`, `src="`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `src='/`, `src='`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `action="/`, `action="`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `action='/`, `action='`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `data-src="/`, `data-src="`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `data-src='/`, `data-src='`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `data-href="/`, `data-href="`+proxyPrefix+`/`)
				content = strings.ReplaceAll(content, `data-href='/`, `data-href='`+proxyPrefix+`/`)

				// Fix CSS url() references: url(/path) and url("/path") and url('/path')
				if isCSS || isHTML {
					// CSS url() references
					content = strings.ReplaceAll(content, `url(/`, `url(`+proxyPrefix+`/`)
					content = strings.ReplaceAll(content, `url("/`, `url("`+proxyPrefix+`/`)
					content = strings.ReplaceAll(content, `url('/`, `url('`+proxyPrefix+`/`)
				}

				// Fix JavaScript location redirects
				content = strings.ReplaceAll(content, `location.href="/"`, `location.href="`+proxyPrefix+`/"`)
				content = strings.ReplaceAll(content, `location.href='/'`, `location.href='`+proxyPrefix+`/'`)
				content = strings.ReplaceAll(content, `window.location="/"`, `window.location="`+proxyPrefix+`/"`)
				content = strings.ReplaceAll(content, `window.location='/'`, `window.location='`+proxyPrefix+`/'`)

				// Add base tag to HTML to help resolve relative URLs
				// IMPORTANT: base must reflect the directory of the UPSTREAM request path
				// (not our incoming /proxy/<serial>/... path) to avoid duplicating the proxy prefix.
				if isHTML {
					if rewritten, ok := rewriteExistingBaseTag(content, proxyPrefix, target.Host); ok {
						content = rewritten
					} else if !strings.Contains(strings.ToLower(content), "<base") {
						// Use the upstream request path from the reverse proxy response
						upstreamPath := "/"
						if resp != nil && resp.Request != nil && resp.Request.URL != nil {
							upstreamPath = resp.Request.URL.Path
						}
						dir := path.Dir(upstreamPath)
						if !strings.HasSuffix(dir, "/") {
							dir += "/"
						}
						baseHref := proxyPrefix + dir
						baseTag := "<base href=\"" + baseHref + "\">"
						contentLower := strings.ToLower(content)
						if idx := strings.Index(contentLower, "<head>"); idx != -1 {
							content = content[:idx+6] + baseTag + content[idx+6:]
						} else if idx := strings.Index(contentLower, "<head "); idx != -1 {
							// Find end of <head ...> tag
							if endIdx := strings.Index(content[idx:], ">"); endIdx != -1 {
								insertPos := idx + endIdx + 1
								content = content[:insertPos] + baseTag + content[insertPos:]
							}
						}
					}
				}

				newBody := []byte(content)

				// Detect if we got a login page despite having cached session
				// This means the session was invalidated (user logged out)
				if isHTML && capturedJar != nil {
					contentLower := strings.ToLower(content)
					// Check for common login page indicators
					hasLoginForm := strings.Contains(contentLower, "type=\"password\"") ||
						strings.Contains(contentLower, "type='password'")
					hasLoginKeywords := strings.Contains(contentLower, "login") ||
						strings.Contains(contentLower, "password") ||
						strings.Contains(contentLower, "username") ||
						strings.Contains(contentLower, "sign in")

					// If this looks like a login page, clear the cached session
					if hasLoginForm && hasLoginKeywords {
						appLogger.Info("Proxy: detected login page - clearing cached session (likely logged out)", "serial", serial)
						proxySessionCache.Clear(serial)
					}
				}

				resp.Body = io.NopCloser(bytes.NewReader(newBody))
				// We've rewritten the body; ensure Content-Encoding is cleared and length matches
				resp.Header.Del("Content-Encoding")
				resp.ContentLength = int64(len(newBody))
				resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(newBody)))
			}

			// Cache static resources for performance (printers are very slow)
			if isStaticResource && resp.StatusCode == http.StatusOK {
				// Read the body to cache it
				body, err := io.ReadAll(resp.Body)
				if err == nil {
					resp.Body.Close()
					// Cache for 15 minutes
					cacheKey := serial + ":" + targetPath
					staticCache.Set(cacheKey, body, resp.Header.Get("Content-Type"), resp.Header.Clone(), 15*time.Minute)
					appLogger.Debug("Proxy: cached static resource", "serial", serial, "path", targetPath, "size", len(body))
					// Restore the body for the response
					resp.Body = io.NopCloser(bytes.NewReader(body))
					resp.ContentLength = int64(len(body))
				}
			}

			return nil
		}

		// Add error handler for proxy failures
		rproxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			appLogger.WarnRateLimited("proxy_error_"+serial, 1*time.Minute, "Proxy error", "serial", serial, "error", err.Error())
			if err == context.DeadlineExceeded || r.Context().Err() == context.DeadlineExceeded {
				http.Error(w, "Printer did not respond within 45 seconds. The device may be busy, turned off, or its web interface may be disabled.", http.StatusGatewayTimeout)
			} else {
				http.Error(w, fmt.Sprintf("Proxy connection failed: %v", err), http.StatusBadGateway)
			}
		}

		// Serve the proxied request with response diagnostics
		lrw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		// Ensure we always log completion/timeout via defer
		defer func() {
			dur := time.Since(start)
			if dur > 30*time.Second {
				appLogger.Warn("Proxy slow/timeout", "serial", serial, "status", lrw.status, "bytes", lrw.bytes, "duration_ms", dur.Milliseconds(), "path", originalPath)
			} else if lrw.status >= 500 {
				appLogger.WarnRateLimited("proxy_upstream_"+serial, 1*time.Minute, "Proxy upstream error", "serial", serial, "status", lrw.status, "bytes", lrw.bytes, "duration_ms", dur.Milliseconds(), "path", originalPath)
			} else {
				appLogger.TraceTag("proxy_response", "Proxy completed", "serial", serial, "status", lrw.status, "bytes", lrw.bytes, "duration_ms", dur.Milliseconds(), "path", originalPath)
			}
		}()

		rproxy.ServeHTTP(lrw, r)
	})

	// List merged device profiles (using storage interface)
	http.HandleFunc("/devices/list", func(w http.ResponseWriter, r *http.Request) {
		// List only saved devices (is_saved=true)
		saved := true
		devices, err := deviceStore.List(context.Background(), storage.DeviceFilter{IsSaved: &saved})
		if err != nil {
			http.Error(w, "failed to list devices: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Format for compatibility with existing frontend
		out := []map[string]interface{}{}
		for _, device := range devices {
			// Convert to PrinterInfo for compatibility
			pi := storage.DeviceToPrinterInfo(device)
			out = append(out, map[string]interface{}{
				"serial":       device.Serial,
				"path":         device.Serial + ".json", // For compatibility
				"printer_info": pi,
				"info":         pi, // Alias for compatibility
				"asset_number": device.AssetNumber,
				"location":     device.Location,
				"web_ui_url":   device.WebUIURL,
				// New unified device type fields
				"device_type":        device.DeviceType,
				"source_type":        device.SourceType,
				"is_usb":             device.IsUSB,
				"initial_page_count": device.InitialPageCount,
				"port_name":          device.PortName,
				"driver_name":        device.DriverName,
				"is_default":         device.IsDefault,
				"is_shared":          device.IsShared,
				"spooler_status":     device.SpoolerStatus,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// Get a merged device profile by serial. /devices/get?serial=SERIAL
	http.HandleFunc("/devices/get", func(w http.ResponseWriter, r *http.Request) {
		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}

		// Try database first
		ctx := context.Background()
		device, err := deviceStore.Get(ctx, serial)
		if err == nil {
			// Convert fields directly for response

			// Fetch latest metrics for this device
			var pageCount int
			var tonerLevels map[string]interface{}
			if snapshot, err := deviceStore.GetLatestMetrics(ctx, device.Serial); err == nil && snapshot != nil {
				pageCount = snapshot.PageCount
				tonerLevels = snapshot.TonerLevels
			}

			// If metrics do not contain toner_levels, try to synthesize from RawData
			if len(tonerLevels) == 0 && device.RawData != nil {
				// If raw_data already contains a structured toner_levels map, use it
				if tl, ok := device.RawData["toner_levels"].(map[string]interface{}); ok && len(tl) > 0 {
					tonerLevels = tl
				} else {
					// Otherwise, look for per-color keys and build a map
					tl := map[string]interface{}{}
					if v, ok := device.RawData["toner_level_black"].(float64); ok {
						tl["Black"] = int(v)
					} else if v, ok := device.RawData["toner_level_black"].(int); ok {
						tl["Black"] = v
					}
					if v, ok := device.RawData["toner_level_cyan"].(float64); ok {
						tl["Cyan"] = int(v)
					} else if v, ok := device.RawData["toner_level_cyan"].(int); ok {
						tl["Cyan"] = v
					}
					if v, ok := device.RawData["toner_level_magenta"].(float64); ok {
						tl["Magenta"] = int(v)
					} else if v, ok := device.RawData["toner_level_magenta"].(int); ok {
						tl["Magenta"] = v
					}
					if v, ok := device.RawData["toner_level_yellow"].(float64); ok {
						tl["Yellow"] = int(v)
					} else if v, ok := device.RawData["toner_level_yellow"].(int); ok {
						tl["Yellow"] = v
					}
					if len(tl) > 0 {
						tonerLevels = tl
					}
				}
			}

			// Create response with all device fields + printer_info for compatibility
			response := map[string]interface{}{
				"serial":          device.Serial,
				"ip":              device.IP,
				"manufacturer":    device.Manufacturer,
				"model":           device.Model,
				"hostname":        device.Hostname,
				"firmware":        device.Firmware,
				"mac_address":     device.MACAddress,
				"subnet_mask":     device.SubnetMask,
				"gateway":         device.Gateway,
				"dns_servers":     device.DNSServers,
				"dhcp_server":     device.DHCPServer,
				"page_count":      pageCount,
				"toner_levels":    tonerLevels,
				"consumables":     device.Consumables,
				"status_messages": device.StatusMessages,
				"asset_number":    device.AssetNumber,
				"location":        device.Location,
				"web_ui_url":      device.WebUIURL,
				"last_seen":       device.LastSeen,
				"created_at":      device.CreatedAt,
				"first_seen":      device.FirstSeen,
				"is_saved":        device.IsSaved,

				// New unified device type fields
				"device_type":        device.DeviceType,
				"source_type":        device.SourceType,
				"is_usb":             device.IsUSB,
				"initial_page_count": device.InitialPageCount,
				"port_name":          device.PortName,
				"driver_name":        device.DriverName,
				"is_default":         device.IsDefault,
				"is_shared":          device.IsShared,
				"spooler_status":     device.SpoolerStatus,

				// Include RawData if present for extended fields
				"raw_data": device.RawData,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
			return
		}
		// Not found in database
		http.Error(w, "not found", http.StatusNotFound)
	})

	// Canonical device profile endpoint (device metadata + latest metrics).
	// GET /api/devices/profile?serial=SERIAL
	// This avoids compatibility/merged fields in legacy /devices/get.
	http.HandleFunc("/api/devices/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		device, err := deviceStore.Get(ctx, serial)
		if err != nil {
			if err == storage.ErrNotFound {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to get device: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var snapshot *storage.MetricsSnapshot
		if s, err := deviceStore.GetLatestMetrics(ctx, serial); err == nil {
			snapshot = s
		} else if err != storage.ErrNotFound {
			http.Error(w, "failed to get latest metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"device":         device,
			"latest_metrics": snapshot,
		})
	})

	// POST /api/devices/initial-page-count - Set initial page count baseline for audit trail
	http.HandleFunc("/api/devices/initial-page-count", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Serial   string `json:"serial"`
			Count    int    `json:"count"`
			Reason   string `json:"reason,omitempty"`
			Username string `json:"username,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}
		if req.Count < 0 {
			http.Error(w, "count must be non-negative", http.StatusBadRequest)
			return
		}

		// Default username if not provided
		if req.Username == "" {
			req.Username = "system"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		if err := deviceStore.SetInitialPageCount(ctx, req.Serial, req.Count, req.Username, req.Reason); err != nil {
			if err == storage.ErrNotFound {
				http.Error(w, "device not found", http.StatusNotFound)
				return
			}
			appLogger.Error("Failed to set initial page count", "serial", req.Serial, "error", err)
			http.Error(w, "failed to set initial page count: "+err.Error(), http.StatusInternalServerError)
			return
		}

		appLogger.Info("Initial page count set", "serial", req.Serial, "count", req.Count, "by", req.Username)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"serial":  req.Serial,
			"count":   req.Count,
		})
	})

	// GET /api/devices/audit - Get page count audit history for a device
	http.HandleFunc("/api/devices/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		// Optional limit parameter (default: 100)
		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		audits, err := deviceStore.GetPageCountAudit(ctx, serial, limit)
		if err != nil {
			appLogger.Error("Failed to get page count audit", "serial", serial, "error", err)
			http.Error(w, "failed to get audit history: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"serial": serial,
			"audits": audits,
			"count":  len(audits),
		})
	})

	// GET /api/devices/usage - Get page count usage since initial baseline
	http.HandleFunc("/api/devices/usage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		usage, initial, current, err := deviceStore.GetPageCountUsage(ctx, serial)
		if err != nil {
			if err == storage.ErrNotFound {
				http.Error(w, "device not found", http.StatusNotFound)
				return
			}
			appLogger.Error("Failed to get page count usage", "serial", serial, "error", err)
			http.Error(w, "failed to get usage: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"serial":             serial,
			"usage":              usage,
			"initial_page_count": initial,
			"current_page_count": current,
		})
	})

	// Save a device by marking it as saved. POST { serial: "SERIAL" }
	http.HandleFunc("/devices/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Serial string `json:"serial"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		if err := deviceStore.MarkSaved(ctx, req.Serial); err != nil {
			appLogger.Error("Failed to save device", "serial", req.Serial, "error", err)
			http.Error(w, "failed to save device: "+err.Error(), http.StatusInternalServerError)
			return
		}

		appLogger.Info("Device marked as saved", "serial", req.Serial)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "saved",
			"serial": req.Serial,
		})
	})

	// Save all discovered devices (marks all visible unsaved devices as saved)
	http.HandleFunc("/devices/save/all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		ctx := context.Background()
		count, err := deviceStore.MarkAllSaved(ctx)
		if err != nil {
			appLogger.Error("Failed to save all devices", "error", err)
			http.Error(w, "failed to save all devices: "+err.Error(), http.StatusInternalServerError)
			return
		}

		appLogger.Info("Marked devices as saved", "count", count)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "saved",
			"count":  count,
		})
	})

	// Delete a device profile by serial. POST { serial: "SERIAL" }
	http.HandleFunc("/devices/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Serial string `json:"serial"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}

		// Sanitize serial to prevent path traversal attacks
		safeSerial := filepath.Base(req.Serial)
		if safeSerial == "." || safeSerial == ".." || safeSerial != req.Serial {
			http.Error(w, "invalid serial number", http.StatusBadRequest)
			return
		}

		// Try database first
		ctx := context.Background()
		deletedFromDB := false

		err := deviceStore.Delete(ctx, safeSerial)
		if err == nil {
			deletedFromDB = true
			appLogger.Info("Deleted device from database", "serial", safeSerial)
		} else if err != storage.ErrNotFound {
			appLogger.Error("Database delete error", "error", err.Error())
			// Continue to file delete as fallback
		}

		// Fallback: delete JSON file (using sanitized serial) if DB delete failed
		if !deletedFromDB {
			p := filepath.Join(".", "logs", "devices", safeSerial+".json")
			if err := os.Remove(p); err != nil {
				if os.IsNotExist(err) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		// Notify the server about device deletion via WebSocket (if connected)
		// Skip notification if this delete was initiated by the server (via proxy)
		// to avoid a race condition where both the HTTP handler and WebSocket handler
		// try to delete from server storage simultaneously.
		if r.Header.Get("X-PrintMaster-Server-Request") == "" {
			go notifyServerDeviceDeleted(safeSerial)
		}

		// Note: Device will naturally be re-discovered during next scan if still on network
		// No need to immediately re-scan as this defeats the purpose of deletion

		w.WriteHeader(http.StatusOK)
	})

	// Auth endpoints leverage agentAuth manager for local session enforcement
	http.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if agentAuth == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		agentAuth.handleAuthMe(w, r)
	})

	http.HandleFunc("/api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if agentAuth == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
		agentAuth.handleAuthLogout(w, r)
	})

	http.HandleFunc("/api/v1/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if agentAuth == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		agentAuth.handleAuthCallback(w, r)
	})

	http.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if agentAuth == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		agentAuth.handleAuthLogin(w, r)
	})

	http.HandleFunc("/api/v1/auth/options", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if agentAuth == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agentAuth.optionsPayload())
	})

	// Version endpoint
	http.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version":    Version,
			"build_time": BuildTime,
			"git_commit": GitCommit,
			"build_type": BuildType,
			"go_version": runtime.Version(),
			"os":         runtime.GOOS,
			"arch":       runtime.GOARCH,
		})
	})

	// GET /api/jobs/:id - Get status of a background job
	http.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		// Extract job ID from path
		jobID := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
		if jobID == "" {
			http.Error(w, "job_id required", http.StatusBadRequest)
			return
		}

		job := getJob(jobID)
		if job == nil {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(job)
	})

	// Auto-update status and control endpoint
	http.HandleFunc("/api/autoupdate/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		w.Header().Set("Content-Type", "application/json")

		if manager == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"enabled":  false,
				"reason":   "not initialized (no server connection)",
				"status":   "disabled",
				"platform": agent.GetPlatformInfo(),
				"arch":     runtime.GOARCH,
			})
			return
		}

		status := manager.Status()
		json.NewEncoder(w).Encode(status)
	})

	// Trigger an immediate update check
	http.HandleFunc("/api/autoupdate/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			if appLogger != nil {
				appLogger.Info("Update check requested but auto-update manager not initialized (check server connection)")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "auto-update not available",
				"message": "Agent must be connected to a server for updates. Check server configuration.",
			})
			return
		}

		if appLogger != nil {
			appLogger.Info("Manual update check triggered via API")
		}

		// Run check in background with a reasonable timeout
		go func() {
			checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := manager.CheckNow(checkCtx); err != nil && appLogger != nil {
				appLogger.Warn("Manual update check failed", "error", err)
			} else if appLogger != nil {
				appLogger.Info("Manual update check completed")
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "check_triggered",
			"message": "Update check has been scheduled",
		})
	})

	// Force reinstall the latest build regardless of current version
	http.HandleFunc("/api/autoupdate/force", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			if appLogger != nil {
				appLogger.Info("Force update requested but auto-update manager not initialized (check server connection)")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "auto-update not available",
				"message": "Agent must be connected to a server for updates. Check server configuration.",
			})
			return
		}

		var payload struct {
			Reason string `json:"reason"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && err != io.EOF {
				http.Error(w, "invalid JSON payload", http.StatusBadRequest)
				return
			}
		}

		reason := strings.TrimSpace(payload.Reason)
		if reason == "" {
			reason = "agent_ui_force_reinstall"
		}

		if appLogger != nil {
			appLogger.Info("Force reinstall requested via API", "reason", reason)
		}

		go func(reason string) {
			runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			if err := manager.ForceInstallLatest(runCtx, reason); err != nil {
				if appLogger != nil {
					appLogger.Warn("Force reinstall failed", "error", err, "reason", reason)
				}
			} else if appLogger != nil {
				appLogger.Info("Force reinstall completed successfully", "reason", reason)
			}
		}(reason)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "force_triggered",
			"message": "Forced reinstall has been scheduled",
		})
	})

	// Cancel an in-progress update
	http.HandleFunc("/api/autoupdate/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		autoUpdateManagerMu.RLock()
		manager := autoUpdateManager
		autoUpdateManagerMu.RUnlock()

		if manager == nil {
			http.Error(w, "auto-update not available", http.StatusServiceUnavailable)
			return
		}

		if !manager.Cancel() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Cannot cancel update at this stage (may already be restarting or not in progress)",
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "cancelled",
			"message": "Update cancellation requested",
		})
	})

	// Metrics history endpoints
	http.HandleFunc("/api/devices/metrics/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		snapshot, err := deviceStore.GetLatestMetrics(ctx, serial)
		if err != nil {
			if err == storage.ErrNotFound {
				http.Error(w, "no metrics found", http.StatusNotFound)
			} else {
				http.Error(w, "failed to get metrics: "+err.Error(), http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshot)
	})

	// GET /api/devices/metrics/bounds?serial=SERIAL
	// Returns min/max timestamps (across all tiers) without fetching the full series.
	http.HandleFunc("/api/devices/metrics/bounds", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		store, ok := deviceStore.(*storage.SQLiteStore)
		if !ok || store == nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		minTS, maxTS, total, err := store.GetTieredMetricsBounds(ctx, serial)
		if err != nil {
			if err == storage.ErrNotFound {
				http.Error(w, "no metrics found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to get metrics bounds: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"serial":        serial,
			"min_timestamp": minTS.UTC().Format(time.RFC3339Nano),
			"max_timestamp": maxTS.UTC().Format(time.RFC3339Nano),
			"points":        total,
		})
	})

	http.HandleFunc("/api/devices/metrics/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		serial := r.URL.Query().Get("serial")
		if serial == "" {
			http.Error(w, "serial parameter required", http.StatusBadRequest)
			return
		}

		// Parse maxPoints (default 200, reasonable for chart display)
		maxPoints := 200
		if mp := r.URL.Query().Get("maxPoints"); mp != "" {
			if n, err := strconv.Atoi(mp); err == nil && n > 0 {
				maxPoints = n
				if maxPoints > 10000 { // Cap at 10k to prevent memory issues
					maxPoints = 10000
				}
			}
		}

		// Raw mode disables downsampling
		rawMode := r.URL.Query().Get("raw") == "true"

		// Support both period-based and custom date range queries
		var since, until time.Time
		now := time.Now()

		// Check for custom date range first
		sinceStr := r.URL.Query().Get("since")
		untilStr := r.URL.Query().Get("until")

		if sinceStr != "" && untilStr != "" {
			// Custom date range
			var err error
			since, err = time.Parse(time.RFC3339, sinceStr)
			if err != nil {
				http.Error(w, "invalid since parameter (use RFC3339 format)", http.StatusBadRequest)
				return
			}
			until, err = time.Parse(time.RFC3339, untilStr)
			if err != nil {
				http.Error(w, "invalid until parameter (use RFC3339 format)", http.StatusBadRequest)
				return
			}
		} else {
			// Period-based range
			period := r.URL.Query().Get("period")
			if period == "" {
				period = "week" // default
			}

			until = now
			switch period {
			case "day":
				since = now.Add(-24 * time.Hour)
			case "week":
				since = now.Add(-7 * 24 * time.Hour)
			case "month":
				since = now.Add(-30 * 24 * time.Hour)
			case "year":
				since = now.Add(-365 * 24 * time.Hour)
			default:
				since = now.Add(-7 * 24 * time.Hour) // default to week
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		// Use tiered metrics retrieval so the store returns the best-resolution
		// data for the requested time range (raw/hourly/daily/monthly).
		snapshots, err := deviceStore.GetTieredMetricsHistory(ctx, serial, since, until)
		if err != nil {
			// Log the error server-side to aid debugging (will appear in agent logs)
			agent.Error(fmt.Sprintf("Failed to get metrics history: serial=%s error=%v", serial, err))
			http.Error(w, "failed to get metrics history: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Downsample if needed and not in raw mode
		if !rawMode && len(snapshots) > maxPoints {
			snapshots = downsampleAgentMetrics(snapshots, maxPoints)
		}

		if agent.DebugEnabled {
			agent.Debug(fmt.Sprintf("GET /api/devices/metrics/history - serial=%s, since=%s, until=%s, found=%d snapshots, raw=%v",
				serial, since.Format(time.RFC3339), until.Format(time.RFC3339), len(snapshots), rawMode))
			if len(snapshots) > 0 {
				first := snapshots[0]
				last := snapshots[len(snapshots)-1]
				agent.Debug(fmt.Sprintf("  First: timestamp=%s, page_count=%d", first.Timestamp.Format(time.RFC3339), first.PageCount))
				agent.Debug(fmt.Sprintf("  Last: timestamp=%s, page_count=%d", last.Timestamp.Format(time.RFC3339), last.PageCount))
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshots)
	})

	// POST /api/devices/metrics/delete - delete a single metrics row by id (tier optional)
	http.HandleFunc("/api/devices/metrics/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ID   int64  `json:"id"`
			Tier string `json:"tier,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		if req.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if deviceStore == nil {
			http.Error(w, "storage unavailable", http.StatusInternalServerError)
			return
		}

		if err := deviceStore.DeleteMetricByID(ctx, req.Tier, req.ID); err != nil {
			agent.Error(fmt.Sprintf("Failed to delete metrics row: id=%d tier=%s error=%v", req.ID, req.Tier, err))
			http.Error(w, "failed to delete metrics row: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// POST /devices/metrics/collect - Manually collect metrics for a device
	// Supports async mode via ?async=true query param, returns job_id for progress tracking
	http.HandleFunc("/devices/metrics/collect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Serial string `json:"serial"`
			IP     string `json:"ip"`
			Async  bool   `json:"async"` // If true, run in background and return job_id
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// Also check query param for async mode
		if r.URL.Query().Get("async") == "true" {
			req.Async = true
		}

		if req.Serial == "" {
			http.Error(w, "serial required", http.StatusBadRequest)
			return
		}

		// Check if this is a USB device - if so, use USB metrics collection
		var device *storage.Device
		if deviceStore != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var err error
			device, err = deviceStore.Get(ctx, req.Serial)
			if err == nil && device != nil {
				if req.IP == "" {
					req.IP = device.IP
				}
			}
		}

		// For async mode, run collection in background
		if req.Async {
			jobID := registerJob("metrics_collect")

			// Return job ID immediately
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"job_id":  jobID,
				"status":  "pending",
				"message": "Metrics collection started",
			})

			// Run collection in background
			go collectMetricsAsync(jobID, req.Serial, req.IP, device)
			return
		}

		// Synchronous mode (original behavior)
		// Check for USB device type
		if device != nil && (device.DeviceType == "usb" || device.IsUSB) && usbProxySupported() {
			// USB device - use USB proxy metrics collection
			appLogger.Info("Collecting USB metrics", "serial", req.Serial)

			storageSnapshot, ok := usbProxyMetricsSnapshot(r.Context(), req.Serial)
			if !ok {
				appLogger.Warn("USB metrics collection failed", "serial", req.Serial)
				http.Error(w, "USB metrics collection failed", http.StatusInternalServerError)
				return
			}

			// Save to database
			saveCtx, saveCancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer saveCancel()
			if err := deviceStore.SaveMetricsSnapshot(saveCtx, storageSnapshot); err != nil {
				appLogger.Warn("Failed to save USB metrics", "serial", req.Serial, "error", err.Error())
				http.Error(w, "failed to save USB metrics: "+err.Error(), http.StatusInternalServerError)
				return
			}

			appLogger.Info("USB metrics collected and saved", "serial", req.Serial, "total_pages", storageSnapshot.PageCount)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"serial":      req.Serial,
				"total_pages": storageSnapshot.PageCount,
				"source":      "usb",
				"saved":       true,
			})
			return
		}

		// Network device - use SNMP metrics collection (original code)
		if req.IP == "" {
			http.Error(w, "ip required", http.StatusBadRequest)
			return
		}

		// Collect metrics snapshot using new scanner
		metricsCtx, cancelMetrics := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancelMetrics()
		vendorHint := ""
		// Get vendor hint from database if possible
		if deviceStore != nil {
			device, getErr := deviceStore.Get(metricsCtx, req.Serial)
			if getErr == nil && device != nil {
				vendorHint = device.Manufacturer
			}
		}

		// Use new scanner for metrics collection
		appLogger.Info("Collecting metrics", "serial", req.Serial, "ip", req.IP, "vendor_hint", vendorHint)
		agentSnapshot, err := CollectMetrics(metricsCtx, req.IP, req.Serial, vendorHint, 10)
		if err != nil {
			appLogger.Warn("Metrics collection failed", "serial", req.Serial, "ip", req.IP, "error", err.Error())
			if agent.DebugEnabled {
				agent.Debug(fmt.Sprintf("POST /devices/metrics/collect - FAILED for %s (%s): %s", req.Serial, req.IP, err.Error()))
			}
			http.Error(w, "failed to collect metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Convert to storage type
		storageSnapshot := &storage.MetricsSnapshot{}
		storageSnapshot.Serial = agentSnapshot.Serial
		storageSnapshot.PageCount = agentSnapshot.PageCount
		storageSnapshot.ColorPages = agentSnapshot.ColorPages
		storageSnapshot.MonoPages = agentSnapshot.MonoPages
		storageSnapshot.ScanCount = agentSnapshot.ScanCount
		storageSnapshot.TonerLevels = agentSnapshot.TonerLevels
		storageSnapshot.FaxPages = agentSnapshot.FaxPages
		storageSnapshot.CopyPages = agentSnapshot.CopyPages
		storageSnapshot.OtherPages = agentSnapshot.OtherPages
		storageSnapshot.CopyMonoPages = agentSnapshot.CopyMonoPages
		storageSnapshot.CopyFlatbedScans = agentSnapshot.CopyFlatbedScans
		storageSnapshot.CopyADFScans = agentSnapshot.CopyADFScans
		storageSnapshot.FaxFlatbedScans = agentSnapshot.FaxFlatbedScans
		storageSnapshot.FaxADFScans = agentSnapshot.FaxADFScans
		storageSnapshot.ScanToHostFlatbed = agentSnapshot.ScanToHostFlatbed
		storageSnapshot.ScanToHostADF = agentSnapshot.ScanToHostADF
		storageSnapshot.DuplexSheets = agentSnapshot.DuplexSheets
		storageSnapshot.JamEvents = agentSnapshot.JamEvents
		storageSnapshot.ScannerJamEvents = agentSnapshot.ScannerJamEvents

		// Save to database
		saveCtx, cancelSave := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancelSave()
		if err := deviceStore.SaveMetricsSnapshot(saveCtx, storageSnapshot); err != nil {
			http.Error(w, "failed to save metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}

		appLogger.Info("Metrics collected successfully",
			"serial", req.Serial,
			"ip", req.IP,
			"page_count", agentSnapshot.PageCount,
			"color_pages", agentSnapshot.ColorPages,
			"mono_pages", agentSnapshot.MonoPages,
			"scan_count", agentSnapshot.ScanCount,
			"fax_pages", agentSnapshot.FaxPages,
			"copy_pages", agentSnapshot.CopyPages,
			"duplex_sheets", agentSnapshot.DuplexSheets,
			"jam_events", agentSnapshot.JamEvents)

		if agent.DebugEnabled {
			agent.Debug(fmt.Sprintf("POST /devices/metrics/collect - SUCCESS for %s (%s): PageCount=%d, ColorPages=%d, MonoPages=%d, ScanCount=%d",
				req.Serial, req.IP, agentSnapshot.PageCount, agentSnapshot.ColorPages, agentSnapshot.MonoPages, agentSnapshot.ScanCount))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "ok",
			"serial":      req.Serial,
			"page_count":  agentSnapshot.PageCount,
			"color_pages": agentSnapshot.ColorPages,
			"mono_pages":  agentSnapshot.MonoPages,
			"scan_count":  agentSnapshot.ScanCount,
		})
	})

	// vendor add handler moved to mib_suggestions_api.go to centralize candidate APIs

	// Expose trace tags under /settings/trace_tags (moved from legacy /dev_settings)
	http.HandleFunc("/settings/trace_tags", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			tags := appLogger.GetTraceTags()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"tags": tags})
			return
		}

		if r.Method == http.MethodPost {
			var req struct {
				Tags map[string]bool `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}

			appLogger.SetTraceTags(req.Tags)

			// Persist to config store for restarts
			if agentConfigStore != nil {
				_ = agentConfigStore.SetConfigValue("trace_tags", req.Tags)
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// Request the current server-managed settings immediately.
	http.HandleFunc("/settings/reload-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		uploadWorkerMu.RLock()
		worker := uploadWorker
		uploadWorkerMu.RUnlock()
		if worker == nil {
			http.Error(w, "server connection unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := worker.ReloadSettings(ctx); err != nil {
			http.Error(w, "failed to reload server settings: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
	})

	// Unified settings endpoint to get/save all settings at once
	http.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		if agentConfigStore == nil {
			http.Error(w, "config store unavailable", http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
			snapshot := loadUnifiedSettings(agentConfigStore)
			// Build response with server-managed metadata
			isServerManaged := settingsManager != nil && settingsManager.HasManagedSnapshot()
			resp := map[string]interface{}{
				"discovery":        snapshot.Discovery,
				"snmp":             snapshot.SNMP,
				"features":         snapshot.Features,
				"spooler":          snapshot.Spooler,
				"logging":          snapshot.Logging,
				"web":              snapshot.Web,
				"server_managed":   isServerManaged,
				"managed_sections": []string{},
			}
			if isServerManaged {
				// When server-managed, discovery/snmp/features/spooler are locked (logging/web are local)
				resp["managed_sections"] = settingsManager.ManagedSections()
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return

		case http.MethodPost:
			var req struct {
				Discovery map[string]interface{} `json:"discovery"`
				SNMP      map[string]interface{} `json:"snmp"`
				Features  map[string]interface{} `json:"features"`
				Spooler   map[string]interface{} `json:"spooler"`
				Logging   map[string]interface{} `json:"logging"`
				Web       map[string]interface{} `json:"web"`
				Reset     bool                   `json:"reset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}

			// Discovery/SNMP/Features/Spooler are fleet-managed sections. When the
			// agent has an active server-managed settings snapshot, reject attempts
			// to change them here instead of silently persisting values that
			// loadUnifiedSettings() will ignore on the next read (which previously
			// made saves look successful but have no lasting effect).
			if settingsManager != nil && settingsManager.HasManagedSnapshot() {
				managedSections := make(map[string]bool)
				for _, section := range settingsManager.ManagedSections() {
					managedSections[section] = true
				}
				var lockedSections []string
				if req.Discovery != nil && managedSections["discovery"] {
					lockedSections = append(lockedSections, "discovery")
				}
				if req.SNMP != nil && managedSections["snmp"] {
					lockedSections = append(lockedSections, "snmp")
				}
				if req.Features != nil && managedSections["features"] {
					lockedSections = append(lockedSections, "features")
				}
				if req.Spooler != nil && managedSections["spooler"] {
					lockedSections = append(lockedSections, "spooler")
				}
				if len(lockedSections) > 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"error":       "cannot modify server-managed settings",
						"locked_keys": lockedSections,
						"reason":      "These sections are managed by the connected server and cannot be edited locally",
					})
					return
				}
			}

			if req.Reset {
				_ = agentConfigStore.SetConfigValue("discovery_settings", map[string]interface{}{})
				_ = agentConfigStore.SetConfigValue("settings", map[string]interface{}{})
				stopAutoDiscover()
				stopLiveMDNS()
				stopLiveWSDiscovery()
				stopLiveSSDP()
				stopSNMPTrap()
				stopLLMNR()
				stopMetricsRescan()
				agent.SetDebugEnabled(false)
				agent.SetDumpParseDebug(false)
				defaults := pmsettings.DefaultSettings()
				if txt, err := agentConfigStore.GetRanges(); err == nil {
					defaults.Discovery.RangesText = txt
				}
				if ipnets, err := agent.GetLocalSubnets(); err == nil && len(ipnets) > 0 {
					defaults.Discovery.DetectedSubnet = ipnets[0].String()
				}
				applyFeaturesSettingsEffects(&defaults.Features)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(defaults)
				return
			}

			current := loadUnifiedSettings(agentConfigStore)

			if req.Discovery != nil {
				updated := current.Discovery
				mapIntoStruct(req.Discovery, &updated)
				if _, ok := req.Discovery["ranges_text"]; ok {
					maxAddrs := 4096
					res, err := agent.ParseRangeText(updated.RangesText, maxAddrs)
					if err != nil {
						http.Error(w, "validation error: "+err.Error(), http.StatusBadRequest)
						return
					}
					if len(res.Errors) > 0 {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(res)
						return
					}
					if err := agentConfigStore.SetRanges(updated.RangesText); err != nil {
						http.Error(w, "failed to save ranges: "+err.Error(), http.StatusInternalServerError)
						return
					}
				}
				discMap := structToMap(updated)
				delete(discMap, "ranges_text")
				delete(discMap, "detected_subnet")
				if err := agentConfigStore.SetConfigValue("discovery_settings", discMap); err != nil {
					http.Error(w, "failed to save discovery settings: "+err.Error(), http.StatusInternalServerError)
					return
				}
				applyDiscoveryEffects(discMap)
				current.Discovery = updated
			}

			// Save all settings to unified envelope
			var envelope map[string]interface{}
			_ = agentConfigStore.GetConfigValue("settings", &envelope)
			if envelope == nil {
				envelope = map[string]interface{}{}
			}

			if req.SNMP != nil {
				updated := current.SNMP
				mapIntoStruct(req.SNMP, &updated)
				envelope["snmp"] = structToMap(updated)
				current.SNMP = updated
			}

			if req.Features != nil {
				updated := current.Features
				mapIntoStruct(req.Features, &updated)
				envelope["features"] = structToMap(updated)
				current.Features = updated
			}

			if req.Spooler != nil {
				updated := current.Spooler
				mapIntoStruct(req.Spooler, &updated)
				envelope["spooler"] = structToMap(updated)
				current.Spooler = updated
				// Apply spooler settings immediately (restart worker if needed)
				applySpoolerSettings(&current.Spooler)
			}

			if req.Logging != nil {
				updated := current.Logging
				mapIntoStruct(req.Logging, &updated)
				envelope["logging"] = structToMap(updated)
				current.Logging = updated
				// Apply log level immediately
				if appLogger != nil && updated.Level != "" {
					if lvl := logger.LevelFromString(updated.Level); lvl >= 0 {
						appLogger.SetLevel(lvl)
						appLogger.Info("Log level changed", "level", updated.Level)
					}
				}
				// Apply debug flags
				agent.SetDebugEnabled(updated.Level == "debug")
				agent.SetDumpParseDebug(updated.DumpParseDebug)
			}

			if req.Web != nil {
				updated := current.Web
				mapIntoStruct(req.Web, &updated)
				envelope["web"] = structToMap(updated)
				current.Web = updated
			}

			if err := agentConfigStore.SetConfigValue("settings", envelope); err != nil {
				http.Error(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
				return
			}

			pmsettings.Sanitize(&current)
			applyFeaturesSettingsEffects(&current.Features)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(current)
			return
		}

		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	http.HandleFunc("/settings/server", func(w http.ResponseWriter, r *http.Request) {
		dataDir, err := config.GetDataDirectory("agent", isService)
		if err != nil {
			http.Error(w, "failed to determine data directory", http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
			status := snapshotServerConnectionStatus(agentConfig, dataDir)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(status)
			return
		case http.MethodDelete:
			if err := disconnectFromServer(agentConfig, dataDir); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			status := snapshotServerConnectionStatus(agentConfig, dataDir)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"status":  status,
			})
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	})

	// Join the central server using a join token issued by the server.
	// Body: {"server_url":"https://central:9443","token":"<raw join token>","ca_path":"/path/to/ca.pem","insecure":false}
	http.HandleFunc("/settings/probe-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			ServerURL string `json:"server_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		result, err := probeServer(r.Context(), in.ServerURL)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	http.HandleFunc("/settings/join", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var in struct {
			ServerURL string `json:"server_url"`
			Token     string `json:"token"`
			CAPath    string `json:"ca_path,omitempty"`
			Insecure  bool   `json:"insecure,omitempty"`
			AgentName string `json:"agent_name,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid json"}`))
			return
		}
		if in.ServerURL == "" || in.Token == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"server_url and token required"}`))
			return
		}

		result, err := performServerJoin(
			r.Context(),
			ctx,
			serverJoinParams{
				ServerURL: in.ServerURL,
				Token:     in.Token,
				CAPath:    in.CAPath,
				Insecure:  in.Insecure,
				AgentName: in.AgentName,
			},
			agentConfig,
			agentConfigStore,
			deviceStore,
			settingsManager,
			appLogger,
			isService,
		)
		if err != nil {
			status := joinErrorStatus(err)
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     true,
			"tenant_id":   result.TenantID,
			"agent_token": result.AgentToken,
		})
	})

	http.HandleFunc("/settings/device-auth/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			ServerURL string `json:"server_url"`
			CAPath    string `json:"ca_path,omitempty"`
			Insecure  bool   `json:"insecure,omitempty"`
			AgentName string `json:"agent_name,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid json"}`))
			return
		}
		serverURL := strings.TrimSpace(in.ServerURL)
		if serverURL == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"server_url required"}`))
			return
		}
		// Validate CA path to prevent path traversal attacks
		caPath := strings.TrimSpace(in.CAPath)
		if caPath != "" {
			// Only allow .pem, .crt, .cer extensions for CA certificates
			ext := strings.ToLower(filepath.Ext(caPath))
			if ext != ".pem" && ext != ".crt" && ext != ".cer" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"ca_path must be a .pem, .crt, or .cer file"}`))
				return
			}
			// Verify the file exists (but don't allow path traversal outside data dir)
			if strings.Contains(caPath, "..") {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"ca_path cannot contain path traversal characters"}`))
				return
			}
		}
		dataDir, err := config.GetDataDirectory("agent", isService)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"failed to determine data directory"}`))
			return
		}
		agentID, err := LoadOrGenerateAgentID(dataDir)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"failed to load or generate agent id"}`))
			return
		}
		agentName := resolveAgentDisplayName(agentConfig, in.AgentName)
		hostname, _ := os.Hostname()
		reqBody := agent.DeviceAuthStartRequest{
			AgentID:      agentID,
			AgentName:    agentName,
			AgentVersion: Version,
			Hostname:     hostname,
			Platform:     agent.GetPlatformInfo(),
		}
		client := agent.NewServerClientWithName(serverURL, agentID, agentName, "", caPath, in.Insecure)
		respBody, err := client.DeviceAuthStart(r.Context(), reqBody)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}
		if respBody == nil {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":"server returned empty response"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":       true,
			"code":          respBody.Code,
			"poll_token":    respBody.PollToken,
			"expires_at":    respBody.ExpiresAt,
			"authorize_url": respBody.AuthorizeURL,
			"agent_id":      agentID,
			"agent_name":    agentName,
		})
	})

	http.HandleFunc("/settings/device-auth/poll", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			ServerURL string `json:"server_url"`
			PollToken string `json:"poll_token"`
			CAPath    string `json:"ca_path,omitempty"`
			Insecure  bool   `json:"insecure,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid json"}`))
			return
		}
		serverURL := strings.TrimSpace(in.ServerURL)
		pollToken := strings.TrimSpace(in.PollToken)
		if serverURL == "" || pollToken == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"server_url and poll_token required"}`))
			return
		}
		// Validate CA path to prevent path traversal attacks
		caPath := strings.TrimSpace(in.CAPath)
		if caPath != "" {
			ext := strings.ToLower(filepath.Ext(caPath))
			if ext != ".pem" && ext != ".crt" && ext != ".cer" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"ca_path must be a .pem, .crt, or .cer file"}`))
				return
			}
			if strings.Contains(caPath, "..") {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"ca_path cannot contain path traversal characters"}`))
				return
			}
		}
		dataDir, err := config.GetDataDirectory("agent", isService)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"failed to determine data directory"}`))
			return
		}
		agentID, err := LoadOrGenerateAgentID(dataDir)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"failed to load or generate agent id"}`))
			return
		}
		agentName := resolveAgentDisplayName(agentConfig, "")
		client := agent.NewServerClientWithName(serverURL, agentID, agentName, "", caPath, in.Insecure)
		respBody, err := client.DeviceAuthPoll(r.Context(), pollToken)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}
		if respBody == nil {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":"server returned empty response"}`))
			return
		}
		out := map[string]interface{}{
			"success": respBody.Success,
			"status":  respBody.Status,
			"code":    respBody.Code,
			"message": respBody.Message,
		}
		if respBody.JoinToken != "" {
			out["join_token"] = respBody.JoinToken
		}
		if respBody.TenantID != "" {
			out["tenant_id"] = respBody.TenantID
		}
		if respBody.AgentName != "" {
			out["agent_name"] = respBody.AgentName
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Legacy subnet scan endpoint (deprecated, use /settings/discovery)
	http.HandleFunc("/settings/subnet_scan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			enabled := true // default to true
			if agentConfigStore != nil {
				var setting struct {
					Enabled bool `json:"enabled"`
				}
				setting.Enabled = true // default
				_ = agentConfigStore.GetConfigValue("subnet_scan_enabled", &setting)
				enabled = setting.Enabled
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"enabled": enabled})
			return
		}
		if r.Method == "POST" {
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if agentConfigStore != nil {
				if err := agentConfigStore.SetConfigValue("subnet_scan_enabled", map[string]bool{"enabled": req.Enabled}); err != nil {
					http.Error(w, "failed to save setting: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// API endpoint to regenerate TLS certificates
	http.HandleFunc("/api/regenerate-certs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get data directory
		dataDir, err := storage.GetDataDir("PrintMaster")
		if err != nil {
			http.Error(w, "failed to get data directory: "+err.Error(), http.StatusInternalServerError)
			return
		}

		certFile := filepath.Join(dataDir, "server.crt")
		keyFile := filepath.Join(dataDir, "server.key")

		// Delete existing certificates
		os.Remove(certFile)
		os.Remove(keyFile)

		// Generate new certificates
		newCertFile, newKeyFile, err := ensureTLSCertificates("", "")
		if err != nil {
			http.Error(w, "failed to generate certificates: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Certificates regenerated successfully. Restart agent to use new certificates.",
			"cert":    newCertFile,
			"key":     newKeyFile,
		})
	})

	// Register local printer (spooler) API handlers
	// Type assert to get LocalPrinterStore interface (SQLiteStore implements both DeviceStore and LocalPrinterStore)
	if localPrinterStore, ok := deviceStore.(storage.LocalPrinterStore); ok {
		// Store globally for runtime settings changes
		globalLocalPrinterStore = localPrinterStore
		RegisterSpoolerHandlers(localPrinterStore)

		// Start spooler worker for USB/local printer tracking (Windows/macOS/Linux via CUPS)
		// Check if spooler tracking is enabled via unified settings
		unified := loadUnifiedSettings(agentConfigStore)
		if unified.Spooler.Enabled {
			pollInterval := time.Duration(unified.Spooler.PollIntervalSeconds) * time.Second
			if pollInterval < 5*time.Second {
				pollInterval = 5 * time.Second
			}
			config := SpoolerWorkerConfig{
				PollInterval:           pollInterval,
				IncludeNetworkPrinters: unified.Spooler.IncludeNetworkPrinters,
				IncludeVirtualPrinters: unified.Spooler.IncludeVirtualPrinters,
				AutoTrackUSB:           true,
				AutoTrackLocal:         false,
			}
			if err := StartSpoolerWorker(localPrinterStore, config, appLogger); err != nil {
				appLogger.Warn("Failed to start spooler worker", "error", err)
			}
		}
		// Ensure spooler worker is stopped on shutdown
		defer StopSpoolerWorker()
	} else {
		appLogger.Warn("Device store does not support local printer operations, spooler tracking disabled")
	}

	// Initialize USB proxy for IPP-USB printers (Windows only)
	// This enables web UI access for USB-connected printers via the same /proxy/ endpoint
	RegisterUSBProxyHandlers()
	if err := InitUSBProxy(appLogger); err != nil {
		appLogger.Warn("Failed to initialize USB proxy", "error", err)
	} else {
		appLogger.Info("USB proxy initialized")
	}
	defer StopUSBProxy()

	// Get HTTP/HTTPS settings
	enableHTTP := true
	enableHTTPS := true
	httpPort := "8080"
	httpsPort := "8443"
	redirectHTTPToHTTPS := false
	customCertPath := ""
	customKeyPath := ""

	// Try to load settings from unified_settings (new format) first, then fall back to legacy
	if agentConfigStore != nil {
		// First try new unified settings (v2 schema)
		unified := loadUnifiedSettings(agentConfigStore)
		// Check if web settings have been loaded (HTTPPort is always set with defaults)
		if unified.Web.HTTPPort != "" {
			enableHTTP = unified.Web.EnableHTTP
			enableHTTPS = unified.Web.EnableHTTPS
			httpPort = unified.Web.HTTPPort
			if unified.Web.HTTPSPort != "" {
				httpsPort = unified.Web.HTTPSPort
			}
			redirectHTTPToHTTPS = unified.Web.RedirectHTTPToHTTPS
			customCertPath = unified.Web.CustomCertPath
			customKeyPath = unified.Web.CustomKeyPath
		} else {
			// Legacy fallback: read from old security_settings key
			var securitySettings map[string]interface{}
			if err := agentConfigStore.GetConfigValue("security_settings", &securitySettings); err == nil {
				if val, ok := securitySettings["enable_http"].(bool); ok {
					enableHTTP = val
				}
				if val, ok := securitySettings["enable_https"].(bool); ok {
					enableHTTPS = val
				}
				if val, ok := securitySettings["http_port"].(string); ok && val != "" {
					httpPort = val
				}
				if val, ok := securitySettings["https_port"].(string); ok && val != "" {
					httpsPort = val
				}
				if val, ok := securitySettings["redirect_http_to_https"].(bool); ok {
					redirectHTTPToHTTPS = val
				}
				if val, ok := securitySettings["custom_cert_path"].(string); ok {
					customCertPath = val
				}
				if val, ok := securitySettings["custom_key_path"].(string); ok {
					customKeyPath = val
				}
			}
		}
	}

	// Load or generate TLS certificates for HTTPS
	certFile, keyFile, err := ensureTLSCertificates(customCertPath, customKeyPath)
	if err != nil {
		appLogger.Error("Failed to setup TLS certificates", "error", err.Error())
		certFile = ""
		keyFile = ""
	}

	// Default to HTTPS if certificates are available
	if certFile == "" || keyFile == "" {
		enableHTTPS = false
		appLogger.Warn("HTTPS disabled: TLS certificates not available")
	}

	// Ensure at least one server is enabled
	if !enableHTTP && !enableHTTPS {
		enableHTTP = true
		appLogger.Warn("Both HTTP and HTTPS disabled in settings, enabling HTTP as fallback")
	}

	rootHandler := http.Handler(http.DefaultServeMux)
	if agentAuth != nil {
		rootHandler = agentAuth.Wrap(rootHandler)
	}

	// Register local handler globally for direct proxy invocation
	// This allows the server to proxy to the agent's web UI without HTTP round-trip
	// The handler is set globally so it's available even if upload worker starts later
	setLocalProxyHandler(rootHandler)

	// Create server instances for graceful shutdown
	var httpServer *http.Server
	var httpsServer *http.Server
	var wg sync.WaitGroup

	// Start HTTP server
	if enableHTTP {
		// Create HTTP server with optional redirect to HTTPS
		var httpHandler http.Handler
		if redirectHTTPToHTTPS && enableHTTPS {
			// Redirect handler using 302 (temporary redirect)
			httpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Build HTTPS URL
				host := r.Host
				// Replace port if it's the HTTP port
				if strings.Contains(host, ":"+httpPort) {
					host = strings.Replace(host, ":"+httpPort, ":"+httpsPort, 1)
				} else if !strings.Contains(host, ":") {
					// No port specified, add HTTPS port
					host = host + ":" + httpsPort
				}

				httpsURL := "https://" + host + r.RequestURI
				// Use 302 (Found) for temporary redirect, not 301 (permanent)
				http.Redirect(w, r, httpsURL, http.StatusFound)
			})
			appLogger.Info("HTTP server will redirect to HTTPS", "httpPort", httpPort, "httpsPort", httpsPort)
		} else {
			// Use default handler (http.DefaultServeMux with all registered routes)
			httpHandler = rootHandler
		}

		httpServer = &http.Server{
			Addr:              ":" + httpPort,
			Handler:           httpHandler,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			appLogger.Info("Starting HTTP server", "port", httpPort)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				appLogger.Error("HTTP server failed", "error", err.Error())
			}
		}()
	}

	// Start HTTPS server
	if enableHTTPS && certFile != "" && keyFile != "" {
		// Load TLS certificate
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			appLogger.Error("Failed to load TLS certificate", "error", err.Error())
		} else {
			tlsCfg := &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}

			httpsServer = &http.Server{
				Handler:           rootHandler,
				ReadTimeout:       30 * time.Second,
				ReadHeaderTimeout: 10 * time.Second,
				WriteTimeout:      120 * time.Second, // USB proxy can be very slow (5-10s per page)
				IdleTimeout:       120 * time.Second,
			}

			wg.Add(1)
			go func() {
				defer wg.Done()

				// Create base TCP listener
				baseListener, err := net.Listen("tcp", ":"+httpsPort)
				if err != nil {
					appLogger.Error("Failed to create HTTPS listener", "error", err.Error())
					return
				}

				// Wrap with HTTP redirect detection (handles http:// requests to HTTPS port)
				redirectListener := newHTTPRedirectListener(baseListener, httpsPort)

				// Wrap with TLS
				tlsListener := tls.NewListener(redirectListener, tlsCfg)

				appLogger.Info("Starting HTTPS server", "port", httpsPort)
				appLogger.Info("HTTP→HTTPS redirect enabled on HTTPS port")

				if err := httpsServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
					appLogger.Error("HTTPS server failed", "error", err.Error())
				}
			}()
		}
	}

	// Wait for shutdown signal
	<-ctx.Done()
	appLogger.Info("Shutdown signal received, stopping servers...")

	// Stop background services first (quick operations)
	uploadWorkerMu.Lock()
	if uploadWorker != nil {
		uploadWorker.Stop()
		uploadWorker = nil
	}
	uploadWorkerMu.Unlock()
	if sseHub != nil {
		sseHub.Stop()
	}

	// Graceful shutdown with 20 second timeout (well before service 30s timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()

	if httpServer != nil {
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			appLogger.Error("HTTP server shutdown error", "error", err.Error())
		} else {
			appLogger.Info("HTTP server stopped gracefully")
		}
	}

	if httpsServer != nil {
		if err := httpsServer.Shutdown(shutdownCtx); err != nil {
			appLogger.Error("HTTPS server shutdown error", "error", err.Error())
		} else {
			appLogger.Info("HTTPS server stopped gracefully")
		}
	}

	// Wait for servers to finish
	wg.Wait()
	appLogger.Info("All servers stopped")
}
