package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	commonconfig "printmaster/common/config"
	pmsettings "printmaster/common/settings"
	"printmaster/server/handlers"
	serversettings "printmaster/server/settings"
	"printmaster/server/storage"
	"printmaster/server/tenancy"
	"strconv"
	"sync"
	"testing"
	"time"
)

// setupTestServer creates a test server with in-memory storage
func setupTestServer(t *testing.T) (*httptest.Server, storage.Store) {
	t.Helper()

	// Create in-memory store
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}

	// Set globals for handlers
	serverStore = store
	// Note: serverLogger is nil in tests, handlers should handle gracefully

	// Create a dedicated mux for this test server to avoid races when tests run
	// in parallel. Register the core handlers on that mux and register tenancy
	// routes onto the same mux using the mux-aware registration function.
	mux := http.NewServeMux()

	// Create health API and register its routes
	healthAPI := handlers.NewHealthAPI(handlers.HealthAPIOptions{
		Version:         Version,
		BuildTime:       BuildTime,
		GitCommit:       GitCommit,
		BuildType:       BuildType,
		ProtocolVersion: ProtocolVersion,
		ProcessStart:    processStart,
		TenancyChecker:  tenancy.IsEnabled,
	})
	healthAPI.RegisterRoutes(mux)

	// Keep /api/v1/agents/register for compatibility (it will return 403)
	mux.HandleFunc("/api/v1/agents/register", handleAgentRegister)
	mux.HandleFunc("/api/v1/agents/heartbeat", requireAuth(handleAgentHeartbeat))
	// For tests, inject a default admin user into requests for UI endpoints
	mux.HandleFunc("/api/v1/agents/list", WrapWithAdmin(handleAgentsList))
	mux.HandleFunc("/api/v1/agents/", WrapWithAdmin(handleAgentDetails))
	mux.HandleFunc("/api/v1/devices/batch", requireAuth(handleDevicesBatch))
	mux.HandleFunc("/api/v1/metrics/batch", requireAuth(handleMetricsBatch))

	// Register tenancy routes onto this mux (avoids global DefaultServeMux)
	tenancy.RegisterRoutesOnMux(mux, store)

	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		store.Close()
	})

	return server, store
}

func TestHandleWebUI_RedirectsWhenUnauthenticated(t *testing.T) {
	// Note: not parallel due to global serverStore mutation
	prevStore := serverStore
	serverStore = nil
	t.Cleanup(func() { serverStore = prevStore })

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	resp := httptest.NewRecorder()

	handleWebUI(resp, req)

	result := resp.Result()
	if result.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", result.StatusCode)
	}
	location := result.Header.Get("Location")
	if location != "/login?redirect=%2F" {
		t.Fatalf("expected redirect to /login with redirect param, got %s", location)
	}
}

func TestHandleWebUI_ServesForAuthenticatedUser(t *testing.T) {
	// Test for authenticated user serving
	// Note: not parallel due to global serverStore mutation
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	prevStore := serverStore
	serverStore = store
	t.Cleanup(func() { serverStore = prevStore })

	ctx := context.Background()
	user := &storage.User{Username: "admin", Role: storage.RoleAdmin}
	if err := store.CreateUser(ctx, user, "password"); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	session, err := store.CreateSession(ctx, user.ID, 1)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.AddCookie(&http.Cookie{Name: "pm_session", Value: session.Token})
	resp := httptest.NewRecorder()

	handleWebUI(resp, req)

	result := resp.Result()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", result.StatusCode)
	}
	if ct := result.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html response, got %s", ct)
	}
}

func TestHealthEndpoint(t *testing.T) {
	// Test for health endpoint
	t.Parallel()

	server, _ := setupTestServer(t)

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("Failed to call /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["status"] != "healthy" {
		t.Errorf("Expected status=healthy, got %v", result["status"])
	}
}

