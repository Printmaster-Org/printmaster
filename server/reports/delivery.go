package reports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"printmaster/common/logger"
	"printmaster/server/storage"
)

// deliveryPreviewRows caps how many result rows are embedded in an email body
// or webhook payload. The full result always remains downloadable from the
// server, so deliveries stay small enough for mail relays and webhook receivers.
const deliveryPreviewRows = 100

// Mailer sends one email to a single recipient. Its signature matches the
// server's sendHTMLEmail so the existing SMTP configuration is reused as-is.
type Mailer func(to, subject, htmlBody, textBody string) error

// WebhookPoster posts a JSON payload to an operator-supplied URL. The server
// injects webhook.Poster, which enforces the shared SSRF guard and retries.
type WebhookPoster interface {
	PostJSON(ctx context.Context, url string, headers map[string]string, payload interface{}) error
}

// Delivery describes one successfully generated report run to hand off to its
// configured destinations.
type Delivery struct {
	Report *storage.ReportDefinition
	Run    *storage.ReportRun
	Result *GenerateResult
}

// Deliverer sends completed report runs to their configured destinations.
// Implementations must return a non-nil error when any destination failed so
// the scheduler can record it on the run.
type Deliverer interface {
	Deliver(ctx context.Context, d Delivery) error
}

// NotificationDeliverer delivers report runs by email and/or webhook based on
// the report definition's EmailRecipients and WebhookURL.
type NotificationDeliverer struct {
	mailer    Mailer
	poster    WebhookPoster
	formatter *Formatter
	logger    *logger.Logger
}

// NewNotificationDeliverer builds a deliverer. A nil mailer or poster makes the
// corresponding channel fail loudly (logged and recorded on the run) instead of
// silently dropping a delivery the operator asked for.
func NewNotificationDeliverer(mailer Mailer, poster WebhookPoster, log *logger.Logger) *NotificationDeliverer {
	return &NotificationDeliverer{mailer: mailer, poster: poster, formatter: NewFormatter(), logger: loggerOrDefault(log)}
}

