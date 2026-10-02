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

func (s *tenantReportStore) agentAllowed(agent *storage.Agent) bool {
	if agent == nil || !reportContains(s.report.TenantIDs, agent.TenantID) ||
		(len(s.report.AgentIDs) > 0 && !reportContains(s.report.AgentIDs, agent.AgentID)) {
		return false
	}
	if len(s.report.SiteIDs) == 0 {
		return true
	}
	for _, id := range agent.SiteIDs {
		if reportContains(s.report.SiteIDs, id) {
			return true
		}
	}
	return false
}

func (s *tenantReportStore) ListAgents(ctx context.Context) ([]*storage.Agent, error) {
	agents, err := s.GeneratorStore.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.Agent, 0)
	for _, agent := range agents {
		if len(s.report.SiteIDs) > 0 {
			if source, ok := s.GeneratorStore.(interface {
				GetAgentSiteIDs(context.Context, string) ([]string, error)
			}); ok {
				ids, err := source.GetAgentSiteIDs(ctx, agent.AgentID)
				if err != nil {
					return nil, err
				}
				copy := *agent
				copy.SiteIDs = ids
				agent = &copy
			}
		}
		if s.agentAllowed(agent) {
			result = append(result, agent)
		}
	}
	return result, nil
}

func (s *tenantReportStore) GetAgent(ctx context.Context, id string) (*storage.Agent, error) {
	agent, err := s.GeneratorStore.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.agentAllowed(agent) {
		return nil, fmt.Errorf("agent outside report scope")
	}
	return agent, nil
}

func (s *tenantReportStore) ListAllDevices(ctx context.Context) ([]*storage.Device, error) {
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(agents))
	for _, agent := range agents {
		allowed[agent.AgentID] = true
	}
	devices, err := s.GeneratorStore.ListAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.Device, 0)
	s.deviceOwners = make(map[string]string)
	for _, device := range devices {
		if allowed[device.AgentID] {
			result = append(result, device)
			s.deviceOwners[device.Serial] = device.AgentID
		}
	}
	return result, nil
}

func (s *tenantReportStore) ListTenants(ctx context.Context) ([]*storage.Tenant, error) {
	tenants, err := s.GeneratorStore.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.Tenant, 0)
	for _, tenant := range tenants {
		if reportContains(s.report.TenantIDs, tenant.ID) {
			result = append(result, tenant)
		}
	}
	return result, nil
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
	sites, err := s.GeneratorStore.ListSitesByTenant(ctx, id)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.Site, 0)
	for _, site := range sites {
		if site.TenantID == id && (len(s.report.SiteIDs) == 0 || reportContains(s.report.SiteIDs, site.ID)) {
			result = append(result, site)
		}
	}
	return result, nil
}

func (s *tenantReportStore) ListAlerts(ctx context.Context, filter storage.AlertFilter) ([]*storage.Alert, error) {
	// Do not let underlying pagination exclude permitted rows before scoping.
	filter.Limit, filter.Offset = 0, 0
	alerts, err := s.GeneratorStore.ListAlerts(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := make([]*storage.Alert, 0)
	for _, alert := range alerts {
		if reportContains(s.report.TenantIDs, alert.TenantID) &&
			(len(s.report.SiteIDs) == 0 || reportContains(s.report.SiteIDs, alert.SiteID)) &&
			(len(s.report.AgentIDs) == 0 || reportContains(s.report.AgentIDs, alert.AgentID)) {
			result = append(result, alert)
		}
	}
	return result, nil
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
