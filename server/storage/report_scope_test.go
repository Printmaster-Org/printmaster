package storage

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestSQLiteReportScopedQueries(t *testing.T) {
	t.Parallel()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "report-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checkReportScopedQueries(t, store)
}

func checkReportScopedQueries(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	scoped, ok := store.(ReportScopedStore)
	if !ok {
		t.Fatal("store does not implement ReportScopedStore")
	}

	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(ctx, &Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, site := range []*Site{
		{ID: "site-a", TenantID: "tenant-a", Name: "A"},
		{ID: "site-a2", TenantID: "tenant-a", Name: "A2"},
		{ID: "site-b", TenantID: "tenant-b", Name: "B"},
	} {
		if err := store.CreateSite(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	agentIDs := []string{"agent-a1", "agent-a2", "agent-a-unassigned", "agent-a-cross-site", "agent-b1"}
	tenantForAgent := map[string]string{
		"agent-a1": "tenant-a", "agent-a2": "tenant-a", "agent-a-unassigned": "tenant-a",
		"agent-a-cross-site": "tenant-a", "agent-b1": "tenant-b",
	}
	for _, id := range agentIDs {
		if err := store.RegisterAgent(ctx, &Agent{AgentID: id, Token: "token-" + id, TenantID: tenantForAgent[id]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, assignment := range [][2]string{
		{"agent-a1", "site-a"}, {"agent-a2", "site-a2"},
		{"agent-a1", "site-b"}, {"agent-a-cross-site", "site-b"},
		{"agent-b1", "site-b"},
	} {
		if err := store.AssignAgentToSite(ctx, assignment[0], assignment[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range agentIDs {
		device := &Device{AgentID: id}
		device.Serial = "serial-" + id
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}
	creator, ok := store.(interface {
		CreateAlert(context.Context, *Alert) (int64, error)
	})
	if !ok {
		t.Fatal("store does not support alert fixtures")
	}
	now := time.Now().UTC()
	for _, alert := range []*Alert{
		{Type: "device", Severity: "warning", Scope: "device", Status: "active", TenantID: "tenant-a", SiteID: "site-a", AgentID: "agent-a1", Title: "A site", TriggeredAt: now},
		{Type: "device", Severity: "warning", Scope: "device", Status: "active", TenantID: "tenant-a", SiteID: "site-b", AgentID: "agent-a1", Title: "Mismatched site", TriggeredAt: now.Add(time.Second)},
		{Type: "device", Severity: "warning", Scope: "device", Status: "active", TenantID: "tenant-b", SiteID: "site-b", AgentID: "agent-b1", Title: "B site", TriggeredAt: now.Add(2 * time.Second)},
	} {
		if _, err := creator.CreateAlert(ctx, alert); err != nil {
			t.Fatal(err)
		}
	}

	siteScope := ReportDataScope{TenantIDs: []string{"tenant-a"}, SiteIDs: []string{"site-a"}}
	agents, err := scoped.ListAgentsForReport(ctx, siteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, agentIDsOf(agents), []string{"agent-a1"})
	agent, err := scoped.GetAgentForReport(ctx, "agent-a1", siteScope)
	if err != nil || agent == nil || agent.AgentID != "agent-a1" {
		t.Fatalf("scoped agent read: %+v, %v", agent, err)
	}
	foreign, err := scoped.GetAgentForReport(ctx, "agent-b1", siteScope)
	if err != nil || foreign != nil {
		t.Fatalf("foreign agent read: %+v, %v", foreign, err)
	}
	devices, err := scoped.ListDevicesForReport(ctx, siteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, deviceSerialsOf(devices), []string{"serial-agent-a1"})
	alerts, err := scoped.ListAlertsForReport(ctx, AlertFilter{}, siteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, alertTitlesOf(alerts), []string{"A site"})
	tenants, err := scoped.ListTenantsForReport(ctx, siteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, tenantIDsOf(tenants), []string{"tenant-a"})
	sites, err := scoped.ListSitesForReport(ctx, siteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, siteIDsOf(sites), []string{"site-a"})

	crossTenantSiteScope := ReportDataScope{TenantIDs: []string{"tenant-a"}, SiteIDs: []string{"site-b"}}
	agents, err = scoped.ListAgentsForReport(ctx, crossTenantSiteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, agentIDsOf(agents), nil)
	devices, err = scoped.ListDevicesForReport(ctx, crossTenantSiteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, deviceSerialsOf(devices), nil)
	alerts, err = scoped.ListAlertsForReport(ctx, AlertFilter{}, crossTenantSiteScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, alertTitlesOf(alerts), nil)

	emptyScope := ReportDataScope{}
	agents, err = scoped.ListAgentsForReport(ctx, emptyScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, agentIDsOf(agents), nil)
	devices, err = scoped.ListDevicesForReport(ctx, emptyScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, deviceSerialsOf(devices), nil)
	tenants, err = scoped.ListTenantsForReport(ctx, emptyScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, tenantIDsOf(tenants), nil)
	sites, err = scoped.ListSitesForReport(ctx, emptyScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, siteIDsOf(sites), nil)
	alerts, err = scoped.ListAlertsForReport(ctx, AlertFilter{}, emptyScope)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIDs(t, alertTitlesOf(alerts), nil)
}

func assertReportIDs(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func agentIDsOf(agents []*Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.AgentID)
	}
	return ids
}

func deviceSerialsOf(devices []*Device) []string {
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.Serial)
	}
	return ids
}

func tenantIDsOf(tenants []*Tenant) []string {
	ids := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		ids = append(ids, tenant.ID)
	}
	return ids
}

func siteIDsOf(sites []*Site) []string {
	ids := make([]string, 0, len(sites))
	for _, site := range sites {
		ids = append(ids, site.ID)
	}
	return ids
}

func alertTitlesOf(alerts []*Alert) []string {
	ids := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		ids = append(ids, alert.Title)
	}
	return ids
}