// Deliver sends the run to every configured destination. Every destination is
// attempted even if an earlier one fails; all failures are joined.
func (d *NotificationDeliverer) Deliver(ctx context.Context, del Delivery) error {
	if del.Report == nil || del.Run == nil || del.Result == nil {
		return fmt.Errorf("incomplete delivery request")
	}
	recipients := del.Report.EmailRecipients
	webhookURL := strings.TrimSpace(del.Report.WebhookURL)
	if len(recipients) == 0 && webhookURL == "" {
		return nil
	}

	var errs []error
	if len(recipients) > 0 {
		errs = append(errs, d.deliverEmail(del, recipients)...)
	}
	if webhookURL != "" {
		if err := d.deliverWebhook(ctx, del, webhookURL); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *NotificationDeliverer) deliverEmail(del Delivery, recipients []string) []error {
	if d.mailer == nil {
		d.logger.Warn("Report email delivery requested but no mailer is configured",
			"report_id", del.Report.ID, "run_id", del.Run.ID)
		return []error{fmt.Errorf("email delivery is not configured on this server")}
	}

	preview, truncated := previewResult(del.Result)
	htmlBody, err := d.formatter.FormatHTML(preview, del.Report.Name)
	if err != nil {
		return []error{fmt.Errorf("render email body: %w", err)}
	}
	html := string(htmlBody)
	if truncated {
		// Inject the note just before </body> so it renders under the table.
		note := fmt.Sprintf(`<p style="color:#666">Showing the first %d of %d rows. Download the full report from PrintMaster (run #%d).</p>`,
			deliveryPreviewRows, del.Result.RowCount, del.Run.ID)
		html = strings.Replace(html, "</body>", note+"</body>", 1)
	}
	text := emailTextBody(del, truncated)
	subject := fmt.Sprintf("PrintMaster report: %s", del.Report.Name)

	var errs []error
	for _, to := range recipients {
		if err := d.mailer(to, subject, html, text); err != nil {
			d.logger.Error("Report email delivery failed",
				"report_id", del.Report.ID, "run_id", del.Run.ID, "recipient", to, "error", err)
			errs = append(errs, fmt.Errorf("email to %s: %w", to, err))
			continue
		}
		d.logger.Info("Report emailed",
			"report_id", del.Report.ID, "run_id", del.Run.ID, "recipient", to, "rows", del.Result.RowCount)
	}
	return errs
}

func (d *NotificationDeliverer) deliverWebhook(ctx context.Context, del Delivery, url string) error {
	if d.poster == nil {
		d.logger.Warn("Report webhook delivery requested but no poster is configured",
			"report_id", del.Report.ID, "run_id", del.Run.ID)
		return fmt.Errorf("webhook delivery is not configured on this server")
	}
	if err := d.poster.PostJSON(ctx, url, nil, webhookPayload(del)); err != nil {
		// The URL is not logged: it may embed a secret token.
		d.logger.Error("Report webhook delivery failed",
			"report_id", del.Report.ID, "run_id", del.Run.ID, "error", err)
		return fmt.Errorf("webhook: %w", err)
	}
	d.logger.Info("Report webhook delivered",
		"report_id", del.Report.ID, "run_id", del.Run.ID, "rows", del.Result.RowCount)
	return nil
}

// ReportWebhookPayload is the JSON document posted to a report's webhook URL.
// Field names are part of the public integration contract; add, don't rename.
type ReportWebhookPayload struct {
	Event         string           `json:"event"`
	ReportID      int64            `json:"report_id"`
	ReportName    string           `json:"report_name"`
	ReportType    string           `json:"report_type"`
	RunID         int64            `json:"run_id"`
	ScheduleID    *int64           `json:"schedule_id,omitempty"`
	Status        string           `json:"status"`
	Format        string           `json:"format"`
	StartedAt     time.Time        `json:"started_at"`
	CompletedAt   *time.Time       `json:"completed_at,omitempty"`
	RowCount      int              `json:"row_count"`
	Summary       map[string]any   `json:"summary,omitempty"`
	Columns       []string         `json:"columns,omitempty"`
	Rows          []map[string]any `json:"rows,omitempty"`
	RowsTruncated bool             `json:"rows_truncated"`
}

func webhookPayload(del Delivery) ReportWebhookPayload {
	preview, truncated := previewResult(del.Result)
	return ReportWebhookPayload{
		Event:         "report.completed",
		ReportID:      del.Report.ID,
		ReportName:    del.Report.Name,
		ReportType:    del.Report.Type,
		RunID:         del.Run.ID,
		ScheduleID:    del.Run.ScheduleID,
		Status:        del.Run.Status,
		Format:        del.Run.Format,
		StartedAt:     del.Run.StartedAt,
		CompletedAt:   del.Run.CompletedAt,
		RowCount:      del.Result.RowCount,
		Summary:       del.Result.Summary,
		Columns:       preview.Columns,
		Rows:          preview.Rows,
		RowsTruncated: truncated,
	}
}

// previewResult returns a shallow copy of result limited to deliveryPreviewRows rows.
func previewResult(result *GenerateResult) (*GenerateResult, bool) {
	preview := *result
	if len(result.Rows) <= deliveryPreviewRows {
		return &preview, false
	}
	preview.Rows = result.Rows[:deliveryPreviewRows]
	return &preview, true
}

func emailTextBody(del Delivery, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", del.Report.Name)
	fmt.Fprintf(&b, "Report type: %s\nRows: %d\nRun: #%d\n", del.Report.Type, del.Result.RowCount, del.Run.ID)
	for k, v := range del.Result.Summary {
		fmt.Fprintf(&b, "%s: %s\n", formatLabel(k), formatValue(v))
	}
	if truncated {
		fmt.Fprintf(&b, "\nThe HTML version shows the first %d rows. Download the full report from PrintMaster.\n", deliveryPreviewRows)
	}
	return b.String()
}
