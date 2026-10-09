// Package alerts provides notification dispatch functionality for alerts.
package alerts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"printmaster/server/storage"
	"printmaster/server/webhook"
)

// SetAllowTestWebhooks sets whether localhost URLs are allowed for webhook testing.
// This should only be called during test initialization. The guard itself
// lives in the shared webhook package so alerts and reports enforce it identically.
func SetAllowTestWebhooks(allow bool) {
	webhook.SetAllowPrivateTargets(allow)
}

// poster returns the shared retrying HTTP poster configured for this notifier.
func (n *Notifier) poster() webhook.Poster {
	return webhook.Poster{Client: n.client, MaxRetries: n.config.MaxRetries, RetryDelay: n.config.RetryDelay}
}

// sanitizeEmailHeader removes CR and LF characters from email header values
// to prevent email header injection attacks.
func sanitizeEmailHeader(s string) string {
	// Replace all CR and LF characters with spaces
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	// Also handle null bytes which could be used for attacks
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(s)
}

// validateAndSanitizeEmail validates and sanitizes an email address.
// Returns the sanitized address and nil error if valid, or empty string and error if invalid.
func validateAndSanitizeEmail(email string) (string, error) {
	// Check for header injection attempts
	if strings.ContainsAny(email, "\r\n\x00") {
		return "", fmt.Errorf("email address contains invalid characters")
	}
	// Basic format check
	if !strings.Contains(email, "@") || len(email) < 3 {
		return "", fmt.Errorf("invalid email address format")
	}
	// Return sanitized version
	return sanitizeEmailHeader(email), nil
}

// SMTPConfig holds SMTP server settings for email notifications.
type SMTPConfig struct {
	Enabled bool
	Host    string
	Port    int
	User    string
	Pass    string
	From    string
}

// NotifierConfig configures the notification dispatcher.
type NotifierConfig struct {
	// Logger for notification events
	Logger *slog.Logger

	// HTTP client for webhook/Slack notifications
	HTTPClient *http.Client

	// Max retries for failed notifications
	MaxRetries int

	// Retry delay between attempts
	RetryDelay time.Duration

	// SMTP configuration for email notifications (used as fallback when channel doesn't specify)
	SMTP SMTPConfig
}

// NotifierStore defines the storage operations needed by the notifier.
type NotifierStore interface {
	// Get notification channels
	GetNotificationChannel(ctx context.Context, id int64) (*storage.NotificationChannel, error)
	ListNotificationChannels(ctx context.Context) ([]storage.NotificationChannel, error)

	// Get alert rules to find associated channels
	GetAlertRule(ctx context.Context, id int64) (*storage.AlertRule, error)

	// Update notification tracking on alerts
	UpdateAlertNotificationStatus(ctx context.Context, id int64, sent int, lastNotified time.Time) error

	// Get settings for rate limits, quiet hours, etc.
	GetAlertSettings(ctx context.Context) (*storage.AlertSettings, error)
}

// Notifier dispatches notifications for triggered alerts.
type Notifier struct {
	store  NotifierStore
	config NotifierConfig
	logger *slog.Logger
	client *http.Client

	mu           sync.Mutex
	lastNotified map[string]time.Time // Track last notification per channel+alert combo
}

// NewNotifier creates a new notification dispatcher.
func NewNotifier(store NotifierStore, config NotifierConfig) *Notifier {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := config.HTTPClient
	if client == nil {
		client = webhook.DefaultClient()
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = 5 * time.Second
	}

	return &Notifier{
		store:        store,
		config:       config,
		logger:       logger,
		client:       client,
		lastNotified: make(map[string]time.Time),
	}
}

