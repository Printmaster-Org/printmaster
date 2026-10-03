package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"printmaster/server/reports"
	"printmaster/server/storage"
	"strconv"
	"strings"
	"time"
)

// ReportStore wraps the storage.Store to implement report interfaces.
type ReportStore struct {
	store storage.Store
}

func (rs *ReportStore) ListAgentsForReport(ctx context.Context, scope storage.ReportDataScope) ([]*storage.Agent, error) {
	return rs.store.ListAgentsForReport(ctx, scope)
}

func (rs *ReportStore) GetAgentForReport(ctx context.Context, agentID string, scope storage.ReportDataScope) (*storage.Agent, error) {
	return rs.store.GetAgentForReport(ctx, agentID, scope)
}

func (rs *ReportStore) ListDevicesForReport(ctx context.Context, scope storage.ReportDataScope) ([]*storage.Device, error) {
	return rs.store.ListDevicesForReport(ctx, scope)
}

func (rs *ReportStore) ListTenantsForReport(ctx context.Context, scope storage.ReportDataScope) ([]*storage.Tenant, error) {
	return rs.store.ListTenantsForReport(ctx, scope)
}

func (rs *ReportStore) ListSitesForReport(ctx context.Context, scope storage.ReportDataScope) ([]*storage.Site, error) {
	return rs.store.ListSitesForReport(ctx, scope)
}

func (rs *ReportStore) ListAlertsForReport(ctx context.Context, filter storage.AlertFilter, scope storage.ReportDataScope) ([]*storage.Alert, error) {
	return rs.store.ListAlertsForReport(ctx, filter, scope)
}

func (rs *ReportStore) ListAllDevices(ctx context.Context) ([]*storage.Device, error) {
	return rs.store.ListAllDevices(ctx)
}

func (rs *ReportStore) GetLatestMetrics(ctx context.Context, serial string) (*storage.MetricsSnapshot, error) {
	return rs.store.GetLatestMetrics(ctx, serial)
}

func (rs *ReportStore) GetMetricsAtOrBefore(ctx context.Context, serial string, at time.Time) (*storage.MetricsSnapshot, error) {
	return rs.store.GetMetricsAtOrBefore(ctx, serial, at)
}

func (rs *ReportStore) GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*storage.MetricsSnapshot, error) {
	return rs.store.GetMetricsHistory(ctx, serial, since)
}

func (rs *ReportStore) ListAgents(ctx context.Context) ([]*storage.Agent, error) {
	return rs.store.ListAgents(ctx)
}

func (rs *ReportStore) GetAgentSiteIDs(ctx context.Context, agentID string) ([]string, error) {
	return rs.store.GetAgentSiteIDs(ctx, agentID)
}

func (rs *ReportStore) GetAgent(ctx context.Context, agentID string) (*storage.Agent, error) {
	return rs.store.GetAgent(ctx, agentID)
}

func (rs *ReportStore) ListTenants(ctx context.Context) ([]*storage.Tenant, error) {
	return rs.store.ListTenants(ctx)
}

func (rs *ReportStore) GetTenant(ctx context.Context, id string) (*storage.Tenant, error) {
	return rs.store.GetTenant(ctx, id)
}

func (rs *ReportStore) ListSitesByTenant(ctx context.Context, tenantID string) ([]*storage.Site, error) {
	return rs.store.ListSitesByTenant(ctx, tenantID)
}

func (rs *ReportStore) ListAlerts(ctx context.Context, filter storage.AlertFilter) ([]*storage.Alert, error) {
	return rs.store.ListAlerts(ctx, filter)
}

func (rs *ReportStore) GetAlertSummary(ctx context.Context) (*storage.AlertSummary, error) {
	return rs.store.GetAlertSummary(ctx)
}

func (rs *ReportStore) GetReport(ctx context.Context, id int64) (*storage.ReportDefinition, error) {
	return rs.store.GetReport(ctx, id)
}

func (rs *ReportStore) GetDueSchedules(ctx context.Context, before time.Time) ([]*storage.ReportSchedule, error) {
	return rs.store.GetDueSchedules(ctx, before)
}

func (rs *ReportStore) UpdateScheduleAfterRun(ctx context.Context, scheduleID int64, runID int64, nextRun time.Time, failed bool) error {
	return rs.store.UpdateScheduleAfterRun(ctx, scheduleID, runID, nextRun, failed)
}

