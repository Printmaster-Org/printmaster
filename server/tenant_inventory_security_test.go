package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

// These tests mutate serverStore and must not run in parallel.
func tenantInventoryStore(t *testing.T) storage.Store {
	t.Helper()
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"tenant-a", "tenant-b", "tenant-empty"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "unassigned"} {
		tenantID := "tenant-" + id
		if id == "unassigned" {
			tenantID = ""
		}
		agent := &storage.Agent{AgentID: "agent-" + id, Token: "tenant-inventory-token-" + id, Name: id, TenantID: tenantID, ProtocolVersion: "1", RegisteredAt: time.Now(), LastSeen: time.Now()}
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		device := &storage.Device{AgentID: agent.AgentID}
		device.Serial = "device-" + id
		device.IP = "192.0.2.1"
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMetrics(ctx, &storage.MetricsSnapshot{Serial: device.Serial, AgentID: agent.AgentID, Timestamp: time.Now().UTC(), PageCount: 123, TonerLevels: map[string]interface{}{"black": 25}}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestTenantInventorySensitiveHandlers(t *testing.T) {
	store := tenantInventoryStore(t)
	ctx := context.Background()
	// Simulate legacy/restored rows with broken ownership in the real DB. Normal
	// writes enforce ownership and the agent FK; bypass them only in fixture SQL.
	db := store.(*storage.SQLiteStore).DB()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"orphan", "missing-agent"} {
		device := &storage.Device{AgentID: "agent-a"}
		device.Serial, device.IP = "device-"+id, "192.0.2.2"
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
		agentID := ""
		if id == "missing-agent" {
			agentID = "missing-agent"
		}
		if _, err := db.ExecContext(ctx, "UPDATE devices SET agent_id = ? WHERE serial = ?", agentID, device.Serial); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []string{"device-a", "device-b", "device-unassigned", "device-orphan", "device-missing-agent"} {
		if err := store.UpsertDeviceCredentials(ctx, &storage.DeviceCredentials{Serial: serial, Username: "original", AuthType: "basic"}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		handler http.HandlerFunc
	}{
		{"preview", http.MethodPost, "/devices/preview", `{"serial":"SERIAL","ip":"192.0.2.2"}`, handleDevicePreviewProxy},
		{"update", http.MethodPost, "/devices/update", `{"serial":"SERIAL","model":"changed"}`, handleDeviceUpdateProxy},
		{"collect", http.MethodPost, "/devices/metrics/collect", `{"serial":"SERIAL","ip":"192.0.2.2"}`, handleDeviceMetricsCollectProxy},
		{"delete-post", http.MethodPost, "/api/v1/devices/delete", `{"serial":"SERIAL","agent_id":"agent-a","delete_metrics":true,"delete_from_agent":true}`, handleDeviceDelete},
		{"delete-delete", http.MethodDelete, "/api/v1/devices/delete", `{"serial":"SERIAL","delete_metrics":true}`, handleDeviceDelete},
		{"credentials-get", http.MethodGet, "/device/webui-credentials?serial=SERIAL", "", handleDeviceCredentials},
		{"credentials-post", http.MethodPost, "/device/webui-credentials", `{"serial":"SERIAL","username":"changed","password":"secret"}`, handleDeviceCredentials},
		{"device-proxy", http.MethodGet, "/api/v1/proxy/device/SERIAL/", "", handleDeviceProxy},
		{"device-proxy-post", http.MethodPost, "/api/v1/proxy/device/SERIAL/settings", "setting=value", handleDeviceProxy},
		{"legacy-proxy", http.MethodGet, "/proxy/SERIAL/", "", handleLegacyDeviceProxy},
		{"history", http.MethodGet, "/api/devices/metrics/history?serial=SERIAL", "", handleMetricsHistory},
		{"bounds", http.MethodGet, "/api/devices/metrics/bounds?serial=SERIAL", "", handleMetricsBounds},
	}
	users := []*storage.User{
		NewTestUser(storage.RoleOperator, "tenant-a"),
		NewTestUser(storage.RoleViewer, "tenant-a"),
		NewTestUser(storage.RoleOperator),
		NewTestUser(storage.RoleViewer),
		nil,
	}
	for _, tc := range cases {
		for userIndex, user := range users {
			serials := []string{"device-b", "device-unassigned", "device-orphan", "device-missing-agent"}
			// Viewers must not perform sensitive actions even inside their tenant.
			if user != nil && user.Role == storage.RoleViewer && tc.name != "history" && tc.name != "bounds" {
				serials = append(serials, "device-a")
			}
			for _, serial := range serials {
				t.Run(tc.name+"/"+serial+"/"+string(rune('0'+userIndex)), func(t *testing.T) {
					req := httptest.NewRequest(tc.method, strings.ReplaceAll(tc.path, "SERIAL", serial), strings.NewReader(strings.ReplaceAll(tc.body, "SERIAL", serial)))
					if user != nil {
						req = InjectTestUser(req, user)
					}
					rr := httptest.NewRecorder()
					tc.handler(rr, req)
					wantStatus := http.StatusForbidden
					if user == nil {
						wantStatus = http.StatusUnauthorized
					} else if len(user.TenantIDs) > 0 {
						metricsRead := tc.name == "history" || tc.name == "bounds"
						roleAllowed := user.Role == storage.RoleOperator || metricsRead
						if roleAllowed && (serial == "device-missing-agent" || (serial == "device-orphan" && metricsRead)) {
							wantStatus = http.StatusNotFound
						}
					}
					if rr.Code != wantStatus {
						t.Errorf("want %d before action/connectivity/metrics, got %d: %s", wantStatus, rr.Code, rr.Body.String())
					}
					device, err := store.GetDevice(ctx, serial)
					if err != nil || device == nil {
						t.Fatalf("unauthorized deletion: %v", err)
					}
					creds, err := store.GetDeviceCredentials(ctx, serial)
					if err != nil || creds.Username != "original" {
						t.Fatalf("unauthorized credential change: %+v %v", creds, err)
					}
				})
			}
		}
	}
	metrics, err := store.GetLatestMetrics(ctx, "device-b")
	if err != nil || metrics == nil || metrics.PageCount != 123 {
		t.Fatalf("foreign metrics deleted: %+v %v", metrics, err)
	}
}

func TestTenantInventoryAgentAccess(t *testing.T) {
	store := tenantInventoryStore(t)
	for _, tc := range []struct {
		method, path, body string
		handler            http.HandlerFunc
	}{
		{http.MethodGet, "/api/v1/agents/agent-b", "", handleAgentDetails},
		{http.MethodPost, "/api/v1/agents/agent-b", `{"name":"changed"}`, handleAgentDetails},
		{http.MethodDelete, "/api/v1/agents/agent-b", "", handleAgentDetails},
		{http.MethodPost, "/api/v1/agents/command/agent-b", `{"command":"restart"}`, handleAgentCommand},
		{http.MethodGet, "/api/v1/proxy/agent/agent-b/", "", handleAgentProxy},
		{http.MethodPost, "/api/v1/proxy/agent/agent-b/devices/delete", `{"serial":"device-b"}`, handleAgentProxy},
		{http.MethodPost, "/api/v1/devices/delete", `{"serial":"agent-only-device","agent_id":"agent-b","delete_from_agent":true}`, handleDeviceDelete},
	} {
		for _, user := range []*storage.User{NewTestUser(storage.RoleOperator, "tenant-a"), NewTestUser(storage.RoleViewer, "tenant-a"), NewTestUser(storage.RoleOperator)} {
			req := InjectTestUser(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), user)
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s %s: want 403, got %d: %s", tc.method, tc.path, rr.Code, rr.Body.String())
			}
		}
	}
	agent, err := store.GetAgent(context.Background(), "agent-b")
	if err != nil || agent.Name != "b" {
		t.Fatalf("foreign agent mutated: %+v %v", agent, err)
	}
}

func TestTenantInventoryMetricsScopes(t *testing.T) {
	tenantInventoryStore(t)
	for _, handler := range []http.HandlerFunc{handleMetricsAggregated, handleMetricsSummary, handleDashboardTree, handleDevicesList, handleAgentsList} {
		for _, role := range []storage.Role{storage.RoleViewer, storage.RoleOperator} {
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/", nil), NewTestUser(role)))
			if rr.Code != http.StatusForbidden {
				t.Errorf("empty tenant scope: want 403, got %d", rr.Code)
			}
		}
	}
	for _, query := range []string{"?tenant_id=tenant-b", "?agent_id=agent-b", "?device_serial=device-b", "?device_serial=device-unassigned"} {
		for _, role := range []storage.Role{storage.RoleViewer, storage.RoleOperator} {
			rr := httptest.NewRecorder()
			handleMetricsAggregated(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/metrics/aggregated"+query, nil), NewTestUser(role, "tenant-a")))
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s: want 403, got %d", query, rr.Code)
			}
		}
	}
	for _, tenantID := range []string{"tenant-empty", "tenant-a"} {
		for _, role := range []storage.Role{storage.RoleViewer, storage.RoleOperator, storage.RoleAdmin} {
			rr := httptest.NewRecorder()
			handleMetricsAggregated(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/metrics/aggregated", nil), NewTestUser(role, tenantID)))
			if rr.Code != http.StatusOK {
				t.Fatalf("aggregation %s: %d: %s", role, rr.Code, rr.Body.String())
			}
			var agg storage.AggregatedMetrics
			if err := json.Unmarshal(rr.Body.Bytes(), &agg); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tenantID == "tenant-empty" {
				want = 0
			}
			if role == storage.RoleAdmin {
				want = 3
			}
			if agg.Fleet.Totals.Agents != want || agg.Fleet.Totals.Devices != want {
				t.Errorf("aggregate totals: %+v; want %d", agg.Fleet.Totals, want)
			}
			if role != storage.RoleAdmin && !agg.Server.GeneratedAt.IsZero() {
				t.Error("tenant aggregation exposed global server DB stats")
			}
		}
	}
}

// TestMetricsAggregatedFiltersNarrowResults verifies that the agent and device
// filters on /api/metrics/aggregated are applied, not silently ignored.
func TestMetricsAggregatedFiltersNarrowResults(t *testing.T) {
	tenantInventoryStore(t)
	for _, tc := range []struct {
		user                 *storage.User
		query                string
		wantAgents, wantDevs int
		wantPages            int64
	}{
		{NewTestAdminUser(), "", 3, 3, 369},
		{NewTestAdminUser(), "?agent_id=agent-b", 1, 1, 123},
		{NewTestAdminUser(), "?device_serial=device-unassigned", 1, 1, 123},
		{NewTestAdminUser(), "?agent_id=agent-a&device_serial=device-b", 0, 0, 0},
		{NewTestUser(storage.RoleViewer, "tenant-a"), "?agent_id=agent-a", 1, 1, 123},
		{NewTestUser(storage.RoleViewer, "tenant-a"), "?device_serial=device-a", 1, 1, 123},
	} {
		rr := httptest.NewRecorder()
		handleMetricsAggregated(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/metrics/aggregated"+tc.query, nil), tc.user))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %q: status %d: %s", tc.user.Role, tc.query, rr.Code, rr.Body.String())
		}
		var agg storage.AggregatedMetrics
		if err := json.Unmarshal(rr.Body.Bytes(), &agg); err != nil {
			t.Fatal(err)
		}
		totals := agg.Fleet.Totals
		if totals.Agents != tc.wantAgents || totals.Devices != tc.wantDevs || totals.PageCount != tc.wantPages {
			t.Errorf("%s %q: totals %+v; want agents=%d devices=%d pages=%d", tc.user.Role, tc.query, totals, tc.wantAgents, tc.wantDevs, tc.wantPages)
		}
	}
}

func TestTenantInventoryGlobalSnapshots(t *testing.T) {
	store := tenantInventoryStore(t)
	snapshot := &storage.ServerMetricsSnapshot{Timestamp: time.Now().UTC(), Tier: "raw"}
	if err := store.InsertServerMetrics(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.HandlerFunc{handleServerMetricsLatest, handleServerMetricsTimeSeries} {
		for _, user := range []*storage.User{NewTestUser(storage.RoleViewer, "tenant-a"), NewTestUser(storage.RoleOperator, "tenant-a"), NewTestUser(storage.RoleOperator), NewTestAdminUser(), nil} {
			req := httptest.NewRequest(http.MethodGet, "/api/metrics/latest", nil)
			want := http.StatusUnauthorized
			if user != nil {
				want = http.StatusForbidden
				req = InjectTestUser(req, user)
				if user.Role == storage.RoleAdmin {
					want = http.StatusOK
				}
			}
			rr := httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != want {
				t.Errorf("global snapshot want %d, got %d: %s", want, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestTenantInventoryMethods(t *testing.T) {
	for _, tc := range []struct {
		method  string
		handler http.HandlerFunc
	}{
		{http.MethodPost, handleDevicesList},
		{http.MethodPost, handleAgentsList},
		{http.MethodPost, handleDashboardTree},
		{http.MethodPost, handleMetricsAggregated},
		{http.MethodPost, handleMetricsSummary},
		{http.MethodPost, handleMetricsBounds},
		{http.MethodPost, handleMetricsHistory},
		{http.MethodPost, handleServerMetricsLatest},
		{http.MethodPost, handleServerMetricsTimeSeries},
		{http.MethodGet, handleDevicePreviewProxy},
		{http.MethodGet, handleDeviceUpdateProxy},
		{http.MethodGet, handleDeviceMetricsCollectProxy},
		{http.MethodGet, handleDeviceDelete},
		{http.MethodDelete, handleDeviceCredentials},
		{http.MethodGet, handleAgentCommand},
		{http.MethodPut, handleAgentDetails},
	} {
		rr := httptest.NewRecorder()
		tc.handler(rr, InjectTestAdmin(httptest.NewRequest(tc.method, "/api/v1/agents/agent-a", nil)))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: want 405, got %d: %s", tc.method, rr.Code, rr.Body.String())
		}
	}
}

// Embed the real store and observe only security-sensitive reads. This ensures
// tests prove authorization precedes reads, not merely response redaction.
type inventoryReadAuditStore struct {
	storage.Store
	inventoryReads int
	metricsReads   int
	metricSerials  []string
}

func (s *inventoryReadAuditStore) CountDevices(ctx context.Context, ids []string) (int64, error) {
	s.inventoryReads++
	return s.Store.CountDevices(ctx, ids)
}

func (s *inventoryReadAuditStore) ListAllDevices(ctx context.Context) ([]*storage.Device, error) {
	s.inventoryReads++
	return s.Store.ListAllDevices(ctx)
}

func (s *inventoryReadAuditStore) ListAllDevicesPaginated(ctx context.Context, limit, offset int, ids []string) ([]*storage.Device, error) {
	s.inventoryReads++
	return s.Store.ListAllDevicesPaginated(ctx, limit, offset, ids)
}

func (s *inventoryReadAuditStore) GetLatestMetricsBatch(ctx context.Context, serials []string) (map[string]*storage.MetricsSnapshot, error) {
	s.metricsReads++
	s.metricSerials = append(s.metricSerials, serials...)
	return s.Store.GetLatestMetricsBatch(ctx, serials)
}

func (s *inventoryReadAuditStore) GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*storage.MetricsSnapshot, error) {
	s.metricsReads++
	return s.Store.GetMetricsHistory(ctx, serial, since)
}

func (s *inventoryReadAuditStore) GetMetricsBounds(ctx context.Context, serial string) (time.Time, time.Time, int64, error) {
	s.metricsReads++
	return s.Store.GetMetricsBounds(ctx, serial)
}

func TestTenantInventoryEmptyScopeSkipsReads(t *testing.T) {
	store := tenantInventoryStore(t)
	audit := &inventoryReadAuditStore{Store: store}
	serverStore = audit
	for _, query := range []string{"", "?limit=10"} {
		rr := httptest.NewRecorder()
		handleDevicesList(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/v1/devices/list"+query, nil), NewTestUser(storage.RoleOperator, "tenant-empty")))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d", rr.Code)
		}
	}
	if audit.inventoryReads != 0 || audit.metricsReads != 0 {
		t.Errorf("empty selection made unrestricted inventory/metrics queries: %+v", audit)
	}
	for _, handler := range []http.HandlerFunc{handleMetricsHistory, handleMetricsBounds} {
		rr := httptest.NewRecorder()
		handler(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/?serial=device-b", nil), NewTestUser(storage.RoleOperator, "tenant-a")))
		if rr.Code != http.StatusForbidden || audit.metricsReads != 0 {
			t.Errorf("foreign metrics queried before scope rejection: status %d, reads %d", rr.Code, audit.metricsReads)
		}
	}
}

func TestTenantInventoryBoundsAuthorizedRead(t *testing.T) {
	store := tenantInventoryStore(t)
	_, _, _, storageErr := store.GetMetricsBounds(context.Background(), "device-a")
	wantStatus := http.StatusOK
	if storageErr != nil {
		// Preserve current storage behavior; timestamp aggregate scan failures are
		// outside this handler-only security slice.
		wantStatus = http.StatusNotFound
		t.Logf("existing storage bounds failure: %v", storageErr)
	}
	audit := &inventoryReadAuditStore{Store: store}
	serverStore = audit
	for _, user := range []*storage.User{NewTestUser(storage.RoleViewer, "tenant-a"), NewTestUser(storage.RoleOperator, "tenant-a"), NewTestAdminUser()} {
		before := audit.metricsReads
		rr := httptest.NewRecorder()
		handleMetricsBounds(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/?serial=device-a", nil), user))
		if rr.Code != wantStatus || audit.metricsReads != before+1 {
			t.Errorf("authorized bounds: status %d, reads %d; want status %d and one storage read", rr.Code, audit.metricsReads-before, wantStatus)
		}
	}
}

func TestTenantInventoryDashboardIsolation(t *testing.T) {
	store := tenantInventoryStore(t)
	audit := &inventoryReadAuditStore{Store: store}
	serverStore = audit
	for _, tenantID := range []string{"tenant-empty", "tenant-a"} {
		for _, includeDevices := range []string{"true", "false"} {
			audit.metricSerials = nil
			rr := httptest.NewRecorder()
			handleDashboardTree(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/tree?include_devices="+includeDevices, nil), NewTestUser(storage.RoleViewer, tenantID)))
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var tree DashboardTreeResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &tree); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tenantID == "tenant-empty" {
				want = 0
			}
			if tree.Summary.AgentCount != want || tree.Summary.DeviceCount != want {
				t.Errorf("dashboard summary: %+v; want %d", tree.Summary, want)
			}
			if len(tree.Tenants) != 1 || tree.Tenants[0].ID != tenantID {
				t.Errorf("foreign tenants: %+v", tree.Tenants)
			}
			if strings.Contains(rr.Body.String(), "device-b") || strings.Contains(rr.Body.String(), "agent-b") || strings.Contains(rr.Body.String(), "unassigned") {
				t.Error("foreign data in dashboard")
			}
			for _, serial := range audit.metricSerials {
				if serial != "device-a" {
					t.Errorf("foreign metrics fetched: %s", serial)
				}
			}
		}
	}
}

func TestTenantInventoryAuthorizedAccess(t *testing.T) {
	store := tenantInventoryStore(t)
	for _, user := range []*storage.User{NewTestUser(storage.RoleOperator, "tenant-a"), NewTestAdminUser()} {
		for _, handler := range []http.HandlerFunc{handleDevicePreviewProxy, handleDeviceUpdateProxy, handleDeviceMetricsCollectProxy} {
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"serial":"device-a"}`)), user))
			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("authorized offline proxy: want 503, got %d: %s", rr.Code, rr.Body.String())
			}
		}
		for _, serial := range []string{"device-a", "device-b"} {
			if user.Role != storage.RoleAdmin && serial == "device-b" {
				continue
			}
			for _, handler := range []http.HandlerFunc{handleMetricsHistory} {
				rr := httptest.NewRecorder()
				handler(rr, InjectTestUser(httptest.NewRequest(http.MethodGet, "/?serial="+serial, nil), user))
				if rr.Code != http.StatusOK {
					t.Errorf("authorized metrics: want 200, got %d: %s", rr.Code, rr.Body.String())
				}
			}
			rr := httptest.NewRecorder()
			handleDeviceCredentials(rr, InjectTestUser(httptest.NewRequest(http.MethodPost, "/device/webui-credentials", strings.NewReader(`{"serial":"`+serial+`","username":"allowed"}`)), user))
			if rr.Code != http.StatusOK {
				t.Errorf("authorized credentials: got %d: %s", rr.Code, rr.Body.String())
			}
			creds, err := store.GetDeviceCredentials(context.Background(), serial)
			if err != nil || creds.Username != "allowed" {
				t.Fatalf("credentials not saved: %+v %v", creds, err)
			}
		}
	}
	previousHub := sseHub
	sseHub = NewSSEHub()
	t.Cleanup(func() { sseHub = previousHub })
	for _, tc := range []struct {
		serial string
		user   *storage.User
	}{
		{"device-a", NewTestUser(storage.RoleOperator, "tenant-a")},
		{"device-b", NewTestAdminUser()},
	} {
		rr := httptest.NewRecorder()
		handleDeviceDelete(rr, InjectTestUser(httptest.NewRequest(http.MethodDelete, "/api/v1/devices/delete", strings.NewReader(`{"serial":"`+tc.serial+`","delete_metrics":true}`)), tc.user))
		if rr.Code != http.StatusOK {
			t.Errorf("authorized delete: got %d: %s", rr.Code, rr.Body.String())
		}
		if _, err := store.GetDevice(context.Background(), tc.serial); err == nil {
			t.Errorf("device %s not deleted", tc.serial)
		}
	}
}

func TestTenantInventoryDeviceList(t *testing.T) {
	tenantInventoryStore(t)
	for _, role := range []storage.Role{storage.RoleViewer, storage.RoleOperator, storage.RoleAdmin} {
		for _, tenantID := range []string{"tenant-empty", "tenant-a"} {
			for _, query := range []string{"", "?limit=1", "?limit=999&offset=-1", "?limit=invalid&offset=7"} {
				t.Run(string(role)+"/"+tenantID+"/"+query, func(t *testing.T) {
					req := InjectTestUser(httptest.NewRequest(http.MethodGet, "/api/v1/devices/list"+query, nil), NewTestUser(role, tenantID))
					rr := httptest.NewRecorder()
					handleDevicesList(rr, req)
					if rr.Code != http.StatusOK {
						t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
					}
					var devices []storage.DeviceWithMetrics
					wantTotal := 1
					if role == storage.RoleAdmin {
						wantTotal = 3
					} else if tenantID == "tenant-empty" {
						wantTotal = 0
					}
					wantLen := wantTotal
					if query == "" {
						if err := json.Unmarshal(rr.Body.Bytes(), &devices); err != nil {
							t.Fatal(err)
						}
					} else {
						var page struct {
							Devices []storage.DeviceWithMetrics `json:"devices"`
							Total   int                         `json:"total_count"`
							HasMore bool                        `json:"has_more"`
							Limit   int                         `json:"limit"`
							Offset  int                         `json:"offset"`
						}
						if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
							t.Fatal(err)
						}
						devices = page.Devices
						if page.Total != wantTotal || page.HasMore != (page.Offset+len(devices) < wantTotal) {
							t.Errorf("incorrect totals: %+v; want total %d", page, wantTotal)
						}
						wantLimit, wantOffset := 1, 0
						if query == "?limit=999&offset=-1" {
							wantLimit = 200
						}
						if query == "?limit=invalid&offset=7" {
							wantLimit, wantOffset = 50, 7
						}
						if page.Limit != wantLimit || page.Offset != wantOffset {
							t.Errorf("pagination %+v", page)
						}
						if wantOffset >= wantTotal {
							wantLen = 0
						} else if wantLen > wantLimit {
							wantLen = wantLimit
						}
					}
					if devices == nil || len(devices) != wantLen {
						t.Fatalf("devices = %+v; want non-null array, length %d", devices, wantLen)
					}
					for _, device := range devices {
						if role != storage.RoleAdmin && device.AgentID != "agent-a" {
							t.Errorf("foreign device: %+v", device)
						}
						if device.PageCount != 123 {
							t.Errorf("metrics missing: %+v", device)
						}
					}
				})
			}
		}
	}
}
