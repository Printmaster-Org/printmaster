package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pmsettings "printmaster/common/settings"

	_ "modernc.org/sqlite" // Pure Go SQLite driver (no CGO required)
)

// SQLiteStore implements Store using SQLite, embedding BaseStore for common operations
type SQLiteStore struct {
	*BaseStore // Embed BaseStore for common operations
}

// NewSQLiteStore creates a new SQLite-backed store
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Ensure directory exists (unless in-memory)
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory: %w", err)
		}
	}

	// Build connection string with pragmas (skip for in-memory databases)
	connStr := dbPath
	const busyTimeoutMS = 30000 // 30 seconds - gives time for long operations
	if dbPath != ":memory:" {
		// WAL mode allows concurrent reads while writing
		// _txlock=immediate acquires write lock at transaction start, reducing contention
		connStr += fmt.Sprintf("?_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000&_foreign_keys=ON&_txlock=immediate", busyTimeoutMS)
	} else {
		connStr += fmt.Sprintf("?_busy_timeout=%d&_foreign_keys=ON", busyTimeoutMS)
	}

	// Open database
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Connection pool settings for SQLite:
	// - MaxOpenConns: Allow multiple connections for reads (WAL mode supports this)
	// - MaxIdleConns: Keep some connections ready to reduce open/close overhead
	// - ConnMaxLifetime: Prevent stale connections
	// Note: SQLite handles write serialization internally with busy_timeout
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	logInfo("Opened SQLite database", "path", dbPath)

	// Create BaseStore with SQLite dialect
	base := &BaseStore{
		db:      db,
		dialect: &SQLiteDialect{},
		dbPath:  dbPath,
	}

	store := &SQLiteStore{
		BaseStore: base,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}

	return store, nil
}

