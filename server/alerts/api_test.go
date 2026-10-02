package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	commonstorage "printmaster/common/storage"
	"printmaster/server/authz"
	"printmaster/server/storage"
)

type apiPrincipalKey struct{}

type apiPrincipal struct {
	subject authz.Subject
	scope   TenantScope
}

func tenantPrincipal(role storage.Role, tenants ...string) apiPrincipal {
	return apiPrincipal{
		subject: authz.Subject{Role: role, AllowedTenantIDs: tenants},
		scope:   TenantScope{TenantIDs: tenants},
	}
}

func adminPrincipal() apiPrincipal {
	return apiPrincipal{subject: authz.Subject{Role: storage.RoleAdmin, IsAdmin: true}, scope: TenantScope{AllTenants: true}}
}

type apiRoundTripper func(*http.Request) (*http.Response, error)

func (fn apiRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type apiFixture struct {
	store    *storage.SQLiteStore
	api      *API
	mux      *http.ServeMux
	ids      map[string]map[string]int64
	sends    atomic.Int64
	audits   atomic.Int64
	policyID int64
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &apiFixture{store: store, ids: make(map[string]map[string]int64)}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Keep fixture counts independent of globally seeded default rules.
	seeded, err := store.ListAlertRules(ctx)
	must(err)
	for _, rule := range seeded {
		must(store.DeleteAlertRule(ctx, rule.ID))
	}
	for _, tenant := range []string{"a", "b"} {
		must(store.CreateTenant(ctx, &storage.Tenant{ID: tenant, Name: tenant}))
		must(store.RegisterAgent(ctx, &storage.Agent{AgentID: "agent-" + tenant, Token: "alerts-api-token-" + tenant, TenantID: tenant}))
		must(store.CreateSite(ctx, &storage.Site{ID: "site-" + tenant, TenantID: tenant, Name: tenant}))
		must(store.UpsertDevice(ctx, &storage.Device{
			Device: commonstorage.Device{Serial: "device-" + tenant}, AgentID: "agent-" + tenant,
		}))
	}
	for _, owner := range []string{"a", "b", "global", "shared"} {
		tenants := []string{owner}
		tenant := owner
		if owner == "global" {
			tenants, tenant = nil, ""
		}
		if owner == "shared" {
			tenants = []string{"a", "b"}
			tenant = "b"
		}
		f.ids[owner] = make(map[string]int64)
		id, err := store.CreateNotificationChannel(ctx, &storage.NotificationChannel{
			Name: owner, Type: storage.ChannelTypeWebhook, Enabled: true, TenantIDs: tenants,
			ConfigJSON: `{"url":"https://8.8.8.8/notify","secret":"` + owner + `"}`,
		})
		must(err)
		f.ids[owner]["channel"] = id
		id, err = store.CreateAlertRule(ctx, &storage.AlertRule{
			Name: owner, Type: storage.AlertTypeCustom, Severity: storage.AlertSeverityWarning,
			Scope: storage.AlertScopeTenant, Enabled: true, TenantIDs: tenants,
		})
		must(err)
		f.ids[owner]["rule"] = id
		id, err = store.CreateAlert(ctx, &storage.Alert{
			Title: owner, Type: storage.AlertTypeCustom, Severity: storage.AlertSeverityWarning,
			Scope: storage.AlertScopeTenant, Status: storage.AlertStatusActive, TenantID: tenant,
			TriggeredAt: time.Now().UTC(),
		})
		must(err)
		f.ids[owner]["alert"] = id
		id, err = store.CreateAlertMaintenanceWindow(ctx, &storage.AlertMaintenanceWindow{
			Name: owner, Scope: storage.AlertScopeTenant, TenantID: tenant,
			StartTime: time.Now().UTC().Add(-time.Hour), EndTime: time.Now().UTC().Add(time.Hour),
		})
		must(err)
		f.ids[owner]["window"] = id
	}
	f.policyID, err = store.CreateEscalationPolicy(ctx, &storage.EscalationPolicy{
		Name: "global", Enabled: true, Steps: []storage.EscalationStep{{ChannelIDs: []int64{f.ids["global"]["channel"]}}},
	})
	must(err)
	notifier := NewNotifier(store, NotifierConfig{HTTPClient: &http.Client{
		Transport: apiRoundTripper(func(r *http.Request) (*http.Response, error) {
			f.sends.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
		}),
	}})
	f.api, err = NewAPI(store, APIOptions{
		Authorizer: func(r *http.Request, action authz.Action, resource authz.ResourceRef) error {
			p, ok := r.Context().Value(apiPrincipalKey{}).(apiPrincipal)
			if !ok {
				return authz.ErrUnauthorized
			}
			return authz.Authorize(p.subject, action, resource)
		},
		ScopeResolver: func(r *http.Request) (TenantScope, error) {
			p, ok := r.Context().Value(apiPrincipalKey{}).(apiPrincipal)
			if !ok {
				return TenantScope{}, authz.ErrUnauthorized
			}
			return p.scope, nil
		},
		ActorResolver: func(*http.Request) string { return "actor-from-principal" },
		AuditLogger:   func(*http.Request, *storage.AuditEntry) { f.audits.Add(1) },
		Notifier:      notifier,
	})
	must(err)
	f.mux = http.NewServeMux()
	f.api.RegisterRoutes(RouteConfig{Mux: f.mux, FeatureEnabled: true})
	return f
}

func (f *apiFixture) request(t *testing.T, p apiPrincipal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r = r.WithContext(context.WithValue(r.Context(), apiPrincipalKey{}, p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, want, w.Body.String())
	}
}

func decodeResponse[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAPITenantCollections(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	paths := map[string]string{
		"alerts": "/api/v1/alerts", "rules": "/api/v1/alert-rules",
		"channels": "/api/v1/notification-channels", "windows": "/api/v1/maintenance-windows",
		"active windows": "/api/v1/maintenance-windows?active=true",
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				p    apiPrincipal
				want int
			}{
				{"viewer a", tenantPrincipal(storage.RoleViewer, "a"), 1},
				{"operator a", tenantPrincipal(storage.RoleOperator, "a"), 1},
				{"empty restricted", tenantPrincipal(storage.RoleOperator), 0},
				{"restricted admin", tenantPrincipal(storage.RoleAdmin, "a"), 1},
				{"two tenants", tenantPrincipal(storage.RoleOperator, "a", "b"), 3},
				{"explicit admin", adminPrincipal(), 4},
			} {
				w := f.request(t, tc.p, http.MethodGet, path, nil)
				expectStatus(t, w, http.StatusOK)
				res := decodeResponse[map[string]json.RawMessage](t, w)
				var count int
				_ = json.Unmarshal(res["count"], &count)
				if count != tc.want {
					t.Fatalf("%s: count = %d, want %d: %s", tc.name, count, tc.want, w.Body.String())
				}
			}
			separator := "?"
			if strings.Contains(path, "?") {
				separator = "&"
			}
			expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodGet, path+separator+"tenant_id=b", nil), http.StatusForbidden)
			expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator), http.MethodGet, path+separator+"tenant_id=a", nil), http.StatusForbidden)
			w := f.request(t, adminPrincipal(), http.MethodGet, path+separator+"tenant_id=a", nil)
			expectStatus(t, w, http.StatusOK)
			res := decodeResponse[map[string]json.RawMessage](t, w)
			want := "1"
			if name == "rules" || name == "channels" {
				want = "2" // admin also owns shared a/b configurations
			}
			if string(res["count"]) != want {
				t.Fatalf("admin tenant filter did not narrow: %s", w.Body.String())
			}
		})
	}
	// A broader caller set may be narrowed to a single allowed tenant.
	w := f.request(t, tenantPrincipal(storage.RoleOperator, "a", "b"), http.MethodGet, "/api/v1/alerts?tenant_id=a", nil)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"total_count":1`) {
		t.Fatal(w.Body.String())
	}
}

func TestAPITenantPaginationAndSummary(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	ctx := context.Background()
	for _, status := range []string{storage.AlertStatusActive, storage.AlertStatusAcknowledged, storage.AlertStatusSuppressed, storage.AlertStatusResolved} {
		alert := &storage.Alert{Title: status, TenantID: "a", Type: "scoped", Scope: storage.AlertScopeDevice,
			Severity: storage.AlertSeverityCritical, Status: status, TriggeredAt: time.Now().UTC()}
		if status == storage.AlertStatusResolved {
			alert.Status = storage.AlertStatusActive
		}
		id, err := f.store.CreateAlert(ctx, alert)
		if err != nil {
			t.Fatal(err)
		}
		if status == storage.AlertStatusResolved {
			if err := f.store.ResolveAlert(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, "/api/v1/alerts?limit=1&offset=1", nil)
	expectStatus(t, w, http.StatusOK)
	page := decodeResponse[struct {
		Alerts     []storage.Alert `json:"alerts"`
		TotalCount int             `json:"total_count"`
		HasMore    bool            `json:"has_more"`
	}](t, w)
	if page.TotalCount != 3 || len(page.Alerts) != 1 || !page.HasMore || page.Alerts[0].TenantID != "a" {
		t.Fatalf("scoped pagination: %s", w.Body.String())
	}
	w = f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, "/api/v1/alerts?limit=1&offset=100", nil)
	expectStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), `"has_more":true`) || !strings.Contains(w.Body.String(), `"alerts":[]`) {
		t.Fatal(w.Body.String())
	}
	w = f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, "/api/v1/alerts?offset=100", nil)
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"count":3`) {
		t.Fatalf("offset without limit changed legacy behavior: %s", w.Body.String())
	}
	w = f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, "/api/v1/alerts/summary", nil)
	expectStatus(t, w, http.StatusOK)
	summary := decodeResponse[storage.AlertSummary](t, w)
	if summary.ActiveCount != 2 || summary.AcknowledgedCount != 1 || summary.SuppressedCount != 1 || summary.ResolvedTodayCount != 1 ||
		summary.CriticalCount != 1 || summary.WarningCount != 1 || summary.ActiveRules != 1 || summary.ActiveChannels != 1 || !summary.HasMaintenance {
		t.Fatalf("scoped summary: %+v", summary)
	}
	w = f.request(t, tenantPrincipal(storage.RoleOperator), http.MethodGet, "/api/v1/alerts/summary", nil)
	expectStatus(t, w, http.StatusOK)
	summary = decodeResponse[storage.AlertSummary](t, w)
	if summary.ActiveCount != 0 || summary.ActiveRules != 0 || summary.ActiveChannels != 0 || summary.HasMaintenance {
		t.Fatalf("empty scope leaked summary: %+v", summary)
	}
	expectStatus(t, f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, "/api/v1/alerts/summary?tenant_id=b", nil), http.StatusForbidden)
	w = f.request(t, adminPrincipal(), http.MethodGet, "/api/v1/alerts/summary?tenant_id=a", nil)
	expectStatus(t, w, http.StatusOK)
	if decodeResponse[storage.AlertSummary](t, w).ActiveCount != 2 {
		t.Fatal("admin summary filter not honored")
	}
	w = f.request(t, adminPrincipal(), http.MethodGet, "/api/v1/alerts/summary", nil)
	expectStatus(t, w, http.StatusOK)
	if decodeResponse[storage.AlertSummary](t, w).ActiveCount != 5 {
		t.Fatal("explicit global admin lost global summary")
	}
}