// NotifyForAlert sends notifications for a triggered alert.
// It looks up the associated rule's channels and dispatches to each.
func (n *Notifier) NotifyForAlert(ctx context.Context, alert *storage.Alert) error {
	// Skip if alert has no associated rule
	if alert.RuleID == 0 {
		return nil
	}

	// Check quiet hours
	settings, _ := n.store.GetAlertSettings(ctx)
	if isInQuietHours(settings) && !isCriticalAlertType(alert.Type) {
		n.logger.Debug("skipping notification during quiet hours", "alert_id", alert.ID, "type", alert.Type)
		return nil
	}

	// Get the rule to find associated channels
	rule, err := n.store.GetAlertRule(ctx, alert.RuleID)
	if err != nil {
		return fmt.Errorf("failed to get alert rule: %w", err)
	}

	if len(rule.ChannelIDs) == 0 {
		n.logger.Debug("no notification channels configured for rule", "rule_id", rule.ID)
		return nil
	}

	// Send to each channel
	var notificationsSent int
	var lastErr error

	for _, channelID := range rule.ChannelIDs {
		channel, err := n.store.GetNotificationChannel(ctx, channelID)
		if err != nil {
			n.logger.Warn("failed to get notification channel", "channel_id", channelID, "error", err)
			continue
		}

		if !channel.Enabled {
			continue
		}

		// Check rate limiting (convert hourly limit to minutes: 60/limit)
		rateLimitMins := 0
		if channel.RateLimitPerHour > 0 {
			rateLimitMins = 60 / channel.RateLimitPerHour
		}
		if !n.shouldNotify(channel.ID, alert.ID, rateLimitMins) {
			n.logger.Debug("rate limited notification", "channel_id", channel.ID, "alert_id", alert.ID)
			continue
		}

		// Dispatch based on channel type
		err = n.dispatchToChannel(ctx, channel, alert)
		if err != nil {
			n.logger.Error("failed to send notification", "channel", channel.Name, "type", channel.Type, "error", err)
			lastErr = err
			continue
		}

		notificationsSent++
		n.recordNotification(channel.ID, alert.ID)
		n.logger.Info("notification sent", "channel", channel.Name, "type", channel.Type, "alert_id", alert.ID)
	}

	// Update alert notification tracking
	if notificationsSent > 0 {
		now := time.Now().UTC()
		_ = n.store.UpdateAlertNotificationStatus(ctx, alert.ID, alert.NotificationsSent+notificationsSent, now)
	}

	return lastErr
}

func (n *Notifier) shouldNotify(channelID, alertID int64, rateLimitMins int) bool {
	if rateLimitMins <= 0 {
		return true
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	key := fmt.Sprintf("%d:%d", channelID, alertID)
	lastTime, exists := n.lastNotified[key]
	if !exists {
		return true
	}

	return time.Since(lastTime) >= time.Duration(rateLimitMins)*time.Minute
}

func (n *Notifier) recordNotification(channelID, alertID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()

	key := fmt.Sprintf("%d:%d", channelID, alertID)
	n.lastNotified[key] = time.Now()
}

// TestChannel sends a test notification to a channel without any rate limiting or quiet hours checks.
func (n *Notifier) TestChannel(ctx context.Context, channel *storage.NotificationChannel, alert *storage.Alert) error {
	return n.dispatchToChannel(ctx, channel, alert)
}

func (n *Notifier) dispatchToChannel(ctx context.Context, channel *storage.NotificationChannel, alert *storage.Alert) error {
	// Parse config from JSON
	var config map[string]interface{}
	if channel.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(channel.ConfigJSON), &config); err != nil {
			return fmt.Errorf("failed to parse channel config: %w", err)
		}
	}

	switch channel.Type {
	case storage.ChannelTypeEmail:
		return n.sendEmail(ctx, config, alert)
	case storage.ChannelTypeWebhook:
		return n.sendWebhook(ctx, config, alert)
	case storage.ChannelTypeSlack:
		return n.sendSlack(ctx, config, alert)
	case storage.ChannelTypeTeams:
		return n.sendMSTeams(ctx, config, alert)
	case storage.ChannelTypePagerDuty:
		return n.sendPagerDuty(ctx, config, alert)
	case storage.ChannelTypeDiscord:
		return n.sendDiscord(ctx, config, alert)
	case storage.ChannelTypeTelegram:
		return n.sendTelegram(ctx, config, alert)
	case storage.ChannelTypePushover:
		return n.sendPushover(ctx, config, alert)
	case storage.ChannelTypeNtfy:
		return n.sendNtfy(ctx, config, alert)
	default:
		return fmt.Errorf("unsupported notification type: %s", channel.Type)
	}
}

