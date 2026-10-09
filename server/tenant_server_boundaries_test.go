package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/common/logger"
	"printmaster/server/storage"

	"github.com/gorilla/websocket"
)

// Globals are restored; these tests must not run in parallel.
type boundaryFixture struct {
	store    storage.Store
	sessions map[string]string
	users    map[string]*storage.User
}

func newBoundaryFixture(t *testing.T) boundaryFixture {
	t.Helper()
	previousStore, previousLogger, previousHub := serverStore, serverLogger, sseHub
	store := SetupTestStore(t)
	// SQLite :memory: is per connection; concurrent stream authorization must
	// share the same fixture DB rather than open an empty second connection.
	store.(*storage.SQLiteStore).DB().SetMaxOpenConns(1)
	// Logger.Close releases agent.log; on Windows an open handle makes the
	// TempDir cleanup (registered first, so run after this one) fail the test.
	fixtureLogger := logger.New(logger.ERROR, t.TempDir(), 100)
	serverLogger = fixtureLogger
	sseHub = NewSSEHub()
	hub := sseHub
	t.Cleanup(func() {
		hub.Stop()
		serverStore, serverLogger, sseHub = previousStore, previousLogger, previousHub
		if err := fixtureLogger.Close(); err != nil {
			t.Logf("close fixture logger: %v", err)
		}
	})
	f := boundaryFixture{store: store, sessions: map[string]string{}, users: map[string]*storage.User{}}
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		tenantID := "boundary-" + id
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: tenantID, Name: tenantID}); err != nil {
			t.Fatal(err)
		}
		agent := &storage.Agent{AgentID: "boundary-agent-" + id, Name: id, TenantID: tenantID,
			Token: "boundary-machine-" + id, RegisteredAt: time.Now(), LastSeen: time.Now()}
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		device := &storage.Device{AgentID: agent.AgentID}
		device.Serial = "boundary-device-" + id
		device.Model = "original"
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []struct {
		name    string
		role    storage.Role
		tenants []string
	}{
		{"viewer", storage.RoleViewer, []string{"boundary-a"}},
		{"operator", storage.RoleOperator, []string{"boundary-a"}},
		{"other", storage.RoleOperator, []string{"boundary-b"}},
		{"empty", storage.RoleOperator, nil},
		{"admin", storage.RoleAdmin, nil},
	} {
		user := &storage.User{Username: "boundary-" + spec.name, Role: spec.role, TenantIDs: spec.tenants}
		if err := store.CreateUser(ctx, user, "boundary-test-password"); err != nil {
			t.Fatal(err)
		}
		session, err := store.CreateSession(ctx, user.ID, 60)
		if err != nil {
			t.Fatal(err)
		}
		f.sessions[spec.name], f.users[spec.name] = session.Token, user
	}
	return f
}

func (f boundaryFixture) request(t *testing.T, handler http.HandlerFunc, name, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token := f.sessions[name]; token != "" {
		r.AddCookie(&http.Cookie{Name: "pm_session", Value: token})
	}
	w := httptest.NewRecorder()
	requireWebAuth(handler)(w, r)
	return w
}

