package reports

import (
	"context"
	"fmt"
	"printmaster/common/logger"
	"printmaster/server/storage"
	"sync"
	"time"
)

// SchedulerStore defines storage operations for the scheduler.
type SchedulerStore interface {
	GeneratorStore

	// Reports
	GetReport(ctx context.Context, id int64) (*storage.ReportDefinition, error)

	// Schedules
	GetDueSchedules(ctx context.Context, before time.Time) ([]*storage.ReportSchedule, error)
	UpdateScheduleAfterRun(ctx context.Context, scheduleID int64, runID int64, nextRun time.Time, failed bool) error

	// Runs
	CreateReportRun(ctx context.Context, run *storage.ReportRun) error
	UpdateReportRun(ctx context.Context, run *storage.ReportRun) error
}

// Scheduler runs scheduled reports and ad-hoc report runs. Every execution
// path goes through execute so run bookkeeping is identical everywhere.
type Scheduler struct {
	store     SchedulerStore
	generator *Generator
	formatter *Formatter
	deliverer Deliverer
	logger    *logger.Logger

	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// SchedulerOption customizes a Scheduler at construction time.
type SchedulerOption func(*Scheduler)

// WithDeliverer sends completed scheduled runs to their email/webhook
// destinations. Without it, scheduled runs are generated and stored only.
func WithDeliverer(d Deliverer) SchedulerOption {
	return func(s *Scheduler) { s.deliverer = d }
}

// NewScheduler creates a new report scheduler.
func NewScheduler(store SchedulerStore, log *logger.Logger, opts ...SchedulerOption) *Scheduler {
	log = loggerOrDefault(log)
	s := &Scheduler{
		store:     store,
		generator: NewGenerator(store),
		formatter: NewFormatter(),
		logger:    log,
		interval:  1 * time.Minute,
		stopCh:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetInterval sets the check interval for due schedules.
func (s *Scheduler) SetInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = d
}

// Start begins the scheduler background loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	s.wg.Add(1)
	go s.loop()

	s.logger.Info("Report scheduler started", "interval", s.interval)
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	s.mu.Unlock()

	s.wg.Wait()
	s.logger.Info("Report scheduler stopped")
}

func (s *Scheduler) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run once immediately
	s.checkAndRun()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndRun()
		}
	}
}

func (s *Scheduler) checkAndRun() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Get schedules that are due
	schedules, err := s.store.GetDueSchedules(ctx, time.Now())
	if err != nil {
		s.logger.Error("Failed to get due schedules", "error", err)
		return
	}

	for _, schedule := range schedules {
		if err := s.runSchedule(ctx, schedule); err != nil {
			s.logger.Error("Failed to run scheduled report",
				"schedule_id", schedule.ID,
				"report_id", schedule.ReportID,
				"error", err)
		}
	}
}

// runSchedule executes a due schedule, advances its next run time, and
// delivers the result. The schedule always advances, even on failure, so a
// broken report cannot hot-loop every tick; failure_count records the streak.
func (s *Scheduler) runSchedule(ctx context.Context, schedule *storage.ReportSchedule) error {
	report, err := s.loadReport(ctx, schedule.ReportID)
	if err != nil {
		return err
	}

	run, result, execErr := s.execute(ctx, report, &schedule.ID, "scheduler")
	if run == nil {
		// No run record exists, so there is nothing to attach the failure to.
		// Still advance the schedule to avoid retrying every tick.
		if err := s.store.UpdateScheduleAfterRun(ctx, schedule.ID, 0, s.calculateNextRun(schedule), true); err != nil {
			s.logger.Error("Failed to advance report schedule", "schedule_id", schedule.ID, "error", err)
		}
		return execErr
	}

	nextRun := s.calculateNextRun(schedule)
	if err := s.store.UpdateScheduleAfterRun(ctx, schedule.ID, run.ID, nextRun, execErr != nil); err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	if execErr != nil {
		return execErr
	}

	s.logger.Info("Scheduled report completed",
		"report_id", report.ID,
		"report_name", report.Name,
		"schedule_id", schedule.ID,
		"run_id", run.ID,
		"row_count", result.RowCount,
		"duration_ms", run.DurationMS,
		"next_run_at", nextRun)

	s.deliver(ctx, report, run, result)
	return nil
}

