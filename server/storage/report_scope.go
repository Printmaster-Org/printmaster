package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func reportIDsClause(column string, ids []string) (string, []interface{}) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return fmt.Sprintf("%s IN (%s)", column, placeholders), args
}

// appendReportAgentScope adds tenant, optional agent, and optional site filters.
// Callers must reject an empty tenant scope before constructing a query.
func appendReportAgentScope(query string, args []interface{}, alias string, scope ReportDataScope) (string, []interface{}) {
	clause, values := reportIDsClause(alias+".tenant_id", scope.TenantIDs)
	query += " AND " + clause
	args = append(args, values...)

	if len(scope.AgentIDs) > 0 {
		clause, values = reportIDsClause(alias+".agent_id", scope.AgentIDs)
		query += " AND " + clause
		args = append(args, values...)
	}
	if len(scope.SiteIDs) > 0 {
		clause, values = reportIDsClause("report_site.id", scope.SiteIDs)
		query += ` AND EXISTS (
			SELECT 1 FROM agent_sites report_assignment
			JOIN sites report_site ON report_site.id = report_assignment.site_id
			WHERE report_assignment.agent_id = ` + alias + `.agent_id
			  AND report_site.tenant_id = ` + alias + `.tenant_id
			  AND ` + clause + `)`
		args = append(args, values...)
	}
	return query, args
}

func (s *BaseStore) ListAgentsForReport(ctx context.Context, scope ReportDataScope) ([]*Agent, error) {
	if len(scope.TenantIDs) == 0 {
		return []*Agent{}, nil
	}
	query := `
		SELECT a.id, a.agent_id, a.name, a.hostname, a.ip, a.platform, a.version, a.protocol_version,
		       a.token, a.tenant_id, a.registered_at, a.last_seen, a.status,
		       a.os_version, a.go_version, a.architecture, a.num_cpu, a.total_memory_mb,
		       a.build_type, a.git_commit, a.last_heartbeat, a.device_count,
		       a.last_device_sync, a.last_metrics_sync
		FROM agents a WHERE 1=1
	`
	query, args := appendReportAgentScope(query, nil, "a", scope)
	query += " ORDER BY a.last_seen DESC"
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list report agents: %w", err)
	}
	defer rows.Close()

	agents := make([]*Agent, 0)
	for rows.Next() {
		agent, err := s.scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *BaseStore) GetAgentForReport(ctx context.Context, agentID string, scope ReportDataScope) (*Agent, error) {
	if agentID == "" || len(scope.TenantIDs) == 0 {
		return nil, nil
	}
	query := `
		SELECT a.id, a.agent_id, a.name, a.hostname, a.ip, a.platform, a.version, a.protocol_version,
		       a.token, a.tenant_id, a.registered_at, a.last_seen, a.status,
		       a.os_version, a.go_version, a.architecture, a.num_cpu, a.total_memory_mb,
		       a.build_type, a.git_commit, a.last_heartbeat, a.device_count,
		       a.last_device_sync, a.last_metrics_sync
		FROM agents a WHERE a.agent_id = ?
	`
	query, args := appendReportAgentScope(query, []interface{}{agentID}, "a", scope)
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get report agent: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return s.scanAgent(rows)
}

func (s *BaseStore) ListDevicesForReport(ctx context.Context, scope ReportDataScope) ([]*Device, error) {
	if len(scope.TenantIDs) == 0 {
		return []*Device{}, nil
	}
	query := `
		SELECT d.serial, d.agent_id, d.ip, d.manufacturer, d.model, d.hostname, d.firmware,
		       d.mac_address, d.subnet_mask, d.gateway, d.consumables, d.status_messages,
		       d.last_seen, d.first_seen, d.created_at, d.discovery_method,
		       d.asset_number, d.location, d.description, d.web_ui_url, d.raw_data,
		       d.device_type, d.source_type, d.is_usb, d.port_name, d.driver_name,
		       d.is_default, d.is_shared, d.spooler_status, d.usb_webui_available
		FROM devices d
		JOIN agents a ON a.agent_id = d.agent_id
		WHERE 1=1
	`
	query, args := appendReportAgentScope(query, nil, "a", scope)
	query += " ORDER BY d.last_seen DESC"
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list report devices: %w", err)
	}
	defer rows.Close()
	return s.scanDevices(rows)
}

