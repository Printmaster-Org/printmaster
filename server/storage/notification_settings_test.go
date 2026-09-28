package storage

import (
	"context"
	"testing"
)

func TestTenantNotificationSettingsLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.CreateTenant(ctx, &Tenant{ID: "tenant-notify", Name: "Notify Tenant"}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	got, err := s.GetTenantNotificationSettings(ctx, "tenant-notify")
	if err != nil {
		t.Fatalf("GetTenantNotificationSettings (initial): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil before configuration, got %+v", got)
	}

	rec := &NotificationSettings{
		TenantID:            "tenant-notify",
		Enabled:             true,
		Recipients:          []string{"it@acme.example", "ops@acme.example"},
		NotifyOnCritical:    true,
		NotifyOnWarning:     true,
		DailySummaryEnabled: true,
		DailySummaryTime:    "07:30",
		UpdatedBy:           "admin",
	}
	if err := s.UpsertTenantNotificationSettings(ctx, rec); err != nil {
		t.Fatalf("UpsertTenantNotificationSettings: %v", err)
	}

	got, err = s.GetTenantNotificationSettings(ctx, "tenant-notify")
	if err != nil {
		t.Fatalf("GetTenantNotificationSettings: %v", err)
	}
	if got == nil {
		t.Fatal("expected settings after upsert")
	}
	if !got.Enabled || len(got.Recipients) != 2 || got.Recipients[0] != "it@acme.example" {
		t.Errorf("unexpected record: %+v", got)
	}
	if got.DailySummaryTime != "07:30" || got.DailySummaryTimezone != "Local" {
		t.Errorf("schedule defaults not applied: %+v", got)
	}

	rec.Recipients = []string{"only@acme.example"}
	rec.NotifyOnWarning = false
	if err := s.UpsertTenantNotificationSettings(ctx, rec); err != nil {
		t.Fatalf("UpsertTenantNotificationSettings (update): %v", err)
	}
	got, _ = s.GetTenantNotificationSettings(ctx, "tenant-notify")
	if len(got.Recipients) != 1 || got.NotifyOnWarning {
		t.Errorf("update not applied: %+v", got)
	}

	all, err := s.ListTenantNotificationSettings(ctx)
	if err != nil {
		t.Fatalf("ListTenantNotificationSettings: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected one record, got %d", len(all))
	}

	if err := s.DeleteTenantNotificationSettings(ctx, "tenant-notify"); err != nil {
		t.Fatalf("DeleteTenantNotificationSettings: %v", err)
	}
	got, err = s.GetTenantNotificationSettings(ctx, "tenant-notify")
	if err != nil {
		t.Fatalf("GetTenantNotificationSettings after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}