// initSchema creates tables if they don't exist
func (s *SQLiteStore) initSchema() error {
	schema := `
	-- Schema version tracking
	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- Registered agents
	CREATE TABLE IF NOT EXISTS agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		hostname TEXT NOT NULL,
		ip TEXT NOT NULL,
		platform TEXT NOT NULL,
		version TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		token TEXT NOT NULL,
		registered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status TEXT NOT NULL DEFAULT 'active',
		os_version TEXT,
		go_version TEXT,
		architecture TEXT,
		num_cpu INTEGER DEFAULT 0,
		total_memory_mb INTEGER DEFAULT 0,
		build_type TEXT,
		git_commit TEXT,
		last_heartbeat DATETIME,
		device_count INTEGER DEFAULT 0,
		last_device_sync DATETIME,
		last_metrics_sync DATETIME,
		tenant_id TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_agents_agent_id ON agents(agent_id);
	CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen);
	CREATE INDEX IF NOT EXISTS idx_agents_token ON agents(token);
	CREATE INDEX IF NOT EXISTS idx_agents_tenant_id ON agents(tenant_id);

	-- Devices discovered by agents
	CREATE TABLE IF NOT EXISTS devices (
		serial TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		manufacturer TEXT,
		model TEXT,
		hostname TEXT,
		firmware TEXT,
		mac_address TEXT,
		subnet_mask TEXT,
		gateway TEXT,
		consumables TEXT,
		status_messages TEXT,
		last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		discovery_method TEXT,
		asset_number TEXT,
		location TEXT,
		description TEXT,
		web_ui_url TEXT,
		raw_data TEXT,
		tenant_id TEXT,
		device_type TEXT,
		source_type TEXT,
		is_usb INTEGER DEFAULT 0,
		port_name TEXT,
		driver_name TEXT,
		is_default INTEGER DEFAULT 0,
		is_shared INTEGER DEFAULT 0,
		spooler_status TEXT,
		usb_webui_available INTEGER DEFAULT 0,
		FOREIGN KEY(agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_devices_agent_id ON devices(agent_id);
	CREATE INDEX IF NOT EXISTS idx_devices_ip ON devices(ip);
	CREATE INDEX IF NOT EXISTS idx_devices_last_seen ON devices(last_seen);
	CREATE INDEX IF NOT EXISTS idx_devices_owner_order ON devices(agent_id, last_seen DESC, serial);
	CREATE INDEX IF NOT EXISTS idx_devices_tenant_id ON devices(tenant_id);

	-- Metrics history
	CREATE TABLE IF NOT EXISTS metrics_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		serial TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		page_count INTEGER DEFAULT 0,
		color_pages INTEGER DEFAULT 0,
		mono_pages INTEGER DEFAULT 0,
		scan_count INTEGER DEFAULT 0,
		toner_levels TEXT,
		tenant_id TEXT,
		FOREIGN KEY(serial) REFERENCES devices(serial) ON DELETE CASCADE,
		FOREIGN KEY(agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_metrics_serial ON metrics_history(serial);
	CREATE INDEX IF NOT EXISTS idx_metrics_agent_id ON metrics_history(agent_id);
	CREATE INDEX IF NOT EXISTS idx_metrics_timestamp ON metrics_history(timestamp);
	CREATE INDEX IF NOT EXISTS idx_metrics_tenant_id ON metrics_history(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_metrics_serial_timestamp ON metrics_history(serial, timestamp);
	CREATE INDEX IF NOT EXISTS idx_metrics_owner_latest ON metrics_history(serial, agent_id, timestamp DESC, page_count DESC);

	-- Server metrics history for Netdata-style dashboards (tiered storage)
	CREATE TABLE IF NOT EXISTS server_metrics_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME NOT NULL,
		tier TEXT NOT NULL DEFAULT 'raw',
		fleet_json TEXT NOT NULL,
		server_json TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_server_metrics_timestamp ON server_metrics_history(timestamp);
	CREATE INDEX IF NOT EXISTS idx_server_metrics_tier ON server_metrics_history(tier);
	CREATE INDEX IF NOT EXISTS idx_server_metrics_tier_ts ON server_metrics_history(tier, timestamp);

	-- Audit log for agent and admin operations
	CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		actor_type TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		actor_name TEXT,
		action TEXT NOT NULL,
		target_type TEXT,
		target_id TEXT,
		tenant_id TEXT,
		severity TEXT NOT NULL DEFAULT 'info',
		details TEXT,
		metadata TEXT,
		ip_address TEXT,
		user_agent TEXT,
		request_id TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor_type, actor_id);
	CREATE INDEX IF NOT EXISTS idx_audit_tenant ON audit_log(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_log(action);

	-- Tenants table for multi-tenant support
	CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		contact_name TEXT,
		contact_email TEXT,
		contact_phone TEXT,
		business_unit TEXT,
		billing_code TEXT,
		address TEXT,
		login_domain TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- Sites table for organizing devices within tenants
	CREATE TABLE IF NOT EXISTS sites (
		id TEXT PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		address TEXT,
		filter_rules TEXT, -- JSON array of filter rules
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_sites_tenant ON sites(tenant_id);

	-- Agent-to-site assignments (many-to-many: one agent can serve multiple sites)
	CREATE TABLE IF NOT EXISTS agent_sites (
		agent_id TEXT NOT NULL,
		site_id TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (agent_id, site_id),
		FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_agent_sites_site ON agent_sites(site_id);
	CREATE INDEX IF NOT EXISTS idx_agent_sites_agent ON agent_sites(agent_id);

	-- Join tokens for agent onboarding (store only token hash)
	CREATE TABLE IF NOT EXISTS join_tokens (
		id TEXT PRIMARY KEY,
		token_hash TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		one_time INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		used_at DATETIME,
		revoked INTEGER DEFAULT 0,
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_join_tokens_hash ON join_tokens(token_hash);
	CREATE INDEX IF NOT EXISTS idx_join_tokens_tenant ON join_tokens(tenant_id);

	-- Pending agent registrations (for expired but known tokens)
	CREATE TABLE IF NOT EXISTS pending_agent_registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL,
		name TEXT,
		hostname TEXT,
		ip TEXT,
		platform TEXT,
		agent_version TEXT,
		protocol_version TEXT,
		expired_token_id TEXT NOT NULL,
		expired_tenant_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		reviewed_at DATETIME,
		reviewed_by TEXT,
		notes TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_pending_registrations_status ON pending_agent_registrations(status);
	CREATE INDEX IF NOT EXISTS idx_pending_registrations_tenant ON pending_agent_registrations(expired_tenant_id);
	CREATE INDEX IF NOT EXISTS idx_pending_registrations_agent ON pending_agent_registrations(agent_id);

	-- Local users for UI and API authentication
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user',
		tenant_id TEXT,
		email TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

	CREATE TABLE IF NOT EXISTS user_tenants (
		user_id INTEGER NOT NULL,
		tenant_id TEXT NOT NULL,
		PRIMARY KEY (user_id, tenant_id),
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_user_tenants_tenant ON user_tenants(tenant_id);

	-- Sessions for local login (token stored as SHA-256 hash)
	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);

	-- Password reset tokens (store hashed token)
	CREATE TABLE IF NOT EXISTS password_resets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_hash TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		expires_at DATETIME NOT NULL,
		used INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_password_resets_user_id ON password_resets(user_id);

	-- User invitations (email-based signup)
	CREATE TABLE IF NOT EXISTS user_invitations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		username TEXT,
		role TEXT NOT NULL DEFAULT 'viewer',
		tenant_id TEXT,
		token_hash TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		used INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_by TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_user_invitations_email ON user_invitations(email);

	-- OIDC providers for SSO
	CREATE TABLE IF NOT EXISTS oidc_providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		issuer TEXT NOT NULL,
		client_id TEXT NOT NULL,
		client_secret TEXT NOT NULL,
		scopes TEXT NOT NULL DEFAULT 'openid profile email',
		icon TEXT,
		button_text TEXT,
		button_style TEXT,
		auto_login INTEGER NOT NULL DEFAULT 0,
		tenant_id TEXT,
		default_role TEXT NOT NULL DEFAULT 'user',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS oidc_sessions (
		id TEXT PRIMARY KEY,
		provider_slug TEXT NOT NULL,
		tenant_id TEXT,
		nonce TEXT NOT NULL,
		state TEXT NOT NULL,
		redirect_url TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (provider_slug) REFERENCES oidc_providers(slug) ON DELETE CASCADE,
		FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS oidc_links (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider_slug TEXT NOT NULL,
		subject TEXT NOT NULL,
		email TEXT,
		user_id INTEGER NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(provider_slug, subject),
		FOREIGN KEY (provider_slug) REFERENCES oidc_providers(slug) ON DELETE CASCADE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	-- Settings tables (global + tenant overrides)
	CREATE TABLE IF NOT EXISTS settings_global (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		schema_version TEXT NOT NULL,
		payload TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_by TEXT
	);

	CREATE TABLE IF NOT EXISTS settings_tenant (
		tenant_id TEXT PRIMARY KEY,
		schema_version TEXT NOT NULL,
		payload TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_by TEXT,
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	-- Agent settings overrides (per-agent override patches)
	CREATE TABLE IF NOT EXISTS settings_agent_override (
		agent_id TEXT PRIMARY KEY,
		schema_version TEXT NOT NULL,
		payload TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_by TEXT,
		FOREIGN KEY(agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE
	);

	-- Fleet update policy table (per-tenant auto-update configuration)
	CREATE TABLE IF NOT EXISTS fleet_update_policies (
		tenant_id TEXT PRIMARY KEY,
		update_check_days INTEGER NOT NULL DEFAULT 0,
		version_pin_strategy TEXT NOT NULL DEFAULT 'minor',
		allow_major_upgrade INTEGER NOT NULL DEFAULT 0,
		target_version TEXT,
		maintenance_window_enabled INTEGER NOT NULL DEFAULT 0,
		maintenance_window_start_hour INTEGER DEFAULT 2,
		maintenance_window_start_min INTEGER DEFAULT 0,
		maintenance_window_end_hour INTEGER DEFAULT 4,
		maintenance_window_end_min INTEGER DEFAULT 0,
		maintenance_window_timezone TEXT DEFAULT 'UTC',
		maintenance_window_days TEXT,
		rollout_staggered INTEGER NOT NULL DEFAULT 0,
		rollout_max_concurrent INTEGER DEFAULT 0,
		rollout_batch_size INTEGER DEFAULT 0,
		rollout_delay_between_waves INTEGER DEFAULT 300,
		rollout_jitter_seconds INTEGER DEFAULT 60,
		rollout_emergency_abort INTEGER NOT NULL DEFAULT 0,
		collect_telemetry INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS fleet_update_policy_global (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		update_check_days INTEGER NOT NULL DEFAULT 0,
		version_pin_strategy TEXT NOT NULL DEFAULT 'minor',
		allow_major_upgrade INTEGER NOT NULL DEFAULT 0,
		target_version TEXT,
		maintenance_window_enabled INTEGER NOT NULL DEFAULT 0,
		maintenance_window_start_hour INTEGER DEFAULT 2,
		maintenance_window_start_min INTEGER DEFAULT 0,
		maintenance_window_end_hour INTEGER DEFAULT 4,
		maintenance_window_end_min INTEGER DEFAULT 0,
		maintenance_window_timezone TEXT DEFAULT 'UTC',
		maintenance_window_days TEXT,
		rollout_staggered INTEGER NOT NULL DEFAULT 0,
		rollout_max_concurrent INTEGER DEFAULT 0,
		rollout_batch_size INTEGER DEFAULT 0,
		rollout_delay_between_waves INTEGER DEFAULT 300,
		rollout_jitter_seconds INTEGER DEFAULT 60,
		rollout_emergency_abort INTEGER NOT NULL DEFAULT 0,
		collect_telemetry INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- Release artifact cache (phase 2 auto-update intake)
	CREATE TABLE IF NOT EXISTS release_artifacts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		component TEXT NOT NULL,
		version TEXT NOT NULL,
		platform TEXT NOT NULL,
		arch TEXT NOT NULL,
		channel TEXT NOT NULL DEFAULT 'stable',
		source_url TEXT NOT NULL,
		cache_path TEXT,
		sha256 TEXT,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		release_notes TEXT,
		published_at DATETIME,
		downloaded_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(component, version, platform, arch)
	);

	CREATE INDEX IF NOT EXISTS idx_release_artifacts_component ON release_artifacts(component);

	CREATE TABLE IF NOT EXISTS signing_keys (
		id TEXT PRIMARY KEY,
		algorithm TEXT NOT NULL,
		public_key TEXT NOT NULL,
		private_key TEXT NOT NULL,
		notes TEXT,
		active INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		rotated_at DATETIME
	);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_signing_keys_active ON signing_keys(active)
		WHERE active = 1;

	CREATE TABLE IF NOT EXISTS release_manifests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		component TEXT NOT NULL,
		version TEXT NOT NULL,
		platform TEXT NOT NULL,
		arch TEXT NOT NULL,
		channel TEXT NOT NULL DEFAULT 'stable',
		manifest_version TEXT NOT NULL,
		manifest_json TEXT NOT NULL,
		signature TEXT NOT NULL,
		signing_key_id TEXT NOT NULL,
		generated_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(component, version, platform, arch)
	);

	CREATE INDEX IF NOT EXISTS idx_release_manifests_component ON release_manifests(component);

	CREATE TABLE IF NOT EXISTS self_update_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status TEXT NOT NULL,
		requested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		completed_at DATETIME,
		current_version TEXT,
		target_version TEXT,
		channel TEXT NOT NULL DEFAULT 'stable',
		platform TEXT,
		arch TEXT,
		release_artifact_id INTEGER,
		error_code TEXT,
		error_message TEXT,
		metadata_json TEXT,
		requested_by TEXT,
		FOREIGN KEY(release_artifact_id) REFERENCES release_artifacts(id) ON DELETE SET NULL
	);

	CREATE INDEX IF NOT EXISTS idx_self_update_runs_requested_at ON self_update_runs(requested_at DESC);
	CREATE INDEX IF NOT EXISTS idx_self_update_runs_status ON self_update_runs(status);

	-- =============================================
	-- Alerting System Tables
	-- =============================================

	-- Active and historical alerts
	CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_id INTEGER,
		type TEXT NOT NULL,
		severity TEXT NOT NULL,
		scope TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		tenant_id TEXT,
		site_id TEXT,
		agent_id TEXT,
		device_serial TEXT,
		title TEXT NOT NULL,
		message TEXT NOT NULL,
		details TEXT,
		triggered_at DATETIME NOT NULL,
		acknowledged_at DATETIME,
		acknowledged_by TEXT,
		resolved_at DATETIME,
		suppressed_until DATETIME,
		expires_at DATETIME,
		escalation_level INTEGER NOT NULL DEFAULT 0,
		last_escalated_at DATETIME,
		state_change_count INTEGER NOT NULL DEFAULT 0,
		is_flapping INTEGER NOT NULL DEFAULT 0,
		parent_alert_id INTEGER,
		child_count INTEGER NOT NULL DEFAULT 0,
		notifications_sent INTEGER NOT NULL DEFAULT 0,
		last_notified_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(rule_id) REFERENCES alert_rules(id) ON DELETE SET NULL,
		FOREIGN KEY(parent_alert_id) REFERENCES alerts(id) ON DELETE SET NULL
	);

	CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status);
	CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts(severity);
	CREATE INDEX IF NOT EXISTS idx_alerts_scope ON alerts(scope);
	CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts(type);
	CREATE INDEX IF NOT EXISTS idx_alerts_tenant_id ON alerts(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_alerts_triggered_at ON alerts(triggered_at DESC);

	-- Alert rules configuration
	CREATE TABLE IF NOT EXISTS alert_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		description TEXT,
		enabled INTEGER NOT NULL DEFAULT 1,
		type TEXT NOT NULL,
		severity TEXT NOT NULL,
		scope TEXT NOT NULL,
		tenant_ids TEXT,
		site_ids TEXT,
		agent_ids TEXT,
		condition_json TEXT,
		threshold REAL,
		threshold_unit TEXT,
		duration_minutes INTEGER NOT NULL DEFAULT 0,
		channel_ids TEXT,
		escalation_policy_id INTEGER,
		cooldown_minutes INTEGER NOT NULL DEFAULT 15,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_by TEXT,
		FOREIGN KEY(escalation_policy_id) REFERENCES escalation_policies(id) ON DELETE SET NULL
	);

	CREATE INDEX IF NOT EXISTS idx_alert_rules_type ON alert_rules(type);
	CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules(enabled);

	-- Notification channels
	CREATE TABLE IF NOT EXISTS notification_channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		config_json TEXT,
		min_severity TEXT NOT NULL DEFAULT 'warning',
		tenant_ids TEXT,
		rate_limit_per_hour INTEGER NOT NULL DEFAULT 100,
		last_sent_at DATETIME,
		sent_this_hour INTEGER NOT NULL DEFAULT 0,
		use_quiet_hours INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_notification_channels_type ON notification_channels(type);
	CREATE INDEX IF NOT EXISTS idx_notification_channels_enabled ON notification_channels(enabled);

	-- Escalation policies
	CREATE TABLE IF NOT EXISTS escalation_policies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		description TEXT,
		enabled INTEGER NOT NULL DEFAULT 1,
		steps_json TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- Maintenance windows for alert suppression
	CREATE TABLE IF NOT EXISTS maintenance_windows (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		description TEXT,
		scope TEXT NOT NULL,
		tenant_id TEXT,
		site_id TEXT,
		agent_id TEXT,
		device_serial TEXT,
		start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL,
		timezone TEXT NOT NULL DEFAULT 'UTC',
		recurring INTEGER NOT NULL DEFAULT 0,
		recur_pattern TEXT,
		recur_days TEXT,
		alert_types TEXT,
		allow_critical INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_by TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_maintenance_windows_time ON maintenance_windows(start_time, end_time);
	CREATE INDEX IF NOT EXISTS idx_maintenance_windows_scope ON maintenance_windows(scope);

	-- Alert settings (key-value for JSON storage)
	CREATE TABLE IF NOT EXISTS alert_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	-- =============================================
	-- Reporting System Tables
	-- =============================================

	-- Report definitions (templates)
	CREATE TABLE IF NOT EXISTS reports (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		description TEXT,
		type TEXT NOT NULL,
		format TEXT NOT NULL DEFAULT 'json',
		scope TEXT NOT NULL DEFAULT 'fleet',
		tenant_ids TEXT,
		site_ids TEXT,
		agent_ids TEXT,
		device_filter TEXT,
		time_range_type TEXT,
		time_range_days INTEGER,
		time_range_start TEXT,
		time_range_end TEXT,
		options_json TEXT,
		columns TEXT,
		group_by TEXT,
		order_by TEXT,
		report_limit INTEGER,
		email_recipients TEXT,
		webhook_url TEXT,
		created_by TEXT,
		is_built_in INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_reports_type ON reports(type);
	CREATE INDEX IF NOT EXISTS idx_reports_scope ON reports(scope);
	CREATE INDEX IF NOT EXISTS idx_reports_created_by ON reports(created_by);

	-- Report schedules
	CREATE TABLE IF NOT EXISTS report_schedules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		report_id INTEGER NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		frequency TEXT NOT NULL,
		day_of_week INTEGER,
		day_of_month INTEGER,
		time_of_day TEXT NOT NULL,
		timezone TEXT NOT NULL DEFAULT 'UTC',
		next_run_at DATETIME NOT NULL,
		last_run_at DATETIME,
		last_run_id INTEGER REFERENCES report_runs(id),
		failure_count INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_report_schedules_report ON report_schedules(report_id);
	CREATE INDEX IF NOT EXISTS idx_report_schedules_next_run ON report_schedules(next_run_at) WHERE enabled = 1;

	-- Report runs (execution history)
	CREATE TABLE IF NOT EXISTS report_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		report_id INTEGER NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
		schedule_id INTEGER REFERENCES report_schedules(id) ON DELETE SET NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		format TEXT NOT NULL,
		started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		duration_ms INTEGER,
		parameters_json TEXT,
		row_count INTEGER,
		result_size_bytes INTEGER,
		result_path TEXT,
		result_data TEXT,
		error_message TEXT,
		run_by TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_report_runs_report ON report_runs(report_id);
	CREATE INDEX IF NOT EXISTS idx_report_runs_schedule ON report_runs(schedule_id);
	CREATE INDEX IF NOT EXISTS idx_report_runs_status ON report_runs(status);
	CREATE INDEX IF NOT EXISTS idx_report_runs_started ON report_runs(started_at);

	-- Device web UI credentials (for proxy auto-login)
	-- Stored on server so agents remain stateless
	CREATE TABLE IF NOT EXISTS device_credentials (
		serial TEXT PRIMARY KEY,
		username TEXT NOT NULL DEFAULT '',
		encrypted_password TEXT NOT NULL DEFAULT '',
		auth_type TEXT NOT NULL DEFAULT 'basic',
		auto_login INTEGER NOT NULL DEFAULT 0,
		tenant_id TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(serial) REFERENCES devices(serial) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_device_credentials_tenant ON device_credentials(tenant_id);

	-- Tenant-scoped admin notification preferences (fleet level lives in config.toml)
	CREATE TABLE IF NOT EXISTS tenant_notification_settings (
		tenant_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0,
		recipients TEXT NOT NULL DEFAULT '[]',
		notify_on_critical INTEGER NOT NULL DEFAULT 1,
		notify_on_warning INTEGER NOT NULL DEFAULT 0,
		daily_summary_enabled INTEGER NOT NULL DEFAULT 0,
		daily_summary_time TEXT NOT NULL DEFAULT '08:00',
		daily_summary_timezone TEXT NOT NULL DEFAULT 'Local',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_by TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
	);
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	if err := s.ensureGlobalSettingsSeed(); err != nil {
		return err
	}

	// Seed default alert rules on first run
	if err := s.SeedDefaultAlertRules(context.Background()); err != nil {
		return err
	}

	// Run migrations for existing databases
	if err := s.runMigrations(); err != nil {
		return fmt.Errorf("migrations failed: %w", err)
	}

	logInfo("Schema initialized for DB", "path", s.dbPath, "schemaVersion", currentSchemaVersion)

	return nil
}

// runMigrations runs best-effort migrations for existing databases
func (s *SQLiteStore) runMigrations() error {
	// Legacy column migrations (safe to run even if columns exist)
	// We've consolidated all columns in the schema above, but keep this
	// for databases that were upgraded from older versions
	altStmts := []string{
		// tenancy tenant_id support
		"ALTER TABLE agents ADD COLUMN tenant_id TEXT",
		"ALTER TABLE devices ADD COLUMN tenant_id TEXT",
		"ALTER TABLE metrics_history ADD COLUMN tenant_id TEXT",
		"ALTER TABLE oidc_providers ADD COLUMN tenant_id TEXT",
		"ALTER TABLE tenants ADD COLUMN contact_name TEXT",
		"ALTER TABLE tenants ADD COLUMN contact_email TEXT",
		"ALTER TABLE tenants ADD COLUMN contact_phone TEXT",
		"ALTER TABLE tenants ADD COLUMN business_unit TEXT",
		"ALTER TABLE tenants ADD COLUMN billing_code TEXT",
		"ALTER TABLE tenants ADD COLUMN address TEXT",
		"ALTER TABLE tenants ADD COLUMN login_domain TEXT",
		"ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'",
		"ALTER TABLE agents ADD COLUMN name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE agents ADD COLUMN os_version TEXT",
		"ALTER TABLE agents ADD COLUMN go_version TEXT",
		"ALTER TABLE agents ADD COLUMN architecture TEXT",
		"ALTER TABLE agents ADD COLUMN num_cpu INTEGER DEFAULT 0",
		"ALTER TABLE agents ADD COLUMN total_memory_mb INTEGER DEFAULT 0",
		"ALTER TABLE agents ADD COLUMN build_type TEXT",
		"ALTER TABLE agents ADD COLUMN git_commit TEXT",
		"ALTER TABLE agents ADD COLUMN last_heartbeat DATETIME",
		"ALTER TABLE agents ADD COLUMN device_count INTEGER DEFAULT 0",
		"ALTER TABLE agents ADD COLUMN last_device_sync DATETIME",
		"ALTER TABLE agents ADD COLUMN last_metrics_sync DATETIME",
		"ALTER TABLE users ADD COLUMN email TEXT",
		// USB/spooler device classification support
		"ALTER TABLE devices ADD COLUMN device_type TEXT",
		"ALTER TABLE devices ADD COLUMN source_type TEXT",
		"ALTER TABLE devices ADD COLUMN is_usb INTEGER DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN port_name TEXT",
		"ALTER TABLE devices ADD COLUMN driver_name TEXT",
		"ALTER TABLE devices ADD COLUMN is_default INTEGER DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN is_shared INTEGER DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN spooler_status TEXT",
	}

	for _, stmt := range altStmts {
		if _, err := s.db.Exec(stmt); err != nil {
			if isSQLiteDuplicateColumnErr(err) {
				logDebug("SQLite migration statement skipped (column already exists)", "stmt", stmt)
				continue
			}
			logWarn("SQLite migration statement (ignored error)", "stmt", stmt, "error", err)
		} else {
			logDebug("SQLite migration statement applied (or already present)", "stmt", stmt)
		}
	}

	// Create unique index for tenant login domains
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_login_domain ON tenants(login_domain) WHERE login_domain IS NOT NULL AND login_domain != ''`); err != nil {
		logWarn("SQLite migration statement (ignored error)", "stmt", "CREATE UNIQUE INDEX idx_tenants_login_domain", "error", err)
	} else {
		logDebug("SQLite migration statement applied (or already present)", "stmt", "CREATE UNIQUE INDEX idx_tenants_login_domain")
	}

	// Data migrations
	s.backfillUserTenantMappings()
	s.migrateLegacyRoles()
	if err := s.migrateSettingsAgentOverrideFK(); err != nil {
		logWarn("SQLite migration failed (settings_agent_override foreign key)", "error", err)
	}
	s.migrateAlertRuleTypes()

	// Update schema version
	var currentVersion int
	err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("failed to check schema version: %w", err)
	}

	if currentVersion < currentSchemaVersion {
		_, err = s.db.Exec("INSERT OR REPLACE INTO schema_version (version, applied_at) VALUES (?, ?)",
			currentSchemaVersion, time.Now())
		if err != nil {
			return fmt.Errorf("failed to update schema version: %w", err)
		}
	}

	// Security migration: detect and clear legacy sessions stored as raw tokens
	// New behavior stores SHA-256 hex tokens (64 chars). If any existing rows
	// contain tokens of a different length, clear the sessions table so users
	// must re-authenticate.
	var nonHashedCount int
	err = s.db.QueryRow("SELECT COALESCE(COUNT(1),0) FROM sessions WHERE LENGTH(token) != 64").Scan(&nonHashedCount)
	if err == nil && nonHashedCount > 0 {
		logInfo("Clearing legacy sessions (security migration)", "non_hashed_count", nonHashedCount)
		if _, derr := s.db.Exec("DELETE FROM sessions"); derr != nil {
			logWarn("Failed to clear legacy sessions", "error", derr)
		}
	}

	return nil
}

