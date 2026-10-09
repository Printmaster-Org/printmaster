package main

// Scheduled report API: the Alerts > Report Scheduling UI creates, edits,
// runs, and deletes schedules here. A schedule created from the UI owns a
// dedicated report definition that carries the report type, output format,
// and delivery destinations (email recipients and webhook URL).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"printmaster/server/reports"
	"printmaster/server/storage"
	"printmaster/server/webhook"
)

// maxReportEmailRecipients bounds fan-out per report so a single schedule
// cannot be used to mass-mail through the server's SMTP relay.
const maxReportEmailRecipients = 20

// reportScheduler is the process-wide scheduler started in main. Handlers use
// reportRunner so tests (which never call main) still get a working runner.
var reportScheduler *reports.Scheduler

// newReportScheduler wires the scheduler to the server store, the shared SMTP
// sender, and the SSRF-guarded webhook poster.
func newReportScheduler() *reports.Scheduler {
	deliverer := reports.NewNotificationDeliverer(sendHTMLEmail, webhook.Poster{}, serverLogger)
	return reports.NewScheduler(&ReportStore{store: serverStore}, serverLogger, reports.WithDeliverer(deliverer))
}

func reportRunner() *reports.Scheduler {
	if reportScheduler != nil {
		return reportScheduler
	}
	return newReportScheduler()
}

// reportScheduleView is a schedule plus the parent report fields the UI shows.
type reportScheduleView struct {
	*storage.ReportSchedule
	ReportName      string   `json:"report_name"`
	ReportType      string   `json:"report_type"`
	OutputFormat    string   `json:"output_format"`
	EmailRecipients []string `json:"email_recipients"`
	WebhookURL      string   `json:"webhook_url,omitempty"`
}

func newReportScheduleView(schedule *storage.ReportSchedule, report *storage.ReportDefinition) reportScheduleView {
	view := reportScheduleView{ReportSchedule: schedule, EmailRecipients: []string{}}
	if report != nil {
		view.ReportName = report.Name
		view.ReportType = report.Type
		view.OutputFormat = report.Format
		view.WebhookURL = report.WebhookURL
		if len(report.EmailRecipients) > 0 {
			view.EmailRecipients = report.EmailRecipients
		}
	}
	return view
}

// reportScheduleRequest is the create/update body. Pointer fields distinguish
// "omitted" (keep the current value on update) from explicit zero values. Its
// timing fields share ReportSchedule's JSON names, so legacy clients that post
// a bare ReportSchedule keep working.
type reportScheduleRequest struct {
	Name       *string `json:"name"`
	Enabled    *bool   `json:"enabled"`
	Frequency  *string `json:"frequency"`
	DayOfWeek  *int    `json:"day_of_week"`
	DayOfMonth *int    `json:"day_of_month"`
	TimeOfDay  *string `json:"time_of_day"`
	Timezone   *string `json:"timezone"`

	// Report definition fields.
	ReportType      *string   `json:"report_type"`
	OutputFormat    *string   `json:"output_format"`
	EmailRecipients *[]string `json:"email_recipients"`
	WebhookURL      *string   `json:"webhook_url"`
	// TenantID optionally narrows a new schedule to one tenant. Ignored on update:
	// report tenant scope is immutable.
	TenantID string `json:"tenant_id"`
	// ReportID is accepted only so legacy full-object PUTs can echo it back.
	// Moving a schedule to a different report is rejected: authorization was
	// checked against the current parent report only.
	ReportID *int64 `json:"report_id"`
}

func (req *reportScheduleRequest) touchesReport() bool {
	return req.ReportType != nil || req.OutputFormat != nil || req.EmailRecipients != nil || req.WebhookURL != nil
}

