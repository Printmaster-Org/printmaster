package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// Report Storage Methods (BaseStore)
// ============================================================

// CreateReport creates a new report definition.
func (s *BaseStore) CreateReport(ctx context.Context, report *ReportDefinition) error {
	tenantIDs := strings.Join(report.TenantIDs, ",")
	siteIDs := strings.Join(report.SiteIDs, ",")
	agentIDs := strings.Join(report.AgentIDs, ",")
	columns := strings.Join(report.Columns, ",")
	groupBy := strings.Join(report.GroupBy, ",")
	emailRecipients := strings.Join(report.EmailRecipients, ",")

	id, err := s.insertReturningID(ctx, `
		INSERT INTO reports (
			name, description, type, format, scope,
			tenant_ids, site_ids, agent_ids, device_filter,
			time_range_type, time_range_days, time_range_start, time_range_end,
			options_json, columns, group_by, order_by, report_limit,
			email_recipients, webhook_url, created_by, is_built_in
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		report.Name, report.Description, report.Type, report.Format, report.Scope,
		tenantIDs, siteIDs, agentIDs, report.DeviceFilter,
		report.TimeRangeType, report.TimeRangeDays, report.TimeRangeStart, report.TimeRangeEnd,
		report.OptionsJSON, columns, groupBy, report.OrderBy, report.Limit,
		emailRecipients, report.WebhookURL, report.CreatedBy, report.IsBuiltIn,
	)
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}

	report.ID = id
	report.CreatedAt = time.Now()
	report.UpdatedAt = report.CreatedAt
	return nil
}

// UpdateReport updates an existing report definition.
func (s *BaseStore) UpdateReport(ctx context.Context, report *ReportDefinition) error {
	existing, err := s.GetReport(ctx, report.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("report not found")
	}
	// Ownership is immutable: old results must never be reclassified by editing
	// their parent definition. Clone the definition to change its tenant scope.
	if existing.Scope != report.Scope || strings.Join(existing.TenantIDs, ",") != strings.Join(report.TenantIDs, ",") {
		return fmt.Errorf("report tenant scope is immutable; create a new report")
	}
	tenantIDs := strings.Join(report.TenantIDs, ",")
	siteIDs := strings.Join(report.SiteIDs, ",")
	agentIDs := strings.Join(report.AgentIDs, ",")
	columns := strings.Join(report.Columns, ",")
	groupBy := strings.Join(report.GroupBy, ",")
	emailRecipients := strings.Join(report.EmailRecipients, ",")

	_, err = s.execContext(ctx, `
		UPDATE reports SET
			name = ?, description = ?, type = ?, format = ?, scope = ?,
			tenant_ids = ?, site_ids = ?, agent_ids = ?, device_filter = ?,
			time_range_type = ?, time_range_days = ?, time_range_start = ?, time_range_end = ?,
			options_json = ?, columns = ?, group_by = ?, order_by = ?, report_limit = ?,
			email_recipients = ?, webhook_url = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`,
		report.Name, report.Description, report.Type, report.Format, report.Scope,
		tenantIDs, siteIDs, agentIDs, report.DeviceFilter,
		report.TimeRangeType, report.TimeRangeDays, report.TimeRangeStart, report.TimeRangeEnd,
		report.OptionsJSON, columns, groupBy, report.OrderBy, report.Limit,
		emailRecipients, report.WebhookURL, report.ID,
	)
	if err != nil {
		return fmt.Errorf("update report: %w", err)
	}
	report.UpdatedAt = time.Now()
	return nil
}

// GetReport retrieves a report by ID.
func (s *BaseStore) GetReport(ctx context.Context, id int64) (*ReportDefinition, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, name, description, type, format, scope,
			tenant_ids, site_ids, agent_ids, device_filter,
			time_range_type, time_range_days, time_range_start, time_range_end,
			options_json, columns, group_by, order_by, report_limit,
			email_recipients, webhook_url, created_by, is_built_in,
			created_at, updated_at
		FROM reports WHERE id = ?
	`, id)
	return s.scanReport(row)
}