func TestRunHealthCheckHTTP(t *testing.T) {
	t.Parallel()

	// Create a health API for the test
	healthAPI := handlers.NewHealthAPI(handlers.HealthAPIOptions{})
	server := httptest.NewServer(http.HandlerFunc(healthAPI.HandleHealth))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, _ := strconv.Atoi(parsed.Port())

	cfg := DefaultConfig()
	cfg.Server.HTTPPort = port
	cfg.Server.HTTPSPort = 0

	configPath := filepath.Join(t.TempDir(), "server-config.toml")
	if err := commonconfig.WriteTOML(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runHealthCheck(configPath); err != nil {
		t.Fatalf("expected health check success, got error: %v", err)
	}
}

func TestRunHealthCheckHTTPS(t *testing.T) {
	t.Parallel()

	// Create a health API for the test
	healthAPI := handlers.NewHealthAPI(handlers.HealthAPIOptions{})
	server := httptest.NewTLSServer(http.HandlerFunc(healthAPI.HandleHealth))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, _ := strconv.Atoi(parsed.Port())

	cfg := DefaultConfig()
	cfg.Server.HTTPPort = 0
	cfg.Server.HTTPSPort = port

	configPath := filepath.Join(t.TempDir(), "server-config-https.toml")
	if err := commonconfig.WriteTOML(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runHealthCheck(configPath); err != nil {
		t.Fatalf("expected health check success, got error: %v", err)
	}
}

func TestRunHealthCheckFailure(t *testing.T) {
	t.Parallel()

	// Allocate an unused port and close it so the health check will fail to connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := DefaultConfig()
	cfg.Server.HTTPPort = port
	cfg.Server.HTTPSPort = 0

	configPath := filepath.Join(t.TempDir(), "server-config-fail.toml")
	if err := commonconfig.WriteTOML(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runHealthCheck(configPath); err == nil {
		t.Fatalf("expected health check to fail, but it succeeded")
	}
}

func TestVersionEndpoint(t *testing.T) {
	t.Parallel()

	server, _ := setupTestServer(t)

	resp, err := http.Get(server.URL + "/api/version")
	if err != nil {
		t.Fatalf("Failed to call /api/version: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if _, ok := result["version"]; !ok {
		t.Error("Response missing 'version' field")
	}
	if _, ok := result["protocol_version"]; !ok {
		t.Error("Response missing 'protocol_version' field")
	}
	// Check tenancy_enabled is present (added for UI to determine pending registrations visibility)
	if _, ok := result["tenancy_enabled"]; !ok {
		t.Error("Response missing 'tenancy_enabled' field")
	}
}

func TestAgentRegistration(t *testing.T) {
	// Test for agent registration
	// Note: Not parallel due to shared global serverStore
	server, store := setupTestServer(t)

	// Create tenant and join token, then register agent using register-with-token
	ctx := context.Background()
	tn := &storage.Tenant{ID: "test-tenant-01", Name: "TestTenant"}
	if err := store.CreateTenant(ctx, tn); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	_, rawToken, err := store.CreateJoinToken(ctx, tn.ID, 60, true)
	if err != nil {
		t.Fatalf("Failed to create join token: %v", err)
	}

	reqBody := map[string]interface{}{
		"token":            rawToken,
		"agent_id":         "test-agent-01",
		"agent_version":    "v0.2.0",
		"protocol_version": "1",
		"hostname":         "test-host",
		"ip":               "192.168.1.100",
		"platform":         "windows",
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(server.URL+"/api/v1/agents/register-with-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response
	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}
	agentToken, ok := result["agent_token"].(string)
	if !ok || agentToken == "" {
		t.Error("Response missing or empty agent_token")
	}

	// Verify agent in database
	agent, err := store.GetAgent(ctx, "test-agent-01")
	if err != nil {
		t.Fatalf("Failed to retrieve agent from store: %v", err)
	}

	if agent.AgentID != "test-agent-01" {
		t.Errorf("Expected AgentID=test-agent-01, got %s", agent.AgentID)
	}
	if agent.Token != agentToken {
		t.Errorf("Token mismatch: response=%s, db=%s", agentToken, agent.Token)
	}
	if agent.Status != "active" {
		t.Errorf("Expected status=active, got %s", agent.Status)
	}
}

func TestHeartbeatRequiresAuth(t *testing.T) {
	t.Parallel()

	server, _ := setupTestServer(t)

	// Try heartbeat without token
	reqBody := map[string]interface{}{
		"agent_id":  "test-agent-01",
		"timestamp": time.Now(),
		"status":    "active",
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(server.URL+"/api/v1/agents/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to call heartbeat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401 Unauthorized, got %d", resp.StatusCode)
	}
}

func TestHeartbeatWithValidToken(t *testing.T) {
	// Note: Not parallel due to shared global serverStore
	server, store := setupTestServer(t)
	ctx := context.Background()

	// Register agent first
	agent := &storage.Agent{
		AgentID:         "test-agent-02",
		Hostname:        "test-host",
		IP:              "192.168.1.100",
		Platform:        "windows",
		Version:         "v0.2.0",
		ProtocolVersion: "1",
		Token:           "test-token-12345",
		RegisteredAt:    time.Now(),
		LastSeen:        time.Now(),
		Status:          "active",
	}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}

	// Send heartbeat with token
	reqBody := map[string]interface{}{
		"agent_id":  "test-agent-02",
		"timestamp": time.Now(),
		"status":    "active",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", server.URL+"/api/v1/agents/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token-12345")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to call heartbeat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}
}

func TestHeartbeatReturnsSettingsSnapshotWhenVersionDiffers(t *testing.T) {
	// Note: not parallel because globals mutated
	server, store := setupTestServer(t)
	ctx := context.Background()

	agent := &storage.Agent{
		AgentID:         "settings-agent",
		Hostname:        "snap-host",
		IP:              "10.0.0.5",
		Platform:        "linux",
		Version:         "v0.3.0",
		ProtocolVersion: "1",
		Token:           "snap-token",
		RegisteredAt:    time.Now(),
		LastSeen:        time.Now(),
		Status:          "active",
	}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("failed to seed agent: %v", err)
	}

	var err error
	settingsResolver, err = serversettings.NewResolver(store)
	if err != nil {
		t.Fatalf("failed to create settings resolver: %v", err)
	}
	t.Cleanup(func() { settingsResolver = nil })

	cfg := pmsettings.DefaultSettings()
	cfg.Discovery.AutoDiscoverEnabled = true
	rec := &storage.SettingsRecord{SchemaVersion: "schema-x", Settings: cfg, UpdatedAt: time.Unix(1000, 0)}
	if err := store.UpsertGlobalSettings(ctx, rec); err != nil {
		t.Fatalf("failed to persist global settings: %v", err)
	}

	snapshot, err := serversettings.BuildAgentSnapshot(ctx, settingsResolver, agent.TenantID, agent.AgentID)
	if err != nil {
		t.Fatalf("build snapshot failed: %v", err)
	}
	if snapshot.Version == "" {
		t.Fatalf("expected snapshot version")
	}

	body := map[string]interface{}{
		"agent_id":  agent.AgentID,
		"timestamp": time.Now(),
		"status":    "active",
	}
	payload, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agents/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agent.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heartbeat request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if result["settings_version"] != snapshot.Version {
		t.Fatalf("expected settings_version %s, got %v", snapshot.Version, result["settings_version"])
	}
	if _, ok := result["settings_snapshot"]; !ok {
		t.Fatalf("expected settings snapshot in response")
	}

	body["settings_version"] = snapshot.Version
	payload, _ = json.Marshal(body)
	req2, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agents/heartbeat", bytes.NewReader(payload))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+agent.Token)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("heartbeat request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var second map[string]interface{}
	if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response failed: %v", err)
	}
	if _, ok := second["settings_snapshot"]; ok {
		t.Fatalf("expected snapshot to be omitted when agent is up-to-date")
	}
}

func TestHeartbeatWithInvalidToken(t *testing.T) {
	t.Parallel()

	server, _ := setupTestServer(t)

	reqBody := map[string]interface{}{
		"agent_id":  "test-agent-02",
		"timestamp": time.Now(),
		"status":    "active",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", server.URL+"/api/v1/agents/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to call heartbeat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", resp.StatusCode)
	}
}

func TestHandleUsersEnforcesAdmin(t *testing.T) {
	store := SetupTestStore(t)
	ctx := context.Background()
	admin := NewTestAdminUser()
	if err := store.CreateUser(ctx, admin, "secret"); err != nil {
		t.Fatalf("failed to seed admin user: %v", err)
	}

	viewerReq := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	viewerReq = InjectTestUser(viewerReq, NewTestUser(storage.RoleViewer))
	viewerRec := httptest.NewRecorder()
	handleUsers(viewerRec, viewerReq)
	if viewerRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d", viewerRec.Code)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	adminReq = InjectTestUser(adminReq, admin)
	adminRec := httptest.NewRecorder()
	handleUsers(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d", adminRec.Code)
	}
}

func TestHandleLogsAuthorization(t *testing.T) {
	t.Parallel()

	unauthedReq := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	unauthedRec := httptest.NewRecorder()
	handleLogs(unauthedRec, unauthedReq)
	if unauthedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous request, got %d", unauthedRec.Code)
	}

	viewerReq := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	viewerReq = InjectTestUser(viewerReq, NewTestUser(storage.RoleViewer))
	viewerRec := httptest.NewRecorder()
	handleLogs(viewerRec, viewerReq)
	if viewerRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer, got %d", viewerRec.Code)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	adminReq = InjectTestAdmin(adminReq)
	adminRec := httptest.NewRecorder()
	handleLogs(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d", adminRec.Code)
	}
}

func TestHandleAuditLogsAdminOnly(t *testing.T) {
	store := SetupTestStore(t)
	ctx := context.Background()
	entry := &storage.AuditEntry{
		Timestamp: time.Now(),
		ActorType: storage.AuditActorAgent,
		ActorID:   "agent-1",
		Action:    "test",
		Severity:  storage.AuditSeverityInfo,
		Details:   "",
	}
	if err := store.SaveAuditEntry(ctx, entry); err != nil {
		t.Fatalf("failed to seed audit entry: %v", err)
	}

	operatorReq := httptest.NewRequest(http.MethodGet, "/api/audit/logs", nil)
	operatorReq = InjectTestUser(operatorReq, NewTestUser(storage.RoleOperator))
	operatorRec := httptest.NewRecorder()
	handleAuditLogs(operatorRec, operatorReq)
	if operatorRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for operator, got %d", operatorRec.Code)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/audit/logs", nil)
	adminReq = InjectTestUser(adminReq, NewTestAdminUser())
	adminRec := httptest.NewRecorder()
	handleAuditLogs(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d", adminRec.Code)
	}
}

func TestDevicesBatchUpload(t *testing.T) {
	// Note: Not parallel due to shared global serverStore
	server, store := setupTestServer(t)
	ctx := context.Background()

	// Register agent
	agent := &storage.Agent{
		AgentID:         "test-agent-03",
		Hostname:        "test-host",
		IP:              "192.168.1.100",
		Platform:        "windows",
		Version:         "v0.2.0",
		ProtocolVersion: "1",
		Token:           "test-token-67890",
		RegisteredAt:    time.Now(),
		LastSeen:        time.Now(),
		Status:          "active",
	}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}

	// Upload devices
	devices := []map[string]interface{}{
		{
			"serial":       "ABC123",
			"ip":           "192.168.1.50",
			"manufacturer": "HP",
			"model":        "LaserJet Pro",
			"hostname":     "printer-01",
			"is_saved":     true,
		},
		{
			"serial":       "XYZ789",
			"ip":           "192.168.1.51",
			"manufacturer": "Canon",
			"model":        "PIXMA",
			"hostname":     "printer-02",
			"is_saved":     false,
		},
	}

	reqBody := map[string]interface{}{
		"agent_id":  "test-agent-03",
		"timestamp": time.Now(),
		"devices":   devices,
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", server.URL+"/api/v1/devices/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token-67890")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to upload devices: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}

	// Verify devices in database
	device1, err := store.GetDevice(ctx, "ABC123")
	if err != nil {
		t.Errorf("Failed to retrieve device ABC123: %v", err)
	} else if device1.Manufacturer != "HP" {
		t.Errorf("Expected manufacturer=HP, got %s", device1.Manufacturer)
	}
	if device1 != nil && (device1.IsSaved == nil || !*device1.IsSaved) {
		t.Errorf("saved state missing: %+v", device1.IsSaved)
	}

	device2, err := store.GetDevice(ctx, "XYZ789")
	if err != nil {
		t.Errorf("Failed to retrieve device XYZ789: %v", err)
	} else if device2.Manufacturer != "Canon" {
		t.Errorf("Expected manufacturer=Canon, got %s", device2.Manufacturer)
	}
	if device2 != nil && (device2.IsSaved == nil || *device2.IsSaved) {
		t.Errorf("discovered state missing: %+v", device2.IsSaved)
	}
}

func TestMetricsBatchUpload(t *testing.T) {
	// Note: Not parallel due to shared global serverStore
	server, store := setupTestServer(t)
	ctx := context.Background()

	// Register agent
	agent := &storage.Agent{
		AgentID:         "test-agent-04",
		Hostname:        "test-host",
		IP:              "192.168.1.100",
		Platform:        "windows",
		Version:         "v0.2.0",
		ProtocolVersion: "1",
		Token:           "test-token-abcde",
		RegisteredAt:    time.Now(),
		LastSeen:        time.Now(),
		Status:          "active",
	}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}

	// Create device first
	device := &storage.Device{}
	device.Serial = "METRICS-TEST-01"
	device.AgentID = "test-agent-04"
	device.IP = "192.168.1.60"
	device.LastSeen = time.Now()
	device.FirstSeen = time.Now()
	device.CreatedAt = time.Now()

	if err := store.UpsertDevice(ctx, device); err != nil {
		t.Fatalf("Failed to create device: %v", err)
	}

	// Upload metrics
	metrics := []map[string]interface{}{
		{
			"serial":     "METRICS-TEST-01",
			"timestamp":  time.Now(),
			"page_count": 1234,
			"toner_levels": map[string]interface{}{
				"black": 75,
				"cyan":  80,
			},
		},
	}

	reqBody := map[string]interface{}{
		"agent_id":  "test-agent-04",
		"timestamp": time.Now(),
		"metrics":   metrics,
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", server.URL+"/api/v1/metrics/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token-abcde")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to upload metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}

	// Verify metrics in database
	retrieved, err := store.GetLatestMetrics(ctx, "METRICS-TEST-01")
	if err != nil {
		t.Fatalf("Failed to retrieve metrics: %v", err)
	}
	if retrieved.PageCount != 1234 {
		t.Errorf("Expected page_count=1234, got %d", retrieved.PageCount)
	}
}

func TestGenerateToken(t *testing.T) {
	t.Parallel()

	// Generate multiple tokens
	tokens := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token, err := generateToken()
		if err != nil {
			t.Fatalf("Failed to generate token: %v", err)
		}

		// Check uniqueness
		if tokens[token] {
			t.Errorf("Duplicate token generated: %s", token)
		}
		tokens[token] = true

		// Check length (32 bytes base64-encoded = 44 chars)
		if len(token) < 40 {
			t.Errorf("Token too short: %s (len=%d)", token, len(token))
		}
	}
}