func TestAPIForeignRecordOperations(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	collections := map[string]string{"alert": "alerts", "rule": "alert-rules", "channel": "notification-channels", "window": "maintenance-windows"}
	for kind, collection := range collections {
		for _, owner := range []string{"b", "global", "shared"} {
			path := fmt.Sprintf("/api/v1/%s/%d", collection, f.ids[owner][kind])
			methods := []string{http.MethodGet, http.MethodDelete}
			if kind != "alert" {
				methods = append(methods, http.MethodPut)
			}
			for _, method := range methods {
				t.Run(method+path, func(t *testing.T) {
					expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), method, path, map[string]any{"tenant_id": "a"}), http.StatusNotFound)
				})
			}
			if kind == "alert" {
				for _, action := range []string{"acknowledge", "resolve"} {
					expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodPost, path+"/"+action, nil), http.StatusNotFound)
				}
			}
			if kind == "channel" {
				expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodPost, path+"/test", nil), http.StatusNotFound)
			}
		}
	}
	if f.audits.Load() != 0 || f.sends.Load() != 0 {
		t.Fatal("denied operations caused side effects")
	}
	for _, owner := range []string{"b", "global", "shared"} {
		alert, err := f.store.GetAlert(context.Background(), f.ids[owner]["alert"])
		if err != nil || alert == nil || alert.Status != storage.AlertStatusActive || alert.AcknowledgedAt != nil {
			t.Fatalf("foreign alert changed: %+v, %v", alert, err)
		}
	}
	for kind, collection := range collections {
		path := fmt.Sprintf("/api/v1/%s/%d", collection, f.ids["a"][kind])
		expectStatus(t, f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodGet, path, nil), http.StatusOK)
		expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator), http.MethodGet, path, nil), http.StatusNotFound)
		expectStatus(t, f.request(t, adminPrincipal(), http.MethodGet, fmt.Sprintf("/api/v1/%s/%d", collection, f.ids["b"][kind]), nil), http.StatusOK)
	}
}

