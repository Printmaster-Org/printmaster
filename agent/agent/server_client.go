package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	pmsettings "printmaster/common/settings"
	"printmaster/common/updatepolicy"
)

// ServerClient handles uploading agent data to the central PrintMaster server
// This is the agent's HTTP client for server communication
type ServerClient struct {
	BaseURL            string
	AgentID            string
	AgentName          string // User-friendly agent name
	Token              string
	HTTPClient         *http.Client
	InsecureSkipVerify bool
	mu                 sync.RWMutex
	lastHeartbeat      time.Time
	lastDeviceUpload   time.Time
	lastMetricsUpload  time.Time
}

// SettingsSnapshot mirrors the server's managed settings payload.
type SettingsSnapshot struct {
	Version         string              `json:"version"`
	SchemaVersion   string              `json:"schema_version"`
	UpdatedAt       time.Time           `json:"updated_at"`
	ManagedSections []string            `json:"managed_sections"`
	Settings        pmsettings.Settings `json:"settings"`
}

// HeartbeatResult captures metadata returned from a heartbeat call.
type HeartbeatResult struct {
	SettingsVersion string
	Snapshot        *SettingsSnapshot
}

// NewServerClient creates a new server uploader for this agent
// If caCertPath is provided, uses it to validate server certificate (for self-signed certs)
// If caCertPath is empty, uses system CA pool (works with Let's Encrypt)
func NewServerClient(baseURL, agentID, token string) *ServerClient {
	return NewServerClientWithName(baseURL, agentID, "", token, "", false)
}

// NewServerClientWithName creates a new server client with agent name
func NewServerClientWithName(baseURL, agentID, agentName, token, caCertPath string, insecureSkipVerify bool) *ServerClient {
	// Use agent package logger for structured logging when available
	Info(fmt.Sprintf("NewServerClientWithName baseURL=%s insecureSkipVerify=%v caCertPath=%s", baseURL, insecureSkipVerify, caCertPath))
	var tlsConfig *tls.Config

	if caCertPath != "" {
		// Validate CA cert path to prevent path traversal attacks
		cleanPath := filepath.Clean(caCertPath)
		if strings.Contains(cleanPath, "..") {
			Warn(fmt.Sprintf("Invalid CA certificate path (path traversal attempt): %s", caCertPath))
			caCertPath = "" // Fall back to system CA
		} else {
			caCertPath = cleanPath
		}
	}

	if caCertPath != "" {
		// Custom CA (self-signed server certificate)
		caCert, err := os.ReadFile(caCertPath)
		if err == nil {
			caCertPool := x509.NewCertPool()
			if caCertPool.AppendCertsFromPEM(caCert) {
				tlsConfig = &tls.Config{
					RootCAs:            caCertPool,
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: insecureSkipVerify,
				}
			}
		}
	}

	if tlsConfig == nil {
		// Use system CA pool (works with Let's Encrypt and other public CAs)
		tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: insecureSkipVerify,
		}
	}

	return &ServerClient{
		BaseURL:            baseURL,
		AgentID:            agentID,
		AgentName:          agentName,
		Token:              token,
		InsecureSkipVerify: insecureSkipVerify,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		},
	}
}

// NewServerClientWithCA creates a new server client with optional custom CA certificate
func NewServerClientWithCA(baseURL, agentID, token, caCertPath string) *ServerClient {
	return NewServerClientWithName(baseURL, agentID, "", token, caCertPath, false)
}

// NewServerClientWithCAAndSkipVerify creates a new server client with optional custom CA and skip verify option
func NewServerClientWithCAAndSkipVerify(baseURL, agentID, token, caCertPath string, insecureSkipVerify bool) *ServerClient {
	return NewServerClientWithName(baseURL, agentID, "", token, caCertPath, insecureSkipVerify)
}

// IsInsecureSkipVerify returns whether this client was configured to skip TLS verification.
func (c *ServerClient) IsInsecureSkipVerify() bool {
	return c.InsecureSkipVerify
}

// SetToken updates the authentication token
func (c *ServerClient) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Token = token
}

// GetToken retrieves the current authentication token
func (c *ServerClient) GetToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Token
}

// GetServerURL retrieves the base server URL
func (c *ServerClient) GetServerURL() string {
	return c.BaseURL
}