// Email notification
func (n *Notifier) sendEmail(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		config = make(map[string]interface{})
	}

	// Try channel-specific SMTP settings first, then fall back to global config
	host, _ := config["smtp_host"].(string)
	portFloat, _ := config["smtp_port"].(float64)
	port := int(portFloat)
	username, _ := config["smtp_username"].(string)
	password, _ := config["smtp_password"].(string)
	from, _ := config["from_address"].(string)

	// Fall back to global SMTP config if channel doesn't have SMTP settings
	if host == "" && n.config.SMTP.Host != "" {
		host = n.config.SMTP.Host
		port = n.config.SMTP.Port
		username = n.config.SMTP.User
		password = n.config.SMTP.Pass
		if from == "" {
			from = n.config.SMTP.From
		}
	}

	// Get recipient list - try both "to_addresses" (legacy) and "to" (new UI format)
	var toList []interface{}
	if tl, ok := config["to_addresses"].([]interface{}); ok && len(tl) > 0 {
		toList = tl
	} else if tl, ok := config["to"].([]interface{}); ok && len(tl) > 0 {
		toList = tl
	}

	if host == "" || from == "" || len(toList) == 0 {
		var missing []string
		if host == "" {
			missing = append(missing, "SMTP host not configured in server settings")
		}
		if from == "" {
			missing = append(missing, "from address not configured")
		}
		if len(toList) == 0 {
			missing = append(missing, "no recipient addresses specified")
		}
		return fmt.Errorf("incomplete email configuration: %s", strings.Join(missing, ", "))
	}

	// Validate and sanitize from address to prevent header injection
	sanitizedFrom, err := validateAndSanitizeEmail(from)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}

	// Build recipient list with validation
	var sanitizedTo []string
	for _, addr := range toList {
		if s, ok := addr.(string); ok {
			sanitized, err := validateAndSanitizeEmail(s)
			if err != nil {
				n.logger.Warn("skipping invalid recipient address", "address", s, "error", err)
				continue
			}
			sanitizedTo = append(sanitizedTo, sanitized)
		}
	}

	if len(sanitizedTo) == 0 {
		return fmt.Errorf("no valid recipient addresses")
	}

	// Build message with sanitized headers to prevent header injection
	subject := sanitizeEmailHeader(fmt.Sprintf("[%s] %s", strings.ToUpper(string(alert.Severity)), alert.Title))
	body := fmt.Sprintf(`Alert: %s
Severity: %s
Scope: %s
Time: %s

%s

---
This is an automated alert from PrintMaster.
`, alert.Title, alert.Severity, alert.Scope, alert.TriggeredAt.Format(time.RFC3339), alert.Message)

	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		sanitizedFrom, strings.Join(sanitizedTo, ", "), subject, body))

	addr := fmt.Sprintf("%s:%d", host, port)

	// Send with retry
	var lastErr error
	for i := 0; i < n.config.MaxRetries; i++ {
		var auth smtp.Auth
		if username != "" {
			auth = smtp.PlainAuth("", username, password, host)
		}

		// Recipients validated and sanitized via validateAndSanitizeEmail above
		sendErr := smtp.SendMail(addr, auth, sanitizedFrom, sanitizedTo, msg)
		if sendErr == nil {
			return nil
		}
		lastErr = sendErr
		time.Sleep(n.config.RetryDelay)
	}

	return fmt.Errorf("email send failed after %d attempts: %w", n.config.MaxRetries, lastErr)
}
func (n *Notifier) sendWebhook(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("webhook channel has no configuration")
	}

	url, _ := config["url"].(string)
	if url == "" {
		return fmt.Errorf("webhook URL not configured")
	}

	// Get optional headers
	headers := make(map[string]string)
	if h, ok := config["headers"].(map[string]interface{}); ok {
		for k, v := range h {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}

	// Build payload
	payload := map[string]interface{}{
		"alert_id":      alert.ID,
		"type":          alert.Type,
		"severity":      alert.Severity,
		"scope":         alert.Scope,
		"status":        alert.Status,
		"title":         alert.Title,
		"message":       alert.Message,
		"triggered_at":  alert.TriggeredAt.Format(time.RFC3339),
		"device_serial": alert.DeviceSerial,
		"agent_id":      alert.AgentID,
		"site_id":       alert.SiteID,
		"tenant_id":     alert.TenantID,
	}

	return n.postJSON(ctx, url, headers, payload)
}

// Slack notification
func (n *Notifier) sendSlack(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("slack channel has no configuration")
	}

	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return fmt.Errorf("slack webhook URL not configured")
	}

	slackChannel, _ := config["channel"].(string)
	username, _ := config["username"].(string)
	if username == "" {
		username = "PrintMaster"
	}

	// Pick color based on severity
	color := "#17a2b8" // info - blue
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		color = "#dc3545" // red
	case storage.AlertSeverityWarning:
		color = "#ffc107" // yellow
	}

	// Build Slack message
	payload := map[string]interface{}{
		"username": username,
		"attachments": []map[string]interface{}{
			{
				"color":  color,
				"title":  alert.Title,
				"text":   alert.Message,
				"footer": fmt.Sprintf("PrintMaster Alert | %s", alert.Scope),
				"ts":     alert.TriggeredAt.Unix(),
				"fields": []map[string]interface{}{
					{"title": "Severity", "value": string(alert.Severity), "short": true},
					{"title": "Type", "value": string(alert.Type), "short": true},
				},
			},
		},
	}

	if slackChannel != "" {
		payload["channel"] = slackChannel
	}

	return n.postJSON(ctx, webhookURL, nil, payload)
}

