package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	pmsettings "printmaster/common/settings"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

type fakeStore struct {
	global        *storage.SettingsRecord
	tenants       map[string]*storage.Tenant
	tenantRecords map[string]*storage.TenantSettingsRecord
	agents        map[string]*storage.Agent
	agentRecords  map[string]*storage.AgentSettingsRecord
	lastGlobal    *storage.SettingsRecord
	lastTenant    *storage.TenantSettingsRecord
	deleteCalls   []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenants:       make(map[string]*storage.Tenant),
		tenantRecords: make(map[string]*storage.TenantSettingsRecord),
		agents:        make(map[string]*storage.Agent),
		agentRecords:  make(map[string]*storage.AgentSettingsRecord),
	}
}

func (s *fakeStore) GetGlobalSettings(context.Context) (*storage.SettingsRecord, error) {
	if s.global == nil {
		return nil, nil
	}
	copy := *s.global
	copy.Settings = s.global.Settings
	return &copy, nil
}

func (s *fakeStore) UpsertGlobalSettings(ctx context.Context, rec *storage.SettingsRecord) error {
	copy := *rec
	copy.Settings = rec.Settings
	s.global = &copy
	s.lastGlobal = rec
	return nil
}

func (s *fakeStore) GetTenantSettings(ctx context.Context, tenantID string) (*storage.TenantSettingsRecord, error) {
	if rec, ok := s.tenantRecords[tenantID]; ok {
		copy := *rec
		copy.Overrides = cloneMap(rec.Overrides)
		return &copy, nil
	}
	return nil, nil
}

func (s *fakeStore) UpsertTenantSettings(ctx context.Context, rec *storage.TenantSettingsRecord) error {
	copy := *rec
	copy.Overrides = cloneMap(rec.Overrides)
	s.tenantRecords[rec.TenantID] = &copy
	s.lastTenant = rec
	return nil
}

func (s *fakeStore) DeleteTenantSettings(ctx context.Context, tenantID string) error {
	delete(s.tenantRecords, tenantID)
	s.deleteCalls = append(s.deleteCalls, tenantID)
	return nil
}

func (s *fakeStore) GetTenant(ctx context.Context, tenantID string) (*storage.Tenant, error) {
	if tenant, ok := s.tenants[tenantID]; ok {
		copy := *tenant
		return &copy, nil
	}
	return nil, sql.ErrNoRows
}

func (s *fakeStore) GetAgent(ctx context.Context, agentID string) (*storage.Agent, error) {
	if agent, ok := s.agents[agentID]; ok {
		copy := *agent
		return &copy, nil
	}
	return nil, sql.ErrNoRows
}

func (s *fakeStore) GetAgentSettings(ctx context.Context, agentID string) (*storage.AgentSettingsRecord, error) {
	if rec, ok := s.agentRecords[agentID]; ok {
		copy := *rec
		copy.Overrides = cloneMap(rec.Overrides)
		return &copy, nil
	}
	return nil, nil
}

func (s *fakeStore) UpsertAgentSettings(ctx context.Context, rec *storage.AgentSettingsRecord) error {
	copy := *rec
	copy.Overrides = cloneMap(rec.Overrides)
	s.agentRecords[rec.AgentID] = &copy
	return nil
}

func (s *fakeStore) DeleteAgentSettings(ctx context.Context, agentID string) error {
	delete(s.agentRecords, agentID)
	return nil
}

func allowAllAuthorizer(_ *http.Request, _ authz.Action, _ authz.ResourceRef) error {
	return nil
}

func TestSettingsTenantSecurity(t *testing.T) {
	for _, role := range []storage.Role{storage.RoleOperator, storage.RoleViewer, storage.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			db, err := storage.NewSQLiteStore(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			initialSettings := pmsettings.DefaultSettings()
			initialSettings.SNMP.TimeoutMS = 4321
			if err := db.UpsertGlobalSettings(ctx, &storage.SettingsRecord{Settings: initialSettings, ManagedSections: []string{"discovery"}, UpdatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			initialGlobal, err := db.GetGlobalSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"tenant-a", "tenant-b"} {
				if err := db.CreateTenant(ctx, &storage.Tenant{ID: id, Name: id}); err != nil {
					t.Fatal(err)
				}
			}
			for _, tenantID := range []string{"tenant-a", "tenant-b", ""} {
				agent := &storage.Agent{AgentID: "agent-" + tenantID, TenantID: tenantID, Token: "original-" + tenantID, RegisteredAt: time.Now(), LastSeen: time.Now()}
				if err := db.RegisterAgent(ctx, agent); err != nil {
					t.Fatal(err)
				}
			}
			api, err := NewAPI(db, nil, APIOptions{Authorizer: func(r *http.Request, action authz.Action, ref authz.ResourceRef) error {
				if r.Header.Get("Authorization") == "" {
					return authz.ErrUnauthorized
				}
				return authz.Authorize(authz.Subject{Role: role, IsAdmin: role == storage.RoleAdmin, AllowedTenantIDs: []string{"tenant-a"}}, action, ref)
			}})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			api.RegisterRoutes(RouteConfig{Mux: mux, FeatureEnabled: true})
			globalBody, _ := json.Marshal(struct {
				pmsettings.Settings
				ManagedSections []string `json:"managed_sections"`
			}{pmsettings.DefaultSettings(), []string{"discovery"}})
			for _, tc := range []struct {
				path string
				body string
			}{
				{"/api/v1/settings/global", string(globalBody)},
				{"/api/v1/settings/tenants/tenant-a", `{"discovery":{"snmp_enabled":false}}`},
				{"/api/v1/settings/tenants/tenant-b", `{"discovery":{"snmp_enabled":false}}`},
				{"/api/v1/settings/agents/agent-tenant-a", `{"discovery":{"snmp_enabled":false}}`},
				{"/api/v1/settings/agents/agent-tenant-b", `{"discovery":{"snmp_enabled":false}}`},
				{"/api/v1/settings/agents/agent-", `{"discovery":{"snmp_enabled":false}}`},
			} {
				for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
					if strings.HasSuffix(tc.path, "/global") && method == http.MethodDelete {
						continue
					}
					t.Run(method+tc.path, func(t *testing.T) {
						req := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
						req.Header.Set("Authorization", "test-session")
						rr := httptest.NewRecorder()
						mux.ServeHTTP(rr, req)
						want := http.StatusOK
						if role != storage.RoleAdmin {
							global := strings.HasSuffix(tc.path, "/global")
							if strings.Contains(tc.path, "tenant-b") || strings.HasSuffix(tc.path, "agent-") || (global && method != http.MethodGet) || (role == storage.RoleViewer && method != http.MethodGet) {
								want = http.StatusForbidden
							}
						}
						if rr.Code != want {
							t.Fatalf("got %d, want %d: %s", rr.Code, want, rr.Body.String())
						}
					})
				}
			}
			if role != storage.RoleAdmin {
				global, err := db.GetGlobalSettings(ctx)
				if err != nil || !reflect.DeepEqual(global, initialGlobal) {
					t.Fatalf("forbidden global write persisted: %+v, %v", global, err)
				}
				for _, id := range []string{"agent-", "agent-tenant-b"} {
					rec, err := db.GetAgentSettings(ctx, id)
					if err != nil || rec != nil {
						t.Fatalf("forbidden agent write persisted: %+v, %v", rec, err)
					}
				}
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/v1/settings/global", strings.NewReader(string(globalBody))))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous write returned %d", rr.Code)
			}
		})
	}
}