func (s *BaseStore) ListTenantsForReport(ctx context.Context, scope ReportDataScope) ([]*Tenant, error) {
	if len(scope.TenantIDs) == 0 {
		return []*Tenant{}, nil
	}
	query := `
		SELECT id, name, description, contact_name, contact_email, contact_phone,
		       business_unit, billing_code, address, login_domain, created_at
		FROM tenants WHERE 1=1
	`
	clause, args := reportIDsClause("id", scope.TenantIDs)
	query += " AND " + clause + " ORDER BY created_at DESC"
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list report tenants: %w", err)
	}
	defer rows.Close()

	tenants := make([]*Tenant, 0)
	for rows.Next() {
		var tenant Tenant
		var loginDomain sql.NullString
		if err := rows.Scan(&tenant.ID, &tenant.Name, &tenant.Description, &tenant.ContactName, &tenant.ContactEmail,
			&tenant.ContactPhone, &tenant.BusinessUnit, &tenant.BillingCode, &tenant.Address,
			&loginDomain, &tenant.CreatedAt); err != nil {
			return nil, err
		}
		tenant.LoginDomain = loginDomain.String
		tenants = append(tenants, &tenant)
	}
	return tenants, rows.Err()
}

func (s *BaseStore) ListSitesForReport(ctx context.Context, scope ReportDataScope) ([]*Site, error) {
	if len(scope.TenantIDs) == 0 {
		return []*Site{}, nil
	}
	query := `SELECT id, tenant_id, name, description, address, filter_rules, created_at FROM sites WHERE 1=1`
	clause, args := reportIDsClause("tenant_id", scope.TenantIDs)
	query += " AND " + clause
	if len(scope.SiteIDs) > 0 {
		clause, values := reportIDsClause("id", scope.SiteIDs)
		query += " AND " + clause
		args = append(args, values...)
	}
	query += " ORDER BY name"
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list report sites: %w", err)
	}
	defer rows.Close()

	sites := make([]*Site, 0)
	for rows.Next() {
		var site Site
		var rulesJSON sql.NullString
		if err := rows.Scan(&site.ID, &site.TenantID, &site.Name, &site.Description, &site.Address, &rulesJSON, &site.CreatedAt); err != nil {
			return nil, err
		}
		if rulesJSON.Valid && rulesJSON.String != "" {
			_ = json.Unmarshal([]byte(rulesJSON.String), &site.FilterRules)
		}
		sites = append(sites, &site)
	}
	return sites, rows.Err()
}

func (s *BaseStore) ListAlertsForReport(ctx context.Context, filter AlertFilter, scope ReportDataScope) ([]*Alert, error) {
	if len(scope.TenantIDs) == 0 {
		return []*Alert{}, nil
	}
	query := `
		SELECT a.id, a.rule_id, a.type, a.severity, a.scope, a.status,
		       a.tenant_id, a.site_id, a.agent_id, a.device_serial,
		       a.title, a.message, a.details,
		       a.triggered_at, a.acknowledged_at, a.acknowledged_by, a.resolved_at,
		       a.suppressed_until, a.expires_at,
		       a.escalation_level, a.last_escalated_at,
		       a.state_change_count, a.is_flapping,
		       a.parent_alert_id, a.child_count,
		       a.notifications_sent, a.last_notified_at,
		       a.created_at, a.updated_at
		FROM alerts a WHERE 1=1
	`
	clause, args := reportIDsClause("a.tenant_id", scope.TenantIDs)
	query += " AND " + clause
	if len(scope.SiteIDs) > 0 {
		clause, values := reportIDsClause("report_site.id", scope.SiteIDs)
		query += ` AND EXISTS (
			SELECT 1 FROM sites report_site
			WHERE report_site.id = a.site_id
			  AND report_site.tenant_id = a.tenant_id
			  AND ` + clause + `)`
		args = append(args, values...)
	}
	if len(scope.AgentIDs) > 0 {
		clause, values := reportIDsClause("report_agent.agent_id", scope.AgentIDs)
		query += ` AND EXISTS (
			SELECT 1 FROM agents report_agent
			WHERE report_agent.agent_id = a.agent_id
			  AND report_agent.tenant_id = a.tenant_id
			  AND ` + clause + `)`
		args = append(args, values...)
	}
	if filter.Severity != "" {
		query += " AND a.severity = ?"
		args = append(args, filter.Severity)
	}
	if filter.Scope != "" {
		query += " AND a.scope = ?"
		args = append(args, filter.Scope)
	}
	if filter.Type != "" {
		query += " AND a.type = ?"
		args = append(args, filter.Type)
	}
	if filter.Status != "" {
		query += " AND a.status = ?"
		args = append(args, filter.Status)
	}
	if filter.TenantID != "" {
		query += " AND a.tenant_id = ?"
		args = append(args, filter.TenantID)
	}
	if filter.StartTime != nil {
		query += " AND a.triggered_at >= ?"
		args = append(args, *filter.StartTime)
	}
	if filter.EndTime != nil {
		query += " AND a.triggered_at <= ?"
		args = append(args, *filter.EndTime)
	}
	query += " ORDER BY a.triggered_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list report alerts: %w", err)
	}
	defer rows.Close()
	alerts, err := s.scanAlerts(rows)
	if err != nil {
		return nil, err
	}
	result := make([]*Alert, len(alerts))
	for i := range alerts {
		result[i] = &alerts[i]
	}
	return result, nil
}
