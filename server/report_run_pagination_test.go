package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"printmaster/server/storage"
)

// Observe storage requests, not just serialized responses: discarding a body
// after fetching it would still leave the unbounded-read regression intact.
type reportRunReadStore struct {
	storage.Store
	filters   []storage.ReportRunFilter
	bodyReads []int64
}

func (s *reportRunReadStore) ListReportRuns(ctx context.Context, filter storage.ReportRunFilter) ([]*storage.ReportRun, error) {
	s.filters = append(s.filters, filter)
	if !filter.MetadataOnly || filter.Limit <= 0 || filter.Limit > 100 {
		return nil, fmt.Errorf("unbounded or result-bearing visibility query: %+v", filter)
	}
	return s.Store.ListReportRuns(ctx, filter)
}

func (s *reportRunReadStore) GetReportRun(ctx context.Context, id int64) (*storage.ReportRun, error) {
	s.bodyReads = append(s.bodyReads, id)
	return s.Store.GetReportRun(ctx, id)
}

func TestReportRunVisibilityPagination(t *testing.T) {
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	definitions := make(map[string]*storage.ReportDefinition)
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		report := &storage.ReportDefinition{Name: tenant, Scope: storage.ReportScopeTenant, TenantIDs: []string{tenant}, Type: storage.ReportTypeAgentInventory, Format: storage.ReportFormatJSON}
		if err := store.CreateReport(ctx, report); err != nil {
			t.Fatal(err)
		}
		definitions[tenant] = report
	}
	body := strings.Repeat("inline-secret", 8192)
	now := time.Now().UTC()
	sequence := 0
	create := func(tenant, snapshot, status string) int64 {
		t.Helper()
		run := &storage.ReportRun{ReportID: definitions[tenant].ID, StartedAt: now.Add(-time.Duration(sequence) * time.Second), Status: status, Format: storage.ReportFormatJSON, ResultData: body, ResultSize: int64(len(body))}
		sequence++
		if err := store.CreateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateReportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if snapshot != "" {
			if _, err := store.(*storage.SQLiteStore).DB().ExecContext(ctx, `UPDATE report_runs SET parameters_json = ? WHERE id = ?`, snapshot, run.ID); err != nil {
				t.Fatal(err)
			}
		}
		return run.ID
	}
	foreign := create("tenant-b", "", storage.ReportStatusCompleted)
	for i := 0; i < 109; i++ {
		create("tenant-b", "", storage.ReportStatusCompleted)
	}
	// A permitted parent alone must never expose a legacy, malformed, global,
	// foreign, mixed-tenant or unknown-version execution snapshot.
	invalid := []string{
		`{}`, `not-json`,
		`{"report_security":{"version":1,"scope":"fleet","tenant_ids":["tenant-a"]}}`,
		`{"report_security":{"version":1,"scope":"tenant","tenant_ids":["tenant-b"]}}`,
		`{"report_security":{"version":1,"scope":"tenant","tenant_ids":["tenant-a","tenant-b"]}}`,
		`{"report_security":{"version":2,"scope":"tenant","tenant_ids":["tenant-a"]}}`,
	}
	var unverified int64
	for i := 0; i < 101; i++ {
		unverified = create("tenant-a", invalid[i%len(invalid)], storage.ReportStatusCompleted)
	}
	first := create("tenant-a", "", storage.ReportStatusCompleted)
	create("tenant-b", "", storage.ReportStatusCompleted)
	second := create("tenant-a", "", storage.ReportStatusCompleted)
	third := create("tenant-a", "", storage.ReportStatusCompleted)
	failed := create("tenant-a", "", storage.ReportStatusFailed)
	spy := &reportRunReadStore{Store: store}
	serverStore = spy
	user := NewTestUser(storage.RoleViewer, "tenant-a")
	p := getPrincipal(InjectTestUser(httptest.NewRequest("GET", "/", nil), user))

	for _, tc := range []struct {
		name   string
		filter storage.ReportRunFilter
		ids    []int64
	}{
		{"first", storage.ReportRunFilter{Limit: 1}, []int64{first}},
		{"offset", storage.ReportRunFilter{Limit: 2, Offset: 1}, []int64{second, third}},
		{"past-end", storage.ReportRunFilter{Limit: 2, Offset: 99}, nil},
		{"unlimited", storage.ReportRunFilter{}, []int64{first, second, third, failed}},
		{"status", storage.ReportRunFilter{Status: storage.ReportStatusFailed, Limit: 1}, []int64{failed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy.filters = nil
			runs, err := visibleReportRuns(ctx, p, tc.filter)
			if err != nil || len(runs) != len(tc.ids) {
				t.Fatalf("visible page: len=%d want=%d err=%v", len(runs), len(tc.ids), err)
			}
			for i, run := range runs {
				if run.ID != tc.ids[i] || run.ResultData != "" {
					t.Fatalf("wrong visible metadata at %d: id=%d body length=%d", i, run.ID, len(run.ResultData))
				}
			}
			for i, filter := range spy.filters {
				if filter.Offset != i*100 || filter.Status != tc.filter.Status {
					t.Fatalf("wrong candidate pagination/filter: %+v", spy.filters)
				}
			}
			if tc.name == "first" && len(spy.filters) != 3 {
				t.Fatalf("must scan past foreign candidates, stop at visible page: %+v", spy.filters)
			}
		})
	}

	request := func(user *storage.User, path string, handler http.HandlerFunc, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler(res, InjectTestUser(httptest.NewRequest("GET", path, nil), user))
		if res.Code != want {
			t.Fatalf("%s: got %d want %d: %s", path, res.Code, want, res.Body.String())
		}
		return res
	}
	for _, caller := range []*storage.User{user, NewTestUser(storage.RoleAdmin)} {
		for _, path := range []string{
			"/api/v1/report-runs?limit=1&status=completed",
			fmt.Sprintf("/api/v1/report-runs?limit=1&report_id=%d", definitions["tenant-a"].ID),
			fmt.Sprintf("/api/v1/reports/%d/runs?limit=1", definitions["tenant-a"].ID),
		} {
			handler := http.HandlerFunc(handleReportRunsCollection)
			if strings.Contains(path, "/reports/") {
				handler = handleReport
			}
			res := request(caller, path, handler, 200)
			var page struct {
				Runs  []map[string]any `json:"runs"`
				Count int              `json:"count"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.Count != 1 || len(page.Runs) != 1 {
				t.Fatalf("page envelope changed: %s", res.Body.String())
			}
			if _, exists := page.Runs[0]["result_data"]; exists {
				t.Fatal("list serialized inline result body")
			}
			if caller.Role != storage.RoleAdmin && int64(page.Runs[0]["id"].(float64)) != first {
				t.Fatalf("foreign snapshots consumed visible limit: %s", res.Body.String())
			}
		}
	}
	res := request(user, "/api/v1/report-runs?status=unknown", handleReportRunsCollection, 200)
	if strings.TrimSpace(res.Body.String()) != `{"count":0,"runs":[]}` {
		t.Fatalf("empty envelope changed: %s", res.Body.String())
	}
	res = request(user, "/api/v1/reports/summary", handleReportSummary, 200)
	var summary storage.ReportSummary
	if err := json.Unmarshal(res.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.TotalRuns != 4 || summary.SuccessfulRuns != 3 || summary.FailedRunsLast24h != 1 || summary.StorageUsedBytes != 4*int64(len(body)) {
		t.Fatalf("metadata-only summary changed: %+v", summary)
	}
	for _, id := range []int64{foreign, unverified} {
		for _, suffix := range []string{"", "/download"} {
			request(user, fmt.Sprintf("/api/v1/report-runs/%d%s", id, suffix), handleReportRunResult, 404)
		}
	}
	if len(spy.bodyReads) != 0 {
		t.Fatalf("listing/summary/unauthorized requests fetched bodies: %v", spy.bodyReads)
	}
	res = request(user, fmt.Sprintf("/api/v1/report-runs/%d", first), handleReportRunResult, 200)
	var detail storage.ReportRun
	if err := json.Unmarshal(res.Body.Bytes(), &detail); err != nil || detail.ResultData != body {
		t.Fatalf("authorized detail body compatibility lost: %v", err)
	}
	res = request(user, fmt.Sprintf("/api/v1/report-runs/%d/download", first), handleReportRunResult, 200)
	if res.Body.String() != body || res.Header().Get("Content-Type") != "application/json" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("authorized download semantics changed")
	}
	if len(spy.bodyReads) != 2 || spy.bodyReads[0] != first || spy.bodyReads[1] != first {
		t.Fatalf("unexpected authorized body reads: %v", spy.bodyReads)
	}
}
