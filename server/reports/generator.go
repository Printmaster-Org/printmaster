package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"printmaster/server/storage"
	"sort"
	"strings"
	"time"
)

// GeneratorStore defines the store interface needed by the generator.
type GeneratorStore interface {
	// Devices
	ListAllDevices(ctx context.Context) ([]*storage.Device, error)
	GetLatestMetrics(ctx context.Context, serial string) (*storage.MetricsSnapshot, error)
	GetMetricsAtOrBefore(ctx context.Context, serial string, at time.Time) (*storage.MetricsSnapshot, error)
	GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*storage.MetricsSnapshot, error)

	// Agents
	ListAgents(ctx context.Context) ([]*storage.Agent, error)
	GetAgent(ctx context.Context, agentID string) (*storage.Agent, error)

	// Tenants & Sites
	ListTenants(ctx context.Context) ([]*storage.Tenant, error)
	GetTenant(ctx context.Context, id string) (*storage.Tenant, error)
	ListSitesByTenant(ctx context.Context, tenantID string) ([]*storage.Site, error)

	// Alerts
	ListAlerts(ctx context.Context, filter storage.AlertFilter) ([]*storage.Alert, error)
	GetAlertSummary(ctx context.Context) (*storage.AlertSummary, error)
}

// Generator generates reports from stored data.
type Generator struct {
	store GeneratorStore
}

// NewGenerator creates a new report generator.
func NewGenerator(store GeneratorStore) *Generator {
	return &Generator{store: store}
}

