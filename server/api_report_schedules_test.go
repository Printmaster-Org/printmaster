package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"printmaster/common/logger"
	"printmaster/server/reports"
	"printmaster/server/storage"
)

// capturingDeliverer records deliveries instead of sending email/webhooks.
type capturingDeliverer struct {
	mu  sync.Mutex
	got []reports.Delivery
}

func (c *capturingDeliverer) Deliver(ctx context.Context, d reports.Delivery) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, d)
	return nil
}

func TestReportScheduleAPI(t *testing.T) {
	previousStore, previousScheduler, previousLogger := serverStore, reportScheduler, serverLogger
	t.Cleanup(func() { serverStore, reportScheduler, serverLogger = previousStore, previousScheduler, previousLogger })
	serverLogger = logger.New(logger.ERROR, t.TempDir(), 100)
	store := SetupTestStore(t)
	ctx := context.Background()
	if err := store.CreateTenant(ctx, &storage.Tenant{ID: "tenant-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	deliverer := &capturingDeliverer{}
	reportScheduler = reports.NewScheduler(&ReportStore{store: store}, serverLogger, reports.WithDeliverer(deliverer))

	tokens := map[string]string{}
	for _, f := range []struct {
		name string
		role storage.Role
		ids  []string
	}{
		{"admin", storage.RoleAdmin, nil},
		{"operator", storage.RoleOperator, []string{"tenant-a"}},
		{"viewer", storage.RoleViewer, []string{"tenant-a"}},
	} {
		user := &storage.User{Username: f.name, Role: f.role, TenantIDs: f.ids}
		if err := store.CreateUser(ctx, user, "schedule-test-password"); err != nil {
			t.Fatal(err)
		}
		session, err := store.CreateSession(ctx, user.ID, 60)
		if err != nil {
			t.Fatal(err)
		}
		tokens[f.name] = session.Token
	}

	mux := reportSecurityMux()
	request := func(user, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			if data, err = json.Marshal(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s %s: got %d want %d: %s", user, method, path, res.Code, want, res.Body.String())
		}
		return res
	}
	decodeView := func(res *httptest.ResponseRecorder) reportScheduleView {
		t.Helper()
		view := reportScheduleView{ReportSchedule: &storage.ReportSchedule{}}
		if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode view: %v: %s", err, res.Body.String())
		}
		return view
	}

	valid := map[string]any{
		"name":             "Weekly supplies",
		"report_type":      storage.ReportTypeSuppliesStatus,
		"output_format":    "html",
		"frequency":        "weekly",
		"day_of_week":      5,
		"time_of_day":      "09:30",
		"timezone":         "UTC",
		"email_recipients": []string{"Ops@Example.com", "ops@example.com", "Manager <mgr@example.com>"},
		"webhook_url":      "https://203.0.113.10/hook",
	}

	t.Run("viewer cannot create", func(t *testing.T) {
		request("viewer", http.MethodPost, "/api/v1/report-schedules", valid, http.StatusForbidden)
	})

	t.Run("validation", func(t *testing.T) {
		for name, patch := range map[string]map[string]any{
			"missing name":       {"name": ""},
			"bad frequency":      {"frequency": "hourly"},
			"bad time":           {"time_of_day": "25:00"},
			"bad timezone":       {"timezone": "Mars/Base"},
			"bad weekday":        {"day_of_week": 9},
			"bad month day":      {"frequency": "monthly", "day_of_month": 31},
			"bad email":          {"email_recipients": []string{"not-an-email"}},
			"comma in email":     {"email_recipients": []string{"\"a,b\"@example.com"}},
			"private webhook":    {"webhook_url": "http://127.0.0.1/hook"},
			"bad report type":    {"report_type": "nope"},
			"bad output format":  {"output_format": "pdf"},
			"too many emails":    {"email_recipients": manyRecipients(maxReportEmailRecipients + 1)},
			"foreign tenant id":  {"tenant_id": "tenant-z"},
			"non-http webhook":   {"webhook_url": "ftp://example.com/x"},
			"localhost webhook":  {"webhook_url": "http://localhost/x"},
			"metadata webhook":   {"webhook_url": "http://169.254.169.254/latest"},
			"private-net hook":   {"webhook_url": "http://10.0.0.5/x"},
			"day of week negate": {"day_of_week": -1},
		} {
			body := map[string]any{}
			for k, v := range valid {
				body[k] = v
			}
			for k, v := range patch {
				body[k] = v
			}
			want := http.StatusBadRequest
			if name == "foreign tenant id" {
				want = http.StatusForbidden
			}
			t.Run(name, func(t *testing.T) {
				request("operator", http.MethodPost, "/api/v1/report-schedules", body, want)
			})
		}
	})

	var scheduleID int64
	t.Run("operator creates tenant-scoped schedule", func(t *testing.T) {
		view := decodeView(request("operator", http.MethodPost, "/api/v1/report-schedules", valid, http.StatusCreated))
		scheduleID = view.ID
		if view.ReportType != storage.ReportTypeSuppliesStatus || view.OutputFormat != "html" || view.WebhookURL != "https://203.0.113.10/hook" {
			t.Fatalf("unexpected view: %+v", view)
		}
		if strings.Join(view.EmailRecipients, ",") != "Ops@Example.com,mgr@example.com" {
			t.Fatalf("recipients not normalized/deduplicated (first spelling wins): %v", view.EmailRecipients)
		}
		if view.DayOfWeek != 5 || view.TimeOfDay != "09:30" || !view.NextRunAt.After(time.Now()) || view.NextRunAt.Weekday() != time.Friday {
			t.Fatalf("unexpected timing: %+v", view.ReportSchedule)
		}
		report, err := store.GetReport(ctx, view.ReportID)
		if err != nil || report == nil {
			t.Fatalf("GetReport: %v", err)
		}
		if report.Scope != storage.ReportScopeTenant || strings.Join(report.TenantIDs, ",") != "tenant-a" {
			t.Fatalf("report not tenant scoped: scope=%s tenants=%v", report.Scope, report.TenantIDs)
		}
	})

	t.Run("list exposes delivery fields", func(t *testing.T) {
		res := request("viewer", http.MethodGet, "/api/v1/report-schedules", nil, http.StatusOK)
		var body struct {
			Schedules []map[string]any `json:"schedules"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Schedules) != 1 || body.Schedules[0]["next_run_at"] == nil || body.Schedules[0]["report_type"] != storage.ReportTypeSuppliesStatus {
			t.Fatalf("unexpected list: %s", res.Body.String())
		}
	})

	path := "/api/v1/report-schedules/" + strconv.FormatInt(scheduleID, 10)
	t.Run("update delivery and timing", func(t *testing.T) {
		view := decodeView(request("operator", http.MethodPut, path, map[string]any{
			"frequency": "monthly", "day_of_month": 15, "email_recipients": []string{}, "webhook_url": "",
		}, http.StatusOK))
		if view.Frequency != "monthly" || view.DayOfMonth != 15 || len(view.EmailRecipients) != 0 || view.WebhookURL != "" {
			t.Fatalf("update not applied: %+v", view)
		}
		if view.NextRunAt.Day() != 15 {
			t.Fatalf("next run not recalculated: %v", view.NextRunAt)
		}
		request("operator", http.MethodPut, path, map[string]any{"webhook_url": "http://192.168.0.1/x"}, http.StatusBadRequest)
		request("viewer", http.MethodPut, path, map[string]any{"enabled": false}, http.StatusForbidden)
	})

	t.Run("run now delivers without moving next run", func(t *testing.T) {
		before, err := store.GetReportSchedule(ctx, scheduleID)
		if err != nil {
			t.Fatal(err)
		}
		request("viewer", http.MethodPost, path+"/run", nil, http.StatusForbidden)
		res := request("operator", http.MethodPost, path+"/run", nil, http.StatusOK)
		var run storage.ReportRun
		if err := json.Unmarshal(res.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.Status != storage.ReportStatusCompleted || run.RunBy != "operator" {
			t.Fatalf("unexpected run: %+v", run)
		}
		if len(deliverer.got) != 1 {
			t.Fatalf("deliveries = %d, want 1", len(deliverer.got))
		}
		after, err := store.GetReportSchedule(ctx, scheduleID)
		if err != nil {
			t.Fatal(err)
		}
		if !after.NextRunAt.Equal(before.NextRunAt) {
			t.Fatalf("next run moved: %v -> %v", before.NextRunAt, after.NextRunAt)
		}
		request("operator", http.MethodGet, path+"/run", nil, http.StatusMethodNotAllowed)
	})

	t.Run("delete", func(t *testing.T) {
		request("operator", http.MethodDelete, path, nil, http.StatusNoContent)
		request("operator", http.MethodGet, path, nil, http.StatusNotFound)
	})
}

func manyRecipients(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "user" + strconv.Itoa(i) + "@example.com"
	}
	return out
}
