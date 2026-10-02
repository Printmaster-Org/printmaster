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
