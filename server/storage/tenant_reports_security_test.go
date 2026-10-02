package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReportsExactTenantMembership(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkReportsExactTenantMembership(t, s)
}

func TestReportRunMetadataReads(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkReportRunMetadataReads(t, s)
}

func checkReportRunMetadataReads(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	report := &ReportDefinition{Name: "metadata", Scope: ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Type: ReportTypeAgentInventory, Format: ReportFormatJSON}
	if err := s.CreateReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	var ids []int64
	body := strings.Repeat("result-body", 16384)
	for i := 0; i < 3; i++ {
		run := &ReportRun{ReportID: report.ID, StartedAt: started, Status: ReportStatusCompleted, Format: ReportFormatJSON, ResultData: body, ResultSize: int64(len(body)), RowCount: 7, DurationMS: 12, ResultPath: "metadata.json", RunBy: "operator"}
		if err := s.CreateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	for offset := 0; offset < len(ids); offset++ {
		runs, err := s.ListReportRuns(ctx, ReportRunFilter{ReportID: report.ID, Status: ReportStatusCompleted, Since: &started, Limit: 1, Offset: offset, MetadataOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].ID != ids[len(ids)-1-offset] {
			t.Fatalf("unstable metadata page %d: %+v", offset, runs)
		}
		run := runs[0]
		if run.ResultData != "" || run.ResultSize != int64(len(body)) || run.RowCount != 7 || run.DurationMS != 12 || run.ReportName != report.Name || run.ResultPath != "metadata.json" || run.RunBy != "operator" {
			t.Fatalf("metadata projection lost fields or included body: %+v", run)
		}
		metadata, err := s.GetReportRunMetadata(ctx, run.ID)
		if err != nil || metadata == nil || metadata.ResultData != "" || metadata.ParametersJSON != run.ParametersJSON {
			t.Fatalf("metadata get: %+v, %v", metadata, err)
		}
		if tenants, ok := ReportRunTenantScope(metadata); !ok || len(tenants) != 1 || tenants[0] != "tenant-a" {
			t.Fatal("metadata read lost verified execution scope")
		}
		full, err := s.GetReportRun(ctx, run.ID)
		if err != nil || full == nil || full.ResultData != body {
			t.Fatalf("full get compatibility lost: %v", err)
		}
	}
	runs, err := s.ListReportRuns(ctx, ReportRunFilter{ReportID: report.ID})
	if err != nil || len(runs) != len(ids) || runs[0].ResultData != body {
		t.Fatalf("default storage list compatibility lost: %v", err)
	}
	missing, err := s.GetReportRunMetadata(ctx, ids[len(ids)-1]+1)
	if err != nil || missing != nil {
		t.Fatalf("missing metadata: %+v, %v", missing, err)
	}
}

func checkReportsExactTenantMembership(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	for _, fixture := range []struct {
		name string
		ids  []string
	}{
		{"0-global", nil}, {"1-prefix", []string{"tenant-ab"}},
		{"2-exact", []string{"tenant-a"}}, {"3-multi", []string{"tenant-b", "tenant-a"}},
		{"4-case", []string{"Tenant-A"}}, {"5-wildcards", []string{"tenant_%"}},
		{"6-not-wildcards", []string{"tenant-xyz"}},
	} {
		report := &ReportDefinition{Name: fixture.name, TenantIDs: fixture.ids, Scope: ReportScopeTenant, Type: ReportTypeAgentInventory, Format: ReportFormatJSON}
		report.IsBuiltIn = fixture.name == "0-global"
		if err := s.CreateReport(ctx, report); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		filter ReportFilter
		names  []string
	}{
		{ReportFilter{TenantID: "tenant-a"}, []string{"2-exact", "3-multi"}},
		{ReportFilter{TenantID: "tenant-a", Limit: 1, Offset: 1}, []string{"3-multi"}},
		{ReportFilter{TenantID: "Tenant-A"}, []string{"4-case"}},
		{ReportFilter{TenantID: "tenant_%"}, []string{"5-wildcards"}},
		{ReportFilter{TenantID: "%"}, nil},
		{ReportFilter{TenantID: "tenant-"}, nil},
	} {
		t.Run(tc.filter.TenantID+string(rune(tc.filter.Offset+'0')), func(t *testing.T) {
			reports, err := s.ListReports(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(reports) != len(tc.names) {
				t.Fatalf("got %d reports, want %v", len(reports), tc.names)
			}
			for i, name := range tc.names {
				if reports[i].Name != name {
					t.Fatalf("got %q, want %q", reports[i].Name, name)
				}
			}
		})
	}
	for _, builtIn := range []bool{true, false} {
		reports, err := s.ListReports(ctx, ReportFilter{BuiltIn: &builtIn})
		if err != nil {
			t.Fatal(err)
		}
		want := 6
		if builtIn {
			want = 1
		}
		if len(reports) != want {
			t.Fatalf("built-in=%v got %d want %d", builtIn, len(reports), want)
		}
	}
}

func TestReportRunSecurityOwnership(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkReportRunSecurityOwnership(t, s)
}

func checkReportRunSecurityOwnership(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	report := &ReportDefinition{Name: "A", Scope: ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Type: ReportTypeAgentInventory, Format: ReportFormatJSON}
	if err := s.CreateReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	run := &ReportRun{ReportID: report.ID, StartedAt: time.Now(), Status: ReportStatusRunning, Format: ReportFormatJSON, ParametersJSON: `{"existing":true}`}
	if err := s.CreateReportRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetReportRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids, verified := ReportRunTenantScope(stored)
	if !verified || len(ids) != 1 || ids[0] != "tenant-a" {
		t.Fatalf("missing immutable execution scope: %+v", stored)
	}
	report.TenantIDs = []string{"tenant-b"}
	if err := s.UpdateReport(ctx, report); err == nil {
		t.Fatal("tenant ownership edit accepted")
	}
	report.TenantIDs = []string{"tenant-a"}
	report.Scope = ReportScopeFleet
	if err := s.UpdateReport(ctx, report); err == nil {
		t.Fatal("scope edit accepted")
	}
	for _, raw := range []string{"", `{}`, `{"report_security":{"version":1,"scope":"fleet","tenant_ids":["tenant-a"]}}`, `{"report_security":{"version":2,"scope":"tenant","tenant_ids":["tenant-a"]}}`} {
		if _, ok := ReportRunTenantScope(&ReportRun{ParametersJSON: raw}); ok {
			t.Fatalf("unverified/global result accepted: %s", raw)
		}
	}
	other := &ReportDefinition{Name: "B", Scope: ReportScopeTenant, TenantIDs: []string{"tenant-b"}, Type: ReportTypeAgentInventory, Format: ReportFormatJSON}
	if err := s.CreateReport(ctx, other); err != nil {
		t.Fatal(err)
	}
	schedule := &ReportSchedule{ReportID: other.ID, Name: "B job", Frequency: ReportFrequencyDaily, TimeOfDay: "08:00", Timezone: "UTC", NextRunAt: time.Now()}
	if err := s.CreateReportSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateReportRun(ctx, &ReportRun{ReportID: report.ID, ScheduleID: &schedule.ID, StartedAt: time.Now(), Status: ReportStatusRunning, Format: ReportFormatJSON}); err == nil {
		t.Fatal("cross-report schedule run accepted")
	}
}