// RunScheduleNow executes a schedule immediately on behalf of runBy and
// delivers the result exactly like a timed run, without moving next_run_at.
func (s *Scheduler) RunScheduleNow(ctx context.Context, schedule *storage.ReportSchedule, runBy string) (*storage.ReportRun, error) {
	report, err := s.loadReport(ctx, schedule.ReportID)
	if err != nil {
		return nil, err
	}
	run, result, err := s.execute(ctx, report, &schedule.ID, runBy)
	if err != nil {
		return run, err
	}
	s.logger.Info("Report schedule run on demand",
		"report_id", report.ID, "schedule_id", schedule.ID, "run_id", run.ID, "run_by", runBy, "row_count", result.RowCount)
	s.deliver(ctx, report, run, result)
	return run, nil
}

// RunNow executes a report immediately (ad-hoc run). Ad-hoc runs are returned
// to the caller for download and are never delivered by email or webhook.
func (s *Scheduler) RunNow(ctx context.Context, reportID int64, runBy string) (*storage.ReportRun, error) {
	report, err := s.loadReport(ctx, reportID)
	if err != nil {
		return nil, err
	}
	run, _, err := s.execute(ctx, report, nil, runBy)
	return run, err
}

func (s *Scheduler) loadReport(ctx context.Context, reportID int64) (*storage.ReportDefinition, error) {
	report, err := s.store.GetReport(ctx, reportID)
	if err != nil {
		return nil, fmt.Errorf("get report: %w", err)
	}
	if report == nil {
		return nil, fmt.Errorf("report %d not found", reportID)
	}
	return report, nil
}

// execute creates a run record, generates and formats the report, and stores
// the outcome on the run. It returns a nil run only if the run record could
// not be created; otherwise the run reflects success or failure.
func (s *Scheduler) execute(ctx context.Context, report *storage.ReportDefinition, scheduleID *int64, runBy string) (*storage.ReportRun, *GenerateResult, error) {
	run := &storage.ReportRun{
		ReportID:   report.ID,
		ScheduleID: scheduleID,
		Status:     storage.ReportStatusRunning,
		Format:     report.Format,
		StartedAt:  time.Now(),
		RunBy:      runBy,
	}
	if err := s.store.CreateReportRun(ctx, run); err != nil {
		return nil, nil, fmt.Errorf("create run: %w", err)
	}

	startTime, endTime := s.calculateTimeRange(report)
	result, err := s.generator.Generate(ctx, GenerateParams{Report: report, StartTime: startTime, EndTime: endTime})
	if err != nil {
		s.failRun(ctx, run, err.Error())
		return run, nil, fmt.Errorf("generate report: %w", err)
	}

	data, err := s.format(report, result)
	if err != nil {
		s.failRun(ctx, run, fmt.Sprintf("format error: %v", err))
		return run, nil, fmt.Errorf("format report: %w", err)
	}

	now := time.Now()
	run.Status = storage.ReportStatusCompleted
	run.CompletedAt = &now
	run.DurationMS = now.Sub(run.StartedAt).Milliseconds()
	run.RowCount = result.RowCount
	run.ResultSize = int64(len(data))
	run.ResultData = string(data) // Stored inline; results are bounded by report row limits.
	if err := s.store.UpdateReportRun(ctx, run); err != nil {
		return run, nil, fmt.Errorf("update run: %w", err)
	}
	return run, result, nil
}

func (s *Scheduler) format(report *storage.ReportDefinition, result *GenerateResult) ([]byte, error) {
	switch report.Format {
	case storage.ReportFormatCSV:
		return s.formatter.FormatCSV(result)
	case storage.ReportFormatHTML:
		return s.formatter.FormatHTML(result, report.Name)
	default:
		return s.formatter.FormatJSON(result, true)
	}
}

func (s *Scheduler) failRun(ctx context.Context, run *storage.ReportRun, message string) {
	now := time.Now()
	run.Status = storage.ReportStatusFailed
	run.CompletedAt = &now
	run.DurationMS = now.Sub(run.StartedAt).Milliseconds()
	run.ErrorMessage = message
	if err := s.store.UpdateReportRun(ctx, run); err != nil {
		s.logger.Error("Failed to record report run failure", "run_id", run.ID, "error", err)
	}
}

