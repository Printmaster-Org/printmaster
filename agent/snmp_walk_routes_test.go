package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"printmaster/agent/agent"
)

func TestSNMPWalkRoutesRequireAdmin(t *testing.T) {
	snmpWalkManager = agent.NewSNMPWalkManager(t.TempDir(), nil, nil)
	t.Cleanup(func() { snmpWalkManager = nil })

	cases := []struct {
		name   string
		role   string
		status int
	}{
		{"operator via server", "operator", http.StatusForbidden},
		{"viewer via server", "viewer", http.StatusForbidden},
		{"missing role via server", "", http.StatusForbidden},
		{"admin via server", "admin", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, snmpWalkRoutePrefix, nil)
			req = req.WithContext(agent.WithServerProxy(req.Context(), agent.ServerProxyIdentity{User: "u", Role: tc.role}))
			rec := httptest.NewRecorder()
			handleSNMPWalks(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, snmpWalkRoutePrefix, nil)
	req = req.WithContext(context.WithValue(req.Context(), agentPrincipalContextKey, &AgentPrincipal{Username: "u", Role: "viewer"}))
	rec := httptest.NewRecorder()
	handleSNMPWalks(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("local viewer status = %d", rec.Code)
	}

	// Forged proxy headers from a network client must not grant admin.
	forged := httptest.NewRequest(http.MethodGet, snmpWalkRoutePrefix, nil)
	forged.Header.Set("X-PrintMaster-Proxy", "server")
	forged.Header.Set("X-PrintMaster-Role", "admin")
	forged = forged.WithContext(context.WithValue(forged.Context(), agentPrincipalContextKey, &AgentPrincipal{Username: "u", Role: "viewer"}))
	rec = httptest.NewRecorder()
	agent.StripUntrustedProxyHeaders(http.HandlerFunc(handleSNMPWalks)).ServeHTTP(rec, forged)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged header status = %d", rec.Code)
	}
}

func TestSNMPWalkRoutesListAndValidate(t *testing.T) {
	snmpWalkManager = agent.NewSNMPWalkManager(t.TempDir(), nil, nil)
	t.Cleanup(func() { snmpWalkManager = nil })

	admin := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(agent.WithServerProxy(req.Context(), agent.ServerProxyIdentity{User: "u", Role: "admin"}))
		rec := httptest.NewRecorder()
		handleSNMPWalks(rec, req)
		return rec
	}

	rec := admin(http.MethodGet, snmpWalkRoutePrefix, "")
	var list struct {
		DefaultMaxOIDs int      `json:"default_max_oids"`
		DefaultRoots   []string `json:"default_roots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.DefaultMaxOIDs != agent.DefaultSNMPWalkMaxOIDs || len(list.DefaultRoots) == 0 {
		t.Fatalf("list = %s (%v)", rec.Body.String(), err)
	}

	if rec := admin(http.MethodPost, snmpWalkRoutePrefix, `{"ip":"not-an-ip"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid ip status = %d", rec.Code)
	}
	if rec := admin(http.MethodGet, snmpWalkRoutePrefix+"/missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}
	if rec := admin(http.MethodGet, snmpWalkRoutePrefix+"/missing/download", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing download status = %d", rec.Code)
	}
}