// DeleteReport deletes a report and its schedules/runs.
func (s *BaseStore) DeleteReport(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM reports WHERE id = ?`, id)
	return err
}

// ListReports lists reports matching the filter.
func (s *BaseStore) ListReports(ctx context.Context, filter ReportFilter) ([]*ReportDefinition, error) {
	if filter.IsBuiltIn == nil {
		filter.IsBuiltIn = filter.BuiltIn
	}
	query := `
		SELECT id, name, description, type, format, scope,
			tenant_ids, site_ids, agent_ids, device_filter,
			time_range_type, time_range_days, time_range_start, time_range_end,
			options_json, columns, group_by, order_by, report_limit,
			email_recipients, webhook_url, created_by, is_built_in,
			created_at, updated_at
		FROM reports WHERE 1=1
	`
	var args []interface{}

	if filter.Type != "" {
		query += " AND type = ?"
		args = append(args, filter.Type)
	}
	if filter.Scope != "" {
		query += " AND scope = ?"
		args = append(args, filter.Scope)
	}
	if filter.CreatedBy != "" {
		query += " AND created_by = ?"
		args = append(args, filter.CreatedBy)
	}
	if filter.IsBuiltIn != nil {
		query += " AND is_built_in = ?"
		args = append(args, *filter.IsBuiltIn)
	}

	query += " ORDER BY name"
	if filter.Limit > 0 && filter.TenantID == "" {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()

	reports := make([]*ReportDefinition, 0)
	matched := 0
	for rows.Next() {
		r, err := s.scanReportRow(rows)
		if err != nil {
			return nil, err
		}
		if filter.TenantID != "" {
			// Decode the existing comma-separated representation rather than using
			// SQL LIKE (substring/wildcard matching). Empty scopes are not tenants.
			found := false
			for _, id := range r.TenantIDs {
				if id == filter.TenantID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			matched++
			if matched <= filter.Offset || (filter.Limit > 0 && len(reports) >= filter.Limit) {
				continue
			}
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

// scanReport scans a single report row.
func (s *BaseStore) scanReport(row *sql.Row) (*ReportDefinition, error) {
	var r ReportDefinition
	var tenantIDs, siteIDs, agentIDs, columns, groupBy, emailRecipients sql.NullString
	var description, deviceFilter, timeRangeType, timeRangeStart, timeRangeEnd sql.NullString
	var optionsJSON, orderBy, webhookURL, createdBy sql.NullString
	var timeRangeDays, limit sql.NullInt64
	var isBuiltIn interface{}

	err := row.Scan(
		&r.ID, &r.Name, &description, &r.Type, &r.Format, &r.Scope,
		&tenantIDs, &siteIDs, &agentIDs, &deviceFilter,
		&timeRangeType, &timeRangeDays, &timeRangeStart, &timeRangeEnd,
		&optionsJSON, &columns, &groupBy, &orderBy, &limit,
		&emailRecipients, &webhookURL, &createdBy, &isBuiltIn,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan report: %w", err)
	}

	r.Description = description.String
	r.DeviceFilter = deviceFilter.String
	r.TimeRangeType = timeRangeType.String
	r.TimeRangeDays = int(timeRangeDays.Int64)
	r.TimeRangeStart = timeRangeStart.String
	r.TimeRangeEnd = timeRangeEnd.String
	r.OptionsJSON = optionsJSON.String
	r.OrderBy = orderBy.String
	r.Limit = int(limit.Int64)
	r.WebhookURL = webhookURL.String
	r.CreatedBy = createdBy.String
	r.IsBuiltIn = intToBool(isBuiltIn)

	if tenantIDs.String != "" {
		r.TenantIDs = strings.Split(tenantIDs.String, ",")
	}
	if siteIDs.String != "" {
		r.SiteIDs = strings.Split(siteIDs.String, ",")
	}
	if agentIDs.String != "" {
		r.AgentIDs = strings.Split(agentIDs.String, ",")
	}
	if columns.String != "" {
		r.Columns = strings.Split(columns.String, ",")
	}
	if groupBy.String != "" {
		r.GroupBy = strings.Split(groupBy.String, ",")
	}
	if emailRecipients.String != "" {
		r.EmailRecipients = strings.Split(emailRecipients.String, ",")
	}

	return &r, nil
}

// scanReportRow scans from rows.
func (s *BaseStore) scanReportRow(rows *sql.Rows) (*ReportDefinition, error) {
	var r ReportDefinition
	var tenantIDs, siteIDs, agentIDs, columns, groupBy, emailRecipients sql.NullString
	var description, deviceFilter, timeRangeType, timeRangeStart, timeRangeEnd sql.NullString
	var optionsJSON, orderBy, webhookURL, createdBy sql.NullString
	var timeRangeDays, limit sql.NullInt64
	var isBuiltIn interface{}

	err := rows.Scan(
		&r.ID, &r.Name, &description, &r.Type, &r.Format, &r.Scope,
		&tenantIDs, &siteIDs, &agentIDs, &deviceFilter,
		&timeRangeType, &timeRangeDays, &timeRangeStart, &timeRangeEnd,
		&optionsJSON, &columns, &groupBy, &orderBy, &limit,
		&emailRecipients, &webhookURL, &createdBy, &isBuiltIn,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan report row: %w", err)
	}

	r.Description = description.String
	r.DeviceFilter = deviceFilter.String
	r.TimeRangeType = timeRangeType.String
	r.TimeRangeDays = int(timeRangeDays.Int64)
	r.TimeRangeStart = timeRangeStart.String
	r.TimeRangeEnd = timeRangeEnd.String
	r.OptionsJSON = optionsJSON.String
	r.OrderBy = orderBy.String
	r.Limit = int(limit.Int64)
	r.WebhookURL = webhookURL.String
	r.CreatedBy = createdBy.String
	r.IsBuiltIn = intToBool(isBuiltIn)

	if tenantIDs.String != "" {
		r.TenantIDs = strings.Split(tenantIDs.String, ",")
	}
	if siteIDs.String != "" {
		r.SiteIDs = strings.Split(siteIDs.String, ",")
	}
	if agentIDs.String != "" {
		r.AgentIDs = strings.Split(agentIDs.String, ",")
	}
	if columns.String != "" {
		r.Columns = strings.Split(columns.String, ",")
	}
	if groupBy.String != "" {
		r.GroupBy = strings.Split(groupBy.String, ",")
	}
	if emailRecipients.String != "" {
		r.EmailRecipients = strings.Split(emailRecipients.String, ",")
	}

	return &r, nil
}

// ============================================================
// Report Schedule Storage Methods (BaseStore)
// ============================================================

// CreateReportSchedule creates a new schedule.
func (s *BaseStore) CreateReportSchedule(ctx context.Context, schedule *ReportSchedule) error {
	id, err := s.insertReturningID(ctx, `
		INSERT INTO report_schedules (
			report_id, name, enabled, frequency,
			day_of_week, day_of_month, time_of_day, timezone,
			next_run_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		schedule.ReportID, schedule.Name, schedule.Enabled, schedule.Frequency,
		schedule.DayOfWeek, schedule.DayOfMonth, schedule.TimeOfDay, schedule.Timezone,
		schedule.NextRunAt,
	)
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}

	schedule.ID = id
	schedule.CreatedAt = time.Now()
	schedule.UpdatedAt = schedule.CreatedAt
	return nil
}