func TestExtractClientIP(t *testing.T) {
	// Note: Not using t.Parallel() because we need to set serverConfig for proxy tests

	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectedIP    string
		behindProxy   bool // Whether to simulate being behind a proxy
	}{
		{
			name:       "Direct connection",
			remoteAddr: "192.168.1.100:54321",
			expectedIP: "192.168.1.100",
		},
		{
			name:          "Behind proxy with X-Forwarded-For",
			remoteAddr:    "10.0.0.1:12345", // Trusted private IP
			xForwardedFor: "203.0.113.1, 192.168.1.1",
			expectedIP:    "203.0.113.1",
			behindProxy:   true,
		},
		{
			name:        "Behind proxy with X-Real-IP",
			remoteAddr:  "10.0.0.1:12345", // Trusted private IP
			xRealIP:     "203.0.113.2",
			expectedIP:  "203.0.113.2",
			behindProxy: true,
		},
		{
			name:          "X-Forwarded-For takes precedence",
			remoteAddr:    "10.0.0.1:12345", // Trusted private IP
			xForwardedFor: "203.0.113.3",
			xRealIP:       "203.0.113.4",
			expectedIP:    "203.0.113.3",
			behindProxy:   true,
		},
		{
			name:          "Ignores proxy headers when not behind proxy",
			remoteAddr:    "192.168.1.100:12345",
			xForwardedFor: "203.0.113.5",
			expectedIP:    "192.168.1.100",
			behindProxy:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore serverConfig
			oldConfig := serverConfig
			if tt.behindProxy {
				serverConfig = &Config{
					Server: ServerConfig{
						BehindProxy: true,
					},
				}
				// Reset the trusted proxies cache for this test
				parsedTrustedProxies = nil
				trustedProxiesOnce = sync.Once{}
			} else {
				serverConfig = nil
			}
			defer func() {
				serverConfig = oldConfig
				parsedTrustedProxies = nil
				trustedProxiesOnce = sync.Once{}
			}()

			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			ip := extractClientIP(req)
			if ip != tt.expectedIP {
				t.Errorf("Expected IP %s, got %s", tt.expectedIP, ip)
			}
		})
	}
}

func TestParseDeviceProxyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		prefix     string
		serial     string
		targetPath string
		err        bool
	}{
		{
			name:       "API path root",
			path:       "/api/v1/proxy/device/X7GT003032/",
			prefix:     "/api/v1/proxy/device/",
			serial:     "X7GT003032",
			targetPath: "/",
		},
		{
			name:       "Legacy device prefix",
			path:       "/proxy/device/X7GT003032/info/panel",
			prefix:     "/proxy/",
			serial:     "X7GT003032",
			targetPath: "/info/panel",
		},
		{
			name:       "Legacy root",
			path:       "/proxy/X7GT003032/status",
			prefix:     "/proxy/",
			serial:     "X7GT003032",
			targetPath: "/status",
		},
		{
			name:   "Missing serial",
			path:   "/proxy/",
			prefix: "/proxy/",
			err:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serial, target, err := parseDeviceProxyPath(tt.path, tt.prefix)
			if tt.err {
				if err == nil {
					t.Fatalf("Expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if serial != tt.serial {
				t.Fatalf("Serial mismatch: want %s got %s", tt.serial, serial)
			}
			if target != tt.targetPath {
				t.Fatalf("Target mismatch: want %s got %s", tt.targetPath, target)
			}
		})
	}
}

func TestRecoverDeviceProxyResourcePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		referer  string
		wantPath string
		ok       bool
	}{
		{
			name:     "Kyocera asset missing serial",
			path:     "/api/v1/proxy/device/js/ssrc/base.js",
			referer:  "https://printmaster.example/api/v1/proxy/device/RH28Y00024/",
			wantPath: "/api/v1/proxy/device/RH28Y00024/js/ssrc/base.js",
			ok:       true,
		},
		{
			name:    "non-resource path is not repaired",
			path:    "/api/v1/proxy/device/RH28Y00024/js/ssrc/base.js",
			referer: "https://printmaster.example/api/v1/proxy/device/RH28Y00024/",
		},
		{
			name:    "unscoped referer is not trusted",
			path:    "/api/v1/proxy/device/js/ssrc/base.js",
			referer: "https://printmaster.example/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Referer", tt.referer)

			gotPath, gotOK := recoverDeviceProxyResourcePath(req)
			if gotOK != tt.ok || gotPath != tt.wantPath {
				t.Fatalf("recoverDeviceProxyResourcePath() = (%q, %t), want (%q, %t)", gotPath, gotOK, tt.wantPath, tt.ok)
			}
		})
	}
}