func (s *SQLiteStore) migrateSettingsAgentOverrideFK() error {
	// Ensure settings_agent_override.agent_id references agents.agent_id (stable UUID), not agents.id.
	// SQLite can't alter foreign key constraints in-place, so rebuild the table if needed.
	var tableExists int
	err := s.db.QueryRow(`SELECT COALESCE(COUNT(1), 0) FROM sqlite_master WHERE type='table' AND name='settings_agent_override'`).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("failed to check settings_agent_override existence: %w", err)
	}
	if tableExists == 0 {
		return nil
	}

	var fkToAgentsID int
	err = s.db.QueryRow(`SELECT COALESCE(COUNT(1),0) FROM pragma_foreign_key_list('settings_agent_override') WHERE "table" = 'agents' AND "to" = 'id'`).Scan(&fkToAgentsID)
	if err != nil {
		return fmt.Errorf("failed to inspect settings_agent_override foreign keys: %w", err)
	}
	if fkToAgentsID == 0 {
		return nil
	}

	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("failed to disable foreign keys for migration: %w", err)
	}
	defer s.db.Exec("PRAGMA foreign_keys=ON")

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS settings_agent_override_new"); err != nil {
		return fmt.Errorf("failed to drop settings_agent_override_new: %w", err)
	}

	create := `
		CREATE TABLE settings_agent_override_new (
			agent_id TEXT PRIMARY KEY,
			schema_version TEXT NOT NULL,
			payload TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_by TEXT,
			FOREIGN KEY(agent_id) REFERENCES agents(agent_id) ON DELETE CASCADE
		);
	`
	if _, err := tx.Exec(create); err != nil {
		return fmt.Errorf("failed to create settings_agent_override_new: %w", err)
	}

	// Preserve data and normalize any legacy numeric values that referenced agents.id.
	copy := `
		INSERT OR REPLACE INTO settings_agent_override_new (agent_id, schema_version, payload, updated_at, updated_by)
		SELECT
			COALESCE(a1.agent_id, a2.agent_id, o.agent_id) AS agent_id,
			o.schema_version,
			o.payload,
			o.updated_at,
			o.updated_by
		FROM settings_agent_override o
		LEFT JOIN agents a1 ON a1.agent_id = o.agent_id
		LEFT JOIN agents a2 ON (o.agent_id NOT GLOB '*[^0-9]*' AND a2.id = CAST(o.agent_id AS INTEGER));
	`
	if _, err := tx.Exec(copy); err != nil {
		return fmt.Errorf("failed to copy settings_agent_override data: %w", err)
	}

	if _, err := tx.Exec("DROP TABLE settings_agent_override"); err != nil {
		return fmt.Errorf("failed to drop old settings_agent_override: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE settings_agent_override_new RENAME TO settings_agent_override"); err != nil {
		return fmt.Errorf("failed to rename settings_agent_override_new: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit settings_agent_override migration: %w", err)
	}

	logInfo("SQLite migrated settings_agent_override foreign key")
	return nil
}

