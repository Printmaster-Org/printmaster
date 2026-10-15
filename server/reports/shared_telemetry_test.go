package reports

import (
	"context"
	"testing"
	"time"

	"printmaster/server/storage"
)

func TestTenantReportSharedTelemetry(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, tenant := range []string{"a", "b"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: tenant, Name: tenant}); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []*storage.Agent{
		{AgentID: "owner", Token: "owner", TenantID: "a"},
		{AgentID: "reporter", Token: "reporter", TenantID: "a"},
		{AgentID: "foreign", Token: "foreign", TenantID: "b"},
	} {
		if err := store.RegisterAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	device := &storage.Device{AgentID: "owner"}
	device.Serial = "shared"
	if err := store.UpsertDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.SaveMetrics(ctx, &storage.MetricsSnapshot{Serial: device.Serial, AgentID: "reporter", Timestamp: now, PageCount: 120}); err != nil {
		t.Fatal(err)
	}
	scoped := &tenantReportStore{GeneratorStore: store, report: &storage.ReportDefinition{
		TenantIDs: []string{"a"}, AgentIDs: []string{"owner"},
	}}
	for _, getter := range []func() (*storage.MetricsSnapshot, error){
		func() (*storage.MetricsSnapshot, error) { return scoped.GetLatestMetrics(ctx, device.Serial) },
		func() (*storage.MetricsSnapshot, error) { return scoped.GetMetricsAtOrBefore(ctx, device.Serial, now) },
	} {
		metric, err := getter()
		if err != nil || metric == nil || metric.PageCount != 120 || metric.AgentID != "reporter" {
			t.Fatalf("owner-filtered report dropped same-tenant telemetry: %+v %v", metric, err)
		}
	}
	history, err := scoped.GetMetricsHistory(ctx, device.Serial, now.Add(-time.Hour))
	if err != nil || len(history) != 1 || history[0].AgentID != "reporter" {
		t.Fatalf("report history: %+v %v", history, err)
	}
	for _, metric := range []*storage.MetricsSnapshot{
		{Serial: device.Serial, AgentID: "foreign"},
		{Serial: "out-of-scope", AgentID: "reporter"},
		{Serial: "wrong-serial", AgentID: "owner"},
	} {
		allowed, err := scoped.metricAllowed(ctx, metric.Serial, metric)
		if err != nil || allowed {
			t.Fatalf("foreign/out-of-scope report metric accepted: %+v, %v", metric, err)
		}
	}
}
