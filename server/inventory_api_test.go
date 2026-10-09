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

// Explicit forwarding prevents embedded Store from becoming an unsafe fallback.
func (s *inventoryReadAuditStore) InventoryIndex(ctx context.Context, scope storage.InventoryScope) ([]*storage.DeviceIndex, error) {
	return s.Store.(storage.InventoryStore).InventoryIndex(ctx, scope)
}

func (s *inventoryReadAuditStore) InventoryRows(ctx context.Context, scope storage.InventoryScope, keys []string, limit, offset int) ([]*storage.DeviceWithMetrics, error) {
	return s.Store.(storage.InventoryStore).InventoryRows(ctx, scope, keys, limit, offset)
}

func (s *inventoryReadAuditStore) InventoryCount(ctx context.Context, scope storage.InventoryScope) (int64, error) {
	return s.Store.(storage.InventoryStore).InventoryCount(ctx, scope)
}

func (s *inventoryReadAuditStore) InventoryMetrics(ctx context.Context, scope storage.InventoryScope, keys []string) ([]*storage.MetricsSnapshot, error) {
	return s.Store.(storage.InventoryStore).InventoryMetrics(ctx, scope, keys)
}

type progressiveReadAuditStore struct {
	*inventoryReadAuditStore
	ownerReads int
}

func (s *progressiveReadAuditStore) GetAgent(ctx context.Context, id string) (*storage.Agent, error) {
	s.ownerReads++
	return s.Store.GetAgent(ctx, id)
}

func (s *progressiveReadAuditStore) ListAgents(ctx context.Context) ([]*storage.Agent, error) {
	s.ownerReads++
	return s.Store.ListAgents(ctx)
}

func TestProgressiveInventoryHandlers(t *testing.T) {
	store := tenantInventoryStore(t)
	for serial, saved := range map[string]bool{"device-a": true, "device-b": false} {
		device, err := store.GetDevice(context.Background(), serial)
		if err != nil {
			t.Fatal(err)
		}
		device.IsSaved = &saved
		if err := store.UpsertDevice(context.Background(), device); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.(*storage.SQLiteStore).DB().Exec(`INSERT INTO metrics_history(serial,agent_id,timestamp,page_count,toner_levels) VALUES(?,?,?,?,?)`, "device-a", "agent-b", time.Now().UTC().Add(time.Hour), 9999, `{"black":1}`); err != nil {
		t.Fatal(err)
	}
	audit := &progressiveReadAuditStore{inventoryReadAuditStore: &inventoryReadAuditStore{Store: store}}
	serverStore = audit
	for _, role := range []storage.Role{storage.RoleViewer, storage.RoleOperator, storage.RoleAdmin} {
		for _, tc := range []struct {
			method, path, body string
			handler            http.HandlerFunc
		}{
			{"GET", "/api/v1/devices/index", "", handleDevicesIndex},
			{"POST", "/api/v1/devices/rows", `{"serials":["device-b","absent","device-a","device-a"]}`, handleDevicesRows},
			{"POST", "/api/v1/devices/metrics/query", `{"serials":["device-b","absent","device-a","device-a"]}`, handleDevicesMetricsQuery},
			{"GET", "/api/v1/devices/list", "", handleDevicesList},
			{"GET", "/api/v1/devices/list?limit=1", "", handleDevicesList},
		} {
			req := InjectTestUser(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), NewTestUser(role, "tenant-a"))
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != 200 {
				t.Fatalf("%s %s: %d %s", role, tc.path, rr.Code, rr.Body.String())
			}
			if role != storage.RoleAdmin && strings.Contains(rr.Body.String(), "device-b") {
				t.Fatal("foreign serial leaked")
			}
			if strings.Contains(rr.Body.String(), "9999") {
				t.Fatal("foreign-owner legacy metric leaked")
			}
			if strings.Contains(tc.path, "index") && (strings.Contains(rr.Body.String(), "raw_data") || strings.Contains(rr.Body.String(), "toner_levels")) {
				t.Fatal("index heavy fields")
			}
			if strings.Contains(tc.path, "rows") && (strings.Contains(rr.Body.String(), "toner_levels") || strings.Contains(rr.Body.String(), "last_metrics_at") || strings.Contains(rr.Body.String(), "color_pages")) {
				t.Fatal("row lazy metrics loaded")
			}
			if strings.Contains(tc.path, "list") && !strings.Contains(rr.Body.String(), "toner_levels") {
				t.Fatal("legacy toner lost")
			}
			if strings.Contains(tc.path, "rows") || strings.Contains(tc.path, "list") {
				if strings.Contains(rr.Body.String(), "device-a") && !strings.Contains(rr.Body.String(), `"is_saved":true`) {
					t.Fatalf("%s %s: Agent saved state missing: %s", role, tc.path, rr.Body.String())
				}
				if role != storage.RoleAdmin && strings.Contains(rr.Body.String(), `"is_saved":false`) {
					t.Fatal("foreign Agent discovered state leaked")
				}
			}
		}
	}
	for _, tc := range []struct {
		method, body string
		handler      http.HandlerFunc
	}{
		{"GET", "", handleDevicesIndex},
		{"POST", `{"serials":["device-a","device-b"]}`, handleDevicesRows},
		{"POST", `{"serials":["device-a","device-b"]}`, handleDevicesMetricsQuery},
	} {
		for _, user := range []*storage.User{nil, NewTestUser(storage.RoleViewer), NewTestUser(storage.RoleViewer, "tenant-empty")} {
			req := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			if user != nil {
				req = InjectTestUser(req, user)
			}
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			want := 200
			if user == nil {
				want = 401
			} else if len(user.TenantIDs) == 0 {
				want = 403
			}
			if rr.Code != want {
				t.Fatalf("scope: %d %s", rr.Code, rr.Body.String())
			}
			if want == 200 && strings.TrimSpace(rr.Body.String()) != "[]" {
				t.Fatalf("empty scope: %s", rr.Body.String())
			}
		}
	}
	if audit.ownerReads != 0 || audit.inventoryReads != 0 || audit.metricsReads != 0 {
		t.Fatalf("unscoped/per-owner fallback reads: %+v", audit)
	}
}