// UpdateReportSchedule updates a schedule.
func (s *BaseStore) UpdateReportSchedule(ctx context.Context, schedule *ReportSchedule) error {
	_, err := s.execContext(ctx, `
		UPDATE report_schedules SET
			name = ?, enabled = ?, frequency = ?,
			day_of_week = ?, day_of_month = ?, time_of_day = ?, timezone = ?,
			next_run_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`,
		schedule.Name, schedule.Enabled, schedule.Frequency,
		schedule.DayOfWeek, schedule.DayOfMonth, schedule.TimeOfDay, schedule.Timezone,
		schedule.NextRunAt, schedule.ID,
	)
	return err
}

// GetReportSchedule retrieves a schedule by ID.
func (s *BaseStore) GetReportSchedule(ctx context.Context, id int64) (*ReportSchedule, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, report_id, name, enabled, frequency,
			day_of_week, day_of_month, time_of_day, timezone,
			next_run_at, last_run_at, last_run_id, failure_count,
			created_at, updated_at
		FROM report_schedules WHERE id = ?
	`, id)
	return s.scanSchedule(row)
}

// DeleteReportSchedule deletes a schedule.
func (s *BaseStore) DeleteReportSchedule(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM report_schedules WHERE id = ?`, id)
	return err
}

// ListReportSchedules lists schedules for a report. If reportID is 0, lists all schedules.
func (s *BaseStore) ListReportSchedules(ctx context.Context, reportID int64) ([]*ReportSchedule, error) {
	var rows *sql.Rows
	var err error

	if reportID == 0 {
		// List all schedules
		rows, err = s.queryContext(ctx, `
			SELECT id, report_id, name, enabled, frequency,
				day_of_week, day_of_month, time_of_day, timezone,
				next_run_at, last_run_at, last_run_id, failure_count,
				created_at, updated_at
			FROM report_schedules
			ORDER BY name
		`)
	} else {
		// List schedules for specific report
		rows, err = s.queryContext(ctx, `
			SELECT id, report_id, name, enabled, frequency,
				day_of_week, day_of_month, time_of_day, timezone,
				next_run_at, last_run_at, last_run_id, failure_count,
				created_at, updated_at
			FROM report_schedules WHERE report_id = ?
			ORDER BY name
		`, reportID)
	}
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*ReportSchedule
	for rows.Next() {
		sched, err := s.scanScheduleRow(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sched)
	}
	return schedules, rows.Err()
}

