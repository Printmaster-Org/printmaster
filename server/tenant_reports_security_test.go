package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/server/reports"
	"printmaster/server/storage"
)

func reportSecurityMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports", requireWebAuth(handleReports))
	mux.HandleFunc("/api/v1/reports/summary", requireWebAuth(handleReportSummary))
	mux.HandleFunc("/api/v1/reports/types", requireWebAuth(handleReportTypes))
	mux.HandleFunc("/api/v1/reports/", requireWebAuth(handleReport))
	mux.HandleFunc("/api/v1/report-schedules", requireWebAuth(handleReportSchedulesCollection))
	mux.HandleFunc("/api/v1/report-schedules/", requireWebAuth(handleSchedule))
	mux.HandleFunc("/api/v1/report-runs", requireWebAuth(handleReportRunsCollection))
	mux.HandleFunc("/api/v1/report-runs/", requireWebAuth(handleReportRunResult))
	return mux
}

func TestTenantReportsSecurity(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	mux := reportSecurityMux()
	for _, id := range []string{"tenant-a", "tenant-b", "tenant-ab"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	tokens := make(map[string]string)
	for _, fixture := range []struct {
		name string
		role storage.Role
		ids  []string
	}{
		{"viewer", storage.RoleViewer, []string{"tenant-a"}},
		{"operator", storage.RoleOperator, []string{"tenant-a"}},
		{"other", storage.RoleOperator, []string{"tenant-b"}},
		{"multi", storage.RoleViewer, []string{"tenant-a", "tenant-b"}},
		{"empty", storage.RoleOperator, nil},
		{"admin", storage.RoleAdmin, nil},
	} {
		user := &storage.User{Username: fixture.name, Role: fixture.role, TenantIDs: fixture.ids}
		if err := store.CreateUser(ctx, user, "security-test-password"); err != nil {
			t.Fatal(err)
		}
		session, err := store.CreateSession(ctx, user.ID, 60)
		if err != nil {
			t.Fatal(err)
		}
		tokens[fixture.name] = session.Token
	}
	request := func(user, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		if token := tokens[user]; token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s %s: got %d want %d: %s", user, method, path, res.Code, want, res.Body.String())
		}
		return res
	}
	definitions := make(map[string]*storage.ReportDefinition)
	schedules := make(map[string]*storage.ReportSchedule)
	runs := make(map[string]*storage.ReportRun)
	for _, fixture := range []struct {
		name, scope, owner string
		ids                []string
		builtIn            bool
	}{
		{"a", storage.ReportScopeTenant, "operator", []string{"tenant-a"}, false},
		{"b", storage.ReportScopeTenant, "operator", []string{"tenant-b"}, false},
		{"prefix", storage.ReportScopeTenant, "operator", []string{"tenant-ab"}, false},
		{"mixed", storage.ReportScopeTenant, "operator", []string{"tenant-a", "tenant-b"}, false},
		{"not-owned", storage.ReportScopeTenant, "other", []string{"tenant-a"}, false},
		{"builtin", storage.ReportScopeFleet, "", nil, true},
		{"global", storage.ReportScopeFleet, "operator", nil, false},
		{"empty-scope", storage.ReportScopeTenant, "operator", nil, false},
	} {
		report := &storage.ReportDefinition{Name: fixture.name, Scope: fixture.scope, TenantIDs: fixture.ids, CreatedBy: fixture.owner, IsBuiltIn: fixture.builtIn, Type: storage.ReportTypeAgentInventory, Format: storage.ReportFormatJSON}
		if err := store.CreateReport(ctx, report); err != nil {
			t.Fatal(err)
		}
		definitions[fixture.name] = report
		schedule := &storage.ReportSchedule{ReportID: report.ID, Name: fixture.name, Enabled: true, Frequency: storage.ReportFrequencyDaily, TimeOfDay: "08:00", Timezone: "UTC", NextRunAt: time.Now().Add(time.Hour)}
		if err := store.CreateReportSchedule(ctx, schedule); err != nil {
			t.Fatal(err)
		}
		schedules[fixture.name] = schedule
		run := &storage.ReportRun{ReportID: report.ID, ScheduleID: &schedule.ID, RunBy: "operator", Status: storage.ReportStatusCompleted, Format: storage.ReportFormatJSON, StartedAt: time.Now(), ResultData: fixture.name + " secret", ResultSize: 100, DurationMS: 10}
		if err := store.CreateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		runs[fixture.name] = run
	}
	reportPath := func(name string) string { return fmt.Sprintf("/api/v1/reports/%d", definitions[name].ID) }
	schedulePath := func(name string) string { return fmt.Sprintf("/api/v1/report-schedules/%d", schedules[name].ID) }
	runPath := func(name string) string { return fmt.Sprintf("/api/v1/report-runs/%d", runs[name].ID) }
	legacy := &storage.ReportRun{ReportID: definitions["a"].ID, StartedAt: time.Now(), Status: storage.ReportStatusCompleted, Format: storage.ReportFormatJSON, RunBy: "operator"}
	if err := store.CreateReportRun(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-security result; its parent has valid tenant ownership,
	// but the old generator did not enforce that ownership during execution.
	if _, err := store.(*storage.SQLiteStore).DB().ExecContext(ctx, `UPDATE report_runs SET parameters_json = '', result_data = 'legacy fleet secret' WHERE id = ?`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	legacyPath := fmt.Sprintf("/api/v1/report-runs/%d", legacy.ID)
	request("admin", "GET", legacyPath+"/download", nil, 200)
	for _, user := range []string{"viewer", "operator"} {
		request(user, "GET", legacyPath, nil, 404)
		request(user, "GET", legacyPath+"/download", nil, 404)
		for _, name := range []string{"b", "prefix", "mixed", "global", "empty-scope"} {
			request(user, "GET", reportPath(name), nil, 404)
			request(user, "GET", reportPath(name)+"/runs", nil, 404)
			request(user, "GET", reportPath(name)+"/schedules", nil, 404)
			request(user, "GET", schedulePath(name), nil, 404)
			request(user, "GET", runPath(name), nil, 404)
			request(user, "GET", runPath(name)+"/download", nil, 404)
			request(user, "GET", fmt.Sprintf("/api/v1/report-runs?report_id=%d", definitions[name].ID), nil, 404)
		}
		request(user, "GET", reportPath("a"), nil, 200)
		request(user, "GET", runPath("a")+"/download", nil, 200)
		request(user, "GET", reportPath("builtin"), nil, 200)
		request(user, "GET", runPath("builtin"), nil, 404)
		request(user, "GET", runPath("builtin")+"/download", nil, 404)
		request(user, "GET", reportPath("builtin")+"/runs", nil, 404)
		request(user, "GET", reportPath("builtin")+"/schedules", nil, 404)
		request(user, "GET", schedulePath("builtin"), nil, 404)
		for _, path := range []string{"/api/v1/reports?created_by=operator", "/api/v1/reports?limit=1&offset=1", "/api/v1/report-runs", "/api/v1/report-schedules"} {
			res := request(user, "GET", path, nil, 200)
			for _, forbidden := range []string{`"name":"b"`, `"name":"prefix"`, `"name":"mixed"`, `"name":"global"`, "b secret", "builtin secret", "mixed secret", "legacy fleet secret"} {
				if strings.Contains(res.Body.String(), forbidden) {
					t.Fatalf("foreign data leaked through %s: %s", path, res.Body.String())
				}
			}
		}
		res := request(user, "GET", "/api/v1/reports/summary", nil, 200)
		var summary storage.ReportSummary
		if err := json.Unmarshal(res.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.TotalReports != 3 || summary.TotalRuns != 2 || summary.TotalSchedules != 2 || summary.StorageUsedBytes != 200 || summary.AverageRunTimeMS != 10 {
			t.Fatalf("summary leaked or lost scope: %+v", summary)
		}
	}
	for _, name := range []string{"a", "b", "mixed", "builtin", "global"} {
		request("admin", "GET", reportPath(name), nil, 200)
		request("admin", "GET", runPath(name)+"/download", nil, 200)
	}
	request("multi", "GET", reportPath("mixed"), nil, 200)
	request("multi", "GET", runPath("mixed"), nil, 200)
	for _, path := range []string{"/api/v1/reports", "/api/v1/reports/types", "/api/v1/reports/summary", "/api/v1/report-runs", "/api/v1/report-schedules", reportPath("a"), runPath("a"), schedulePath("a")} {
		request("empty", "GET", path, nil, 403)
		request("anonymous", "GET", path, nil, 401)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		request("viewer", method, reportPath("a"), definitions["a"], 403)
		request("viewer", method, schedulePath("a"), schedules["a"], 403)
		request("operator", method, reportPath("b"), definitions["b"], 404)
		request("operator", method, schedulePath("b"), schedules["b"], 404)
		request("operator", method, reportPath("not-owned"), definitions["not-owned"], 403)
		request("operator", method, schedulePath("not-owned"), schedules["not-owned"], 403)
	}
	request("viewer", "POST", reportPath("a")+"/run", nil, 403)
	request("viewer", "POST", reportPath("a")+"/schedules", schedules["a"], 403)
	request("operator", "POST", reportPath("b")+"/run", nil, 404)
	request("operator", "POST", reportPath("b")+"/schedules", schedules["b"], 404)
	request("operator", "POST", reportPath("builtin")+"/run", nil, 404)
	request("operator", "POST", reportPath("not-owned")+"/run", nil, 403)
	request("operator", "GET", reportPath("a")+"/runanything", nil, 404)
	request("operator", "GET", "/api/v1/report-runs?report_id=0", nil, 400)
	request("operator", "GET", "/api/v1/report-runs?report_id=oops", nil, 400)
	request("operator", "GET", reportPath("a")+"/runs", nil, 200) // Must not dispatch to /run.
	foreignScope := *definitions["a"]
	foreignScope.TenantIDs = []string{"tenant-b"}
	request("operator", "POST", "/api/v1/reports", &foreignScope, 403)
	request("operator", "PUT", reportPath("a"), &foreignScope, 400)
	request("admin", "PUT", reportPath("a"), &foreignScope, 400)
	request("operator", "POST", "/api/v1/reports", definitions["global"], 403)
	request("viewer", "POST", "/api/v1/reports", definitions["a"], 403)
	newReport := *definitions["a"]
	newReport.IsBuiltIn, newReport.CreatedBy = true, "admin"
	res := request("operator", "POST", "/api/v1/reports", &newReport, 201)
	var created storage.ReportDefinition
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.IsBuiltIn || created.CreatedBy != "operator" || created.ID == newReport.ID {
		t.Fatalf("spoofed metadata accepted: %+v", created)
	}
	request("operator", "PUT", reportPath("a"), definitions["a"], 200)
	request("operator", "PUT", schedulePath("a"), schedules["a"], 200)
	reparent := *schedules["a"]
	reparent.ReportID = definitions["b"].ID
	request("operator", "PUT", schedulePath("a"), &reparent, 400)
	request("operator", "POST", reportPath("a")+"/schedules", schedules["a"], 201)
	request("operator", "POST", reportPath("a")+"/run", nil, 200)
	request("admin", "POST", reportPath("builtin")+"/run", nil, 200)
	request("operator", "DELETE", schedulePath("a"), nil, 204)
	request("operator", "DELETE", reportPath("a"), nil, 204)
}

func TestTenantReportsGenerationSecurity(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		agent := &storage.Agent{AgentID: "agent-" + id, Token: "tenant-reports-token-" + id, TenantID: id, Name: id, RegisteredAt: time.Now(), LastSeen: time.Now(), ProtocolVersion: "1"}
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		device := &storage.Device{Serial: "device-" + id, AgentID: agent.AgentID, Model: id, LastSeen: time.Now(), FirstSeen: time.Now()}
		if err := store.UpsertDevice(ctx, device); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateSite(ctx, &storage.Site{ID: "site-" + id, TenantID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAgentSites(ctx, agent.AgentID, []string{"site-" + id}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateAlert(ctx, &storage.Alert{TenantID: id, AgentID: agent.AgentID, Title: id, Type: storage.AlertTypeAgentOffline, Scope: storage.AlertScopeAgent, Status: storage.AlertStatusActive, Severity: storage.AlertSeverityWarning, TriggeredAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"agent", "site"} {
		report := &storage.ReportDefinition{Name: "bad scope", Scope: storage.ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Type: storage.ReportTypeDeviceInventory, Format: storage.ReportFormatJSON}
		if field == "agent" {
			report.AgentIDs = []string{"agent-tenant-b"}
		} else {
			report.SiteIDs = []string{"site-tenant-b"}
		}
		req := InjectTestUser(httptest.NewRequest("POST", "/api/v1/reports", nil), NewTestUser(storage.RoleOperator, "tenant-a"))
		res := httptest.NewRecorder()
		if validateReportScope(res, req, report) || res.Code != http.StatusForbidden {
			t.Fatalf("foreign %s accepted", field)
		}
	}
	generator := reports.NewGenerator(&ReportStore{store: store})
	for _, kind := range []string{storage.ReportTypeDeviceInventory, storage.ReportTypeAgentInventory, storage.ReportTypeSiteInventory, storage.ReportTypeAlertHistory} {
		for _, ids := range [][]string{{"tenant-a"}, {"no-agents"}} {
			report := &storage.ReportDefinition{Scope: storage.ReportScopeTenant, TenantIDs: ids, Type: kind, Format: storage.ReportFormatJSON}
			result, err := generator.Generate(ctx, reports.GenerateParams{Report: report, StartTime: time.Now().Add(-time.Hour), EndTime: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result)
			if bytes.Contains(data, []byte("tenant-b")) {
				t.Fatalf("%s leaked foreign tenant: %s", kind, data)
			}
			if ids[0] == "no-agents" && result.RowCount != 0 {
				t.Fatalf("%s empty scope became fleet: %s", kind, data)
			}
			if ids[0] == "tenant-a" && result.RowCount != 1 {
				t.Fatalf("%s lost permitted tenant data: %s", kind, data)
			}
		}
	}
	siteReport := &storage.ReportDefinition{Scope: storage.ReportScopeSite, TenantIDs: []string{"tenant-a"}, SiteIDs: []string{"site-tenant-a"}, Type: storage.ReportTypeDeviceInventory}
	result, err := generator.Generate(ctx, reports.GenerateParams{Report: siteReport})
	if err != nil || result.RowCount != 1 {
		t.Fatalf("site scope lost permitted device: result=%+v err=%v", result, err)
	}
	// A reused serial must not reveal metrics written by a foreign agent or
	// metrics whose historical owning agent is missing/unknown.
	for _, metric := range []*storage.MetricsSnapshot{
		{Serial: "device-tenant-a", AgentID: "agent-tenant-a", Timestamp: time.Now().Add(-time.Minute), PageCount: 10},
		{Serial: "device-tenant-a", AgentID: "agent-tenant-b", Timestamp: time.Now(), PageCount: 987654},
	} {
		seed := *metric
		seed.AgentID = "agent-tenant-a"
		if err := store.SaveMetrics(ctx, &seed); err != nil {
			t.Fatal(err)
		}
		if metric.AgentID != seed.AgentID {
			// Simulate legacy metrics with mismatched ownership; normal writes
			// now reject these rows before report authorization is exercised.
			if _, err := store.(*storage.SQLiteStore).DB().ExecContext(ctx, "UPDATE metrics_history SET agent_id = ? WHERE serial = ? AND page_count = ?", metric.AgentID, metric.Serial, metric.PageCount); err != nil {
				t.Fatal(err)
			}
		}
	}
	usage := &storage.ReportDefinition{Scope: storage.ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Type: storage.ReportTypeUsageSummary, TimeRangeType: "current"}
	result, err = generator.Generate(ctx, reports.GenerateParams{Report: usage})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte("987654")) {
		t.Fatalf("foreign historical metrics leaked: %s", data)
	}
	for _, report := range []*storage.ReportDefinition{
		{Scope: storage.ReportScopeTenant, Type: storage.ReportTypeDeviceInventory},
		{Scope: storage.ReportScopeTenant, TenantIDs: []string{"tenant-a"}, Type: storage.ReportTypeAlertSummary},
	} {
		if _, err := generator.Generate(ctx, reports.GenerateParams{Report: report}); err == nil {
			t.Fatal("unscoped definition/global aggregate accepted")
		}
	}
}
