package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// NotificationSettings holds admin-notification preferences for a single scope
// level. A record with an empty TenantID is the fleet (server-admin) level;
// records with a TenantID are tenant-admin level.
type NotificationSettings struct {
	TenantID             string    `json:"tenant_id,omitempty"`
	Enabled              bool      `json:"enabled"`
	Recipients           []string  `json:"recipients"`
	NotifyOnCritical     bool      `json:"notify_on_critical"`
	NotifyOnWarning      bool      `json:"notify_on_warning"`
	DailySummaryEnabled  bool      `json:"daily_summary_enabled"`
	DailySummaryTime     string    `json:"daily_summary_time"`
	DailySummaryTimezone string    `json:"daily_summary_timezone"`
	UpdatedAt            time.Time `json:"updated_at"`
	UpdatedBy            string    `json:"updated_by,omitempty"`
}

const notificationSettingsColumns = `tenant_id, enabled, recipients, notify_on_critical, notify_on_warning,
	daily_summary_enabled, daily_summary_time, daily_summary_timezone, updated_at, COALESCE(updated_by, '')`

// GetTenantNotificationSettings returns the notification preferences for a tenant,
// or nil when the tenant has never configured them.
func (s *BaseStore) GetTenantNotificationSettings(ctx context.Context, tenantID string) (*NotificationSettings, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant id required")
	}
	query := `SELECT ` + notificationSettingsColumns + ` FROM tenant_notification_settings WHERE tenant_id = ?`
	rec, err := scanNotificationSettings(s.queryRowContext(ctx, query, tenantID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant notification settings: %w", err)
	}
	return rec, nil
}

// ListTenantNotificationSettings returns notification preferences for every tenant
// that has configured them.
func (s *BaseStore) ListTenantNotificationSettings(ctx context.Context) ([]*NotificationSettings, error) {
	query := `SELECT ` + notificationSettingsColumns + ` FROM tenant_notification_settings ORDER BY tenant_id`
	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list tenant notification settings: %w", err)
	}
	defer rows.Close()

	var out []*NotificationSettings
	for rows.Next() {
		rec, err := scanNotificationSettings(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tenant notification settings: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// UpsertTenantNotificationSettings stores notification preferences for a tenant.
func (s *BaseStore) UpsertTenantNotificationSettings(ctx context.Context, rec *NotificationSettings) error {
	if rec == nil {
		return fmt.Errorf("notification settings required")
	}
	tenantID := strings.TrimSpace(rec.TenantID)
	if tenantID == "" {
		return fmt.Errorf("tenant id required")
	}
	recipients := rec.Recipients
	if recipients == nil {
		recipients = []string{}
	}
	encoded, err := json.Marshal(recipients)
	if err != nil {
		return fmt.Errorf("encode recipients: %w", err)
	}
	if strings.TrimSpace(rec.DailySummaryTime) == "" {
		rec.DailySummaryTime = "08:00"
	}
	if strings.TrimSpace(rec.DailySummaryTimezone) == "" {
		rec.DailySummaryTimezone = "Local"
	}
	if strings.TrimSpace(rec.UpdatedBy) == "" {
		rec.UpdatedBy = "system"
	}
	rec.TenantID = tenantID
	rec.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO tenant_notification_settings (
			tenant_id, enabled, recipients, notify_on_critical, notify_on_warning,
			daily_summary_enabled, daily_summary_time, daily_summary_timezone, updated_at, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
			enabled = excluded.enabled,
			recipients = excluded.recipients,
			notify_on_critical = excluded.notify_on_critical,
			notify_on_warning = excluded.notify_on_warning,
			daily_summary_enabled = excluded.daily_summary_enabled,
			daily_summary_time = excluded.daily_summary_time,
			daily_summary_timezone = excluded.daily_summary_timezone,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by
	`
	_, err = s.execContext(ctx, query,
		rec.TenantID,
		rec.Enabled,
		string(encoded),
		rec.NotifyOnCritical,
		rec.NotifyOnWarning,
		rec.DailySummaryEnabled,
		rec.DailySummaryTime,
		rec.DailySummaryTimezone,
		rec.UpdatedAt,
		rec.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("upsert tenant notification settings: %w", err)
	}
	return nil
}

// DeleteTenantNotificationSettings removes a tenant's notification preferences.
func (s *BaseStore) DeleteTenantNotificationSettings(ctx context.Context, tenantID string) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("tenant id required")
	}
	_, err := s.execContext(ctx, `DELETE FROM tenant_notification_settings WHERE tenant_id = ?`, tenantID)
	return err
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanNotificationSettings(row rowScanner) (*NotificationSettings, error) {
	var (
		rec        NotificationSettings
		recipients sql.NullString
		updatedAt  sql.NullTime
	)
	err := row.Scan(
		&rec.TenantID,
		&rec.Enabled,
		&recipients,
		&rec.NotifyOnCritical,
		&rec.NotifyOnWarning,
		&rec.DailySummaryEnabled,
		&rec.DailySummaryTime,
		&rec.DailySummaryTimezone,
		&updatedAt,
		&rec.UpdatedBy,
	)
	if err != nil {
		return nil, err
	}
	rec.Recipients = []string{}
	if recipients.Valid && strings.TrimSpace(recipients.String) != "" {
		if err := json.Unmarshal([]byte(recipients.String), &rec.Recipients); err != nil {
			return nil, fmt.Errorf("decode recipients: %w", err)
		}
	}
	if updatedAt.Valid {
		rec.UpdatedAt = updatedAt.Time
	}
	return &rec, nil
}