// GetDueSchedules returns schedules that need to run.
func (s *BaseStore) GetDueSchedules(ctx context.Context, before time.Time) ([]*ReportSchedule, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, report_id, name, enabled, frequency,
			day_of_week, day_of_month, time_of_day, timezone,
			next_run_at, last_run_at, last_run_id, failure_count,
			created_at, updated_at
		FROM report_schedules 
		WHERE enabled = ? AND next_run_at <= ?
		ORDER BY next_run_at
	`, true, before)
	if err != nil {
		return nil, fmt.Errorf("get due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*ReportSchedule
	for rows.Next() {
		sched, err := s.scanScheduleRow(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sched)
	}
	return schedules, rows.Err()
}

// UpdateScheduleAfterRun updates schedule after a run completes.
func (s *BaseStore) UpdateScheduleAfterRun(ctx context.Context, scheduleID int64, runID int64, nextRun time.Time, failed bool) error {
	_, err := s.execContext(ctx, `
		UPDATE report_schedules SET
			last_run_at = CURRENT_TIMESTAMP,
			last_run_id = ?,
			next_run_at = ?,
			failure_count = CASE WHEN ? THEN failure_count + 1 ELSE 0 END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, runID, nextRun, failed, scheduleID)
	return err
}

func (s *BaseStore) scanSchedule(row *sql.Row) (*ReportSchedule, error) {
	var sched ReportSchedule
	var dayOfWeek, dayOfMonth sql.NullInt64
	var lastRunAt sql.NullTime
	var lastRunID sql.NullInt64

	err := row.Scan(
		&sched.ID, &sched.ReportID, &sched.Name, &sched.Enabled, &sched.Frequency,
		&dayOfWeek, &dayOfMonth, &sched.TimeOfDay, &sched.Timezone,
		&sched.NextRunAt, &lastRunAt, &lastRunID, &sched.FailureCount,
		&sched.CreatedAt, &sched.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan schedule: %w", err)
	}

	sched.DayOfWeek = int(dayOfWeek.Int64)
	sched.DayOfMonth = int(dayOfMonth.Int64)
	if lastRunAt.Valid {
		sched.LastRunAt = &lastRunAt.Time
	}
	if lastRunID.Valid {
		sched.LastRunID = &lastRunID.Int64
	}

	return &sched, nil
}