func TestResolverResolveGlobalDefaults(t *testing.T) {
	store := newFakeStore()
	resolver, err := NewResolver(store)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}
	snap, err := resolver.ResolveGlobal(context.Background())
	if err != nil {
		t.Fatalf("resolve global failed: %v", err)
	}
	if snap.SchemaVersion != pmsettings.SchemaVersion {
		t.Fatalf("unexpected schema version: %s", snap.SchemaVersion)
	}
	defaults := pmsettings.DefaultSettings()
	if snap.Settings.Discovery.SNMPEnabled != defaults.Discovery.SNMPEnabled {
		t.Fatalf("expected default discovery settings")
	}
}

func TestResolverResolveForTenantOverrides(t *testing.T) {
	store := newFakeStore()
	store.global = &storage.SettingsRecord{SchemaVersion: "v1", Settings: pmsettings.DefaultSettings()}
	store.tenantRecords["tenant-a"] = &storage.TenantSettingsRecord{
		TenantID:      "tenant-a",
		SchemaVersion: "v1",
		Overrides: map[string]interface{}{
			"discovery": map[string]interface{}{"snmp_enabled": false},
		},
		UpdatedAt: time.Unix(200, 0),
		UpdatedBy: "tester",
	}
	resolver, err := NewResolver(store)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}
	snap, err := resolver.ResolveForTenant(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("resolve tenant failed: %v", err)
	}
	if snap.Settings.Discovery.SNMPEnabled {
		t.Fatalf("override not applied")
	}
	if len(snap.OverridePaths) != 1 || snap.OverridePaths[0] != "discovery.snmp_enabled" {
		t.Fatalf("unexpected override paths: %+v", snap.OverridePaths)
	}
	if snap.OverridesUpdatedBy != "tester" {
		t.Fatalf("override metadata missing")
	}
}

func TestAPIHandleGlobalPutPersistsSettings(t *testing.T) {
	store := newFakeStore()
	api, err := NewAPI(store, nil, APIOptions{
		Authorizer:    allowAllAuthorizer,
		ActorResolver: func(*http.Request) string { return "alice" },
	})
	if err != nil {
		t.Fatalf("NewAPI failed: %v", err)
	}
	payload := pmsettings.DefaultSettings()
	payload.SNMP.TimeoutMS = 1234
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/global", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	api.handleGlobal(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if store.lastGlobal == nil {
		t.Fatalf("global settings were not persisted")
	}
	if store.lastGlobal.Settings.SNMP.TimeoutMS != 1234 {
		t.Fatalf("override not saved")
	}
	if store.lastGlobal.UpdatedBy != "alice" {
		t.Fatalf("updated by not recorded")
	}
}

func TestAPIHandleTenantPutStoresOverrides(t *testing.T) {
	store := newFakeStore()
	store.global = &storage.SettingsRecord{SchemaVersion: "v1", Settings: pmsettings.DefaultSettings()}
	store.tenants["tenant-a"] = &storage.Tenant{ID: "tenant-a", Name: "Tenant A"}
	api, err := NewAPI(store, nil, APIOptions{
		Authorizer:    allowAllAuthorizer,
		ActorResolver: func(*http.Request) string { return "bob" },
	})
	if err != nil {
		t.Fatalf("NewAPI failed: %v", err)
	}
	patch := map[string]interface{}{
		"discovery": map[string]interface{}{"snmp_enabled": false},
	}
	body, _ := json.Marshal(patch)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/tenants/tenant-a", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	api.handleTenantSettings(rr, req, "tenant-a")
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if store.lastTenant == nil {
		t.Fatalf("tenant overrides not saved")
	}
	if store.lastTenant.UpdatedBy != "bob" {
		t.Fatalf("actor not recorded")
	}
	if _, ok := store.lastTenant.Overrides["discovery"].(map[string]interface{})["snmp_enabled"]; !ok {
		t.Fatalf("override missing from stored record: %+v", store.lastTenant.Overrides)
	}
}