// MS Teams notification
func (n *Notifier) sendMSTeams(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("teams channel has no configuration")
	}

	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return fmt.Errorf("teams webhook URL not configured")
	}

	// Pick theme color based on severity
	themeColor := "0078D7" // info - blue
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		themeColor = "DC3545" // red
	case storage.AlertSeverityWarning:
		themeColor = "FFC107" // yellow
	}

	// Build Teams Adaptive Card
	payload := map[string]interface{}{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": themeColor,
		"summary":    alert.Title,
		"sections": []map[string]interface{}{
			{
				"activityTitle":    alert.Title,
				"activitySubtitle": fmt.Sprintf("%s | %s", alert.Scope, alert.Severity),
				"text":             alert.Message,
				"facts": []map[string]interface{}{
					{"name": "Severity", "value": string(alert.Severity)},
					{"name": "Type", "value": string(alert.Type)},
					{"name": "Time", "value": alert.TriggeredAt.Format(time.RFC3339)},
				},
			},
		},
	}

	return n.postJSON(ctx, webhookURL, nil, payload)
}

// PagerDuty notification
func (n *Notifier) sendPagerDuty(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("pagerduty channel has no configuration")
	}

	routingKey, _ := config["routing_key"].(string)
	if routingKey == "" {
		return fmt.Errorf("pagerduty routing key not configured")
	}

	// Map severity to PagerDuty severity
	pdSeverity := "info"
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		pdSeverity = "critical"
	case storage.AlertSeverityWarning:
		pdSeverity = "warning"
	}

	// Build PagerDuty Events API v2 payload
	payload := map[string]interface{}{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"dedup_key":    fmt.Sprintf("printmaster-%d", alert.ID),
		"payload": map[string]interface{}{
			"summary":   alert.Title,
			"severity":  pdSeverity,
			"source":    "PrintMaster",
			"component": string(alert.Scope),
			"group":     string(alert.Type),
			"class":     "alert",
			"custom_details": map[string]interface{}{
				"message":       alert.Message,
				"device_serial": alert.DeviceSerial,
				"agent_id":      alert.AgentID,
				"triggered_at":  alert.TriggeredAt.Format(time.RFC3339),
			},
		},
	}

	// Allow custom API URL for testing
	apiURL := "https://events.pagerduty.com/v2/enqueue"
	if customURL, ok := config["api_url"].(string); ok && customURL != "" {
		apiURL = customURL
	}

	return n.postJSON(ctx, apiURL, nil, payload)
}

// postJSON posts a JSON payload to the given URL with SSRF validation and retries.
func (n *Notifier) postJSON(ctx context.Context, url string, headers map[string]string, payload interface{}) error {
	return n.poster().PostJSON(ctx, url, headers, payload)
}

// Discord notification via webhook
func (n *Notifier) sendDiscord(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("discord channel has no configuration")
	}

	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return fmt.Errorf("discord webhook URL not configured")
	}

	username, _ := config["username"].(string)
	if username == "" {
		username = "PrintMaster"
	}

	// Pick color based on severity (Discord uses decimal color)
	color := 1752220 // info - blue
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		color = 15158332 // red
	case storage.AlertSeverityWarning:
		color = 16776960 // yellow
	}

	// Build Discord embed payload
	payload := map[string]interface{}{
		"username": username,
		"embeds": []map[string]interface{}{
			{
				"title":       alert.Title,
				"description": alert.Message,
				"color":       color,
				"timestamp":   alert.TriggeredAt.Format(time.RFC3339),
				"footer": map[string]string{
					"text": fmt.Sprintf("PrintMaster | %s", alert.Scope),
				},
				"fields": []map[string]interface{}{
					{"name": "Severity", "value": string(alert.Severity), "inline": true},
					{"name": "Type", "value": string(alert.Type), "inline": true},
				},
			},
		},
	}

	return n.postJSON(ctx, webhookURL, nil, payload)
}