// applyTo overlays the request's schedule fields onto schedule.
func (req *reportScheduleRequest) applyTo(schedule *storage.ReportSchedule) {
	if req.Name != nil {
		schedule.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		schedule.Enabled = *req.Enabled
	}
	if req.Frequency != nil {
		schedule.Frequency = *req.Frequency
	}
	if req.DayOfWeek != nil {
		schedule.DayOfWeek = *req.DayOfWeek
	}
	if req.DayOfMonth != nil {
		schedule.DayOfMonth = *req.DayOfMonth
	}
	if req.TimeOfDay != nil {
		schedule.TimeOfDay = *req.TimeOfDay
	}
	if req.Timezone != nil {
		schedule.Timezone = *req.Timezone
	}
}

// applyToReport overlays the request's report fields onto report.
func (req *reportScheduleRequest) applyToReport(report *storage.ReportDefinition) {
	if req.ReportType != nil {
		report.Type = strings.TrimSpace(*req.ReportType)
	}
	if req.OutputFormat != nil {
		report.Format = strings.ToLower(strings.TrimSpace(*req.OutputFormat))
	}
	if req.EmailRecipients != nil {
		report.EmailRecipients = *req.EmailRecipients
	}
	if req.WebhookURL != nil {
		report.WebhookURL = *req.WebhookURL
	}
}

// normalizeReportSchedule validates and canonicalizes timing fields, then
// computes the next run. Monthly days are limited to 1-28 so every month has
// the configured day.
func normalizeReportSchedule(schedule *storage.ReportSchedule) error {
	if schedule.Name == "" {
		return fmt.Errorf("schedule name is required")
	}
	switch schedule.Frequency {
	case storage.ScheduleFrequencyDaily, storage.ScheduleFrequencyWeekly, storage.ScheduleFrequencyMonthly:
	default:
		return fmt.Errorf("frequency must be daily, weekly, or monthly")
	}
	if schedule.TimeOfDay == "" {
		schedule.TimeOfDay = "08:00"
	}
	t, err := time.Parse("15:04", schedule.TimeOfDay)
	if err != nil {
		return fmt.Errorf("time_of_day must be HH:MM (24-hour)")
	}
	schedule.TimeOfDay = t.Format("15:04")
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q", schedule.Timezone)
	}
	if schedule.DayOfWeek < 0 || schedule.DayOfWeek > 6 {
		return fmt.Errorf("day_of_week must be 0 (Sunday) through 6 (Saturday)")
	}
	if schedule.Frequency == storage.ScheduleFrequencyMonthly {
		if schedule.DayOfMonth == 0 {
			schedule.DayOfMonth = 1
		}
		if schedule.DayOfMonth < 1 || schedule.DayOfMonth > 28 {
			return fmt.Errorf("day_of_month must be between 1 and 28")
		}
	}
	schedule.NextRunAt = reports.NextRun(schedule, time.Now())
	return nil
}

// normalizeReportDefinitionFields validates a definition's type, format, and
// delivery destinations in place. Recipients are reduced to bare addresses,
// de-duplicated case-insensitively, and capped; the webhook URL must pass the
// shared SSRF guard.
func normalizeReportDefinitionFields(report *storage.ReportDefinition) error {
	if report.Type != "" && !reports.IsSupportedType(report.Type) {
		return fmt.Errorf("unsupported report type %q", report.Type)
	}
	switch report.Format {
	case "":
		report.Format = storage.ReportFormatJSON
	case storage.ReportFormatJSON, storage.ReportFormatCSV, storage.ReportFormatHTML:
	default:
		return fmt.Errorf("format must be json, csv, or html")
	}

	recipients := make([]string, 0, len(report.EmailRecipients))
	seen := make(map[string]struct{}, len(report.EmailRecipients))
	for _, raw := range report.EmailRecipients {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		addr, err := mail.ParseAddress(raw)
		if err != nil {
			return fmt.Errorf("invalid email recipient %q", raw)
		}
		clean, err := validateEmailAddress(addr.Address)
		// Storage joins recipients with commas, so a comma can never be part of one.
		if err != nil || strings.Contains(clean, ",") {
			return fmt.Errorf("invalid email recipient %q", raw)
		}
		key := strings.ToLower(clean)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		recipients = append(recipients, clean)
	}
	if len(recipients) > maxReportEmailRecipients {
		return fmt.Errorf("at most %d email recipients are allowed", maxReportEmailRecipients)
	}
	report.EmailRecipients = recipients

	report.WebhookURL = strings.TrimSpace(report.WebhookURL)
	if report.WebhookURL != "" {
		safe, err := webhook.ValidateURL(report.WebhookURL)
		if err != nil {
			return fmt.Errorf("invalid webhook URL: %w", err)
		}
		report.WebhookURL = safe
	}
	return nil
}