func (s *SQLiteStore) ensureGlobalSettingsSeed() error {
	defaults := pmsettings.DefaultSettings()
	pmsettings.Sanitize(&defaults)
	payload, err := json.Marshal(defaults)
	if err != nil {
		return fmt.Errorf("failed to marshal default settings: %w", err)
	}
	stmt := `
		INSERT INTO settings_global (id, schema_version, payload, updated_at, updated_by)
		SELECT 1, ?, ?, CURRENT_TIMESTAMP, 'system'
		WHERE NOT EXISTS (SELECT 1 FROM settings_global WHERE id = 1);
	`
	if _, err := s.db.Exec(stmt, pmsettings.SchemaVersion, string(payload)); err != nil {
		return fmt.Errorf("failed to seed global settings: %w", err)
	}
	return nil
}

// backfillUserTenantMappings migrates legacy single-tenant users to user_tenants table
func (s *SQLiteStore) backfillUserTenantMappings() {
	// Check if we have any users with tenant_id that are not in user_tenants
	rows, err := s.db.Query(`
		SELECT u.id, u.tenant_id 
		FROM users u
		WHERE u.tenant_id IS NOT NULL 
		  AND u.tenant_id != ''
		  AND NOT EXISTS (SELECT 1 FROM user_tenants ut WHERE ut.user_id = u.id AND ut.tenant_id = u.tenant_id)
	`)
	if err != nil {
		logDebug("backfillUserTenantMappings: query failed", "error", err)
		return
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var userID int
		var tenantID string
		if err := rows.Scan(&userID, &tenantID); err != nil {
			continue
		}
		_, _ = s.db.Exec("INSERT OR IGNORE INTO user_tenants (user_id, tenant_id) VALUES (?, ?)", userID, tenantID)
		count++
	}
	if count > 0 {
		logInfo("Backfilled user_tenants mappings", "count", count)
	}
}