func TestAPICreateAndUpdateOwnership(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	p := tenantPrincipal(storage.RoleOperator, "a")
	for _, tenant := range []string{"b", ""} {
		bodies := map[string]any{
			"alerts":                storage.Alert{Type: "custom", TenantID: tenant},
			"alert-rules":           storage.AlertRule{Name: "new", Type: "custom", TenantIDs: []string{tenant}},
			"notification-channels": storage.NotificationChannel{Name: "new", Type: "webhook", TenantIDs: []string{tenant}},
			"maintenance-windows": storage.AlertMaintenanceWindow{Name: "new", Scope: "tenant", TenantID: tenant,
				StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
		}
		for collection, body := range bodies {
			expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/"+collection, body), http.StatusForbidden)
			if collection != "alerts" {
				kind := map[string]string{"alert-rules": "rule", "notification-channels": "channel", "maintenance-windows": "window"}[collection]
				expectStatus(t, f.request(t, p, http.MethodPut, fmt.Sprintf("/api/v1/%s/%d", collection, f.ids["a"][kind]), body), http.StatusForbidden)
			}
		}
	}
	for _, ids := range [][]string{nil, {"a", "b"}, {"a", ""}} {
		expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/alert-rules", storage.AlertRule{Name: "new", Type: "custom", TenantIDs: ids}), http.StatusForbidden)
		expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/notification-channels", storage.NotificationChannel{Name: "new", Type: "webhook", TenantIDs: ids}), http.StatusForbidden)
	}
	parentID := f.ids["b"]["alert"]
	for _, body := range []storage.Alert{
		{Type: "custom", TenantID: "a", AgentID: "agent-b"},
		{Type: "custom", TenantID: "a", SiteID: "site-b"},
		{Type: "custom", TenantID: "a", DeviceSerial: "device-b"},
		{Type: "custom", TenantID: "a", RuleID: f.ids["b"]["rule"]},
		{Type: "custom", TenantID: "a", ParentAlertID: &parentID},
		{Type: "custom", TenantID: "a", Scope: "fleet"},
	} {
		w := f.request(t, p, http.MethodPost, "/api/v1/alerts", body)
		if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Fatalf("foreign reference accepted: %s", w.Body.String())
		}
	}
	for _, rule := range []storage.AlertRule{
		{Name: "new", Type: "custom", TenantIDs: []string{"a"}, ChannelIDs: []int64{f.ids["b"]["channel"]}},
		{Name: "new", Type: "custom", TenantIDs: []string{"a"}, EscalationPolicyID: &f.policyID},
		{Name: "new", Type: "custom", TenantIDs: []string{"a"}, AgentIDs: []string{"agent-b"}},
		{Name: "new", Type: "custom", TenantIDs: []string{"a"}, SiteIDs: []string{"site-b"}},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			path := "/api/v1/alert-rules"
			if method == http.MethodPut {
				path += fmt.Sprintf("/%d", f.ids["a"]["rule"])
			}
			w := f.request(t, p, method, path, rule)
			if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
				t.Fatalf("foreign rule reference accepted: %s", w.Body.String())
			}
		}
	}
	for _, scope := range []string{"", "fleet"} {
		expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/maintenance-windows", storage.AlertMaintenanceWindow{
			Name: "spoofed fleet", Scope: scope, TenantID: "a", StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour),
		}), http.StatusForbidden)
	}
	if f.audits.Load() != 0 {
		t.Fatal("rejected writes generated success audits")
	}
	window := storage.AlertMaintenanceWindow{Name: "foreign agent", Scope: "agent", TenantID: "a", AgentID: "agent-b",
		StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)}
	expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/maintenance-windows", window), http.StatusForbidden)
	expectStatus(t, f.request(t, p, http.MethodPut, fmt.Sprintf("/api/v1/maintenance-windows/%d", f.ids["a"]["window"]), window), http.StatusForbidden)
	// Valid tenant creates/updates still work; actor is not supplied by the client.
	w := f.request(t, p, http.MethodPost, "/api/v1/alerts", storage.Alert{Type: "custom", TenantID: "a", AgentID: "agent-a", DeviceSerial: "device-a"})
	expectStatus(t, w, http.StatusCreated)
	id := decodeResponse[storage.Alert](t, w).ID
	expectStatus(t, f.request(t, p, http.MethodPost, fmt.Sprintf("/api/v1/alerts/%d/acknowledge", id), map[string]string{"acknowledged_by": "spoof"}), http.StatusOK)
	alert, err := f.store.GetAlert(context.Background(), id)
	if err != nil || alert.AcknowledgedBy != "actor-from-principal" {
		t.Fatalf("incorrect acknowledgement actor: %+v %v", alert, err)
	}
	expectStatus(t, f.request(t, p, http.MethodPost, fmt.Sprintf("/api/v1/alerts/%d/resolve", id), nil), http.StatusOK)
	expectStatus(t, f.request(t, p, http.MethodPut, fmt.Sprintf("/api/v1/notification-channels/%d", f.ids["a"]["channel"]), storage.NotificationChannel{
		Name: "updated", Type: "webhook", TenantIDs: []string{"a"},
	}), http.StatusOK)
	for collection, body := range map[string]any{
		"alert-rules":           storage.AlertRule{Name: "owned rule", Type: "custom", TenantIDs: []string{"a"}, ChannelIDs: []int64{f.ids["a"]["channel"]}},
		"notification-channels": storage.NotificationChannel{Name: "owned channel", Type: "webhook", TenantIDs: []string{"a"}},
		"maintenance-windows": storage.AlertMaintenanceWindow{Name: "owned window", Scope: "tenant", TenantID: "a",
			StartTime: time.Now().UTC(), EndTime: time.Now().UTC().Add(time.Hour)},
	} {
		expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator), http.MethodPost, "/api/v1/"+collection, body), http.StatusForbidden)
		expectStatus(t, f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodPost, "/api/v1/"+collection, body), http.StatusForbidden)
		w := f.request(t, p, http.MethodPost, "/api/v1/"+collection, body)
		expectStatus(t, w, http.StatusCreated)
		created := decodeResponse[struct {
			ID int64 `json:"id"`
		}](t, w)
		expectStatus(t, f.request(t, p, http.MethodPut, fmt.Sprintf("/api/v1/%s/%d", collection, created.ID), body), http.StatusOK)
		expectStatus(t, f.request(t, p, http.MethodDelete, fmt.Sprintf("/api/v1/%s/%d", collection, created.ID), nil), http.StatusNoContent)
	}
}