// Telegram notification via Bot API
func (n *Notifier) sendTelegram(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("telegram channel has no configuration")
	}

	botToken, _ := config["bot_token"].(string)
	chatID, _ := config["chat_id"].(string)
	if botToken == "" || chatID == "" {
		return fmt.Errorf("telegram bot_token and chat_id are required")
	}

	// Build message text with formatting
	severityEmoji := "ℹ️"
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		severityEmoji = "🚨"
	case storage.AlertSeverityWarning:
		severityEmoji = "⚠️"
	}

	text := fmt.Sprintf("%s *%s*\n\n%s\n\n*Severity:* %s\n*Type:* %s\n*Scope:* %s\n*Time:* %s",
		severityEmoji, escapeMarkdownV2(alert.Title),
		escapeMarkdownV2(alert.Message),
		escapeMarkdownV2(string(alert.Severity)),
		escapeMarkdownV2(string(alert.Type)),
		escapeMarkdownV2(alert.Scope),
		escapeMarkdownV2(alert.TriggeredAt.Format(time.RFC3339)))

	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "MarkdownV2",
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	if customURL, ok := config["api_url"].(string); ok && customURL != "" {
		apiURL = customURL
	}

	return n.postJSON(ctx, apiURL, nil, payload)
}

// escapeMarkdownV2 escapes special characters for Telegram MarkdownV2
func escapeMarkdownV2(s string) string {
	specialChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	result := s
	for _, char := range specialChars {
		result = strings.ReplaceAll(result, char, "\\"+char)
	}
	return result
}

// Pushover notification for iOS/Android push
func (n *Notifier) sendPushover(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("pushover channel has no configuration")
	}

	userKey, _ := config["user_key"].(string)
	apiToken, _ := config["api_token"].(string)
	if userKey == "" || apiToken == "" {
		return fmt.Errorf("pushover user_key and api_token are required")
	}

	// Map severity to Pushover priority (-2 to 2)
	priority := 0 // normal
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		priority = 1 // high priority
	case storage.AlertSeverityWarning:
		priority = 0 // normal
	case storage.AlertSeverityInfo:
		priority = -1 // low priority
	}

	payload := map[string]interface{}{
		"token":    apiToken,
		"user":     userKey,
		"title":    alert.Title,
		"message":  alert.Message,
		"priority": priority,
		"url":      "", // Could add a link to the alert in UI
	}

	// Optional device targeting
	if device, ok := config["device"].(string); ok && device != "" {
		payload["device"] = device
	}

	// Optional sound
	if sound, ok := config["sound"].(string); ok && sound != "" {
		payload["sound"] = sound
	}

	apiURL := "https://api.pushover.net/1/messages.json"
	if customURL, ok := config["api_url"].(string); ok && customURL != "" {
		apiURL = customURL
	}

	return n.postJSON(ctx, apiURL, nil, payload)
}

// ntfy notification (self-hosted or ntfy.sh)
func (n *Notifier) sendNtfy(ctx context.Context, config map[string]interface{}, alert *storage.Alert) error {
	if config == nil {
		return fmt.Errorf("ntfy channel has no configuration")
	}

	topic, _ := config["topic"].(string)
	if topic == "" {
		return fmt.Errorf("ntfy topic is required")
	}

	serverURL, _ := config["server_url"].(string)
	if serverURL == "" {
		serverURL = "https://ntfy.sh" // default to public ntfy.sh
	}

	// Map severity to ntfy priority
	priority := "default"
	tags := "printer"
	switch alert.Severity {
	case storage.AlertSeverityCritical:
		priority = "urgent"
		tags = "rotating_light,printer"
	case storage.AlertSeverityWarning:
		priority = "high"
		tags = "warning,printer"
	}

	// Build the ntfy publish URL
	publishURL := fmt.Sprintf("%s/%s", strings.TrimSuffix(serverURL, "/"), topic)

	headers := map[string]string{
		"Title":    alert.Title,
		"Priority": priority,
		"Tags":     tags,
	}

	// Add optional click action
	if clickURL, ok := config["click_url"].(string); ok && clickURL != "" {
		headers["Click"] = clickURL
	}

	// Add optional authentication
	if token, ok := config["access_token"].(string); ok && token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if user, ok := config["username"].(string); ok && user != "" {
		if pass, ok := config["password"].(string); ok {
			headers["Authorization"] = "Basic " + basicAuth(user, pass)
		}
	}

	// ntfy accepts plain text body
	message := fmt.Sprintf("%s\n\nSeverity: %s | Type: %s | Scope: %s",
		alert.Message, alert.Severity, alert.Type, alert.Scope)

	return n.postText(ctx, publishURL, headers, message)
}

// basicAuth returns base64-encoded basic auth string
func basicAuth(user, pass string) string {
	auth := user + ":" + pass
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

// postText posts plain text to the given URL with SSRF validation and retries.
func (n *Notifier) postText(ctx context.Context, url string, headers map[string]string, body string) error {
	return n.poster().Post(ctx, url, "text/plain", headers, []byte(body))
}
