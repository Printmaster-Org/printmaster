package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"printmaster/server/storage"
)

func TestOIDCProviderPayloadRoles(t *testing.T) {
	for _, role := range []string{"admin", "operator", "viewer", "user", "", " VIEWER "} {
		p, err := buildProviderFromPayload(&oidcProviderPayload{Slug: "test", DisplayName: "Test", Issuer: "https://issuer.example", ClientID: "client", ClientSecret: "secret", DefaultRole: role}, nil, "")
		if err != nil || p.DefaultRole != storage.NormalizeRole(role) {
			t.Fatalf("role %q: %+v %v", role, p, err)
		}
	}
	if _, err := buildProviderFromPayload(&oidcProviderPayload{Slug: "test", DisplayName: "Test", Issuer: "https://issuer.example", ClientID: "client", ClientSecret: "secret", DefaultRole: "owner"}, nil, ""); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestOIDCAgentRedirectIntegration(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	for _, target := range []string{"", "boundary-agent-b", "missing", "boundary-agent-a"} {
		callback := "https://agent.example/api/v1/auth/callback?return_to=%2Fdevices"
		if target != "" {
			callback += "&agent_id=" + target
		}
		before := len(agentCallbackTokens)
		redirect, err := issueOIDCAgentRedirect(ctx, f.users["operator"], callback)
		if target != "boundary-agent-a" {
			if err == nil || len(agentCallbackTokens) != before {
				t.Fatalf("unsupported target issued grant: %q %v", target, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(redirect)
		if u.Query().Get("return_to") != "/devices" || u.Query().Get("agent_id") != target {
			t.Fatal("redirect context lost")
		}
		body := `{"token":"` + u.Query().Get("token") + `","agent_id":"` + target + `"}`
		w := httptest.NewRecorder()
		handleAgentAuthCallbackValidate(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/agent-callback/validate", strings.NewReader(body)))
		var response struct {
			Valid      bool   `json:"valid"`
			Authorized bool   `json:"authorized"`
			AgentID    string `json:"agent_id"`
			TenantID   string `json:"agent_tenant_id"`
			Role       string `json:"role"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Valid || !response.Authorized || response.AgentID != target || response.TenantID != "boundary-a" || response.Role != "operator" {
			t.Fatalf("incomplete validation: %d %s", w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		handleAgentAuthCallbackValidate(w, httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader(body)))
		if w.Code != http.StatusUnauthorized {
			t.Fatal("grant replay accepted")
		}
	}
	for _, callback := range []string{"//evil.example/api/v1/auth/callback?agent_id=x", "http://evil.example/api/v1/auth/callback?agent_id=x", "https://u:p@agent.example/api/v1/auth/callback?agent_id=x", "https://agent.example/api/v1/auth/callback?agent_id=x&agent_id=y"} {
		if _, _, err := agentCallbackTarget(callback, ""); err == nil {
			t.Fatalf("unsafe callback accepted: %s", callback)
		}
	}
	if _, _, err := agentCallbackTarget("https://agent.example/api/v1/auth/callback?agent_id=x", "y"); err == nil {
		t.Fatal("conflicting targets accepted")
	}
}

func TestOIDCAgentStartRejectsUnsupportedTarget(t *testing.T) {
	f := newBoundaryFixture(t)
	provider := &storage.OIDCProvider{Slug: "integration", Issuer: "https://unused.invalid", ClientID: "client", DefaultRole: storage.RoleViewer}
	if err := f.store.CreateOIDCProvider(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	for _, callback := range []string{
		"https://agent.example/api/v1/auth/callback",
		"http://agent.example/api/v1/auth/callback?agent_id=boundary-agent-a",
		"https://agent.example/api/v1/auth/callback?agent_id=boundary-agent-b",
	} {
		query := url.Values{"redirect": {callback}}
		if strings.Contains(callback, "boundary-agent-b") {
			query.Set("agent_id", "boundary-agent-a")
		}
		w := httptest.NewRecorder()
		handleOIDCStart(w, httptest.NewRequest(http.MethodGet, "/auth/oidc/start/integration?"+query.Encode(), nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsupported start reached discovery: %d %s", w.Code, w.Body.String())
		}
	}
	if got := sanitizeRedirectTarget("https://agent.example/api/v1/auth/callback?agent_id=boundary-agent-a"); !strings.HasPrefix(got, "https://agent.example/") {
		t.Fatalf("supported callback normalized away: %s", got)
	}
	for _, redirect := range []string{"//evil.example", "https://evil.example/", "/\\evil.example", "/\t/evil.example"} {
		if got := sanitizeRedirectTarget(redirect); got != "/" {
			t.Fatalf("unsafe redirect survived: %q -> %q", redirect, got)
		}
	}
}