func (rs *ReportStore) CreateReportRun(ctx context.Context, run *storage.ReportRun) error {
	return rs.store.CreateReportRun(ctx, run)
}

func (rs *ReportStore) UpdateReportRun(ctx context.Context, run *storage.ReportRun) error {
	return rs.store.UpdateReportRun(ctx, run)
}

// Reports use the principal's role and complete tenant set, never query-supplied
// ownership. Global built-ins are readable templates only; their jobs/results
// are administrator-only. Operators may manage only their own definitions.
func reportPrincipal(w http.ResponseWriter, r *http.Request, write bool) *Principal {
	p := getPrincipal(r)
	if p == nil || p.User == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	minimum := storage.RoleViewer
	if write {
		minimum = storage.RoleOperator
	}
	if !p.HasRole(minimum) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil
	}
	if _, ok := tenantScope(p); !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil
	}
	return p
}

func reportTenantsAllowed(p *Principal, ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if id == "" || strings.Contains(id, ",") || !p.CanAccessTenant(id) {
			return false
		}
	}
	return true
}

func reportVisible(p *Principal, report *storage.ReportDefinition, template bool) bool {
	if report == nil {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	if template && report.IsBuiltIn && report.Scope == storage.ReportScopeFleet && len(report.TenantIDs) == 0 {
		return true
	}
	switch report.Scope {
	case storage.ReportScopeTenant, storage.ReportScopeSite, storage.ReportScopeAgent, storage.ReportScopeDevice:
		return reportTenantsAllowed(p, report.TenantIDs)
	default:
		return false
	}
}

func reportForRequest(w http.ResponseWriter, r *http.Request, id int64, write, template bool) *storage.ReportDefinition {
	p := reportPrincipal(w, r, write)
	if p == nil {
		return nil
	}
	report, err := serverStore.GetReport(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed to load report", http.StatusInternalServerError)
		return nil
	}
	if !reportVisible(p, report, template) {
		http.NotFound(w, r)
		return nil
	}
	if write && !p.IsAdmin() && (report.IsBuiltIn || report.CreatedBy != p.User.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil
	}
	return report
}

func validateReportScope(w http.ResponseWriter, r *http.Request, report *storage.ReportDefinition) bool {
	p := getPrincipal(r)
	if !reportVisible(p, report, false) {
		http.Error(w, "Forbidden report scope", http.StatusForbidden)
		return false
	}
	// IDs are serialized as comma-separated strings. Reject ambiguous input
	// rather than silently expanding one tenant ID into several on retrieval.
	for _, ids := range [][]string{report.TenantIDs, report.SiteIDs, report.AgentIDs} {
		for _, id := range ids {
			if id == "" || strings.Contains(id, ",") {
				http.Error(w, "Invalid report scope ID", http.StatusBadRequest)
				return false
			}
		}
	}
	if p.IsAdmin() {
		return true
	}
	for _, id := range report.AgentIDs {
		agent, err := serverStore.GetAgent(r.Context(), id)
		if err != nil || agent == nil || !reportHasID(report.TenantIDs, agent.TenantID) {
			http.Error(w, "Forbidden report agent", http.StatusForbidden)
			return false
		}
	}
	for _, id := range report.SiteIDs {
		site, err := serverStore.GetSite(r.Context(), id)
		if err != nil || site == nil || !reportHasID(report.TenantIDs, site.TenantID) {
			http.Error(w, "Forbidden report site", http.StatusForbidden)
			return false
		}
	}
	return true
}

func reportHasID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func reportRunVisible(p *Principal, run *storage.ReportRun, report *storage.ReportDefinition) bool {
	if !reportVisible(p, report, false) {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	ids, verified := storage.ReportRunTenantScope(run)
	return verified && reportTenantsAllowed(p, ids)
}

func visibleReportRuns(ctx context.Context, p *Principal, filter storage.ReportRunFilter) ([]*storage.ReportRun, error) {
	limit, offset := filter.Limit, filter.Offset
	// Apply pagination to visible rows, but never materialize candidate bodies.
	// A stable storage order makes offset-based metadata batches deterministic.
	const batchSize = 100
	filter.Limit, filter.Offset, filter.MetadataOnly = batchSize, 0, true
	visible := make([]*storage.ReportRun, 0)
	parents := make(map[int64]*storage.ReportDefinition)
	for {
		runs, err := serverStore.ListReportRuns(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			report, loaded := parents[run.ReportID]
			if !loaded {
				report, err = serverStore.GetReport(ctx, run.ReportID)
				if err != nil {
					return nil, err
				}
				parents[run.ReportID] = report
			}
			if !reportRunVisible(p, run, report) {
				continue
			}
			if offset > 0 {
				offset--
				continue
			}
			visible = append(visible, run)
			if limit > 0 && len(visible) >= limit {
				return visible, nil
			}
		}
		if len(runs) < batchSize {
			return visible, nil
		}
		filter.Offset += len(runs)
	}
}

func visibleReportSchedules(ctx context.Context, p *Principal) ([]*storage.ReportSchedule, error) {
	schedules, err := serverStore.ListReportSchedules(ctx, 0)
	if err != nil {
		return nil, err
	}
	visible := make([]*storage.ReportSchedule, 0)
	for _, schedule := range schedules {
		report, err := serverStore.GetReport(ctx, schedule.ReportID)
		if err != nil {
			return nil, err
		}
		if reportVisible(p, report, false) {
			visible = append(visible, schedule)
		}
	}
	return visible, nil
}

// handleReports handles GET /api/v1/reports and POST /api/v1/reports
func handleReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := reportPrincipal(w, r, r.Method != http.MethodGet)
	if p == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List reports
		filter := storage.ReportFilter{}

		if t := r.URL.Query().Get("type"); t != "" {
			filter.Type = storage.ReportType(t)
		}
		if s := r.URL.Query().Get("scope"); s != "" {
			filter.Scope = storage.ReportScope(s)
		}
		if cb := r.URL.Query().Get("created_by"); cb != "" {
			filter.CreatedBy = cb
		}
		if b := r.URL.Query().Get("built_in"); b != "" {
			val := b == "true"
			filter.IsBuiltIn = &val
		}
		if l := r.URL.Query().Get("limit"); l != "" {
			if lv, err := strconv.Atoi(l); err == nil {
				filter.Limit = lv
			}
		}
		if o := r.URL.Query().Get("offset"); o != "" {
			if ov, err := strconv.Atoi(o); err == nil {
				filter.Offset = ov
			}
		}

		limit, offset := filter.Limit, filter.Offset
		filter.Limit, filter.Offset = 0, 0
		reports, err := serverStore.ListReports(ctx, filter)
		if err != nil {
			serverLogger.Error("Failed to list reports", "error", err)
			http.Error(w, fmt.Sprintf("list reports: %v", err), http.StatusInternalServerError)
			return
		}

		visible := make([]*storage.ReportDefinition, 0)
		for _, report := range reports {
			if reportVisible(p, report, true) {
				visible = append(visible, report)
			}
		}
		if offset > len(visible) {
			offset = len(visible)
		}
		if offset > 0 {
			visible = visible[offset:]
		}
		if limit > 0 && len(visible) > limit {
			visible = visible[:limit]
		}
		reports = visible
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"reports": reports,
			"count":   len(reports),
		})

	case http.MethodPost:
		// Create report
		var report storage.ReportDefinition
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}

		report.ID = 0
		report.IsBuiltIn = false
		report.CreatedBy = p.User.Username
		if !validateReportScope(w, r, &report) {
			return
		}

		if err := serverStore.CreateReport(ctx, &report); err != nil {
			serverLogger.Error("Failed to create report", "name", report.Name, "type", report.Type, "error", err)
			http.Error(w, fmt.Sprintf("create report: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(report)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReport handles GET/PUT/DELETE /api/v1/reports/{id}
func handleReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract ID from path
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/reports/")
	// Handle sub-paths
	if idx := strings.Index(idStr, "/"); idx >= 0 {
		subPath := idStr[idx:]
		idStr = idStr[:idx]

		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid report ID", http.StatusBadRequest)
			return
		}

		switch subPath {
		case "/run":
			handleReportRun(w, r, id)
		case "/schedules":
			handleReportSchedules(w, r, id)
		case "/runs":
			handleReportRuns(w, r, id)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid report ID", http.StatusBadRequest)
		return
	}
	existing := reportForRequest(w, r, id, r.Method != http.MethodGet, r.Method == http.MethodGet)
	if existing == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(existing)

	case http.MethodPut:
		var report storage.ReportDefinition
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		report.ID = id
		report.CreatedBy, report.IsBuiltIn, report.CreatedAt = existing.CreatedBy, existing.IsBuiltIn, existing.CreatedAt
		if report.Scope != existing.Scope || strings.Join(report.TenantIDs, ",") != strings.Join(existing.TenantIDs, ",") {
			http.Error(w, "Report tenant scope is immutable; create a new report", http.StatusBadRequest)
			return
		}
		if !validateReportScope(w, r, &report) {
			return
		}

		if err := serverStore.UpdateReport(ctx, &report); err != nil {
			serverLogger.Error("Failed to update report", "report_id", report.ID, "error", err)
			http.Error(w, fmt.Sprintf("update report: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(report)

	case http.MethodDelete:
		if err := serverStore.DeleteReport(ctx, id); err != nil {
			serverLogger.Error("Failed to delete report", "report_id", id, "error", err)
			http.Error(w, fmt.Sprintf("delete report: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReportRun handles POST /api/v1/reports/{id}/run
func handleReportRun(w http.ResponseWriter, r *http.Request, reportID int64) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if reportForRequest(w, r, reportID, true, false) == nil {
		return
	}

	// Get username from session
	username := "api"
	if principal := getPrincipal(r); principal != nil {
		username = principal.User.Username
	}

	// Create report store wrapper
	reportStore := &ReportStore{store: serverStore}
	scheduler := reports.NewScheduler(reportStore, serverLogger)

	run, err := scheduler.RunNow(ctx, reportID, username)
	if err != nil {
		serverLogger.Error("Report run failed", "report_id", reportID, "username", username, "error", err)
		http.Error(w, fmt.Sprintf("run report: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run)
}

// handleReportRuns handles GET /api/v1/reports/{id}/runs
func handleReportRuns(w http.ResponseWriter, r *http.Request, reportID int64) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if reportForRequest(w, r, reportID, false, false) == nil {
		return
	}

	filter := storage.ReportRunFilter{
		ReportID: reportID,
		Limit:    50,
	}

	if s := r.URL.Query().Get("status"); s != "" {
		filter.Status = storage.ReportStatus(s)
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if lv, err := strconv.Atoi(l); err == nil {
			filter.Limit = lv
		}
	}

	runs, err := visibleReportRuns(ctx, getPrincipal(r), filter)
	if err != nil {
		serverLogger.Error("Failed to list report runs", "report_id", reportID, "error", err)
		http.Error(w, fmt.Sprintf("list runs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"runs":  runs,
		"count": len(runs),
	})
}

// handleReportRunsCollection handles GET /api/v1/report-runs (list all runs)
func handleReportRunsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := reportPrincipal(w, r, false)
	if p == nil {
		return
	}

	filter := storage.ReportRunFilter{
		Limit: 50,
	}

	if s := r.URL.Query().Get("status"); s != "" {
		filter.Status = storage.ReportStatus(s)
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if lv, err := strconv.Atoi(l); err == nil {
			filter.Limit = lv
		}
	}
	if rid := r.URL.Query().Get("report_id"); rid != "" {
		rv, err := strconv.ParseInt(rid, 10, 64)
		if err != nil || rv <= 0 {
			http.Error(w, "Invalid report ID", http.StatusBadRequest)
			return
		}
		if reportForRequest(w, r, rv, false, false) == nil {
			return
		}
		filter.ReportID = rv
	}

	runs, err := visibleReportRuns(ctx, p, filter)
	if err != nil {
		serverLogger.Error("Failed to list all report runs", "error", err)
		http.Error(w, fmt.Sprintf("list runs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"runs":  runs,
		"count": len(runs),
	})
}

// handleReportSchedules handles GET/POST /api/v1/reports/{id}/schedules
func handleReportSchedules(w http.ResponseWriter, r *http.Request, reportID int64) {
	ctx := r.Context()
	if reportForRequest(w, r, reportID, r.Method != http.MethodGet, false) == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		schedules, err := serverStore.ListReportSchedules(ctx, reportID)
		if err != nil {
			serverLogger.Error("Failed to list report schedules", "report_id", reportID, "error", err)
			http.Error(w, fmt.Sprintf("list schedules: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"schedules": schedules,
			"count":     len(schedules),
		})

	case http.MethodPost:
		var schedule storage.ReportSchedule
		if err := json.NewDecoder(r.Body).Decode(&schedule); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		schedule.ReportID = reportID

		// Calculate initial next_run_at if not set
		if schedule.NextRunAt.IsZero() {
			schedule.NextRunAt = calculateInitialNextRun(&schedule)
		}

		if err := serverStore.CreateReportSchedule(ctx, &schedule); err != nil {
			serverLogger.Error("Failed to create report schedule", "report_id", reportID, "error", err)
			http.Error(w, fmt.Sprintf("create schedule: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(schedule)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReportSchedulesCollection handles GET /api/v1/report-schedules (list all schedules)
func handleReportSchedulesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p := reportPrincipal(w, r, false)
	if p == nil {
		return
	}
	schedules, err := visibleReportSchedules(ctx, p)
	if err != nil {
		serverLogger.Error("Failed to list all report schedules", "error", err)
		http.Error(w, fmt.Sprintf("list schedules: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"schedules": schedules,
		"count":     len(schedules),
	})
}

// handleSchedule handles GET/PUT/DELETE /api/v1/report-schedules/{id}
func handleSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if reportPrincipal(w, r, r.Method != http.MethodGet) == nil {
		return
	}

	// Extract ID
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/report-schedules/")
	id, err := strconv.ParseInt(idStr, 10, 64)
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
	if reportForRequest(w, r, existing.ReportID, r.Method != http.MethodGet, false) == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(existing)

	case http.MethodPut:
		var schedule storage.ReportSchedule
		if err := json.NewDecoder(r.Body).Decode(&schedule); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		schedule.ID = id
		if schedule.ReportID != 0 && schedule.ReportID != existing.ReportID {
			http.Error(w, "Schedule report ownership is immutable", http.StatusBadRequest)
			return
		}
		schedule.ReportID = existing.ReportID

		// Recalculate next run if schedule changed
		if schedule.NextRunAt.IsZero() {
			schedule.NextRunAt = calculateInitialNextRun(&schedule)
		}

		if err := serverStore.UpdateReportSchedule(ctx, &schedule); err != nil {
			serverLogger.Error("Failed to update report schedule", "schedule_id", id, "error", err)
			http.Error(w, fmt.Sprintf("update schedule: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(schedule)

	case http.MethodDelete:
		if err := serverStore.DeleteReportSchedule(ctx, id); err != nil {
			serverLogger.Error("Failed to delete report schedule", "schedule_id", id, "error", err)
			http.Error(w, fmt.Sprintf("delete schedule: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReportRunResult handles GET /api/v1/report-runs/{id}
func handleReportRunResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := reportPrincipal(w, r, false)
	if p == nil {
		return
	}

	// Extract ID
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/report-runs/")
	// Handle /download suffix
	download := false
	if strings.HasSuffix(idStr, "/download") {
		download = true
		idStr = strings.TrimSuffix(idStr, "/download")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid run ID", http.StatusBadRequest)
		return
	}

	run, err := serverStore.GetReportRunMetadata(ctx, id)
	if err != nil {
		serverLogger.Error("Failed to get report run", "run_id", id, "error", err)
		http.Error(w, fmt.Sprintf("get run: %v", err), http.StatusInternalServerError)
		return
	}
	if run == nil {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}
	report, err := serverStore.GetReport(ctx, run.ReportID)
	if err != nil {
		http.Error(w, "Failed to load report", http.StatusInternalServerError)
		return
	}
	if !reportRunVisible(p, run, report) {
		http.NotFound(w, r)
		return
	}

	// Load the body only after checking both current and execution ownership.
	run, err = serverStore.GetReportRun(ctx, id)
	if err != nil {
		serverLogger.Error("Failed to get report run", "run_id", id, "error", err)
		http.Error(w, fmt.Sprintf("get run: %v", err), http.StatusInternalServerError)
		return
	}
	if run == nil || !reportRunVisible(p, run, report) {
		http.NotFound(w, r)
		return
	}

	if download && run.ResultData != "" {
		// Serve the result as a file download
		var contentType, ext string
		switch run.Format {
		case storage.ReportFormatJSON:
			contentType = "application/json"
			ext = "json"
		case storage.ReportFormatCSV:
			contentType = "text/csv"
			ext = "csv"
		case storage.ReportFormatHTML:
			contentType = "text/html"
			ext = "html"
		default:
			contentType = "application/octet-stream"
			ext = "txt"
		}

		filename := fmt.Sprintf("report_%d_%s.%s", run.ReportID, run.StartedAt.Format("20060102_150405"), ext)

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Write([]byte(run.ResultData))
		return
	}

	// Return run metadata
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run)
}

// handleReportTypes handles GET /api/v1/reports/types
func handleReportTypes(w http.ResponseWriter, r *http.Request) {
	if reportPrincipal(w, r, false) == nil {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	types := storage.GetBuiltInReportTypes()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"types": types,
		"count": len(types),
	})
}

// handleReportSummary handles GET /api/v1/reports/summary
func handleReportSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := reportPrincipal(w, r, false)
	if p == nil {
		return
	}

	summary, err := scopedReportSummary(ctx, p)
	if err != nil {
		serverLogger.Error("Failed to get report summary", "error", err)
		http.Error(w, fmt.Sprintf("get summary: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func scopedReportSummary(ctx context.Context, p *Principal) (*storage.ReportSummary, error) {
	if p.IsAdmin() {
		return serverStore.GetReportSummary(ctx)
	}
	summary := &storage.ReportSummary{ReportsByType: make(map[string]int)}
	definitions, err := serverStore.ListReports(ctx, storage.ReportFilter{})
	if err != nil {
		return nil, err
	}
	for _, report := range definitions {
		if reportVisible(p, report, true) {
			summary.TotalReports++
			summary.ReportsByType[report.Type]++
			if report.IsBuiltIn {
				summary.BuiltInReports++
			} else {
				summary.CustomReports++
			}
		}
	}
	schedules, err := visibleReportSchedules(ctx, p)
	if err != nil {
		return nil, err
	}
	summary.TotalSchedules = len(schedules)
	for _, schedule := range schedules {
		if schedule.Enabled {
			summary.ActiveSchedules++
		}
	}
	runs, err := visibleReportRuns(ctx, p, storage.ReportRunFilter{})
	if err != nil {
		return nil, err
	}
	summary.TotalRuns = len(runs)
	since := time.Now().Add(-24 * time.Hour)
	var duration int64
	var timedRuns int64
	for _, run := range runs {
		if !run.StartedAt.Before(since) {
			summary.RunsLast24h++
			if run.Status == storage.ReportStatusFailed {
				summary.FailedRunsLast24h++
			}
		}
		if run.Status == storage.ReportStatusCompleted {
			summary.SuccessfulRuns++
			if run.DurationMS > 0 {
				duration += run.DurationMS
				timedRuns++
			}
		}
		summary.StorageUsedBytes += run.ResultSize
	}
	if timedRuns > 0 {
		summary.AverageRunTimeMS = duration / timedRuns
	}
	return summary, nil
}

// calculateInitialNextRun calculates the first run time for a new schedule.
func calculateInitialNextRun(schedule *storage.ReportSchedule) time.Time {
	now := time.Now()

	var hour, min int
	fmt.Sscanf(schedule.TimeOfDay, "%d:%d", &hour, &min)

	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	nowLocal := now.In(loc)

	switch schedule.Frequency {
	case storage.ScheduleFrequencyDaily:
		next := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), hour, min, 0, 0, loc)
		if next.Before(now) {
			next = next.Add(24 * time.Hour)
		}
		return next.UTC()

	case storage.ScheduleFrequencyWeekly:
		next := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), hour, min, 0, 0, loc)
		daysUntil := (schedule.DayOfWeek - int(next.Weekday()) + 7) % 7
		if daysUntil == 0 && next.Before(now) {
			daysUntil = 7
		}
		next = next.Add(time.Duration(daysUntil) * 24 * time.Hour)
		return next.UTC()

	case storage.ScheduleFrequencyMonthly:
		next := time.Date(nowLocal.Year(), nowLocal.Month(), schedule.DayOfMonth, hour, min, 0, 0, loc)
		if next.Before(now) {
			next = next.AddDate(0, 1, 0)
		}
		return next.UTC()
	}

	return now.Add(24 * time.Hour)
}
