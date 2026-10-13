package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestIsAdminOnlyAgentProxyPath(t *testing.T) {
	cases := map[string]bool{
		"api/diagnostics/snmp-walks":              true,
		"api/diagnostics/snmp-walks/abc/download": true,
		"api/diagnostics":                         true,
		"api/../api/diagnostics/snmp-walks":       true,
		"./api//diagnostics/snmp-walks":           true,
		"api/diagnosticsfoo":                      false,
		"api/devices":                             false,
		"discover":                                false,
		"":                                        false,
	}
	for in, want := range cases {
		if got := isAdminOnlyAgentProxyPath(in); got != want {
			t.Errorf("isAdminOnlyAgentProxyPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAgentProxyDiagnosticsRequireAdmin(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	if err := store.CreateTenant(ctx, &storage.Tenant{ID: "tenant-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterAgent(ctx, &storage.Agent{AgentID: "agent-a", Token: "walk-proxy-token", TenantID: "tenant-a", RegisteredAt: time.Now(), LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}

	operator := NewTestUser(storage.RoleOperator, "tenant-a")
	cases := []struct {
		name string
		user *storage.User
		path string
		want int
	}{
		// Agent is not WebSocket-connected, so requests that pass authz get 503.
		{"operator normal path", operator, "/api/v1/proxy/agent/agent-a/api/devices", http.StatusServiceUnavailable},
		{"operator walk list", operator, "/api/v1/proxy/agent/agent-a/api/diagnostics/snmp-walks", http.StatusForbidden},
		{"operator walk download", operator, "/api/v1/proxy/agent/agent-a/api/diagnostics/snmp-walks/x/download", http.StatusForbidden},
		{"admin walk list", NewTestAdminUser(), "/api/v1/proxy/agent/agent-a/api/diagnostics/snmp-walks", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := InjectTestUser(httptest.NewRequest(http.MethodGet, tc.path, nil), tc.user)
			rec := httptest.NewRecorder()
			handleAgentProxy(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