// GenerateResult holds the result of report generation.
type GenerateResult struct {
	Data     interface{}       `json:"data"`
	Rows     []map[string]any  `json:"rows,omitempty"`
	Columns  []string          `json:"columns,omitempty"`
	Summary  map[string]any    `json:"summary,omitempty"`
	RowCount int               `json:"row_count"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// GenerateParams defines parameters for report generation.
type GenerateParams struct {
	Report    *storage.ReportDefinition
	StartTime time.Time
	EndTime   time.Time
}

// Generate generates a report based on its definition.
func (g *Generator) Generate(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	report := params.Report
	if report == nil {
		return nil, fmt.Errorf("missing report definition")
	}
	if report.Scope != "" && report.Scope != storage.ReportScopeFleet && len(report.TenantIDs) == 0 {
		return nil, fmt.Errorf("tenant-scoped report requires explicit tenant ownership")
	}
	if len(report.TenantIDs) > 0 {
		// Restrict the data source itself. Individual generators historically
		// ignored tenant filters (including sites and alert aggregates).
		g = NewGenerator(&tenantReportStore{GeneratorStore: g.store, report: report})
	}

	// Normalize report type: convert dots to underscores for backwards compatibility
	// (e.g., "usage.summary" -> "usage_summary")
	reportType := strings.ReplaceAll(report.Type, ".", "_")

	switch reportType {
	// Inventory reports
	case storage.ReportTypeDeviceInventory:
		return g.generateDeviceInventory(ctx, params)
	case storage.ReportTypeAgentInventory:
		return g.generateAgentInventory(ctx, params)
	case storage.ReportTypeSiteInventory:
		return g.generateSiteInventory(ctx, params)

	// Usage reports
	case storage.ReportTypeUsageSummary:
		return g.generateUsageSummary(ctx, params)
	case storage.ReportTypeUsageByDevice:
		return g.generateUsageByDevice(ctx, params)
	case storage.ReportTypeTopPrinters:
		return g.generateTopPrinters(ctx, params)

	// Supplies reports
	case storage.ReportTypeSuppliesStatus:
		return g.generateSuppliesStatus(ctx, params)
	case storage.ReportTypeSuppliesLow:
		return g.generateSuppliesLow(ctx, params)
	case storage.ReportTypeSuppliesCritical:
		return g.generateSuppliesCritical(ctx, params)

	// Health reports
	case storage.ReportTypeHealthSummary:
		return g.generateHealthSummary(ctx, params)
	case storage.ReportTypeOfflineDevices:
		return g.generateOfflineDevices(ctx, params)
	case storage.ReportTypeErrorDevices:
		return g.generateErrorDevices(ctx, params)
	case storage.ReportTypeAgentHealth:
		return g.generateAgentHealth(ctx, params)

	// Alert reports
	case storage.ReportTypeAlertSummary:
		return g.generateAlertSummary(ctx, params)
	case storage.ReportTypeAlertHistory:
		return g.generateAlertHistory(ctx, params)

	default:
		return nil, fmt.Errorf("unsupported report type: %s", report.Type)
	}
}

// ---------- Inventory Reports ----------

func (g *Generator) generateDeviceInventory(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	// Apply filters if specified
	devices = g.filterDevices(devices, params.Report)

	rows := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		row := map[string]any{
			"serial":       d.Serial,
			"ip":           d.IP,
			"manufacturer": d.Manufacturer,
			"model":        d.Model,
			"hostname":     d.Hostname,
			"firmware":     d.Firmware,
			"mac_address":  d.MACAddress,
			"agent_id":     d.AgentID,
			"location":     d.Location,
			"asset_number": d.AssetNumber,
			"last_seen":    d.LastSeen,
			"first_seen":   d.FirstSeen,
		}
		rows = append(rows, row)
	}

	columns := []string{
		"serial", "ip", "manufacturer", "model", "hostname",
		"firmware", "mac_address", "agent_id", "location",
		"asset_number", "last_seen", "first_seen",
	}

	// Filter columns if specified
	if len(params.Report.Columns) > 0 {
		columns = params.Report.Columns
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_devices": len(devices),
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateAgentInventory(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	agents, err := g.store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	// Apply filters
	agents = g.filterAgents(agents, params.Report)

	rows := make([]map[string]any, 0, len(agents))
	var onlineCount, offlineCount int

	for _, a := range agents {
		isOnline := time.Since(a.LastSeen) < 5*time.Minute
		status := "offline"
		if isOnline {
			status = "online"
			onlineCount++
		} else {
			offlineCount++
		}

		row := map[string]any{
			"agent_id":      a.AgentID,
			"name":          a.Name,
			"hostname":      a.Hostname,
			"ip":            a.IP,
			"platform":      a.Platform,
			"version":       a.Version,
			"status":        status,
			"last_seen":     a.LastSeen,
			"device_count":  a.DeviceCount,
			"os_version":    a.OSVersion,
			"architecture":  a.Architecture,
			"registered_at": a.RegisteredAt,
		}
		rows = append(rows, row)
	}

	columns := []string{
		"agent_id", "name", "hostname", "ip", "platform",
		"version", "status", "last_seen", "device_count",
		"os_version", "architecture", "registered_at",
	}

	if len(params.Report.Columns) > 0 {
		columns = params.Report.Columns
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_agents":   len(agents),
			"online_agents":  onlineCount,
			"offline_agents": offlineCount,
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateSiteInventory(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	tenants, err := g.store.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}

	var rows []map[string]any
	tenantByID := make(map[string]*storage.Tenant, len(tenants))
	for _, t := range tenants {
		tenantByID[t.ID] = t
	}

	var sites []*storage.Site
	if scoped, ok := g.store.(interface {
		ListReportSites(context.Context) ([]*storage.Site, error)
	}); ok {
		sites, err = scoped.ListReportSites(ctx)
		if err != nil {
			return nil, fmt.Errorf("list report sites: %w", err)
		}
	} else {
		for _, tenant := range tenants {
			tenantSites, err := g.store.ListSitesByTenant(ctx, tenant.ID)
			if err != nil {
				continue
			}
			sites = append(sites, tenantSites...)
		}
	}

	for _, site := range sites {
		tenant := tenantByID[site.TenantID]
		if tenant == nil {
			continue
		}
		rows = append(rows, map[string]any{
			"site_id":      site.ID,
			"site_name":    site.Name,
			"tenant_id":    tenant.ID,
			"tenant_name":  tenant.Name,
			"description":  site.Description,
			"address":      site.Address,
			"agent_count":  site.AgentCount,
			"device_count": site.DeviceCount,
			"created_at":   site.CreatedAt,
		})
	}

	columns := []string{
		"site_id", "site_name", "tenant_id", "tenant_name",
		"description", "address", "agent_count", "device_count", "created_at",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_sites":   len(rows),
			"total_tenants": len(tenants),
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

// ---------- Usage Reports ----------

func (g *Generator) generateUsageSummary(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	devices = g.filterDevices(devices, params.Report)

	includeDeltas := params.Report != nil && params.Report.TimeRangeType != "current"

	columns := []string{
		"serial", "model", "manufacturer", "location", "agent_id", "last_seen",
		"page_count_current", "color_pages_current", "mono_pages_current", "scan_count_current",
	}
	if includeDeltas {
		columns = append(columns,
			"baseline_at",
			"page_count_then", "color_pages_then", "mono_pages_then", "scan_count_then",
			"page_count_delta", "color_pages_delta", "mono_pages_delta", "scan_count_delta",
		)
	}

	rows := make([]map[string]any, 0, len(devices))

	var deviceCount int
	var totalPages, totalColor, totalMono, totalScan int64
	var totalDeltaPages, totalDeltaColor, totalDeltaMono, totalDeltaScan int64

	for _, d := range devices {
		if d == nil || d.Serial == "" {
			continue
		}
		current, err := g.store.GetLatestMetrics(ctx, d.Serial)
		if err != nil || current == nil {
			continue
		}

		deviceCount++
		totalPages += int64(current.PageCount)
		totalColor += int64(current.ColorPages)
		totalMono += int64(current.MonoPages)
		totalScan += int64(current.ScanCount)

		row := map[string]any{
			"serial":              d.Serial,
			"model":               d.Model,
			"manufacturer":        d.Manufacturer,
			"location":            d.Location,
			"agent_id":            d.AgentID,
			"last_seen":           d.LastSeen,
			"page_count_current":  current.PageCount,
			"color_pages_current": current.ColorPages,
			"mono_pages_current":  current.MonoPages,
			"scan_count_current":  current.ScanCount,
		}

		if includeDeltas {
			baseline := current
			baselineAt := current.Timestamp

			if b, err := g.store.GetMetricsAtOrBefore(ctx, d.Serial, params.StartTime); err == nil && b != nil {
				baseline = b
				baselineAt = b.Timestamp
			} else {
				// Fallback for stores that only support "since" queries: use the earliest snapshot after start.
				hist, err := g.store.GetMetricsHistory(ctx, d.Serial, params.StartTime)
				if err == nil {
					for _, snap := range hist {
						if snap != nil {
							baseline = snap
							baselineAt = snap.Timestamp
							break
						}
					}
				}
			}

			dPage := int64(current.PageCount - baseline.PageCount)
			dColor := int64(current.ColorPages - baseline.ColorPages)
			dMono := int64(current.MonoPages - baseline.MonoPages)
			dScan := int64(current.ScanCount - baseline.ScanCount)

			totalDeltaPages += dPage
			totalDeltaColor += dColor
			totalDeltaMono += dMono
			totalDeltaScan += dScan

			row["baseline_at"] = baselineAt
			row["page_count_then"] = baseline.PageCount
			row["color_pages_then"] = baseline.ColorPages
			row["mono_pages_then"] = baseline.MonoPages
			row["scan_count_then"] = baseline.ScanCount
			row["page_count_delta"] = dPage
			row["color_pages_delta"] = dColor
			row["mono_pages_delta"] = dMono
			row["scan_count_delta"] = dScan
		}

		rows = append(rows, row)
	}

	// Keep output stable and useful: sort by most pages (or most delta if in delta mode)
	sort.Slice(rows, func(i, j int) bool {
		if includeDeltas {
			pi, _ := rows[i]["page_count_delta"].(int64)
			pj, _ := rows[j]["page_count_delta"].(int64)
			return pi > pj
		}
		pi, _ := rows[i]["page_count_current"].(int)
		pj, _ := rows[j]["page_count_current"].(int)
		return pi > pj
	})

	if params.Report != nil && params.Report.Limit > 0 && len(rows) > params.Report.Limit {
		rows = rows[:params.Report.Limit]
	}

	summary := map[string]any{
		"period_start":              params.StartTime,
		"period_end":                params.EndTime,
		"total_devices":             deviceCount,
		"page_count_current_total":  totalPages,
		"color_pages_current_total": totalColor,
		"mono_pages_current_total":  totalMono,
		"scan_count_current_total":  totalScan,
	}
	if includeDeltas {
		summary["page_count_delta_total"] = totalDeltaPages
		summary["color_pages_delta_total"] = totalDeltaColor
		summary["mono_pages_delta_total"] = totalDeltaMono
		summary["scan_count_delta_total"] = totalDeltaScan
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary:  summary,
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateUsageByDevice(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	devices = g.filterDevices(devices, params.Report)

	rows := make([]map[string]any, 0, len(devices))
	var totalPages int64

	for _, d := range devices {
		metrics, err := g.store.GetLatestMetrics(ctx, d.Serial)
		if err != nil || metrics == nil {
			continue
		}

		row := map[string]any{
			"serial":       d.Serial,
			"model":        d.Model,
			"manufacturer": d.Manufacturer,
			"location":     d.Location,
			"agent_id":     d.AgentID,
			"page_count":   metrics.PageCount,
			"color_pages":  metrics.ColorPages,
			"mono_pages":   metrics.MonoPages,
			"scan_count":   metrics.ScanCount,
			"last_seen":    d.LastSeen,
		}
		rows = append(rows, row)
		totalPages += int64(metrics.PageCount)
	}

	// Sort by page count descending by default
	sort.Slice(rows, func(i, j int) bool {
		pi, _ := rows[i]["page_count"].(int)
		pj, _ := rows[j]["page_count"].(int)
		return pi > pj
	})

	// Apply limit
	if params.Report.Limit > 0 && len(rows) > params.Report.Limit {
		rows = rows[:params.Report.Limit]
	}

	columns := []string{
		"serial", "model", "manufacturer", "location", "agent_id",
		"page_count", "color_pages", "mono_pages", "scan_count", "last_seen",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_devices":    len(devices),
			"total_page_count": totalPages,
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateTopPrinters(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	// This is essentially usage by device with a default limit
	if params.Report.Limit == 0 {
		params.Report.Limit = 10
	}
	return g.generateUsageByDevice(ctx, params)
}

// ---------- Supplies Reports ----------

func (g *Generator) generateSuppliesStatus(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	devices = g.filterDevices(devices, params.Report)

	rows := make([]map[string]any, 0, len(devices))
	var criticalCount, lowCount, okCount int

	for _, d := range devices {
		metrics, err := g.store.GetLatestMetrics(ctx, d.Serial)
		if err != nil || metrics == nil {
			continue
		}

		// Parse toner levels
		tonerLevels := metrics.TonerLevels
		minLevel := 100

		for _, level := range tonerLevels {
			levelInt := toInt(level)
			if levelInt < minLevel {
				minLevel = levelInt
			}
		}

		status := "ok"
		if minLevel < 10 {
			status = "critical"
			criticalCount++
		} else if minLevel < 20 {
			status = "low"
			lowCount++
		} else {
			okCount++
		}

		row := map[string]any{
			"serial":       d.Serial,
			"model":        d.Model,
			"manufacturer": d.Manufacturer,
			"location":     d.Location,
			"toner_levels": tonerLevels,
			"min_level":    minLevel,
			"status":       status,
			"last_seen":    d.LastSeen,
		}
		rows = append(rows, row)
	}

	// Sort by min level ascending (lowest first)
	sort.Slice(rows, func(i, j int) bool {
		li, _ := rows[i]["min_level"].(int)
		lj, _ := rows[j]["min_level"].(int)
		return li < lj
	})

	columns := []string{
		"serial", "model", "manufacturer", "location",
		"toner_levels", "min_level", "status", "last_seen",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_devices":    len(rows),
			"critical_devices": criticalCount,
			"low_devices":      lowCount,
			"ok_devices":       okCount,
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateSuppliesLow(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	result, err := g.generateSuppliesStatus(ctx, params)
	if err != nil {
		return nil, err
	}

	// Parse threshold from options
	threshold := 20
	if params.Report.OptionsJSON != "" {
		var opts map[string]interface{}
		if err := json.Unmarshal([]byte(params.Report.OptionsJSON), &opts); err == nil {
			if t, ok := opts["threshold"].(float64); ok {
				threshold = int(t)
			}
		}
	}

	// Filter to low and critical only
	var filtered []map[string]any
	for _, row := range result.Rows {
		if level, ok := row["min_level"].(int); ok && level < threshold {
			filtered = append(filtered, row)
		}
	}

	result.Rows = filtered
	result.RowCount = len(filtered)
	return result, nil
}

func (g *Generator) generateSuppliesCritical(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	result, err := g.generateSuppliesStatus(ctx, params)
	if err != nil {
		return nil, err
	}

	// Parse threshold from options
	threshold := 10
	if params.Report.OptionsJSON != "" {
		var opts map[string]interface{}
		if err := json.Unmarshal([]byte(params.Report.OptionsJSON), &opts); err == nil {
			if t, ok := opts["threshold"].(float64); ok {
				threshold = int(t)
			}
		}
	}

	// Filter to critical only
	var filtered []map[string]any
	for _, row := range result.Rows {
		if level, ok := row["min_level"].(int); ok && level < threshold {
			filtered = append(filtered, row)
		}
	}

	result.Rows = filtered
	result.RowCount = len(filtered)
	return result, nil
}

// ---------- Health Reports ----------

func (g *Generator) generateHealthSummary(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	agents, err := g.store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	// Device health stats
	var onlineDevices, offlineDevices, errorDevices, warningDevices int
	offlineThreshold := 10 * time.Minute

	for _, d := range devices {
		if time.Since(d.LastSeen) > offlineThreshold {
			offlineDevices++
		} else {
			onlineDevices++
		}

		// Check status messages for errors/warnings
		for _, msg := range d.StatusMessages {
			if containsAny(msg, []string{"error", "jam", "failure"}) {
				errorDevices++
				break
			} else if containsAny(msg, []string{"warning", "low"}) {
				warningDevices++
				break
			}
		}
	}

	// Agent health stats
	var onlineAgents, offlineAgents int
	agentOfflineThreshold := 5 * time.Minute

	for _, a := range agents {
		if time.Since(a.LastSeen) > agentOfflineThreshold {
			offlineAgents++
		} else {
			onlineAgents++
		}
	}

	summary := map[string]any{
		"total_devices":   len(devices),
		"online_devices":  onlineDevices,
		"offline_devices": offlineDevices,
		"error_devices":   errorDevices,
		"warning_devices": warningDevices,
		"total_agents":    len(agents),
		"online_agents":   onlineAgents,
		"offline_agents":  offlineAgents,
		"health_score":    calculateHealthScore(onlineDevices, len(devices), errorDevices),
	}

	return &GenerateResult{
		Data:     summary,
		Summary:  summary,
		RowCount: 1,
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateOfflineDevices(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	offlineThreshold := 10 * time.Minute

	var rows []map[string]any
	for _, d := range devices {
		offlineDuration := time.Since(d.LastSeen)
		if offlineDuration > offlineThreshold {
			row := map[string]any{
				"serial":           d.Serial,
				"model":            d.Model,
				"manufacturer":     d.Manufacturer,
				"ip":               d.IP,
				"location":         d.Location,
				"agent_id":         d.AgentID,
				"last_seen":        d.LastSeen,
				"offline_duration": offlineDuration.String(),
				"offline_hours":    int(offlineDuration.Hours()),
			}
			rows = append(rows, row)
		}
	}

	// Sort by offline duration descending
	sort.Slice(rows, func(i, j int) bool {
		hi, _ := rows[i]["offline_hours"].(int)
		hj, _ := rows[j]["offline_hours"].(int)
		return hi > hj
	})

	columns := []string{
		"serial", "model", "manufacturer", "ip", "location",
		"agent_id", "last_seen", "offline_duration",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"offline_device_count": len(rows),
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateErrorDevices(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	devices, err := g.store.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	var rows []map[string]any
	for _, d := range devices {
		var errors []string
		for _, msg := range d.StatusMessages {
			if containsAny(msg, []string{"error", "jam", "failure", "warning"}) {
				errors = append(errors, msg)
			}
		}

		if len(errors) > 0 {
			row := map[string]any{
				"serial":       d.Serial,
				"model":        d.Model,
				"manufacturer": d.Manufacturer,
				"ip":           d.IP,
				"location":     d.Location,
				"agent_id":     d.AgentID,
				"errors":       errors,
				"error_count":  len(errors),
				"last_seen":    d.LastSeen,
			}
			rows = append(rows, row)
		}
	}

	columns := []string{
		"serial", "model", "manufacturer", "ip", "location",
		"agent_id", "errors", "error_count", "last_seen",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"error_device_count": len(rows),
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateAgentHealth(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	agents, err := g.store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}

	agents = g.filterAgents(agents, params.Report)

	offlineThreshold := 5 * time.Minute
	var rows []map[string]any
	var onlineCount, offlineCount int

	for _, a := range agents {
		isOnline := time.Since(a.LastSeen) < offlineThreshold
		status := "offline"
		if isOnline {
			status = "online"
			onlineCount++
		} else {
			offlineCount++
		}

		var offlineDuration string
		if !isOnline {
			offlineDuration = time.Since(a.LastSeen).String()
		}

		row := map[string]any{
			"agent_id":         a.AgentID,
			"name":             a.Name,
			"hostname":         a.Hostname,
			"ip":               a.IP,
			"version":          a.Version,
			"status":           status,
			"device_count":     a.DeviceCount,
			"last_seen":        a.LastSeen,
			"offline_duration": offlineDuration,
		}
		rows = append(rows, row)
	}

	// Sort offline first, then by name
	sort.Slice(rows, func(i, j int) bool {
		si, _ := rows[i]["status"].(string)
		sj, _ := rows[j]["status"].(string)
		if si != sj {
			return si == "offline"
		}
		ni, _ := rows[i]["name"].(string)
		nj, _ := rows[j]["name"].(string)
		return ni < nj
	})

	columns := []string{
		"agent_id", "name", "hostname", "ip", "version",
		"status", "device_count", "last_seen", "offline_duration",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_agents":   len(agents),
			"online_agents":  onlineCount,
			"offline_agents": offlineCount,
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

// ---------- Alert Reports ----------

func (g *Generator) generateAlertSummary(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	summary, err := g.store.GetAlertSummary(ctx)
	if err != nil {
		return nil, fmt.Errorf("get alert summary: %w", err)
	}

	// Calculate totals from the nested counts
	totalCritical := summary.CriticalCounts.Devices + summary.CriticalCounts.Agents + summary.CriticalCounts.Sites + summary.CriticalCounts.Tenants
	totalWarning := summary.WarningCounts.Devices + summary.WarningCounts.Agents + summary.WarningCounts.Sites + summary.WarningCounts.Tenants

	data := map[string]any{
		"critical_counts":    summary.CriticalCounts,
		"warning_counts":     summary.WarningCounts,
		"healthy_counts":     summary.HealthyCounts,
		"offline_counts":     summary.OfflineCounts,
		"total_critical":     totalCritical,
		"total_warning":      totalWarning,
		"acknowledged_count": summary.AcknowledgedCount,
		"suppressed_count":   summary.SuppressedCount,
		"active_rules":       summary.ActiveRules,
		"active_channels":    summary.ActiveChannels,
		"is_quiet_hours":     summary.IsQuietHours,
		"has_maintenance":    summary.HasMaintenance,
		"alerts_by_type":     summary.AlertsByType,
	}

	return &GenerateResult{
		Data:     data,
		Summary:  data,
		RowCount: 1,
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

func (g *Generator) generateAlertHistory(ctx context.Context, params GenerateParams) (*GenerateResult, error) {
	filter := storage.AlertFilter{
		StartTime: &params.StartTime,
	}

	alerts, err := g.store.ListAlerts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}

	var rows []map[string]any
	for _, a := range alerts {
		row := map[string]any{
			"id":            a.ID,
			"type":          a.Type,
			"severity":      a.Severity,
			"scope":         a.Scope,
			"status":        a.Status,
			"title":         a.Title,
			"message":       a.Message,
			"tenant_id":     a.TenantID,
			"site_id":       a.SiteID,
			"agent_id":      a.AgentID,
			"device_serial": a.DeviceSerial,
			"triggered_at":  a.TriggeredAt,
			"resolved_at":   a.ResolvedAt,
		}
		rows = append(rows, row)
	}

	// Apply limit
	if params.Report.Limit > 0 && len(rows) > params.Report.Limit {
		rows = rows[:params.Report.Limit]
	}

	columns := []string{
		"id", "type", "severity", "scope", "status", "title",
		"message", "triggered_at", "resolved_at",
	}

	return &GenerateResult{
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		Summary: map[string]any{
			"total_alerts": len(alerts),
		},
		Metadata: map[string]string{
			"report_type": string(params.Report.Type),
			"generated":   time.Now().UTC().Format(time.RFC3339),
		},
	}, nil
}

// ---------- Helper Functions ----------

func (g *Generator) filterDevices(devices []*storage.Device, report *storage.ReportDefinition) []*storage.Device {
	if len(report.TenantIDs) == 0 && len(report.SiteIDs) == 0 && len(report.AgentIDs) == 0 {
		return devices
	}

	// Build lookup maps
	agentFilter := make(map[string]bool)
	for _, id := range report.AgentIDs {
		agentFilter[id] = true
	}

	var filtered []*storage.Device
	for _, d := range devices {
		// Filter by agent if specified
		if len(agentFilter) > 0 && !agentFilter[d.AgentID] {
			continue
		}
		// TODO: Add tenant/site filtering when device has those fields
		filtered = append(filtered, d)
	}
	return filtered
}

func (g *Generator) filterAgents(agents []*storage.Agent, report *storage.ReportDefinition) []*storage.Agent {
	if len(report.AgentIDs) == 0 {
		return agents
	}

	agentFilter := make(map[string]bool)
	for _, id := range report.AgentIDs {
		agentFilter[id] = true
	}

	var filtered []*storage.Agent
	for _, a := range agents {
		if agentFilter[a.AgentID] {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		return 0
	default:
		return 0
	}
}

func containsAny(s string, substrs []string) bool {
	lower := s
	for _, sub := range substrs {
		if len(s) > 0 && len(sub) > 0 {
			// Simple case-insensitive contains
			if contains(lower, sub) {
				return true
			}
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func calculateHealthScore(online, total, errors int) float64 {
	if total == 0 {
		return 100.0
	}

	onlineRatio := float64(online) / float64(total)
	errorPenalty := float64(errors) / float64(total) * 20

	score := onlineRatio*100 - errorPenalty
	if score < 0 {
		score = 0
	}
	return score
}