func TestProgressiveInventoryValidation(t *testing.T) {
	store := tenantInventoryStore(t)
	keys := make([]string, 101)
	for i := range keys {
		keys[i] = strings.Repeat("x", i+1)
	}
	tooMany, _ := json.Marshal(map[string]interface{}{"serials": keys})
	for _, handler := range []http.HandlerFunc{handleDevicesRows, handleDevicesMetricsQuery} {
		for _, valid := range [][]string{keys[:100], append([]string{"device-a"}, makeRepeatedInventoryKeys(150)...)} {
			body, _ := json.Marshal(map[string]interface{}{"serials": valid})
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest("POST", "/", strings.NewReader(string(body))), NewTestUser(storage.RoleViewer, "tenant-a")))
			if rr.Code != 200 {
				t.Fatalf("valid distinct-key bound: %d %s", rr.Code, rr.Body.String())
			}
		}
		for _, body := range []string{"", `null`, `[]`, `{}`, `{"serials":null}`, `{"serials":[1]}`, `{"serials":[""]}`, `{"serials":[" "]}`, `{"serials":[],"tenant_id":"tenant-b"}`, `{"serials":[]} {}`, string(tooMany), `{"serials":["` + strings.Repeat("x", 65536) + `"]}`} {
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest("POST", "/", strings.NewReader(body)), NewTestUser(storage.RoleViewer, "tenant-a")))
			if rr.Code != 400 {
				t.Fatalf("malformed body: %d %s", rr.Code, rr.Body.String())
			}
		}
		for _, body := range []string{`{"serials":[]}`, `{"serials":["missing","device-b"]}`} {
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest("POST", "/", strings.NewReader(body)), NewTestUser(storage.RoleViewer, "tenant-a")))
			if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != "[]" {
				t.Fatalf("empty: %d %s", rr.Code, rr.Body.String())
			}
		}
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			rr := httptest.NewRecorder()
			handler(rr, InjectTestUser(httptest.NewRequest(method, "/", nil), NewTestAdminUser()))
			if rr.Code != 405 {
				t.Fatalf("method: %d", rr.Code)
			}
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		rr := httptest.NewRecorder()
		handleDevicesIndex(rr, InjectTestUser(httptest.NewRequest(method, "/", nil), NewTestAdminUser()))
		if rr.Code != 405 {
			t.Fatalf("index method: %d", rr.Code)
		}
	}
	// A Store-only wrapper must fail closed; no all-fleet fallback.
	serverStore = struct{ storage.Store }{store}
	for _, tc := range []struct {
		method  string
		handler http.HandlerFunc
	}{{"GET", handleDevicesIndex}, {"GET", handleDevicesList}, {"POST", handleDevicesRows}, {"POST", handleDevicesMetricsQuery}} {
		rr := httptest.NewRecorder()
		tc.handler(rr, InjectTestUser(httptest.NewRequest(tc.method, "/", strings.NewReader(`{"serials":[]}`)), NewTestAdminUser()))
		if rr.Code != 500 {
			t.Fatalf("missing specialized storage: %d", rr.Code)
		}
	}
}

func makeRepeatedInventoryKeys(count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = "device-a"
	}
	return keys
}

func TestProgressiveInventorySessionRefresh(t *testing.T) {
	store := tenantInventoryStore(t)
	ctx := context.Background()
	user := NewTestUser(storage.RoleViewer, "tenant-a")
	if err := store.CreateUser(ctx, user, "inventory-test-password"); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, user.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		handler            http.HandlerFunc
	}{
		{"GET", "/api/v1/devices/index", "", handleDevicesIndex},
		{"POST", "/api/v1/devices/rows", `{"serials":["device-a","device-b"]}`, handleDevicesRows},
		{"POST", "/api/v1/devices/metrics/query", `{"serials":["device-a","device-b"]}`, handleDevicesMetricsQuery},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc(tc.path, requireWebAuth(tc.handler))
		for _, tenant := range []string{"tenant-a", "tenant-b", ""} {
			user.TenantID, user.TenantIDs = tenant, []string{}
			if tenant != "" {
				user.TenantIDs = []string{tenant}
			}
			if err := store.UpdateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+session.Token)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			want := 200
			if tenant == "" {
				want = 403
			}
			if rr.Code != want {
				t.Fatalf("fresh membership %s: %d %s", tenant, rr.Code, rr.Body.String())
			}
			if tenant != "" && !strings.Contains(rr.Body.String(), "device-"+strings.TrimPrefix(tenant, "tenant-")) {
				t.Fatal("fresh tenant missing")
			}
			if tenant == "tenant-b" && strings.Contains(rr.Body.String(), "device-a") {
				t.Fatal("stale session scope")
			}
		}
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer tenant-inventory-token-a")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Fatalf("machine token accepted: %d", rr.Code)
		}
	}
}
