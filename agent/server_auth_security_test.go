package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func securityAuthManager(serverURL, agentID string) *agentAuthManager {
	cfg := DefaultAgentConfig()
	cfg.Web.Auth.Mode = "server"
	cfg.Web.Auth.AllowLocalAdmin = false
	cfg.Server.URL = serverURL
	cfg.Server.AgentID = agentID
	return newAgentAuthManager(cfg, newAgentSessionManager())
}

func TestServerCallbackSecurity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]interface{})
		status int
		id     string
		allow  bool
	}{
		{"authorized", nil, 200, "machine-a", true},
		{"unauthenticated", nil, 401, "machine-a", false},
		{"forbidden", nil, 403, "machine-a", false},
		{"invalid", func(p map[string]interface{}) { p["valid"] = false }, 200, "machine-a", false},
		{"legacy-unbound", func(p map[string]interface{}) {
			delete(p, "agent_id")
			delete(p, "authorized")
			delete(p, "agent_tenant_id")
		}, 200, "machine-a", false},
		{"unauthorized", func(p map[string]interface{}) { p["authorized"] = false }, 200, "machine-a", false},
		{"wrong-agent", func(p map[string]interface{}) { p["agent_id"] = "machine-b" }, 200, "machine-a", false},
		{"wrong-tenant", func(p map[string]interface{}) { p["agent_tenant_id"] = "tenant-b" }, 200, "machine-a", false},
		{"missing-tenant", func(p map[string]interface{}) { delete(p, "agent_tenant_id") }, 200, "machine-a", false},
		{"unscoped", func(p map[string]interface{}) { delete(p, "tenant_ids"); delete(p, "tenant_id") }, 200, "machine-a", false},
		{"unknown-role", func(p map[string]interface{}) { p["role"] = "owner" }, 200, "machine-a", false},
		{"empty-user", func(p map[string]interface{}) { p["username"] = "" }, 200, "machine-a", false},
		{"expired", func(p map[string]interface{}) { p["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339) }, 200, "machine-a", false},
		{"missing-expiry", func(p map[string]interface{}) { delete(p, "expires_at") }, 200, "machine-a", false},
		{"no-agent-id", nil, 200, "", false},
		{"admin", func(p map[string]interface{}) { p["role"] = "admin"; delete(p, "tenant_ids") }, 200, "machine-a", true},
		{"viewer", func(p map[string]interface{}) { p["role"] = "viewer" }, 200, "machine-a", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/agent-callback/validate" {
					t.Error("callback endpoint changed")
				}
				var req map[string]string
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["agent_id"] != "machine-a" || req["token"] != "callback-token" || req["authorized"] != "" {
					t.Errorf("validation must send only token + machine identity: %+v %v", req, err)
				}
				payload := map[string]interface{}{
					"valid": true, "authorized": true, "agent_id": "machine-a", "agent_tenant_id": "tenant-a",
					"username": "operator", "role": "operator", "tenant_ids": []string{"tenant-a"},
					"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				}
				if tc.mutate != nil {
					tc.mutate(payload)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer server.Close()
			a := securityAuthManager(server.URL, tc.id)
			res := httptest.NewRecorder()
			a.handleAuthCallback(res, httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?token=callback-token&agent_id=machine-b&authorized=true&return_to=/devices", nil))
			if tc.allow {
				if res.Code != http.StatusFound || res.Header().Get("Location") != "/devices" || len(res.Result().Cookies()) != 1 {
					t.Fatalf("authorized callback failed: %d %+v", res.Code, res.Header())
				}
				cookie := res.Result().Cookies()[0]
				sess, ok := a.sessions.Get(cookie.Value)
				if !ok || sess.Principal.Source != "server-callback" || (tc.name != "admin" && len(sess.Principal.TenantIDs) != 1) {
					t.Fatal("callback principal/session scope lost")
				}
			} else if len(res.Result().Cookies()) != 0 || !strings.Contains(res.Header().Get("Location"), "error=invalid_token") {
				t.Fatalf("untrusted callback issued session: %d %+v", res.Code, res.Header())
			}
			if tc.id == "" && calls != 0 {
				t.Fatal("missing machine identity contacted validation server")
			}
		})
	}
}

func TestServerLoginTargetSecurity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     string
		target string
		tenant string
		role   string
		status int
		allow  bool
	}{
		{"authorized", "machine-a", "machine-a", "tenant-a", "operator", 200, true},
		{"viewer", "machine-a", "machine-a", "tenant-a", "viewer", 200, true},
		{"admin", "machine-a", "machine-a", "tenant-b", "admin", 200, true},
		{"foreign-response", "machine-a", "machine-a", "tenant-b", "operator", 200, false},
		{"wrong-agent", "machine-a", "machine-b", "tenant-a", "operator", 200, false},
		{"unassigned", "machine-a", "machine-a", "", "operator", 200, false},
		{"unauthenticated", "machine-a", "machine-a", "tenant-a", "operator", 401, false},
		{"forbidden", "machine-a", "machine-a", "tenant-a", "operator", 403, false},
		{"missing-agent", "machine-a", "machine-a", "tenant-a", "operator", 404, false},
		{"no-agent-id", "", "machine-a", "tenant-a", "operator", 200, false},
		{"unknown-role", "machine-a", "machine-a", "tenant-a", "owner", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls, targetCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/auth/login" {
					cookie, err := r.Cookie("pm_session")
					if err != nil || cookie.Value != "server-session" {
						t.Error("target/me request must use authenticated server session")
					}
				}
				switch r.URL.Path {
				case "/api/v1/auth/login":
					_ = json.NewEncoder(w).Encode(map[string]string{"token": "server-session", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
				case "/api/v1/auth/me":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"username": "user", "role": tc.role, "tenant_ids": []string{"tenant-a"}})
				case "/api/v1/agents/machine-a":
					targetCalls++
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]string{"agent_id": tc.target, "tenant_id": tc.tenant})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			a := securityAuthManager(server.URL, tc.id)
			res := httptest.NewRecorder()
			a.handleAuthLogin(res, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?authorized=true&agent_id=machine-b", strings.NewReader(`{"username":"user","password":"password","authorized":true,"agent_id":"machine-b"}`)))
			if tc.allow {
				if res.Code != http.StatusOK || len(res.Result().Cookies()) != 1 || targetCalls != 1 {
					t.Fatalf("authorized login failed: %d %s", res.Code, res.Body.String())
				}
			} else if res.Code == http.StatusOK || len(res.Result().Cookies()) != 0 {
				t.Fatalf("unauthorized login issued session: %d %s", res.Code, res.Body.String())
			}
			if tc.id == "" && calls != 0 {
				t.Fatal("login without persisted agent ID contacted server")
			}
		})
	}
}

func TestServerAuthMachineIdentityAndRedirectSecurity(t *testing.T) {
	a := securityAuthManager("https://server.example", "original")
	// Configuration is hydrated/enrollment updates it after manager construction.
	a.config.Server.AgentID = "persisted-machine"
	loginURL, err := url.Parse(a.serverLoginURL(httptest.NewRequest(http.MethodGet, "http://agent.example/devices", nil)))
	if err != nil || loginURL.Query().Get("agent_id") != "persisted-machine" {
		t.Fatalf("login did not bind current machine: %v %v", loginURL, err)
	}
	callback, err := url.Parse(loginURL.Query().Get("redirect"))
	if err != nil || callback.Path != "/api/v1/auth/callback" || callback.Query().Get("agent_id") != "persisted-machine" {
		t.Fatalf("callback path/identity changed: %v %v", callback, err)
	}
	client, err := a.newServerHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(httptest.NewRequest("GET", "https://foreign.example", nil), nil); err != http.ErrUseLastResponse {
		t.Fatal("server auth followed redirect")
	}
	a.mode = "local"
	if _, _, _, err := a.validateServerCallbackToken(context.Background(), "token"); err == nil {
		t.Fatal("local auth accepted server callback")
	}
}