// migrateLegacyRoles converts old role names to new standard ones
func (s *SQLiteStore) migrateLegacyRoles() {
	// "admin" -> "admin" (unchanged)
	// "viewer" -> "viewer" (unchanged)
	// Empty or NULL -> "user"
	_, _ = s.db.Exec(`UPDATE users SET role = 'user' WHERE role IS NULL OR role = ''`)
}

// migrateAlertRuleTypes updates legacy alert rule types and scopes to match
// what the evaluator expects. Old rules used generic types like "threshold"
// and scope "all" which the evaluator doesn't recognize.
func (s *SQLiteStore) migrateAlertRuleTypes() {
	// Check if alert_rules table exists
	var tableExists int
	err := s.db.QueryRow(`SELECT COALESCE(COUNT(1), 0) FROM sqlite_master WHERE type='table' AND name='alert_rules'`).Scan(&tableExists)
	if err != nil || tableExists == 0 {
		return
	}

	migrations := []struct {
		desc     string
		oldType  string
		oldScope string
		newType  string
		newScope string
		where    string
	}{
		// "threshold" type with warning severity -> supply_low
		{"Low Toner Warning", "threshold", "all", "supply_low", "device",
			"type = 'threshold' AND severity = 'warning' AND scope = 'all'"},
		// "threshold" type with critical severity -> supply_critical
		{"Critical Toner Level", "threshold", "all", "supply_critical", "device",
			"type = 'threshold' AND severity = 'critical' AND scope = 'all'"},
		// "offline" type -> device_offline
		{"Printer Offline", "offline", "all", "device_offline", "device",
			"type = 'offline' AND scope = 'all'"},
		// "status" type -> device_error
		{"Status Alerts", "status", "all", "device_error", "device",
			"type = 'status' AND scope = 'all'"},
		// agent_offline with scope all -> agent scope
		{"Agent Disconnected", "agent_offline", "all", "agent_offline", "agent",
			"type = 'agent_offline' AND scope = 'all'"},
	}

	for _, m := range migrations {
		result, err := s.db.Exec(
			`UPDATE alert_rules SET type = ?, scope = ? WHERE `+m.where,
			m.newType, m.newScope)
		if err != nil {
			logDebug("migrateAlertRuleTypes: failed to update", "desc", m.desc, "error", err)
			continue
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			logInfo("Migrated alert rules", "desc", m.desc, "count", affected, "oldType", m.oldType, "newType", m.newType)
		}
	}
}

