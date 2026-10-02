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
	"strings"
	"testing"
	"time"

	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

type enrollmentSubjectKey struct{}

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
