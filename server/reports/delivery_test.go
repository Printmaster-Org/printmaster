package reports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"printmaster/server/storage"
)

type sentEmail struct{ to, subject, html, text string }

type fakeMailer struct {
	mu     sync.Mutex
	sent   []sentEmail
	failTo map[string]bool
}

func (m *fakeMailer) send(to, subject, html, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failTo[to] {
		return errors.New("smtp refused")
	}
	m.sent = append(m.sent, sentEmail{to, subject, html, text})
	return nil
}

type fakePoster struct {
	mu       sync.Mutex
	urls     []string
	payloads []ReportWebhookPayload
	err      error
}

func (p *fakePoster) PostJSON(ctx context.Context, url string, headers map[string]string, payload interface{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.urls = append(p.urls, url)
	p.payloads = append(p.payloads, payload.(ReportWebhookPayload))
	return p.err
}

func testDelivery(rows int) Delivery {
	result := &GenerateResult{Columns: []string{"serial"}, Summary: map[string]any{"total": rows}, RowCount: rows}
	for i := 0; i < rows; i++ {
		result.Rows = append(result.Rows, map[string]any{"serial": fmt.Sprintf("SN%03d", i)})
	}
	scheduleID := int64(9)
	now := time.Now()
	return Delivery{
		Report: &storage.ReportDefinition{ID: 3, Name: "Fleet", Type: storage.ReportTypeDeviceInventory},
		Run:    &storage.ReportRun{ID: 42, ScheduleID: &scheduleID, Status: storage.ReportStatusCompleted, Format: storage.ReportFormatCSV, StartedAt: now, CompletedAt: &now},
		Result: result,
	}
}

func TestNotificationDelivererEmailsEachRecipient(t *testing.T) {
	mailer := &fakeMailer{}
	d := NewNotificationDeliverer(mailer.send, nil, testLogger())
	del := testDelivery(2)
	del.Report.EmailRecipients = []string{"a@example.com", "b@example.com"}

	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(mailer.sent) != 2 || mailer.sent[0].to != "a@example.com" || mailer.sent[1].to != "b@example.com" {
		t.Fatalf("unexpected sends: %+v", mailer.sent)
	}
	msg := mailer.sent[0]
	if msg.subject != "PrintMaster report: Fleet" || !strings.Contains(msg.html, "SN001") || !strings.Contains(msg.text, "Rows: 2") {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if strings.Contains(msg.html, "Showing the first") {
		t.Fatal("small result should not carry a truncation note")
	}
}

func TestNotificationDelivererTruncatesLargeResults(t *testing.T) {
	mailer := &fakeMailer{}
	poster := &fakePoster{}
	d := NewNotificationDeliverer(mailer.send, poster, testLogger())
	del := testDelivery(deliveryPreviewRows + 5)
	del.Report.EmailRecipients = []string{"a@example.com"}
	del.Report.WebhookURL = "https://hooks.example.com/r"

	if err := d.Deliver(context.Background(), del); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !strings.Contains(mailer.sent[0].html, "Showing the first 100 of 105 rows") {
		t.Fatal("expected truncation note in email HTML")
	}
	if strings.Contains(mailer.sent[0].html, "SN104") {
		t.Fatal("email should not include rows beyond the preview")
	}
	p := poster.payloads[0]
	if p.Event != "report.completed" || p.RunID != 42 || p.ReportID != 3 || p.RowCount != 105 ||
		len(p.Rows) != deliveryPreviewRows || !p.RowsTruncated || p.ScheduleID == nil || *p.ScheduleID != 9 {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if len(del.Result.Rows) != deliveryPreviewRows+5 {
		t.Fatal("preview must not mutate the original result")
	}
}

func TestNotificationDelivererJoinsFailuresAndContinues(t *testing.T) {
	mailer := &fakeMailer{failTo: map[string]bool{"bad@example.com": true}}
	poster := &fakePoster{err: errors.New("boom")}
	d := NewNotificationDeliverer(mailer.send, poster, testLogger())
	del := testDelivery(1)
	del.Report.EmailRecipients = []string{"bad@example.com", "good@example.com"}
	del.Report.WebhookURL = "https://hooks.example.com/r"

	err := d.Deliver(context.Background(), del)
	if err == nil || !strings.Contains(err.Error(), "bad@example.com") || !strings.Contains(err.Error(), "webhook: boom") {
		t.Fatalf("expected joined errors, got %v", err)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to != "good@example.com" {
		t.Fatalf("later recipients must still be attempted: %+v", mailer.sent)
	}
}

func TestNotificationDelivererMissingChannelsFailLoudly(t *testing.T) {
	d := NewNotificationDeliverer(nil, nil, testLogger())
	del := testDelivery(1)
	del.Report.EmailRecipients = []string{"a@example.com"}
	del.Report.WebhookURL = "https://hooks.example.com/r"

	err := d.Deliver(context.Background(), del)
	if err == nil || !strings.Contains(err.Error(), "email delivery is not configured") ||
		!strings.Contains(err.Error(), "webhook delivery is not configured") {
		t.Fatalf("expected not-configured errors, got %v", err)
	}
}

func TestNotificationDelivererNoDestinationsIsNoop(t *testing.T) {
	d := NewNotificationDeliverer(nil, nil, testLogger())
	if err := d.Deliver(context.Background(), testDelivery(1)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
}

// recordingDeliverer captures deliveries made by the scheduler.
type recordingDeliverer struct {
	mu   sync.Mutex
	got  []Delivery
	fail error
}

func (r *recordingDeliverer) Deliver(ctx context.Context, d Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, d)
	return r.fail
}

func scheduledStore() *mockSchedulerStore {
	store := newMockSchedulerStore()
	store.reports[1] = &storage.ReportDefinition{
		ID: 1, Name: "Inventory", Type: storage.ReportTypeDeviceInventory, Format: storage.ReportFormatCSV,
		TimeRangeType: "last_24h", EmailRecipients: []string{"a@example.com"},
	}
	store.schedules = []*storage.ReportSchedule{{
		ID: 7, ReportID: 1, Frequency: storage.ScheduleFrequencyDaily, TimeOfDay: "08:00",
		NextRunAt: time.Now().Add(-time.Minute),
	}}
	return store
}

func TestSchedulerDeliversScheduledRuns(t *testing.T) {
	store := scheduledStore()
	rec := &recordingDeliverer{}
	s := NewScheduler(store, testLogger(), WithDeliverer(rec))

	if err := s.runSchedule(context.Background(), store.schedules[0]); err != nil {
		t.Fatalf("runSchedule: %v", err)
	}
	if len(rec.got) != 1 || rec.got[0].Run.Status != storage.ReportStatusCompleted || rec.got[0].Report.ID != 1 {
		t.Fatalf("unexpected deliveries: %+v", rec.got)
	}
	calls := store.getScheduleCalls()
	if len(calls) != 1 || calls[0].failed || !calls[0].nextRun.After(time.Now()) {
		t.Fatalf("schedule not advanced correctly: %+v", calls)
	}
}

func TestSchedulerRecordsDeliveryFailureWithoutFailingRun(t *testing.T) {
	store := scheduledStore()
	rec := &recordingDeliverer{fail: errors.New("smtp down")}
	s := NewScheduler(store, testLogger(), WithDeliverer(rec))

	if err := s.runSchedule(context.Background(), store.schedules[0]); err != nil {
		t.Fatalf("runSchedule: %v", err)
	}
	run := store.getRuns()[0]
	if run.Status != storage.ReportStatusCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	if run.ErrorMessage != "delivery failed: smtp down" {
		t.Fatalf("run error = %q", run.ErrorMessage)
	}
	if store.getScheduleCalls()[0].failed {
		t.Fatal("delivery failure must not count as a schedule failure")
	}
}

func TestSchedulerRunScheduleNowDeliversWithoutAdvancing(t *testing.T) {
	store := scheduledStore()
	rec := &recordingDeliverer{}
	s := NewScheduler(store, testLogger(), WithDeliverer(rec))

	run, err := s.RunScheduleNow(context.Background(), store.schedules[0], "alice")
	if err != nil {
		t.Fatalf("RunScheduleNow: %v", err)
	}
	if run.RunBy != "alice" || run.ScheduleID == nil || *run.ScheduleID != 7 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if len(rec.got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(rec.got))
	}
	if calls := store.getScheduleCalls(); len(calls) != 0 {
		t.Fatalf("RunScheduleNow must not move next_run_at: %+v", calls)
	}
}

func TestSchedulerRunNowNeverDelivers(t *testing.T) {
	store := scheduledStore()
	rec := &recordingDeliverer{}
	s := NewScheduler(store, testLogger(), WithDeliverer(rec))

	if _, err := s.RunNow(context.Background(), 1, "alice"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if len(rec.got) != 0 {
		t.Fatal("ad-hoc runs must not be delivered")
	}
}

func TestSchedulerFailedRunAdvancesScheduleWithoutDelivery(t *testing.T) {
	store := scheduledStore()
	store.reports[1].Type = "not_a_real_type"
	rec := &recordingDeliverer{}
	s := NewScheduler(store, testLogger(), WithDeliverer(rec))

	if err := s.runSchedule(context.Background(), store.schedules[0]); err == nil {
		t.Fatal("expected generation error")
	}
	calls := store.getScheduleCalls()
	if len(calls) != 1 || !calls[0].failed || !calls[0].nextRun.After(time.Now()) {
		t.Fatalf("failed run must still advance the schedule: %+v", calls)
	}
	if len(rec.got) != 0 {
		t.Fatal("failed runs must not be delivered")
	}
}

func TestNextRun(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	base := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC) // Wednesday
	tests := []struct {
		name     string
		schedule storage.ReportSchedule
		now      time.Time
		want     time.Time
	}{
		{"daily later today", storage.ReportSchedule{Frequency: "daily", TimeOfDay: "15:30"}, base, time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)},
		{"daily already passed", storage.ReportSchedule{Frequency: "daily", TimeOfDay: "08:00"}, base, time.Date(2026, 3, 5, 8, 0, 0, 0, time.UTC)},
		{"daily exactly now rolls over", storage.ReportSchedule{Frequency: "daily", TimeOfDay: "12:00"}, base, time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)},
		{"weekly friday", storage.ReportSchedule{Frequency: "weekly", DayOfWeek: 5, TimeOfDay: "09:00"}, base, time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)},
		{"weekly same day passed", storage.ReportSchedule{Frequency: "weekly", DayOfWeek: 3, TimeOfDay: "08:00"}, base, time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)},
		{"monthly next month", storage.ReportSchedule{Frequency: "monthly", DayOfMonth: 1, TimeOfDay: "06:00"}, base, time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC)},
		{"monthly clamps 31 to 28", storage.ReportSchedule{Frequency: "monthly", DayOfMonth: 31, TimeOfDay: "06:00"}, base, time.Date(2026, 3, 28, 6, 0, 0, 0, time.UTC)},
		// US DST starts 2026-03-08: 08:00 New York is 13:00Z before and 12:00Z after.
		{"timezone across DST", storage.ReportSchedule{Frequency: "weekly", DayOfWeek: 1, TimeOfDay: "08:00", Timezone: "America/New_York"}, base, time.Date(2026, 3, 9, 8, 0, 0, 0, ny).UTC()},
		{"unknown frequency", storage.ReportSchedule{Frequency: "hourly"}, base, base.Add(24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NextRun(&tt.schedule, tt.now); !got.Equal(tt.want) {
				t.Fatalf("NextRun = %v, want %v", got, tt.want)
			}
		})
	}
}