// Register performs initial agent registration with the server
// Returns the authentication token on success
func (c *ServerClient) Register(ctx context.Context, version string) (string, error) {
	type RegisterRequest struct {
		AgentID         string `json:"agent_id"`
		Name            string `json:"name,omitempty"` // User-friendly name
		AgentVersion    string `json:"agent_version"`
		ProtocolVersion string `json:"protocol_version"`
		Hostname        string `json:"hostname"`
		IP              string `json:"ip"`
		Platform        string `json:"platform"`
		// Additional metadata
		OSVersion     string `json:"os_version,omitempty"`
		GoVersion     string `json:"go_version,omitempty"`
		Architecture  string `json:"architecture,omitempty"`
		NumCPU        int    `json:"num_cpu,omitempty"`
		TotalMemoryMB int64  `json:"total_memory_mb,omitempty"`
		BuildType     string `json:"build_type,omitempty"`
		GitCommit     string `json:"git_commit,omitempty"`
	}

	type RegisterResponse struct {
		Success bool   `json:"success"`
		AgentID string `json:"agent_id"`
		Token   string `json:"token"`
		Message string `json:"message"`
	}

	hostname, _ := getHostname()
	localIP, _ := getLocalIP()

	req := RegisterRequest{
		AgentID:         c.AgentID,
		Name:            c.AgentName, // Use client's agent name
		AgentVersion:    version,
		ProtocolVersion: "1",
		Hostname:        hostname,
		IP:              localIP,
		Platform:        GetPlatformInfo(),
		OSVersion:       GetOSVersionDetailed(),
		GoVersion:       runtime.Version(),
		Architecture:    runtime.GOARCH,
		NumCPU:          runtime.NumCPU(),
		TotalMemoryMB:   getTotalMemoryMB(),
		BuildType:       getBuildType(),
		GitCommit:       getGitCommit(),
	}

	var resp RegisterResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/agents/register", req, &resp, false); err != nil {
		return "", fmt.Errorf("registration failed: %w", err)
	}

	if !resp.Success {
		return "", fmt.Errorf("registration failed: %s", resp.Message)
	}

	// Store token for future requests
	if resp.Token != "" {
		c.SetToken(resp.Token)
	}

	return resp.Token, nil
}

// RegisterWithToken registers the agent using a join token issued by the server.
// On success it returns the agent-scoped token issued by the server and the
// tenant ID the agent was assigned to. The returned agent token is also stored
// on the client instance for future requests.
func (c *ServerClient) RegisterWithToken(ctx context.Context, joinToken string, version string) (string, string, error) {
	type JoinRequest struct {
		Token           string `json:"token"`
		AgentID         string `json:"agent_id"`
		Name            string `json:"name,omitempty"`
		AgentVersion    string `json:"agent_version,omitempty"`
		ProtocolVersion string `json:"protocol_version,omitempty"`
	}

	type JoinResponse struct {
		Success    bool   `json:"success"`
		TenantID   string `json:"tenant_id"`
		AgentToken string `json:"agent_token"`
		Message    string `json:"message,omitempty"`
	}

	hostname, _ := getHostname()

	req := JoinRequest{
		Token:           joinToken,
		AgentID:         c.AgentID,
		Name:            c.AgentName,
		AgentVersion:    version,
		ProtocolVersion: "1",
	}

	var resp JoinResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/agents/register-with-token", req, &resp, false); err != nil {
		return "", "", fmt.Errorf("register-with-token failed: %w", err)
	}

	if !resp.Success {
		return "", "", fmt.Errorf("register-with-token failed: %s", resp.Message)
	}

	// Store token for future authenticated requests
	if resp.AgentToken != "" {
		c.SetToken(resp.AgentToken)
	}

	// If Name wasn't set, and server returned success, ensure we have hostname set
	if c.AgentName == "" {
		c.AgentName = hostname
	}

	return resp.AgentToken, resp.TenantID, nil
}