func (s *BaseStore) scanScheduleRow(rows *sql.Rows) (*ReportSchedule, error) {
	var sched ReportSchedule
	var dayOfWeek, dayOfMonth sql.NullInt64
	var lastRunAt sql.NullTime
	var lastRunID sql.NullInt64

	err := rows.Scan(
		&sched.ID, &sched.ReportID, &sched.Name, &sched.Enabled, &sched.Frequency,
		&dayOfWeek, &dayOfMonth, &sched.TimeOfDay, &sched.Timezone,
		&sched.NextRunAt, &lastRunAt, &lastRunID, &sched.FailureCount,
		&sched.CreatedAt, &sched.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan schedule row: %w", err)
	}

	sched.DayOfWeek = int(dayOfWeek.Int64)
	sched.DayOfMonth = int(dayOfMonth.Int64)
	if lastRunAt.Valid {
		sched.LastRunAt = &lastRunAt.Time
	}
	if lastRunID.Valid {
		sched.LastRunID = &lastRunID.Int64
	}

	return &sched, nil
}

// ============================================================
// Report Run Storage Methods (BaseStore)
// ============================================================

// CreateReportRun creates a new report run.
func (s *BaseStore) CreateReportRun(ctx context.Context, run *ReportRun) error {
	report, err := s.GetReport(ctx, run.ReportID)
	if err != nil {
		return err
	}
	if report == nil {
		return fmt.Errorf("report not found")
	}
	if run.ScheduleID != nil {
		schedule, err := s.GetReportSchedule(ctx, *run.ScheduleID)
		if err != nil {
			return err
		}
		if schedule == nil || schedule.ReportID != run.ReportID {
			return fmt.Errorf("schedule does not belong to report")
		}
	}
	// Persist an immutable execution ownership snapshot. Legacy runs without
	// this marker cannot prove tenant isolation and remain administrator-only.
	var params map[string]json.RawMessage
	if run.ParametersJSON != "" {
		if err := json.Unmarshal([]byte(run.ParametersJSON), &params); err != nil {
			return fmt.Errorf("invalid run parameters: %w", err)
		}
	}
	if params == nil {
		params = make(map[string]json.RawMessage)
	}
	scope, _ := json.Marshal(reportRunScope{Version: 1, Scope: report.Scope, TenantIDs: report.TenantIDs})
	params["report_security"] = scope
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	run.ParametersJSON = string(data)
	id, err := s.insertReturningID(ctx, `
		INSERT INTO report_runs (
			report_id, schedule_id, status, format,
			started_at, parameters_json, run_by
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		run.ReportID, run.ScheduleID, run.Status, run.Format,
		run.StartedAt, run.ParametersJSON, run.RunBy,
	)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}

	run.ID = id
	run.CreatedAt = time.Now()
	return nil
}

type reportRunScope struct {
	Version   int      `json:"version"`
	Scope     string   `json:"scope"`
	TenantIDs []string `json:"tenant_ids"`
}

// ReportRunTenantScope returns verified execution ownership, not the mutable
// parent report's current scope. Global and pre-security runs fail closed.
func ReportRunTenantScope(run *ReportRun) ([]string, bool) {
	var params struct {
		Security reportRunScope `json:"report_security"`
	}
	if run == nil || json.Unmarshal([]byte(run.ParametersJSON), &params) != nil {
		return nil, false
	}
	s := params.Security
	if s.Version != 1 || len(s.TenantIDs) == 0 {
		return nil, false
	}
	switch s.Scope {
	case ReportScopeTenant, ReportScopeSite, ReportScopeAgent, ReportScopeDevice:
	default:
		return nil, false
	}
	for _, id := range s.TenantIDs {
		if id == "" || strings.Contains(id, ",") {
			return nil, false
		}
	}
	return s.TenantIDs, true
}

// UpdateReportRun updates a run (typically to set completion status).
func (s *BaseStore) UpdateReportRun(ctx context.Context, run *ReportRun) error {
	_, err := s.execContext(ctx, `
		UPDATE report_runs SET
			status = ?, completed_at = ?, duration_ms = ?,
			row_count = ?, result_size_bytes = ?, result_path = ?, result_data = ?,
			error_message = ?
		WHERE id = ?
	`,
		run.Status, run.CompletedAt, run.DurationMS,
		run.RowCount, run.ResultSize, run.ResultPath, run.ResultData,
		run.ErrorMessage, run.ID,
	)
	return err
}

// GetReportRun retrieves a run by ID.
func (s *BaseStore) GetReportRun(ctx context.Context, id int64) (*ReportRun, error) {
	return s.getReportRun(ctx, id, false)
}

// GetReportRunMetadata retrieves execution ownership without loading its result.
func (s *BaseStore) GetReportRunMetadata(ctx context.Context, id int64) (*ReportRun, error) {
	return s.getReportRun(ctx, id, true)
}

func (s *BaseStore) getReportRun(ctx context.Context, id int64, metadataOnly bool) (*ReportRun, error) {
	resultColumn := "result_data"
	if metadataOnly {
		resultColumn = "NULL"
	}
	row := s.queryRowContext(ctx, fmt.Sprintf(`
		SELECT id, report_id, schedule_id, status, format,
			started_at, completed_at, duration_ms,
			parameters_json, row_count, result_size_bytes, result_path, %s,
			error_message, run_by, created_at
		FROM report_runs WHERE id = ?
	`, resultColumn), id)
	return s.scanRun(row)
}

// DeleteReportRun deletes a run.
func (s *BaseStore) DeleteReportRun(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM report_runs WHERE id = ?`, id)
	return err
}