// isSQLiteDuplicateColumnErr returns true when the provided error indicates an
// ALTER TABLE attempted to add a column that already exists.
func isSQLiteDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// GetDeviceCredentials retrieves credentials for proxy auto-login
func (s *SQLiteStore) GetDeviceCredentials(ctx context.Context, serial string) (*DeviceCredentials, error) {
	var creds DeviceCredentials
	var autoLogin int
	err := s.db.QueryRowContext(ctx, `
		SELECT serial, username, encrypted_password, auth_type, auto_login, tenant_id, created_at, updated_at
		FROM device_credentials
		WHERE serial = ?
	`, serial).Scan(
		&creds.Serial, &creds.Username, &creds.EncryptedPassword, &creds.AuthType,
		&autoLogin, &creds.TenantID, &creds.CreatedAt, &creds.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	creds.AutoLogin = autoLogin != 0
	return &creds, nil
}

// UpsertDeviceCredentials creates or updates credentials for a device
func (s *SQLiteStore) UpsertDeviceCredentials(ctx context.Context, creds *DeviceCredentials) error {
	autoLogin := 0
	if creds.AutoLogin {
		autoLogin = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_credentials (serial, username, encrypted_password, auth_type, auto_login, tenant_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(serial) DO UPDATE SET
			username = excluded.username,
			encrypted_password = excluded.encrypted_password,
			auth_type = excluded.auth_type,
			auto_login = excluded.auto_login,
			tenant_id = excluded.tenant_id,
			updated_at = CURRENT_TIMESTAMP
	`, creds.Serial, creds.Username, creds.EncryptedPassword, creds.AuthType, autoLogin, creds.TenantID)
	return err
}

// DeleteDeviceCredentials removes credentials for a device
func (s *SQLiteStore) DeleteDeviceCredentials(ctx context.Context, serial string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM device_credentials WHERE serial = ?`, serial)
	return err
}

// tableHasColumn checks if a table has a specific column
// Note: Logging helpers (logInfo, logDebug, logWarn) are defined in logging.go