func TestAPIFleetConfigurationsCannotSpoofOwnership(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	p := tenantPrincipal(storage.RoleOperator, "a")
	ctx := context.Background()
	id, err := f.store.CreateAlertRule(ctx, &storage.AlertRule{
		Name: "mislabeled fleet rule", Type: "custom", Scope: "fleet", TenantIDs: []string{"a"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		expectStatus(t, f.request(t, p, method, fmt.Sprintf("/api/v1/alert-rules/%d", id), map[string]any{}), http.StatusNotFound)
	}
	for _, scope := range []string{"", "fleet"} {
		id, err = f.store.CreateAlertMaintenanceWindow(ctx, &storage.AlertMaintenanceWindow{
			Name: "mislabeled fleet window", Scope: scope, TenantID: "a",
			StartTime: time.Now().UTC().Add(-time.Hour), EndTime: time.Now().UTC().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			expectStatus(t, f.request(t, p, method, fmt.Sprintf("/api/v1/maintenance-windows/%d", id), map[string]any{}), http.StatusNotFound)
		}
	}
	for _, path := range []string{"/api/v1/alert-rules", "/api/v1/maintenance-windows", "/api/v1/maintenance-windows?active=true"} {
		w := f.request(t, p, http.MethodGet, path, nil)
		expectStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), `"count":1`) {
			t.Fatalf("fleet configuration leaked: %s", w.Body.String())
		}
	}
}

func TestAPIGlobalRecordsAndNotificationTests(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	p := tenantPrincipal(storage.RoleOperator, "a")
	for _, path := range []string{"/api/v1/alert-settings", "/api/v1/escalation-policies", fmt.Sprintf("/api/v1/escalation-policies/%d", f.policyID)} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPost} {
			w := f.request(t, p, method, path, map[string]any{})
			if w.Code != http.StatusMethodNotAllowed {
				expectStatus(t, w, http.StatusForbidden)
			}
		}
		expectStatus(t, f.request(t, adminPrincipal(), http.MethodGet, path, nil), http.StatusOK)
	}
	testBody := map[string]string{"type": "webhook", "name": "test", "config_json": `{"url":"https://8.8.8.8/test"}`}
	expectStatus(t, f.request(t, p, http.MethodPost, "/api/v1/notification-channels/test", testBody), http.StatusForbidden)
	expectStatus(t, f.request(t, adminPrincipal(), http.MethodPost, "/api/v1/notification-channels/test", testBody), http.StatusOK)
	expectStatus(t, f.request(t, p, http.MethodPost, fmt.Sprintf("/api/v1/notification-channels/%d/test", f.ids["a"]["channel"]), nil), http.StatusOK)
	expectStatus(t, f.request(t, tenantPrincipal(storage.RoleViewer, "a"), http.MethodPost, fmt.Sprintf("/api/v1/notification-channels/%d/test", f.ids["a"]["channel"]), nil), http.StatusForbidden)
	if f.sends.Load() != 2 {
		t.Fatalf("sends = %d, want 2", f.sends.Load())
	}
}