// ListReportRuns lists runs matching the filter.
func (s *BaseStore) ListReportRuns(ctx context.Context, filter ReportRunFilter) ([]*ReportRun, error) {
	resultColumn := "rr.result_data"
	if filter.MetadataOnly {
		resultColumn = "NULL"
	}
	query := fmt.Sprintf(`
		SELECT rr.id, rr.report_id, r.name, r.type, rr.schedule_id, rr.status, rr.format,
			rr.started_at, rr.completed_at, rr.duration_ms,
			rr.parameters_json, rr.row_count, rr.result_size_bytes, rr.result_path, %s,
			rr.error_message, rr.run_by, rr.created_at
		FROM report_runs rr
		LEFT JOIN reports r ON rr.report_id = r.id
		WHERE 1=1
	`, resultColumn)
	var args []interface{}

	if filter.ReportID > 0 {
		query += " AND rr.report_id = ?"
		args = append(args, filter.ReportID)
	}
	if filter.ScheduleID != nil {
		query += " AND rr.schedule_id = ?"
		args = append(args, *filter.ScheduleID)
	}
	if filter.Status != "" {
		query += " AND rr.status = ?"
		args = append(args, filter.Status)
	}
	if filter.Since != nil && !filter.Since.IsZero() {
		query += " AND rr.started_at >= ?"
		args = append(args, *filter.Since)
	}

	query += " ORDER BY rr.started_at DESC, rr.id DESC"
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []*ReportRun
	for rows.Next() {
		r, err := s.scanRunRow(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *BaseStore) scanRun(row *sql.Row) (*ReportRun, error) {
	var run ReportRun
	var scheduleID sql.NullInt64
	var completedAt sql.NullTime
	var durationMS, rowCount, resultSize sql.NullInt64
	var parametersJSON, resultPath, resultData, errorMessage, runBy sql.NullString

	err := row.Scan(
		&run.ID, &run.ReportID, &scheduleID, &run.Status, &run.Format,
		&run.StartedAt, &completedAt, &durationMS,
		&parametersJSON, &rowCount, &resultSize, &resultPath, &resultData,
		&errorMessage, &runBy, &run.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}

	if scheduleID.Valid {
		run.ScheduleID = &scheduleID.Int64
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	run.DurationMS = durationMS.Int64
	run.RowCount = int(rowCount.Int64)
	run.ResultSize = resultSize.Int64
	run.ParametersJSON = parametersJSON.String
	run.ResultPath = resultPath.String
	run.ResultData = resultData.String
	run.ErrorMessage = errorMessage.String
	run.RunBy = runBy.String

	return &run, nil
}

func (s *BaseStore) scanRunRow(rows *sql.Rows) (*ReportRun, error) {
	var run ReportRun
	var reportName, reportType sql.NullString
	var scheduleID sql.NullInt64
	var completedAt sql.NullTime
	var durationMS, rowCount, resultSize sql.NullInt64
	var parametersJSON, resultPath, resultData, errorMessage, runBy sql.NullString

	err := rows.Scan(
		&run.ID, &run.ReportID, &reportName, &reportType, &scheduleID, &run.Status, &run.Format,
		&run.StartedAt, &completedAt, &durationMS,
		&parametersJSON, &rowCount, &resultSize, &resultPath, &resultData,
		&errorMessage, &runBy, &run.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan run row: %w", err)
	}

	run.ReportName = reportName.String
	run.ReportType = reportType.String
	if scheduleID.Valid {
		run.ScheduleID = &scheduleID.Int64
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	run.DurationMS = durationMS.Int64
	run.RowCount = int(rowCount.Int64)
	run.ResultSize = resultSize.Int64
	run.ParametersJSON = parametersJSON.String
	run.ResultPath = resultPath.String
	run.ResultData = resultData.String
	run.ErrorMessage = errorMessage.String
	run.RunBy = runBy.String

	return &run, nil
}

// ============================================================
// Report Statistics Methods (BaseStore)
// ============================================================

// GetReportSummary returns dashboard statistics for reports.
func (s *BaseStore) GetReportSummary(ctx context.Context) (*ReportSummary, error) {
	var summary ReportSummary

	// Total reports
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM reports`).Scan(&summary.TotalReports)
	if err != nil {
		return nil, fmt.Errorf("count reports: %w", err)
	}

	// Total and active schedules
	err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM report_schedules`).Scan(&summary.TotalSchedules)
	if err != nil {
		return nil, fmt.Errorf("count schedules: %w", err)
	}
	err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM report_schedules WHERE enabled = ?`, true).Scan(&summary.ActiveSchedules)
	if err != nil {
		return nil, fmt.Errorf("count active schedules: %w", err)
	}

	// Run statistics
	err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM report_runs`).Scan(&summary.TotalRuns)
	if err != nil {
		return nil, fmt.Errorf("count runs: %w", err)
	}

	since24h := time.Now().Add(-24 * time.Hour)
	err = s.queryRowContext(ctx, `
		SELECT COUNT(*) FROM report_runs WHERE started_at >= ?
	`, since24h).Scan(&summary.RunsLast24h)
	if err != nil {
		return nil, fmt.Errorf("count runs 24h: %w", err)
	}

	err = s.queryRowContext(ctx, `
		SELECT COUNT(*) FROM report_runs WHERE started_at >= ? AND status = 'failed'
	`, since24h).Scan(&summary.FailedRunsLast24h)
	if err != nil {
		return nil, fmt.Errorf("count failed runs: %w", err)
	}

	err = s.queryRowContext(ctx, `
		SELECT COUNT(*) FROM report_runs WHERE status = 'completed'
	`).Scan(&summary.SuccessfulRuns)
	if err != nil {
		return nil, fmt.Errorf("count successful runs: %w", err)
	}

	// Average run time
	var avgTime sql.NullFloat64
	err = s.queryRowContext(ctx, `
		SELECT AVG(duration_ms) FROM report_runs WHERE status = 'completed' AND duration_ms > 0
	`).Scan(&avgTime)
	if err != nil {
		return nil, fmt.Errorf("avg run time: %w", err)
	}
	summary.AverageRunTimeMS = int64(avgTime.Float64)

	// Storage used
	var storageUsed sql.NullInt64
	err = s.queryRowContext(ctx, `
		SELECT COALESCE(SUM(result_size_bytes), 0) FROM report_runs
	`).Scan(&storageUsed)
	if err != nil {
		return nil, fmt.Errorf("storage used: %w", err)
	}
	summary.StorageUsedBytes = storageUsed.Int64

	return &summary, nil
}

// CleanupOldReportRuns deletes runs older than the retention period.
func (s *BaseStore) CleanupOldReportRuns(ctx context.Context, olderThan time.Time) (int64, error) {
	// Use UTC formatted string for consistent comparison with SQLite CURRENT_TIMESTAMP
	result, err := s.execContext(ctx, `
		DELETE FROM report_runs WHERE created_at < ?
	`, olderThan.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("cleanup runs: %w", err)
	}
	return result.RowsAffected()
}

// builtInReportDef contains metadata for built-in reports
type builtInReportDef struct {
	Name        string
	Description string
	Type        string
}

// getBuiltInReportDefs returns definitions for all built-in reports
func getBuiltInReportDefs() []builtInReportDef {
	return []builtInReportDef{
		{"Device Inventory", "Complete list of all discovered devices", string(ReportTypeDeviceInventory)},
		{"Usage Summary", "Fleet-wide usage statistics", string(ReportTypeUsageSummary)},
		{"Supplies Status", "Current supply levels for all devices", string(ReportTypeSuppliesStatus)},
		{"Alert History", "Recent alerts and their resolutions", string(ReportTypeAlertHistory)},
		{"Agent Status", "Current agent connectivity and health", string(ReportTypeAgentStatus)},
		{"Fleet Health", "Overall fleet health metrics", string(ReportTypeFleetHealth)},
		{"Usage by Device", "Usage breakdown per device", string(ReportTypeUsageByDevice)},
		{"Usage by Agent", "Usage breakdown per agent", string(ReportTypeUsageByAgent)},
		{"Usage by Site", "Usage breakdown per site", string(ReportTypeUsageBySite)},
		{"Usage by Tenant", "Usage breakdown per tenant", string(ReportTypeUsageByTenant)},
		{"Usage Trends", "Historical usage trends", string(ReportTypeUsageTrends)},
		{"Top Printers", "Most-used printers by page count", string(ReportTypeTopPrinters)},
	}
}

// SeedBuiltInReports creates default built-in reports.
func (s *BaseStore) SeedBuiltInReports(ctx context.Context) error {
	// Check if already seeded
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM reports WHERE is_built_in = %s`, s.dialect.BoolValue(true))
	err := s.queryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return fmt.Errorf("check built-in reports: %w", err)
	}
	if count > 0 {
		return nil // Already seeded
	}

	types := getBuiltInReportDefs()
	for _, t := range types {
		report := &ReportDefinition{
			Name:        t.Name,
			Description: t.Description,
			Type:        t.Type,
			Format:      string(ReportFormatJSON),
			Scope:       string(ReportScopeFleet),
			IsBuiltIn:   true,
		}

		// Set sensible defaults based on type
		switch t.Type {
		case string(ReportTypeUsageSummary), string(ReportTypeUsageByDevice), string(ReportTypeUsageByAgent),
			string(ReportTypeUsageBySite), string(ReportTypeUsageByTenant), string(ReportTypeUsageTrends):
			report.TimeRangeType = "last_30d"
		case string(ReportTypeTopPrinters):
			report.TimeRangeType = "last_30d"
			report.Limit = 10
			report.OrderBy = "page_count DESC"
		case string(ReportTypeSuppliesStatus):
			opts := map[string]interface{}{"threshold": 20}
			data, _ := json.Marshal(opts)
			report.OptionsJSON = string(data)
		case string(ReportTypeAlertHistory):
			report.TimeRangeType = "last_7d"
		}

		if err := s.CreateReport(ctx, report); err != nil {
			return fmt.Errorf("seed report %s: %w", t.Name, err)
		}
	}

	return nil
}