func TestAgentRegistrationWithMetadata(t *testing.T) {
	// Test for agent registration with metadata
	// Test that new metadata fields are properly stored
	server, store := setupTestServer(t)

	// Create tenant and join token, then register agent with metadata using register-with-token
	ctx := context.Background()
	tn := &storage.Tenant{ID: "test-tenant-02", Name: "MetaTenant"}
	if err := store.CreateTenant(ctx, tn); err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	_, rawToken, err := store.CreateJoinToken(ctx, tn.ID, 60, true)
	if err != nil {
		t.Fatalf("Failed to create join token: %v", err)
	}

	// Register agent with extended metadata
	reqBody := map[string]interface{}{
		"token":            rawToken,
		"agent_id":         "test-agent-metadata",
		"agent_version":    "v0.3.0",
		"protocol_version": "1",
		"hostname":         "test-metadata-host",
		"ip":               "192.168.1.200",
		"platform":         "linux",
		"os_version":       "Ubuntu 22.04",
		"go_version":       "go1.27.0",
		"architecture":     "amd64",
		"num_cpu":          8,
		"total_memory_mb":  16384,
		"build_type":       "release",
		"git_commit":       "abc123def456",
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(server.URL+"/api/v1/agents/register-with-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}

	// Verify all metadata is stored
	agent, err := store.GetAgent(ctx, "test-agent-metadata")
	if err != nil {
		t.Fatalf("Failed to retrieve agent from store: %v", err)
	}

	// Check basic fields
	if agent.AgentID != "test-agent-metadata" {
		t.Errorf("Expected AgentID=test-agent-metadata, got %s", agent.AgentID)
	}
	if agent.Hostname != "test-metadata-host" {
		t.Errorf("Expected Hostname=test-metadata-host, got %s", agent.Hostname)
	}

	// Check metadata fields
	if agent.OSVersion != "Ubuntu 22.04" {
		t.Errorf("Expected OSVersion=Ubuntu 22.04, got %s", agent.OSVersion)
	}
	if agent.GoVersion != "go1.27.0" {
		t.Errorf("Expected GoVersion=go1.27.0, got %s", agent.GoVersion)
	}
	if agent.Architecture != "amd64" {
		t.Errorf("Expected Architecture=amd64, got %s", agent.Architecture)
	}
	if agent.NumCPU != 8 {
		t.Errorf("Expected NumCPU=8, got %d", agent.NumCPU)
	}
	if agent.TotalMemoryMB != 16384 {
		t.Errorf("Expected TotalMemoryMB=16384, got %d", agent.TotalMemoryMB)
	}
	if agent.BuildType != "release" {
		t.Errorf("Expected BuildType=release, got %s", agent.BuildType)
	}
	if agent.GitCommit != "abc123def456" {
		t.Errorf("Expected GitCommit=abc123def456, got %s", agent.GitCommit)
	}
}

func TestAgentDetailsEndpoint(t *testing.T) {
	// Test for agent details endpoint
	// Test the /api/v1/agents/{id} endpoint
	_, store := setupTestServer(t)
	ctx := context.Background()

	// Create test agent with full metadata
	agent := &storage.Agent{
		AgentID:         "test-agent-details",
		Hostname:        "details-host",
		IP:              "192.168.1.150",
		Platform:        "darwin",
		Version:         "v0.3.0",
		ProtocolVersion: "1",
		Token:           "details-token-123",
		RegisteredAt:    time.Now().Add(-24 * time.Hour),
		LastSeen:        time.Now(),
		Status:          "active",
		OSVersion:       "macOS 14.0",
		GoVersion:       "go1.27.0",
		Architecture:    "arm64",
		NumCPU:          10,
		TotalMemoryMB:   32768,
		BuildType:       "release",
		GitCommit:       "xyz789abc123",
		LastHeartbeat:   time.Now().Add(-5 * time.Minute),
		DeviceCount:     5,
		LastDeviceSync:  time.Now().Add(-10 * time.Minute),
		LastMetricsSync: time.Now().Add(-15 * time.Minute),
	}
	if err := store.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("Failed to register agent: %v", err)
	}

	// Setup the agent details handler (inject admin user for test)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/", WrapWithAdmin(handleAgentDetails))
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	// Fetch agent details (attach admin user to context so handler auth passes)
	req, _ := http.NewRequest("GET", testServer.URL+"/api/v1/agents/test-agent-details", nil)
	req = InjectTestAdmin(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch agent details: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result storage.Agent
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify all fields are returned
	if result.AgentID != "test-agent-details" {
		t.Errorf("Expected AgentID=test-agent-details, got %s", result.AgentID)
	}
	if result.Hostname != "details-host" {
		t.Errorf("Expected Hostname=details-host, got %s", result.Hostname)
	}
	if result.OSVersion != "macOS 14.0" {
		t.Errorf("Expected OSVersion=macOS 14.0, got %s", result.OSVersion)
	}
	if result.Architecture != "arm64" {
		t.Errorf("Expected Architecture=arm64, got %s", result.Architecture)
	}
	if result.NumCPU != 10 {
		t.Errorf("Expected NumCPU=10, got %d", result.NumCPU)
	}
	if result.TotalMemoryMB != 32768 {
		t.Errorf("Expected TotalMemoryMB=32768, got %d", result.TotalMemoryMB)
	}

	// Verify token is not exposed
	if result.Token != "" {
		t.Error("Token should not be exposed in API response")
	}
}

func TestAgentDetailsNotFound(t *testing.T) {
	t.Parallel()

	_, _ = setupTestServer(t)

	// Setup the agent details handler (inject admin user for test)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/", WrapWithAdmin(handleAgentDetails))
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	// Try to fetch non-existent agent (attach admin user to context so handler auth passes)
	req, _ := http.NewRequest("GET", testServer.URL+"/api/v1/agents/non-existent-agent", nil)
	req = InjectTestAdmin(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch agent details: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}
}

func TestAgentsListEndpoint(t *testing.T) {
	// Test for agents list endpoint
	// Test the /api/v1/agents/list endpoint
	server, store := setupTestServer(t)
	ctx := context.Background()

	// Create multiple test agents
	agents := []*storage.Agent{
		{
			AgentID:         "list-agent-01",
			Hostname:        "host-01",
			IP:              "192.168.1.10",
			Platform:        "windows",
			Version:         "v0.3.0",
			ProtocolVersion: "1",
			Token:           "token-01",
			RegisteredAt:    time.Now(),
			LastSeen:        time.Now(),
			Status:          "active",
		},
		{
			AgentID:         "list-agent-02",
			Hostname:        "host-02",
			IP:              "192.168.1.11",
			Platform:        "linux",
			Version:         "v0.3.0",
			ProtocolVersion: "1",
			Token:           "token-02",
			RegisteredAt:    time.Now(),
			LastSeen:        time.Now().Add(-30 * time.Minute),
			Status:          "inactive",
		},
	}

	for _, agent := range agents {
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}
	}

	// Fetch agents list (attach admin user to context so handler auth passes)
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/agents/list", nil)
	req = InjectTestAdmin(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch agents list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result []*storage.Agent
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Should have at least 2 agents (may have more from other tests)
	if len(result) < 2 {
		t.Errorf("Expected at least 2 agents, got %d", len(result))
	}

	// Verify tokens are not exposed
	for _, agent := range result {
		if agent.Token != "" {
			t.Errorf("Token should not be exposed for agent %s", agent.AgentID)
		}
	}
}

func TestAgentsListConnectionType(t *testing.T) {
	// Test for agents list connection type
	// Not parallel due to global wsConnections map
	server, store := setupTestServer(t)
	ctx := context.Background()

	// Agent with active WS connection
	wsAgent := &storage.Agent{
		AgentID:      "ws-agent",
		Hostname:     "ws-host",
		RegisteredAt: time.Now(),
		LastSeen:     time.Now(),
		Status:       "active",
		Token:        "t-ws",
	}
	// Agent with recent HTTP heartbeat
	httpAgent := &storage.Agent{
		AgentID:      "http-agent",
		Hostname:     "http-host",
		RegisteredAt: time.Now(),
		LastSeen:     time.Now().Add(-30 * time.Second),
		Status:       "active",
		Token:        "t-http",
	}
	// Agent offline
	offAgent := &storage.Agent{
		AgentID:      "off-agent",
		Hostname:     "off-host",
		RegisteredAt: time.Now(),
		LastSeen:     time.Now().Add(-10 * time.Minute),
		Status:       "offline",
		Token:        "t-off",
	}

	if err := store.RegisterAgent(ctx, wsAgent); err != nil {
		t.Fatalf("failed to register ws agent: %v", err)
	}
	if err := store.RegisterAgent(ctx, httpAgent); err != nil {
		t.Fatalf("failed to register http agent: %v", err)
	}
	if err := store.RegisterAgent(ctx, offAgent); err != nil {
		t.Fatalf("failed to register off agent: %v", err)
	}

	// Simulate ws connection by inserting key into wsConnections map
	wsConnectionsLock.Lock()
	wsConnections["ws-agent"] = nil // presence matters, value may be nil in tests
	wsConnectionsLock.Unlock()

	// Request agents list (attach admin user to context so handler auth passes)
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/agents/list", nil)
	req = InjectTestAdmin(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch agents list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d", resp.StatusCode)
	}

	var list []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Build lookup by agent_id
	byID := map[string]map[string]interface{}{}
	for _, obj := range list {
		if id, ok := obj["agent_id"].(string); ok {
			byID[id] = obj
		}
	}

	// ws-agent should be 'ws'
	if obj, ok := byID["ws-agent"]; ok {
		if ct, _ := obj["connection_type"].(string); ct != "ws" {
			t.Errorf("ws-agent connection_type expected 'ws', got '%s'", ct)
		}
	} else {
		t.Errorf("ws-agent missing from list response")
	}

	// http-agent should be 'http'
	if obj, ok := byID["http-agent"]; ok {
		if ct, _ := obj["connection_type"].(string); ct != "http" {
			t.Errorf("http-agent connection_type expected 'http', got '%s'", ct)
		}
	} else {
		t.Errorf("http-agent missing from list response")
	}

	// off-agent should be 'none'
	if obj, ok := byID["off-agent"]; ok {
		if ct, _ := obj["connection_type"].(string); ct != "none" {
			t.Errorf("off-agent connection_type expected 'none', got '%s'", ct)
		}
	} else {
		t.Errorf("off-agent missing from list response")
	}
}

func TestBuildAgentUpdateAuditEntry(t *testing.T) {
	agent := &storage.Agent{
		AgentID:  "agent-123",
		Name:     "HQ Agent",
		Hostname: "hq-agent.local",
		TenantID: "tenant-01",
		Version:  "1.2.3",
	}
	meta := map[string]interface{}{"foo": "bar"}
	entry := buildAgentUpdateAuditEntry(agent, "agent.update.check", "details", meta)
	if entry == nil {
		t.Fatalf("expected audit entry")
	}
	if entry.TargetID != agent.AgentID {
		t.Fatalf("expected TargetID %s, got %s", agent.AgentID, entry.TargetID)
	}
	if entry.TenantID != agent.TenantID {
		t.Fatalf("expected TenantID %s, got %s", agent.TenantID, entry.TenantID)
	}
	if entry.Metadata["foo"] != "bar" {
		t.Fatalf("expected metadata foo=bar, got %v", entry.Metadata["foo"])
	}
	if entry.Metadata["agent_id"] != agent.AgentID {
		t.Fatalf("expected metadata agent_id")
	}
	if entry.Metadata["agent_name"] != agent.Name {
		t.Fatalf("expected metadata agent_name")
	}
	if entry.Metadata["hostname"] != agent.Hostname {
		t.Fatalf("expected metadata hostname")
	}
	if entry.Metadata["agent_version"] != agent.Version {
		t.Fatalf("expected metadata agent_version")
	}
	if entry.Metadata["agent_display_name"] != agent.Name {
		t.Fatalf("expected metadata agent_display_name")
	}
	meta["foo"] = "baz"
	if entry.Metadata["foo"] != "bar" {
		t.Fatalf("expected metadata copy to be isolated")
	}
}

func TestDisplayNameForAgent(t *testing.T) {
	cases := []struct {
		name  string
		agent *storage.Agent
		want  string
	}{
		{
			name:  "prefers name",
			agent: &storage.Agent{Name: "Agent Friendly", Hostname: "host", AgentID: "id"},
			want:  "Agent Friendly",
		},
		{
			name:  "falls back to hostname",
			agent: &storage.Agent{Hostname: "host-only", AgentID: "id"},
			want:  "host-only",
		},
		{
			name:  "falls back to ID",
			agent: &storage.Agent{AgentID: "agent-id"},
			want:  "agent-id",
		},
	}
	for _, tc := range cases {
		if got := displayNameForAgent(tc.agent); got != tc.want {
			t.Fatalf("%s: expected %s, got %s", tc.name, tc.want, got)
		}
	}
}

// TestAdminBootstrapLogic verifies the admin user bootstrap pattern from main.go.
// GetUserByUsername returns sql.ErrNoRows for a non-existent user, which
// bootstrapAdminUser in main.go handles by creating the admin user.
func TestAdminBootstrapLogic(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	adminUser := "admin"
	adminPass := "testpass123"

	t.Run("CreatesAdminWhenNoUserExists", func(t *testing.T) {
		// Simulate the bootstrap logic from main.go
		existingUser, err := store.GetUserByUsername(ctx, adminUser)
		// sql.ErrNoRows means user doesn't exist - this is expected
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetUserByUsername failed with unexpected error: %v", err)
		}

		// If error is sql.ErrNoRows, existingUser should be nil
		if existingUser != nil {
			t.Fatal("Expected nil for non-existent user, got a user")
		}

		// Since user doesn't exist, create admin
		u := &storage.User{Username: adminUser, Role: storage.RoleAdmin}
		if err := store.CreateUser(ctx, u, adminPass); err != nil {
			t.Fatalf("CreateUser failed: %v", err)
		}

		// Verify user was created
		created, err := store.GetUserByUsername(ctx, adminUser)
		if err != nil {
			t.Fatalf("GetUserByUsername after create failed: %v", err)
		}
		if created == nil {
			t.Fatal("User was not created")
		}
		if created.Username != adminUser {
			t.Errorf("Username = %q, want %q", created.Username, adminUser)
		}
		if created.Role != storage.RoleAdmin {
			t.Errorf("Role = %q, want %q", created.Role, storage.RoleAdmin)
		}
	})

	t.Run("SkipsCreationWhenUserExists", func(t *testing.T) {
		// User should already exist from previous test
		existingUser, err := store.GetUserByUsername(ctx, adminUser)
		if err != nil {
			t.Fatalf("GetUserByUsername failed: %v", err)
		}

		if existingUser == nil {
			t.Fatal("Expected existing user, got nil")
		}

		// Trying to create the same user again should fail
		u := &storage.User{Username: adminUser, Role: storage.RoleAdmin}
		err = store.CreateUser(ctx, u, "differentpass")
		if err == nil {
			t.Error("Expected error when creating duplicate user, got nil")
		}
	})

	t.Run("GetUserByUsernameReturnsSqlErrNoRowsForMissingUser", func(t *testing.T) {
		// GetUserByUsername returns sql.ErrNoRows for non-existent users
		nonExistentUser, err := store.GetUserByUsername(ctx, "nonexistent_user_12345")
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetUserByUsername should return sql.ErrNoRows for missing user, got: %v", err)
		}
		if nonExistentUser != nil {
			t.Error("GetUserByUsername should return nil user for non-existent user")
		}
	})
}