func TestAPIScopeFailsClosed(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	paths := []string{"/api/v1/alerts", "/api/v1/alerts/summary", "/api/v1/alert-rules", "/api/v1/notification-channels", "/api/v1/escalation-policies", "/api/v1/maintenance-windows", "/api/v1/alert-settings"}
	for _, path := range paths {
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		expectStatus(t, w, http.StatusUnauthorized)
	}
	f.api.scopeResolver = nil
	for _, path := range paths {
		expectStatus(t, f.request(t, adminPrincipal(), http.MethodGet, path, nil), http.StatusInternalServerError)
	}
	f.api.scopeResolver = func(*http.Request) (TenantScope, error) { return TenantScope{}, errors.New("scope lookup failed") }
	expectStatus(t, f.request(t, adminPrincipal(), http.MethodGet, "/api/v1/alerts", nil), http.StatusForbidden)
	f.api.scopeResolver = func(*http.Request) (TenantScope, error) { return TenantScope{}, authz.ErrUnauthorized }
	expectStatus(t, f.request(t, adminPrincipal(), http.MethodGet, "/api/v1/alerts", nil), http.StatusUnauthorized)
	f.api.scopeResolver = func(*http.Request) (TenantScope, error) { return TenantScope{AllTenants: true}, nil }
	expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodGet, "/api/v1/alerts", nil), http.StatusForbidden)
	f.api.scopeResolver = func(*http.Request) (TenantScope, error) { return TenantScope{TenantIDs: []string{"b"}}, nil }
	expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodGet, "/api/v1/alerts", nil), http.StatusForbidden)
}
