package reports

import (
	"context"
	"fmt"
	"time"

	"printmaster/server/storage"
)

// tenantReportStore enforces report ownership for every generator, including
// scheduled jobs, without depending on each report type remembering to filter.
// Empty resolved agent sets stay empty; they never mean unrestricted access.
type tenantReportStore struct {
	GeneratorStore
	report       *storage.ReportDefinition
	deviceOwners map[string]string
}

func reportContains(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate != "" && candidate == id {
			return true
		}
	}
	return false
}

func (s *tenantReportStore) reportDataScope() storage.ReportDataScope {
	return storage.ReportDataScope{
		TenantIDs: s.report.TenantIDs,
		SiteIDs:   s.report.SiteIDs,
		AgentIDs:  s.report.AgentIDs,
	}
}

func (s *tenantReportStore) reportScopedStore() (storage.ReportScopedStore, error) {
	scoped, ok := s.GeneratorStore.(storage.ReportScopedStore)
	if !ok {
		return nil, fmt.Errorf("generator store does not support scoped report queries")
	}
	return scoped, nil
}

func (s *tenantReportStore) ListAgents(ctx context.Context) ([]*storage.Agent, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	return scoped.ListAgentsForReport(ctx, s.reportDataScope())
}

func (s *tenantReportStore) GetAgent(ctx context.Context, id string) (*storage.Agent, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	return scoped.GetAgentForReport(ctx, id, s.reportDataScope())
}

func (s *tenantReportStore) ListAllDevices(ctx context.Context) ([]*storage.Device, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	devices, err := scoped.ListDevicesForReport(ctx, s.reportDataScope())
	if err != nil {
		return nil, err
	}
	s.deviceOwners = make(map[string]string)
	for _, device := range devices {
		s.deviceOwners[device.Serial] = device.AgentID
	}
	return devices, nil
}

func (s *tenantReportStore) ListTenants(ctx context.Context) ([]*storage.Tenant, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	return scoped.ListTenantsForReport(ctx, s.reportDataScope())
}

func (s *tenantReportStore) GetTenant(ctx context.Context, id string) (*storage.Tenant, error) {
	if !reportContains(s.report.TenantIDs, id) {
		return nil, fmt.Errorf("tenant outside report scope")
	}
	return s.GeneratorStore.GetTenant(ctx, id)
}

func (s *tenantReportStore) ListSitesByTenant(ctx context.Context, id string) ([]*storage.Site, error) {
	if !reportContains(s.report.TenantIDs, id) {
		return nil, fmt.Errorf("tenant outside report scope")
	}
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	return scoped.ListSitesForReport(ctx, storage.ReportDataScope{
		TenantIDs: []string{id},
		SiteIDs:   s.report.SiteIDs,
	})
}

func (s *tenantReportStore) ListReportSites(ctx context.Context) ([]*storage.Site, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	return scoped.ListSitesForReport(ctx, s.reportDataScope())
}

func (s *tenantReportStore) ListAlerts(ctx context.Context, filter storage.AlertFilter) ([]*storage.Alert, error) {
	scoped, err := s.reportScopedStore()
	if err != nil {
		return nil, err
	}
	// Preserve report totals; the report limit is applied after generation.
	filter.Limit, filter.Offset = 0, 0
	return scoped.ListAlertsForReport(ctx, filter, s.reportDataScope())
}

func (s *tenantReportStore) GetAlertSummary(ctx context.Context) (*storage.AlertSummary, error) {
	// The existing aggregate mixes tenant data with global rules/channels and
	// maintenance settings. Never label that global aggregate tenant-safe.
	return nil, fmt.Errorf("alert summary reports require administrator fleet scope")
}

func (s *tenantReportStore) metricAllowed(ctx context.Context, serial string, metric *storage.MetricsSnapshot) bool {
	if metric == nil || metric.AgentID == "" {
		return false
	}
	if s.deviceOwners == nil {
		if _, err := s.ListAllDevices(ctx); err != nil {
			return false
		}
	}
	return s.deviceOwners[serial] == metric.AgentID
}

func (s *tenantReportStore) GetLatestMetrics(ctx context.Context, serial string) (*storage.MetricsSnapshot, error) {
	metric, err := s.GeneratorStore.GetLatestMetrics(ctx, serial)
	if err != nil || !s.metricAllowed(ctx, serial, metric) {
		return nil, err
	}
	return metric, nil
}

func (s *tenantReportStore) GetMetricsAtOrBefore(ctx context.Context, serial string, atTime time.Time) (*storage.MetricsSnapshot, error) {
	metric, err := s.GeneratorStore.GetMetricsAtOrBefore(ctx, serial, atTime)
	if err != nil || !s.metricAllowed(ctx, serial, metric) {
		return nil, err
	}
	return metric, nil
}

func (s *tenantReportStore) GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*storage.MetricsSnapshot, error) {
	metrics, err := s.GeneratorStore.GetMetricsHistory(ctx, serial, since)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.MetricsSnapshot, 0)
	for _, metric := range metrics {
		if s.metricAllowed(ctx, serial, metric) {
			result = append(result, metric)
		}
	}
	return result, nil
}