func TestTenantServerBoundariesMachineBatches(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, spec := range []struct {
		name, field string
		handler     http.HandlerFunc
	}{
		{"devices", "devices", handleDevicesBatch}, {"metrics", "metrics", handleMetricsBatch},
	} {
		for _, agentID := range []string{"boundary-agent-b", "", " boundary-agent-a "} {
			t.Run(spec.name+"/mismatch/"+agentID, func(t *testing.T) {
				body := `{"agent_id":"` + agentID + `","` + spec.field + `":[{"serial":"boundary-device-b","model":"stolen","page_count":999}]}`
				r := httptest.NewRequest(http.MethodPost, "/api/v1/"+spec.name+"/batch", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer boundary-machine-a")
				w := httptest.NewRecorder()
				requireAuth(spec.handler)(w, r)
				if w.Code != http.StatusForbidden {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
			})
		}
		for _, serial := range []string{"boundary-device-a", "boundary-device-b"} {
			body := `{"agent_id":"boundary-agent-a","timestamp":"2026-10-02T12:00:00Z","` + spec.field + `":[{"serial":"` + serial + `","model":"allowed","page_count":7}]}`
			r := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer boundary-machine-a")
			w := httptest.NewRecorder()
			requireAuth(spec.handler)(w, r)
			var response struct {
				Stored int `json:"stored"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			want := 1
			if serial == "boundary-device-b" {
				want = 0
			}
			if response.Stored != want {
				t.Fatalf("%s/%s stored=%d want=%d", spec.name, serial, response.Stored, want)
			}
		}
		w := httptest.NewRecorder()
		spec.handler(w, httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(`{"agent_id":"boundary-agent-a"}`)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("missing machine context: %d", w.Code)
		}
		w = f.request(t, spec.handler, "operator", http.MethodPost, "/batch", `{"agent_id":"boundary-agent-a"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("session is not machine identity: %d", w.Code)
		}
	}
	device, err := f.store.GetDevice(context.Background(), "boundary-device-b")
	if err != nil || device.AgentID != "boundary-agent-b" || device.Model != "original" {
		t.Fatalf("foreign device changed: %+v %v", device, err)
	}
	metric, _ := f.store.GetLatestMetrics(context.Background(), "boundary-device-b")
	if metric != nil {
		t.Fatalf("foreign metrics written: %+v", metric)
	}
}

func TestTenantServerBoundariesCredentialTenant(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, tenant := range []string{"boundary-b", "", "boundary-a"} {
		if err := f.store.UpsertDeviceCredentials(context.Background(), &storage.DeviceCredentials{
			Serial: "boundary-device-a", TenantID: tenant, Username: "sensitive", AuthType: "basic",
		}); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/device-credentials?serial=boundary-device-a", nil)
		r.Header.Set("Authorization", "Bearer boundary-machine-a")
		w := httptest.NewRecorder()
		requireAuth(handleAgentDeviceCredentials)(w, r)
		want := http.StatusForbidden
		if tenant == "boundary-a" {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Fatalf("tenant %q: %d: %s", tenant, w.Code, w.Body.String())
		}
		if want == http.StatusForbidden && strings.Contains(w.Body.String(), "sensitive") {
			t.Fatal("credential disclosure")
		}
	}
}

func TestTenantServerBoundariesAdminActions(t *testing.T) {
	f := newBoundaryFixture(t)
	previousIntake := intakeWorker
	intakeWorker = nil
	t.Cleanup(func() { intakeWorker = previousIntake })
	for _, spec := range []struct {
		name, method string
		handler      http.HandlerFunc
		adminStatus  int
	}{
		{"logs", http.MethodGet, handleLogs, http.StatusOK},
		{"logs-clear", http.MethodPost, handleLogsClear, http.StatusOK},
		{"settings-sources", http.MethodGet, handleServerSettingsSources, http.StatusOK},
		{"release-sync", http.MethodPost, handleReleasesSync, http.StatusServiceUnavailable},
	} {
		for _, name := range []string{"viewer", "operator", "other", "empty", "admin", "anonymous"} {
			w := f.request(t, spec.handler, name, spec.method, "/"+spec.name, "")
			want := http.StatusForbidden
			if name == "admin" {
				want = spec.adminStatus
			}
			if name == "anonymous" {
				want = http.StatusUnauthorized
			}
			if w.Code != want {
				t.Errorf("%s/%s: %d want %d: %s", spec.name, name, w.Code, want, w.Body.String())
			}
		}
	}
}

func TestTenantServerBoundariesProxyHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/proxy", nil)
	r.Header = http.Header{
		"Authorization": {"Bearer central-secret"}, "Cookie": {"pm_session=central-secret; device_session=printer-secret; pm_agent_session=agent-secret", "PM_SESSION=another-secret; prefs=dark"},
		"X-Printmaster-User": {"spoofed"}, "X-Printmaster-Role": {"admin"}, "X-Printmaster-Proxy": {"server"},
		"X-Printmaster-Server-Request": {"true"}, "X-Forwarded-For": {"127.0.0.1"}, "Forwarded": {"for=127.0.0.1"},
		"X-Internal-Auth": {"secret"}, "Proxy-Authorization": {"Basic secret"}, "Connection": {"keep-alive, X-Hop"},
		"X-Hop": {"secret"}, "Content-Type": {"application/json"}, "Accept": {"text/html"}, "X-CSRF-Token": {"device-csrf"},
	}
	for _, device := range []bool{false, true} {
		headers := outgoingProxyHeaders(r, device)
		for _, key := range []string{"Authorization", "X-Printmaster-User", "X-Printmaster-Role", "X-Printmaster-Proxy", "X-Printmaster-Server-Request", "X-Forwarded-For", "Forwarded", "X-Internal-Auth", "Proxy-Authorization", "Connection", "X-Hop"} {
			if _, exists := headers[key]; exists {
				t.Errorf("forwarded %s", key)
			}
		}
		if strings.Contains(headers["Cookie"], "central-secret") || strings.Contains(headers["Cookie"], "another-secret") || strings.Contains(headers["Cookie"], "agent-secret") {
			t.Fatal("forwarded central/agent credential")
		}
		if device && headers["Cookie"] != "device_session=printer-secret; prefs=dark" {
			t.Fatalf("device cookies lost: %q", headers["Cookie"])
		}
		if !device && headers["Cookie"] != "" {
			t.Fatal("agent UI retained browser cookies")
		}
		if headers["Content-Type"] != "application/json" || headers["X-Csrf-Token"] != "device-csrf" {
			t.Fatal("needed device headers lost")
		}
	}
}

func TestTenantServerBoundariesCallbacks(t *testing.T) {
	f := newBoundaryFixture(t)
	issue := func(name, agent string) *httptest.ResponseRecorder {
		return f.request(t, handleAgentAuthCallback, name, http.MethodPost, "/callback", `{"agent_id":"`+agent+`","callback_url":"https://agent.example/auth/callback"}`)
	}
	for _, name := range []string{"viewer", "operator", "empty"} {
		if w := issue(name, "boundary-agent-b"); w.Code != http.StatusForbidden {
			t.Fatalf("foreign callback %s: %d", name, w.Code)
		}
	}
	if w := issue("admin", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("unbound callback: %d", w.Code)
	}
	for _, callbackURL := range []string{"ftp://localhost/auth/callback", "https:///auth/callback", "https://user:password@agent.example/auth/callback"} {
		w := f.request(t, handleAgentAuthCallback, "operator", http.MethodPost, "/callback", `{"agent_id":"boundary-agent-a","callback_url":"`+callbackURL+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe callback %q: %d", callbackURL, w.Code)
		}
	}
	for _, target := range []string{"", "boundary-agent-b", "boundary-agent-a"} {
		w := issue("operator", "boundary-agent-a")
		var grant struct {
			Token string `json:"token"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &grant) != nil || grant.Token == "" {
			t.Fatalf("issuer %d: %s", w.Code, w.Body.String())
		}
		validate := func() *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			handleAgentAuthCallbackValidate(w, httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(`{"token":"`+grant.Token+`","agent_id":"`+target+`"}`)))
			return w
		}
		want := http.StatusForbidden
		if target == "boundary-agent-a" {
			want = http.StatusOK
		}
		if w := validate(); w.Code != want {
			t.Fatalf("target %q status %d want %d: %s", target, w.Code, want, w.Body.String())
		}
		if w := validate(); w.Code != http.StatusUnauthorized {
			t.Fatalf("one-time grant reused: %d", w.Code)
		}
	}
	grant := generateAgentCallbackToken(f.users["operator"], "boundary-agent-b", "https://agent.example/auth/callback")
	w := httptest.NewRecorder()
	handleAgentAuthCallbackValidate(w, httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(`{"token":"`+grant.Token+`","agent_id":"boundary-agent-b"}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("current ownership not checked: %d", w.Code)
	}
}

func TestTenantServerBoundariesEventClassification(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, spec := range []struct {
		event   SSEEvent
		visible bool
	}{
		{SSEEvent{Type: "agent_heartbeat", Data: map[string]interface{}{"agent_id": "boundary-agent-a"}}, true},
		{SSEEvent{Type: "device_updated", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "boundary-device-a"}}, true},
		{SSEEvent{Type: "device_updated", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "serial": "boundary-device-b"}}, false},
		{SSEEvent{Type: "agent_heartbeat", Data: map[string]interface{}{"agent_id": "boundary-agent-b", "tenant_id": "boundary-a"}}, false},
		{SSEEvent{Type: "agent_heartbeat", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "tenant_id": "boundary-b"}}, false},
		{SSEEvent{Type: "agent_heartbeat", Data: map[string]interface{}{"agent_id": "missing"}}, false},
		{SSEEvent{Type: "unknown", Data: map[string]interface{}{"agent_id": "boundary-agent-a", "tenant_id": "boundary-a"}}, false},
		{SSEEvent{Type: "metrics_snapshot", Data: map[string]interface{}{"tenant_id": "boundary-a"}}, false},
		{SSEEvent{Type: "release_sync_progress"}, false},
		{SSEEvent{Type: "agent_deleted", TenantID: "boundary-a", Data: map[string]interface{}{"agent_id": "deleted"}}, true},
		{SSEEvent{Type: "agent_deleted", Data: map[string]interface{}{"agent_id": "deleted", "tenant_id": "boundary-a"}}, false},
	} {
		if got := eventVisibleToPrincipal(context.Background(), newPrincipal(f.users["operator"]), spec.event); got != spec.visible {
			t.Errorf("event %+v visible=%v", spec.event, got)
		}
		if !eventVisibleToPrincipal(context.Background(), newPrincipal(f.users["admin"]), spec.event) {
			t.Errorf("admin lost event %+v", spec.event)
		}
		if eventVisibleToPrincipal(context.Background(), newPrincipal(f.users["empty"]), spec.event) || eventVisibleToPrincipal(context.Background(), nil, spec.event) {
			t.Errorf("empty/anonymous gained event %+v", spec.event)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	r.AddCookie(&http.Cookie{Name: "pm_session", Value: f.sessions["operator"]})
	if principal := streamPrincipal(r); principal == nil || !principal.CanAccessTenant("boundary-a") {
		t.Fatal("valid stream principal missing")
	}
	if err := f.store.DeleteSession(context.Background(), f.sessions["operator"]); err != nil {
		t.Fatal(err)
	}
	if streamPrincipal(r) != nil {
		t.Fatal("revoked session retained stream scope")
	}
}

func TestTenantServerBoundariesLiveStreams(t *testing.T) {
	f := newBoundaryFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/events", requireWebAuth(handleSSE))
	mux.HandleFunc("/ws", requireWebAuth(handleUIWebSocket))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, transport := range []string{"sse", "ws"} {
		for _, name := range []string{"operator", "admin"} {
			t.Run(transport+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var readType func() string
				if transport == "sse" {
					r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events", nil)
					r.AddCookie(&http.Cookie{Name: "pm_session", Value: f.sessions[name]})
					response, err := http.DefaultClient.Do(r)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					reader := bufio.NewReader(response.Body)
					readType = func() string {
						t.Helper()
						for {
							line, err := reader.ReadString('\n')
							if err != nil {
								t.Fatal(err)
							}
							if strings.HasPrefix(line, "event: ") {
								return strings.TrimSpace(strings.TrimPrefix(line, "event: "))
							}
						}
					}
				} else {
					headers := http.Header{"Cookie": {"pm_session=" + f.sessions[name]}, "Origin": {server.URL}}
					conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", headers)
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					readType = func() string {
						t.Helper()
						var message struct {
							Type string `json:"type"`
						}
						if err := conn.ReadJSON(&message); err != nil {
							t.Fatal(err)
						}
						return message.Type
					}
				}
				_ = readType() // connected/version confirms subscriber registration
				sseHub.Broadcast(SSEEvent{Type: "agent_connected", Data: map[string]interface{}{"agent_id": "boundary-agent-b"}})
				sseHub.Broadcast(SSEEvent{Type: "unknown_sensitive", Data: map[string]interface{}{"secret": "global"}})
				sseHub.Broadcast(SSEEvent{Type: "agent_heartbeat", Data: map[string]interface{}{"agent_id": "boundary-agent-a"}})
				if name == "admin" {
					if got := readType(); got != "agent_connected" {
						t.Fatalf("admin first event %q", got)
					}
					if got := readType(); got != "unknown_sensitive" {
						t.Fatalf("admin global event %q", got)
					}
				}
				if got := readType(); got != "agent_heartbeat" {
					t.Fatalf("cross-tenant/global event leaked: %q", got)
				}
			})
		}
	}
}
