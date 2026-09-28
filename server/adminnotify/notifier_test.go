package adminnotify

import (
	"context"
	"sync"
	"testing"
	"time"

	"printmaster/server/storage"
)

type mockStore struct {
	mu      sync.Mutex
	alerts  map[int64]*storage.Alert
	nextID  int64
	tenants map[string]*storage.NotificationSettings
}

func newMockStore() *mockStore {
	return &mockStore{
		alerts:  make(map[int64]*storage.Alert),
		tenants: make(map[string]*storage.NotificationSettings),
	}
}

func (m *mockStore) CreateAlert(_ context.Context, alert *storage.Alert) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	alert.ID = m.nextID
	cp := *alert
	m.alerts[alert.ID] = &cp
	return alert.ID, nil
}

func (m *mockStore) ListActiveAlerts(_ context.Context, filters storage.AlertFilters) ([]storage.Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []storage.Alert
	for _, a := range m.alerts {
		if a.Status != storage.AlertStatusActive && a.Status != storage.AlertStatusAcknowledged {
			continue
		}
		if filters.Type != "" && a.Type != filters.Type {
			continue
		}
		if filters.TenantID != "" && a.TenantID != filters.TenantID {
			continue
		}
		out = append(out, *a)
	}
	return out, nil
}

func (m *mockStore) ResolveAlert(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.alerts[id]; ok {
		a.Status = storage.AlertStatusResolved
	}
	return nil
}

func (m *mockStore) UpdateAlertNotificationStatus(_ context.Context, id int64, sent int, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.alerts[id]; ok {
		a.NotificationsSent = sent
	}
	return nil
}

func (m *mockStore) GetTenantNotificationSettings(_ context.Context, tenantID string) (*storage.NotificationSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tenants[tenantID], nil
}

func (m *mockStore) ListTenantNotificationSettings(context.Context) ([]*storage.NotificationSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*storage.NotificationSettings, 0, len(m.tenants))
	for _, rec := range m.tenants {
		out = append(out, rec)
	}
	return out, nil
}

func (m *mockStore) UpsertTenantNotificationSettings(_ context.Context, rec *storage.NotificationSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tenants[rec.TenantID] = rec
	return nil
}

func (m *mockStore) DeleteTenantNotificationSettings(_ context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tenants, tenantID)
	return nil
}

func (m *mockStore) ListTenants(context.Context) ([]*storage.Tenant, error) {
	return nil, nil
}

type recorder struct {
	mu   sync.Mutex
	sent []string
}

func (r *recorder) mailer(to, _, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, to)
	return nil
}

func (r *recorder) recipients() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.sent...)
}

func fleetEnabled() Config {
	return Config{
		Enabled:          true,
		Recipients:       []string{"ops@example.com"},
		NotifyOnCritical: true,
	}
}

func newTestNotifier(t *testing.T, store Store, mailer Mailer, cfg func() Config) *Notifier {
	t.Helper()
	return New(Options{Store: store, Mailer: mailer, FleetConfig: cfg})
}

func TestRaiseLatchesRepeatEvents(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	rec := &recorder{}
	n := newTestNotifier(t, store, rec.mailer, fleetEnabled)
	ev := Event{Key: "oidc.credentials.entra", Severity: storage.AlertSeverityCritical, Title: "secret expired", Message: "rotate it"}

	created, err := n.Raise(context.Background(), ev)
	if err != nil || !created {
		t.Fatalf("first raise: created=%v err=%v", created, err)
	}
	for i := 0; i < 3; i++ {
		created, err = n.Raise(context.Background(), ev)
		if err != nil {
			t.Fatalf("repeat raise: %v", err)
		}
		if created {
			t.Fatal("repeat raise created a second alert; latch failed")
		}
	}
	if got := rec.recipients(); len(got) != 1 {
		t.Fatalf("expected exactly one email, got %v", got)
	}
}