// deliver hands a completed run to the configured Deliverer. Delivery failures
// do not fail the run (the report itself was produced and is downloadable);
// they are logged and recorded in the run's error message for operators.
func (s *Scheduler) deliver(ctx context.Context, report *storage.ReportDefinition, run *storage.ReportRun, result *GenerateResult) {
	if s.deliverer == nil {
		if len(report.EmailRecipients) > 0 || report.WebhookURL != "" {
			s.logger.Warn("Report has delivery destinations but no deliverer is configured",
				"report_id", report.ID, "run_id", run.ID)
		}
		return
	}
	err := s.deliverer.Deliver(ctx, Delivery{Report: report, Run: run, Result: result})
	if err == nil {
		return
	}
	s.logger.Error("Report delivery failed", "report_id", report.ID, "run_id", run.ID, "error", err)
	run.ErrorMessage = "delivery failed: " + err.Error()
	if uerr := s.store.UpdateReportRun(ctx, run); uerr != nil {
		s.logger.Error("Failed to record report delivery failure", "run_id", run.ID, "error", uerr)
	}
}

func (s *Scheduler) calculateTimeRange(report *storage.ReportDefinition) (time.Time, time.Time) {
	now := time.Now().UTC()
	endTime := now

	switch report.TimeRangeType {
	case "current":
		return now, endTime
	case "last_24h":
		return now.Add(-24 * time.Hour), endTime
	case "last_7d":
		return now.Add(-7 * 24 * time.Hour), endTime
	case "last_30d":
		return now.Add(-30 * 24 * time.Hour), endTime
	case "last_90d":
		return now.Add(-90 * 24 * time.Hour), endTime
	case "custom":
		if report.TimeRangeDays > 0 {
			return now.Add(-time.Duration(report.TimeRangeDays) * 24 * time.Hour), endTime
		}
		// Try to parse absolute dates
		if report.TimeRangeStart != "" && report.TimeRangeEnd != "" {
			start, err1 := time.Parse("2006-01-02", report.TimeRangeStart)
			end, err2 := time.Parse("2006-01-02", report.TimeRangeEnd)
			if err1 == nil && err2 == nil {
				return start, end
			}
		}
	}

	// Default to last 30 days
	return now.Add(-30 * 24 * time.Hour), endTime
}

func (s *Scheduler) calculateNextRun(schedule *storage.ReportSchedule) time.Time {
	return NextRun(schedule, time.Now())
}

// NextRun returns the first occurrence of schedule strictly after now, in UTC.
// TimeOfDay ("HH:MM") and the weekday/day-of-month are interpreted in the
// schedule's Timezone (UTC if empty or unknown). Calendar arithmetic uses
// AddDate so DST transitions keep the configured wall-clock time. Monthly
// days are clamped to 1-28 so every month has the configured day. Unknown
// frequencies fall back to 24 hours from now.
func NextRun(schedule *storage.ReportSchedule, now time.Time) time.Time {
	var hour, minute int
	fmt.Sscanf(schedule.TimeOfDay, "%d:%d", &hour, &minute)

	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}
	nowLocal := now.In(loc)
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), hour, minute, 0, 0, loc)

	switch schedule.Frequency {
	case storage.ScheduleFrequencyDaily:
		next := today
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next.UTC()

	case storage.ScheduleFrequencyWeekly:
		daysUntil := (schedule.DayOfWeek - int(today.Weekday()) + 7) % 7
		next := today.AddDate(0, 0, daysUntil)
		if !next.After(now) {
			next = next.AddDate(0, 0, 7)
		}
		return next.UTC()

	case storage.ScheduleFrequencyMonthly:
		day := schedule.DayOfMonth
		if day < 1 {
			day = 1
		} else if day > 28 {
			day = 28
		}
		next := time.Date(nowLocal.Year(), nowLocal.Month(), day, hour, minute, 0, 0, loc)
		if !next.After(now) {
			next = next.AddDate(0, 1, 0)
		}
		return next.UTC()
	}

	return now.Add(24 * time.Hour)
}

// loggerOrDefault guarantees a usable logger: *logger.Logger methods are not
// nil-safe, and callers (notably tests) may construct reports types before the
// server logger exists.
func loggerOrDefault(log *logger.Logger) *logger.Logger {
	if log != nil {
		return log
	}
	return logger.New(logger.INFO, "", 100)
}