// defaultTimeRangeForFrequency picks the window that matches the cadence, so a
// weekly usage report covers exactly the week since the previous delivery.
func defaultTimeRangeForFrequency(frequency string) string {
	switch frequency {
	case storage.ScheduleFrequencyDaily:
		return "last_24h"
	case storage.ScheduleFrequencyWeekly:
		return "last_7d"
	default:
		return "last_30d"
	}
}

// scheduleReportScope chooses ownership for a schedule's dedicated report.
// Administrators get fleet scope unless they name a tenant; everyone else is
// pinned to a tenant they can access (their full tenant set by default).
func scheduleReportScope(p *Principal, tenantID string) (string, []string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID != "" {
		if !p.CanAccessTenant(tenantID) {
			return "", nil, fmt.Errorf("tenant not accessible")
		}
		return storage.ReportScopeTenant, []string{tenantID}, nil
	}
	if p.IsAdmin() {
		return storage.ReportScopeFleet, nil, nil
	}
	return storage.ReportScopeTenant, p.AllowedTenantIDs(), nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleReportSchedulesCollection handles GET (list visible schedules) and
// POST (create a schedule plus its dedicated report) on /api/v1/report-schedules.
func handleReportSchedulesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := reportPrincipal(w, r, false)
		if p == nil {
			return
		}
		schedules, err := visibleReportSchedules(r.Context(), p)
		if err != nil {
			logError("Failed to list all report schedules", "error", err)
			http.Error(w, fmt.Sprintf("list schedules: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"schedules": schedules, "count": len(schedules)})
	case http.MethodPost:
		createReportScheduleWithReport(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func createReportScheduleWithReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := reportPrincipal(w, r, true)
	if p == nil {
		return
	}
	var req reportScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	schedule := storage.ReportSchedule{Enabled: true, Frequency: storage.ScheduleFrequencyWeekly}
	req.applyTo(&schedule)
	if err := normalizeReportSchedule(&schedule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scope, tenantIDs, err := scheduleReportScope(p, req.TenantID)
	if err != nil {
		http.Error(w, "Forbidden report scope", http.StatusForbidden)
		return
	}
	report := storage.ReportDefinition{
		Name:          schedule.Name,
		Description:   "Created by a report schedule",
		Type:          storage.ReportTypeDeviceInventory,
		Format:        storage.ReportFormatCSV,
		Scope:         scope,
		TenantIDs:     tenantIDs,
		TimeRangeType: defaultTimeRangeForFrequency(schedule.Frequency),
		CreatedBy:     p.User.Username,
	}
	req.applyToReport(&report)
	if err := normalizeReportDefinitionFields(&report); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validateReportScope(w, r, &report) {
		return
	}

	if err := serverStore.CreateReport(ctx, &report); err != nil {
		logError("Failed to create scheduled report definition", "name", report.Name, "error", err)
		http.Error(w, "Failed to create report", http.StatusInternalServerError)
		return
	}
	schedule.ReportID = report.ID
	if err := serverStore.CreateReportSchedule(ctx, &schedule); err != nil {
		logError("Failed to create report schedule", "report_id", report.ID, "error", err)
		// Roll back the dedicated definition so failed creates leave no orphans.
		if derr := serverStore.DeleteReport(ctx, report.ID); derr != nil {
			logError("Failed to roll back scheduled report definition", "report_id", report.ID, "error", derr)
		}
		http.Error(w, "Failed to create schedule", http.StatusInternalServerError)
		return
	}

	logInfo("Report schedule created",
		"schedule_id", schedule.ID, "report_id", report.ID, "type", report.Type, "frequency", schedule.Frequency,
		"email_recipients", len(report.EmailRecipients), "webhook", report.WebhookURL != "",
		"next_run_at", schedule.NextRunAt, "user", p.User.Username)
	writeJSON(w, http.StatusCreated, newReportScheduleView(&schedule, &report))
}

// handleSchedule handles GET/PUT/DELETE /api/v1/report-schedules/{id} and
// POST /api/v1/report-schedules/{id}/run.
func handleSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/report-schedules/")
	runRequested := strings.HasSuffix(rest, "/run")
	rest = strings.TrimSuffix(rest, "/run")

	write := r.Method != http.MethodGet
	if reportPrincipal(w, r, write) == nil {
		return
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		http.Error(w, "Invalid schedule ID", http.StatusBadRequest)
		return
	}
	existing, err := serverStore.GetReportSchedule(ctx, id)
	if err != nil {
		http.Error(w, "Failed to load schedule", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.NotFound(w, r)
		return
	}
	report := reportForRequest(w, r, existing.ReportID, write, false)
	if report == nil {
		return
	}

	if runRequested {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		runScheduleNow(w, r, existing)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, newReportScheduleView(existing, report))
	case http.MethodPut:
		updateReportSchedule(w, r, existing, report)
	case http.MethodDelete:
		if err := serverStore.DeleteReportSchedule(ctx, id); err != nil {
			logError("Failed to delete report schedule", "schedule_id", id, "error", err)
			http.Error(w, fmt.Sprintf("delete schedule: %v", err), http.StatusInternalServerError)
			return
		}
		logInfo("Report schedule deleted", "schedule_id", id, "report_id", existing.ReportID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func updateReportSchedule(w http.ResponseWriter, r *http.Request, existing *storage.ReportSchedule, report *storage.ReportDefinition) {
	ctx := r.Context()
	var req reportScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if req.ReportID != nil && *req.ReportID != existing.ReportID {
		http.Error(w, "A schedule cannot be moved to a different report", http.StatusBadRequest)
		return
	}

	schedule := *existing
	req.applyTo(&schedule)
	if err := normalizeReportSchedule(&schedule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.touchesReport() {
		if report.IsBuiltIn {
			http.Error(w, "Built-in report definitions cannot be changed from a schedule", http.StatusBadRequest)
			return
		}
		updated := *report
		req.applyToReport(&updated)
		if err := normalizeReportDefinitionFields(&updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !validateReportScope(w, r, &updated) {
			return
		}
		if err := serverStore.UpdateReport(ctx, &updated); err != nil {
			logError("Failed to update scheduled report definition", "report_id", report.ID, "error", err)
			http.Error(w, "Failed to update report", http.StatusInternalServerError)
			return
		}
		report = &updated
	}

	if err := serverStore.UpdateReportSchedule(ctx, &schedule); err != nil {
		logError("Failed to update report schedule", "schedule_id", schedule.ID, "error", err)
		http.Error(w, fmt.Sprintf("update schedule: %v", err), http.StatusInternalServerError)
		return
	}
	logInfo("Report schedule updated",
		"schedule_id", schedule.ID, "report_id", report.ID, "enabled", schedule.Enabled, "frequency", schedule.Frequency,
		"email_recipients", len(report.EmailRecipients), "webhook", report.WebhookURL != "", "next_run_at", schedule.NextRunAt)
	writeJSON(w, http.StatusOK, newReportScheduleView(&schedule, report))
}

func runScheduleNow(w http.ResponseWriter, r *http.Request, schedule *storage.ReportSchedule) {
	username := getPrincipal(r).User.Username
	run, err := reportRunner().RunScheduleNow(r.Context(), schedule, username)
	if err != nil {
		logError("On-demand schedule run failed", "schedule_id", schedule.ID, "user", username, "error", err)
		http.Error(w, fmt.Sprintf("run schedule: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