// DeviceAuthStartRequest captures the metadata sent to the server when initiating
// a device-authorization flow from the agent UI.
type DeviceAuthStartRequest struct {
	AgentID      string `json:"agent_id"`
	AgentName    string `json:"agent_name,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Platform     string `json:"platform,omitempty"`
}

// DeviceAuthStartResponse mirrors the server payload returned after creating a
// device authorization request.
type DeviceAuthStartResponse struct {
	Success      bool   `json:"success"`
	Code         string `json:"code"`
	PollToken    string `json:"poll_token"`
	ExpiresAt    string `json:"expires_at"`
	AuthorizeURL string `json:"authorize_url"`
	Message      string `json:"message"`
}

// DeviceAuthPollResponse captures the status returned while polling an ongoing
// device authorization exchange.
type DeviceAuthPollResponse struct {
	Success   bool   `json:"success"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	JoinToken string `json:"join_token,omitempty"`
	TenantID  string `json:"tenant_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

// DeviceAuthStart begins the login-based onboarding handshake by creating a
// short-lived approval code on the server.
func (c *ServerClient) DeviceAuthStart(ctx context.Context, req DeviceAuthStartRequest) (*DeviceAuthStartResponse, error) {
	var resp DeviceAuthStartResponse
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/agents/device-auth/start", req, &resp, false); err != nil {
		return nil, fmt.Errorf("device auth start failed: %w", err)
	}
	if !resp.Success {
		if resp.Message != "" {
			return nil, fmt.Errorf("device auth start failed: %s", resp.Message)
		}
		return nil, fmt.Errorf("device auth start failed")
	}
	return &resp, nil
}

// DeviceAuthPoll checks the server for updates about a pending device
// authorization request using the opaque poll token issued during start.
func (c *ServerClient) DeviceAuthPoll(ctx context.Context, pollToken string) (*DeviceAuthPollResponse, error) {
	cleanToken := strings.TrimSpace(pollToken)
	if cleanToken == "" {
		return nil, fmt.Errorf("poll token required")
	}
	var resp DeviceAuthPollResponse
	payload := map[string]string{"poll_token": cleanToken}
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/agents/device-auth/poll", payload, &resp, false); err != nil {
		return nil, fmt.Errorf("device auth poll failed: %w", err)
	}
	if !resp.Success {
		if resp.Message != "" {
			return nil, fmt.Errorf("device auth poll failed: %s", resp.Message)
		}
		return nil, fmt.Errorf("device auth poll failed")
	}
	return &resp, nil
}

// AgentVersionInfo contains agent version metadata for heartbeat
type AgentVersionInfo struct {
	Version         string
	ProtocolVersion string
	BuildType       string
	GitCommit       string
}

// Heartbeat sends a keep-alive signal to the server and returns any managed settings snapshot metadata.
func (c *ServerClient) Heartbeat(ctx context.Context, settingsVersion string) (*HeartbeatResult, error) {
	return c.HeartbeatWithVersion(ctx, settingsVersion, nil)
}

// HeartbeatWithVersion sends a heartbeat with optional version info to update server-side agent metadata.
func (c *ServerClient) HeartbeatWithVersion(ctx context.Context, settingsVersion string, versionInfo *AgentVersionInfo) (*HeartbeatResult, error) {
	type HeartbeatRequest struct {
		AgentID         string    `json:"agent_id"`
		Timestamp       time.Time `json:"timestamp"`
		Status          string    `json:"status"`
		SettingsVersion string    `json:"settings_version,omitempty"`
		// Version info - sent to keep server DB up to date after agent updates
		Version         string `json:"version,omitempty"`
		ProtocolVersion string `json:"protocol_version,omitempty"`
		Hostname        string `json:"hostname,omitempty"`
		IP              string `json:"ip,omitempty"`
		Platform        string `json:"platform,omitempty"`
		OSVersion       string `json:"os_version,omitempty"`
		GoVersion       string `json:"go_version,omitempty"`
		Architecture    string `json:"architecture,omitempty"`
		BuildType       string `json:"build_type,omitempty"`
		GitCommit       string `json:"git_commit,omitempty"`
	}

	type HeartbeatResponse struct {
		Success          bool              `json:"success"`
		SettingsVersion  string            `json:"settings_version,omitempty"`
		SettingsSnapshot *SettingsSnapshot `json:"settings_snapshot,omitempty"`
	}

	hostname, _ := os.Hostname()

	req := HeartbeatRequest{
		AgentID:         c.AgentID,
		Timestamp:       time.Now(),
		Status:          "active",
		SettingsVersion: settingsVersion,
		Hostname:        hostname,
		Platform:        runtime.GOOS,
		Architecture:    runtime.GOARCH,
		GoVersion:       runtime.Version(),
	}

	// Include version info if provided
	if versionInfo != nil {
		req.Version = versionInfo.Version
		req.ProtocolVersion = versionInfo.ProtocolVersion
		req.BuildType = versionInfo.BuildType
		req.GitCommit = versionInfo.GitCommit
	}

	var resp HeartbeatResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/agents/heartbeat", req, &resp, true); err != nil {
		return nil, fmt.Errorf("heartbeat failed: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("heartbeat failed")
	}

	c.mu.Lock()
	c.lastHeartbeat = time.Now()
	c.mu.Unlock()

	return &HeartbeatResult{
		SettingsVersion: resp.SettingsVersion,
		Snapshot:        resp.SettingsSnapshot,
	}, nil
}

// UploadDevices sends discovered devices to the server
func (c *ServerClient) UploadDevices(ctx context.Context, devices []interface{}) error {
	type DevicesBatchRequest struct {
		AgentID   string        `json:"agent_id"`
		Timestamp time.Time     `json:"timestamp"`
		Devices   []interface{} `json:"devices"`
	}

	req := DevicesBatchRequest{
		AgentID:   c.AgentID,
		Timestamp: time.Now(),
		Devices:   devices,
	}

	var resp map[string]interface{}
	if err := c.doRequest(ctx, "POST", "/api/v1/devices/batch", req, &resp, true); err != nil {
		return fmt.Errorf("device upload failed: %w", err)
	}

	c.mu.Lock()
	c.lastDeviceUpload = time.Now()
	c.mu.Unlock()

	return nil
}

// UploadMetrics sends device metrics to the server
func (c *ServerClient) UploadMetrics(ctx context.Context, metrics []interface{}) error {
	type MetricsBatchRequest struct {
		AgentID   string        `json:"agent_id"`
		Timestamp time.Time     `json:"timestamp"`
		Metrics   []interface{} `json:"metrics"`
	}

	req := MetricsBatchRequest{
		AgentID:   c.AgentID,
		Timestamp: time.Now(),
		Metrics:   metrics,
	}

	var resp map[string]interface{}
	if err := c.doRequest(ctx, "POST", "/api/v1/metrics/batch", req, &resp, true); err != nil {
		return fmt.Errorf("metrics upload failed: %w", err)
	}

	c.mu.Lock()
	c.lastMetricsUpload = time.Now()
	c.mu.Unlock()

	return nil
}

// LogAuditEvent sends an audit log entry to the server
func (c *ServerClient) LogAuditEvent(ctx context.Context, action, resourceType, resourceID string, details map[string]interface{}) error {
	type AuditRequest struct {
		AgentID      string                 `json:"agent_id"`
		Timestamp    time.Time              `json:"timestamp"`
		Action       string                 `json:"action"`
		ResourceType string                 `json:"resource_type"`
		ResourceID   string                 `json:"resource_id"`
		Details      map[string]interface{} `json:"details"`
	}

	req := AuditRequest{
		AgentID:      c.AgentID,
		Timestamp:    time.Now(),
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
	}

	var resp map[string]interface{}
	if err := c.doRequest(ctx, "POST", "/api/v1/audit/log", req, &resp, true); err != nil {
		// Don't fail the operation if audit logging fails, just log it
		return fmt.Errorf("audit log failed: %w", err)
	}

	return nil
}

// DeviceCredentials holds web UI credentials for auto-login
type DeviceCredentials struct {
	Exists    bool   `json:"exists"`
	Username  string `json:"username"`
	Password  string `json:"password"` // plaintext (decrypted by server)
	AuthType  string `json:"auth_type"`
	AutoLogin bool   `json:"auto_login"`
	Error     string `json:"error,omitempty"`
}

// GetDeviceCredentials fetches device web UI credentials from the server.
// Returns nil if credentials are not found or an error occurs.
func (c *ServerClient) GetDeviceCredentials(ctx context.Context, serial string) (*DeviceCredentials, error) {
	if serial == "" {
		return nil, fmt.Errorf("serial required")
	}

	var resp DeviceCredentials
	path := "/api/v1/agents/device-credentials?serial=" + url.QueryEscape(serial)
	if err := c.doRequest(ctx, "GET", path, nil, &resp, true); err != nil {
		return nil, err
	}

	if !resp.Exists {
		return nil, fmt.Errorf("credentials not found")
	}

	return &resp, nil
}

// GetStats returns client statistics
func (c *ServerClient) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]interface{}{
		"last_heartbeat":      c.lastHeartbeat,
		"last_device_upload":  c.lastDeviceUpload,
		"last_metrics_upload": c.lastMetricsUpload,
		"has_token":           c.Token != "",
	}
}

// doRequest performs an HTTP request with optional authentication
func (c *ServerClient) doRequest(ctx context.Context, method, path string, reqBody, respBody interface{}, requireAuth bool) error {
	url := c.BaseURL + path

	// Encode request body
	var bodyReader io.Reader
	if reqBody != nil {
		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("failed to encode request: %w", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	}

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "PrintMaster-Agent/1.0")

	// Add authentication if required and token available
	if requireAuth {
		token := c.GetToken()
		if token == "" {
			return fmt.Errorf("authentication required but no token available")
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	// Perform request (log for debugging)
	tokenPresent := false
	if requireAuth {
		token := c.GetToken()
		tokenPresent = token != ""
	}
	Debug(fmt.Sprintf("HTTP request: method=%s url=%s requireAuth=%v tokenPresent=%v", method, url, requireAuth, tokenPresent))

	// Perform request
	httpResp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		Error(fmt.Sprintf("HTTP request failed: %v", err))
		return fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response body
	respData, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		Error(fmt.Sprintf("Server returned non-2xx status %d for %s %s: %s", httpResp.StatusCode, method, url, string(respData)))
		return fmt.Errorf("server returned status %d: %s", httpResp.StatusCode, string(respData))
	}

	// Decode response if needed
	if respBody != nil {
		if err := json.Unmarshal(respData, respBody); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// Helper functions

func getHostname() (string, error) {
	// Try to import os package
	hostname, err := getHostnameInternal()
	if err != nil || hostname == "" {
		return "unknown", err
	}
	return hostname, nil
}

func getLocalIP() (string, error) {
	// Try to get local IP
	ip, err := getLocalIPInternal()
	if err != nil || ip == "" {
		return "unknown", err
	}
	return ip, nil
}

// These will be implemented in helpers.go or can be simple stubs
var getHostnameInternal = func() (string, error) {
	// Implementation will use os.Hostname()
	return "agent-host", nil
}

var getLocalIPInternal = func() (string, error) {
	// Implementation will use net.InterfaceAddrs()
	return "192.168.1.100", nil
}

// getTotalMemoryMB returns the total system memory in MB
func getTotalMemoryMB() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// This returns allocated memory, not total system memory
	// For actual total system memory, would need platform-specific syscalls
	return int64(m.Sys / 1024 / 1024)
}

// These variables will be set at build time via -ldflags
var (
	buildType = "dev"
	gitCommit = "unknown"
)

// getBuildType returns the build type (dev or release)
func getBuildType() string {
	return buildType
}

// getGitCommit returns the git commit hash
func getGitCommit() string {
	return gitCommit
}

// UpdateManifest represents a signed manifest for an available update.
type UpdateManifest struct {
	ManifestVersion string `json:"manifest_version"`
	Component       string `json:"component"`
	Version         string `json:"version"`
	MinorLine       string `json:"minor_line"`
	Platform        string `json:"platform"`
	Arch            string `json:"arch"`
	// Format is the artifact packaging: "binary" or "msi". Servers that
	// predate formats omit it, which means binary.
	Format      string    `json:"format,omitempty"`
	Channel     string    `json:"channel"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
	SourceURL   string    `json:"source_url"`
	DownloadURL string    `json:"download_url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	Signature   string    `json:"signature,omitempty"`
}

// GetLatestManifest fetches the latest update manifest from the server. format
// names the artifact packaging this install can apply ("binary" or "msi");
// empty means binary.
func (c *ServerClient) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*UpdateManifest, error) {
	type ManifestRequest struct {
		AgentID         string `json:"agent_id"`
		Component       string `json:"component"`
		Platform        string `json:"platform"`
		Arch            string `json:"arch"`
		Channel         string `json:"channel"`
		ExplicitChannel bool   `json:"explicit_channel,omitempty"`
		Format          string `json:"format,omitempty"`
	}

	type ManifestResponse struct {
		Success  bool            `json:"success"`
		Manifest *UpdateManifest `json:"manifest,omitempty"`
		Message  string          `json:"message,omitempty"`
	}

	req := ManifestRequest{
		AgentID:         c.AgentID,
		Component:       component,
		Platform:        platform,
		Arch:            arch,
		Channel:         channel,
		ExplicitChannel: updatepolicy.HasExplicitChannel(ctx),
		Format:          format,
	}

	var resp ManifestResponse
	if err := c.doRequest(ctx, "POST", "/api/v1/agents/update/manifest", req, &resp, true); err != nil {
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}

	if !resp.Success {
		if resp.Message != "" {
			return nil, fmt.Errorf("manifest request failed: %s", resp.Message)
		}
		return nil, fmt.Errorf("manifest request failed")
	}

	if resp.Manifest == nil {
		return nil, fmt.Errorf("no manifest returned")
	}

	return resp.Manifest, nil
}

// DownloadArtifact downloads an update artifact to the specified path.
// Supports resuming partial downloads via HTTP Range requests.
// Returns the total bytes downloaded (including resumed bytes).
func (c *ServerClient) DownloadArtifact(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64) (int64, error) {
	return c.DownloadArtifactWithProgress(ctx, manifest, destPath, resumeFrom, nil)
}

// DownloadArtifactWithProgress downloads an artifact with optional progress reporting.
// The callback receives progress percentage (0-100) and total bytes read.
func (c *ServerClient) DownloadArtifactWithProgress(ctx context.Context, manifest *UpdateManifest, destPath string, resumeFrom int64, progressCb DownloadProgressCallback) (int64, error) {
	if manifest == nil {
		return 0, fmt.Errorf("manifest required")
	}

	downloadURL := manifest.DownloadURL
	if downloadURL == "" {
		// Construct URL from base + version
		downloadURL = fmt.Sprintf("%s/api/v1/agents/update/download/%s/%s/%s",
			c.BaseURL, manifest.Component, manifest.Version, manifest.Platform+"-"+manifest.Arch)
		if format, ok := updatepolicy.ParseArtifactFormat(manifest.Format); ok && format != updatepolicy.ArtifactFormatBinary {
			downloadURL += "?format=" + url.QueryEscape(format.String())
		}
	} else if strings.HasPrefix(downloadURL, "/") {
		// Server returned a relative URL - prepend the base URL
		downloadURL = strings.TrimSuffix(c.BaseURL, "/") + downloadURL
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authorization
	c.mu.RLock()
	token := c.Token
	c.mu.RUnlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Add range header for resume
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Open destination file
	flags := os.O_CREATE | os.O_WRONLY
	if resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		resumeFrom = 0 // Server didn't support range, start fresh
	}

	file, err := os.OpenFile(destPath, flags, 0o644)
	if err != nil {
		return 0, fmt.Errorf("failed to open destination file: %w", err)
	}
	defer file.Close()

	// Use progress reader if callback is provided and we know the total size
	var reader io.Reader = resp.Body
	totalSize := manifest.SizeBytes
	if resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent {
		// For resumed downloads, remaining size is total - already downloaded
		totalSize = manifest.SizeBytes - resumeFrom
	}
	if progressCb != nil && totalSize > 0 {
		reader = newProgressReader(resp.Body, totalSize, progressCb)
	}

	written, err := io.Copy(file, reader)
	if err != nil {
		return resumeFrom + written, fmt.Errorf("download interrupted: %w", err)
	}

	return resumeFrom + written, nil
}

// ReportUpdateStatus sends telemetry about an update operation to the server.
func (c *ServerClient) ReportUpdateStatus(ctx context.Context, status, runID, currentVersion, targetVersion string, errCode, errMsg string, metadata map[string]interface{}) error {
	type TelemetryRequest struct {
		AgentID        string                 `json:"agent_id"`
		RunID          string                 `json:"run_id,omitempty"`
		Status         string                 `json:"status"`
		CurrentVersion string                 `json:"current_version"`
		TargetVersion  string                 `json:"target_version,omitempty"`
		ErrorCode      string                 `json:"error_code,omitempty"`
		ErrorMessage   string                 `json:"error_message,omitempty"`
		Timestamp      time.Time              `json:"timestamp"`
		Metadata       map[string]interface{} `json:"metadata,omitempty"`
	}

	req := TelemetryRequest{
		AgentID:        c.AgentID,
		RunID:          runID,
		Status:         status,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		ErrorCode:      errCode,
		ErrorMessage:   errMsg,
		Timestamp:      time.Now(),
		Metadata:       metadata,
	}

	var resp map[string]interface{}
	if err := c.doRequest(ctx, "POST", "/api/v1/agents/update/telemetry", req, &resp, true); err != nil {
		return fmt.Errorf("telemetry report failed: %w", err)
	}

	return nil
}
