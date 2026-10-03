package reports

import (
	"context"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestTenantScheduledReportSecurity(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: tenant, Name: tenant}); err != nil {
			t.Fatal(err)
		}
		if err := store.RegisterAgent(ctx, &storage.Agent{AgentID: "agent-" + tenant, Token: "scheduled-report-token-" + tenant, TenantID: tenant, Name: tenant, RegisteredAt: time.Now(), LastSeen: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	report := &storage.ReportDefinition{Name: "A agents", Type: storage.ReportTypeAgentInventory, Scope: storage.ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Format: storage.ReportFormatJSON, CreatedBy: "operator-a"}
	if err := store.CreateReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	schedule := &storage.ReportSchedule{ReportID: report.ID, Name: "A daily", Enabled: true, Frequency: storage.ReportFrequencyDaily, TimeOfDay: "08:00", Timezone: "UTC", NextRunAt: time.Now()}
	if err := store.CreateReportSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(store, testLogger())
	if err := scheduler.runSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.RunNow(ctx, report.ID, "operator-a"); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListReportRuns(ctx, storage.ReportRunFilter{ReportID: report.ID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%v error=%v", runs, err)
	}
	for _, run := range runs {
		ids, verified := storage.ReportRunTenantScope(run)
		if !verified || len(ids) != 1 || ids[0] != "tenant-a" || run.Status != storage.ReportStatusCompleted {
			t.Fatalf("missing execution ownership: %+v", run)
		}
		if !strings.Contains(run.ResultData, "tenant-a") || strings.Contains(run.ResultData, "tenant-b") {
			t.Fatalf("job scope leaked/dropped tenant: %s", run.ResultData)
		}
	}
}

func TestTenantSiteReportsUseScopedQueries(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: tenant, Name: tenant}); err != nil {
			t.Fatal(err)
		}
	}
	for _, site := range []*storage.Site{
		{ID: "site-a", TenantID: "tenant-a", Name: "A"},
		{ID: "site-b", TenantID: "tenant-b", Name: "B"},
	} {
		if err := store.CreateSite(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct{ agentID, tenantID, siteID string }{
		{"agent-a", "tenant-a", "site-a"},
		{"agent-a-cross-site", "tenant-a", "site-b"},
		{"agent-b", "tenant-b", "site-b"},
	} {
		if err := store.RegisterAgent(ctx, &storage.Agent{AgentID: fixture.agentID, Token: "token-" + fixture.agentID, TenantID: fixture.tenantID}); err != nil {
			t.Fatal(err)
		}
		if err := store.AssignAgentToSite(ctx, fixture.agentID, fixture.siteID); err != nil {
			t.Fatal(err)
		}
		device := &storage.Device{AgentID: fixture.agentID}
		device.Serial = "serial-" + fixture.agentID
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
	}

	for _, report := range []*storage.ReportDefinition{
		{Type: storage.ReportTypeAgentInventory, Scope: storage.ReportScopeSite, TenantIDs: []string{"tenant-a"}, SiteIDs: []string{"site-a"}},
		{Type: storage.ReportTypeSiteInventory, Scope: storage.ReportScopeSite, TenantIDs: []string{"tenant-a"}, SiteIDs: []string{"site-a"}},
	} {
		result, err := NewGenerator(store).Generate(ctx, GenerateParams{Report: report})
		if err != nil {
			t.Fatal(err)
		}
		if result.RowCount != 1 || len(result.Rows) != 1 {
			t.Fatalf("report %q returned unexpected rows: %+v", report.Type, result.Rows)
		}
		if report.Type == storage.ReportTypeAgentInventory && result.Rows[0]["agent_id"] != "agent-a" {
			t.Fatalf("agent report escaped selected site: %+v", result.Rows)
		}
		if report.Type == storage.ReportTypeSiteInventory && result.Rows[0]["site_id"] != "site-a" {
			t.Fatalf("site report escaped selected tenant/site: %+v", result.Rows)
		}
	}
}

func TestTenantReportRequiresScopedStore(t *testing.T) {
	t.Parallel()
	_, err := NewGenerator(newMockGeneratorStore()).Generate(context.Background(), GenerateParams{
		Report: &storage.ReportDefinition{
			Type: storage.ReportTypeAgentInventory, Scope: storage.ReportScopeTenant, TenantIDs: []string{"tenant-a"},
		},
	})
	if err == nil {
		t.Fatal("tenant report accepted a store without DB-scoped report queries")
	}
}