func TestClearAllowsRaiseAgain(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	rec := &recorder{}
	n := newTestNotifier(t, store, rec.mailer, fleetEnabled)
	ev := Event{Key: "oidc.credentials.entra", Severity: storage.AlertSeverityCritical, Title: "secret expired"}

	if _, err := n.Raise(context.Background(), ev); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if err := n.Clear(context.Background(), ev.Key, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	created, err := n.Raise(context.Background(), ev)
	if err != nil || !created {
		t.Fatalf("raise after clear: created=%v err=%v", created, err)
	}
	if got := rec.recipients(); len(got) != 2 {
		t.Fatalf("expected two emails, got %v", got)
	}
}

func TestSeverityGateSuppressesEmailButStillLatches(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	rec := &recorder{}
	n := newTestNotifier(t, store, rec.mailer, fleetEnabled) // warnings disabled

	created, err := n.Raise(context.Background(), Event{Key: "k", Severity: storage.AlertSeverityWarning, Title: "t"})
	if err != nil || !created {
		t.Fatalf("raise: created=%v err=%v", created, err)
	}
	if got := rec.recipients(); len(got) != 0 {
		t.Fatalf("warning should not email when notify_on_warning is off, got %v", got)
	}
}

func TestTenantEventNotifiesBothLevels(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	store.tenants["acme"] = &storage.NotificationSettings{
		TenantID:         "acme",
		Enabled:          true,
		Recipients:       []string{"it@acme.example"},
		NotifyOnCritical: true,
	}
	rec := &recorder{}
	n := newTestNotifier(t, store, rec.mailer, fleetEnabled)

	if _, err := n.Raise(context.Background(), Event{
		Key: "oidc.credentials.acme", Severity: storage.AlertSeverityCritical,
		Title: "secret expired", TenantID: "acme",
	}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	got := rec.recipients()
	if len(got) != 2 {
		t.Fatalf("expected fleet + tenant recipients, got %v", got)
	}
}

func TestTenantOnlyAudienceSkipsFleet(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	store.tenants["acme"] = &storage.NotificationSettings{
		TenantID:         "acme",
		Enabled:          true,
		Recipients:       []string{"it@acme.example"},
		NotifyOnCritical: true,
	}
	rec := &recorder{}
	n := newTestNotifier(t, store, rec.mailer, fleetEnabled)

	if _, err := n.Raise(context.Background(), Event{
		Key: "k", Severity: storage.AlertSeverityCritical, Title: "t",
		TenantID: "acme", Audience: AudienceTenant,
	}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	got := rec.recipients()
	if len(got) != 1 || got[0] != "it@acme.example" {
		t.Fatalf("expected tenant-only delivery, got %v", got)
	}
}

func TestLatchIsPerTenant(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	n := newTestNotifier(t, store, nil, fleetEnabled)

	ev := Event{Key: "agent.offline", Severity: storage.AlertSeverityCritical, Title: "t", TenantID: "a"}
	if created, _ := n.Raise(context.Background(), ev); !created {
		t.Fatal("expected first raise to create")
	}
	ev.TenantID = "b"
	if created, _ := n.Raise(context.Background(), ev); !created {
		t.Fatal("a different tenant must get its own latch")
	}
}

func TestNormalizeRecipientsRejectsInjection(t *testing.T) {
	t.Parallel()
	got := normalizeRecipients([]string{
		"ok@example.com",
		"OK@example.com",
		"bad\r\nBcc: attacker@evil.example",
		"not-an-email",
		"   ",
	})
	if len(got) != 1 || got[0] != "ok@example.com" {
		t.Fatalf("unexpected recipients: %v", got)
	}
}

func TestSummaryDue(t *testing.T) {
	t.Parallel()
	cfg := Config{DailySummaryTime: "08:00", DailySummaryTimezone: "UTC"}

	before := time.Date(2026, 3, 1, 7, 59, 0, 0, time.UTC)
	if due, _ := summaryDue(before, cfg); due {
		t.Error("should not be due before the scheduled time")
	}
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	due, day := summaryDue(at, cfg)
	if !due || day != "2026-03-01" {
		t.Errorf("expected due at scheduled time, got due=%v day=%s", due, day)
	}
	stale := time.Date(2026, 3, 1, 23, 0, 0, 0, time.UTC)
	if due, _ := summaryDue(stale, cfg); due {
		t.Error("should not catch up many hours later")
	}
}

func TestValidators(t *testing.T) {
	t.Parallel()
	if !ValidSummaryTime("23:59") || ValidSummaryTime("24:00") || ValidSummaryTime("8am") {
		t.Error("ValidSummaryTime is incorrect")
	}
	if !ValidTimezone("") || !ValidTimezone("Local") || ValidTimezone("Mars/Olympus") {
		t.Error("ValidTimezone is incorrect")
	}
}

func TestValidateSettingsRequiresRecipientsWhenEnabled(t *testing.T) {
	t.Parallel()
	rec := &storage.NotificationSettings{Enabled: true}
	if err := ValidateSettings(rec); err == nil {
		t.Error("expected error when enabling with no recipients")
	}
	rec = &storage.NotificationSettings{Enabled: true, Recipients: []string{"a@b.com"}, DailySummaryTime: "9:30"}
	if err := ValidateSettings(rec); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if rec.DailySummaryTimezone != "Local" {
		t.Errorf("timezone default not applied: %q", rec.DailySummaryTimezone)
	}
}
