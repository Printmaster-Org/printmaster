// PrintMaster Server - Web UI JavaScript

const DEFAULT_ROLE_PRIORITY = { admin: 3, operator: 2, viewer: 1 };
const BASE_TAB_LABELS = {
    dashboard: 'Dashboard',
    agents: 'Agents',
    devices: 'Devices',
    metrics: 'Metrics',
    logs: 'Logs'
};

// Admin tab consolidates: Users, Access, Tenants, Fleet, Server, Alerts Config, Audit
const TAB_DEFINITIONS = {
    alerts: {
        label: 'Alerts',
        minRole: 'operator', // Operators can view alerts (not just admins)
        templateId: 'tab-template-alerts',
        onMount: () => initAlertsTab()
    },
    admin: {
        label: 'Admin',
        // Allow operators+ to see Admin tab - specific subtabs are gated by applyAdminSubtabVisibility()
        minRole: 'operator',
        templateId: 'tab-template-admin',
        onMount: () => initAdminTab()
    }
};

// Valid admin sub-views
const VALID_ADMIN_VIEWS = ['users', 'access', 'tenants', 'fleet', 'server', 'alertsconfig', 'audit'];

// Valid alerts sub-views
const VALID_ALERTS_VIEWS = ['summary', 'active', 'history', 'reports'];

const SERVER_UI_STATE_KEYS = {
    ACTIVE_TAB: 'pm_server_active_tab',
    ADMIN_VIEW: 'pm_server_admin_view',
    ALERTS_VIEW: 'pm_server_alerts_view',
    SETTINGS_VIEW: 'pm_server_settings_view',
    LOG_VIEW: 'pm_server_log_view',
    LOG_VIEW_MODE: 'pm_server_log_view_mode',
    TENANTS_VIEW: 'pm_server_tenants_view',
    AGENTS_VIEW: 'pm_server_agents_view',
    AGENTS_SORT_KEY: 'pm_server_agents_sort_key',
    AGENTS_SORT_DIR: 'pm_server_agents_sort_dir',
    DEVICES_VIEW: 'pm_server_devices_view',
    DEVICES_SORT_KEY: 'pm_server_devices_sort_key',
    DEVICES_SORT_DIR: 'pm_server_devices_sort_dir',
};

const VALID_SETTINGS_VIEWS = ['server', 'sso', 'fleet', 'updates'];
const VALID_LOG_VIEWS = ['system'];
const VALID_LOG_VIEW_MODES = ['table', 'raw'];
const VALID_TENANT_VIEWS = ['directory'];

function getPersistedUIState(key, fallback, allowedValues) {
    try {
        if (typeof window === 'undefined' || !window.localStorage) {
            return fallback;
        }
        const value = window.localStorage.getItem(key);
        if (value === null || value === undefined || value === '') {
            return fallback;
        }
        if (Array.isArray(allowedValues) && allowedValues.length > 0 && !allowedValues.includes(value)) {
            return fallback;
        }
        return value;
    } catch (err) {
        return fallback;
    }
}

function persistUIState(key, value) {
    try {
        if (typeof window === 'undefined' || !window.localStorage) {
            return;
        }
        window.localStorage.setItem(key, value);
    } catch (err) {
        // No-op: best-effort persistence only
    }
}

let currentUser = null;
const mountedTabs = new Set();

/**
 * Check if the current user is scoped to specific tenants (not a global admin).
 * Tenant-scoped users have tenant_ids populated and should not see global-level settings.
 */
function isTenantScopedUser() {
    if (!currentUser) return false;
    // Admins are never tenant-scoped
    if (normalizeRole(currentUser.role) === 'admin') return false;
    // Check if user has specific tenant assignments
    const tenantIds = currentUser.tenant_ids;
    return Array.isArray(tenantIds) && tenantIds.length > 0;
}

/**
 * Get the tenant IDs the current user is allowed to access.
 * Returns empty array for global admins (they can access all).
 */
function getUserTenantIds() {
    if (!currentUser) return [];
    const tenantIds = currentUser.tenant_ids;
    return Array.isArray(tenantIds) ? tenantIds : [];
}

/**
 * Initialize horizontal scroll indicators for table wrappers.
 * Adds visual cues (shadow gradients) to show when more content is available.
 * @param {HTMLElement|string} containerOrSelector - The table wrapper element or selector
 */
function initTableScrollIndicators(containerOrSelector) {
    const container = typeof containerOrSelector === 'string'
        ? document.querySelector(containerOrSelector)
        : containerOrSelector;

    if (!container || !container.classList.contains('table-wrapper')) return;

    const updateScrollIndicators = () => {
        const { scrollLeft, scrollWidth, clientWidth } = container;
        const canScrollLeft = scrollLeft > 5;
        const canScrollRight = scrollLeft + clientWidth < scrollWidth - 5;

        container.classList.toggle('can-scroll-left', canScrollLeft);
        container.classList.toggle('can-scroll-right', canScrollRight);
    };

    // Initial check
    updateScrollIndicators();

    // Update on scroll
    container.addEventListener('scroll', updateScrollIndicators, { passive: true });

    // Update on resize
    const resizeObserver = new ResizeObserver(updateScrollIndicators);
    resizeObserver.observe(container);

    // Store cleanup function for later removal if needed
    container._scrollIndicatorCleanup = () => {
        container.removeEventListener('scroll', updateScrollIndicators);
        resizeObserver.disconnect();
    };
}

/**
 * Initialize all table scroll indicators in the document.
 * Called on page load and after dynamic content updates.
 */
function initAllTableScrollIndicators() {
    document.querySelectorAll('.table-wrapper').forEach(wrapper => {
        if (!wrapper._scrollIndicatorCleanup) {
            initTableScrollIndicators(wrapper);
        }
    });
}

function initFilterDrawer(sidebarId, triggerId, closeId) {
    const sidebar = document.getElementById(sidebarId);
    const trigger = document.getElementById(triggerId);
    const closeButton = document.getElementById(closeId);
    if (!sidebar || !trigger || !closeButton) return;

    const setExpanded = (expanded, restoreFocus = false) => {
        sidebar.classList.toggle('collapsed', !expanded);
        trigger.setAttribute('aria-expanded', String(expanded));
        if (restoreFocus) trigger.focus();
    };

    setExpanded(false);
    trigger.addEventListener('click', () => {
        setExpanded(true);
        const firstField = sidebar.querySelector('input:not(:disabled), select:not(:disabled)');
        if (firstField) firstField.focus();
    });
    closeButton.addEventListener('click', () => setExpanded(false, true));

    document.addEventListener('pointerdown', event => {
        if (!sidebar.classList.contains('collapsed') &&
            !sidebar.contains(event.target) &&
            !trigger.contains(event.target)) {
            setExpanded(false);
        }
    });
    document.addEventListener('keydown', event => {
        if (event.key === 'Escape' && !sidebar.classList.contains('collapsed')) {
            setExpanded(false, true);
        }
    });
}

/**
 * Check if user is a global admin (not tenant-scoped).
 */
function isGlobalAdmin() {
    return currentUser && normalizeRole(currentUser.role) === 'admin';
}
let usersUIInitialized = false;
let tenantsUIInitialized = false;
let tenantModalInitialized = false;
let tenantsSubtabsInitialized = false;
let activeTenantsView = getPersistedUIState(SERVER_UI_STATE_KEYS.TENANTS_VIEW, 'directory', VALID_TENANT_VIEWS);
let addAgentUIInitialized = false;
let ssoAdminInitialized = false;
let logSubtabsInitialized = false;
let settingsSubtabsInitialized = false;
let adminSubtabsInitialized = false;
let alertsSubtabsInitialized = false;
let activeAdminView = getPersistedUIState(SERVER_UI_STATE_KEYS.ADMIN_VIEW, 'users', VALID_ADMIN_VIEWS);
let activeAlertsView = getPersistedUIState(SERVER_UI_STATE_KEYS.ALERTS_VIEW, 'summary', VALID_ALERTS_VIEWS);
let activeSettingsView = getPersistedUIState(SERVER_UI_STATE_KEYS.SETTINGS_VIEW, 'server', VALID_SETTINGS_VIEWS);
let activeLogView = getPersistedUIState(SERVER_UI_STATE_KEYS.LOG_VIEW, 'system', VALID_LOG_VIEWS);
let activeLogViewMode = getPersistedUIState(SERVER_UI_STATE_KEYS.LOG_VIEW_MODE, 'table', VALID_LOG_VIEW_MODES);
let currentLogLines = []; // Store parsed log entries for view switching

// Logs pagination and infinite scroll state
const logsState = {
    entries: [],          // Parsed log entries currently loaded
    total: 0,             // Total logs available on server (with current filters)
    offset: 0,            // Current offset into the filtered logs
    limit: 50,            // Page size
    hasMore: false,       // More older logs available
    hasPrevious: false,   // Newer logs available (when scrolled up)
    loading: false,       // Currently fetching
    searchDebounce: null, // Debounce timer for search
    scrollObserver: null, // IntersectionObserver for infinite scroll
    maxLoaded: 100,       // Maximum entries to keep loaded (cull beyond this)
    lastLoadTime: 0,      // Timestamp of last load (for cooldown)
    cooldownMs: 2000,     // Minimum ms between infinite scroll loads
};

const AUDIT_SEVERITY_VALUES = ['error', 'warn', 'info'];
const AUDIT_AUTO_REFRESH_INTERVAL_MS = 15000;
let auditLogEntries = [];
let auditFilterState = {
    search: '',
    action: '',
    tenant: '',
    severities: new Set(AUDIT_SEVERITY_VALUES),
};
let auditFiltersInitialized = false;
let auditAutoRefreshHandle = null;
let auditLastUpdated = null;
let auditLiveRequested = false;
// Progressive rendering state for audit logs
const auditRenderState = {
    displayed: 0,
    pageSize: 50,
    observer: null,
    filteredEntries: [],
};
let auditDataLoaded = false;

// Progressive rendering state for alert history
const alertHistoryRenderState = {
    displayed: 0,
    pageSize: 50,
    observer: null,
    filteredAlerts: [],
    allAlerts: [],
};

const METRICS_RANGE_WINDOWS = {
    '5m': 5 * 60 * 1000,
    '15m': 15 * 60 * 1000,
    '30m': 30 * 60 * 1000,
    '1h': 1 * 60 * 60 * 1000,
    '6h': 6 * 60 * 60 * 1000,
    '12h': 12 * 60 * 60 * 1000,
    '24h': 24 * 60 * 60 * 1000,
    '7d': 7 * 24 * 60 * 60 * 1000,
    '30d': 30 * 24 * 60 * 60 * 1000,
    '90d': 90 * 24 * 60 * 60 * 1000,
    '365d': 365 * 24 * 60 * 60 * 1000,
};
const METRICS_DEFAULT_RANGE = '6h';
// FLEET_SERIES_COLORS is now provided by utils/charts.js
const metricsVM = {
    range: METRICS_DEFAULT_RANGE,
    summary: null,
    aggregated: null,
    loading: false,
    lastFetched: null,
    error: null,
    // Filter state for scoped metrics
    filters: {
        tenantId: '',
        agentId: '',
        deviceSerial: '',
    },
};

const DEVICE_STATUS_KEYS = ['healthy', 'warning', 'error', 'jam', 'offline', 'unknown'];
const DEVICE_STATUS_ORDER = { healthy: 0, warning: 1, error: 2, jam: 3, offline: 4, unknown: 5 };
const DEVICE_OFFLINE_AFTER_MS = 15 * 60 * 1000;
const DEVICE_CONSUMABLE_KEYS = ['critical', 'low', 'medium', 'high', 'unknown'];
const DEVICE_CONSUMABLE_ORDER = { critical: 4, low: 3, medium: 2, high: 1, unknown: 0 };
const DEVICE_CONSUMABLE_LABELS = {
    critical: 'Critical',
    low: 'Low',
    medium: 'Medium',
    high: 'High',
    unknown: 'Unknown',
};
const DEVICES_SORT_KEYS = ['last_seen', 'manufacturer', 'agent', 'tenant', 'status', 'location', 'ip', 'page_count', 'consumables'];
const DEVICES_VIEW_OPTIONS = ['cards', 'table'];
const DEVICES_DEFAULT_VIEW = getPersistedUIState(SERVER_UI_STATE_KEYS.DEVICES_VIEW, 'table', DEVICES_VIEW_OPTIONS);
const DEVICES_DEFAULT_SORT_KEY = getPersistedUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_KEY, 'last_seen', DEVICES_SORT_KEYS);
const DEVICES_DEFAULT_SORT_DIR = getPersistedUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_DIR, 'desc', ['asc', 'desc']);

const AGENT_STATUS_KEYS = ['active', 'degraded', 'offline'];
const AGENT_STATUS_ORDER = { active: 0, degraded: 1, offline: 2 };
const AGENT_STATUS_LABELS = { active: 'Active', degraded: 'Degraded', offline: 'Offline' };
const AGENT_STATUS_COLORS = {
    active: 'var(--success)',
    degraded: 'var(--warning)',
    offline: 'var(--danger)'
};
const AGENTS_SORT_KEYS = ['last_seen', 'name', 'tenant', 'status', 'version', 'platform'];
const AGENTS_VIEW_OPTIONS = ['cards', 'table'];
const AGENTS_DEFAULT_VIEW = getPersistedUIState(SERVER_UI_STATE_KEYS.AGENTS_VIEW, 'table', AGENTS_VIEW_OPTIONS);
const AGENTS_DEFAULT_SORT_KEY = getPersistedUIState(SERVER_UI_STATE_KEYS.AGENTS_SORT_KEY, 'last_seen', AGENTS_SORT_KEYS);
const AGENTS_DEFAULT_SORT_DIR = getPersistedUIState(SERVER_UI_STATE_KEYS.AGENTS_SORT_DIR, 'desc', ['asc', 'desc']);
const AGENTS_METRICS_MAX_AGE_MS = 60 * 1000;

const devicesVM = {
    loading: false,
    loaded: false,
    error: null,
    items: [],
    filtered: [],
    filters: {
        query: '',
        agentId: '',
        tenantId: '',
        manufacturer: '',
        statuses: new Set(DEVICE_STATUS_KEYS),
        consumables: new Set(DEVICE_CONSUMABLE_KEYS),
        sortKey: DEVICES_DEFAULT_SORT_KEY || 'last_seen',
        sortDir: DEVICES_DEFAULT_SORT_DIR || 'desc',
    },
    view: DEVICES_DEFAULT_VIEW || 'table',
    stats: {
        total: 0,
        filtered: 0,
        totalStatuses: {},
        filteredStatuses: {},
    },
    uiInitialized: false,
    // Progressive rendering state
    render: {
        displayed: 0,
        pageSize: 30,
        observer: null,
    },
    // Table customizer instance
    tableCustomizer: null,
    // Selection state (file-explorer style selection)
    selection: {
        selectedIds: new Set(),    // Set of selected device IDs (serial or IP)
        lastSelected: null,         // Last selected ID for shift-click range selection
    },
};

const agentsVM = {
    loading: false,
    loaded: false,
    error: null,
    items: [],
    filtered: [],
    metrics: {
        summary: null,
        lastFetched: null,
    },
    latestVersion: null,
    // Per-agent update state: { agentId: { status, progress, message, targetVersion, error } }
    updateState: {},
    // Whether to automatically check for agent updates on page load
    checkUpdatesOnLoad: true,
    // Track if an update check is in progress
    updateCheckInProgress: false,
    filters: {
        query: '',
        version: '',
        platform: '',
        tenantId: '',
        statuses: new Set(AGENT_STATUS_KEYS),
        sortKey: AGENTS_DEFAULT_SORT_KEY || 'last_seen',
        sortDir: AGENTS_DEFAULT_SORT_DIR || 'desc',
    },
    view: AGENTS_DEFAULT_VIEW || 'table',
    stats: {
        total: 0,
        filtered: 0,
        totalStatuses: buildAgentStatusCounts(),
        filteredStatuses: buildAgentStatusCounts(),
    },
    uiInitialized: false,
    // Table customizer instance
    tableCustomizer: null,
    // Selection state (file-explorer style selection)
    selection: {
        selectedIds: new Set(),    // Set of selected agent IDs
        lastSelected: null,         // Last selected ID for shift-click range selection
    },
};

const tenantsVM = {
    loading: false,
    loaded: false,
    error: null,
    items: [],
    filtered: [],
    filters: {
        query: '',
        sortKey: 'name',
        sortDir: 'asc',
    },
    stats: {
        total: 0,
        filtered: 0,
    },
    uiInitialized: false,
};

const agentDirectory = {
    items: [],
    byId: new Map(),
    lastFetched: 0,
};

const tenantDirectory = {
    items: [],
    byId: new Map(),
    lastFetched: 0,
};

const SERVER_SETTINGS_SCHEMA = [
    {
        section: 'server',
        title: 'Network & Proxy',
        description: 'Listener ports, binding address, and proxy awareness.',
        fields: [
            { key: 'http_port', label: 'HTTP Port', type: 'number', min: 1, max: 65535, required: true, helper: 'Plain HTTP listener used for health checks or reverse proxies.', configKey: 'server.http_port' },
            { key: 'https_port', label: 'HTTPS Port', type: 'number', min: 1, max: 65535, required: true, helper: 'Direct TLS listener when not running behind a reverse proxy.', configKey: 'server.https_port' },
            { key: 'bind_address', label: 'Bind Address', type: 'text', placeholder: '0.0.0.0', helper: 'Interface to bind when accepting connections.', configKey: 'server.bind_address', fullWidth: true },
            { key: 'behind_proxy', label: 'Behind Reverse Proxy', type: 'checkbox', helper: 'Trust X-Forwarded-* headers and skip automatic TLS.', configKey: 'server.behind_proxy' },
            { key: 'proxy_use_https', label: 'Proxy Uses HTTPS', type: 'checkbox', helper: 'When behind a proxy, assume incoming traffic was HTTPS.', configKey: 'server.proxy_use_https' },
            { key: 'auto_approve_agents', label: 'Auto-Approve Agents', type: 'checkbox', helper: 'Automatically trust new agents without manual approval.', configKey: 'server.auto_approve_agents' },
            { key: 'agent_timeout_minutes', label: 'Agent Timeout (minutes)', type: 'number', min: 1, helper: 'Time window before an agent is considered offline.', configKey: 'server.agent_timeout_minutes' }
        ]
    },
    {
        section: 'security',
        title: 'Authentication & Rate Limits',
        description: 'Brute-force protection for the built-in login experience.',
        fields: [
            { key: 'rate_limit_enabled', label: 'Rate Limiting Enabled', type: 'checkbox', helper: 'Reject login attempts after repeated failures.', configKey: 'security.rate_limit_enabled' },
            { key: 'rate_limit_max_attempts', label: 'Max Attempts', type: 'number', min: 1, helper: 'Failed logins allowed before triggering a block.', configKey: 'security.rate_limit_max_attempts' },
            { key: 'rate_limit_block_minutes', label: 'Block Duration (minutes)', type: 'number', min: 1, helper: 'How long to block an IP/user after exceeding attempts.', configKey: 'security.rate_limit_block_minutes' },
            { key: 'rate_limit_window_minutes', label: 'Window (minutes)', type: 'number', min: 1, helper: 'Rolling window for counting failed attempts.', configKey: 'security.rate_limit_window_minutes' }
        ]
    },
    {
        section: 'tls',
        title: 'TLS & Certificates',
        description: 'Choose how HTTPS certificates are provisioned.',
        fields: [
            {
                key: 'mode',
                label: 'TLS Mode',
                type: 'select',
                required: true,
                options: [
                    { value: 'self-signed', label: 'Self-signed (default)' },
                    { value: 'custom', label: 'Custom certificate' },
                    { value: 'letsencrypt', label: 'Let\'s Encrypt (automatic)' }
                ]

            },

            { key: 'letsencrypt_domain', label: 'Let\'s Encrypt Domain', type: 'text', placeholder: 'pm.yourdomain.com', helper: 'FQDN requested from Let\'s Encrypt.', configKey: 'tls.letsencrypt.domain' },
            { key: 'letsencrypt_email', label: 'Let\'s Encrypt Email', type: 'text', placeholder: 'ops@yourdomain.com', helper: 'Administrative contact for ACME registration.', configKey: 'tls.letsencrypt.email' },
            { key: 'letsencrypt_cache_dir', label: 'Let\'s Encrypt Cache Dir', type: 'text', placeholder: 'letsencrypt-cache', helper: 'Directory for cached ACME assets.', configKey: 'tls.letsencrypt.cache_dir' },
            { key: 'letsencrypt_accept_tos', label: 'Accept Let\'s Encrypt Terms', type: 'checkbox', helper: 'Required before automatic certificate issuance.', configKey: 'tls.letsencrypt.accept_tos' }
        ]
    },
    {
        section: 'logging',
        title: 'Logging Level',
        description: 'Control verbosity for new log entries.',
        fields: [
            {
                key: 'level',
                label: 'Log Level',
                type: 'select',
                required: true,
                options: [
                    { value: 'ERROR', label: 'ERROR' },
                    { value: 'WARN', label: 'WARN' },
                    { value: 'INFO', label: 'INFO' },
                    { value: 'DEBUG', label: 'DEBUG' },
                    { value: 'TRACE', label: 'TRACE' }
                ],
                helper: 'Changes apply immediately without restarting.',
                configKey: 'logging.level'
            }
        ]
    },
    {
        section: 'releases',
        title: 'Release Intake',
        description: 'Control how many GitHub releases are cached locally for auto-update and packaging.',
        fields: [
            { key: 'include_prerelease', label: 'Include Beta / Dev Releases', type: 'select', options: [{ value: '', label: 'Automatic (based on Server build)' }, { value: 'true', label: 'Enabled' }, { value: 'false', label: 'Disabled' }], helper: 'Required to cache beta/dev releases. Restart Server after changing, then sync release artifacts.', configKey: 'releases.include_prerelease' },
            { key: 'max_releases', label: 'Max Releases per Component', type: 'number', min: 1, required: true, helper: 'Upper bound of releases ingested for each component on every sync.', configKey: 'releases.max_releases' },
            { key: 'poll_interval_minutes', label: 'Sync Interval (minutes)', type: 'number', min: 15, required: true, helper: 'How often the server polls GitHub for new releases.', configKey: 'releases.poll_interval_minutes' },
            { key: 'retention_versions', label: 'Retention (versions)', type: 'number', min: 0, required: true, helper: 'How many versions to keep per component. Set to 0 to keep all versions (no pruning).', configKey: 'releases.retention_versions' }
        ]
    },
    {
        section: 'self_update',
        title: 'Server Self-Update',
        description: 'Adjust how the server checks for and stages new versions.',
        fields: [
            { key: 'enabled', label: 'Enable Self-Update', type: 'checkbox', helper: 'Allow the server to download and stage signed updates automatically.', configKey: 'server.self_update_enabled' },
            { key: 'channel', label: 'Update Channel', type: 'text', placeholder: 'stable', required: true, helper: 'Release channel to follow (e.g. stable, beta).', configKey: 'self_update.channel' },
            { key: 'max_artifacts', label: 'Max Cached Artifacts', type: 'number', min: 1, required: true, helper: 'Number of newest artifacts evaluated when picking an update candidate.', configKey: 'self_update.max_artifacts' },
            { key: 'check_interval_minutes', label: 'Check Interval (minutes)', type: 'number', min: 30, required: true, helper: 'Frequency of automatic self-update checks.', configKey: 'self_update.check_interval_minutes' }
        ]
    },
    {
        section: 'smtp',
        title: 'SMTP Notifications',
        description: 'Optional email settings for alerts and reports.',
        fields: [
            { key: 'enabled', label: 'Enable SMTP', type: 'checkbox', helper: 'Toggle email delivery for alerting.', configKey: 'smtp.enabled' },
            { key: 'host', label: 'SMTP Host', type: 'text', placeholder: 'smtp.office365.com', helper: 'Hostname or IP of your SMTP relay.', configKey: 'smtp.host' },
            { key: 'port', label: 'SMTP Port', type: 'number', min: 1, max: 65535, helper: 'Port used to connect to your SMTP server.', configKey: 'smtp.port' },
            { key: 'user', label: 'SMTP Username', type: 'text', helper: 'Leave blank if your relay allows anonymous auth.', configKey: 'smtp.user' },
            { key: 'pass', label: 'SMTP Password', type: 'password', placeholder: 'Leave blank to keep existing secret', helper: 'Value is only stored if you provide a new password.', configKey: 'smtp.pass' },
            { key: 'from', label: 'From Address', type: 'text', placeholder: 'printmaster@yourdomain.com', helper: 'Default sender for outbound email.', configKey: 'smtp.from' },
            {
                key: 'email_theme', label: 'Email Theme', type: 'select', options: [
                    { value: 'auto', label: 'Auto (follows user preference)' },
                    { value: 'dark', label: 'Dark (Solarized Dark)' },
                    { value: 'light', label: 'Light (Solarized Light)' }
                ], helper: 'Color theme for HTML emails (invites, password resets).', configKey: 'smtp.email_theme'
            }
        ]
    },
    {
        section: 'notifications',
        title: 'Administrator Notifications',
        description: 'Server-level alerts sent to your operations team. Each condition is emailed once and stays latched until it is resolved. Tenants configure their own admin recipients under Tenants > Notifications.',
        fields: [
            { key: 'enabled', label: 'Enable Admin Notifications', type: 'checkbox', helper: 'Requires SMTP to be configured above.', configKey: 'notifications.enabled' },
            { key: 'admin_emails', label: 'Server Admin Emails', type: 'text', placeholder: 'ops@yourdomain.com, oncall@yourdomain.com', helper: 'Comma-separated list of server administrators.', configKey: 'notifications.admin_emails' },
            { key: 'notify_on_critical', label: 'Email Critical Events', type: 'checkbox', helper: 'Expired credentials, failed background jobs, and other breakages.', configKey: 'notifications.notify_on_critical' },
            { key: 'notify_on_warning', label: 'Email Warning Events', type: 'checkbox', helper: 'Lower-severity problems that still need attention.', configKey: 'notifications.notify_on_warning' },
            { key: 'daily_summary_enabled', label: 'Send Daily Summary', type: 'checkbox', helper: 'One digest per day covering unresolved fleet alerts.', configKey: 'notifications.daily_summary_enabled' },
            { key: 'daily_summary_time', label: 'Daily Summary Time (HH:MM)', type: 'text', placeholder: '08:00', helper: 'Local time of day the digest is sent.', configKey: 'notifications.daily_summary_time' },
            { key: 'daily_summary_timezone', label: 'Daily Summary Timezone', type: 'text', placeholder: 'Local', helper: 'IANA timezone name (e.g. America/New_York) or Local.', configKey: 'notifications.daily_summary_timezone' }
        ]
    }
];

const serverSettingsVM = {
    data: null,
    original: null,
    lockedKeys: new Set(),
    activeSection: 'server',
    loading: false,
    saving: false,
    dirty: false,
    restartRequired: false,
    statusMessage: '',
    statusTone: 'muted',
    lastError: null,
};

function getRBAC() {
    if (typeof window === 'undefined') {
        return null;
    }
    return window.__pm_rbac || null;
}

function getRolePriorityMap() {
    const rbac = getRBAC();
    return (rbac && rbac.ROLE_PRIORITY) ? rbac.ROLE_PRIORITY : DEFAULT_ROLE_PRIORITY;
}

function normalizeRole(role) {
    const rbac = getRBAC();
    if (rbac && typeof rbac.normalizeRole === 'function') {
        return rbac.normalizeRole(role);
    }
    return (role || '').toString().toLowerCase();
}

function userHasRole(minRole) {
    if (!currentUser) return false;
    const rbac = getRBAC();
    if (rbac && typeof rbac.userHasRequiredRole === 'function') {
        return rbac.userHasRequiredRole(currentUser.role, minRole);
    }
    const priorities = getRolePriorityMap();
    const current = priorities[normalizeRole(currentUser.role)] || 0;
    const required = priorities[normalizeRole(minRole)] || 0;
    return current >= required;
}

function userCan(action) {
    if (!currentUser || !action) {
        return false;
    }
    const rbac = getRBAC();
    if (rbac && typeof rbac.canPerformAction === 'function') {
        return rbac.canPerformAction(currentUser.role, action);
    }
    if (rbac && rbac.ACTION_MIN_ROLE && rbac.ACTION_MIN_ROLE[action]) {
        return userHasRole(rbac.ACTION_MIN_ROLE[action]);
    }
    return false;
}

function debounce(fn, wait = 250) {
    let timeout;
    return (...args) => {
        clearTimeout(timeout);
        timeout = setTimeout(() => fn.apply(null, args), wait);
    };
}

/**
 * Get a human-readable display name for an agent.
 * Fallback chain: name → hostname → agent_id → 'Unknown'
 * @param {Object} agent - Agent object with name, hostname, agent_id fields
 * @param {string} [fallbackId] - Optional fallback if agent_id is missing
 * @returns {string} Display name for the agent
 */
function getAgentDisplayName(agent, fallbackId) {
    if (!agent) return fallbackId || 'Unknown';
    return agent.name || agent.hostname || agent.agent_id || fallbackId || 'Unknown';
}

function buildDynamicTabs() {
    Object.entries(TAB_DEFINITIONS).forEach(([tabId, config]) => {
        const requiredAction = config && config.requiredAction;
        const canShow = requiredAction ? userCan(requiredAction) : userHasRole((config && config.minRole) || 'viewer');
        if (canShow) {
            mountTab(tabId, config);
        }
    });
}

function mountTab(tabId, config) {
    if (mountedTabs.has(tabId)) {
        return;
    }
    createTabButtons(tabId, config.label);
    ensureTabPanel(tabId, config.templateId);
    mountedTabs.add(tabId);
    if (typeof config.onMount === 'function') {
        config.onMount();
    }
}

function createTabButtons(tabId, label) {
    const desktop = document.getElementById('desktop_tabs');
    if (desktop && !desktop.querySelector(`.tab[data-target="${tabId}"]`)) {
        const btn = document.createElement('button');
        btn.className = 'tab';
        btn.dataset.target = tabId;
        btn.textContent = label;
        desktop.appendChild(btn);
        registerTabButton(btn);
    }
    const mobile = document.getElementById('mobile_nav');
    if (mobile && !mobile.querySelector(`.tab[data-target="${tabId}"]`)) {
        const btn = document.createElement('button');
        btn.className = 'tab';
        btn.dataset.target = tabId;
        btn.textContent = label;
        mobile.appendChild(btn);
        registerTabButton(btn);
    }

    // Also add to mobile bottom tabs with appropriate icon
    const iconMap = {
        'settings': '<svg viewBox="0 0 24 24"><path d="M19.14 12.94c.04-.31.06-.63.06-.94 0-.31-.02-.63-.06-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.04.31-.06.63-.06.94s.02.63.06.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg>',
        'alerts': '<svg viewBox="0 0 24 24"><path d="M12 22c1.1 0 2-.9 2-2h-4c0 1.1.89 2 2 2zm6-6v-5c0-3.07-1.64-5.64-4.5-6.32V4c0-.83-.67-1.5-1.5-1.5s-1.5.67-1.5 1.5v.68C7.63 5.36 6 7.92 6 11v5l-2 2v1h16v-1l-2-2z"/></svg>',
        'admin': '<svg viewBox="0 0 24 24"><path d="M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm0 10.99h7c-.53 4.12-3.28 7.79-7 8.94V12H5V6.3l7-3.11v8.8z"/></svg>'
    };

    if (iconMap[tabId]) {
        ensureMobileBottomTab(tabId, label, iconMap[tabId]);
    }
}

function ensureTabPanel(tabId, templateId) {
    if (document.querySelector(`[data-tab="${tabId}"]`)) {
        return;
    }
    const tpl = document.getElementById(templateId);
    if (!tpl || !tpl.content) {
        return;
    }
    const container = document.querySelector('.content-container');
    if (!container) {
        return;
    }
    container.appendChild(tpl.content.cloneNode(true));
}

function applyRBACVisibility() {
    buildDynamicTabs();
    configureRBACActions();
}

function configureRBACActions() {
    const joinBtn = document.getElementById('join_token_btn');
    if (joinBtn) {
        if (userCan('join_tokens.write')) {
            joinBtn.style.display = '';
            initAddAgentUI();
        } else {
            joinBtn.style.display = 'none';
        }
    }
}

function registerTabButton(tab) {
    if (!tab || tab.dataset.tabRegistered === 'true') {
        return;
    }
    tab.dataset.tabRegistered = 'true';
    tab.addEventListener('click', () => {
        switchTab(tab.dataset.target);
        // Close mobile nav drawer when tab is selected
        closeMobileNav();
    });
}

function getTabLabel(targetTab) {
    if (TAB_DEFINITIONS[targetTab]) {
        return TAB_DEFINITIONS[targetTab].label;
    }
    return BASE_TAB_LABELS[targetTab] || targetTab;
}

// ====== Initialization ======
document.addEventListener('DOMContentLoaded', function () {
    window.__pm_shared.log('PrintMaster Server UI loaded');

    // Before initializing the UI, ensure user is authenticated (shared auth util)
    window.__pm_auth.ensureAuth().then(async user => {
        if (!user) {
            // ensureAuthenticated will redirect to login for us
            return;
        }

        currentUser = user;
        applyRBACVisibility();

        // Initialize theme toggle
        initThemeToggle();

        // Initialize tabs (after dynamic tabs injected)
        initTabs();
        initLogSubTabs();

        // Initialize hash-based navigation (enables browser back/forward buttons)
        initHashNavigation();

        // Check config status and show warning if needed
        checkConfigStatus();

        // Load server status first (sets tenancy_enabled flag needed by other components)
        await loadServerStatus();

        // Check if onboarding wizard should be shown (first-run setup)
        await checkOnboardingStatus();

        // Now restore preferred tab (which may trigger loadPendingRegistrations that needs tenancy flag)
        restorePreferredTab();

        // Load initial data
        loadAgents();
        // Also load pending registrations if agents tab is active (in case restorePreferredTab didn't trigger it)
        if (document.querySelector('[data-tab="agents"]:not(.hidden)')) {
            initPendingRegistrationsUI();
            loadPendingRegistrations();
        }
        initMetricsRangeControls();
        initMetricsFilterControls();
        loadMetrics();
        window._metricsInterval = setInterval(() => {
            if (isMetricsTabActive()) {
                loadMetrics(true);
            }
        }, 60000);

        // Set up periodic refresh for server status only
        // Keep the interval ID so we can cancel polling when WebSocket is active
        window._serverStatusInterval = setInterval(loadServerStatus, 30000); // Every 30 seconds

        // Periodically refresh pending registrations when on agents tab
        window._pendingRegsInterval = setInterval(() => {
            if (document.querySelector('[data-tab="agents"]:not(.hidden)')) {
                loadPendingRegistrations();
            }
        }, 30000); // Every 30 seconds

        // Try WebSocket first for low-latency liveness; fallback to SSE if WS not available
        connectWS();
        // Also keep SSE as a fallback
        connectSSE();
        // Update auth-related UI (logout button)
        updateAuthUI();

        // Initialize horizontal scroll indicators for all table wrappers
        initAllTableScrollIndicators();
    }).catch(err => {
        window.__pm_shared.error('Auth initialization failed', err);
    });
});

// Ensure user is authenticated, show login modal if not. Resolves true once authenticated.
// ensureAuthenticated replaced by shared utility window.__pm_auth.ensureAuth()

function showLoginModal() {
    const modal = document.getElementById('login_modal');
    if (!modal) return;
    modal.style.display = 'flex';
    document.getElementById('login_username').focus();

    const submit = document.getElementById('login_submit');
    const cancel = document.getElementById('login_cancel');
    const errEl = document.getElementById('login_error');

    const doSubmit = async () => {
        errEl.style.display = 'none';
        const u = document.getElementById('login_username').value || '';
        const p = document.getElementById('login_password').value || '';
        try {
            const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: u, password: p }) });
            if (!r.ok) {
                const text = await r.text();
                errEl.textContent = text || 'Invalid credentials';
                errEl.style.display = 'block';
                return;
            }
            // success - hide modal and re-init UI
            modal.style.display = 'none';
            window.location.reload();
        } catch (ex) {
            errEl.textContent = ex && ex.message ? ex.message : 'Login failed';
            errEl.style.display = 'block';
        }
    };

    submit.onclick = doSubmit;
    cancel.onclick = () => { modal.style.display = 'none'; };
    document.getElementById('login_password').addEventListener('keypress', function (e) { if (e.key === 'Enter') { doSubmit(); } });
}

// Log out the current user and show login modal
async function logout() {
    try {
        const r = await fetch('/api/v1/auth/logout', { method: 'POST' });
        if (!r.ok) {
            // still attempt to clear UI
            window.location = '/login';
            window.__pm_shared.showToast('Logged out (server responded ' + r.status + ')', 'info');
            document.getElementById('logout_btn').style.display = 'none';
            return;
        }
        document.getElementById('logout_btn').style.display = 'none';
        window.location = '/login';
        window.__pm_shared.showToast('Logged out', 'success');
    } catch (err) {
        window.__pm_shared.error('Logout failed', err);
        window.location = '/login';
    }
}

// Show or hide logout button based on current auth state
function updateAuthUI() {
    const btn = document.getElementById('logout_btn');
    if (!btn) return;
    if (currentUser) {
        btn.style.display = 'inline-block';
        btn.onclick = logout;
    } else {
        btn.style.display = 'none';
    }
}

// ====== WebSocket Connection (UI liveness channel) ======
function connectWS() {
    try {
        const protocol = (location.protocol === 'https:') ? 'wss' : 'ws';
        const wsURL = protocol + '://' + location.host + '/api/ws/ui';
        const socket = new WebSocket(wsURL);

        socket.addEventListener('open', () => {
            window.__pm_shared.log('UI WebSocket connected, disabling /api/version polling');
            if (window._serverStatusInterval) {
                clearInterval(window._serverStatusInterval);
                window._serverStatusInterval = null;
            }
        });

        socket.addEventListener('message', (ev) => {
            try {
                const msg = JSON.parse(ev.data);
                // Handle version message specially
                if (msg.type === 'version') {
                    // Optionally update version badge in UI
                    if (msg.data && msg.data.version) {
                        const verEl = document.getElementById('server_version');
                        if (verEl) verEl.textContent = msg.data.version;
                    }
                }
                // Additional messages may be forwarded to existing handlers in future
            } catch (e) {
                window.__pm_shared.warn('Failed to parse WS message', e);
            }
        });

        socket.addEventListener('close', (e) => {
            window.__pm_shared.warn('UI WebSocket closed, falling back to polling and SSE', e);
            // Restart polling if not already running
            if (!window._serverStatusInterval) {
                window._serverStatusInterval = setInterval(loadServerStatus, 30000);
            }
            // Optionally try to reconnect after a delay
            setTimeout(connectWS, 5000);
        });

        socket.addEventListener('error', (e) => {
            window.__pm_shared.error('UI WebSocket error', e);
            // Let close handler restart fallback
        });
    } catch (e) {
        window.__pm_shared.warn('WebSocket not available, continuing with SSE/polling', e);
    }
}

// ====== SSE Connection ======
let _sseConnected = false;

function updateLiveIndicator(connected) {
    _sseConnected = connected;
    const indicator = document.getElementById('metrics_live_indicator');
    if (indicator) {
        if (connected) {
            indicator.classList.remove('disconnected');
            indicator.innerHTML = '● LIVE';
            indicator.title = 'Live updates active (5s refresh)';
        } else {
            indicator.classList.add('disconnected');
            indicator.innerHTML = '○ OFFLINE';
            indicator.title = 'Live updates disconnected - will reconnect';
        }
    }
}

function connectSSE() {
    const eventSource = new EventSource('/api/events');
    eventSource.onopen = (e) => {
        window.__pm_shared.log('SSE onopen, readyState=', eventSource.readyState);
        updateLiveIndicator(true);
    };

    eventSource.addEventListener('connected', (e) => {
        window.__pm_shared.log('SSE connected:', e.data);
        updateLiveIndicator(true);
    });

    eventSource.addEventListener('agent_registered', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Agent registered (SSE):', data);
            upsertAgentRecord(data);
            // Show joined bubble when registration event received
            try { setAgentJoined(data.agent_id, true); } catch (ex) { }
        } catch (err) {
            window.__pm_shared.warn('Failed to parse agent_registered event, falling back to full reload:', err);
            loadAgents();
        }
    });

    eventSource.addEventListener('agent_connected', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Agent connected (SSE):', data);
            updateAgentConnection(data.agent_id, 'ws');

            // Check if this agent was in "restarting" state (update in progress)
            const updateState = agentsVM.updateState[data.agent_id];
            if (updateState && updateState.status === 'restarting') {
                window.__pm_shared.log('Agent reconnected after update restart:', data.agent_id);
                // Fetch fresh agent data to check new version
                handleAgentReconnectAfterUpdate(data.agent_id, updateState);
            }
        } catch (err) {
            window.__pm_shared.warn('Failed to parse agent_connected event, falling back to full reload:', err);
            loadAgents();
        }
    });

    eventSource.addEventListener('agent_disconnected', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Agent disconnected (SSE):', data);
            updateAgentConnection(data.agent_id, 'none');
        } catch (err) {
            window.__pm_shared.warn('Failed to parse agent_disconnected event, falling back to full reload:', err);
            loadAgents();
        }
    });

    eventSource.addEventListener('agent_heartbeat', (e) => {
        try {
            const data = JSON.parse(e.data);
            // Update agent's status/last seen in-place
            updateAgentHeartbeat(data.agent_id, data.status, data.last_seen || data.timestamp);
        } catch (err) {
            window.__pm_shared.log('Agent heartbeat (raw):', e.data);
        }
    });

    eventSource.addEventListener('device_updated', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Device updated (SSE):', data);
            // SSE is a notification, not an authorized inventory projection.
            // Coalesce uploads; refresh index and invalidate stale hydration.
            if (isDevicesTabActive()) {
                clearTimeout(devicesRefreshTimer);
                devicesRefreshTimer = setTimeout(() => loadDevices(true), 250);
            }
        } catch (err) {
            window.__pm_shared.warn('Failed to parse device_updated event, falling back to full reload:', err);
            if (isDevicesTabActive()) {
                loadDevices(true);
            }
        }
    });

    eventSource.addEventListener('update_progress', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Update progress (SSE):', data);
            handleAgentUpdateProgress(data);
        } catch (err) {
            window.__pm_shared.warn('Failed to parse update_progress event:', err);
        }
    });

    // Release sync progress events
    eventSource.addEventListener('release_sync_progress', (e) => {
        try {
            const data = JSON.parse(e.data);
            window.__pm_shared.log('Release sync progress (SSE):', data);
            handleReleaseSyncProgress(data);
        } catch (err) {
            window.__pm_shared.warn('Failed to parse release_sync_progress event:', err);
        }
    });

    // Live metrics refresh - update dashboard charts in real-time
    eventSource.addEventListener('metrics_snapshot', (e) => {
        try {
            const snapshot = JSON.parse(e.data);
            handleLiveMetricsSnapshot(snapshot);
        } catch (err) {
            window.__pm_shared.warn('Failed to parse metrics_snapshot event:', err);
        }
    });

    eventSource.onerror = (e) => {
        // EventSource provides automatic reconnects, but log useful state
        updateLiveIndicator(false);
        try {
            window.__pm_shared.error('SSE connection error:', e, 'readyState=', eventSource.readyState);
        } catch (ex) {
            window.__pm_shared.error('SSE connection error and failed to read readyState', ex);
        }
        // EventSource will automatically try to reconnect
    };
}

// ====== Config Status Check ======
function checkConfigStatus() {
    // Check if user dismissed this warning
    if (localStorage.getItem('hideConfigWarning') === 'true') {
        return;
    }

    fetch('/api/config/status')
        .then(res => res.json())
        .then(data => {
            if (data.errors && data.errors.length > 0) {
                // Config errors found - show modal with details
                let message = 'The server configuration file(s) failed to load:\n\n';
                data.errors.forEach(err => {
                    message += `• ${err}\n`;
                });
                message += '\nThe server is running with default settings. Please check your config.toml file.';

                window.__pm_shared.showAlert(message, '⚠️ Configuration Error', true, true);
            } else if (data.using_defaults) {
                // No config file found - show informational modal
                let message = 'No configuration file was found in any of these locations:\n\n';
                data.searched_paths.forEach(path => {
                    message += `• ${path}\n`;
                });
                message += '\nThe server is running with default settings.';

                window.__pm_shared.showAlert(message, 'ℹ️ Using Default Configuration', false, true);
            }
        })
        .catch(err => {
            window.__pm_shared.error('Failed to check config status:', err);
        });
}

// Metrics modal (delegate to shared implementation)
function showDeviceMetricsModal(serial, preset) {
    if (!serial) return;
    if (typeof window !== 'undefined' && typeof window.showMetricsModal === 'function') {
        try {
            window.showMetricsModal({ serial, preset });
            return;
        } catch (e) {
            window.__pm_shared.warn('shared.showMetricsModal failed', e);
        }
    }
    // Fallback: minimal alert
    window.__pm_shared.showAlert('Metrics UI not available for ' + serial, 'Metrics', false, false);
}

// Toggle the visibility of the metrics time selector for a given container.
// targetId: optional id of the container that holds the metrics UI (e.g. 'metrics_modal_body' or 'metrics_content').
function toggleMetricsTimeSelector(targetId) {
    try {
        let container = null;
        if (targetId) container = document.getElementById(targetId);
        // Fallbacks: modal body or generic metrics content
        if (!container) container = document.getElementById('metrics_modal_body') || document.getElementById('metrics_content') || document.body;

        const selector = container.querySelector('#metrics_time_selector');
        const btn = container.querySelector('#metrics_toggle_time_btn');
        if (!selector || !btn) return;

        const nowHidden = selector.classList.toggle('hidden');
        btn.textContent = nowHidden ? 'Show time selector' : 'Hide time selector';
        btn.setAttribute('aria-expanded', (!nowHidden).toString());
    } catch (e) {
        window.__pm_shared.warn('toggleMetricsTimeSelector failed', e);
    }
}

// ====== Theme Toggle ======
function initThemeToggle() {
    const toggle = document.getElementById('theme-toggle-checkbox');
    const savedTheme = localStorage.getItem('theme') || 'dark';

    if (savedTheme === 'light') {
        toggle.checked = true;
        document.body.classList.add('light-mode');
    }

    toggle.addEventListener('change', function () {
        if (this.checked) {
            document.body.classList.add('light-mode');
            localStorage.setItem('theme', 'light');
        } else {
            document.body.classList.remove('light-mode');
            localStorage.setItem('theme', 'dark');
        }
    });
}

// ====== Tab Management ======
function initTabs() {
    const allTabs = document.querySelectorAll('.tabbar .tab');
    const hamburger = document.querySelector('.hamburger-icon');
    const mobileNav = document.getElementById('mobile_nav');
    const mobileNavToggle = document.getElementById('mobile_nav_toggle');
    const mobileNavOverlay = document.getElementById('mobile_nav_overlay');

    allTabs.forEach(registerTabButton);

    // Initialize mobile bottom tab bar
    initMobileBottomTabs();

    // Legacy hamburger menu (kept for backwards compatibility)
    if (hamburger) {
        hamburger.addEventListener('click', () => {
            mobileNav.classList.toggle('active');
        });
    }

    // Old floating toggle button for mobile navigation (deprecated)
    if (mobileNavToggle && mobileNav) {
        mobileNavToggle.addEventListener('click', () => {
            const isActive = mobileNav.classList.toggle('active');
            mobileNavToggle.classList.toggle('active', isActive);
            if (mobileNavOverlay) {
                mobileNavOverlay.classList.toggle('active', isActive);
            }
        });
    }

    // Close mobile nav when clicking overlay
    if (mobileNavOverlay) {
        mobileNavOverlay.addEventListener('click', () => {
            closeMobileNav();
        });
    }

    // Close mobile nav on escape key
    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && mobileNav && mobileNav.classList.contains('active')) {
            closeMobileNav();
        }
    });
}

// Initialize the mobile bottom tab bar navigation
function initMobileBottomTabs() {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;

    const tabItems = bottomTabs.querySelectorAll('.mobile-tab-item');
    tabItems.forEach(item => {
        item.addEventListener('click', () => {
            const target = item.dataset.target;
            if (target) {
                switchTab(target);
            }
        });
    });
}

function syncMobileBottomTabsWidth() {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;

    // `window.innerWidth` includes the layout scrollbar on some mobile engines.
    // Use the visible document width so the fixed bar never extends past the
    // screen and hides trailing role-gated tabs such as Admin.
    bottomTabs.style.width = `${document.documentElement.clientWidth}px`;
    bottomTabs.style.right = 'auto';
}

syncMobileBottomTabsWidth();
window.addEventListener('resize', syncMobileBottomTabsWidth, { passive: true });
if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', syncMobileBottomTabsWidth, { passive: true });
}

// Update mobile bottom tabs active state when switching tabs
function updateMobileBottomTabsActiveState(targetTab) {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;

    const tabItems = bottomTabs.querySelectorAll('.mobile-tab-item');
    tabItems.forEach(item => {
        if (item.dataset.target === targetTab) {
            item.classList.add('active');
        } else {
            item.classList.remove('active');
        }
    });
}

// Dynamically add a tab to mobile bottom tabs if needed
function ensureMobileBottomTab(tabId, label, iconSvg) {
    const bottomTabs = document.getElementById('mobile_bottom_tabs');
    if (!bottomTabs) return;

    const inner = bottomTabs.querySelector('.mobile-bottom-tabs-inner');
    if (!inner) return;

    // Check if tab already exists
    if (inner.querySelector(`[data-target="${tabId}"]`)) return;

    // Create new tab item
    const item = document.createElement('button');
    item.className = 'mobile-tab-item';
    item.dataset.target = tabId;
    item.setAttribute('aria-label', label);
    item.innerHTML = iconSvg + `<span>${label}</span>`;

    item.addEventListener('click', () => {
        switchTab(tabId);
    });

    inner.appendChild(item);
}

function closeMobileNav() {
    const mobileNav = document.getElementById('mobile_nav');
    const mobileNavToggle = document.getElementById('mobile_nav_toggle');
    const mobileNavOverlay = document.getElementById('mobile_nav_overlay');

    if (mobileNav) mobileNav.classList.remove('active');
    if (mobileNavToggle) mobileNavToggle.classList.remove('active');
    if (mobileNavOverlay) mobileNavOverlay.classList.remove('active');
}

function initLogSubTabs() {
    if (logSubtabsInitialized) {
        return;
    }
    logSubtabsInitialized = true;

    const subtabButtons = document.querySelectorAll('.log-subtab');
    subtabButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const target = btn.dataset.logview || 'system';
            switchLogView(target);
        });
    });

    // Initialize log view mode toggle (table vs raw)
    initLogViewModeToggle();

    // Initialize logs sidebar toggle
    initLogsSidebarToggle();

    // Log action buttons
    const copyLogsBtn = document.getElementById('copy_logs_btn');
    if (copyLogsBtn) {
        copyLogsBtn.addEventListener('click', copyLogs);
    }

    const downloadLogsBtn = document.getElementById('download_logs_btn');
    if (downloadLogsBtn) {
        downloadLogsBtn.addEventListener('click', downloadLogs);
    }

    const clearLogBtn = document.getElementById('clear_log_btn');
    if (clearLogBtn) {
        clearLogBtn.addEventListener('click', clearLogs);
    }

    // Refresh button
    const refreshBtn = document.getElementById('logs_refresh_btn');
    if (refreshBtn) {
        refreshBtn.addEventListener('click', () => {
            // Reset pagination state and reload from newest
            logsState.offset = 0;
            logsState.entries = [];
            loadLogs();
            window.__pm_shared.showToast('Logs refreshed', 'info');
        });
    }

    // Note: Audit logs are now in Admin > Audit tab, initialized separately
}

function initLogsSidebarToggle() {
    initFilterDrawer('logs_sidebar', 'logs_filters_open', 'logs_sidebar_toggle');
}

function initLogViewModeToggle() {
    const viewButtons = document.querySelectorAll('.log-view-btn[data-log-view-mode]');
    viewButtons.forEach(btn => {
        btn.addEventListener('click', () => {
            const mode = btn.dataset.logViewMode || 'table';
            switchLogViewMode(mode);
        });
    });
    // Apply initial state
    syncLogViewModeUI();

    // Add filter event handlers for system logs (server-side filtering)
    const levelFilter = document.getElementById('log_level_filter');
    if (levelFilter) {
        levelFilter.addEventListener('change', () => {
            // Reset pagination and reload with new filter
            logsState.offset = 0;
            logsState.entries = [];
            loadLogs();
        });
    }
    const searchFilter = document.getElementById('log_search_filter');
    if (searchFilter) {
        searchFilter.addEventListener('input', () => {
            // Debounce search to avoid excessive API calls
            if (logsState.searchDebounce) {
                clearTimeout(logsState.searchDebounce);
            }
            logsState.searchDebounce = setTimeout(() => {
                // Reset pagination and reload with new search
                logsState.offset = 0;
                logsState.entries = [];
                loadLogs();
            }, 300);
        });
    }
}

function switchLogViewMode(mode) {
    if (!VALID_LOG_VIEW_MODES.includes(mode)) {
        mode = 'table';
    }
    activeLogViewMode = mode;
    persistUIState(SERVER_UI_STATE_KEYS.LOG_VIEW_MODE, mode);
    syncLogViewModeUI();
    rerenderCurrentLogs();
}

function syncLogViewModeUI() {
    document.querySelectorAll('.log-view-btn[data-log-view-mode]').forEach(btn => {
        const btnMode = btn.dataset.logViewMode || 'table';
        btn.classList.toggle('active', btnMode === activeLogViewMode);
    });

    const tableContainer = document.getElementById('log_table_container');
    const rawContainer = document.getElementById('log');

    if (activeLogViewMode === 'table') {
        if (tableContainer) tableContainer.classList.remove('hidden');
        if (rawContainer) rawContainer.classList.add('hidden');
    } else {
        if (tableContainer) tableContainer.classList.add('hidden');
        if (rawContainer) rawContainer.classList.remove('hidden');
    }
}

function rerenderCurrentLogs() {
    if (activeLogViewMode === 'table') {
        renderLogsTable(currentLogLines);
    } else {
        renderLogsRaw(currentLogLines);
    }
}

function switchLogView(view) {
    // Note: Audit logs moved to Admin > Audit tab
    // Logs tab now only shows system logs
    activeLogView = 'system';
    persistUIState(SERVER_UI_STATE_KEYS.LOG_VIEW, 'system');

    document.querySelectorAll('.log-subtab').forEach(btn => {
        const target = btn.dataset.logview || 'system';
        if (target === 'system') {
            btn.classList.add('active');
        } else {
            btn.classList.remove('active');
        }
    });

    document.querySelectorAll('[data-logview-panel]').forEach(panel => {
        const target = panel.dataset.logviewPanel || 'system';
        if (target === 'system') {
            panel.classList.remove('hidden');
        } else {
            panel.classList.add('hidden');
        }
    });

    loadLogs();
}

// ============================================
// Admin Tab Functions (consolidated admin UI)
// ============================================

function initAdminTab() {
    initAdminSubTabs();
    applyAdminSubtabVisibility();
    // Pick a valid starting view for the user
    const validView = getValidAdminViewForUser(activeAdminView);
    switchAdminView(validView, true);
}

/**
 * Get a valid admin view for the current user.
 * If the requested view is not accessible, return the first accessible one.
 */
function getValidAdminViewForUser(preferredView) {
    const accessibleViews = getAccessibleAdminViews();
    if (accessibleViews.includes(preferredView)) {
        return preferredView;
    }
    return accessibleViews[0] || 'fleet';
}

/**
 * Get list of admin sub-views the current user can access.
 * - Global admins: All views
 * - Tenant-scoped users (any role): Only fleet and alertsconfig for their tenants
 * - Global operators: Same as tenant-scoped (fleet/alertsconfig) since they can't manage users/tenants anyway
 */
function getAccessibleAdminViews() {
    if (isGlobalAdmin()) {
        return VALID_ADMIN_VIEWS;
    }
    // Non-admin users (including tenant-scoped) can only access fleet and alertsconfig
    // Even global operators can't manage users, tenants, access, server, or audit
    return ['fleet', 'alertsconfig'];
}

/**
 * Apply visibility rules to admin subtabs based on user's tenant scope.
 */
function applyAdminSubtabVisibility() {
    const accessibleViews = getAccessibleAdminViews();
    document.querySelectorAll('.admin-subtab').forEach(btn => {
        const target = btn.dataset.adminview || 'users';
        const canAccess = accessibleViews.includes(target);
        btn.style.display = canAccess ? '' : 'none';
    });
}

function initAdminSubTabs() {
    if (adminSubtabsInitialized) {
        return;
    }
    adminSubtabsInitialized = true;

    // Main admin sub-tabs
    document.querySelectorAll('.admin-subtab').forEach(btn => {
        btn.addEventListener('click', () => {
            const target = btn.dataset.adminview || 'users';
            switchAdminView(target);
        });
    });
}

function switchAdminView(view, force = false) {
    const normalized = VALID_ADMIN_VIEWS.includes(view) ? view : 'users';
    const previous = activeAdminView;
    activeAdminView = normalized;
    persistUIState(SERVER_UI_STATE_KEYS.ADMIN_VIEW, normalized);

    // Update sub-tab button states
    document.querySelectorAll('.admin-subtab').forEach(btn => {
        const target = btn.dataset.adminview || 'users';
        btn.classList.toggle('active', target === normalized);
    });

    // Show/hide panels
    document.querySelectorAll('[data-adminview-panel]').forEach(panel => {
        const target = panel.dataset.adminviewPanel || 'users';
        panel.classList.toggle('hidden', target !== normalized);
    });

    if (force || previous !== normalized) {
        ensureAdminViewReady(normalized);
    }
}

function ensureAdminViewReady(view) {
    switch (view) {
        case 'users':
            initUsersUI();
            loadUsers();
            break;
        case 'access':
            initSSOAdmin();
            refreshSSOProviders();
            loadSessions();
            break;
        case 'tenants':
            initTenantsUI();
            loadTenants();
            break;
        case 'fleet':
            initSettingsUI();
            loadReleaseArtifacts();
            break;
        case 'server':
            loadServerSettings();
            loadSelfUpdateRuns();
            break;
        case 'alertsconfig':
            initAlertRulesUI();
            loadAlertRules();
            break;
        case 'audit':
            initAuditFilterControls();
            loadAuditLogs();
            break;
    }
}

async function openFleetSettingsForTenant(tenantId) {
    if (!tenantId) {
        window.__pm_shared.showToast('No tenant selected', 'error');
        return;
    }
    switchTab('admin');
    switchAdminView('fleet');
    await initSettingsUI();
    settingsUIState.scope = 'tenant';
    settingsUIState.selectedTenantId = tenantId;
    await loadTenantSnapshot(tenantId);
    renderSettingsUI();
}

async function openFleetSettingsForAgent(agentId) {
    if (!agentId) {
        window.__pm_shared.showToast('No agent selected', 'error');
        return;
    }
    switchTab('admin');
    switchAdminView('fleet');
    await initSettingsUI();
    await loadAgentDirectoryForSettings();
    settingsUIState.scope = 'agent';
    settingsUIState.selectedAgentId = agentId;
    await loadAgentSnapshot(agentId);
    renderSettingsUI();
}

// Sessions management for Access sub-tab
async function loadSessions() {
    const container = document.getElementById('sessions_list');
    if (!container) return;
    container.innerHTML = '<div class="muted-text">Loading sessions…</div>';
    try {
        const sessions = await fetchJSON('/api/v1/sessions');
        renderSessions(sessions || []);
    } catch (err) {
        container.innerHTML = `<div style="color:var(--danger);">Failed to load sessions: ${escapeHtml(err.message || err)}</div>`;
    }
}

function renderSessions(sessions) {
    const container = document.getElementById('sessions_list');
    if (!container) return;
    if (!Array.isArray(sessions) || sessions.length === 0) {
        container.innerHTML = '<div class="muted-text">No active sessions.</div>';
        return;
    }
    const rows = sessions.map(s => {
        const created = s.created_at ? new Date(s.created_at).toLocaleString() : 'N/A';
        const expires = s.expires_at ? new Date(s.expires_at).toLocaleString() : 'N/A';
        const username = escapeHtml(s.username || `User #${s.user_id}`);
        return `<tr>
            <td>${username}</td>
            <td>${created}</td>
            <td>${expires}</td>
            <td><button class="ghost-btn danger-btn" data-session-hash="${escapeHtml(s.token_hash || '')}" onclick="revokeSession(this)">Revoke</button></td>
        </tr>`;
    }).join('');
    container.innerHTML = `<table class="data-table">
        <thead><tr><th>User</th><th>Created</th><th>Expires</th><th>Action</th></tr></thead>
        <tbody>${rows}</tbody>
    </table>`;
}

async function revokeSession(btn) {
    const hash = btn.dataset.sessionHash;
    if (!hash) return;
    if (!confirm('Revoke this session? The user will be logged out.')) return;
    try {
        await fetch(`/api/v1/sessions/${encodeURIComponent(hash)}`, { method: 'DELETE' });
        window.__pm_shared.showToast('Session revoked', 'success');
        loadSessions();
    } catch (err) {
        window.__pm_shared.showToast('Failed to revoke session', 'error');
    }
}

// Wire up sessions refresh button
document.addEventListener('DOMContentLoaded', () => {
    const refreshBtn = document.getElementById('sessions_refresh_btn');
    if (refreshBtn) {
        refreshBtn.addEventListener('click', loadSessions);
    }
});

// ============================================
// Alerts Tab Functions (operator-visible)
// ============================================

function initAlertsTab() {
    initAlertsSubTabs();
    switchAlertsView(activeAlertsView, true);
}

function initAlertsSubTabs() {
    if (alertsSubtabsInitialized) {
        return;
    }
    alertsSubtabsInitialized = true;

    // Alerts sub-tabs
    document.querySelectorAll('.alerts-subtab').forEach(btn => {
        btn.addEventListener('click', () => {
            const target = btn.dataset.alertsview || 'active';
            switchAlertsView(target);
        });
    });

    // Active alerts refresh
    const refreshBtn = document.getElementById('refresh_alerts_btn');
    if (refreshBtn) {
        refreshBtn.addEventListener('click', loadActiveAlerts);
    }

    // Report generation buttons
    ['fleet', 'usage', 'supply'].forEach(type => {
        const btn = document.getElementById(`generate_${type}_report_btn`);
        if (btn) {
            btn.addEventListener('click', () => generateReport(type));
        }
    });

    // Alert history filters - use applyAlertHistoryFilters for in-memory filtering
    const historyTimeFilter = document.getElementById('alerts_history_time_filter');
    const historyStatusFilter = document.getElementById('alerts_history_status_filter');
    const historyScopeFilter = document.getElementById('alerts_history_scope_filter');
    const historySearchFilter = document.getElementById('alerts_history_search');

    // Time filter triggers full reload (changes API query)
    if (historyTimeFilter) {
        historyTimeFilter.addEventListener('change', loadAlertHistory);
    }
    // Other filters apply in-memory if data loaded, otherwise reload
    [historyStatusFilter, historyScopeFilter].forEach(el => {
        if (el) {
            el.addEventListener('change', () => {
                if (alertHistoryRenderState.allAlerts.length > 0) {
                    applyAlertHistoryFilters();
                } else {
                    loadAlertHistory();
                }
            });
        }
    });
    if (historySearchFilter) {
        let searchTimeout;
        historySearchFilter.addEventListener('input', () => {
            clearTimeout(searchTimeout);
            searchTimeout = setTimeout(() => {
                if (alertHistoryRenderState.allAlerts.length > 0) {
                    applyAlertHistoryFilters();
                }
            }, 150);
        });
    }
}

function switchAlertsView(view, force = false) {
    const normalized = VALID_ALERTS_VIEWS.includes(view) ? view : 'active';
    const previous = activeAlertsView;
    activeAlertsView = normalized;
    persistUIState(SERVER_UI_STATE_KEYS.ALERTS_VIEW, normalized);

    // Update sub-tab button states
    document.querySelectorAll('.alerts-subtab').forEach(btn => {
        const target = btn.dataset.alertsview || 'active';
        btn.classList.toggle('active', target === normalized);
    });

    // Show/hide panels
    document.querySelectorAll('[data-alertsview-panel]').forEach(panel => {
        const target = panel.dataset.alertsviewPanel || 'active';
        panel.classList.toggle('hidden', target !== normalized);
    });

    if (force || previous !== normalized) {
        ensureAlertsViewReady(normalized);
    }
}

function ensureAlertsViewReady(view) {
    switch (view) {
        case 'summary':
            loadAlertSummary();
            break;
        case 'active':
            loadActiveAlerts();
            break;
        case 'history':
            loadAlertHistory();
            break;
        case 'reports':
            loadRecentReports();
            break;
    }
}

async function loadAlertSummary() {
    try {
        // Fetch summary from API
        const resp = await fetch('/api/v1/alerts/summary');
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const summary = await resp.json();

        // Update summary health cards with counts by severity
        // Backend provides: critical_count, warning_count, info_count (integers)
        const criticalCount = summary.critical_count || 0;
        const warningCount = summary.warning_count || 0;
        const infoCount = summary.info_count || 0;
        const totalActive = summary.active_count || 0;
        const healthyCount = totalActive === 0 ? 1 : 0; // Show checkmark if no active alerts

        const el = (id) => document.getElementById(id);
        if (el('summary_healthy_count')) el('summary_healthy_count').textContent = healthyCount > 0 ? '✓' : 0;
        if (el('summary_warning_count')) el('summary_warning_count').textContent = warningCount;
        if (el('summary_critical_count')) el('summary_critical_count').textContent = criticalCount;
        if (el('summary_offline_count')) el('summary_offline_count').textContent = summary.offline_counts?.agents || 0;

        // Update scope bars with data from summary
        // Backend provides: alerts_by_scope map with keys: device, agent, site, tenant
        const byScope = summary.alerts_by_scope || {};
        const deviceAlerts = byScope['device'] || 0;
        const agentAlerts = byScope['agent'] || 0;
        const siteAlerts = byScope['site'] || 0;
        const tenantAlerts = byScope['tenant'] || 0;

        updateScopeBar('devices', deviceAlerts === 0 ? 100 : 0, deviceAlerts > 0 && deviceAlerts < 5 ? 100 : 0, deviceAlerts >= 5 ? 100 : 0, `${deviceAlerts} alerts`);
        updateScopeBar('agents', agentAlerts === 0 ? 100 : 0, agentAlerts > 0 && agentAlerts < 3 ? 100 : 0, agentAlerts >= 3 ? 100 : 0, `${agentAlerts} alerts`);
        updateScopeBar('sites', siteAlerts === 0 ? 100 : 0, siteAlerts > 0 && siteAlerts < 2 ? 100 : 0, siteAlerts >= 2 ? 100 : 0, `${siteAlerts} alerts`);
        updateScopeBar('tenants', tenantAlerts === 0 ? 100 : 0, tenantAlerts > 0 ? 100 : 0, 0, `${tenantAlerts} alerts`);

        // Update breakdown by type
        // Backend provides: alerts_by_type map with keys: supply_low, supply_critical, device_offline, agent_offline, etc.
        const byType = summary.alerts_by_type || {};
        if (el('breakdown_supply')) el('breakdown_supply').textContent = (byType['supply_low'] || 0) + (byType['supply_critical'] || 0) + (byType['toner_low'] || 0) + (byType['toner_critical'] || 0);
        if (el('breakdown_device_offline')) el('breakdown_device_offline').textContent = byType['device_offline'] || 0;
        if (el('breakdown_agent_offline')) el('breakdown_agent_offline').textContent = byType['agent_offline'] || 0;
        if (el('breakdown_site_outage')) el('breakdown_site_outage').textContent = byType['site_outage'] || 0;
        if (el('breakdown_usage')) el('breakdown_usage').textContent = (byType['usage_threshold'] || 0) + (byType['usage_high'] || 0);
        if (el('breakdown_errors')) el('breakdown_errors').textContent = (byType['device_error'] || 0) + (byType['error'] || 0);

        // Update status indicators
        // Backend provides: active_rules, active_channels
        if (el('status_active_rules')) el('status_active_rules').textContent = summary.active_rules || 0;
        if (el('status_channels')) el('status_channels').textContent = summary.active_channels || 0;

        // Show maintenance mode / quiet hours status
        if (summary.has_maintenance && el('maintenance_indicator')) {
            el('maintenance_indicator').style.display = '';
        }
        if (summary.is_quiet_hours && el('quiet_hours_indicator')) {
            el('quiet_hours_indicator').style.display = '';
        }
    } catch (err) {
        console.error('Failed to load alert summary:', err);
        // Keep showing zeros as fallback
    }

    // Wire up "View All" link (always needed)
    const viewAllLink = document.getElementById('view_all_alerts_link');
    if (viewAllLink && !viewAllLink.dataset.bound) {
        viewAllLink.dataset.bound = 'true';
        viewAllLink.addEventListener('click', (e) => {
            e.preventDefault();
            switchAlertsView('active');
        });
    }

    // Load recent alerts preview
    await loadRecentAlertsPreview();
}

async function loadRecentAlertsPreview() {
    const recentContainer = document.getElementById('recent_alerts_summary');
    if (!recentContainer) return;

    try {
        const resp = await fetch('/api/v1/alerts?limit=5');
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        const alerts = data.alerts || [];

        if (alerts.length === 0) {
            recentContainer.innerHTML = '<div class="muted-text">No recent alerts</div>';
            return;
        }

        // Render recent alerts as compact list
        recentContainer.innerHTML = alerts.map(alert => `
            <div class="recent-alert-item ${alert.severity}" data-alert-id="${alert.id}">
                <span class="alert-severity-dot ${alert.severity}"></span>
                <span class="alert-title">${escapeHtml(alert.title || 'Untitled')}</span>
                <span class="alert-time">${formatRelativeTime(alert.triggered_at)}</span>
            </div>
        `).join('');
    } catch (err) {
        console.error('Failed to load recent alerts:', err);
        recentContainer.innerHTML = '<div class="muted-text">Failed to load recent alerts</div>';
    }
}

function updateScopeBar(scope, healthy, warning, critical, countText) {
    const bar = document.getElementById(`scope_bar_${scope}`);
    const count = document.getElementById(`scope_count_${scope}`);
    if (bar) {
        bar.style.setProperty('--healthy', `${healthy}%`);
        bar.style.setProperty('--warning', `${warning}%`);
        bar.style.setProperty('--critical', `${critical}%`);
    }
    if (count) {
        count.textContent = countText;
    }
}

// Infinite scroll state for active alerts
const alertsInfiniteScroll = {
    offset: 0,
    limit: 50,
    hasMore: true,
    loading: false,
    observer: null,
    sentinelId: 'alerts_load_more_sentinel'
};

async function loadActiveAlerts(append = false) {
    const container = document.getElementById('active_alerts_list');
    if (!container) return;

    // Prevent concurrent loads
    if (alertsInfiniteScroll.loading) return;

    // Reset state on fresh load
    if (!append) {
        alertsInfiniteScroll.offset = 0;
        alertsInfiniteScroll.hasMore = true;
    }

    // Don't fetch if no more data
    if (append && !alertsInfiniteScroll.hasMore) return;

    alertsInfiniteScroll.loading = true;
    const el = (id) => document.getElementById(id);

    try {
        const params = new URLSearchParams({
            status: 'active',
            limit: alertsInfiniteScroll.limit.toString(),
            offset: alertsInfiniteScroll.offset.toString()
        });

        const resp = await fetch(`/api/v1/alerts?${params}`);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        const alerts = data.alerts || [];
        const totalCount = data.total_count || 0;

        // Update pagination state
        alertsInfiniteScroll.offset += alerts.length;
        alertsInfiniteScroll.hasMore = data.has_more === true;

        // Update summary counts from total (only on initial load)
        if (!append) {
            // Fetch all for counts (uses a separate lightweight call or cached total)
            let critical = 0, warning = 0, info = 0, acknowledged = 0, suppressed = 0;
            // For accurate counts, we need all alerts - but we can approximate from the server
            // For now, show counts based on what's loaded + indicate there's more
            alerts.forEach(a => {
                if (a.status === 'acknowledged') acknowledged++;
                else if (a.status === 'suppressed') suppressed++;
                else if (a.severity === 'critical') critical++;
                else if (a.severity === 'warning') warning++;
                else info++;
            });

            const suffix = alertsInfiniteScroll.hasMore ? '+' : '';
            if (el('alerts_critical_count')) el('alerts_critical_count').textContent = critical + suffix;
            if (el('alerts_warning_count')) el('alerts_warning_count').textContent = warning + suffix;
            if (el('alerts_info_count')) el('alerts_info_count').textContent = info + suffix;
            if (el('alerts_acknowledged_count')) el('alerts_acknowledged_count').textContent = acknowledged + suffix;
            if (el('alerts_suppressed_count')) el('alerts_suppressed_count').textContent = suppressed + suffix;
        }

        // Handle empty state
        if (!append && alerts.length === 0) {
            container.innerHTML = `
                <div class="alerts-empty-state">
                    <svg width="48" height="48" viewBox="0 0 16 16" fill="var(--success)">
                        <path d="M16 8A8 8 0 1 1 0 8a8 8 0 0 1 16 0zm-3.97-3.03a.75.75 0 0 0-1.08.022L7.477 9.417 5.384 7.323a.75.75 0 0 0-1.06 1.06L6.97 11.03a.75.75 0 0 0 1.079-.02l3.992-4.99a.75.75 0 0 0-.01-1.05z"/>
                    </svg>
                    <div class="alerts-empty-title">All Clear</div>
                    <div class="alerts-empty-text">No active alerts at this time. Configure alert rules in Admin → Alerts.</div>
                </div>
            `;
            cleanupAlertsInfiniteScroll();
            return;
        }

        // Remove existing sentinel before adding new content
        const existingSentinel = document.getElementById(alertsInfiniteScroll.sentinelId);
        if (existingSentinel) existingSentinel.remove();

        // Render alert cards
        const newContent = alerts.map(alert => renderAlertCard(alert)).join('');

        if (append) {
            container.insertAdjacentHTML('beforeend', newContent);
        } else {
            container.innerHTML = newContent;
        }

        // Add sentinel for infinite scroll if there's more data
        if (alertsInfiniteScroll.hasMore) {
            const sentinel = document.createElement('div');
            sentinel.id = alertsInfiniteScroll.sentinelId;
            sentinel.className = 'alerts-load-sentinel';
            sentinel.innerHTML = '<div class="loading-spinner"></div><span class="muted-text">Loading more alerts...</span>';
            container.appendChild(sentinel);
            setupAlertsInfiniteScroll();
        }

        // Bind action buttons on new elements
        container.querySelectorAll('.alert-action-btn:not([data-bound])').forEach(btn => {
            btn.setAttribute('data-bound', 'true');
            btn.addEventListener('click', (e) => handleAlertAction(e.target.dataset.action, e.target.dataset.alertId));
        });

    } catch (err) {
        console.error('Failed to load active alerts:', err);
        if (!append) {
            container.innerHTML = '<div class="error-text">Failed to load alerts. Please try again.</div>';
        }
    } finally {
        alertsInfiniteScroll.loading = false;
    }
}

function setupAlertsInfiniteScroll() {
    // Clean up existing observer
    if (alertsInfiniteScroll.observer) {
        alertsInfiniteScroll.observer.disconnect();
    }

    const sentinel = document.getElementById(alertsInfiniteScroll.sentinelId);
    if (!sentinel) return;

    // Create IntersectionObserver with rootMargin to trigger before sentinel is visible
    alertsInfiniteScroll.observer = new IntersectionObserver((entries) => {
        entries.forEach(entry => {
            if (entry.isIntersecting && !alertsInfiniteScroll.loading && alertsInfiniteScroll.hasMore) {
                loadActiveAlerts(true); // Append mode
            }
        });
    }, {
        root: null, // viewport
        rootMargin: '200px', // Load 200px before sentinel becomes visible
        threshold: 0
    });

    alertsInfiniteScroll.observer.observe(sentinel);
}

function cleanupAlertsInfiniteScroll() {
    if (alertsInfiniteScroll.observer) {
        alertsInfiniteScroll.observer.disconnect();
        alertsInfiniteScroll.observer = null;
    }
}

function renderAlertCard(alert) {
    const severityClass = alert.severity || 'info';
    const statusBadge = alert.status === 'acknowledged' ? '<span class="badge badge-warning">Acknowledged</span>' :
        alert.status === 'suppressed' ? '<span class="badge badge-muted">Suppressed</span>' : '';
    const timeAgo = formatRelativeTime(alert.triggered_at);
    const scope = alert.scope || 'device';

    let scopeIcon = '';
    switch (scope) {
        case 'device': scopeIcon = '🖨️'; break;
        case 'agent': scopeIcon = '📡'; break;
        case 'site': scopeIcon = '🏢'; break;
        case 'tenant': scopeIcon = '🏛️'; break;
        case 'fleet': scopeIcon = '🌐'; break;
    }

    const details = [];
    if (alert.device_serial) details.push(`Device: ${alert.device_serial}`);
    if (alert.agent_id) details.push(`Agent: ${alert.agent_id.substring(0, 8)}...`);
    if (alert.site_id) details.push(`Site: ${alert.site_id}`);

    return `
        <div class="alert-card alert-${severityClass}" data-alert-id="${alert.id}">
            <div class="alert-card-header">
                <span class="alert-severity-indicator ${severityClass}"></span>
                <span class="alert-scope-icon">${scopeIcon}</span>
                <span class="alert-title">${escapeHtml(alert.title || 'Alert')}</span>
                ${statusBadge}
                <span class="alert-time">${timeAgo}</span>
            </div>
            <div class="alert-card-body">
                <p class="alert-message">${escapeHtml(alert.message || '')}</p>
                ${details.length > 0 ? `<p class="alert-details muted-text">${details.join(' • ')}</p>` : ''}
            </div>
            <div class="alert-card-actions">
                ${alert.status !== 'acknowledged' ? `<button class="btn btn-sm alert-action-btn" data-action="acknowledge" data-alert-id="${alert.id}">Acknowledge</button>` : ''}
                <button class="btn btn-sm btn-success alert-action-btn" data-action="resolve" data-alert-id="${alert.id}">Resolve</button>
            </div>
        </div>
    `;
}

async function handleAlertAction(action, alertId) {
    try {
        const resp = await fetch(`/api/v1/alerts/${alertId}/${action}`, { method: 'POST' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast(`Alert ${action}d successfully`, 'success');
        loadActiveAlerts(); // Refresh the list
    } catch (err) {
        console.error(`Failed to ${action} alert:`, err);
        window.__pm_shared.showToast(`Failed to ${action} alert`, 'error');
    }
}

async function loadAlertHistory() {
    const tbody = document.getElementById('alerts_history_body');
    if (!tbody) return;

    try {
        const resp = await fetch('/api/v1/alerts?status=resolved');
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        const alerts = data.alerts || [];

        // Store all alerts
        alertHistoryRenderState.allAlerts = alerts;

        if (alerts.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" class="alert-history-empty">No alert history available. Alerts will appear here once resolved.</td></tr>';
            cleanupAlertHistoryInfiniteScroll();
            return;
        }

        // Apply filters and render
        applyAlertHistoryFilters();
    } catch (err) {
        console.error('Failed to load alert history:', err);
        tbody.innerHTML = '<tr><td colspan="6" class="alert-history-empty error-text">Failed to load alert history.</td></tr>';
        cleanupAlertHistoryInfiniteScroll();
    }
}

function applyAlertHistoryFilters() {
    const alerts = alertHistoryRenderState.allAlerts;

    // Get filter values
    const statusFilter = document.getElementById('alerts_history_status_filter');
    const scopeFilter = document.getElementById('alerts_history_scope_filter');
    const searchFilter = document.getElementById('alerts_history_search');
    const filterStatus = statusFilter ? statusFilter.value : '';
    const filterScope = scopeFilter ? scopeFilter.value : '';
    const filterSearch = searchFilter ? searchFilter.value.toLowerCase().trim() : '';

    // Apply filters
    const filteredAlerts = alerts.filter(a => {
        if (filterStatus && a.status !== filterStatus) return false;
        if (filterScope && a.scope !== filterScope) return false;
        if (filterSearch) {
            const searchStr = (a.title || '') + ' ' + (a.message || '') + ' ' + (a.target_id || '');
            if (!searchStr.toLowerCase().includes(filterSearch)) return false;
        }
        return true;
    });

    // Store filtered alerts and reset display state
    alertHistoryRenderState.filteredAlerts = filteredAlerts;
    alertHistoryRenderState.displayed = 0;

    // Render the filtered alerts
    renderAlertHistoryTable(filteredAlerts);
}

function renderAlertHistoryRow(a) {
    const triggeredAt = a.triggered_at ? new Date(a.triggered_at) : null;
    const resolvedAt = a.resolved_at ? new Date(a.resolved_at) : null;
    const duration = resolvedAt && triggeredAt ? resolvedAt - triggeredAt : null;

    const timeHtml = triggeredAt
        ? `<span class="ah-time-date">${formatDateShort(triggeredAt)}</span>${formatTimeShort(triggeredAt)}`
        : '<span class="ah-time">—</span>';

    const severityClass = `ah-severity-${(a.severity || 'info').toLowerCase()}`;
    const severityHtml = `<span class="ah-severity ${severityClass}">${escapeHtml((a.severity || 'info').toUpperCase())}</span>`;

    const scopeIcon = getScopeIcon(a.scope);
    const scopeHtml = `<span class="ah-scope">${scopeIcon}${escapeHtml(a.scope || 'device')}</span>`;

    const durationHtml = duration !== null
        ? `<span class="ah-duration">${formatDuration(duration)}</span>`
        : '<span class="ah-duration">—</span>';

    const resolvedHtml = resolvedAt
        ? `<span class="ah-resolved">${formatRelativeTime(resolvedAt)}</span>`
        : '<span class="ah-resolved">—</span>';

    // Build context tags for target info
    const contextTags = [];
    if (a.target_id) {
        contextTags.push(`<span class="ah-context-tag"><span class="tag-key">target</span>=<span class="tag-value">${escapeHtml(a.target_id)}</span></span>`);
    }
    if (a.rule_name) {
        contextTags.push(`<span class="ah-context-tag"><span class="tag-key">rule</span>=<span class="tag-value">${escapeHtml(a.rule_name)}</span></span>`);
    }

    return `<tr>
        <td class="ah-time">${timeHtml}</td>
        <td>${severityHtml}</td>
        <td class="ah-title">
            <div class="ah-title-text">${escapeHtml(a.title || 'Untitled')}</div>
            ${contextTags.length > 0 ? `<div class="ah-context">${contextTags.join('')}</div>` : ''}
        </td>
        <td>${scopeHtml}</td>
        <td>${durationHtml}</td>
        <td>${resolvedHtml}</td>
    </tr>`;
}

function renderAlertHistoryTable(alerts, append = false) {
    const tbody = document.getElementById('alerts_history_body');
    if (!tbody) return;

    if (!Array.isArray(alerts) || alerts.length === 0) {
        const hasFilters = alertHistoryRenderState.allAlerts.length > 0;
        const message = hasFilters
            ? 'No alerts match the current filters'
            : 'No alert history available. Alerts will appear here once resolved.';
        tbody.innerHTML = `<tr><td colspan="6" class="alert-history-empty">${message}</td></tr>`;
        cleanupAlertHistoryInfiniteScroll();
        return;
    }

    // Progressive rendering - only render a page at a time
    if (!append) {
        alertHistoryRenderState.displayed = 0;
        tbody.innerHTML = '';
    }

    const startIdx = alertHistoryRenderState.displayed;
    const endIdx = Math.min(startIdx + alertHistoryRenderState.pageSize, alerts.length);
    const pageAlerts = alerts.slice(startIdx, endIdx);

    // Remove existing sentinel
    const existingSentinel = document.getElementById('alert_history_load_more_sentinel');
    if (existingSentinel) existingSentinel.remove();

    // Render the rows
    const rows = pageAlerts.map(a => renderAlertHistoryRow(a)).join('');
    tbody.insertAdjacentHTML('beforeend', rows);
    alertHistoryRenderState.displayed = endIdx;

    // Add sentinel row if more items available
    if (endIdx < alerts.length) {
        const sentinelRow = document.createElement('tr');
        sentinelRow.id = 'alert_history_load_more_sentinel';
        sentinelRow.className = 'alert-history-load-sentinel';
        sentinelRow.innerHTML = '<td colspan="6" style="text-align:center;padding:16px;"><div class="loading-spinner" style="display:inline-block;margin-right:8px;"></div><span class="muted-text">Loading more alerts...</span></td>';
        tbody.appendChild(sentinelRow);
        setupAlertHistoryInfiniteScroll();
    } else {
        cleanupAlertHistoryInfiniteScroll();
    }
}

// Setup IntersectionObserver for alert history infinite scroll
function setupAlertHistoryInfiniteScroll() {
    cleanupAlertHistoryInfiniteScroll();

    const sentinel = document.getElementById('alert_history_load_more_sentinel');
    if (!sentinel) return;

    alertHistoryRenderState.observer = new IntersectionObserver((entries) => {
        entries.forEach(entry => {
            if (entry.isIntersecting && alertHistoryRenderState.displayed < alertHistoryRenderState.filteredAlerts.length) {
                loadMoreAlertHistory();
            }
        });
    }, {
        root: null,
        rootMargin: '200px',
        threshold: 0
    });

    alertHistoryRenderState.observer.observe(sentinel);
}

// Cleanup the alert history infinite scroll observer
function cleanupAlertHistoryInfiniteScroll() {
    if (alertHistoryRenderState.observer) {
        alertHistoryRenderState.observer.disconnect();
        alertHistoryRenderState.observer = null;
    }
}

// Load more alert history for infinite scroll
function loadMoreAlertHistory() {
    renderAlertHistoryTable(alertHistoryRenderState.filteredAlerts, true);
}

function getScopeIcon(scope) {
    switch (scope) {
        case 'device':
            return '<svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" style="margin-right:4px;vertical-align:-1px;opacity:0.7;"><rect x="3" y="1" width="10" height="11" rx="1"/><rect x="4" y="12" width="8" height="3" rx="0.5"/></svg>';
        case 'agent':
            return '<svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" style="margin-right:4px;vertical-align:-1px;opacity:0.7;"><path d="M6 1v6h4V1H6zM5 0h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V1a1 1 0 0 1 1-1z"/><path d="M8 9v6H2V9h6zM1 8h8a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H1a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1z"/></svg>';
        case 'site':
            return '<svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" style="margin-right:4px;vertical-align:-1px;opacity:0.7;"><path d="M8.354 1.146a.5.5 0 0 0-.708 0l-6 6A.5.5 0 0 0 1.5 7.5v7a.5.5 0 0 0 .5.5h4.5a.5.5 0 0 0 .5-.5v-4h2v4a.5.5 0 0 0 .5.5H14a.5.5 0 0 0 .5-.5v-7a.5.5 0 0 0-.146-.354L13 5.793V2.5a.5.5 0 0 0-.5-.5h-1a.5.5 0 0 0-.5.5v1.293L8.354 1.146z"/></svg>';
        case 'tenant':
            return '<svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" style="margin-right:4px;vertical-align:-1px;opacity:0.7;"><path d="M4 16s-1 0-1-1 1-4 5-4 5 3 5 4-1 1-1 1H4zm4-5.95a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z"/></svg>';
        case 'fleet':
            return '<svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" style="margin-right:4px;vertical-align:-1px;opacity:0.7;"><path d="M0 2a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2V2zm4.5 0a.5.5 0 0 0 0 1h7a.5.5 0 0 0 0-1h-7zM4 5.5a.5.5 0 0 0 .5.5h7a.5.5 0 0 0 0-1h-7a.5.5 0 0 0-.5.5zM4.5 8a.5.5 0 0 0 0 1h7a.5.5 0 0 0 0-1h-7z"/></svg>';
        default:
            return '';
    }
}

// formatDuration(ms) is now formatDurationMs in utils/formatters.js
const formatDuration = formatDurationMs;

async function loadRecentReports() {
    const container = document.getElementById('recent_reports_list');
    if (!container) return;

    // Map report type codes to display names
    const typeDisplayNames = {
        'device_inventory': 'Device Inventory',
        'agent_inventory': 'Agent Inventory',
        'site_inventory': 'Site Inventory',
        'usage_summary': 'Usage Audit',
        'usage_by_device': 'Usage By Device',
        'usage_by_agent': 'Usage By Agent',
        'usage_by_site': 'Usage By Site',
        'usage_trends': 'Usage Trends',
        'supplies_status': 'Supplies Status',
        'supplies_low': 'Supplies Low',
        'supplies_critical': 'Supplies Critical',
        'alert_summary': 'Alert Summary',
        'alert_history': 'Alert History',
        'agent_status': 'Agent Status',
        'agent_health': 'Agent Health',
        'fleet_health': 'Fleet Health',
        'health_summary': 'Health Summary',
        'top_printers': 'Top Printers',
        'offline_devices': 'Offline Devices',
        'error_devices': 'Error Devices',
        'cost_analysis': 'Cost Analysis',
        'custom': 'Custom'
    };

    try {
        const resp = await fetch('/api/v1/report-runs?limit=10');
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        const runs = data.runs || [];

        if (runs.length === 0) {
            container.innerHTML = '<div class="muted-text">No reports generated yet. Use the buttons above to generate a report.</div>';
        } else {
            container.innerHTML = `
                <table class="data-table">
                    <thead>
                        <tr>
                            <th>Report</th>
                            <th>Type</th>
                            <th>Status</th>
                            <th>Generated</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        ${runs.map(run => {
                const typeCode = run.report_type || 'unknown';
                const typeDisplay = typeDisplayNames[typeCode] || typeCode;
                return `
                            <tr>
                                <td>${escapeHtml(run.report_name || 'Report #' + run.report_id)}</td>
                                <td><span class="badge">${typeDisplay}</span></td>
                                <td><span class="badge badge-${run.status === 'completed' ? 'success' : run.status === 'failed' ? 'danger' : 'warning'}">${run.status}</span></td>
                                <td>${new Date(run.started_at).toLocaleString()}</td>
                                <td>
                                    ${run.status === 'completed' ? `
                                        <button class="btn btn-sm" onclick="downloadReportRun(${run.id}, 'csv')">CSV</button>
                                        <button class="btn btn-sm" onclick="downloadReportRun(${run.id}, 'json')">JSON</button>
                                    ` : ''}
                                </td>
                            </tr>
                        `}).join('')}
                    </tbody>
                </table>
            `;
        }
    } catch (err) {
        console.error('Failed to load recent reports:', err);
        container.innerHTML = '<div class="error-text">Failed to load recent reports.</div>';
    }
}

async function downloadReportRun(runId, format) {
    try {
        const resp = await fetch(`/api/v1/report-runs/${runId}`);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const run = await resp.json();

        // Check for result_data (JSON field name from API)
        if (!run.result_data) {
            window.__pm_shared.showToast('Report data not available', 'error');
            return;
        }

        function csvEscape(value) {
            const str = String(value ?? '');
            // Quote if it contains commas, quotes, or newlines
            if (/[\r\n,"]/.test(str)) {
                return `"${str.replace(/"/g, '""')}"`;
            }
            return str;
        }

        function csvFormatValue(val) {
            if (val === null || val === undefined) return '';
            if (typeof val === 'string') return val;
            if (typeof val === 'number' || typeof val === 'boolean') return String(val);
            // Arrays/objects -> JSON to avoid "[object Object]"
            try {
                return JSON.stringify(val);
            } catch (_) {
                return String(val);
            }
        }

        function resultToCSV(result) {
            // Expected shape (from server formatter JSON):
            // { columns, rows, summary, metadata, row_count }
            if (result && Array.isArray(result.rows) && Array.isArray(result.columns) && result.columns.length > 0) {
                // Expand toner_levels into toner_* columns (replace the blob column)
                let columns = [...result.columns];
                if (columns.includes('toner_levels')) {
                    const keySet = new Set();
                    for (const row of result.rows) {
                        const m = row?.toner_levels;
                        if (m && typeof m === 'object' && !Array.isArray(m)) {
                            for (const k of Object.keys(m)) {
                                if (k) keySet.add(k);
                            }
                        }
                    }
                    const keys = Array.from(keySet).sort();
                    const expanded = keys.map(k => `toner_${k}`);
                    columns = columns.flatMap(c => c === 'toner_levels' ? expanded : [c]);
                }

                const header = columns.map(csvEscape).join(',');
                const rows = result.rows.map(row => {
                    return columns.map(col => {
                        if (col.startsWith('toner_')) {
                            const k = col.slice('toner_'.length);
                            return csvEscape(csvFormatValue(row?.toner_levels?.[k]));
                        }
                        return csvEscape(csvFormatValue(row?.[col]));
                    }).join(',');
                }).join('\n');
                return header + (rows ? `\n${rows}` : '');
            }

            // Summary-only reports: output as single-row CSV
            const summary = (result && (result.summary || result.data)) || null;
            if (summary && typeof summary === 'object') {
                const keys = Object.keys(summary).sort();
                const header = keys.map(csvEscape).join(',');
                const values = keys.map(k => csvEscape(csvFormatValue(summary[k]))).join(',');
                return header + `\n${values}`;
            }

            // Fallback
            return '';
        }

        let content, filename, mimeType;
        if (format === 'csv') {
            // If the run itself was generated as CSV, don't try to parse/convert.
            if ((run.format || '').toLowerCase() === 'csv') {
                content = run.result_data;
            } else {
                // Convert JSON-formatted run.result_data to CSV
                const result = typeof run.result_data === 'string' ? JSON.parse(run.result_data) : run.result_data;
                content = resultToCSV(result);
                if (!content) {
                    // As a last resort, include JSON so the user doesn't get an empty file
                    content = JSON.stringify(result, null, 2);
                }
            }
            filename = `report-${runId}.csv`;
            mimeType = 'text/csv';
        } else {
            // JSON download: if the run was generated as JSON already, use it; otherwise warn and return raw.
            if ((run.format || '').toLowerCase() === 'json') {
                content = typeof run.result_data === 'string' ? run.result_data : JSON.stringify(run.result_data, null, 2);
            } else {
                content = typeof run.result_data === 'string' ? run.result_data : JSON.stringify(run.result_data, null, 2);
            }
            filename = `report-${runId}.json`;
            mimeType = 'application/json';
        }

        const blob = new Blob([content], { type: mimeType });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);

        window.__pm_shared.showToast(`Downloaded ${filename}`, 'success');
    } catch (err) {
        console.error('Failed to download report:', err);
        window.__pm_shared.showToast('Failed to download report', 'error');
    }
}

async function generateReport(type) {
    // Map UI type to API report type (must use underscore format to match backend constants)
    const typeMap = {
        'fleet': 'device_inventory',
        'usage': 'usage_summary',
        'supply': 'supplies_status',
        'alert': 'alert_summary'
    };
    const reportType = typeMap[type] || type;

    // Optional time range selector for usage audit
    let timeRangeType;
    let timeRangeDays;
    if (type === 'usage') {
        const rangeEl = document.getElementById('usage_report_range');
        const selected = rangeEl?.value || 'last_30d';
        if (selected === 'custom_365d') {
            timeRangeType = 'custom';
            timeRangeDays = 365;
        } else {
            timeRangeType = selected;
        }
    }

    window.__pm_shared.showToast(`Generating ${type} report...`, 'info');

    try {
        // First create a report definition
        const createResp = await fetch('/api/v1/reports', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                name: `${type.charAt(0).toUpperCase() + type.slice(1)} Report - ${new Date().toLocaleDateString()}`,
                type: reportType,
                format: 'json',
                ...(timeRangeType ? { time_range_type: timeRangeType } : {}),
                ...(timeRangeDays ? { time_range_days: timeRangeDays } : {})
            })
        });

        if (!createResp.ok) throw new Error(`Failed to create report: HTTP ${createResp.status}`);
        const report = await createResp.json();

        // Then run it immediately
        const runResp = await fetch(`/api/v1/reports/${report.id}/run`, {
            method: 'POST'
        });

        if (!runResp.ok) throw new Error(`Failed to run report: HTTP ${runResp.status}`);
        const run = await runResp.json();

        window.__pm_shared.showToast('Report generated successfully!', 'success');

        // Show download modal
        showReportDownloadModal(run);

        // Refresh the recent reports list
        loadRecentReports();
    } catch (err) {
        console.error('Failed to generate report:', err);
        window.__pm_shared.showToast('Failed to generate report: ' + err.message, 'error');
    }
}

function showReportDownloadModal(run) {
    const modal = document.getElementById('report_download_modal');
    if (!modal) return;

    const info = document.getElementById('report_download_info');
    if (info) {
        info.innerHTML = `
            <p style="margin:0 0 8px;color:var(--text);">Your report has been generated.</p>
            <p style="margin:0;color:var(--muted);font-size:13px;">
                Type: ${run.report_type || 'Report'} • 
                Rows: ${run.row_count || 0} • 
                Generated: ${new Date(run.completed_at || run.started_at).toLocaleString()}
            </p>
        `;
    }

    // Wire download buttons
    const csvBtn = document.getElementById('report_download_csv');
    const jsonBtn = document.getElementById('report_download_json');

    if (csvBtn) {
        csvBtn.onclick = () => {
            downloadReportRun(run.id, 'csv');
            modal.style.display = 'none';
        };
    }
    if (jsonBtn) {
        jsonBtn.onclick = () => {
            downloadReportRun(run.id, 'json');
            modal.style.display = 'none';
        };
    }

    // Wire close buttons
    const closeBtn = document.getElementById('report_download_close');
    const closeX = document.getElementById('report_download_close_x');
    const closeModal = () => modal.style.display = 'none';
    if (closeBtn) closeBtn.onclick = closeModal;
    if (closeX) closeX.onclick = closeModal;

    modal.style.display = 'flex';
}

// ============================================
// Alert Rules Config Functions (admin-only)
// ============================================

let alertRulesUIInitialized = false;
let cachedNotificationChannels = [];

/**
 * Toggle collapsible alerts section
 */
function toggleAlertsSection(sectionId) {
    const section = document.getElementById(sectionId);
    if (!section) return;
    section.classList.toggle('collapsed');

    // Persist state to localStorage
    const collapsedSections = getCollapsedAlertsSections();
    collapsedSections[sectionId] = section.classList.contains('collapsed');
    try {
        localStorage.setItem('alertsSectionsCollapsed', JSON.stringify(collapsedSections));
    } catch (err) {
        // Preserve interactive collapse behavior when storage is unavailable.
    }
}

function getCollapsedAlertsSections() {
    try {
        const stored = JSON.parse(localStorage.getItem('alertsSectionsCollapsed') || '{}');
        return stored && typeof stored === 'object' && !Array.isArray(stored) ? stored : {};
    } catch (err) {
        return {};
    }
}

/**
 * Restore collapsed state of alerts sections from localStorage
 */
function restoreAlertsSectionState() {
    const collapsedSections = getCollapsedAlertsSections();
    const collapsedByDefault = new Set([
        'notification_channels_section',
        'escalation_policies_section',
        'maintenance_windows_section',
        'quiet_hours_section',
        'flapping_section',
        'grouping_section',
        'dependencies_section',
        'report_scheduling_section',
    ]);

    document.querySelectorAll('.alerts-section[id]').forEach(section => {
        const isCollapsed = Object.prototype.hasOwnProperty.call(collapsedSections, section.id)
            ? Boolean(collapsedSections[section.id])
            : collapsedByDefault.has(section.id);
        section.classList.toggle('collapsed', isCollapsed);
    });
}

/**
 * Update the quick stats in the alerts config header
 */
function updateAlertsQuickStats(stats) {
    const rulesCount = document.getElementById('stats_rules_count');
    const channelsCount = document.getElementById('stats_channels_count');
    const policiesCount = document.getElementById('stats_policies_count');
    const activeAlerts = document.getElementById('stats_active_alerts');

    if (rulesCount) rulesCount.textContent = stats.rules || 0;
    if (channelsCount) channelsCount.textContent = stats.channels || 0;
    if (policiesCount) policiesCount.textContent = stats.policies || 0;
    if (activeAlerts) activeAlerts.textContent = stats.activeAlerts || 0;

    // Update section badges
    const rulesBadge = document.getElementById('rules_badge');
    const channelsBadge = document.getElementById('channels_badge');
    const escalationBadge = document.getElementById('escalation_badge');
    const schedulesBadge = document.getElementById('schedules_badge');

    if (rulesBadge) rulesBadge.textContent = stats.rules || 0;
    if (channelsBadge) channelsBadge.textContent = stats.channels || 0;
    if (escalationBadge) escalationBadge.textContent = stats.policies || 0;
    if (schedulesBadge) schedulesBadge.textContent = stats.schedules || 0;
}

function initAlertRulesUI() {
    if (alertRulesUIInitialized) return;
    alertRulesUIInitialized = true;

    // Restore collapsed section state
    restoreAlertsSectionState();

    // Rule management buttons
    const newRuleBtn = document.getElementById('new_alert_rule_btn');
    if (newRuleBtn) {
        newRuleBtn.addEventListener('click', () => showAlertRuleModal());
    }

    const newChannelBtn = document.getElementById('new_notification_channel_btn');
    if (newChannelBtn) {
        newChannelBtn.addEventListener('click', () => showNotificationChannelModal());
    }

    const newScheduleBtn = document.getElementById('new_scheduled_report_btn');
    if (newScheduleBtn) {
        newScheduleBtn.addEventListener('click', () => showScheduledReportModal());
    }

    // Escalation policy button
    const newEscalationBtn = document.getElementById('new_escalation_policy_btn');
    if (newEscalationBtn) {
        newEscalationBtn.addEventListener('click', () => showEscalationPolicyModal());
    }

    // Maintenance window button
    const newMaintenanceBtn = document.getElementById('new_maintenance_window_btn');
    if (newMaintenanceBtn) {
        newMaintenanceBtn.addEventListener('click', () => showMaintenanceWindowModal());
    }

    // Quiet hours toggle
    const quietHoursToggle = document.getElementById('quiet_hours_enabled');
    const quietHoursConfig = document.getElementById('quiet_hours_times');
    if (quietHoursToggle && quietHoursConfig) {
        quietHoursToggle.addEventListener('change', () => {
            quietHoursConfig.style.display = quietHoursToggle.checked ? 'block' : 'none';
        });
    }

    // Report generation buttons
    ['fleet', 'usage', 'supply', 'alert'].forEach(type => {
        const btn = document.getElementById(`generate_${type}_report_btn`);
        if (btn) {
            btn.addEventListener('click', () => generateReport(type));
        }
    });

    // Initialize all modal event handlers
    initAlertRuleModal();
    initNotificationChannelModal();
    initEscalationPolicyModal();
    initMaintenanceWindowModal();
    initScheduledReportModal();
}

async function loadAlertRules() {
    const rulesContainer = document.getElementById('alert_rules_list');
    const channelsContainer = document.getElementById('notification_channels_list');
    const schedulesContainer = document.getElementById('scheduled_reports_list');
    const escalationContainer = document.getElementById('escalation_policies_list');
    const maintenanceContainer = document.getElementById('maintenance_windows_list');

    // Stats tracking
    const stats = { rules: 0, channels: 0, policies: 0, schedules: 0, activeAlerts: 0 };

    // Load alert rules
    if (rulesContainer) {
        try {
            const resp = await fetch('/api/v1/alert-rules');
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const rules = data.rules || [];
            stats.rules = rules.length;

            if (rules.length === 0) {
                rulesContainer.innerHTML = `
                    <div class="config-empty-state">
                        <div class="config-empty-state-icon">
                            <svg width="28" height="28" viewBox="0 0 16 16" fill="currentColor"><path d="M8 16A8 8 0 1 0 8 0a8 8 0 0 0 0 16zm.93-9.412-1 4.705c-.07.34.029.533.304.533.194 0 .487-.07.686-.246l-.088.416c-.287.346-.92.598-1.465.598-.703 0-1.002-.422-.808-1.319l.738-3.468c.064-.293.006-.399-.287-.47l-.451-.081.082-.381 2.29-.287zM8 5.5a1 1 0 1 1 0-2 1 1 0 0 1 0 2z"/></svg>
                        </div>
                        <div class="config-empty-state-title">No alert rules yet</div>
                        <div class="config-empty-state-text">Create your first alert rule to start monitoring your printer fleet.</div>
                    </div>`;
            } else {
                rulesContainer.innerHTML = rules.map(rule => `
                    <div class="config-item" data-rule-id="${rule.id}">
                        <div class="config-item-header">
                            <div class="config-item-icon">${getRuleTypeIcon(rule.type)}</div>
                            <div class="config-item-info">
                                <div class="config-item-name">
                                    ${escapeHtml(rule.name)}
                                    <span class="badge badge-${rule.severity}">${rule.severity}</span>
                                    <span class="config-item-status ${rule.enabled ? 'enabled' : 'disabled'}">${rule.enabled ? 'Enabled' : 'Disabled'}</span>
                                </div>
                                <div class="config-item-details">
                                    <span>${formatRuleType(rule.type)}</span>
                                    <span class="config-item-details-divider">•</span>
                                    <span>Scope: ${rule.scope || 'All'}</span>
                                    ${rule.description ? `<span class="config-item-details-divider">•</span><span>${escapeHtml(rule.description)}</span>` : ''}
                                </div>
                            </div>
                        </div>
                        <div class="config-item-actions">
                            <button class="btn btn-sm" onclick="editAlertRule(${rule.id})">Edit</button>
                            <button class="btn btn-sm btn-danger" onclick="deleteAlertRule(${rule.id})">Delete</button>
                        </div>
                    </div>
                `).join('');
            }
        } catch (err) {
            console.error('Failed to load alert rules:', err);
            rulesContainer.innerHTML = '<div class="error-text">Failed to load alert rules.</div>';
        }
    }

    // Load notification channels
    if (channelsContainer) {
        try {
            const resp = await fetch('/api/v1/notification-channels');
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const channels = data.channels || [];
            cachedNotificationChannels = channels;
            stats.channels = channels.length;

            if (channels.length === 0) {
                channelsContainer.innerHTML = `
                    <div class="config-empty-state">
                        <div class="config-empty-state-icon">
                            <svg width="28" height="28" viewBox="0 0 16 16" fill="currentColor"><path d="M0 4a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2V4Zm2-1a1 1 0 0 0-1 1v.217l7 4.2 7-4.2V4a1 1 0 0 0-1-1H2Zm13 2.383-4.708 2.825L15 11.105V5.383Zm-.034 6.876-5.64-3.471L8 9.583l-1.326-.795-5.64 3.47A1 1 0 0 0 2 13h12a1 1 0 0 0 .966-.741ZM1 11.105l4.708-2.897L1 5.383v5.722Z"/></svg>
                        </div>
                        <div class="config-empty-state-title">No notification channels</div>
                        <div class="config-empty-state-text">Add a notification channel to receive alerts via email, Slack, Discord, and more.</div>
                    </div>`;
            } else {
                channelsContainer.innerHTML = channels.map(ch => `
                    <div class="config-item" data-channel-id="${ch.id}">
                        <div class="config-item-header">
                            <div class="config-item-icon channel-${ch.type}">${getChannelIcon(ch.type)}</div>
                            <div class="config-item-info">
                                <div class="config-item-name">
                                    ${escapeHtml(ch.name)}
                                    <span class="badge">${ch.type}</span>
                                    <span class="config-item-status ${ch.enabled ? 'enabled' : 'disabled'}">${ch.enabled ? 'Enabled' : 'Disabled'}</span>
                                </div>
                                <div class="config-item-details">${getChannelSummary(ch)}</div>
                            </div>
                        </div>
                        <div class="config-item-actions">
                            <button class="btn btn-sm btn-outline" onclick="testNotificationChannel(${ch.id})" title="Send test notification">Test</button>
                            <button class="btn btn-sm" onclick="editNotificationChannel(${ch.id})">Edit</button>
                            <button class="btn btn-sm btn-danger" onclick="deleteNotificationChannel(${ch.id})">Delete</button>
                        </div>
                    </div>
                `).join('');
            }
        } catch (err) {
            console.error('Failed to load notification channels:', err);
            channelsContainer.innerHTML = '<div class="error-text">Failed to load notification channels.</div>';
        }
    }

    // Load escalation policies
    if (escalationContainer) {
        try {
            const resp = await fetch('/api/v1/escalation-policies');
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const policies = data.policies || [];
            stats.policies = policies.length;

            if (policies.length === 0) {
                escalationContainer.innerHTML = `
                    <div class="config-empty-state">
                        <div class="config-empty-state-icon">
                            <svg width="28" height="28" viewBox="0 0 16 16" fill="currentColor"><path d="M8 15A7 7 0 1 1 8 1a7 7 0 0 1 0 14zm0 1A8 8 0 1 0 8 0a8 8 0 0 0 0 16z"/><path d="M4.285 9.567a.5.5 0 0 1 .683.183A3.498 3.498 0 0 0 8 11.5a3.498 3.498 0 0 0 3.032-1.75.5.5 0 1 1 .866.5A4.498 4.498 0 0 1 8 12.5a4.498 4.498 0 0 1-3.898-2.25.5.5 0 0 1 .183-.683zM7 6.5C7 7.328 6.552 8 6 8s-1-.672-1-1.5S5.448 5 6 5s1 .672 1 1.5zm4 0c0 .828-.448 1.5-1 1.5s-1-.672-1-1.5S9.448 5 10 5s1 .672 1 1.5z"/></svg>
                        </div>
                        <div class="config-empty-state-title">No escalation policies</div>
                        <div class="config-empty-state-text">Create escalation policies to automatically escalate unacknowledged alerts.</div>
                    </div>`;
            } else {
                escalationContainer.innerHTML = policies.map(p => {
                    const stepsSummary = (p.steps || []).length > 0
                        ? `${p.steps.length} step${p.steps.length !== 1 ? 's' : ''}`
                        : 'No steps';
                    return `
                        <div class="config-item" data-policy-id="${p.id}">
                            <div class="config-item-header">
                                <div class="config-item-icon">
                                    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 15A7 7 0 1 1 8 1a7 7 0 0 1 0 14zm0 1A8 8 0 1 0 8 0a8 8 0 0 0 0 16z"/><path d="m8.93 6.588-2.29.287-.082.38.45.083c.294.07.352.176.288.469l-.738 3.468c-.194.897.105 1.319.808 1.319.545 0 1.178-.252 1.465-.598l.088-.416c-.2.176-.492.246-.686.246-.275 0-.375-.193-.304-.533L8.93 6.588zM9 4.5a1 1 0 1 1-2 0 1 1 0 0 1 2 0z"/></svg>
                                </div>
                                <div class="config-item-info">
                                    <div class="config-item-name">
                                        ${escapeHtml(p.name)}
                                        <span class="badge">${stepsSummary}</span>
                                        <span class="config-item-status ${p.enabled ? 'enabled' : 'disabled'}">${p.enabled ? 'Enabled' : 'Disabled'}</span>
                                    </div>
                                    ${p.description ? `<div class="config-item-details">${escapeHtml(p.description)}</div>` : ''}
                                </div>
                            </div>
                            <div class="config-item-actions">
                                <button class="btn btn-sm" onclick="editEscalationPolicy(${p.id})">Edit</button>
                                <button class="btn btn-sm btn-danger" onclick="deleteEscalationPolicy(${p.id})">Delete</button>
                            </div>
                        </div>
                    `;
                }).join('');
            }
        } catch (err) {
            console.error('Failed to load escalation policies:', err);
            escalationContainer.innerHTML = '<div class="error-text">Failed to load escalation policies.</div>';
        }
    }

    // Load maintenance windows  
    if (maintenanceContainer) {
        try {
            const resp = await fetch('/api/v1/maintenance-windows');
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const windows = data.windows || [];

            if (windows.length === 0) {
                maintenanceContainer.innerHTML = `
                    <div class="config-empty-state">
                        <div class="config-empty-state-icon">
                            <svg width="28" height="28" viewBox="0 0 16 16" fill="currentColor"><path d="M5.5 10.5A.5.5 0 0 1 6 10h4a.5.5 0 0 1 0 1H6a.5.5 0 0 1-.5-.5z"/><path d="M3.5 0a.5.5 0 0 1 .5.5V1h8V.5a.5.5 0 0 1 1 0V1h1a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2V3a2 2 0 0 1 2-2h1V.5a.5.5 0 0 1 .5-.5zM2 2a1 1 0 0 0-1 1v1h14V3a1 1 0 0 0-1-1H2zm13 3H1v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V5z"/></svg>
                        </div>
                        <div class="config-empty-state-title">No maintenance windows</div>
                        <div class="config-empty-state-text">Schedule maintenance windows to suppress alerts during planned downtime.</div>
                    </div>`;
            } else {
                maintenanceContainer.innerHTML = windows.map(w => {
                    const startDate = new Date(w.start_time).toLocaleString();
                    const endDate = new Date(w.end_time).toLocaleString();
                    const isActive = new Date() >= new Date(w.start_time) && new Date() <= new Date(w.end_time);
                    return `
                        <div class="config-item ${isActive ? 'active-window' : ''}" data-window-id="${w.id}">
                            <div class="config-item-header">
                                <div class="config-item-icon" style="${isActive ? 'background:rgba(203,75,22,0.12);color:var(--warning);' : ''}">
                                    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M5.5 10.5A.5.5 0 0 1 6 10h4a.5.5 0 0 1 0 1H6a.5.5 0 0 1-.5-.5z"/><path d="M3.5 0a.5.5 0 0 1 .5.5V1h8V.5a.5.5 0 0 1 1 0V1h1a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2V3a2 2 0 0 1 2-2h1V.5a.5.5 0 0 1 .5-.5zM2 2a1 1 0 0 0-1 1v1h14V3a1 1 0 0 0-1-1H2zm13 3H1v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V5z"/></svg>
                                </div>
                                <div class="config-item-info">
                                    <div class="config-item-name">
                                        ${escapeHtml(w.name)}
                                        ${isActive ? '<span class="badge badge-warning">Active</span>' : ''}
                                        ${w.recurring ? '<span class="badge">Recurring</span>' : ''}
                                    </div>
                                    <div class="config-item-details">
                                        <span>${startDate}</span>
                                        <span class="config-item-details-divider">→</span>
                                        <span>${endDate}</span>
                                        ${w.scope ? `<span class="config-item-details-divider">•</span><span>Scope: ${w.scope}</span>` : ''}
                                    </div>
                                </div>
                            </div>
                            <div class="config-item-actions">
                                <button class="btn btn-sm" onclick="editMaintenanceWindow(${w.id})">Edit</button>
                                <button class="btn btn-sm btn-danger" onclick="deleteMaintenanceWindow(${w.id})">Delete</button>
                            </div>
                        </div>
                    `;
                }).join('');

            }
        } catch (err) {
            console.error('Failed to load maintenance windows:', err);
            maintenanceContainer.innerHTML = '<div class="error-text">Failed to load maintenance windows.</div>';
        }
    }

    // Load scheduled reports
    if (schedulesContainer) {
        try {
            const resp = await fetch('/api/v1/report-schedules');
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const schedules = data.schedules || [];
            stats.schedules = schedules.length;

            if (schedules.length === 0) {
                schedulesContainer.innerHTML = `
                    <div class="config-empty-state">
                        <div class="config-empty-state-icon">
                            <svg width="28" height="28" viewBox="0 0 16 16" fill="currentColor"><path d="M4 11a1 1 0 1 1 2 0v1a1 1 0 1 1-2 0v-1zm6-4a1 1 0 1 1 2 0v5a1 1 0 1 1-2 0V7zM7 9a1 1 0 0 1 2 0v3a1 1 0 1 1-2 0V9z"/><path d="M4 1.5H3a2 2 0 0 0-2 2V14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V3.5a2 2 0 0 0-2-2h-1v1h1a1 1 0 0 1 1 1V14a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1h1v-1z"/><path d="M9.5 1a.5.5 0 0 1 .5.5v1a.5.5 0 0 1-.5.5h-3a.5.5 0 0 1-.5-.5v-1a.5.5 0 0 1 .5-.5h3zm-3-1A1.5 1.5 0 0 0 5 1.5v1A1.5 1.5 0 0 0 6.5 4h3A1.5 1.5 0 0 0 11 2.5v-1A1.5 1.5 0 0 0 9.5 0h-3z"/></svg>
                        </div>
                        <div class="config-empty-state-title">No scheduled reports</div>
                        <div class="config-empty-state-text">Create a schedule to automatically generate and send reports.</div>
                    </div>`;
            } else {
                schedulesContainer.innerHTML = schedules.map(s => {
                    const nextRun = s.next_run ? new Date(s.next_run).toLocaleString() : 'Not scheduled';
                    return `
                        <div class="config-item" data-schedule-id="${s.id}">
                            <div class="config-item-header">
                                <div class="config-item-icon">
                                    <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M4 11a1 1 0 1 1 2 0v1a1 1 0 1 1-2 0v-1zm6-4a1 1 0 1 1 2 0v5a1 1 0 1 1-2 0V7zM7 9a1 1 0 0 1 2 0v3a1 1 0 1 1-2 0V9z"/><path d="M4 1.5H3a2 2 0 0 0-2 2V14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V3.5a2 2 0 0 0-2-2h-1v1h1a1 1 0 0 1 1 1V14a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1h1v-1z"/></svg>
                                </div>
                                <div class="config-item-info">
                                    <div class="config-item-name">
                                        ${escapeHtml(s.name)}
                                        <span class="badge">${s.frequency}</span>
                                        <span class="badge">${s.report_type}</span>
                                        <span class="config-item-status ${s.enabled ? 'enabled' : 'disabled'}">${s.enabled ? 'Enabled' : 'Disabled'}</span>
                                    </div>
                                    <div class="config-item-details">
                                        <span>Next run: ${nextRun}</span>
                                        <span class="config-item-details-divider">•</span>
                                        <span>Format: ${s.output_format || 'csv'}</span>
                                    </div>
                                </div>
                            </div>
                            <div class="config-item-actions">
                                <button class="btn btn-sm btn-outline" onclick="runScheduleNow(${s.id})">Run Now</button>
                                <button class="btn btn-sm btn-danger" onclick="deleteReportSchedule(${s.id})">Delete</button>
                            </div>
                        </div>
                    `;
                }).join('');
            }
        } catch (err) {
            console.error('Failed to load scheduled reports:', err);
            schedulesContainer.innerHTML = '<div class="error-text">Failed to load scheduled reports.</div>';
        }
    }

    // Update quick stats after all data loaded
    updateAlertsQuickStats(stats);
}

/**
 * Get icon for alert rule type
 */
function getRuleTypeIcon(type) {
    switch (type) {
        case 'toner_low':
        case 'supply_low':
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M9.5 13a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0zm0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0zm0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0z"/></svg>';
        case 'offline':
        case 'device_offline':
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M10.706 3.294A12.545 12.545 0 0 0 8 3C5.259 3 2.723 3.882.663 5.379a.485.485 0 0 0-.048.736.518.518 0 0 0 .668.05A11.448 11.448 0 0 1 8 4c.63 0 1.249.05 1.852.148l.854-.854zM8 6c-1.905 0-3.68.56-5.166 1.526a.48.48 0 0 0-.063.745.525.525 0 0 0 .652.065 8.448 8.448 0 0 1 3.51-1.27L8 6zm2.596 1.404.785-.785c.63.24 1.227.545 1.785.907a.482.482 0 0 1 .063.745.525.525 0 0 1-.652.065 8.462 8.462 0 0 0-1.98-.932zM8 10l.933-.933a6.455 6.455 0 0 1 2.013.637c.285.145.326.524.1.75l-.015.015a.532.532 0 0 1-.611.09A5.478 5.478 0 0 0 8 10zm4.905-4.905.747-.747c.59.3 1.153.645 1.685 1.03a.485.485 0 0 1 .047.737.518.518 0 0 1-.668.05 11.493 11.493 0 0 0-1.811-1.07zM9.02 11.78c.238.14.236.464.04.66l-.707.706a.5.5 0 0 1-.707 0l-.707-.707c-.195-.195-.197-.518.04-.66A1.99 1.99 0 0 1 8 11.5c.374 0 .723.102 1.021.28zm4.355-9.905a.53.53 0 0 1 .75.75l-10.75 10.75a.53.53 0 0 1-.75-.75l10.75-10.75z"/></svg>';
        case 'error':
        case 'error_count':
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8.982 1.566a1.13 1.13 0 0 0-1.96 0L.165 13.233c-.457.778.091 1.767.98 1.767h13.713c.889 0 1.438-.99.98-1.767L8.982 1.566zM8 5c.535 0 .954.462.9.995l-.35 3.507a.552.552 0 0 1-1.1 0L7.1 5.995A.905.905 0 0 1 8 5zm.002 6a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>';
        case 'page_count':
        case 'usage':
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M4 11a1 1 0 1 1 2 0v1a1 1 0 1 1-2 0v-1zm6-4a1 1 0 1 1 2 0v5a1 1 0 1 1-2 0V7zM7 9a1 1 0 0 1 2 0v3a1 1 0 1 1-2 0V9z"/><path d="M4 1.5H3a2 2 0 0 0-2 2V14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V3.5a2 2 0 0 0-2-2h-1v1h1a1 1 0 0 1 1 1V14a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1h1v-1z"/></svg>';
        case 'agent_offline':
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M5 3a3 3 0 0 1 6 0v5a3 3 0 0 1-6 0V3z"/><path d="M3.5 6.5A.5.5 0 0 1 4 7v1a4 4 0 0 0 8 0V7a.5.5 0 0 1 1 0v1a5 5 0 0 1-4.5 4.975V15h3a.5.5 0 0 1 0 1h-7a.5.5 0 0 1 0-1h3v-2.025A5 5 0 0 1 3 8V7a.5.5 0 0 1 .5-.5z"/></svg>';
        default:
            return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 16a2 2 0 0 0 2-2H6a2 2 0 0 0 2 2zm.995-14.901a1 1 0 1 0-1.99 0A5.002 5.002 0 0 0 3 6c0 1.098-.5 6-2 7h14c-1.5-1-2-5.902-2-7 0-2.42-1.72-4.44-4.005-4.901z"/></svg>';
    }
}

/**
 * Format rule type for display
 */
function formatRuleType(type) {
    const types = {
        'toner_low': 'Toner Low',
        'supply_low': 'Supply Low',
        'offline': 'Device Offline',
        'device_offline': 'Device Offline',
        'agent_offline': 'Agent Offline',
        'error': 'Error Alert',
        'error_count': 'Error Count',
        'page_count': 'Page Count',
        'usage': 'Usage Alert'
    };
    return types[type] || type;
}

function getChannelIcon(type) {
    switch (type) {
        case 'email': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M0 4a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2V4Zm2-1a1 1 0 0 0-1 1v.217l7 4.2 7-4.2V4a1 1 0 0 0-1-1H2Zm13 2.383-4.708 2.825L15 11.105V5.383Zm-.034 6.876-5.64-3.471L8 9.583l-1.326-.795-5.64 3.47A1 1 0 0 0 2 13h12a1 1 0 0 0 .966-.741ZM1 11.105l4.708-2.897L1 5.383v5.722Z"/></svg>';
        case 'webhook': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M6.354 5.5H4a3 3 0 0 0 0 6h3a3 3 0 0 0 2.83-4H9c-.086 0-.17.01-.25.031A2 2 0 0 1 7 9.5H4a2 2 0 1 1 0-4h2.354z"/><path d="M9 5.5a3 3 0 0 0-2.83 4h1.098A2 2 0 0 1 9 7.5h3a2 2 0 1 1 0 4h-2.354l1-1.5H12a3 3 0 1 0 0-6H9z"/></svg>';
        case 'slack': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M3.362 10.11c0 .926-.756 1.681-1.681 1.681S0 11.036 0 10.111C0 9.186.756 8.43 1.68 8.43h1.682v1.68zm.846 0c0-.924.756-1.68 1.681-1.68s1.681.756 1.681 1.68v4.21c0 .924-.756 1.68-1.68 1.68a1.685 1.685 0 0 1-1.682-1.68v-4.21zM5.89 3.362c-.926 0-1.682-.756-1.682-1.681S4.964 0 5.89 0s1.68.756 1.68 1.68v1.682H5.89zm0 .846c.924 0 1.68.756 1.68 1.681S6.814 7.57 5.89 7.57H1.68C.757 7.57 0 6.814 0 5.89c0-.926.756-1.682 1.68-1.682h4.21zm6.749 1.682c0-.926.755-1.682 1.68-1.682.925 0 1.681.756 1.681 1.681s-.756 1.681-1.68 1.681h-1.681V5.89zm-.848 0c0 .924-.755 1.68-1.68 1.68A1.685 1.685 0 0 1 8.43 5.89V1.68C8.43.757 9.186 0 10.11 0c.926 0 1.681.756 1.681 1.68v4.21zm-1.681 6.748c.926 0 1.682.756 1.682 1.681S11.036 16 10.11 16s-1.681-.756-1.681-1.68v-1.682h1.68zm0-.847c-.924 0-1.68-.755-1.68-1.68 0-.925.756-1.681 1.68-1.681h4.21c.924 0 1.68.756 1.68 1.68 0 .926-.756 1.681-1.68 1.681h-4.21z"/></svg>';
        case 'teams': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M5 0a5 5 0 0 0-4.898 6.002A5 5 0 1 0 10 10.179V6H5V0zm4 6h2v4a4 4 0 1 1-8 0V6h2v4a2 2 0 1 0 4 0V6zM5 1a4 4 0 0 1 4 4H5V1z"/></svg>';
        case 'pagerduty': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 15A7 7 0 1 1 8 1a7 7 0 0 1 0 14zm0 1A8 8 0 1 0 8 0a8 8 0 0 0 0 16z"/><path d="M5.255 5.786a.237.237 0 0 0 .241.247h.825c.138 0 .248-.113.266-.25.09-.656.54-1.134 1.342-1.134.686 0 1.314.343 1.314 1.168 0 .635-.374.927-.965 1.371-.673.489-1.206 1.06-1.168 1.987l.003.217a.25.25 0 0 0 .25.246h.811a.25.25 0 0 0 .25-.25v-.105c0-.718.273-.927 1.01-1.486.609-.463 1.244-.977 1.244-2.056 0-1.511-1.276-2.241-2.673-2.241-1.267 0-2.655.59-2.75 2.286zm1.557 5.763c0 .533.425.927 1.01.927.609 0 1.028-.394 1.028-.927 0-.552-.42-.94-1.029-.94-.584 0-1.009.388-1.009.94z"/></svg>';
        case 'discord': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M13.545 2.907a13.227 13.227 0 0 0-3.257-1.011.05.05 0 0 0-.052.025c-.141.25-.297.577-.406.833a12.19 12.19 0 0 0-3.658 0 8.258 8.258 0 0 0-.412-.833.051.051 0 0 0-.052-.025c-1.125.194-2.22.534-3.257 1.011a.041.041 0 0 0-.021.018C.356 6.024-.213 9.047.066 12.032c.001.014.01.028.021.037a13.276 13.276 0 0 0 3.995 2.02.05.05 0 0 0 .056-.019c.308-.42.582-.863.818-1.329a.05.05 0 0 0-.01-.059.051.051 0 0 0-.018-.011 8.875 8.875 0 0 1-1.248-.595.05.05 0 0 1-.02-.066.051.051 0 0 1 .015-.019c.084-.063.168-.129.248-.195a.05.05 0 0 1 .051-.007c2.619 1.196 5.454 1.196 8.041 0a.052.052 0 0 1 .053.007c.08.066.164.132.248.195a.051.051 0 0 1-.004.085 8.254 8.254 0 0 1-1.249.594.05.05 0 0 0-.03.03.052.052 0 0 0 .003.041c.24.465.515.909.817 1.329a.05.05 0 0 0 .056.019 13.235 13.235 0 0 0 4.001-2.02.049.049 0 0 0 .021-.037c.334-3.451-.559-6.449-2.366-9.106a.034.034 0 0 0-.02-.019Zm-8.198 7.307c-.789 0-1.438-.724-1.438-1.612 0-.889.637-1.613 1.438-1.613.807 0 1.45.73 1.438 1.613 0 .888-.637 1.612-1.438 1.612Zm5.316 0c-.788 0-1.438-.724-1.438-1.612 0-.889.637-1.613 1.438-1.613.807 0 1.451.73 1.438 1.613 0 .888-.631 1.612-1.438 1.612Z"/></svg>';
        case 'telegram': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M16 8A8 8 0 1 1 0 8a8 8 0 0 1 16 0zM8.287 5.906c-.778.324-2.334.994-4.666 2.01-.378.15-.577.298-.595.442-.03.243.275.339.69.47l.175.055c.408.133.958.288 1.243.294.26.006.549-.1.868-.32 2.179-1.471 3.304-2.214 3.374-2.23.05-.012.12-.026.166.016.047.041.042.12.037.141-.03.129-1.227 1.241-1.846 1.817-.193.18-.33.307-.358.336a8.154 8.154 0 0 1-.188.186c-.38.366-.664.64.015 1.088.327.216.589.393.85.571.284.194.568.387.936.629.094.06.183.125.27.187.331.236.63.448.997.414.214-.02.435-.22.547-.82.265-1.417.786-4.486.906-5.751a1.426 1.426 0 0 0-.013-.315.337.337 0 0 0-.114-.217.526.526 0 0 0-.31-.093c-.3.005-.763.166-2.984 1.09z"/></svg>';
        case 'pushover': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M11 1a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V2a1 1 0 0 1 1-1h6zM5 0a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V2a2 2 0 0 0-2-2H5z"/><path d="M8 14a1 1 0 1 0 0-2 1 1 0 0 0 0 2z"/></svg>';
        case 'ntfy': return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 16a2 2 0 0 0 2-2H6a2 2 0 0 0 2 2zm.995-14.901a1 1 0 1 0-1.99 0A5.002 5.002 0 0 0 3 6c0 1.098-.5 6-2 7h14c-1.5-1-2-5.902-2-7 0-2.42-1.72-4.44-4.005-4.901z"/></svg>';
        default: return '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 16a2 2 0 0 0 2-2H6a2 2 0 0 0 2 2zm.995-14.901a1 1 0 1 0-1.99 0A5.002 5.002 0 0 0 3 6c0 1.098-.5 6-2 7h14c-1.5-1-2-5.902-2-7 0-2.42-1.72-4.44-4.005-4.901z"/></svg>';
    }
}

/**
 * Test notification channel by sending a test message
 */
async function testNotificationChannel(id) {
    const btn = event.target;
    const originalText = btn.textContent;
    btn.textContent = 'Sending...';
    btn.disabled = true;

    try {
        const resp = await fetch(`/api/v1/notification-channels/${id}/test`, { method: 'POST' });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
        }
        window.__pm_shared.showToast('Test notification sent successfully', 'success');
    } catch (err) {
        console.error('Failed to test notification channel:', err);
        window.__pm_shared.showToast(`Test failed: ${err.message}`, 'error');
    } finally {
        btn.textContent = originalText;
        btn.disabled = false;
    }
}

async function deleteAlertRule(id) {
    if (!await window.__pm_shared.showConfirm('Delete this alert rule?')) return;
    try {
        const resp = await fetch(`/api/v1/alert-rules/${id}`, { method: 'DELETE' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Alert rule deleted', 'success');
        loadAlertRules();
    } catch (err) {
        console.error('Failed to delete alert rule:', err);
        window.__pm_shared.showToast('Failed to delete alert rule', 'error');
    }
}

function getChannelSummary(channel) {
    if (!channel) return '';
    let config = {};
    try {
        config = channel.config_json ? JSON.parse(channel.config_json) : (channel.config || {});
    } catch (e) { config = {}; }

    switch (channel.type) {
        case 'email':
            const recipients = Array.isArray(config.to) ? config.to.join(', ') : (config.to || 'No recipients');
            return `Recipients: ${escapeHtml(recipients)}`;
        case 'webhook':
            return `URL: ${escapeHtml(config.url || 'Not configured')}`;
        case 'slack':
            const slackChannel = config.channel ? ` (${config.channel})` : '';
            return `Slack webhook${slackChannel}`;
        case 'teams':
            return 'Microsoft Teams webhook';
        case 'pagerduty':
            return `PagerDuty (${config.severity || 'warning'} severity)`;
        default:
            return channel.type;
    }
}

function editNotificationChannel(id) {
    const channel = cachedNotificationChannels.find(ch => ch.id === id);
    if (channel) {
        showNotificationChannelModal(channel);
    } else {
        window.__pm_shared.showToast('Channel not found', 'error');
    }
}

async function deleteNotificationChannel(id) {
    if (!await window.__pm_shared.showConfirm('Delete this notification channel?')) return;
    try {
        const resp = await fetch(`/api/v1/notification-channels/${id}`, { method: 'DELETE' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Notification channel deleted', 'success');
        loadAlertRules();
    } catch (err) {
        console.error('Failed to delete notification channel:', err);
        window.__pm_shared.showToast('Failed to delete notification channel', 'error');
    }
}

async function editEscalationPolicy(id) {
    try {
        const resp = await fetch(`/api/v1/escalation-policies/${id}`);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const policy = await resp.json();
        showEscalationPolicyModal(policy);
    } catch (err) {
        console.error('Failed to load escalation policy:', err);
        window.__pm_shared.showToast('Failed to load escalation policy', 'error');
    }
}

async function deleteEscalationPolicy(id) {
    if (!await window.__pm_shared.showConfirm('Delete this escalation policy?')) return;
    try {
        const resp = await fetch(`/api/v1/escalation-policies/${id}`, { method: 'DELETE' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Escalation policy deleted', 'success');
        loadAlertRules();
    } catch (err) {
        console.error('Failed to delete escalation policy:', err);
        window.__pm_shared.showToast('Failed to delete escalation policy', 'error');
    }
}

async function editMaintenanceWindow(id) {
    try {
        const resp = await fetch(`/api/v1/maintenance-windows/${id}`);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const window_ = await resp.json();
        showMaintenanceWindowModal(window_);
    } catch (err) {
        console.error('Failed to load maintenance window:', err);
        window.__pm_shared.showToast('Failed to load maintenance window', 'error');
    }
}

async function deleteMaintenanceWindow(id) {
    if (!await window.__pm_shared.showConfirm('Delete this maintenance window?')) return;
    try {
        const resp = await fetch(`/api/v1/maintenance-windows/${id}`, { method: 'DELETE' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Maintenance window deleted', 'success');
        loadAlertRules();
    } catch (err) {
        console.error('Failed to delete maintenance window:', err);
        window.__pm_shared.showToast('Failed to delete maintenance window', 'error');
    }
}

async function deleteReportSchedule(id) {
    if (!await window.__pm_shared.showConfirm('Delete this scheduled report?')) return;
    try {
        const resp = await fetch(`/api/v1/report-schedules/${id}`, { method: 'DELETE' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Scheduled report deleted', 'success');
        loadAlertRules();
    } catch (err) {
        console.error('Failed to delete scheduled report:', err);
        window.__pm_shared.showToast('Failed to delete scheduled report', 'error');
    }
}

async function runScheduleNow(scheduleId) {
    try {
        const resp = await fetch(`/api/v1/report-schedules/${scheduleId}/run`, { method: 'POST' });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        window.__pm_shared.showToast('Report generation started', 'success');
        loadRecentReports();
    } catch (err) {
        console.error('Failed to run scheduled report:', err);
        window.__pm_shared.showToast('Failed to run scheduled report', 'error');
    }
}

function editAlertRule(id) {
    // Fetch the rule and populate modal
    fetch(`/api/v1/alert-rules/${id}`)
        .then(resp => {
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            return resp.json();
        })
        .then(rule => {
            showAlertRuleModal(rule);
        })
        .catch(err => {
            console.error('Failed to load alert rule:', err);
            window.__pm_shared.showToast('Failed to load alert rule', 'error');
        });
}

// Initialize Alert Rule Modal
let alertRuleModalInitialized = false;
function initAlertRuleModal() {
    const modal = document.getElementById('alert_rule_modal');
    if (!modal || alertRuleModalInitialized) return;
    alertRuleModalInitialized = true;

    const closeBtn = document.getElementById('alert_rule_modal_close_x');
    const cancelBtn = document.getElementById('alert_rule_cancel');
    const saveBtn = document.getElementById('alert_rule_save');

    if (closeBtn) closeBtn.addEventListener('click', closeAlertRuleModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeAlertRuleModal);
    if (saveBtn) saveBtn.addEventListener('click', saveAlertRule);

    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeAlertRuleModal();
    });

    // Sync severity radio buttons with hidden select
    const severityRadios = modal.querySelectorAll('input[name="alert_severity"]');
    const severitySelect = document.getElementById('alert_rule_severity');
    severityRadios.forEach(radio => {
        radio.addEventListener('change', () => {
            if (severitySelect) severitySelect.value = radio.value;
            updateAlertRulePreview();
        });
    });

    // Update preview when type/threshold/operator changes
    const typeSelect = document.getElementById('alert_rule_type');
    const thresholdInput = document.getElementById('alert_rule_threshold');
    const operatorSelect = document.getElementById('alert_rule_operator');
    const durationInput = document.getElementById('alert_rule_duration');

    if (typeSelect) typeSelect.addEventListener('change', () => {
        updateAlertRuleThresholdVisibility();
        updateAlertRulePreview();
    });
    if (thresholdInput) thresholdInput.addEventListener('input', updateAlertRulePreview);
    if (operatorSelect) operatorSelect.addEventListener('change', updateAlertRulePreview);
    if (durationInput) durationInput.addEventListener('input', updateAlertRulePreview);

    // Show/hide scope ID field based on scope selection
    const scopeSelect = document.getElementById('alert_rule_scope');
    if (scopeSelect) {
        scopeSelect.addEventListener('change', () => {
            const scopeIdField = document.getElementById('alert_rule_scope_id_field');
            if (scopeIdField) {
                scopeIdField.style.display = scopeSelect.value === 'fleet' ? 'none' : 'block';
            }
        });
    }
}

// Update threshold row visibility based on alert type
function updateAlertRuleThresholdVisibility() {
    const typeSelect = document.getElementById('alert_rule_type');
    const thresholdRow = document.getElementById('alert_rule_threshold_row');
    const durationRow = document.getElementById('alert_rule_duration_row');
    const unitSpan = document.getElementById('alert_rule_threshold_unit');

    if (!typeSelect) return;

    const type = typeSelect.value;
    const hasThreshold = ['toner_low', 'supply_empty', 'page_count'].includes(type);
    const hasDuration = ['device_offline', 'agent_offline'].includes(type);

    if (thresholdRow) thresholdRow.style.display = hasThreshold ? 'flex' : 'none';
    if (durationRow) durationRow.style.display = hasDuration ? 'flex' : 'none';

    // Update unit based on type
    if (unitSpan) {
        if (type === 'page_count') {
            unitSpan.textContent = 'pages';
        } else {
            unitSpan.textContent = '%';
        }
    }
}

// Generate human-readable preview of the alert condition
function updateAlertRulePreview() {
    const previewText = document.getElementById('alert_rule_preview_text');
    if (!previewText) return;

    const typeSelect = document.getElementById('alert_rule_type');
    const thresholdInput = document.getElementById('alert_rule_threshold');
    const operatorSelect = document.getElementById('alert_rule_operator');
    const durationInput = document.getElementById('alert_rule_duration');

    const type = typeSelect?.value || 'toner_low';
    const threshold = thresholdInput?.value || '10';
    const operator = operatorSelect?.value || 'lt';
    const duration = durationInput?.value || '5';

    const typeLabels = {
        'toner_low': 'toner level',
        'supply_empty': 'supply level',
        'device_offline': 'device goes offline',
        'device_error': 'device reports an error',
        'agent_offline': 'agent goes offline',
        'page_count': 'page count',
        'custom': 'custom metric'
    };

    const operatorLabels = {
        'lt': 'drops below',
        'lte': 'reaches or drops below',
        'gt': 'exceeds',
        'gte': 'reaches or exceeds',
        'eq': 'equals'
    };

    let preview = '';
    if (['device_offline', 'device_error', 'agent_offline'].includes(type)) {
        preview = `Alert when ${typeLabels[type]} for more than ${duration} minutes`;
    } else {
        const unit = type === 'page_count' ? ' pages' : '%';
        preview = `Alert when ${typeLabels[type]} ${operatorLabels[operator]} ${threshold}${unit}`;
    }

    previewText.textContent = preview;
}

function closeAlertRuleModal() {
    const modal = document.getElementById('alert_rule_modal');
    if (modal) modal.style.display = 'none';
}

// Initialize Notification Channel Modal
let notificationChannelModalInitialized = false;
function initNotificationChannelModal() {
    const modal = document.getElementById('notification_channel_modal');
    if (!modal || notificationChannelModalInitialized) return;
    notificationChannelModalInitialized = true;

    const closeBtn = document.getElementById('notification_channel_modal_close_x');
    const cancelBtn = document.getElementById('channel_cancel');
    const saveBtn = document.getElementById('channel_save');
    const testBtn = document.getElementById('channel_test');
    const typeSelect = document.getElementById('channel_type');

    if (closeBtn) closeBtn.addEventListener('click', closeNotificationChannelModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeNotificationChannelModal);
    if (saveBtn) saveBtn.addEventListener('click', saveNotificationChannel);
    if (testBtn) testBtn.addEventListener('click', testNotificationChannel);

    // Handle type change to show/hide config sections
    if (typeSelect) {
        typeSelect.addEventListener('change', updateChannelConfigSection);
    }

    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeNotificationChannelModal();
    });
}

function closeNotificationChannelModal() {
    const modal = document.getElementById('notification_channel_modal');
    if (modal) modal.style.display = 'none';
}

// Initialize Escalation Policy Modal
let escalationPolicyModalInitialized = false;
function initEscalationPolicyModal() {
    const modal = document.getElementById('escalation_policy_modal');
    if (!modal || escalationPolicyModalInitialized) return;
    escalationPolicyModalInitialized = true;

    const closeBtn = document.getElementById('escalation_policy_modal_close_x');
    const cancelBtn = document.getElementById('escalation_cancel');
    const saveBtn = document.getElementById('escalation_save');
    const addStepBtn = document.getElementById('add_escalation_step');

    if (closeBtn) closeBtn.addEventListener('click', closeEscalationPolicyModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeEscalationPolicyModal);
    if (saveBtn) saveBtn.addEventListener('click', saveEscalationPolicy);
    if (addStepBtn) addStepBtn.addEventListener('click', addEscalationStep);

    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeEscalationPolicyModal();
    });
}

function addEscalationStep(afterMinutes = 15, channelId = '') {
    const container = document.getElementById('escalation_steps_container');
    if (!container) return;

    const stepNum = container.querySelectorAll('.escalation-step').length + 1;
    const stepDiv = document.createElement('div');
    stepDiv.className = 'escalation-step';

    // Build channel options from cached channels
    const channelOptions = (cachedNotificationChannels || [])
        .filter(ch => ch.enabled)
        .map(ch => `<option value="${ch.id}" ${ch.id == channelId ? 'selected' : ''}>${escapeHtml(ch.name)} (${ch.type})</option>`)
        .join('');

    stepDiv.innerHTML = `
        <div class="escalation-step-header">
            <span class="escalation-step-number">Step ${stepNum}</span>
            <button type="button" class="escalation-step-remove remove-step" title="Remove step">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M4.646 4.646a.5.5 0 0 1 .708 0L8 7.293l2.646-2.647a.5.5 0 0 1 .708.708L8.707 8l2.647 2.646a.5.5 0 0 1-.708.708L8 8.707l-2.646 2.647a.5.5 0 0 1-.708-.708L7.293 8 4.646 5.354a.5.5 0 0 1 0-.708z"/></svg>
            </button>
        </div>
        <div class="escalation-step-body">
            <span class="escalation-step-label">After</span>
            <input type="number" class="step-delay" value="${afterMinutes}" min="1" max="1440" title="Minutes to wait before escalating" autocomplete="off" data-1p-ignore data-lpignore="true" />
            <span class="escalation-step-label">minutes, notify via</span>
            <select class="step-channel">
                <option value="">-- Select Channel --</option>
                ${channelOptions}
            </select>
        </div>
    `;

    stepDiv.querySelector('.remove-step').addEventListener('click', () => {
        stepDiv.remove();
        renumberEscalationSteps();
    });

    container.appendChild(stepDiv);
}

function renumberEscalationSteps() {
    const container = document.getElementById('escalation_steps_container');
    if (!container) return;

    container.querySelectorAll('.escalation-step').forEach((step, idx) => {
        const label = step.querySelector('.escalation-step-number');
        if (label) label.textContent = `Step ${idx + 1}`;
    });
}

function closeEscalationPolicyModal() {
    const modal = document.getElementById('escalation_policy_modal');
    if (modal) modal.style.display = 'none';
}

// Initialize Maintenance Window Modal
let maintenanceWindowModalInitialized = false;
function initMaintenanceWindowModal() {
    const modal = document.getElementById('maintenance_window_modal');
    if (!modal || maintenanceWindowModalInitialized) return;
    maintenanceWindowModalInitialized = true;

    const closeBtn = document.getElementById('maintenance_window_modal_close_x');
    const cancelBtn = document.getElementById('maintenance_cancel');
    const saveBtn = document.getElementById('maintenance_save');

    if (closeBtn) closeBtn.addEventListener('click', closeMaintenanceWindowModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeMaintenanceWindowModal);
    if (saveBtn) saveBtn.addEventListener('click', saveMaintenanceWindow);

    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeMaintenanceWindowModal();
    });

    // Handle recurring toggle to show/hide options
    const recurringCheck = document.getElementById('maintenance_recurring');
    const recurringOptions = document.getElementById('maintenance_recurring_options');
    if (recurringCheck && recurringOptions) {
        recurringCheck.addEventListener('change', () => {
            recurringOptions.style.display = recurringCheck.checked ? 'block' : 'none';
        });
    }

    // Update duration preview when dates change
    const startInput = document.getElementById('maintenance_start');
    const endInput = document.getElementById('maintenance_end');
    if (startInput && endInput) {
        startInput.addEventListener('change', updateMaintenanceDurationPreview);
        endInput.addEventListener('change', updateMaintenanceDurationPreview);
    }

    // Show/hide scope ID field based on scope selection
    const scopeSelect = document.getElementById('maintenance_scope');
    if (scopeSelect) {
        scopeSelect.addEventListener('change', () => {
            const scopeIdField = document.getElementById('maintenance_scope_id_field');
            if (scopeIdField) {
                scopeIdField.style.display = scopeSelect.value === 'fleet' ? 'none' : 'block';
            }
        });
    }
}

// Update maintenance duration preview
function updateMaintenanceDurationPreview() {
    const startInput = document.getElementById('maintenance_start');
    const endInput = document.getElementById('maintenance_end');
    const durationText = document.getElementById('maintenance_duration_text');

    if (!startInput || !endInput || !durationText) return;

    const start = startInput.value ? new Date(startInput.value) : null;
    const end = endInput.value ? new Date(endInput.value) : null;

    if (!start || !end) {
        durationText.textContent = 'Select start and end times';
        return;
    }

    if (end <= start) {
        durationText.textContent = 'End time must be after start time';
        durationText.style.color = 'var(--danger)';
        return;
    }

    durationText.style.color = '';

    const diffMs = end - start;
    const diffMins = Math.round(diffMs / 60000);
    const diffHours = Math.floor(diffMins / 60);
    const remainMins = diffMins % 60;

    let duration = '';
    if (diffHours > 0) {
        duration += `${diffHours} hour${diffHours !== 1 ? 's' : ''}`;
        if (remainMins > 0) duration += ` ${remainMins} min`;
    } else {
        duration = `${diffMins} minutes`;
    }

    durationText.textContent = `Duration: ${duration}`;
}

function closeMaintenanceWindowModal() {
    const modal = document.getElementById('maintenance_window_modal');
    if (modal) modal.style.display = 'none';
}

// Initialize Scheduled Report Modal
let scheduledReportModalInitialized = false;
function initScheduledReportModal() {
    const modal = document.getElementById('scheduled_report_modal');
    if (!modal || scheduledReportModalInitialized) return;
    scheduledReportModalInitialized = true;

    const closeBtn = document.getElementById('scheduled_report_modal_close_x');
    const cancelBtn = document.getElementById('schedule_cancel');
    const saveBtn = document.getElementById('schedule_save');

    if (closeBtn) closeBtn.addEventListener('click', closeScheduledReportModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeScheduledReportModal);
    if (saveBtn) saveBtn.addEventListener('click', saveScheduledReport);

    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeScheduledReportModal();
    });
}

function closeScheduledReportModal() {
    const modal = document.getElementById('scheduled_report_modal');
    if (modal) modal.style.display = 'none';
}

function showAlertRuleModal(existingRule = null) {
    const modal = document.getElementById('alert_rule_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const isEdit = existingRule && existingRule.id;

    // Reset/populate form fields
    const nameInput = form.querySelector('#alert_rule_name');
    const typeSelect = form.querySelector('#alert_rule_type');
    const metricSelect = form.querySelector('#alert_rule_metric');
    const conditionSelect = form.querySelector('#alert_rule_condition');
    const thresholdInput = form.querySelector('#alert_rule_threshold');
    const durationInput = form.querySelector('#alert_rule_duration');
    const severitySelect = form.querySelector('#alert_rule_severity');
    const enabledCheck = form.querySelector('#alert_rule_enabled');

    if (nameInput) nameInput.value = existingRule?.name || '';
    if (typeSelect) typeSelect.value = existingRule?.type || 'threshold';
    if (metricSelect) metricSelect.value = existingRule?.metric || 'toner_level';
    if (conditionSelect) conditionSelect.value = existingRule?.condition || 'less_than';
    if (thresholdInput) thresholdInput.value = existingRule?.threshold ?? '';
    if (durationInput) durationInput.value = existingRule?.duration_minutes || 5;
    if (severitySelect) severitySelect.value = existingRule?.severity || 'warning';
    if (enabledCheck) enabledCheck.checked = existingRule?.enabled !== false;

    // Store ID for save
    modal.dataset.editId = isEdit ? existingRule.id : '';

    // Update modal title
    const title = modal.querySelector('.modal-title');
    if (title) title.textContent = isEdit ? 'Edit Alert Rule' : 'New Alert Rule';

    // Load notification channels for selection
    loadChannelsForAlertRule(existingRule?.channel_ids || []);

    modal.style.display = 'flex';
}

// Load notification channels into the alert rule modal
async function loadChannelsForAlertRule(selectedIds = []) {
    const container = document.getElementById('alert_rule_channels');
    if (!container) return;

    container.innerHTML = '<div class="muted-text" style="padding:12px;text-align:center;">Loading channels...</div>';

    try {
        const resp = await fetch('/api/v1/notification-channels');
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

        const data = await resp.json();
        const channels = data.channels || [];

        if (!channels || channels.length === 0) {
            container.innerHTML = '<div class="muted-text" style="padding:12px;text-align:center;">No notification channels configured. <a href="#" onclick="window.__alerting_showNotificationChannelModal();return false;">Create one</a></div>';
            return;
        }

        // Ensure selectedIds is an array
        const selected = Array.isArray(selectedIds) ? selectedIds : [];

        container.innerHTML = channels.map(ch => {
            const isChecked = selected.includes(ch.id);
            const icon = getChannelIcon(ch.type);
            return `
                <label class="channel-checkbox-item">
                    <input type="checkbox" name="alert_rule_channel" value="${ch.id}" ${isChecked ? 'checked' : ''} />
                    <span class="channel-checkbox-icon">${icon}</span>
                    <span class="channel-checkbox-name">${escapeHtml(ch.name)}</span>
                    <span class="channel-checkbox-type">${ch.type}</span>
                </label>
            `;
        }).join('');
    } catch (err) {
        console.error('Failed to load channels:', err);
        container.innerHTML = '<div class="muted-text" style="padding:12px;text-align:center;color:var(--danger);">Failed to load channels</div>';
    }
}

// Helper to get icon for channel type
function getChannelIcon(type) {
    const icons = {
        'email': '📧',
        'slack': '💬',
        'teams': '💼',
        'discord': '🎮',
        'webhook': '🔗',
        'pagerduty': '📟',
        'telegram': '✈️',
        'pushover': '📱',
        'ntfy': '🔔'
    };
    return icons[type] || '📢';
}

async function saveAlertRule() {
    const modal = document.getElementById('alert_rule_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const editId = modal.dataset.editId;

    const payload = {
        name: form.querySelector('#alert_rule_name')?.value || '',
        type: form.querySelector('#alert_rule_type')?.value || 'threshold',
        metric: form.querySelector('#alert_rule_metric')?.value || '',
        condition: form.querySelector('#alert_rule_condition')?.value || '',
        threshold: parseFloat(form.querySelector('#alert_rule_threshold')?.value) || 0,
        duration_minutes: parseInt(form.querySelector('#alert_rule_duration')?.value) || 5,
        severity: form.querySelector('#alert_rule_severity')?.value || 'warning',
        enabled: form.querySelector('#alert_rule_enabled')?.checked !== false,
        // Collect selected channel IDs
        channel_ids: Array.from(form.querySelectorAll('input[name="alert_rule_channel"]:checked'))
            .map(cb => parseInt(cb.value, 10))
            .filter(id => !isNaN(id))
    };

    if (!payload.name) {
        window.__pm_shared.showToast('Name is required', 'error');
        return;
    }

    try {
        const url = editId ? `/api/v1/alert-rules/${editId}` : '/api/v1/alert-rules';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

        window.__pm_shared.showToast(editId ? 'Alert rule updated' : 'Alert rule created', 'success');
        modal.style.display = 'none';
        loadAlertRules();
    } catch (err) {
        console.error('Failed to save alert rule:', err);
        window.__pm_shared.showToast('Failed to save alert rule', 'error');
    }
}

function showNotificationChannelModal(existingChannel = null) {
    const modal = document.getElementById('notification_channel_modal');
    if (!modal) return;

    const isEdit = existingChannel && existingChannel.id;

    // Reset all fields
    const nameInput = document.getElementById('channel_name');
    const typeSelect = document.getElementById('channel_type');
    const enabledCheck = document.getElementById('channel_enabled');
    const minSeveritySelect = document.getElementById('channel_min_severity');
    const rateLimitInput = document.getElementById('channel_rate_limit');

    // Reset basic fields
    if (nameInput) nameInput.value = existingChannel?.name || '';
    if (typeSelect) typeSelect.value = existingChannel?.type || 'email';
    if (enabledCheck) enabledCheck.checked = existingChannel?.enabled !== false;
    if (minSeveritySelect) minSeveritySelect.value = existingChannel?.min_severity || '';
    if (rateLimitInput) rateLimitInput.value = existingChannel?.rate_limit_per_hour || 0;

    // Parse existing config if editing
    let config = {};
    if (existingChannel?.config_json) {
        try { config = JSON.parse(existingChannel.config_json); } catch (e) { config = {}; }
    } else if (existingChannel?.config) {
        config = existingChannel.config;
    }

    // Populate type-specific fields based on channel type
    const channelType = existingChannel?.type || 'email';

    // Email fields
    const emailTo = document.getElementById('channel_email_to');
    const emailSubject = document.getElementById('channel_email_subject');
    if (emailTo) emailTo.value = Array.isArray(config.to) ? config.to.join(', ') : (config.to || '');
    if (emailSubject) emailSubject.value = config.subject_prefix || '';

    // Webhook fields
    const webhookUrl = document.getElementById('channel_webhook_url');
    const webhookMethod = document.getElementById('channel_webhook_method');
    const webhookHeaders = document.getElementById('channel_webhook_headers');
    if (webhookUrl) webhookUrl.value = config.url || '';
    if (webhookMethod) webhookMethod.value = config.method || 'POST';
    if (webhookHeaders) webhookHeaders.value = config.headers ? JSON.stringify(config.headers, null, 2) : '';

    // Slack fields
    const slackUrl = document.getElementById('channel_slack_url');
    const slackChannel = document.getElementById('channel_slack_channel');
    const slackUsername = document.getElementById('channel_slack_username');
    if (slackUrl) slackUrl.value = config.webhook_url || '';
    if (slackChannel) slackChannel.value = config.channel || '';
    if (slackUsername) slackUsername.value = config.username || '';

    // Discord fields
    const discordUrl = document.getElementById('channel_discord_url');
    const discordUsername = document.getElementById('channel_discord_username');
    if (discordUrl) discordUrl.value = config.webhook_url || '';
    if (discordUsername) discordUsername.value = config.username || '';

    // Teams fields
    const teamsUrl = document.getElementById('channel_teams_url');
    if (teamsUrl) teamsUrl.value = config.webhook_url || '';

    // Telegram fields
    const telegramToken = document.getElementById('channel_telegram_token');
    const telegramChat = document.getElementById('channel_telegram_chat');
    if (telegramToken) telegramToken.value = config.bot_token || '';
    if (telegramChat) telegramChat.value = config.chat_id || '';

    // PagerDuty fields
    const pagerdutyKey = document.getElementById('channel_pagerduty_key');
    const pagerdutySeverity = document.getElementById('channel_pagerduty_severity');
    if (pagerdutyKey) pagerdutyKey.value = config.routing_key || '';
    if (pagerdutySeverity) pagerdutySeverity.value = config.severity || 'warning';

    // Pushover fields
    const pushoverUser = document.getElementById('channel_pushover_user');
    const pushoverToken = document.getElementById('channel_pushover_token');
    const pushoverDevice = document.getElementById('channel_pushover_device');
    const pushoverSound = document.getElementById('channel_pushover_sound');
    if (pushoverUser) pushoverUser.value = config.user_key || '';
    if (pushoverToken) pushoverToken.value = config.api_token || '';
    if (pushoverDevice) pushoverDevice.value = config.device || '';
    if (pushoverSound) pushoverSound.value = config.sound || '';

    // ntfy fields
    const ntfyServer = document.getElementById('channel_ntfy_server');
    const ntfyTopic = document.getElementById('channel_ntfy_topic');
    const ntfyUsername = document.getElementById('channel_ntfy_username');
    const ntfyPassword = document.getElementById('channel_ntfy_password');
    const ntfyToken = document.getElementById('channel_ntfy_token');
    if (ntfyServer) ntfyServer.value = config.server_url || '';
    if (ntfyTopic) ntfyTopic.value = config.topic || '';
    if (ntfyUsername) ntfyUsername.value = config.username || '';
    if (ntfyPassword) ntfyPassword.value = config.password || '';
    if (ntfyToken) ntfyToken.value = config.access_token || '';

    modal.dataset.editId = isEdit ? existingChannel.id : '';

    const title = modal.querySelector('.modal-title');
    if (title) title.textContent = isEdit ? 'Edit Notification Channel' : 'New Notification Channel';

    // Show correct config section
    updateChannelConfigSection();

    modal.style.display = 'flex';
}

function updateChannelConfigSection() {
    const typeSelect = document.getElementById('channel_type');
    if (!typeSelect) return;

    const channelType = typeSelect.value;
    const sections = ['email', 'webhook', 'slack', 'discord', 'teams', 'telegram', 'pagerduty', 'pushover', 'ntfy'];

    sections.forEach(section => {
        const el = document.getElementById(`channel_config_${section}`);
        if (el) {
            el.style.display = (section === channelType) ? 'block' : 'none';
        }
    });

    // Update card selection visual
    document.querySelectorAll('.channel-type-card').forEach(card => {
        const radio = card.querySelector('input[type="radio"]');
        if (radio) {
            radio.checked = (card.dataset.type === channelType);
        }
    });
}

async function saveNotificationChannel() {
    const modal = document.getElementById('notification_channel_modal');
    if (!modal) return;

    const editId = modal.dataset.editId;
    const channelType = document.getElementById('channel_type')?.value || 'email';
    const name = document.getElementById('channel_name')?.value?.trim() || '';

    if (!name) {
        window.__pm_shared.showToast('Channel name is required', 'error');
        return;
    }

    // Build config based on channel type
    let config = {};
    let validationError = null;

    switch (channelType) {
        case 'email': {
            const toStr = document.getElementById('channel_email_to')?.value?.trim() || '';
            const subjectPrefix = document.getElementById('channel_email_subject')?.value?.trim() || '';
            if (!toStr) {
                validationError = 'Email recipients are required';
                break;
            }
            const toList = toStr.split(',').map(e => e.trim()).filter(e => e);
            if (toList.length === 0) {
                validationError = 'At least one email recipient is required';
                break;
            }
            config = { to: toList };
            if (subjectPrefix) config.subject_prefix = subjectPrefix;
            break;
        }
        case 'webhook': {
            const url = document.getElementById('channel_webhook_url')?.value?.trim() || '';
            const method = document.getElementById('channel_webhook_method')?.value || 'POST';
            const headersStr = document.getElementById('channel_webhook_headers')?.value?.trim() || '';
            if (!url) {
                validationError = 'Webhook URL is required';
                break;
            }
            config = { url, method };
            if (headersStr) {
                try {
                    config.headers = JSON.parse(headersStr);
                } catch (e) {
                    validationError = 'Invalid JSON in headers field';
                    break;
                }
            }
            break;
        }
        case 'slack': {
            const webhookUrl = document.getElementById('channel_slack_url')?.value?.trim() || '';
            const channel = document.getElementById('channel_slack_channel')?.value?.trim() || '';
            const username = document.getElementById('channel_slack_username')?.value?.trim() || '';
            if (!webhookUrl) {
                validationError = 'Slack webhook URL is required';
                break;
            }
            config = { webhook_url: webhookUrl };
            if (channel) config.channel = channel;
            if (username) config.username = username;
            break;
        }
        case 'discord': {
            const webhookUrl = document.getElementById('channel_discord_url')?.value?.trim() || '';
            const username = document.getElementById('channel_discord_username')?.value?.trim() || '';
            if (!webhookUrl) {
                validationError = 'Discord webhook URL is required';
                break;
            }
            config = { webhook_url: webhookUrl };
            if (username) config.username = username;
            break;
        }
        case 'teams': {
            const webhookUrl = document.getElementById('channel_teams_url')?.value?.trim() || '';
            if (!webhookUrl) {
                validationError = 'Teams webhook URL is required';
                break;
            }
            config = { webhook_url: webhookUrl };
            break;
        }
        case 'telegram': {
            const botToken = document.getElementById('channel_telegram_token')?.value?.trim() || '';
            const chatId = document.getElementById('channel_telegram_chat')?.value?.trim() || '';
            if (!botToken) {
                validationError = 'Telegram bot token is required';
                break;
            }
            if (!chatId) {
                validationError = 'Telegram chat ID is required';
                break;
            }
            config = { bot_token: botToken, chat_id: chatId };
            break;
        }
        case 'pagerduty': {
            const routingKey = document.getElementById('channel_pagerduty_key')?.value?.trim() || '';
            const severity = document.getElementById('channel_pagerduty_severity')?.value || 'warning';
            if (!routingKey) {
                validationError = 'PagerDuty integration key is required';
                break;
            }
            config = { routing_key: routingKey, severity };
            break;
        }
        case 'pushover': {
            const userKey = document.getElementById('channel_pushover_user')?.value?.trim() || '';
            const apiToken = document.getElementById('channel_pushover_token')?.value?.trim() || '';
            const device = document.getElementById('channel_pushover_device')?.value?.trim() || '';
            const sound = document.getElementById('channel_pushover_sound')?.value || '';
            if (!userKey) {
                validationError = 'Pushover user/group key is required';
                break;
            }
            if (!apiToken) {
                validationError = 'Pushover API token is required';
                break;
            }
            config = { user_key: userKey, api_token: apiToken };
            if (device) config.device = device;
            if (sound) config.sound = sound;
            break;
        }
        case 'ntfy': {
            const serverUrl = document.getElementById('channel_ntfy_server')?.value?.trim() || '';
            const topic = document.getElementById('channel_ntfy_topic')?.value?.trim() || '';
            const username = document.getElementById('channel_ntfy_username')?.value?.trim() || '';
            const password = document.getElementById('channel_ntfy_password')?.value?.trim() || '';
            const accessToken = document.getElementById('channel_ntfy_token')?.value?.trim() || '';
            if (!topic) {
                validationError = 'ntfy topic is required';
                break;
            }
            config = { topic };
            if (serverUrl) config.server_url = serverUrl;
            if (accessToken) {
                config.access_token = accessToken;
            } else if (username) {
                config.username = username;
                if (password) config.password = password;
            }
            break;
        }
    }

    if (validationError) {
        window.__pm_shared.showToast(validationError, 'error');
        return;
    }

    const payload = {
        name,
        type: channelType,
        config_json: JSON.stringify(config),
        enabled: document.getElementById('channel_enabled')?.checked !== false,
        min_severity: document.getElementById('channel_min_severity')?.value || '',
        rate_limit_per_hour: parseInt(document.getElementById('channel_rate_limit')?.value, 10) || 0
    };

    try {
        const url = editId ? `/api/v1/notification-channels/${editId}` : '/api/v1/notification-channels';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) {
            const errorText = await resp.text();
            throw new Error(errorText || `HTTP ${resp.status}`);
        }

        window.__pm_shared.showToast(editId ? 'Channel updated' : 'Channel created', 'success');
        modal.style.display = 'none';
        loadAlertRules();
    } catch (err) {
        console.error('Failed to save notification channel:', err);
        window.__pm_shared.showToast('Failed to save: ' + (err.message || err), 'error');
    }
}

// Send test notification to verify channel configuration
async function testNotificationChannel() {
    const modal = document.getElementById('notification_channel_modal');
    if (!modal) return;

    const testBtn = document.getElementById('channel_test');
    const errorDiv = document.getElementById('channel_error');

    // Build config from form
    const channelType = document.getElementById('channel_type')?.value || 'email';
    const channelName = document.getElementById('channel_name')?.value || 'Test Channel';
    const config = buildChannelConfig(channelType);

    // Basic validation
    if (!channelName.trim()) {
        if (errorDiv) {
            errorDiv.textContent = 'Please enter a channel name';
            errorDiv.style.display = 'block';
        }
        return;
    }

    // Show loading state
    if (testBtn) {
        testBtn.disabled = true;
        testBtn.innerHTML = '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" class="spin" style="margin-right:6px;"><path d="M8 0a8 8 0 1 0 0 16A8 8 0 0 0 8 0zm1 14.5a6.5 6.5 0 1 1 0-13 6.5 6.5 0 0 1 0 13z" opacity="0.3"/><path d="M8 0a8 8 0 0 1 8 8h-1.5a6.5 6.5 0 0 0-6.5-6.5V0z"/></svg>Testing...';
    }

    try {
        const resp = await fetch('/api/v1/notification-channels/test', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                type: channelType,
                name: channelName,
                config_json: JSON.stringify(config)
            })
        });

        if (!resp.ok) {
            const errorText = await resp.text();
            throw new Error(errorText || `HTTP ${resp.status}`);
        }

        window.__pm_shared.showToast('Test notification sent successfully!', 'success');
        if (errorDiv) errorDiv.style.display = 'none';
    } catch (err) {
        console.error('Failed to send test notification:', err);
        if (errorDiv) {
            errorDiv.textContent = 'Test failed: ' + (err.message || err);
            errorDiv.style.display = 'block';
        }
        window.__pm_shared.showToast('Test failed: ' + (err.message || err), 'error');
    } finally {
        // Restore button
        if (testBtn) {
            testBtn.disabled = false;
            testBtn.innerHTML = '<svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" style="margin-right:6px;"><path d="M8 15A7 7 0 1 1 8 1a7 7 0 0 1 0 14zm0 1A8 8 0 1 0 8 0a8 8 0 0 0 0 16z"/><path d="M10.97 4.97a.235.235 0 0 0-.02.022L7.477 9.417 5.384 7.323a.75.75 0 0 0-1.06 1.06L6.97 11.03a.75.75 0 0 0 1.079-.02l3.992-4.99a.75.75 0 0 0-1.071-1.05z"/></svg>Send Test';
        }
    }
}

// Helper to build channel config from form fields
function buildChannelConfig(channelType) {
    const config = {};

    switch (channelType) {
        case 'email':
            const emailTo = document.getElementById('channel_email_to')?.value || '';
            config.to = emailTo.split(',').map(e => e.trim()).filter(e => e);
            config.subject_prefix = document.getElementById('channel_email_subject')?.value || '';
            break;
        case 'slack':
            config.webhook_url = document.getElementById('channel_slack_url')?.value || '';
            config.channel = document.getElementById('channel_slack_channel')?.value || '';
            config.username = document.getElementById('channel_slack_username')?.value || '';
            break;
        case 'discord':
            config.webhook_url = document.getElementById('channel_discord_url')?.value || '';
            config.username = document.getElementById('channel_discord_username')?.value || '';
            break;
        case 'teams':
            config.webhook_url = document.getElementById('channel_teams_url')?.value || '';
            break;
        case 'telegram':
            config.bot_token = document.getElementById('channel_telegram_token')?.value || '';
            config.chat_id = document.getElementById('channel_telegram_chat')?.value || '';
            break;
        case 'pagerduty':
            config.routing_key = document.getElementById('channel_pagerduty_key')?.value || '';
            config.severity = document.getElementById('channel_pagerduty_severity')?.value || 'warning';
            break;
        case 'pushover':
            config.user_key = document.getElementById('channel_pushover_user')?.value || '';
            config.api_token = document.getElementById('channel_pushover_token')?.value || '';
            config.device = document.getElementById('channel_pushover_device')?.value || '';
            config.sound = document.getElementById('channel_pushover_sound')?.value || '';
            break;
        case 'ntfy':
            config.server_url = document.getElementById('channel_ntfy_server')?.value || '';
            config.topic = document.getElementById('channel_ntfy_topic')?.value || '';
            config.username = document.getElementById('channel_ntfy_username')?.value || '';
            config.password = document.getElementById('channel_ntfy_password')?.value || '';
            config.token = document.getElementById('channel_ntfy_token')?.value || '';
            break;
        case 'webhook':
            config.url = document.getElementById('channel_webhook_url')?.value || '';
            config.method = document.getElementById('channel_webhook_method')?.value || 'POST';
            try {
                const headersStr = document.getElementById('channel_webhook_headers')?.value || '';
                config.headers = headersStr ? JSON.parse(headersStr) : {};
            } catch (e) {
                config.headers = {};
            }
            break;
    }

    return config;
}

function showEscalationPolicyModal(existingPolicy = null) {
    const modal = document.getElementById('escalation_policy_modal');
    if (!modal) return;

    const isEdit = existingPolicy && existingPolicy.id;

    const nameInput = document.getElementById('escalation_name');
    const descInput = document.getElementById('escalation_description');
    const enabledCheck = document.getElementById('escalation_enabled');
    const stepsContainer = document.getElementById('escalation_steps_container');
    const errorDiv = document.getElementById('escalation_error');

    // Reset form
    if (nameInput) nameInput.value = existingPolicy?.name || '';
    if (descInput) descInput.value = existingPolicy?.description || '';
    if (enabledCheck) enabledCheck.checked = existingPolicy?.enabled !== false;
    if (stepsContainer) stepsContainer.innerHTML = '';
    if (errorDiv) errorDiv.style.display = 'none';

    // Populate existing steps or add a default one
    const steps = existingPolicy?.steps || [];
    if (steps.length > 0) {
        steps.forEach(step => {
            // Handle both channel_ids (array) and legacy channel_id (single)
            const channelId = step.channel_ids?.[0] || step.channel_id || '';
            addEscalationStep(step.delay_minutes || 15, channelId);
        });
    } else {
        // Add a default first step
        addEscalationStep(15, '');
    }

    modal.dataset.editId = isEdit ? existingPolicy.id : '';

    const title = modal.querySelector('.modal-title');
    if (title) title.textContent = isEdit ? 'Edit Escalation Policy' : 'New Escalation Policy';

    modal.style.display = 'flex';
}

async function saveEscalationPolicy() {
    const modal = document.getElementById('escalation_policy_modal');
    if (!modal) return;

    const editId = modal.dataset.editId;
    const errorDiv = document.getElementById('escalation_error');

    // Gather steps from the UI
    const stepsContainer = document.getElementById('escalation_steps_container');
    const steps = [];
    let hasError = false;

    stepsContainer?.querySelectorAll('.escalation-step').forEach((stepDiv, idx) => {
        const delay = parseInt(stepDiv.querySelector('.step-delay')?.value) || 0;
        const channelId = stepDiv.querySelector('.step-channel')?.value;

        if (!channelId) {
            hasError = true;
            if (errorDiv) {
                errorDiv.textContent = `Step ${idx + 1}: Please select a notification channel`;
                errorDiv.style.display = 'block';
            }
            return;
        }

        if (delay < 1) {
            hasError = true;
            if (errorDiv) {
                errorDiv.textContent = `Step ${idx + 1}: Delay must be at least 1 minute`;
                errorDiv.style.display = 'block';
            }
            return;
        }

        steps.push({
            delay_minutes: delay,
            channel_ids: [parseInt(channelId)]
        });
    });

    if (hasError) return;

    const payload = {
        name: document.getElementById('escalation_name')?.value || '',
        description: document.getElementById('escalation_description')?.value || '',
        steps: steps,
        enabled: document.getElementById('escalation_enabled')?.checked !== false
    };

    if (!payload.name) {
        if (errorDiv) {
            errorDiv.textContent = 'Policy name is required';
            errorDiv.style.display = 'block';
        }
        window.__pm_shared.showToast('Name is required', 'error');
        return;
    }

    if (steps.length === 0) {
        if (errorDiv) {
            errorDiv.textContent = 'At least one escalation step is required';
            errorDiv.style.display = 'block';
        }
        window.__pm_shared.showToast('At least one escalation step is required', 'error');
        return;
    }

    try {
        const url = editId ? `/api/v1/escalation-policies/${editId}` : '/api/v1/escalation-policies';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

        window.__pm_shared.showToast(editId ? 'Policy updated' : 'Policy created', 'success');
        modal.style.display = 'none';
        loadAlertRules();
    } catch (err) {
        console.error('Failed to save escalation policy:', err);
        window.__pm_shared.showToast('Failed to save escalation policy', 'error');
    }
}

function showMaintenanceWindowModal(existingWindow = null) {
    const modal = document.getElementById('maintenance_window_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const isEdit = existingWindow && existingWindow.id;

    const nameInput = form.querySelector('#maintenance_name');
    const startInput = form.querySelector('#maintenance_start');
    const endInput = form.querySelector('#maintenance_end');
    const scopeInput = form.querySelector('#maintenance_scope');
    const recurringCheck = form.querySelector('#maintenance_recurring');
    const patternInput = form.querySelector('#maintenance_pattern');

    if (nameInput) nameInput.value = existingWindow?.name || '';
    if (startInput) startInput.value = existingWindow?.start_time ? formatDatetimeLocal(existingWindow.start_time) : '';
    if (endInput) endInput.value = existingWindow?.end_time ? formatDatetimeLocal(existingWindow.end_time) : '';
    if (scopeInput) scopeInput.value = existingWindow?.scope || 'all';
    if (recurringCheck) recurringCheck.checked = existingWindow?.recurring === true;
    if (patternInput) patternInput.value = existingWindow?.recurrence_pattern || '';

    modal.dataset.editId = isEdit ? existingWindow.id : '';

    const title = modal.querySelector('.modal-title');
    if (title) title.textContent = isEdit ? 'Edit Maintenance Window' : 'New Maintenance Window';

    modal.style.display = 'flex';
}

// formatDatetimeLocal is now in utils/formatters.js

async function saveMaintenanceWindow() {
    const modal = document.getElementById('maintenance_window_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const editId = modal.dataset.editId;

    const payload = {
        name: form.querySelector('#maintenance_name')?.value || '',
        start_time: form.querySelector('#maintenance_start')?.value || '',
        end_time: form.querySelector('#maintenance_end')?.value || '',
        scope: form.querySelector('#maintenance_scope')?.value || 'all',
        recurring: form.querySelector('#maintenance_recurring')?.checked === true,
        recurrence_pattern: form.querySelector('#maintenance_pattern')?.value || ''
    };

    if (!payload.name) {
        window.__pm_shared.showToast('Name is required', 'error');
        return;
    }
    if (!payload.start_time || !payload.end_time) {
        window.__pm_shared.showToast('Start and end times are required', 'error');
        return;
    }

    try {
        const url = editId ? `/api/v1/maintenance-windows/${editId}` : '/api/v1/maintenance-windows';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

        window.__pm_shared.showToast(editId ? 'Window updated' : 'Window created', 'success');
        modal.style.display = 'none';
        loadAlertRules();
    } catch (err) {
        console.error('Failed to save maintenance window:', err);
        window.__pm_shared.showToast('Failed to save maintenance window', 'error');
    }
}

function showScheduledReportModal(existingSchedule = null) {
    const modal = document.getElementById('scheduled_report_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const isEdit = existingSchedule && existingSchedule.id;

    const nameInput = form.querySelector('#schedule_name');
    const typeSelect = form.querySelector('#schedule_report_type');
    const formatSelect = form.querySelector('#schedule_format');
    const frequencySelect = form.querySelector('#schedule_frequency');
    const emailInput = form.querySelector('#schedule_email');
    const enabledCheck = form.querySelector('#schedule_enabled');

    if (nameInput) nameInput.value = existingSchedule?.name || '';
    if (typeSelect) typeSelect.value = existingSchedule?.report_type || 'device_inventory';
    if (formatSelect) formatSelect.value = existingSchedule?.output_format || 'csv';
    if (frequencySelect) frequencySelect.value = existingSchedule?.frequency || 'weekly';
    if (emailInput) emailInput.value = existingSchedule?.delivery_email || '';
    if (enabledCheck) enabledCheck.checked = existingSchedule?.enabled !== false;

    modal.dataset.editId = isEdit ? existingSchedule.id : '';

    const title = modal.querySelector('.modal-title');
    if (title) title.textContent = isEdit ? 'Edit Scheduled Report' : 'New Scheduled Report';

    modal.style.display = 'flex';
}

async function saveScheduledReport() {
    const modal = document.getElementById('scheduled_report_modal');
    if (!modal) return;

    const form = modal.querySelector('form') || modal;
    const editId = modal.dataset.editId;

    const payload = {
        name: form.querySelector('#schedule_name')?.value || '',
        report_type: form.querySelector('#schedule_report_type')?.value || 'device_inventory',
        output_format: form.querySelector('#schedule_format')?.value || 'csv',
        frequency: form.querySelector('#schedule_frequency')?.value || 'weekly',
        delivery_email: form.querySelector('#schedule_email')?.value || '',
        enabled: form.querySelector('#schedule_enabled')?.checked !== false
    };

    if (!payload.name) {
        window.__pm_shared.showToast('Name is required', 'error');
        return;
    }

    try {
        const url = editId ? `/api/v1/report-schedules/${editId}` : '/api/v1/report-schedules';
        const method = editId ? 'PUT' : 'POST';
        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);

        window.__pm_shared.showToast(editId ? 'Schedule updated' : 'Schedule created', 'success');
        modal.style.display = 'none';
        loadAlertRules();
    } catch (err) {
        console.error('Failed to save scheduled report:', err);
        window.__pm_shared.showToast('Failed to save scheduled report', 'error');
    }
}

function initSettingsSubTabs() {
    if (settingsSubtabsInitialized) {
        return;
    }
    settingsSubtabsInitialized = true;

    document.querySelectorAll('.settings-subtab').forEach(btn => {
        btn.addEventListener('click', () => {
            const target = btn.dataset.settingsview || 'fleet';
            switchSettingsView(target);
        });
    });
}

function switchSettingsView(view, force = false) {
    // valid views: server | fleet | sso | updates
    let normalized = 'server';
    if (view === 'fleet') normalized = 'fleet';
    else if (view === 'sso') normalized = 'sso';
    else if (view === 'updates') normalized = 'updates';
    const previous = activeSettingsView;
    activeSettingsView = normalized;
    persistUIState(SERVER_UI_STATE_KEYS.SETTINGS_VIEW, normalized);

    document.querySelectorAll('.settings-subtab').forEach(btn => {
        const target = btn.dataset.settingsview || 'fleet';
        btn.classList.toggle('active', target === normalized);
    });

    document.querySelectorAll('[data-settingsview-panel]').forEach(panel => {
        const target = panel.dataset.settingsviewPanel || 'fleet';
        panel.classList.toggle('hidden', target !== normalized);
    });

    if (force || previous !== normalized) {
        ensureSettingsViewReady(normalized);
    }
}

function ensureSettingsViewReady(view) {
    if (view === 'sso') {
        // Initialize SSO admin panel lazily
        initSSOAdmin();
        refreshSSOProviders();
        return;
    }
    if (view === 'server') {
        // Placeholder: fetch and render server settings into server_settings_container
        loadServerSettings();
        loadSelfUpdateRuns();
        return;
    }
    if (view === 'updates') {
        loadSelfUpdateRuns();
        loadReleaseArtifacts();
        return;
    }
    // fleet
    initSettingsUI();
    loadReleaseArtifacts();
}

async function loadServerSettings(forceRefresh = false) {
    const container = document.getElementById('server_settings_container');
    if (!container) return;
    if (serverSettingsVM.loading && !forceRefresh) {
        return;
    }
    serverSettingsVM.loading = true;
    container.innerHTML = '<div style="color:var(--muted);">Loading server settings…</div>';
    try {
        const [settingsResp, sourcesResp] = await Promise.all([
            fetchJSON('/api/v1/server/settings'),
            fetchJSON('/api/v1/server/settings/sources').catch(err => {
                window.__pm_shared.warn('Failed to load server settings lock metadata', err);
                return null;
            })
        ]);
        const normalized = normalizeServerSettings(settingsResp || {});
        serverSettingsVM.original = cloneServerSettingsData(normalized);
        serverSettingsVM.data = cloneServerSettingsData(normalized);
        const lockedKeys = (sourcesResp && Array.isArray(sourcesResp.locked_keys)) ? sourcesResp.locked_keys : [];
        serverSettingsVM.lockedKeys = new Set(lockedKeys);
        serverSettingsVM.dirty = false;
        serverSettingsVM.restartRequired = false;
        serverSettingsVM.lastError = null;
        serverSettingsVM.statusMessage = '';
        serverSettingsVM.statusTone = 'muted';
        renderServerSettingsForm();
    } catch (err) {
        serverSettingsVM.lastError = err;
        const message = err && err.message ? err.message : err;
        container.innerHTML = `<div style="color:var(--danger);">Failed to load server settings: ${message}</div>`;
        window.__pm_shared.error('Failed to load server settings', err);
    } finally {
        serverSettingsVM.loading = false;
    }
}

function normalizeServerSettings(raw) {
    const safeStr = (val) => (val === null || val === undefined) ? '' : String(val);
    const safeBool = (val) => Boolean(val);
    const scoped = raw || {};
    const serverSection = scoped.server || {};
    const securitySection = scoped.security || {};
    const tlsSection = scoped.tls || {};
    const loggingSection = scoped.logging || {};
    const smtpSection = scoped.smtp || {};
    const notificationsSection = scoped.notifications || {};
    const databaseSection = scoped.database || {};
    const releasesSection = scoped.releases || {};
    const selfUpdateSection = scoped.self_update || {};
    return {
        meta: {
            version: safeStr(scoped.version || 'unknown'),
            config_source: safeStr(scoped.config_source || 'config.toml'),
            using_defaults: Boolean(scoped.using_defaults),
            tenancy_enabled: Boolean(scoped.tenancy_enabled),
            database_path: safeStr(databaseSection.path || ''),
        },
        server: {
            http_port: safeStr(serverSection.http_port),
            https_port: safeStr(serverSection.https_port),
            bind_address: safeStr(serverSection.bind_address || ''),
            behind_proxy: safeBool(serverSection.behind_proxy),
            proxy_use_https: safeBool(serverSection.proxy_use_https),
            auto_approve_agents: safeBool(serverSection.auto_approve_agents),
            agent_timeout_minutes: safeStr(serverSection.agent_timeout_minutes),
        },
        security: {
            rate_limit_enabled: safeBool(securitySection.rate_limit_enabled),
            rate_limit_max_attempts: safeStr(securitySection.rate_limit_max_attempts),
            rate_limit_block_minutes: safeStr(securitySection.rate_limit_block_minutes),
            rate_limit_window_minutes: safeStr(securitySection.rate_limit_window_minutes),
        },
        tls: {
            mode: safeStr(tlsSection.mode || 'self-signed') || 'self-signed',
            domain: safeStr(tlsSection.domain || ''),
            cert_path: safeStr(tlsSection.cert_path || ''),
            key_path: safeStr(tlsSection.key_path || ''),
            letsencrypt_domain: safeStr(tlsSection.letsencrypt_domain || ''),
            letsencrypt_email: safeStr(tlsSection.letsencrypt_email || ''),
            letsencrypt_cache_dir: safeStr(tlsSection.letsencrypt_cache_dir || ''),
            letsencrypt_accept_tos: safeBool(tlsSection.letsencrypt_accept_tos),
        },
        logging: {
            level: (safeStr(loggingSection.level || 'INFO') || 'INFO').toUpperCase(),
        },
        smtp: {
            enabled: safeBool(smtpSection.enabled),
            host: safeStr(smtpSection.host || ''),
            port: safeStr(smtpSection.port),
            user: safeStr(smtpSection.user || ''),
            pass: '',
            from: safeStr(smtpSection.from || ''),
            email_theme: safeStr(smtpSection.email_theme || 'auto') || 'auto',
        },
        notifications: {
            enabled: safeBool(notificationsSection.enabled),
            admin_emails: safeStr(notificationsSection.admin_emails || ''),
            notify_on_critical: safeBool(notificationsSection.notify_on_critical),
            notify_on_warning: safeBool(notificationsSection.notify_on_warning),
            daily_summary_enabled: safeBool(notificationsSection.daily_summary_enabled),
            daily_summary_time: safeStr(notificationsSection.daily_summary_time || '08:00') || '08:00',
            daily_summary_timezone: safeStr(notificationsSection.daily_summary_timezone || 'Local') || 'Local',
        },
        releases: {
            max_releases: safeStr(releasesSection.max_releases),
            poll_interval_minutes: safeStr(releasesSection.poll_interval_minutes),
        },
        self_update: {
            enabled: safeBool(selfUpdateSection.enabled),
            channel: safeStr(selfUpdateSection.channel || 'stable') || 'stable',
            max_artifacts: safeStr(selfUpdateSection.max_artifacts),
            check_interval_minutes: safeStr(selfUpdateSection.check_interval_minutes),
        },
    };
}

function cloneServerSettingsData(data) {
    return JSON.parse(JSON.stringify(data || {}));
}

function renderServerSettingsForm() {
    const container = document.getElementById('server_settings_container');
    if (!container) return;
    if (!serverSettingsVM.data) {
        container.innerHTML = '<div style="color:var(--muted);">Server settings are not available.</div>';
        return;
    }
    const activeSection = SERVER_SETTINGS_SCHEMA.some(section => section.section === serverSettingsVM.activeSection)
        ? serverSettingsVM.activeSection
        : SERVER_SETTINGS_SCHEMA[0].section;
    serverSettingsVM.activeSection = activeSection;
    const sectionsHtml = SERVER_SETTINGS_SCHEMA.map(section => renderServerSettingsSection(section, activeSection)).join('');
    const sectionNavigation = renderServerSettingsNavigation(activeSection);
    const metaCards = renderServerSettingsInfoCards();
    const lockSummary = renderServerSettingsLockSummary();
    const restartBanner = `<div id="server_settings_restart_banner" style="display:${(serverSettingsVM.restartRequired && !serverSettingsVM.dirty) ? 'flex' : 'none'};align-items:center;gap:8px;padding:8px 12px;border-radius:6px;background:rgba(255,153,0,0.15);color:var(--warn);font-size:13px;">
        <span style="font-weight:600;">Restart required</span>
        <span>Recycle the PrintMaster server service to apply TLS or network changes.</span>
    </div>`;
    container.innerHTML = `
        ${lockSummary}
        ${restartBanner}
        <div class="server-settings-layout">
            ${sectionNavigation}
            <div class="server-settings-content">${sectionsHtml}</div>
        </div>
        <div class="server-settings-meta">
            ${metaCards}
        </div>
        <div class="server-settings-actions">
            <div id="server_settings_status" role="status" aria-live="polite" style="font-size:13px;color:var(--muted);"></div>
            <div class="server-settings-action-buttons">
                <button id="server_settings_discard_btn" class="btn btn-secondary" type="button">Discard</button>
                <button id="server_settings_save_btn" class="btn btn-primary" type="button">Save changes</button>
            </div>
        </div>
    `;
    bindServerSettingsInputs(container);
    syncServerSettingsActionState();
}

function renderServerSettingsNavigation(activeSection) {
    const items = SERVER_SETTINGS_SCHEMA.map(section => {
        const active = section.section === activeSection;
        return {
            button: `<button class="server-settings-nav-item${active ? ' active' : ''}" type="button" data-server-settings-target="${section.section}" aria-controls="server_settings_section_${section.section}" aria-pressed="${active}">${escapeHtml(section.title)}</button>`,
            option: `<option value="${section.section}" ${active ? 'selected' : ''}>${escapeHtml(section.title)}</option>`,
        };
    });
    const buttons = items.map(item => item.button).join('');
    const options = items.map(item => item.option).join('');
    return `
        <div class="server-settings-navigation">
            <nav class="server-settings-nav" aria-label="Server settings categories">${buttons}</nav>
            <label class="server-settings-mobile-navigation" for="server_settings_section_select">
                <span>Settings category</span>
                <select id="server_settings_section_select">${options}</select>
            </label>
        </div>
    `;
}

function renderServerSettingsInfoCards() {
    const meta = (serverSettingsVM.data && serverSettingsVM.data.meta) || {};
    const cards = [
        { label: 'Version', value: meta.version || 'unknown' },
        { label: 'Config Source', value: meta.config_source || 'config.toml' },
        { label: 'Tenancy', value: meta.tenancy_enabled ? 'Enabled' : 'Disabled' },
        { label: 'Database Path', value: meta.database_path || '(default)' },
    ];
    return cards.map(card => `
        <div class="server-settings-meta-card">
            <div style="font-size:12px;color:var(--muted);text-transform:uppercase;letter-spacing:0.4px;">${card.label}</div>
            <div style="font-size:15px;margin-top:4px;font-family:var(--font-code,monospace);">${escapeHtml(card.value)}</div>
        </div>
    `).join('');
}

function renderServerSettingsLockSummary() {
    if (!serverSettingsVM.lockedKeys || serverSettingsVM.lockedKeys.size === 0) {
        return '';
    }
    const keys = Array.from(serverSettingsVM.lockedKeys).sort();
    const preview = keys.slice(0, 4).map(key => `<code style="background:rgba(255,255,255,0.05);padding:2px 6px;border-radius:4px;">${escapeHtml(key)}</code>`).join(' ');
    const remainder = keys.length > 4 ? ` +${keys.length - 4} more` : '';
    return `
        <div style="border:1px solid var(--border);border-radius:10px;padding:12px;margin-bottom:16px;background:rgba(255,255,255,0.02);font-size:13px;">
            <strong>Managed keys:</strong> ${preview}${remainder}
            <div style="font-size:12px;color:var(--muted);margin-top:4px;">These values come from environment overrides and cannot be edited here.</div>
        </div>
    `;
}

function renderServerSettingsSection(sectionDef, activeSection) {
    const fields = sectionDef.fields || [];
    const fieldGrid = fields.map(field => renderServerSettingsField(sectionDef.section, field)).join('');
    const title = escapeHtml(sectionDef.title || '');
    const description = sectionDef.description ? escapeHtml(sectionDef.description) : '';
    return `
        <section class="server-settings-section${sectionDef.section === activeSection ? '' : ' hidden'}" id="server_settings_section_${sectionDef.section}" data-server-settings-section-panel="${sectionDef.section}" aria-labelledby="server_settings_heading_${sectionDef.section}">
            <div class="server-settings-section-header">
                <h5 id="server_settings_heading_${sectionDef.section}">${title}</h5>
                <p>${description}</p>
            </div>
            <div class="server-settings-field-grid">
                ${fieldGrid}
            </div>
        </section>
    `;
}

function renderServerSettingsField(sectionKey, field) {
    const sectionData = (serverSettingsVM.data && serverSettingsVM.data[sectionKey]) || {};
    let value = sectionData[field.key];
    if (value === null || value === undefined) {
        value = (field.type === 'checkbox') ? false : '';
    }
    const inputId = `server_setting_${sectionKey}_${field.key}`;
    const isLocked = field.configKey && serverSettingsVM.lockedKeys.has(field.configKey);
    const disabledAttr = isLocked ? 'disabled' : '';
    const lockBadge = isLocked ? '<span style="font-size:11px;color:var(--warn);background:rgba(255,153,0,0.15);padding:2px 6px;border-radius:4px;">ENV Override</span>' : '';
    const labelText = escapeHtml(field.label || '');
    let control = '';
    if (field.type === 'checkbox') {
        const checked = value ? 'checked' : '';
        control = `
            <input data-settings-input="true" data-section="${sectionKey}" data-key="${field.key}" id="${inputId}" type="checkbox" ${checked} ${disabledAttr} />
        `;
    } else if (field.type === 'select') {
        const options = (field.options || []).map(opt => `<option value="${escapeHtml(opt.value)}" ${opt.value === value ? 'selected' : ''}>${escapeHtml(opt.label)}</option>`).join('');
        control = `
            <select data-settings-input="true" data-section="${sectionKey}" data-key="${field.key}" id="${inputId}" ${disabledAttr}>
                ${options}
            </select>
        `;
    } else {
        const typeAttr = field.type === 'password' ? 'password' : (field.type === 'number' ? 'number' : 'text');
        const minAttr = (field.type === 'number' && field.min !== undefined) ? `min="${field.min}"` : '';
        const maxAttr = (field.type === 'number' && field.max !== undefined) ? `max="${field.max}"` : '';
        const inputMode = field.type === 'number' ? 'inputmode="numeric" pattern="[0-9]*"' : '';
        control = `
            <input data-settings-input="true" data-section="${sectionKey}" data-key="${field.key}" id="${inputId}" type="${typeAttr}" ${minAttr} ${maxAttr} ${inputMode} ${disabledAttr}
                value="${escapeHtml(value)}" placeholder="${field.placeholder ? escapeHtml(field.placeholder) : ''}" autocomplete="off" data-1p-ignore data-lpignore="true" />
        `;
    }
    const helper = field.helper ? `<div style="font-size:12px;color:var(--muted);">${escapeHtml(field.helper)}</div>` : '';
    return `
        <div style="display:flex;flex-direction:column;gap:6px;${field.fullWidth ? 'grid-column:1 / -1;' : ''}">
            <div style="display:flex;align-items:center;gap:8px;font-weight:600;">
                <label for="${inputId}">${labelText}</label>
                ${lockBadge}
                ${field.required ? '<span style="font-size:11px;color:var(--muted);">*</span>' : ''}
            </div>
            ${control}
            ${helper}
        </div>
    `;
}

function bindServerSettingsInputs(container) {
    if (!container) return;
    const activateSection = section => {
        if (!SERVER_SETTINGS_SCHEMA.some(item => item.section === section)) return;
        serverSettingsVM.activeSection = section;
        container.querySelectorAll('[data-server-settings-target]').forEach(item => {
            const active = item.dataset.serverSettingsTarget === section;
            item.classList.toggle('active', active);
            item.setAttribute('aria-pressed', String(active));
        });
        container.querySelectorAll('[data-server-settings-section-panel]').forEach(panel => {
            panel.classList.toggle('hidden', panel.dataset.serverSettingsSectionPanel !== section);
        });
        const sectionSelect = container.querySelector('#server_settings_section_select');
        if (sectionSelect) sectionSelect.value = section;
    };

    container.querySelectorAll('[data-server-settings-target]').forEach(button => {
        button.addEventListener('click', () => activateSection(button.dataset.serverSettingsTarget));
    });
    const sectionSelect = container.querySelector('#server_settings_section_select');
    if (sectionSelect) sectionSelect.addEventListener('change', () => activateSection(sectionSelect.value));
    container.querySelectorAll('[data-settings-input="true"]').forEach(input => {
        const section = input.dataset.section;
        const key = input.dataset.key;
        if (!section || !key) {
            return;
        }
        if (input.type === 'checkbox') {
            input.addEventListener('change', () => handleServerSettingsInput(section, key, input.checked));
        } else if (input.tagName === 'SELECT') {
            input.addEventListener('change', () => handleServerSettingsInput(section, key, input.value));
        } else {
            input.addEventListener('input', () => handleServerSettingsInput(section, key, input.value));
        }
    });
    const saveBtn = container.querySelector('#server_settings_save_btn');
    const discardBtn = container.querySelector('#server_settings_discard_btn');
    if (saveBtn) {
        saveBtn.addEventListener('click', (e) => {
            e.preventDefault();
            saveServerSettings();
        });
    }
    if (discardBtn) {
        discardBtn.addEventListener('click', (e) => {
            e.preventDefault();
            discardServerSettingsChanges();
        });
    }
}

function handleServerSettingsInput(section, key, value) {
    if (!serverSettingsVM.data || !serverSettingsVM.data[section]) {
        return;
    }
    serverSettingsVM.data[section][key] = value;
    serverSettingsVM.dirty = true;
    serverSettingsVM.statusMessage = 'Unsaved changes';
    serverSettingsVM.statusTone = 'warn';
    serverSettingsVM.lastError = null;
    syncServerSettingsActionState();
}

function syncServerSettingsActionState() {
    const saveBtn = document.getElementById('server_settings_save_btn');
    const discardBtn = document.getElementById('server_settings_discard_btn');
    const statusEl = document.getElementById('server_settings_status');
    const restartBanner = document.getElementById('server_settings_restart_banner');
    if (saveBtn) {
        saveBtn.disabled = serverSettingsVM.saving || !serverSettingsVM.dirty;
    }
    if (discardBtn) {
        discardBtn.disabled = serverSettingsVM.saving || !serverSettingsVM.dirty;
    }
    if (restartBanner) {
        restartBanner.style.display = (serverSettingsVM.restartRequired && !serverSettingsVM.dirty) ? 'flex' : 'none';
    }
    if (statusEl) {
        let message = serverSettingsVM.statusMessage || (serverSettingsVM.dirty ? 'Unsaved changes' : '');
        let color = 'var(--muted)';
        if (serverSettingsVM.saving) {
            message = 'Saving changes…';
            color = 'var(--highlight)';
        } else if (serverSettingsVM.lastError) {
            message = 'Save failed. Check logs for details.';
            color = 'var(--danger)';
        } else if (serverSettingsVM.dirty) {
            color = 'var(--warn)';
        } else if (serverSettingsVM.restartRequired) {
            color = 'var(--warn)';
            message = 'Changes saved. Restart required to apply network/TLS settings.';
        }
        statusEl.textContent = message;
        statusEl.style.color = color;
    }
}

function validateServerSettingsData() {
    if (!serverSettingsVM.data) {
        return { ok: false, message: 'Settings payload not ready.' };
    }
    for (const section of SERVER_SETTINGS_SCHEMA) {
        const dataSection = serverSettingsVM.data[section.section] || {};
        for (const field of section.fields || []) {
            const value = dataSection[field.key];
            if (field.required && field.type !== 'checkbox') {
                if (value === undefined || value === null || String(value).trim() === '') {
                    return { ok: false, message: `${field.label} is required.` };
                }
            }
            if (field.type === 'number' && value !== '' && value !== undefined) {
                if (isNaN(Number(value))) {
                    return { ok: false, message: `${field.label} must be a number.` };
                }
            }
        }
    }
    const tls = serverSettingsVM.data.tls || {};
    if (tls.mode === 'custom') {
        if (!tls.cert_path || !tls.key_path) {
            return { ok: false, message: 'Custom TLS mode requires both certificate and key paths.' };
        }
    }
    if (tls.mode === 'letsencrypt') {
        if (!tls.letsencrypt_domain || !tls.letsencrypt_email) {
            return { ok: false, message: 'Let\'s Encrypt mode requires domain and email.' };
        }
        if (!tls.letsencrypt_accept_tos) {
            return { ok: false, message: 'You must accept the Let\'s Encrypt terms of service.' };
        }
    }
    const releases = serverSettingsVM.data.releases || {};
    if (!releases.max_releases || isNaN(Number(releases.max_releases)) || Number(releases.max_releases) <= 0) {
        return { ok: false, message: 'Release intake max releases must be a positive number.' };
    }
    if (!releases.poll_interval_minutes || isNaN(Number(releases.poll_interval_minutes)) || Number(releases.poll_interval_minutes) <= 0) {
        return { ok: false, message: 'Release sync interval must be a positive number of minutes.' };
    }
    const selfUpdate = serverSettingsVM.data.self_update || {};
    if (!selfUpdate.channel || String(selfUpdate.channel).trim() === '') {
        return { ok: false, message: 'Self-update channel cannot be empty.' };
    }
    if (!selfUpdate.max_artifacts || isNaN(Number(selfUpdate.max_artifacts)) || Number(selfUpdate.max_artifacts) <= 0) {
        return { ok: false, message: 'Self-update max artifacts must be a positive number.' };
    }
    if (!selfUpdate.check_interval_minutes || isNaN(Number(selfUpdate.check_interval_minutes)) || Number(selfUpdate.check_interval_minutes) <= 0) {
        return { ok: false, message: 'Self-update check interval must be a positive number of minutes.' };
    }
    return { ok: true };
}

function buildServerSettingsPayload() {
    if (!serverSettingsVM.data) {
        return null;
    }
    const data = serverSettingsVM.data;
    const lockedKeys = serverSettingsVM.lockedKeys || new Set();
    // Helper to check if a config key is locked by environment variable
    const isLocked = (configKey) => lockedKeys.has(configKey);
    const parseNumber = (val) => {
        if (val === undefined || val === null || String(val).trim() === '') {
            return undefined;
        }
        const parsed = parseInt(val, 10);
        return Number.isNaN(parsed) ? undefined : parsed;
    };
    const pickString = (val) => {
        if (val === undefined || val === null) {
            return undefined;
        }
        return String(val);
    };
    const payload = {
        server: {
            http_port: isLocked('server.http_port') ? undefined : parseNumber(data.server.http_port),
            https_port: isLocked('server.https_port') ? undefined : parseNumber(data.server.https_port),
            bind_address: isLocked('server.bind_address') ? undefined : pickString(data.server.bind_address),
            behind_proxy: isLocked('server.behind_proxy') ? undefined : Boolean(data.server.behind_proxy),
            proxy_use_https: isLocked('server.proxy_use_https') ? undefined : Boolean(data.server.proxy_use_https),
            auto_approve_agents: isLocked('server.auto_approve_agents') ? undefined : Boolean(data.server.auto_approve_agents),
            agent_timeout_minutes: isLocked('server.agent_timeout_minutes') ? undefined : parseNumber(data.server.agent_timeout_minutes),
        },
        security: {
            rate_limit_enabled: isLocked('security.rate_limit_enabled') ? undefined : Boolean(data.security.rate_limit_enabled),
            rate_limit_max_attempts: isLocked('security.rate_limit_max_attempts') ? undefined : parseNumber(data.security.rate_limit_max_attempts),
            rate_limit_block_minutes: isLocked('security.rate_limit_block_minutes') ? undefined : parseNumber(data.security.rate_limit_block_minutes),
            rate_limit_window_minutes: isLocked('security.rate_limit_window_minutes') ? undefined : parseNumber(data.security.rate_limit_window_minutes),
        },
        tls: {
            mode: isLocked('tls.mode') ? undefined : (pickString(data.tls.mode) || 'self-signed'),
            domain: isLocked('tls.domain') ? undefined : (pickString(data.tls.domain) || ''),
            cert_path: isLocked('tls.cert_path') ? undefined : pickString(data.tls.cert_path),
            key_path: isLocked('tls.key_path') ? undefined : pickString(data.tls.key_path),
        },
        logging: {
            level: isLocked('logging.level') ? undefined : (data.logging.level || 'INFO'),
        },
        smtp: {
            enabled: isLocked('smtp.enabled') ? undefined : Boolean(data.smtp.enabled),
            host: isLocked('smtp.host') ? undefined : (pickString(data.smtp.host) || ''),
            port: isLocked('smtp.port') ? undefined : parseNumber(data.smtp.port),
            user: isLocked('smtp.user') ? undefined : (pickString(data.smtp.user) || ''),
            from: isLocked('smtp.from') ? undefined : (pickString(data.smtp.from) || ''),
        },
        releases: {
            max_releases: isLocked('releases.max_releases') ? undefined : parseNumber(data.releases.max_releases),
            poll_interval_minutes: isLocked('releases.poll_interval_minutes') ? undefined : parseNumber(data.releases.poll_interval_minutes),
            retention_versions: isLocked('releases.retention_versions') ? undefined : parseNumber(data.releases.retention_versions),
        },
        self_update: {
            enabled: isLocked('self_update.enabled') ? undefined : Boolean(data.self_update.enabled),
            channel: isLocked('self_update.channel') ? undefined : pickString(data.self_update.channel),
            max_artifacts: isLocked('self_update.max_artifacts') ? undefined : parseNumber(data.self_update.max_artifacts),
            check_interval_minutes: isLocked('self_update.check_interval_minutes') ? undefined : parseNumber(data.self_update.check_interval_minutes),
        },
        notifications: {
            enabled: isLocked('notifications.enabled') ? undefined : Boolean(data.notifications.enabled),
            admin_emails: isLocked('notifications.admin_emails') ? undefined : (pickString(data.notifications.admin_emails) || ''),
            notify_on_critical: isLocked('notifications.notify_on_critical') ? undefined : Boolean(data.notifications.notify_on_critical),
            notify_on_warning: isLocked('notifications.notify_on_warning') ? undefined : Boolean(data.notifications.notify_on_warning),
            daily_summary_enabled: isLocked('notifications.daily_summary_enabled') ? undefined : Boolean(data.notifications.daily_summary_enabled),
            daily_summary_time: isLocked('notifications.daily_summary_time') ? undefined : (pickString(data.notifications.daily_summary_time) || '08:00'),
            daily_summary_timezone: isLocked('notifications.daily_summary_timezone') ? undefined : (pickString(data.notifications.daily_summary_timezone) || 'Local'),
        },
    };
    if (payload.tls.mode === 'letsencrypt') {
        payload.tls.letsencrypt = {
            domain: isLocked('tls.letsencrypt.domain') ? undefined : (pickString(data.tls.letsencrypt_domain) || ''),
            email: isLocked('tls.letsencrypt.email') ? undefined : (pickString(data.tls.letsencrypt_email) || ''),
            cache_dir: isLocked('tls.letsencrypt.cache_dir') ? undefined : pickString(data.tls.letsencrypt_cache_dir),
            accept_tos: isLocked('tls.letsencrypt.accept_tos') ? undefined : Boolean(data.tls.letsencrypt_accept_tos),
        };
    }
    if (data.smtp.pass && data.smtp.pass.trim() !== '' && !isLocked('smtp.pass')) {
        payload.smtp.pass = data.smtp.pass;
    }
    if (payload.smtp.port === undefined) {
        delete payload.smtp.port;
    }
    Object.keys(payload.releases).forEach(key => {
        if (payload.releases[key] === undefined) {
            delete payload.releases[key];
        }
    });
    Object.keys(payload.self_update).forEach(key => {
        if (payload.self_update[key] === undefined) {
            delete payload.self_update[key];
        }
    });
    Object.keys(payload.notifications).forEach(key => {
        if (payload.notifications[key] === undefined) {
            delete payload.notifications[key];
        }
    });
    Object.keys(payload.server).forEach(key => {
        if (payload.server[key] === undefined) {
            delete payload.server[key];
        }
    });
    Object.keys(payload.security).forEach(key => {
        if (payload.security[key] === undefined) {
            delete payload.security[key];
        }
    });
    Object.keys(payload.tls).forEach(key => {
        if (payload.tls[key] === undefined) {
            delete payload.tls[key];
        }
    });
    if (payload.tls.letsencrypt) {
        Object.keys(payload.tls.letsencrypt).forEach(key => {
            if (payload.tls.letsencrypt[key] === undefined) {
                delete payload.tls.letsencrypt[key];
            }
        });
    }
    return payload;
}

async function saveServerSettings() {
    if (!serverSettingsVM.data || serverSettingsVM.saving || !serverSettingsVM.dirty) {
        return;
    }
    const validation = validateServerSettingsData();
    if (!validation.ok) {
        window.__pm_shared.showToast(validation.message, 'warn');
        serverSettingsVM.statusMessage = validation.message;
        serverSettingsVM.statusTone = 'warn';
        syncServerSettingsActionState();
        return;
    }
    const payload = buildServerSettingsPayload();
    if (!payload) {
        window.__pm_shared.showToast('Settings payload was empty.', 'warn');
        return;
    }
    serverSettingsVM.saving = true;
    serverSettingsVM.statusMessage = 'Saving changes…';
    serverSettingsVM.statusTone = 'info';
    serverSettingsVM.lastError = null;
    syncServerSettingsActionState();
    try {
        const resp = await fetchJSON('/api/v1/server/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        const nextState = normalizeServerSettings((resp && resp.settings) || {});
        serverSettingsVM.original = cloneServerSettingsData(nextState);
        serverSettingsVM.data = cloneServerSettingsData(nextState);
        serverSettingsVM.dirty = false;
        serverSettingsVM.restartRequired = Boolean(resp && resp.restart_required);
        serverSettingsVM.statusMessage = serverSettingsVM.restartRequired ? 'Saved. Restart required for some settings.' : 'Settings saved successfully.';
        serverSettingsVM.statusTone = serverSettingsVM.restartRequired ? 'warn' : 'success';
        window.__pm_shared.showToast('Server settings updated.', 'success');
        renderServerSettingsForm();
    } catch (err) {
        serverSettingsVM.lastError = err;
        serverSettingsVM.statusMessage = err && err.message ? err.message : 'Failed to save settings.';
        serverSettingsVM.statusTone = 'error';
        window.__pm_shared.error('Saving server settings failed', err);
        syncServerSettingsActionState();
        return;
    } finally {
        serverSettingsVM.saving = false;
        syncServerSettingsActionState();
    }
}

function discardServerSettingsChanges() {
    if (!serverSettingsVM.dirty || !serverSettingsVM.original) {
        return;
    }
    serverSettingsVM.data = cloneServerSettingsData(serverSettingsVM.original);
    serverSettingsVM.dirty = false;
    serverSettingsVM.statusMessage = 'Changes discarded.';
    serverSettingsVM.statusTone = 'muted';
    renderServerSettingsForm();
    window.__pm_shared.showToast('Server settings reverted.', 'info');
}

function switchTab(targetTab, updateHash = true) {
    if (targetTab !== 'devices' && devicesLoader) {
        cleanupDevicesInfiniteScroll();
        devicesDemand = [];
        devicesLoader.setDemand([]);
        devicesLoader.clearMetricQueue();
        clearTimeout(devicesRefreshTimer);
    }
    // Hide all tabs
    document.querySelectorAll('[data-tab]').forEach(tab => {
        tab.classList.add('hidden');
    });

    // Remove active class from all tab buttons
    document.querySelectorAll('.tab').forEach(tab => {
        tab.classList.remove('active');
    });

    // Show target tab
    const target = document.querySelector(`[data-tab="${targetTab}"]`);
    if (target) {
        target.classList.remove('hidden');
    }

    // Add active class to clicked tab buttons
    document.querySelectorAll(`.tab[data-target="${targetTab}"]`).forEach(tab => {
        tab.classList.add('active');
    });

    // Update mobile bottom tabs active state
    updateMobileBottomTabsActiveState(targetTab);

    // Update mobile menu label
    const label = document.getElementById('current_tab_label');
    if (label) {
        label.textContent = 'Menu - ' + getTabLabel(targetTab);
    }

    // Update URL hash for browser history (enables back button)
    if (updateHash && targetTab) {
        const newHash = '#' + targetTab;
        if (window.location.hash !== newHash) {
            history.pushState({ tab: targetTab }, '', newHash);
        }
    }

    // Load data for specific tabs
    if (targetTab === 'dashboard') {
        initDashboard();
        loadDashboard();
    } else if (targetTab === 'agents') {
        initAgentsUI();
        initPendingRegistrationsUI();
        loadAgents();
        loadPendingRegistrations();
    } else if (targetTab === 'devices') {
        initDevicesUI();
        loadDevices();
    } else if (targetTab === 'metrics') {
        loadMetrics();
    } else if (targetTab === 'settings') {
        initSettingsSubTabs();
        switchSettingsView(activeSettingsView, true);
    } else if (targetTab === 'logs') {
        initLogSubTabs();
        switchLogView(activeLogView || 'system');
    } else if (targetTab === 'alerts') {
        initAlertsTab();
    } else if (targetTab === 'admin') {
        initAdminTab();
    }

    if (targetTab && isTabSelectable(targetTab)) {
        persistUIState(SERVER_UI_STATE_KEYS.ACTIVE_TAB, targetTab);
    }
}

function isTabSelectable(targetTab) {
    if (!targetTab) {
        return false;
    }
    const panel = document.querySelector(`[data-tab="${targetTab}"]`);
    if (!panel) {
        return false;
    }
    const buttons = Array.from(document.querySelectorAll(`.tab[data-target="${targetTab}"]`));
    if (buttons.length === 0) {
        return true;
    }
    return buttons.some(btn => btn.offsetParent !== null);
}

function getTabFromHash() {
    const hash = window.location.hash;
    if (!hash || hash.length < 2) {
        return null;
    }
    return hash.substring(1); // Remove the '#'
}

function restorePreferredTab() {
    // First priority: URL hash (enables back button navigation)
    const hashTab = getTabFromHash();
    if (hashTab && isTabSelectable(hashTab)) {
        switchTab(hashTab, false); // Don't push to history since we're restoring
        return;
    }

    // Second priority: localStorage saved tab
    const stored = getPersistedUIState(SERVER_UI_STATE_KEYS.ACTIVE_TAB, null);
    if (stored && isTabSelectable(stored)) {
        switchTab(stored);
        return;
    }

    // Default: dashboard
    switchTab('dashboard');
}

// Handle browser back/forward button navigation
function initHashNavigation() {
    window.addEventListener('popstate', (event) => {
        let targetTab = null;

        // Try to get tab from state first (more reliable)
        if (event.state && event.state.tab) {
            targetTab = event.state.tab;
        } else {
            // Fall back to hash
            targetTab = getTabFromHash();
        }

        if (targetTab && isTabSelectable(targetTab)) {
            switchTab(targetTab, false); // Don't push new history entry
        }
    });
}

// ---------------------------------------------------------------------------
// Dashboard - Fleet Hierarchy Tree View
// ---------------------------------------------------------------------------
let dashboardInitialized = false;
let dashboardData = null;
let dashboardExpandedNodes = new Set();
let dashboardSearchQuery = '';
let dashboardFilters = {
    showTenants: true,
    showSites: true,
    showAgents: true,
    showDevices: true,
    agentStatus: new Set(['active', 'degraded', 'offline']),
    supplyBand: new Set(['critical', 'low', 'medium', 'high', 'unknown']),
    deviceStatus: new Set(DEVICE_STATUS_KEYS)
};

function initDashboard() {
    if (dashboardInitialized) return;
    dashboardInitialized = true;

    initFilterDrawer('dashboard_sidebar', 'dashboard_filters_open', 'dashboard_sidebar_toggle');

    // Search input
    const searchInput = document.getElementById('dashboard_search');
    if (searchInput) {
        let searchTimeout;
        searchInput.addEventListener('input', () => {
            clearTimeout(searchTimeout);
            searchTimeout = setTimeout(() => {
                dashboardSearchQuery = searchInput.value.toLowerCase().trim();
                renderDashboardTree();
                updateDashboardSearchResults();
            }, 150);
        });
    }

    // Level toggles
    ['tenants', 'agents', 'devices'].forEach(level => {
        const checkbox = document.getElementById(`dashboard_show_${level}`);
        if (checkbox) {
            checkbox.addEventListener('change', () => {
                dashboardFilters[`show${level.charAt(0).toUpperCase() + level.slice(1)}`] = checkbox.checked;
                renderDashboardTree();
            });
        }
    });

    // Pill toggle filters
    initDashboardPillFilter('dashboard_agent_status_filter', 'agentStatus');
    initDashboardPillFilter('dashboard_supply_filter', 'supplyBand');
    initDashboardPillFilter('dashboard_device_status_filter', 'deviceStatus');

    // Reset filters
    const resetBtn = document.getElementById('dashboard_reset_filters');
    if (resetBtn) {
        resetBtn.addEventListener('click', resetDashboardFilters);
    }

    // Tree controls
    const expandAllBtn = document.getElementById('dashboard_expand_all');
    if (expandAllBtn) {
        expandAllBtn.addEventListener('click', () => {
            expandAllDashboardNodes();
            renderDashboardTree();
        });
    }

    const collapseAllBtn = document.getElementById('dashboard_collapse_all');
    if (collapseAllBtn) {
        collapseAllBtn.addEventListener('click', () => {
            dashboardExpandedNodes.clear();
            renderDashboardTree();
        });
    }

    const refreshBtn = document.getElementById('dashboard_refresh');
    if (refreshBtn) {
        refreshBtn.addEventListener('click', () => loadDashboard());
    }
}

function initDashboardPillFilter(containerId, filterKey) {
    const container = document.getElementById(containerId);
    if (!container) return;

    container.querySelectorAll('.pill').forEach(pill => {
        pill.addEventListener('click', () => {
            pill.classList.toggle('active');
            const value = pill.dataset.status || pill.dataset.band;
            if (pill.classList.contains('active')) {
                dashboardFilters[filterKey].add(value);
            } else {
                dashboardFilters[filterKey].delete(value);
            }
            renderDashboardTree();
            updateDashboardActiveFilters();
        });
    });
}

function resetDashboardFilters() {
    dashboardSearchQuery = '';
    const searchInput = document.getElementById('dashboard_search');
    if (searchInput) searchInput.value = '';

    dashboardFilters = {
        showTenants: true,
        showSites: true,
        showAgents: true,
        showDevices: true,
        agentStatus: new Set(['active', 'degraded', 'offline']),
        supplyBand: new Set(['critical', 'low', 'medium', 'high', 'unknown']),
        deviceStatus: new Set(DEVICE_STATUS_KEYS)
    };

    // Update checkboxes
    ['tenants', 'sites', 'agents', 'devices'].forEach(level => {
        const checkbox = document.getElementById(`dashboard_show_${level}`);
        if (checkbox) checkbox.checked = true;
    });

    // Update pills
    document.querySelectorAll('#dashboard_agent_status_filter .pill, #dashboard_supply_filter .pill, #dashboard_device_status_filter .pill').forEach(pill => {
        pill.classList.add('active');
    });

    updateDashboardActiveFilters();
    updateDashboardSearchResults();
    renderDashboardTree();
}

function updateDashboardActiveFilters() {
    const container = document.getElementById('dashboard_active_filters');
    if (!container) return;

    const chips = [];

    // Check if any agent status is filtered
    if (dashboardFilters.agentStatus.size < 3) {
        const missing = ['active', 'degraded', 'offline'].filter(s => !dashboardFilters.agentStatus.has(s));
        missing.forEach(s => {
            chips.push(`<span class="filter-chip">Hiding ${s} agents <button data-filter="agentStatus" data-value="${s}">×</button></span>`);
        });
    }

    // Check if any supply band is filtered
    if (dashboardFilters.supplyBand.size < 5) {
        const missing = ['critical', 'low', 'medium', 'high', 'unknown'].filter(s => !dashboardFilters.supplyBand.has(s));
        missing.forEach(s => {
            chips.push(`<span class="filter-chip">Hiding ${s} supplies <button data-filter="supplyBand" data-value="${s}">×</button></span>`);
        });
    }

    container.innerHTML = chips.join('');

    // Add click handlers for chip removal
    container.querySelectorAll('button').forEach(btn => {
        btn.addEventListener('click', () => {
            const filterKey = btn.dataset.filter;
            const value = btn.dataset.value;
            dashboardFilters[filterKey].add(value);
            // Update corresponding pill
            const pill = document.querySelector(`[data-status="${value}"], [data-band="${value}"]`);
            if (pill) pill.classList.add('active');
            updateDashboardActiveFilters();
            renderDashboardTree();
        });
    });
}

function updateDashboardSearchResults() {
    const container = document.getElementById('dashboard_search_results');
    if (!container) return;

    if (!dashboardSearchQuery || !dashboardData) {
        container.classList.add('hidden');
        return;
    }

    const matches = countDashboardMatches();
    if (matches.total > 0) {
        container.classList.remove('hidden');
        const parts = [];
        if (matches.tenants > 0) parts.push(`${matches.tenants} tenant${matches.tenants !== 1 ? 's' : ''}`);
        if (matches.agents > 0) parts.push(`${matches.agents} agent${matches.agents !== 1 ? 's' : ''}`);
        if (matches.devices > 0) parts.push(`${matches.devices} device${matches.devices !== 1 ? 's' : ''}`);
        container.innerHTML = `<span style="color:var(--success);">Found ${parts.join(', ')}</span>`;
    } else {
        container.classList.remove('hidden');
        container.innerHTML = `<span class="muted-text">No matches found</span>`;
    }
}

function countDashboardMatches() {
    const result = { tenants: 0, agents: 0, devices: 0, total: 0 };
    if (!dashboardData || !dashboardSearchQuery) return result;

    for (const tenant of dashboardData.tenants || []) {
        if (matchesSearch(tenant.name) || matchesSearch(tenant.id)) {
            result.tenants++;
        }
        for (const agent of tenant.agents || []) {
            if (matchesSearch(agent.name) || matchesSearch(agent.agent_id)) {
                result.agents++;
            }
            for (const device of agent.devices || []) {
                if (matchesSearch(device.serial) || matchesSearch(device.manufacturer) ||
                    matchesSearch(device.model) || matchesSearch(device.ip) || matchesSearch(device.location)) {
                    result.devices++;
                }
            }
        }
    }

    result.total = result.tenants + result.agents + result.devices;
    return result;
}

function matchesSearch(value) {
    if (!dashboardSearchQuery || !value) return false;
    return String(value).toLowerCase().includes(dashboardSearchQuery);
}

function expandAllDashboardNodes() {
    if (!dashboardData) return;
    for (const tenant of dashboardData.tenants || []) {
        dashboardExpandedNodes.add(`tenant-${tenant.id}`);
        // Expand sites and their agents
        for (const site of tenant.sites || []) {
            dashboardExpandedNodes.add(`site-${site.id}`);
            for (const agent of site.agents || []) {
                dashboardExpandedNodes.add(`agent-${agent.agent_id}`);
            }
        }
        // Expand unassigned agents (directly under tenant)
        for (const agent of tenant.agents || []) {
            dashboardExpandedNodes.add(`agent-${agent.agent_id}`);
        }
    }
}

async function loadDashboard() {
    const container = document.getElementById('dashboard_tree');
    const refreshBtn = document.getElementById('dashboard_refresh');

    if (refreshBtn) {
        refreshBtn.classList.add('refreshing');
    }

    if (container) {
        container.innerHTML = `
            <div class="dashboard-loading">
                <div class="loading-spinner"></div>
                <span>Loading fleet hierarchy…</span>
            </div>
        `;
    }

    try {
        const resp = await fetchJSON('/api/v1/dashboard/tree');
        dashboardData = resp;
        renderDashboardSummary();
        renderDashboardTree();
        updateDashboardSearchResults();
    } catch (err) {
        console.error('Failed to load dashboard:', err);
        if (container) {
            container.innerHTML = `
                <div class="dashboard-empty">
                    <div class="dashboard-empty-icon">⚠️</div>
                    <div>Failed to load dashboard data</div>
                    <div class="muted-text">${escapeHtml(err.message || err)}</div>
                </div>
            `;
        }
    } finally {
        if (refreshBtn) {
            refreshBtn.classList.remove('refreshing');
        }
    }
}

function renderDashboardSummary() {
    if (!dashboardData || !dashboardData.summary) return;

    const s = dashboardData.summary;
    const setVal = (id, val) => {
        const el = document.getElementById(id);
        if (el) el.textContent = typeof val === 'number' ? val.toLocaleString() : val;
    };

    setVal('dashboard_tenant_count', s.tenant_count || 0);
    setVal('dashboard_site_count', s.site_count || 0);
    setVal('dashboard_agent_count', s.agent_count || 0);
    setVal('dashboard_device_count', s.device_count || 0);
    setVal('dashboard_critical_count', s.critical_supplies || 0);
    setVal('dashboard_low_count', s.low_supplies || 0);
    setVal('dashboard_pages_count', s.total_pages || 0);

    // Highlight critical card if there are critical supplies
    const criticalCard = document.getElementById('dashboard_critical_card');
    if (criticalCard) {
        criticalCard.classList.toggle('warning', (s.critical_supplies || 0) > 0);
    }
}

function renderDashboardTree() {
    const container = document.getElementById('dashboard_tree');
    if (!container || !dashboardData) return;

    const tenants = dashboardData.tenants || [];

    if (tenants.length === 0) {
        container.innerHTML = `
            <div class="dashboard-empty">
                <div class="dashboard-empty-icon">📭</div>
                <div>No tenants or agents found</div>
                <div class="muted-text">Add an agent to get started</div>
            </div>
        `;
        return;
    }

    // Filter and build tree HTML
    const html = buildDashboardTreeHTML(tenants);
    container.innerHTML = html || `
        <div class="dashboard-empty">
            <div class="dashboard-empty-icon">🔍</div>
            <div>No matches found</div>
            <div class="muted-text">Try adjusting your filters</div>
        </div>
    `;

    // Attach event listeners
    attachDashboardTreeListeners(container);
}

function buildDashboardTreeHTML(tenants) {
    let html = '<ul class="dashboard-tree">';
    let hasContent = false;

    for (const tenant of tenants) {
        const tenantNodeId = `tenant-${tenant.id}`;
        const tenantMatches = matchesSearch(tenant.name) || matchesSearch(tenant.id);

        // Collect all agents (from sites + unassigned)
        const allTenantAgents = [];
        for (const site of (tenant.sites || [])) {
            for (const agent of (site.agents || [])) {
                allTenantAgents.push(agent);
            }
        }
        for (const agent of (tenant.agents || [])) {
            allTenantAgents.push(agent);
        }

        // Filter agents by status
        const filteredAgents = allTenantAgents.filter(agent => {
            return dashboardFilters.agentStatus.has(agent.status);
        });

        // Check if any content matches search (for auto-expand)
        const hasMatchingContent = tenantMatches ||
            (tenant.sites || []).some(site => matchesSearch(site.name) || matchesSearch(site.description)) ||
            filteredAgents.some(agent => {
                if (matchesSearch(agent.name) || matchesSearch(agent.agent_id)) return true;
                return (agent.devices || []).some(d =>
                    matchesSearch(d.serial) || matchesSearch(d.manufacturer) ||
                    matchesSearch(d.model) || matchesSearch(d.ip) || matchesSearch(d.location)
                );
            });

        // Skip tenant if no matching content when searching
        if (dashboardSearchQuery && !hasMatchingContent) continue;

        // Auto-expand when searching
        if (dashboardSearchQuery && hasMatchingContent && !tenantMatches) {
            dashboardExpandedNodes.add(tenantNodeId);
        }

        if (dashboardFilters.showTenants) {
            hasContent = true;
            html += buildTenantNodeHTML(tenant, tenantMatches);
        } else if (dashboardFilters.showSites) {
            // Show sites directly without tenant wrapper
            for (const site of (tenant.sites || [])) {
                const siteHTML = buildSiteNodeHTML(site, tenant.id);
                if (siteHTML) {
                    hasContent = true;
                    html += siteHTML;
                }
            }
            // Also show unassigned agents
            for (const agent of (tenant.agents || [])) {
                if (!dashboardFilters.agentStatus.has(agent.status)) continue;
                const agentHTML = buildAgentNodeHTML(agent, tenant.id, null);
                if (agentHTML) {
                    hasContent = true;
                    html += agentHTML;
                }
            }
        } else {
            // Show agents directly without tenant/site wrapper
            for (const agent of filteredAgents) {
                const agentHTML = buildAgentNodeHTML(agent, tenant.id, null);
                if (agentHTML) {
                    hasContent = true;
                    html += agentHTML;
                }
            }
        }
    }

    html += '</ul>';
    return hasContent ? html : '';
}

function buildTenantNodeHTML(tenant, isMatch) {
    const nodeId = `tenant-${tenant.id}`;
    const isExpanded = dashboardExpandedNodes.has(nodeId);
    const sites = tenant.sites || [];
    const unassignedAgents = (tenant.agents || []).filter(a => dashboardFilters.agentStatus.has(a.status));
    const hasChildren = sites.length > 0 || unassignedAgents.length > 0;
    const m = tenant.metrics || {};

    let html = `<li class="dashboard-tree-node" data-node-id="${nodeId}">`;
    html += `<div class="dashboard-tree-row${isMatch ? ' match' : ''}" data-type="tenant" data-id="${tenant.id}">`;
    html += `<button class="dashboard-tree-toggle${isExpanded ? ' expanded' : ''}${hasChildren ? '' : ' no-children'}" aria-expanded="${isExpanded}">▶</button>`;
    html += `<span class="dashboard-tree-icon tenant">🏢</span>`;
    html += `<div class="dashboard-tree-content">`;
    html += `<span class="dashboard-tree-name">${highlightMatch(escapeHtml(tenant.name))}</span>`;
    html += `</div>`;
    html += `<div class="dashboard-tree-metrics">`;
    if (m.site_count > 0) {
        html += `<span class="dashboard-tree-metric" title="Sites">${m.site_count} sites</span>`;
    }
    html += `<span class="dashboard-tree-metric" title="Agents">${m.agent_count || 0} agents</span>`;
    html += `<span class="dashboard-tree-metric" title="Devices">${m.device_count || 0} devices</span>`;
    if (m.critical_supplies > 0) {
        html += `<span class="dashboard-tree-metric critical" title="Critical supplies">⚠️ ${m.critical_supplies}</span>`;
    }
    html += `</div>`;
    html += `</div>`;

    // Children (sites + unassigned agents)
    if (hasChildren) {
        html += `<ul class="dashboard-tree-children${isExpanded ? '' : ' collapsed'}">`;

        // Sites first
        if (dashboardFilters.showSites) {
            for (const site of sites) {
                const siteHTML = buildSiteNodeHTML(site, tenant.id);
                if (siteHTML) html += siteHTML;
            }
        } else if (dashboardFilters.showAgents) {
            // If sites hidden but agents shown, show agents from sites directly
            for (const site of sites) {
                for (const agent of (site.agents || [])) {
                    if (!dashboardFilters.agentStatus.has(agent.status)) continue;
                    const agentHTML = buildAgentNodeHTML(agent, tenant.id, site.id);
                    if (agentHTML) html += agentHTML;
                }
            }
        }

        // Unassigned agents (shown at tenant level)
        if (dashboardFilters.showAgents) {
            for (const agent of unassignedAgents) {
                const agentHTML = buildAgentNodeHTML(agent, tenant.id, null);
                if (agentHTML) html += agentHTML;
            }
        }

        html += `</ul>`;
    }

    html += `</li>`;
    return html;
}

function buildSiteNodeHTML(site, tenantId) {
    const nodeId = `site-${site.id}`;
    const isExpanded = dashboardExpandedNodes.has(nodeId);
    const siteMatches = matchesSearch(site.name) || matchesSearch(site.description) || matchesSearch(site.address);

    // Filter agents
    const filteredAgents = (site.agents || []).filter(agent => {
        return dashboardFilters.agentStatus.has(agent.status);
    });

    // Check if any agent matches search
    const hasMatchingAgent = filteredAgents.some(agent => {
        if (matchesSearch(agent.name) || matchesSearch(agent.agent_id)) return true;
        return (agent.devices || []).some(d =>
            matchesSearch(d.serial) || matchesSearch(d.manufacturer) ||
            matchesSearch(d.model) || matchesSearch(d.ip) || matchesSearch(d.location)
        );
    });

    // Skip if searching and no matches
    if (dashboardSearchQuery && !siteMatches && !hasMatchingAgent) return '';

    // Auto-expand when searching
    if (dashboardSearchQuery && hasMatchingAgent && !siteMatches) {
        dashboardExpandedNodes.add(nodeId);
    }

    const hasChildren = filteredAgents.length > 0;
    const m = site.metrics || {};

    let html = `<li class="dashboard-tree-node" data-node-id="${nodeId}">`;
    html += `<div class="dashboard-tree-row${siteMatches ? ' match' : ''}" data-type="site" data-id="${site.id}" data-tenant="${tenantId}">`;
    html += `<button class="dashboard-tree-toggle${isExpanded ? ' expanded' : ''}${hasChildren ? '' : ' no-children'}" aria-expanded="${isExpanded}">▶</button>`;
    html += `<span class="dashboard-tree-icon site">📍</span>`;
    html += `<div class="dashboard-tree-content">`;
    html += `<span class="dashboard-tree-name">${highlightMatch(escapeHtml(site.name))}</span>`;
    if (site.address) {
        html += `<span class="dashboard-tree-subtitle">${highlightMatch(escapeHtml(site.address))}</span>`;
    }
    html += `</div>`;
    html += `<div class="dashboard-tree-metrics">`;
    html += `<span class="dashboard-tree-metric" title="Agents">${m.agent_count || 0} agents</span>`;
    html += `<span class="dashboard-tree-metric" title="Devices">${m.device_count || 0} devices</span>`;
    if (m.critical_supplies > 0) {
        html += `<span class="dashboard-tree-metric critical" title="Critical supplies">⚠️ ${m.critical_supplies}</span>`;
    }
    html += `</div>`;
    html += `</div>`;

    // Children (agents)
    if (dashboardFilters.showAgents && hasChildren) {
        html += `<ul class="dashboard-tree-children${isExpanded ? '' : ' collapsed'}">`;
        for (const agent of filteredAgents) {
            const agentHTML = buildAgentNodeHTML(agent, tenantId, site.id);
            if (agentHTML) html += agentHTML;
        }
        html += `</ul>`;
    }

    html += `</li>`;
    return html;
}

function buildAgentNodeHTML(agent, tenantId, siteId) {
    const nodeId = `agent-${agent.agent_id}`;
    const isExpanded = dashboardExpandedNodes.has(nodeId);
    const agentMatches = matchesSearch(agent.name) || matchesSearch(agent.agent_id);

    // Filter devices
    const filteredDevices = (agent.devices || []).filter(device => {
        if (!dashboardFilters.supplyBand.has(device.supply_status)) return false;
        if (!dashboardFilters.deviceStatus.has(classifyDeviceStatus(device).code)) return false;
        return true;
    });

    // Check if any device matches search
    const hasMatchingDevice = filteredDevices.some(d =>
        matchesSearch(d.serial) || matchesSearch(d.manufacturer) ||
        matchesSearch(d.model) || matchesSearch(d.ip) || matchesSearch(d.location)
    );

    // Skip if searching and no matches
    if (dashboardSearchQuery && !agentMatches && !hasMatchingDevice) return '';

    // Auto-expand when searching
    if (dashboardSearchQuery && hasMatchingDevice && !agentMatches) {
        dashboardExpandedNodes.add(nodeId);
    }

    const hasChildren = filteredDevices.length > 0;
    const m = agent.metrics || {};
    const statusClass = agent.status || 'offline';

    let html = `<li class="dashboard-tree-node" data-node-id="${nodeId}">`;
    html += `<div class="dashboard-tree-row${agentMatches ? ' match' : ''}" data-type="agent" data-id="${agent.agent_id}" data-tenant="${tenantId}"${siteId ? ` data-site="${siteId}"` : ''}>`;
    html += `<button class="dashboard-tree-toggle${isExpanded ? ' expanded' : ''}${hasChildren ? '' : ' no-children'}" aria-expanded="${isExpanded}">▶</button>`;
    html += `<span class="dashboard-tree-icon agent">💻</span>`;
    html += `<div class="dashboard-tree-content">`;
    html += `<span class="dashboard-tree-name">${highlightMatch(escapeHtml(getAgentDisplayName(agent)))}</span>`;
    html += `<span class="dashboard-status-badge ${statusClass}"><span class="dashboard-status-dot"></span>${statusClass}</span>`;
    html += `</div>`;
    html += `<div class="dashboard-tree-metrics">`;
    html += `<span class="dashboard-tree-metric" title="Devices">${m.device_count || 0} devices</span>`;
    if (agent.version) {
        html += `<span class="dashboard-tree-metric" title="Version">v${escapeHtml(agent.version)}</span>`;
    }
    if (m.critical_supplies > 0) {
        html += `<span class="dashboard-tree-metric critical" title="Critical supplies">⚠️ ${m.critical_supplies}</span>`;
    }
    html += `</div>`;
    html += `</div>`;

    // Children (devices)
    if (dashboardFilters.showDevices && hasChildren) {
        html += `<ul class="dashboard-tree-children${isExpanded ? '' : ' collapsed'}">`;
        for (const device of filteredDevices) {
            const deviceMatches = matchesSearch(device.serial) || matchesSearch(device.manufacturer) ||
                matchesSearch(device.model) || matchesSearch(device.ip) || matchesSearch(device.location);

            // Skip if searching and this device doesn't match
            if (dashboardSearchQuery && !deviceMatches && !agentMatches) continue;

            html += buildDeviceNodeHTML(device, agent.agent_id, deviceMatches);
        }
        html += `</ul>`;
    }

    html += `</li>`;
    return html;
}

function buildDeviceNodeHTML(device, agentId, isMatch) {
    const supplyLevel = device.lowest_supply >= 0 ? device.lowest_supply : -1;
    const supplyStatus = device.supply_status || 'unknown';
    const deviceStatus = classifyDeviceStatus(device).code;
    const displayName = [device.manufacturer, device.model].filter(Boolean).join(' ') || 'Unknown Device';

    let html = `<li class="dashboard-tree-node">`;
    html += `<div class="dashboard-tree-row${isMatch ? ' match' : ''}" data-type="device" data-serial="${device.serial}" data-agent="${agentId}">`;
    html += `<span class="dashboard-tree-toggle no-children"></span>`;
    html += `<span class="dashboard-tree-icon device">🖨️</span>`;
    html += `<div class="dashboard-tree-content">`;
    html += `<span class="dashboard-tree-name">${highlightMatch(escapeHtml(displayName))}</span>`;
    html += `<span class="dashboard-tree-subtitle">${highlightMatch(escapeHtml(device.serial))}</span>`;
    html += `</div>`;
    html += `<div class="dashboard-tree-metrics">`;

    // Status badge
    if (deviceStatus !== 'healthy') {
        html += `<span class="dashboard-status-badge ${deviceStatus}">${deviceStatus}</span>`;
    }

    // Supply indicator
    if (supplyLevel >= 0) {
        html += `<div class="dashboard-supply-indicator" title="Lowest supply: ${supplyLevel}%">`;
        html += `<div class="dashboard-supply-bar"><div class="dashboard-supply-fill ${supplyStatus}" style="width:${supplyLevel}%"></div></div>`;
        html += `<span>${supplyLevel}%</span>`;
        html += `</div>`;
    }

    if (device.page_count > 0) {
        html += `<span class="dashboard-tree-metric" title="Page count">${device.page_count.toLocaleString()} pages</span>`;
    }
    html += `</div>`;
    html += `</div>`;
    html += `</li>`;
    return html;
}

function highlightMatch(text) {
    if (!dashboardSearchQuery || !text) return text;
    const regex = new RegExp(`(${escapeRegex(dashboardSearchQuery)})`, 'gi');
    return text.replace(regex, '<span class="highlight">$1</span>');
}

function escapeRegex(str) {
    return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function attachDashboardTreeListeners(container) {
    // Toggle expand/collapse
    container.querySelectorAll('.dashboard-tree-toggle').forEach(toggle => {
        toggle.addEventListener('click', (e) => {
            e.stopPropagation();
            const node = toggle.closest('.dashboard-tree-node');
            const nodeId = node?.dataset.nodeId;
            if (!nodeId) return;

            const children = node.querySelector('.dashboard-tree-children');
            if (!children) return;

            if (dashboardExpandedNodes.has(nodeId)) {
                dashboardExpandedNodes.delete(nodeId);
                children.classList.add('collapsed');
                toggle.classList.remove('expanded');
                toggle.setAttribute('aria-expanded', 'false');
            } else {
                dashboardExpandedNodes.add(nodeId);
                children.classList.remove('collapsed');
                toggle.classList.add('expanded');
                toggle.setAttribute('aria-expanded', 'true');
            }
        });
    });

    // Row click handlers
    container.querySelectorAll('.dashboard-tree-row').forEach(row => {
        row.addEventListener('click', () => {
            const type = row.dataset.type;

            if (type === 'tenant') {
                // Could navigate to tenant detail or filter devices by tenant
                const tenantId = row.dataset.id;
                console.log('Clicked tenant:', tenantId);
            } else if (type === 'agent') {
                // Navigate to agents tab filtered by this agent
                const agentId = row.dataset.id;
                switchTab('agents');
                const searchInput = document.getElementById('agents_search');
                if (searchInput) {
                    searchInput.value = agentId;
                    searchInput.dispatchEvent(new Event('input'));
                }
            } else if (type === 'device') {
                // Navigate to devices tab filtered by this device
                const serial = row.dataset.serial;
                switchTab('devices');
                const searchInput = document.getElementById('devices_search');
                if (searchInput) {
                    searchInput.value = serial;
                    searchInput.dispatchEvent(new Event('input'));
                }
            }
        });

        // Double-click to toggle expand
        row.addEventListener('dblclick', () => {
            const toggle = row.querySelector('.dashboard-tree-toggle');
            if (toggle && !toggle.classList.contains('no-children')) {
                toggle.click();
            }
        });
    });
}

// ---------------------------------------------------------------------------
// Self-Update Runs Panel
// ---------------------------------------------------------------------------
let selfUpdateRunsInitialized = false;
let selfUpdateCheckPending = false;
let releasesSyncPending = false;

async function loadSelfUpdateRuns() {
    const statusCard = document.getElementById('selfupdate_status_card');
    const runsContainer = document.getElementById('selfupdate_runs_container');

    if (!selfUpdateRunsInitialized) {
        selfUpdateRunsInitialized = true;
        const refreshBtn = document.getElementById('selfupdate_refresh_btn');
        if (refreshBtn) {
            refreshBtn.addEventListener('click', () => loadSelfUpdateRuns());
        }
        const syncBtn = document.getElementById('releases_sync_btn');
        if (syncBtn) {
            syncBtn.addEventListener('click', () => triggerReleasesSync());
        }
    }

    // Load status, runs, and artifacts in parallel
    try {
        const [statusResp, runsResp, artifactsResp] = await Promise.all([
            fetchJSON('/api/v1/selfupdate/status').catch(err => ({ error: err.message || err })),
            fetchJSON('/api/v1/selfupdate/runs').catch(err => ({ error: err.message || err })),
            fetchJSON('/api/v1/releases/artifacts').catch(err => ({ error: err.message || err }))
        ]);

        // Track container status for artifact rendering
        const isContainer = statusResp.is_container === true;
        releaseArtifactsIsContainer = isContainer;

        // Hide entire Server Updates section when running in container
        const serverUpdatesPanel = document.getElementById('server_updates_panel');
        if (serverUpdatesPanel) {
            serverUpdatesPanel.style.display = isContainer ? 'none' : '';
        }

        // Render status card (only if not in container)
        if (statusCard && !isContainer) {
            renderSelfUpdateStatus(statusCard, statusResp);
        }

        // Render runs table (only if not in container)
        if (runsContainer && !isContainer) {
            if (runsResp.error) {
                runsContainer.innerHTML = `<div style="color:var(--danger);">Failed to load history: ${runsResp.error}</div>`;
            } else {
                const runs = Array.isArray(runsResp.runs) ? runsResp.runs : [];
                renderSelfUpdateRuns(runsContainer, runs);
            }
        }

        // Initialize artifacts UI (for toggle handler)
        initReleaseArtifactsUI();

        // Render artifacts
        const artifactsContainer = document.getElementById('releases_artifacts_container');
        if (artifactsContainer) {
            if (artifactsResp.error) {
                artifactsContainer.innerHTML = `<div style="color:var(--danger);">Failed to load artifacts: ${artifactsResp.error}</div>`;
            } else {
                const artifacts = Array.isArray(artifactsResp.artifacts) ? artifactsResp.artifacts : [];
                renderReleaseArtifacts(artifactsContainer, artifacts, isContainer);
            }
        }
    } catch (err) {
        const message = err && err.message ? err.message : err;
        if (statusCard) {
            statusCard.innerHTML = `<div style="color:var(--danger);">Failed to load status: ${message}</div>`;
        }
        if (runsContainer) {
            runsContainer.innerHTML = `<div style="color:var(--danger);">Failed to load history: ${message}</div>`;
        }
    }
}

function renderSelfUpdateStatus(container, status) {
    if (!status || status.error) {
        container.innerHTML = `<div style="color:var(--danger);">Failed to load status: ${status?.error || 'Unknown error'}</div>`;
        return;
    }

    const enabledBadge = status.enabled
        ? '<span style="display:inline-block;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600;background:var(--success)20;color:var(--success);">Enabled</span>'
        : '<span style="display:inline-block;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600;background:var(--danger)20;color:var(--danger);">Disabled</span>';

    const checkBtn = status.enabled
        ? `<button id="selfupdate_check_btn" class="modal-button modal-button-primary" style="padding:6px 12px;">Check for Updates</button>`
        : '';

    container.innerHTML = `
        <div style="display:flex;flex-wrap:wrap;gap:16px;justify-content:space-between;align-items:flex-start;">
            <div style="display:flex;flex-direction:column;gap:8px;">
                <div style="display:flex;align-items:center;gap:8px;">
                    <span style="font-weight:600;">Auto-Update:</span>
                    ${enabledBadge}
                </div>
                ${status.disabled_reason ? `<div style="color:var(--muted);font-size:12px;">Reason: ${status.disabled_reason}</div>` : ''}
                <div style="display:flex;flex-wrap:wrap;gap:16px;font-size:13px;color:var(--muted);">
                    <span><strong>Version:</strong> ${status.current_version || 'Unknown'}</span>
                    <span><strong>Channel:</strong> ${status.channel || 'stable'}</span>
                    <span><strong>Platform:</strong> ${status.platform || '?'}/${status.arch || '?'}</span>
                    <span><strong>Check Interval:</strong> ${status.check_interval || '?'}</span>
                </div>
            </div>
            <div style="display:flex;gap:8px;align-items:center;">
                ${checkBtn}
            </div>
        </div>
    `;

    // Bind check button
    const btn = document.getElementById('selfupdate_check_btn');
    if (btn) {
        btn.addEventListener('click', triggerSelfUpdateCheck);
    }
}

async function triggerSelfUpdateCheck() {
    if (selfUpdateCheckPending) return;
    selfUpdateCheckPending = true;

    const btn = document.getElementById('selfupdate_check_btn');
    if (btn) {
        btn.disabled = true;
        btn.textContent = 'Checking…';
    }

    try {
        const resp = await fetch('/api/v1/selfupdate/check', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin'
        });
        const data = await resp.json().catch(() => ({}));

        if (resp.ok) {
            showToast('Update check initiated', 'success');
            // Reload after a short delay to show results
            setTimeout(() => loadSelfUpdateRuns(), 2000);
        } else {
            showToast(data.error || 'Failed to start update check', 'error');
        }
    } catch (err) {
        showToast('Failed to start update check', 'error');
    } finally {
        selfUpdateCheckPending = false;
        if (btn) {
            btn.disabled = false;
            btn.textContent = 'Check for Updates';
        }
    }
}

function renderSelfUpdateRuns(container, runs) {
    if (!runs || runs.length === 0) {
        container.innerHTML = '<div class="muted-text">No update attempts recorded.</div>';
        return;
    }

    const statusBadge = (status) => {
        const colors = {
            'success': 'var(--success)',
            'failed': 'var(--danger)',
            'rollback': 'var(--warn)',
            'pending': 'var(--muted)',
            'in_progress': 'var(--highlight)',
        };
        const color = colors[status] || 'var(--muted)';
        return `<span style="display:inline-block;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600;background:${color}20;color:${color};">${status}</span>`;
    };

    const formatTime = (ts) => {
        if (!ts) return '—';
        const d = new Date(ts);
        return d.toLocaleString();
    };

    const rows = runs.map(run => `
        <tr>
            <td style="padding:8px;border-bottom:1px solid var(--border);">${formatTime(run.started_at)}</td>
            <td style="padding:8px;border-bottom:1px solid var(--border);">${run.from_version || '—'}</td>
            <td style="padding:8px;border-bottom:1px solid var(--border);">${run.to_version || '—'}</td>
            <td style="padding:8px;border-bottom:1px solid var(--border);">${statusBadge(run.status)}</td>
            <td style="padding:8px;border-bottom:1px solid var(--border);max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${run.message || ''}">${run.message || '—'}</td>
            <td style="padding:8px;border-bottom:1px solid var(--border);">${formatTime(run.finished_at)}</td>
        </tr>
    `).join('');

    container.innerHTML = `
        <table style="width:100%;border-collapse:collapse;font-size:13px;">
            <thead>
                <tr style="text-align:left;color:var(--muted);border-bottom:2px solid var(--border);">
                    <th style="padding:8px;">Started</th>
                    <th style="padding:8px;">From</th>
                    <th style="padding:8px;">To</th>
                    <th style="padding:8px;">Status</th>
                    <th style="padding:8px;">Message</th>
                    <th style="padding:8px;">Finished</th>
                </tr>
            </thead>
            <tbody>
                ${rows}
            </tbody>
        </table>
    `;
}

// ---------------------------------------------------------------------------
// Release Artifacts Display
// ---------------------------------------------------------------------------

// Release sync progress state
let releaseSyncProgress = null;

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function handleReleaseSyncProgress(data) {
    releaseSyncProgress = data;

    const btn = document.getElementById('releases_sync_btn');
    const progressContainer = document.getElementById('releases_sync_progress');

    if (data.phase === 'complete') {
        // Sync finished
        releasesSyncPending = false;
        if (btn) {
            btn.disabled = false;
            btn.textContent = 'Sync from GitHub';
        }
        if (progressContainer) {
            progressContainer.style.display = 'none';
        }
        showToast(data.message || 'Release sync completed', 'success');
        // Reload artifacts list
        setTimeout(() => {
            loadReleaseArtifacts();
            loadSelfUpdateRuns();
        }, 500);
        return;
    }

    if (data.phase === 'error') {
        // Sync failed
        releasesSyncPending = false;
        if (btn) {
            btn.disabled = false;
            btn.textContent = 'Sync from GitHub';
        }
        if (progressContainer) {
            progressContainer.style.display = 'none';
        }
        showToast(data.error || data.message || 'Release sync failed', 'error');
        return;
    }

    // Ongoing sync - update progress display
    if (btn) {
        btn.disabled = true;
    }

    // Create progress container if it doesn't exist
    if (!progressContainer) {
        const card = document.getElementById('releases_artifacts_card');
        if (card) {
            const container = document.createElement('div');
            container.id = 'releases_sync_progress';
            container.style.cssText = 'margin-bottom:12px;padding:12px;background:var(--bg-secondary);border-radius:6px;';
            card.insertBefore(container, card.firstChild.nextSibling);
        }
    }

    const container = document.getElementById('releases_sync_progress');
    if (container) {
        container.style.display = 'block';

        let progressHtml = `<div style="font-size:13px;color:var(--text);margin-bottom:8px;">${escapeHtml(data.message || 'Syncing...')}</div>`;

        if (data.phase === 'downloading' && data.total_files > 0) {
            const pct = data.percent_complete || 0;
            const completedBytes = formatBytes(data.completed_bytes || 0);
            const totalBytes = formatBytes(data.total_bytes || 0);

            progressHtml += `
                <div style="display:flex;justify-content:space-between;font-size:12px;color:var(--muted);margin-bottom:4px;">
                    <span>File ${data.completed_files + 1} of ${data.total_files}${data.current_file ? ': ' + escapeHtml(data.current_file) : ''}</span>
                    <span>${completedBytes} / ${totalBytes}</span>
                </div>
                <div style="background:var(--bg);border-radius:4px;height:8px;overflow:hidden;">
                    <div style="background:var(--highlight);height:100%;width:${pct}%;transition:width 0.2s ease;"></div>
                </div>
                <div style="text-align:right;font-size:11px;color:var(--muted);margin-top:2px;">${pct}%</div>
            `;
        } else if (data.phase === 'fetching' || data.phase === 'processing') {
            progressHtml += `
                <div style="background:var(--bg);border-radius:4px;height:8px;overflow:hidden;">
                    <div style="background:var(--highlight);height:100%;width:100%;animation:pulse 1.5s ease-in-out infinite;"></div>
                </div>
            `;
        }

        container.innerHTML = progressHtml;
    }

    // Update button text
    if (btn) {
        if (data.phase === 'downloading' && data.percent_complete !== undefined) {
            btn.textContent = `Syncing... ${data.percent_complete}%`;
        } else {
            btn.textContent = 'Syncing...';
        }
    }
}

async function triggerReleasesSync() {
    if (releasesSyncPending) return;
    releasesSyncPending = true;

    const btn = document.getElementById('releases_sync_btn');
    if (btn) {
        btn.disabled = true;
        btn.textContent = 'Syncing...';
    }

    // Show initial progress state
    handleReleaseSyncProgress({
        phase: 'fetching',
        message: 'Starting sync...'
    });

    try {
        const resp = await fetch('/api/v1/releases/sync', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin'
        });
        const data = await resp.json().catch(() => ({}));

        if (!resp.ok) {
            // Sync failed to start
            handleReleaseSyncProgress({
                phase: 'error',
                message: 'Failed to start sync',
                error: data.error || 'Unknown error'
            });
        }
        // If ok, progress updates will come via SSE
    } catch (err) {
        handleReleaseSyncProgress({
            phase: 'error',
            message: 'Failed to start sync',
            error: err.message || 'Network error'
        });
    }
}

// ---------------------------------------------------------------------------
// Release Artifacts Loading (shared between Fleet and Server tabs)
// ---------------------------------------------------------------------------
let releaseArtifactsInitialized = false;
let releaseArtifactsIsContainer = false;

function initReleaseArtifactsUI() {
    if (releaseArtifactsInitialized) return;
    releaseArtifactsInitialized = true;

    const syncBtn = document.getElementById('releases_sync_btn');
    if (syncBtn) {
        syncBtn.addEventListener('click', () => triggerReleasesSync());
    }

    // Initialize collapsible header
    const header = document.getElementById('releases_artifacts_header');
    const container = document.getElementById('releases_artifacts_container');
    const chevron = document.getElementById('releases_artifacts_chevron');
    const card = document.getElementById('releases_artifacts_card');

    if (header && container && chevron) {
        header.addEventListener('click', () => {
            const isCollapsed = container.style.display === 'none';
            container.style.display = isCollapsed ? 'block' : 'none';
            chevron.style.transform = isCollapsed ? 'rotate(0deg)' : 'rotate(-90deg)';
            if (card) {
                if (isCollapsed) {
                    card.classList.remove('collapsed');
                } else {
                    card.classList.add('collapsed');
                }
            }
        });
    }
}

async function loadReleaseArtifacts() {
    const artifactsContainer = document.getElementById('releases_artifacts_container');
    if (!artifactsContainer) return;

    initReleaseArtifactsUI();

    try {
        // Fetch status to check if running in container
        const statusResp = await fetchJSON('/api/v1/selfupdate/status').catch(() => ({}));
        releaseArtifactsIsContainer = statusResp.is_container === true;

        const artifactsResp = await fetchJSON('/api/v1/releases/artifacts');
        if (artifactsResp.error) {
            artifactsContainer.innerHTML = `<div style="color:var(--danger);">Failed to load artifacts: ${artifactsResp.error}</div>`;
        } else {
            const artifacts = Array.isArray(artifactsResp.artifacts) ? artifactsResp.artifacts : [];
            renderReleaseArtifacts(artifactsContainer, artifacts, releaseArtifactsIsContainer);
        }
    } catch (err) {
        const message = err && err.message ? err.message : err;
        artifactsContainer.innerHTML = `<div style="color:var(--danger);">Failed to load artifacts: ${message}</div>`;
    }
}

function renderReleaseArtifacts(container, artifacts, isContainer = false) {
    const countEl = document.getElementById('releases_artifacts_count');

    // Filter out server artifacts in container environments
    let filteredArtifacts = artifacts;
    if (isContainer) {
        filteredArtifacts = artifacts.filter(a => a.component !== 'server');
    }

    if (!filteredArtifacts || filteredArtifacts.length === 0) {
        const msg = isContainer
            ? '<div class="muted-text">No agent artifacts cached. Server binaries are not cached in container environments. Click "Sync from GitHub" to fetch releases.</div>'
            : '<div class="muted-text">No cached artifacts. Click "Sync from GitHub" to fetch releases.</div>';
        container.innerHTML = msg;
        if (countEl) countEl.textContent = '';
        return;
    }

    const formatTime = (ts) => {
        if (!ts || ts === '0001-01-01T00:00:00Z') return '—';
        const d = new Date(ts);
        return d.toLocaleDateString();
    };

    const componentBadge = (component) => {
        const colors = {
            'agent': 'var(--highlight)',
            'server': 'var(--success)'
        };
        const color = colors[component] || 'var(--muted)';
        return `<span style="display:inline-block;padding:2px 8px;border-radius:4px;font-size:11px;font-weight:600;background:${color}20;color:${color};">${component}</span>`;
    };

    const platformArchBadge = (platform, arch, cached) => {
        const platformAbbr = {
            'windows': 'Win',
            'linux': 'Linux',
            'darwin': 'macOS'
        };
        const label = platformAbbr[platform] || platform;
        const cachedStyle = cached ? 'color:var(--text);' : 'color:var(--muted);opacity:0.6;';
        const title = cached ? `${platform}/${arch} - Cached` : `${platform}/${arch} - Not cached`;
        return `<span style="display:inline-block;padding:2px 6px;border-radius:4px;font-size:11px;margin-right:4px;background:var(--bg-secondary);${cachedStyle}" title="${title}">${label}/${arch}</span>`;
    };

    // Group artifacts by component, version, and channel
    const grouped = {};
    for (const a of filteredArtifacts) {
        const key = `${a.component}|${a.version}|${a.channel || 'stable'}`;
        if (!grouped[key]) {
            grouped[key] = {
                component: a.component,
                version: a.version,
                channel: a.channel || 'stable',
                published_at: a.published_at,
                platforms: []
            };
        }
        grouped[key].platforms.push({
            platform: a.platform,
            arch: a.arch,
            cached: a.cached,
            size_bytes: a.size_bytes
        });
        // Use latest publish date
        if (a.published_at && a.published_at > grouped[key].published_at) {
            grouped[key].published_at = a.published_at;
        }
    }

    // Convert to array and sort by component (agent first), then version descending
    const groups = Object.values(grouped).sort((a, b) => {
        if (a.component !== b.component) {
            return a.component === 'agent' ? -1 : 1;
        }
        // Sort versions descending (semver-ish comparison)
        return b.version.localeCompare(a.version, undefined, { numeric: true, sensitivity: 'base' });
    });

    // Update count badge
    const uniqueVersions = new Set(groups.map(g => `${g.component}-${g.version}`)).size;
    if (countEl) {
        countEl.textContent = `(${uniqueVersions} version${uniqueVersions !== 1 ? 's' : ''})`;
    }

    // Sort platforms consistently
    const platformOrder = ['windows', 'linux', 'darwin'];
    const archOrder = ['amd64', 'arm64'];

    const rows = groups.map(g => {
        const sortedPlatforms = g.platforms.sort((a, b) => {
            const pA = platformOrder.indexOf(a.platform);
            const pB = platformOrder.indexOf(b.platform);
            if (pA !== pB) return pA - pB;
            const aA = archOrder.indexOf(a.arch);
            const aB = archOrder.indexOf(b.arch);
            return aA - aB;
        });

        const platformBadges = sortedPlatforms.map(p => platformArchBadge(p.platform, p.arch, p.cached)).join('');

        return `
            <tr>
                <td style="padding:8px 12px;border-bottom:1px solid var(--border);">${componentBadge(g.component)}</td>
                <td style="padding:8px 12px;border-bottom:1px solid var(--border);font-family:monospace;font-weight:600;">${g.version}</td>
                <td style="padding:8px 12px;border-bottom:1px solid var(--border);">${platformBadges}</td>
                <td style="padding:8px 12px;border-bottom:1px solid var(--border);">${g.channel}</td>
                <td style="padding:8px 12px;border-bottom:1px solid var(--border);">${formatTime(g.published_at)}</td>
            </tr>
        `;
    }).join('');

    container.innerHTML = `
        ${isContainer ? '<div style="color:var(--warning);font-size:12px;margin-bottom:8px;padding:6px 10px;background:var(--warning)15;border-radius:4px;">Server binaries are not cached in container environments (updates via container image).</div>' : ''}
        <table style="width:100%;border-collapse:collapse;font-size:13px;">
            <thead>
                <tr style="text-align:left;color:var(--muted);border-bottom:2px solid var(--border);">
                    <th style="padding:8px 12px;">Component</th>
                    <th style="padding:8px 12px;">Version</th>
                    <th style="padding:8px 12px;">Platforms</th>
                    <th style="padding:8px 12px;">Channel</th>
                    <th style="padding:8px 12px;">Published</th>
                </tr>
            </thead>
            <tbody>
                ${rows}
            </tbody>
        </table>
    `;
}

// ---------------------------------------------------------------------------
// SSO Admin Integration
// ---------------------------------------------------------------------------

function logSSOWarning(message, err) {
    if (window.__pm_shared && typeof window.__pm_shared.warn === 'function') {
        window.__pm_shared.warn(message, err);
    } else {
        console.warn(message, err);
    }
}

function invokeSSOMethod(method, ...args) {
    if (!window.__pmSSO || typeof window.__pmSSO[method] !== 'function') {
        return;
    }
    try {
        window.__pmSSO[method](...args);
    } catch (err) {
        logSSOWarning('SSO admin call failed: ' + method, err);
    }
}

function initSSOAdmin() {
    if (ssoAdminInitialized) return;
    ssoAdminInitialized = true;
    invokeSSOMethod('init');
}

function refreshSSOProviders() {
    invokeSSOMethod('loadProviders');
}

function syncSSOTenants(list) {
    invokeSSOMethod('syncTenants', Array.isArray(list) ? list : []);
}

// Wrapper functions removed: call sites should use window.__pm_shared.showToast / showConfirm / showAlert directly.

// ====== Onboarding Wizard ======
const onboardingState = {
    currentStep: 'welcome',
    data: null,
    initialized: false,
};

async function checkOnboardingStatus() {
    // Only check for admin users
    if (!currentUser || currentUser.role !== 'admin') return;

    try {
        const response = await fetch('/api/v1/onboarding/status');
        if (!response.ok) return;

        const data = await response.json();
        onboardingState.data = data;

        if (data.needs_onboarding) {
            showOnboardingWizard();
        }
    } catch (error) {
        window.__pm_shared.warn('Failed to check onboarding status:', error);
    }
}

function showOnboardingWizard() {
    const modal = document.getElementById('onboarding_modal');
    if (!modal) return;

    if (!onboardingState.initialized) {
        initOnboardingWizard();
        onboardingState.initialized = true;
    }

    // Reset to first step
    onboardingState.currentStep = 'welcome';
    updateOnboardingStep();

    modal.style.display = 'flex';
}

function initOnboardingWizard() {
    const modal = document.getElementById('onboarding_modal');
    if (!modal) return;

    const skipBtn = document.getElementById('onboarding_skip');
    const nextBtn = document.getElementById('onboarding_next');
    const nameInput = document.getElementById('onboarding_tenant_name');

    if (skipBtn) {
        skipBtn.addEventListener('click', closeOnboardingWizard);
    }

    if (nextBtn) {
        nextBtn.addEventListener('click', handleOnboardingNext);
    }

    if (nameInput) {
        nameInput.addEventListener('input', () => {
            // Clear error when typing
            const errorEl = document.getElementById('onboarding_error');
            if (errorEl) errorEl.textContent = '';
        });
        nameInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                handleOnboardingNext();
            }
        });
    }

    // Listen for onboarding complete event via SSE/WebSocket
    if (window.__pm_ws_eventHandlers) {
        window.__pm_ws_eventHandlers['onboarding_complete'] = (data) => {
            window.__pm_shared.log('Onboarding complete event received', data);
            // Auto-advance to success step if still on tenant step
            if (onboardingState.currentStep === 'tenant') {
                onboardingState.currentStep = 'success';
                updateOnboardingStep();
            }
        };
    }
}

function updateOnboardingStep() {
    const steps = document.querySelectorAll('#onboarding_modal .onboarding-step');
    const nextBtn = document.getElementById('onboarding_next');
    const skipBtn = document.getElementById('onboarding_skip');

    steps.forEach(step => {
        const stepName = step.getAttribute('data-step');
        step.classList.toggle('hidden', stepName !== onboardingState.currentStep);
    });

    // Update button text based on step
    if (nextBtn) {
        switch (onboardingState.currentStep) {
            case 'welcome':
                nextBtn.textContent = 'Get Started';
                break;
            case 'tenant':
                nextBtn.textContent = 'Create Tenant';
                break;
            case 'success':
                nextBtn.textContent = 'Done';
                break;
        }
    }

    // Hide skip on success step
    if (skipBtn) {
        skipBtn.style.display = onboardingState.currentStep === 'success' ? 'none' : '';
    }

    // Focus input on tenant step
    if (onboardingState.currentStep === 'tenant') {
        const nameInput = document.getElementById('onboarding_tenant_name');
        if (nameInput) setTimeout(() => nameInput.focus(), 100);
    }
}

async function handleOnboardingNext() {
    const nextBtn = document.getElementById('onboarding_next');

    switch (onboardingState.currentStep) {
        case 'welcome':
            onboardingState.currentStep = 'tenant';
            updateOnboardingStep();
            break;

        case 'tenant':
            // Validate and create tenant
            const nameInput = document.getElementById('onboarding_tenant_name');
            const emailInput = document.getElementById('onboarding_tenant_email');
            const errorEl = document.getElementById('onboarding_error');

            const name = nameInput?.value?.trim();
            const email = emailInput?.value?.trim();

            if (!name) {
                if (errorEl) errorEl.textContent = 'Please enter a tenant name';
                if (nameInput) nameInput.focus();
                return;
            }

            // Disable button and show loading
            if (nextBtn) {
                nextBtn.disabled = true;
                nextBtn.textContent = 'Creating...';
            }

            try {
                const payload = { name };
                if (email) payload.contact_email = email;

                const response = await fetch('/api/v1/tenants', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });

                if (!response.ok) {
                    const err = await response.json().catch(() => ({}));
                    throw new Error(err.error || `Failed to create tenant (${response.status})`);
                }

                const tenant = await response.json();
                window.__pm_shared.log('Tenant created via onboarding', tenant);

                // Refresh tenant list
                await ensureTenantDirectory(true);

                // Move to success step
                onboardingState.currentStep = 'success';
                updateOnboardingStep();

            } catch (error) {
                window.__pm_shared.error('Failed to create tenant:', error);
                if (errorEl) errorEl.textContent = error.message || 'Failed to create tenant';
            } finally {
                if (nextBtn) {
                    nextBtn.disabled = false;
                    nextBtn.textContent = 'Create Tenant';
                }
            }
            break;

        case 'success':
            closeOnboardingWizard();
            // Refresh agents list to show newly connected agents
            loadAgents();
            break;
    }
}

function closeOnboardingWizard() {
    const modal = document.getElementById('onboarding_modal');
    if (modal) modal.style.display = 'none';
}

// ====== Server Status ======
async function loadServerStatus() {
    try {
        const response = await fetch('/api/version');
        if (!response.ok) {
            const el = document.getElementById('server_status');
            if (el) el.innerHTML = '<span style="color:var(--error);">● Error</span>';
            else window.__pm_shared.warn('server_status element not found in DOM');
            return;
        }

        const data = await response.json();
        const el = document.getElementById('server_status');
        if (el) {
            const ver = data.version || 'unknown';
            const buildType = data.build_type || '';
            const showBuildType = buildType && buildType !== 'release' && !ver.toLowerCase().includes(buildType.toLowerCase());
            el.innerHTML = `<span style="color:var(--success);">● Online</span> v${escapeHtml(ver)}${showBuildType ? ` (${escapeHtml(buildType)})` : ''}`;
            el.title = `Server version: ${ver}` +
                (data.git_commit ? `\nCommit: ${data.git_commit}` : '') +
                (data.build_time ? `\nBuilt: ${data.build_time}` : '');
        }
        else window.__pm_shared.warn('server_status element not found in DOM');

        // Store tenancy_enabled flag globally for other UI components
        window.__pm_tenancy_enabled = Boolean(data.tenancy_enabled);
    } catch (error) {
        window.__pm_shared.error('Failed to load server status:', error);
        const errEl = document.getElementById('server_status');
        if (errEl) errEl.innerHTML = '<span style="color:var(--error);">● Error loading status</span>';
        else window.__pm_shared.warn('server_status element not found in DOM while handling error');
    }
}

// ====== Pending Agent Registrations ======
const pendingRegistrationsVM = {
    items: [],
    loading: false,
    error: null,
    expanded: false,
    uiInitialized: false,
};

function initPendingRegistrationsUI() {
    if (pendingRegistrationsVM.uiInitialized) return;
    pendingRegistrationsVM.uiInitialized = true;

    const toggleBtn = document.getElementById('pending_registrations_toggle');
    if (toggleBtn) {
        toggleBtn.addEventListener('click', () => {
            pendingRegistrationsVM.expanded = !pendingRegistrationsVM.expanded;
            const body = document.getElementById('pending_registrations_body');
            if (body) {
                body.classList.toggle('hidden', !pendingRegistrationsVM.expanded);
            }
            toggleBtn.textContent = pendingRegistrationsVM.expanded ? 'Hide Details' : 'Show Details';
            toggleBtn.setAttribute('aria-expanded', pendingRegistrationsVM.expanded ? 'true' : 'false');
        });
    }

    const refreshBtn = document.getElementById('pending_registrations_refresh');
    if (refreshBtn) {
        refreshBtn.addEventListener('click', () => loadPendingRegistrations(true));
    }

    // Attach event delegation for approve/reject buttons
    const tbody = document.getElementById('pending_registrations_tbody');
    if (tbody) {
        tbody.addEventListener('click', handlePendingRegistrationAction);
    }
}

async function loadPendingRegistrations(force = false) {
    // Only load if tenancy is enabled - check from server settings
    if (!window.__pm_tenancy_enabled) {
        hidePendingRegistrationsSection();
        return;
    }

    if (pendingRegistrationsVM.loading && !force) return;
    pendingRegistrationsVM.loading = true;

    try {
        const response = await fetch('/api/v1/pending-registrations?status=pending');
        if (!response.ok) {
            if (response.status === 403 || response.status === 401) {
                // User doesn't have permission - hide the section silently
                hidePendingRegistrationsSection();
                return;
            }
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        pendingRegistrationsVM.items = Array.isArray(data) ? data : [];
        pendingRegistrationsVM.error = null;

        if (pendingRegistrationsVM.items.length > 0) {
            showPendingRegistrationsSection();
            renderPendingRegistrations();
        } else {
            hidePendingRegistrationsSection();
        }
    } catch (error) {
        pendingRegistrationsVM.error = error;
        // On error, hide the section to not confuse users
        hidePendingRegistrationsSection();
        if (window.__pm_shared && typeof window.__pm_shared.warn === 'function') {
            window.__pm_shared.warn('Failed to load pending registrations', error);
        }
    } finally {
        pendingRegistrationsVM.loading = false;
    }
}

function showPendingRegistrationsSection() {
    const section = document.getElementById('pending_registrations_section');
    if (section) {
        section.classList.remove('hidden');
    }
}

function hidePendingRegistrationsSection() {
    const section = document.getElementById('pending_registrations_section');
    if (section) {
        section.classList.add('hidden');
    }
}

function renderPendingRegistrations() {
    const countEl = document.getElementById('pending_registrations_count');
    if (countEl) {
        countEl.textContent = pendingRegistrationsVM.items.length;
    }

    const tbody = document.getElementById('pending_registrations_tbody');
    if (!tbody) return;

    if (pendingRegistrationsVM.items.length === 0) {
        tbody.innerHTML = '<tr><td colspan="7" class="pending-registrations-empty">No pending registrations</td></tr>';
        return;
    }

    const rows = pendingRegistrationsVM.items.map(reg => {
        const agentName = escapeHtml(reg.name || reg.hostname || reg.agent_id || 'Unknown');
        const platform = escapeHtml(reg.platform || 'Unknown');
        const ip = escapeHtml(reg.ip || 'Unknown');
        const expiredTenant = escapeHtml(reg.expired_tenant_id || 'Unknown');
        const createdAt = reg.created_at ? formatRelativeTime(new Date(reg.created_at)) : 'Unknown';
        const statusClass = (reg.status || 'pending').toLowerCase();

        return `
            <tr data-reg-id="${reg.id}">
                <td>
                    <div style="font-weight:500;">${agentName}</div>
                    <div style="font-size:11px;color:var(--muted);">${escapeHtml(reg.agent_id || '')}</div>
                </td>
                <td>${platform}</td>
                <td>${ip}</td>
                <td>${expiredTenant}</td>
                <td title="${reg.created_at ? new Date(reg.created_at).toLocaleString() : ''}">${createdAt}</td>
                <td><span class="status-badge ${statusClass}">${escapeHtml(reg.status || 'pending')}</span></td>
                <td class="actions-col">
                    ${reg.status === 'pending' ? `
                        <button class="action-btn approve" data-action="approve" data-id="${reg.id}" data-tenant="${escapeHtml(reg.expired_tenant_id || '')}">Approve</button>
                        <button class="action-btn reject" data-action="reject" data-id="${reg.id}">Reject</button>
                    ` : '—'}
                </td>
            </tr>
        `;
    }).join('');

    tbody.innerHTML = rows;
}

async function handlePendingRegistrationAction(event) {
    const btn = event.target.closest('button[data-action]');
    if (!btn) return;

    const action = btn.getAttribute('data-action');
    const id = btn.getAttribute('data-id');

    if (!action || !id) return;

    btn.disabled = true;
    const originalText = btn.textContent;
    btn.textContent = 'Processing…';

    try {
        if (action === 'approve') {
            await approvePendingRegistration(id, btn);
        } else if (action === 'reject') {
            await rejectPendingRegistration(id, btn);
        }
    } catch (error) {
        btn.disabled = false;
        btn.textContent = originalText;
        if (window.__pm_shared && typeof window.__pm_shared.showAlert === 'function') {
            window.__pm_shared.showAlert(`Failed to ${action} registration: ${error.message}`, 'Error', true, false);
        }
    }
}

async function approvePendingRegistration(id, btn) {
    // Get the tenant to assign - use the original expired tenant or prompt for selection
    const tenantId = btn.getAttribute('data-tenant');

    // If no tenant, we need to prompt for one
    if (!tenantId) {
        if (window.__pm_shared && typeof window.__pm_shared.showAlert === 'function') {
            window.__pm_shared.showAlert('Cannot approve: no tenant to assign. The original tenant is not available.', 'Error', true, false);
        }
        btn.disabled = false;
        btn.textContent = 'Approve';
        return;
    }

    const response = await fetch(`/api/v1/pending-registrations/${id}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: 'approve', tenant_id: tenantId }),
    });

    if (!response.ok) {
        const errData = await response.json().catch(() => ({}));
        throw new Error(errData.error || `HTTP ${response.status}`);
    }

    const data = await response.json();

    // Show success with token info
    let message = 'Agent registration approved.';
    if (data.join_token) {
        message += ` A new join token has been generated. The agent will need to reconnect with this token:\n\n${data.join_token}`;
    }

    if (window.__pm_shared && typeof window.__pm_shared.showAlert === 'function') {
        window.__pm_shared.showAlert(message, 'Success', false, false);
    }

    // Refresh the list
    await loadPendingRegistrations(true);
    // Also refresh agents list in case the agent reconnects
    loadAgents(true);
}

async function rejectPendingRegistration(id, btn) {
    // Optionally prompt for rejection notes
    const notes = ''; // Could add a prompt here in the future

    const response = await fetch(`/api/v1/pending-registrations/${id}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: 'reject', notes }),
    });

    if (!response.ok) {
        const errData = await response.json().catch(() => ({}));
        throw new Error(errData.error || `HTTP ${response.status}`);
    }

    if (window.__pm_shared && typeof window.__pm_shared.showAlert === 'function') {
        window.__pm_shared.showAlert('Agent registration rejected.', 'Info', false, false);
    }

    // Refresh the list
    await loadPendingRegistrations(true);
}

// ====== Agents Management ======
async function loadAgents(force = false) {
    initAgentsUI();
    if (agentsVM.loading && !force) {
        return;
    }
    agentsVM.loading = true;
    renderAgentsLoading();
    const tenantPromise = ensureTenantDirectory();
    try {
        const response = await fetch('/api/v1/agents/list');
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const agents = await response.json();
        await tenantPromise;
        updateAgentDirectory(Array.isArray(agents) ? agents : []);
        agentsVM.items = enrichAgents(Array.isArray(agents) ? agents : []);
        agentsVM.stats.total = agentsVM.items.length;
        agentsVM.error = null;
        agentsVM.loaded = true;
        refreshAgentFilters();
        refreshAgentMetrics();
        applyAgentFilters();
        // Defer update version check until after render
        if (agentsVM.checkUpdatesOnLoad) {
            setTimeout(() => checkAgentsForUpdates(), 100);
        }
    } catch (error) {
        agentsVM.error = error;
        renderAgentsError(error);
    } finally {
        agentsVM.loading = false;
    }
}

// Check agents for available updates by fetching latest version
async function checkAgentsForUpdates() {
    if (agentsVM.updateCheckInProgress) {
        return;
    }
    agentsVM.updateCheckInProgress = true;
    updateCheckAllUpdatesButton();
    try {
        const response = await fetch('/api/v1/releases/latest-agent-version');
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        agentsVM.latestVersion = data?.version || null;
        // Re-render to show update buttons
        applyAgentFilters();
    } catch (error) {
        if (window.__pm_shared && typeof window.__pm_shared.warn === 'function') {
            window.__pm_shared.warn('Failed to check for agent updates', error);
        }
    } finally {
        agentsVM.updateCheckInProgress = false;
        updateCheckAllUpdatesButton();
    }
}

// Update the "Check All for Updates" button state
function updateCheckAllUpdatesButton() {
    const btn = document.getElementById('agents_check_updates_btn');
    if (!btn) return;
    if (agentsVM.updateCheckInProgress) {
        btn.disabled = true;
        btn.textContent = 'Checking…';
    } else {
        btn.disabled = false;
        btn.textContent = 'Check for Updates';
    }
}

// ====== Tenants UI ======
function initTenantsUI() {
    if (tenantsUIInitialized) return;
    tenantsUIInitialized = true;
    initTenantModal();
    initTenantsSubTabs();
    initSitesUI();
    switchTenantsView(activeTenantsView, true);

    initFilterDrawer('tenants_sidebar', 'tenants_filters_open', 'tenants_sidebar_toggle');

    // Search filter
    const searchInput = document.getElementById('tenants_search');
    if (searchInput) {
        searchInput.value = tenantsVM.filters.query;
        const handleSearch = debounce((event) => {
            tenantsVM.filters.query = (event.target.value || '').trim().toLowerCase();
            applyTenantFilters();
        }, 200);
        searchInput.addEventListener('input', handleSearch);
    }

    // Sort dropdown
    const sortSelect = document.getElementById('tenants_sort_select');
    if (sortSelect) {
        sortSelect.value = tenantsVM.filters.sortKey;
        sortSelect.addEventListener('change', (event) => {
            tenantsVM.filters.sortKey = event.target.value;
            applyTenantFilters();
        });
    }

    // Sort direction button
    const sortDirBtn = document.getElementById('tenants_sort_dir_btn');
    if (sortDirBtn) {
        updateTenantSortDirButton();
        sortDirBtn.addEventListener('click', () => {
            tenantsVM.filters.sortDir = tenantsVM.filters.sortDir === 'asc' ? 'desc' : 'asc';
            updateTenantSortDirButton();
            applyTenantFilters();
        });
    }

    // Reset filters button
    const resetBtn = document.getElementById('tenants_reset_filters');
    if (resetBtn) {
        resetBtn.addEventListener('click', () => {
            tenantsVM.filters.query = '';
            tenantsVM.filters.sortKey = 'name';
            tenantsVM.filters.sortDir = 'asc';
            if (searchInput) searchInput.value = '';
            if (sortSelect) sortSelect.value = 'name';
            updateTenantSortDirButton();
            applyTenantFilters();
        });
    }

    const btn = document.getElementById('new_tenant_btn');
    if (btn) {
        btn.addEventListener('click', () => openTenantModal());
    }
    loadTenants();
}

function updateTenantSortDirButton() {
    const btn = document.getElementById('tenants_sort_dir_btn');
    if (btn) {
        btn.textContent = tenantsVM.filters.sortDir === 'asc' ? '↑' : '↓';
        btn.title = tenantsVM.filters.sortDir === 'asc' ? 'Ascending' : 'Descending';
    }
}

function applyTenantFilters() {
    const { query, sortKey, sortDir } = tenantsVM.filters;
    let filtered = [...tenantsVM.items];

    // Text search
    if (query) {
        filtered = filtered.filter(t => {
            const searchText = [
                t.name || '',
                t.business_unit || '',
                t.description || '',
                t.contact_name || '',
                t.contact_email || '',
                t.id || '',
                t.login_domain || '',
            ].join(' ').toLowerCase();
            return searchText.includes(query);
        });
    }

    // Sort
    filtered.sort((a, b) => {
        let aVal, bVal;
        switch (sortKey) {
            case 'name':
                aVal = (a.name || '').toLowerCase();
                bVal = (b.name || '').toLowerCase();
                break;
            case 'created':
                aVal = a.created_at || '';
                bVal = b.created_at || '';
                break;
            case 'contact':
                aVal = (a.contact_name || '').toLowerCase();
                bVal = (b.contact_name || '').toLowerCase();
                break;
            default:
                aVal = (a.name || '').toLowerCase();
                bVal = (b.name || '').toLowerCase();
        }
        if (aVal < bVal) return sortDir === 'asc' ? -1 : 1;
        if (aVal > bVal) return sortDir === 'asc' ? 1 : -1;
        return 0;
    });

    tenantsVM.filtered = filtered;
    tenantsVM.stats.filtered = filtered.length;

    // Update counts
    const totalEl = document.getElementById('tenants_total_count');
    const showingEl = document.getElementById('tenants_showing_count');
    if (totalEl) totalEl.textContent = tenantsVM.stats.total;
    if (showingEl) showingEl.textContent = tenantsVM.stats.filtered;

    renderTenantsFiltered();
}

function initTenantModal() {
    const modal = document.getElementById('tenant_modal');
    if (!modal || tenantModalInitialized) return;
    tenantModalInitialized = true;
    const closeBtn = document.getElementById('tenant_modal_close_x');
    const cancelBtn = document.getElementById('tenant_cancel');
    const saveBtn = document.getElementById('tenant_save');
    if (closeBtn) closeBtn.addEventListener('click', closeTenantModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeTenantModal);
    if (saveBtn) saveBtn.addEventListener('click', submitTenantForm);
    modal.addEventListener('click', (e) => {
        if (e.target === modal) closeTenantModal();
    });
}

function initTenantsSubTabs() {
    if (tenantsSubtabsInitialized) return;
    const bar = document.getElementById('tenants_subtab_bar');
    if (!bar) return;
    tenantsSubtabsInitialized = true;
    bar.querySelectorAll('.tenants-subtab').forEach(btn => {
        btn.addEventListener('click', () => {
            const target = btn.getAttribute('data-tenantsview');
            switchTenantsView(target);
        });
    });
}

function switchTenantsView(view, force = false) {
    if (!view) return;
    if (!force && view === activeTenantsView) return;
    const container = document.querySelector('[data-tab="tenants"]');
    if (container) {
        container.querySelectorAll('.tenants-subtab').forEach(btn => {
            const target = btn.getAttribute('data-tenantsview');
            if (target === view) {
                btn.classList.add('active');
            } else {
                btn.classList.remove('active');
            }
        });
        container.querySelectorAll('[data-tenantsview-panel]').forEach(panel => {
            const panelView = panel.getAttribute('data-tenantsview-panel');
            if (panelView === view) {
                panel.classList.remove('hidden');
            } else {
                panel.classList.add('hidden');
            }
        });
    }
    activeTenantsView = view;
    persistUIState(SERVER_UI_STATE_KEYS.TENANTS_VIEW, view);
}

// Callback for after new tenant is created via dropdown "Add Tenant" option
let _tenantDropdownCallback = null;

/**
 * Populate a tenant dropdown select with options and "Add Tenant" entry
 * @param {HTMLSelectElement} selectEl - The select element to populate
 * @param {Object} options - Configuration options
 * @param {string} options.selectedId - ID of tenant to select
 * @param {string} options.placeholder - Placeholder text for empty option
 * @param {boolean} options.showAddOption - Whether to show "Add Tenant" option (default true)
 * @param {boolean} options.required - Whether to include empty placeholder option
 */
function populateTenantDropdown(selectEl, options = {}) {
    if (!selectEl) return;
    const {
        selectedId = '',
        placeholder = 'Select tenant…',
        showAddOption = true,
        required = false
    } = options;

    const tenants = window._tenants || [];
    selectEl.innerHTML = '';

    // Add placeholder option if not required
    if (!required) {
        const emptyOpt = document.createElement('option');
        emptyOpt.value = '';
        emptyOpt.textContent = placeholder;
        selectEl.appendChild(emptyOpt);
    }

    // Add tenant options
    tenants.forEach(t => {
        const opt = document.createElement('option');
        opt.value = t.id;
        opt.textContent = t.name;
        if (t.id === selectedId) opt.selected = true;
        selectEl.appendChild(opt);
    });

    // Add "Add Tenant" option
    if (showAddOption) {
        const addOpt = document.createElement('option');
        addOpt.value = '__add_new_tenant__';
        addOpt.textContent = '+ Add Tenant…';
        addOpt.style.fontWeight = 'bold';
        addOpt.style.fontStyle = 'italic';
        selectEl.appendChild(addOpt);
    }

    // Remove any existing listener to avoid duplicates
    selectEl.removeEventListener('change', handleTenantDropdownChange);
    if (showAddOption) {
        selectEl.addEventListener('change', handleTenantDropdownChange);
    }
}

function handleTenantDropdownChange(e) {
    const selectEl = e.target;
    if (selectEl.value !== '__add_new_tenant__') return;

    // Reset to first option to prevent showing "Add Tenant" as selected
    selectEl.selectedIndex = 0;

    // Store callback to update this dropdown after tenant is created
    _tenantDropdownCallback = {
        selectEl: selectEl,
        previousValue: selectEl.value
    };

    // Open tenant modal for new tenant
    openTenantModal(null);
}

function openTenantModal(tenant) {
    const modal = document.getElementById('tenant_modal');
    if (!modal) return;
    const isEdit = tenant && tenant.id;
    if (isEdit) {
        modal.setAttribute('data-edit-id', tenant.id);
        document.getElementById('tenant_modal_title').textContent = 'Edit Tenant';
        document.getElementById('tenant_save').textContent = 'Save Changes';
    } else {
        modal.removeAttribute('data-edit-id');
        document.getElementById('tenant_modal_title').textContent = 'New Customer';
        document.getElementById('tenant_save').textContent = 'Create & Onboard';
    }
    const safe = (key) => (tenant && tenant[key]) ? tenant[key] : '';
    document.getElementById('tenant_name').value = safe('name');
    document.getElementById('tenant_login_domain').value = safe('login_domain');
    document.getElementById('tenant_contact_name').value = safe('contact_name');
    document.getElementById('tenant_contact_email').value = safe('contact_email');
    document.getElementById('tenant_contact_phone').value = safe('contact_phone');
    document.getElementById('tenant_billing_code').value = safe('billing_code');
    document.getElementById('tenant_address').value = safe('address');
    document.getElementById('tenant_description').value = safe('description');
    const errEl = document.getElementById('tenant_error');
    if (errEl) errEl.textContent = '';

    // Show/hide onboarding section based on new vs edit mode
    const onboardingSection = document.getElementById('tenant_onboarding_section');
    if (onboardingSection) {
        onboardingSection.style.display = isEdit ? 'none' : 'block';
    }

    // Reset onboarding toggles and fields
    const inviteToggle = document.getElementById('tenant_invite_admin_toggle');
    const agentToggle = document.getElementById('tenant_send_agent_toggle');
    const inviteFields = document.getElementById('tenant_invite_admin_fields');
    const agentFields = document.getElementById('tenant_send_agent_fields');
    const smtpWarning = document.getElementById('tenant_smtp_warning');

    if (inviteToggle) {
        inviteToggle.checked = false;
        inviteToggle.onchange = () => {
            if (inviteFields) inviteFields.style.display = inviteToggle.checked ? 'flex' : 'none';
            updateOnboardingSMTPWarning();
        };
    }
    if (agentToggle) {
        agentToggle.checked = false;
        agentToggle.onchange = () => {
            if (agentFields) agentFields.style.display = agentToggle.checked ? 'flex' : 'none';
            updateOnboardingSMTPWarning();
        };
    }
    if (inviteFields) inviteFields.style.display = 'none';
    if (agentFields) agentFields.style.display = 'none';

    // Reset onboarding field values
    const adminEmailEl = document.getElementById('tenant_admin_email');
    const adminUsernameEl = document.getElementById('tenant_admin_username');
    const agentEmailEl = document.getElementById('tenant_agent_email');
    const agentPlatformEl = document.getElementById('tenant_agent_platform');
    if (adminEmailEl) adminEmailEl.value = '';
    if (adminUsernameEl) adminUsernameEl.value = '';
    if (agentEmailEl) agentEmailEl.value = '';
    if (agentPlatformEl) agentPlatformEl.value = 'windows';

    // Auto-populate from contact email if available
    const contactEmail = safe('contact_email');
    if (!isEdit && contactEmail) {
        if (adminEmailEl) adminEmailEl.value = contactEmail;
        if (agentEmailEl) agentEmailEl.value = contactEmail;
    }

    // Auto-sync contact email to onboarding fields when changed (for new tenants only)
    const contactEmailEl = document.getElementById('tenant_contact_email');
    if (!isEdit && contactEmailEl) {
        contactEmailEl.oninput = () => {
            const val = contactEmailEl.value.trim();
            // Only auto-fill if the onboarding fields haven't been manually edited
            if (adminEmailEl && !adminEmailEl.dataset.manuallyEdited) {
                adminEmailEl.value = val;
            }
            if (agentEmailEl && !agentEmailEl.dataset.manuallyEdited) {
                agentEmailEl.value = val;
            }
        };
    }

    // Track manual edits to onboarding email fields
    if (adminEmailEl) {
        adminEmailEl.dataset.manuallyEdited = '';
        adminEmailEl.oninput = () => { adminEmailEl.dataset.manuallyEdited = 'true'; };
    }
    if (agentEmailEl) {
        agentEmailEl.dataset.manuallyEdited = '';
        agentEmailEl.oninput = () => { agentEmailEl.dataset.manuallyEdited = 'true'; };
    }

    // Update SMTP warning visibility
    updateOnboardingSMTPWarning();

    modal.style.display = 'flex';
    setTimeout(() => {
        try { document.getElementById('tenant_name').focus(); } catch (e) { }
    }, 10);
}

// Update SMTP warning for onboarding section
function updateOnboardingSMTPWarning() {
    const smtpWarning = document.getElementById('tenant_smtp_warning');
    const inviteToggle = document.getElementById('tenant_invite_admin_toggle');
    const agentToggle = document.getElementById('tenant_send_agent_toggle');

    if (!smtpWarning) return;

    const needsSMTP = (inviteToggle && inviteToggle.checked) || (agentToggle && agentToggle.checked);
    smtpWarning.style.display = (!smtpEnabled && needsSMTP) ? 'flex' : 'none';
}

function closeTenantModal() {
    const modal = document.getElementById('tenant_modal');
    if (!modal) return;
    modal.style.display = 'none';
    modal.removeAttribute('data-edit-id');
    const errEl = document.getElementById('tenant_error');
    if (errEl) errEl.textContent = '';

    // Reset onboarding fields
    const inviteToggle = document.getElementById('tenant_invite_admin_toggle');
    const agentToggle = document.getElementById('tenant_send_agent_toggle');
    const inviteFields = document.getElementById('tenant_invite_admin_fields');
    const agentFields = document.getElementById('tenant_send_agent_fields');

    if (inviteToggle) inviteToggle.checked = false;
    if (agentToggle) agentToggle.checked = false;
    if (inviteFields) inviteFields.style.display = 'none';
    if (agentFields) agentFields.style.display = 'none';
}

function collectTenantFormData() {
    return {
        name: (document.getElementById('tenant_name').value || '').trim(),
        description: (document.getElementById('tenant_description').value || '').trim(),
        contact_name: (document.getElementById('tenant_contact_name').value || '').trim(),
        contact_email: (document.getElementById('tenant_contact_email').value || '').trim(),
        contact_phone: (document.getElementById('tenant_contact_phone').value || '').trim(),
        billing_code: (document.getElementById('tenant_billing_code').value || '').trim(),
        address: (document.getElementById('tenant_address').value || '').trim(),
        login_domain: (document.getElementById('tenant_login_domain').value || '').trim()
    };
}

async function submitTenantForm() {
    const modal = document.getElementById('tenant_modal');
    const errEl = document.getElementById('tenant_error');
    if (errEl) errEl.style.display = 'none';
    const payload = collectTenantFormData();
    if (!payload.name) {
        if (errEl) {
            errEl.textContent = 'Name is required';
            errEl.style.display = 'block';
        }
        return;
    }
    const editId = modal ? modal.getAttribute('data-edit-id') : '';

    // Collect onboarding options (only for new tenants)
    const inviteToggle = document.getElementById('tenant_invite_admin_toggle');
    const agentToggle = document.getElementById('tenant_send_agent_toggle');
    const inviteAdmin = !editId && inviteToggle && inviteToggle.checked;
    const sendAgentEmail = !editId && agentToggle && agentToggle.checked;

    // Validate onboarding fields if enabled
    if (inviteAdmin) {
        const adminEmail = (document.getElementById('tenant_admin_email').value || '').trim();
        if (!adminEmail) {
            if (errEl) {
                errEl.textContent = 'Admin email is required when inviting an admin';
                errEl.style.display = 'block';
            }
            return;
        }
        if (!adminEmail.includes('@') || !adminEmail.includes('.')) {
            if (errEl) {
                errEl.textContent = 'Please enter a valid admin email address';
                errEl.style.display = 'block';
            }
            return;
        }
    }

    if (sendAgentEmail) {
        const agentEmail = (document.getElementById('tenant_agent_email').value || '').trim();
        if (!agentEmail) {
            if (errEl) {
                errEl.textContent = 'Recipient email is required when sending agent deployment email';
                errEl.style.display = 'block';
            }
            return;
        }
        if (!agentEmail.includes('@') || !agentEmail.includes('.')) {
            if (errEl) {
                errEl.textContent = 'Please enter a valid recipient email address';
                errEl.style.display = 'block';
            }
            return;
        }
    }

    try {
        let newTenantId = null;
        let tenantName = payload.name;

        if (editId) {
            await updateTenant(editId, payload);
            window.__pm_shared.showToast('Tenant updated', 'success');
        } else {
            const result = await createTenant(payload);
            newTenantId = result && result.id ? result.id : null;
            window.__pm_shared.showToast('Customer created', 'success');

            // Handle onboarding actions after tenant is created
            if (newTenantId) {
                const onboardingResults = [];

                // Invite admin if enabled
                if (inviteAdmin && smtpEnabled) {
                    const adminEmail = (document.getElementById('tenant_admin_email').value || '').trim();
                    const adminUsername = (document.getElementById('tenant_admin_username').value || '').trim();
                    try {
                        const invitePayload = {
                            email: adminEmail,
                            role: 'admin',
                            tenant_id: newTenantId
                        };
                        if (adminUsername) invitePayload.username = adminUsername;

                        const r = await fetch('/api/v1/users/invite', {
                            method: 'POST',
                            headers: { 'content-type': 'application/json' },
                            body: JSON.stringify(invitePayload)
                        });
                        if (r.ok) {
                            onboardingResults.push({ type: 'invite', success: true, email: adminEmail });
                        } else {
                            const errText = await r.text();
                            onboardingResults.push({ type: 'invite', success: false, email: adminEmail, error: errText });
                        }
                    } catch (invErr) {
                        onboardingResults.push({ type: 'invite', success: false, email: adminEmail, error: invErr.message || 'Unknown error' });
                    }
                }

                // Send agent deployment email if enabled
                if (sendAgentEmail && smtpEnabled) {
                    const agentEmail = (document.getElementById('tenant_agent_email').value || '').trim();
                    const agentPlatform = document.getElementById('tenant_agent_platform').value || 'windows';
                    try {
                        const agentPayload = {
                            tenant_id: newTenantId,
                            platform: agentPlatform,
                            email: agentEmail,
                            ttl_minutes: 1440 // 24 hours
                        };

                        const r = await fetch('/api/v1/packages/send-email', {
                            method: 'POST',
                            headers: { 'content-type': 'application/json' },
                            body: JSON.stringify(agentPayload)
                        });
                        if (r.ok) {
                            onboardingResults.push({ type: 'agent', success: true, email: agentEmail });
                        } else {
                            const errText = await r.text();
                            onboardingResults.push({ type: 'agent', success: false, email: agentEmail, error: errText });
                        }
                    } catch (agErr) {
                        onboardingResults.push({ type: 'agent', success: false, email: agentEmail, error: agErr.message || 'Unknown error' });
                    }
                }

                // Show onboarding results summary
                if (onboardingResults.length > 0) {
                    showOnboardingResults(tenantName, onboardingResults);
                }
            }
        }
        closeTenantModal();
        await loadTenants();

        // If we have a dropdown callback and this was a new tenant, update the dropdown
        if (_tenantDropdownCallback && newTenantId) {
            const { selectEl } = _tenantDropdownCallback;
            if (selectEl && selectEl.isConnected) {
                populateTenantDropdown(selectEl, { selectedId: newTenantId });
            }
            _tenantDropdownCallback = null;
        }
    } catch (err) {
        const message = (err && err.message) ? err.message : 'Failed to save tenant';
        if (errEl) {
            errEl.textContent = message;
            errEl.style.display = 'block';
        }
    }
}

// Show onboarding results summary
function showOnboardingResults(tenantName, results) {
    const successes = results.filter(r => r.success);
    const failures = results.filter(r => !r.success);

    let message = '';

    if (successes.length > 0 && failures.length === 0) {
        // All successful
        const parts = [];
        const invite = successes.find(r => r.type === 'invite');
        const agent = successes.find(r => r.type === 'agent');
        if (invite) parts.push(`Admin invitation sent to ${invite.email}`);
        if (agent) parts.push(`Agent deployment email sent to ${agent.email}`);
        message = parts.join('. ') + '.';
        window.__pm_shared.showToast(message, 'success');
    } else if (failures.length > 0 && successes.length === 0) {
        // All failed
        const parts = [];
        for (const f of failures) {
            const label = f.type === 'invite' ? 'Admin invite' : 'Agent email';
            parts.push(`${label} failed: ${f.error}`);
        }
        window.__pm_shared.showAlert(parts.join('\n\n'), 'Onboarding Errors', true, false);
    } else {
        // Mixed results
        let msg = 'Onboarding partially completed:\n\n';
        for (const s of successes) {
            const label = s.type === 'invite' ? 'Admin invitation' : 'Agent deployment email';
            msg += `✓ ${label} sent to ${s.email}\n`;
        }
        for (const f of failures) {
            const label = f.type === 'invite' ? 'Admin invite' : 'Agent email';
            msg += `✗ ${label} to ${f.email} failed: ${f.error}\n`;
        }
        window.__pm_shared.showAlert(msg, 'Onboarding Results', false, false);
    }
}

// ====== Users UI ======
let smtpEnabled = false;

function initUsersUI() {
    if (usersUIInitialized) return;
    usersUIInitialized = true;

    const btn = document.getElementById('new_user_btn');
    if (btn) {
        btn.addEventListener('click', () => {
            openUserModal();
        });
    }

    // Invite user button
    const inviteBtn = document.getElementById('invite_user_btn');
    if (inviteBtn) {
        inviteBtn.addEventListener('click', () => {
            openInviteModal();
        });
    }

    // Wire user modal close/buttons
    const userModal = document.getElementById('user_modal');
    if (userModal) {
        document.getElementById('user_modal_close_x').addEventListener('click', () => closeUserModal());
        document.getElementById('user_cancel').addEventListener('click', () => closeUserModal());
        document.getElementById('user_submit').addEventListener('click', submitCreateUser);

        // Password field live validation
        const pwField = document.getElementById('user_password');
        const pwConfirmField = document.getElementById('user_password_confirm');
        if (pwField) {
            pwField.addEventListener('input', updatePasswordStrength);
        }
        if (pwConfirmField) {
            pwConfirmField.addEventListener('input', updatePasswordStrength);
        }

        // Change password toggle for edit mode
        const changePwCheckbox = document.getElementById('user_change_password');
        if (changePwCheckbox) {
            changePwCheckbox.addEventListener('change', (e) => {
                const fields = document.getElementById('user_password_fields');
                if (fields) {
                    fields.classList.toggle('collapsed', !e.target.checked);
                }
            });
        }
    }

    // Wire invite modal close/buttons
    const inviteModal = document.getElementById('invite_user_modal');
    if (inviteModal) {
        document.getElementById('invite_user_modal_close_x').addEventListener('click', () => closeInviteModal());
        document.getElementById('invite_user_cancel').addEventListener('click', () => closeInviteModal());
        document.getElementById('invite_user_submit').addEventListener('click', submitInviteUser);
    }

    // Check SMTP status
    checkSMTPStatus();

    loadUsers();
}

async function checkSMTPStatus() {
    try {
        const r = await fetch('/api/v1/server/settings');
        if (r.ok) {
            const data = await r.json();
            smtpEnabled = data.smtp?.enabled === true && data.smtp?.host;
        }
    } catch (e) {
        smtpEnabled = false;
    }
}

function closeUserModal() {
    const modal = document.getElementById('user_modal');
    if (modal) {
        modal.style.display = 'none';
        modal.removeAttribute('data-edit-id');
    }
}

function closeInviteModal() {
    const modal = document.getElementById('invite_user_modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

function openInviteModal() {
    const modal = document.getElementById('invite_user_modal');
    if (!modal) return;

    // Reset form
    document.getElementById('invite_email').value = '';
    document.getElementById('invite_username').value = '';
    document.getElementById('invite_role').value = 'viewer';
    document.getElementById('invite_error').textContent = '';

    // Populate tenant select with "Add Tenant" option
    const tenantSel = document.getElementById('invite_tenant');
    if (tenantSel) {
        populateTenantDropdown(tenantSel, {
            placeholder: '(Global / Server)',
            showAddOption: true
        });
    }

    // Show/hide SMTP warning
    const smtpWarning = document.getElementById('invite_smtp_warning');
    const submitBtn = document.getElementById('invite_user_submit');
    if (smtpEnabled) {
        if (smtpWarning) smtpWarning.style.display = 'none';
        if (submitBtn) submitBtn.disabled = false;
    } else {
        if (smtpWarning) smtpWarning.style.display = 'flex';
        if (submitBtn) submitBtn.disabled = true;
    }

    modal.style.display = 'flex';
}

async function submitInviteUser() {
    const email = document.getElementById('invite_email').value.trim();
    const username = document.getElementById('invite_username').value.trim();
    const role = document.getElementById('invite_role').value;
    const tenant = document.getElementById('invite_tenant').value;
    const errEl = document.getElementById('invite_error');

    errEl.textContent = '';

    if (!email) {
        errEl.textContent = 'Email address is required';
        return;
    }

    // Basic email validation
    if (!email.includes('@') || !email.includes('.')) {
        errEl.textContent = 'Please enter a valid email address';
        return;
    }

    try {
        const payload = { email, role };
        if (username) payload.username = username;
        if (tenant) payload.tenant_id = tenant;

        const r = await fetch('/api/v1/users/invite', {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify(payload)
        });

        if (!r.ok) {
            const txt = await r.text();
            throw new Error(txt || 'Failed to send invitation');
        }

        closeInviteModal();
        window.__pm_shared.showToast('Invitation sent to ' + email, 'success');
        loadUsers();
    } catch (err) {
        errEl.textContent = (err && err.message) ? err.message : 'Failed to send invitation';
    }
}

async function loadUsers() {
    const el = document.getElementById('users_list');
    if (!el) return;
    el.innerHTML = '<div style="color:var(--muted)">Loading users...</div>';
    try {
        // Ensure tenant directory is loaded so we can resolve IDs to names
        await ensureTenantDirectory();
        const r = await fetch('/api/v1/users');
        if (!r.ok) throw new Error(await r.text());
        const users = await r.json();
        renderUsers(users);
    } catch (err) {
        el.innerHTML = '<div style="color:var(--danger)">Error loading users: ' + escapeHtml(err.message || err) + '</div>';
    }
}

function renderUsers(list) {
    const el = document.getElementById('users_list');
    if (!el) return;
    if (!Array.isArray(list) || list.length === 0) {
        el.innerHTML = '<div class="users-empty-state"><div class="muted-text">No users found.</div></div>';
        return;
    }

    // Role badge styling
    const roleBadge = (role) => {
        const r = (role || 'viewer').toLowerCase();
        return `<span class="role-badge role-${r}">${escapeHtml(r)}</span>`;
    };

    const rows = list.map(u => {
        const username = escapeHtml(u.username || '—');
        const email = escapeHtml(u.email || '');
        const role = u.role || 'viewer';
        const tenantLabel = u.tenant_id ? formatTenantDisplay(u.tenant_id) : '';
        const tenantMarkup = tenantLabel ? `<span class="user-tenant-chip">${escapeHtml(tenantLabel)}</span>` : '<span class="user-tenant-chip global">Global</span>';
        const idAttr = escapeHtml(u.id || '');
        const usernameAttr = escapeHtml(u.username || '');
        const createdAt = u.created_at ? formatRelativeTime(new Date(u.created_at)) : '';
        const initial = (u.username || 'U')[0].toUpperCase();
        return `
            <tr data-user-id="${idAttr}">
                <td>
                    <div class="user-cell">
                        <div class="user-avatar">${initial}</div>
                        <div class="user-info">
                            <div class="user-name">${username}</div>
                            ${email ? `<div class="user-email">${escapeHtml(email)}</div>` : ''}
                        </div>
                    </div>
                </td>
                <td>${roleBadge(role)}</td>
                <td>${tenantMarkup}</td>
                <td class="user-created-col">${createdAt ? `<span title="${u.created_at}">${createdAt}</span>` : '—'}</td>
                <td class="actions-col">
                    <div class="table-actions">
                        <button class="btn-icon" data-action="user-sessions" data-id="${idAttr}" data-username="${usernameAttr}" title="View Sessions">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 8a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm2-3a2 2 0 1 1-4 0 2 2 0 0 1 4 0zm4 8c0 1-1 1-1 1H3s-1 0-1-1 1-4 6-4 6 3 6 4zm-1-.004c-.001-.246-.154-.986-.832-1.664C11.516 10.68 10.289 10 8 10c-2.29 0-3.516.68-4.168 1.332-.678.678-.83 1.418-.832 1.664h10z"/></svg>
                        </button>
                        <button class="btn-icon" data-action="edit-user" data-id="${idAttr}" title="Edit User">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M12.146.146a.5.5 0 0 1 .708 0l3 3a.5.5 0 0 1 0 .708l-10 10a.5.5 0 0 1-.168.11l-5 2a.5.5 0 0 1-.65-.65l2-5a.5.5 0 0 1 .11-.168l10-10zM11.207 2.5 13.5 4.793 14.793 3.5 12.5 1.207 11.207 2.5zm1.586 3L10.5 3.207 4 9.707V10h.5a.5.5 0 0 1 .5.5v.5h.5a.5.5 0 0 1 .5.5v.5h.293l6.5-6.5zm-9.761 5.175-.106.106-1.528 3.821 3.821-1.528.106-.106A.5.5 0 0 1 5 12.5V12h-.5a.5.5 0 0 1-.5-.5V11h-.5a.5.5 0 0 1-.468-.325z"/></svg>
                        </button>
                        <button class="btn-icon btn-danger" data-action="delete-user" data-id="${idAttr}" title="Delete User">
                            <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M5.5 5.5A.5.5 0 0 1 6 6v6a.5.5 0 0 1-1 0V6a.5.5 0 0 1 .5-.5zm2.5 0a.5.5 0 0 1 .5.5v6a.5.5 0 0 1-1 0V6a.5.5 0 0 1 .5-.5zm3 .5a.5.5 0 0 0-1 0v6a.5.5 0 0 0 1 0V6z"/><path fill-rule="evenodd" d="M14.5 3a1 1 0 0 1-1 1H13v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V4h-.5a1 1 0 0 1-1-1V2a1 1 0 0 1 1-1H6a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1h3.5a1 1 0 0 1 1 1v1zM4.118 4 4 4.059V13a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1V4.059L11.882 4H4.118zM2.5 3V2h11v1h-11z"/></svg>
                        </button>
                    </div>
                </td>
            </tr>
        `;
    }).join('\n');

    el.innerHTML = `
        <div class="users-stats-bar">
            <div class="users-stat">
                <span class="users-stat-value">${list.length}</span>
                <span class="users-stat-label">Total Users</span>
            </div>
            <div class="users-stat">
                <span class="users-stat-value">${list.filter(u => u.role === 'admin').length}</span>
                <span class="users-stat-label">Admins</span>
            </div>
            <div class="users-stat">
                <span class="users-stat-value">${list.filter(u => u.role === 'operator').length}</span>
                <span class="users-stat-label">Operators</span>
            </div>
            <div class="users-stat">
                <span class="users-stat-value">${list.filter(u => u.role === 'viewer').length}</span>
                <span class="users-stat-label">Viewers</span>
            </div>
        </div>
        <div class="panel">
            <div class="table-wrapper">
                <table class="simple-table users-table">
                    <thead>
                        <tr>
                            <th>User</th>
                            <th>Role</th>
                            <th>Tenant</th>
                            <th>Created</th>
                            <th class="actions-col">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        ${rows}
                    </tbody>
                </table>
            </div>
        </div>
    `;

    // Attach handlers for edit/delete
    el.querySelectorAll('button[data-action="edit-user"]').forEach(b => {
        b.addEventListener('click', async () => {
            const id = b.getAttribute('data-id');
            openUserEditModal(id);
        });
    });
    el.querySelectorAll('button[data-action="delete-user"]').forEach(b => {
        b.addEventListener('click', async () => {
            const id = b.getAttribute('data-id');
            if (!confirm('Delete user ID ' + id + '? This cannot be undone.')) return;
            try {
                const r = await fetch('/api/v1/users/' + encodeURIComponent(id), { method: 'DELETE' });
                if (!r.ok) throw new Error(await r.text());
                window.__pm_shared.showToast('User deleted', 'success');
                loadUsers();
            } catch (err) {
                window.__pm_shared.showAlert('Failed to delete user: ' + (err.message || err), 'Error', true, false);
            }
        });
    });
    el.querySelectorAll('button[data-action="user-sessions"]').forEach(b => {
        b.addEventListener('click', async () => {
            const id = b.getAttribute('data-id');
            const username = b.getAttribute('data-username') || '';
            await loadUserSessions(id, username);
        });
    });
}

// Track active sessions modal for proper updates
let activeSessionsModal = null;

async function loadUserSessions(userId, username) {
    try {
        const r = await fetch('/api/v1/sessions?user_id=' + encodeURIComponent(userId));
        if (!r.ok) throw new Error(await r.text());
        const sessions = await r.json();
        showSessionsModal(sessions, username, userId);
    } catch (err) {
        window.__pm_shared.showAlert('Failed to load sessions: ' + (err.message || err), 'Error', true, false);
    }
}

// Sessions modal sort state
let sessionsSort = { key: 'created_at', dir: 'desc' };

function showSessionsModal(sessions, username, userId) {
    // Remove existing modal if present (prevents stacking)
    if (activeSessionsModal && activeSessionsModal.parentNode) {
        activeSessionsModal.parentNode.removeChild(activeSessionsModal);
    }

    const currentTokenHash = currentUser?.session_token_hash || '';

    const modal = document.createElement('div');
    modal.className = 'sessions-modal-overlay';
    activeSessionsModal = modal;

    const box = document.createElement('div');
    box.className = 'sessions-modal';

    // Sort sessions
    const sortedSessions = [...(sessions || [])].sort((a, b) => {
        const aVal = a[sessionsSort.key] || '';
        const bVal = b[sessionsSort.key] || '';
        const cmp = aVal < bVal ? -1 : aVal > bVal ? 1 : 0;
        return sessionsSort.dir === 'asc' ? cmp : -cmp;
    });

    const hasCurrentSession = sortedSessions.some(s => s.token === currentTokenHash);
    const hasOtherSessions = sortedSessions.some(s => s.token !== currentTokenHash);

    const sortIcon = (key) => {
        if (sessionsSort.key !== key) return '';
        return sessionsSort.dir === 'asc' ? ' ▲' : ' ▼';
    };

    let content = `
        <div class="sessions-modal-header">
            <div class="sessions-modal-title">
                <svg width="20" height="20" viewBox="0 0 16 16" fill="currentColor"><path d="M8 8a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm2-3a2 2 0 1 1-4 0 2 2 0 0 1 4 0zm4 8c0 1-1 1-1 1H3s-1 0-1-1 1-4 6-4 6 3 6 4zm-1-.004c-.001-.246-.154-.986-.832-1.664C11.516 10.68 10.289 10 8 10c-2.29 0-3.516.68-4.168 1.332-.678.678-.83 1.418-.832 1.664h10z"/></svg>
                Sessions for ${escapeHtml(username || 'user')}
            </div>
            <button class="sessions-modal-close" data-action="close">&times;</button>
        </div>
        <div class="sessions-modal-body">
    `;

    if (!sortedSessions.length) {
        content += '<div class="sessions-empty">No active sessions found.</div>';
    } else {
        content += `
            <div class="sessions-table-wrapper">
                <table class="sessions-table">
                    <thead>
                        <tr>
                            <th data-sort-key="created_at" class="sortable">Created${sortIcon('created_at')}</th>
                            <th data-sort-key="expires_at" class="sortable">Expires${sortIcon('expires_at')}</th>
                            <th>Status</th>
                            <th class="actions-col">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
        `;

        sortedSessions.forEach(s => {
            const created = s.created_at ? new Date(s.created_at) : null;
            const expires = s.expires_at ? new Date(s.expires_at) : null;
            const createdStr = created ? formatRelativeTime(created) : '—';
            const expiresStr = expires ? formatRelativeTime(expires) : '—';
            const createdFull = created ? created.toLocaleString() : '';
            const expiresFull = expires ? expires.toLocaleString() : '';
            const isCurrent = s.token === currentTokenHash;
            const isExpired = expires && expires < new Date();

            content += `
                <tr class="${isCurrent ? 'current-session' : ''} ${isExpired ? 'expired-session' : ''}">
                    <td title="${escapeHtml(createdFull)}">${createdStr}</td>
                    <td title="${escapeHtml(expiresFull)}">${expiresStr}</td>
                    <td>
                        ${isCurrent ? '<span class="session-badge current">Current</span>' : ''}
                        ${isExpired ? '<span class="session-badge expired">Expired</span>' : ''}
                        ${!isCurrent && !isExpired ? '<span class="session-badge active">Active</span>' : ''}
                    </td>
                    <td class="actions-col">
                        ${isCurrent ? `
                            <button class="btn-sm btn-outline" data-action="revoke-others" title="End all other sessions">End Others</button>
                        ` : `
                            <button class="btn-sm btn-danger" data-action="revoke-session" data-key="${escapeHtml(s.token || '')}">Revoke</button>
                        `}
                    </td>
                </tr>
            `;
        });

        content += `
                    </tbody>
                </table>
            </div>
        `;
    }

    content += '</div>';

    // Footer with bulk actions
    if (sortedSessions.length > 0) {
        content += `
            <div class="sessions-modal-footer">
                <div class="sessions-count">${sortedSessions.length} session${sortedSessions.length !== 1 ? 's' : ''}</div>
                <div class="sessions-actions">
                    <button class="btn-outline" data-action="close">Close</button>
                    <button class="btn-danger" data-action="revoke-all">End All Sessions</button>
                </div>
            </div>
        `;
    } else {
        content += `
            <div class="sessions-modal-footer">
                <div class="sessions-actions">
                    <button class="btn-outline" data-action="close">Close</button>
                </div>
            </div>
        `;
    }

    box.innerHTML = content;
    modal.appendChild(box);
    document.body.appendChild(modal);

    // Close handlers - use querySelectorAll for both close button and X button
    modal.querySelectorAll('[data-action="close"]').forEach(btn => {
        btn.addEventListener('click', () => {
            document.body.removeChild(modal);
            activeSessionsModal = null;
        });
    });
    modal.querySelectorAll('.sessions-modal-close').forEach(btn => {
        btn.addEventListener('click', () => {
            document.body.removeChild(modal);
            activeSessionsModal = null;
        });
    });
    modal.addEventListener('click', (e) => {
        if (e.target === modal) {
            document.body.removeChild(modal);
            activeSessionsModal = null;
        }
    });

    // Sort handlers
    modal.querySelectorAll('th[data-sort-key]').forEach(th => {
        th.addEventListener('click', () => {
            const key = th.getAttribute('data-sort-key');
            if (sessionsSort.key === key) {
                sessionsSort.dir = sessionsSort.dir === 'asc' ? 'desc' : 'asc';
            } else {
                sessionsSort.key = key;
                sessionsSort.dir = 'desc';
            }
            showSessionsModal(sessions, username, userId);
        });
    });

    // Revoke single session
    modal.querySelectorAll('button[data-action="revoke-session"]').forEach(b => {
        b.addEventListener('click', async () => {
            const key = b.getAttribute('data-key');
            if (!await window.__pm_shared.showConfirm('Revoke this session?', 'Confirm')) return;
            try {
                const r = await fetch('/api/v1/sessions/' + encodeURIComponent(key), { method: 'DELETE' });
                if (!r.ok) throw new Error(await r.text());
                window.__pm_shared.showToast('Session revoked', 'success');
                await loadUserSessions(userId, username);
            } catch (err) {
                window.__pm_shared.showAlert('Failed to revoke session: ' + (err.message || err), 'Error', true, false);
            }
        });
    });

    // Revoke all other sessions
    modal.querySelectorAll('button[data-action="revoke-others"]').forEach(b => {
        b.addEventListener('click', async () => {
            const otherSessions = sortedSessions.filter(s => s.token !== currentTokenHash);
            if (otherSessions.length === 0) {
                window.__pm_shared.showToast('No other sessions to revoke', 'info');
                return;
            }
            if (!await window.__pm_shared.showConfirm(`End ${otherSessions.length} other session${otherSessions.length !== 1 ? 's' : ''}?`, 'Confirm')) return;
            try {
                for (const s of otherSessions) {
                    await fetch('/api/v1/sessions/' + encodeURIComponent(s.token), { method: 'DELETE' });
                }
                window.__pm_shared.showToast('Other sessions ended', 'success');
                await loadUserSessions(userId, username);
            } catch (err) {
                window.__pm_shared.showAlert('Failed to revoke sessions: ' + (err.message || err), 'Error', true, false);
            }
        });
    });

    // Revoke all sessions
    modal.querySelectorAll('button[data-action="revoke-all"]').forEach(b => {
        b.addEventListener('click', async () => {
            const willLogOut = sortedSessions.some(s => s.token === currentTokenHash);
            const msg = willLogOut
                ? `End all ${sortedSessions.length} session${sortedSessions.length !== 1 ? 's' : ''}? You will be logged out.`
                : `End all ${sortedSessions.length} session${sortedSessions.length !== 1 ? 's' : ''}?`;
            if (!await window.__pm_shared.showConfirm(msg, 'Confirm')) return;
            try {
                for (const s of sortedSessions) {
                    await fetch('/api/v1/sessions/' + encodeURIComponent(s.token), { method: 'DELETE' });
                }
                window.__pm_shared.showToast('All sessions ended', 'success');
                if (willLogOut) {
                    window.location.href = '/login';
                } else {
                    await loadUserSessions(userId, username);
                }
            } catch (err) {
                window.__pm_shared.showAlert('Failed to revoke sessions: ' + (err.message || err), 'Error', true, false);
            }
        });
    });
}

// Cached password policy
let passwordPolicy = null;

async function loadPasswordPolicy() {
    try {
        const r = await fetch('/api/v1/users/password-policy');
        if (r.ok) {
            passwordPolicy = await r.json();
            updatePasswordHint();
        }
    } catch (err) {
        console.warn('Failed to load password policy:', err);
    }
    return passwordPolicy;
}

function updatePasswordHint() {
    const hint = document.getElementById('user_password_hint');
    if (!hint || !passwordPolicy) return;
    const parts = [`min ${passwordPolicy.min_length || 8} chars`];
    if (passwordPolicy.require_uppercase) parts.push('uppercase');
    if (passwordPolicy.require_lowercase) parts.push('lowercase');
    if (passwordPolicy.require_number) parts.push('number');
    if (passwordPolicy.require_special) parts.push('special char');
    hint.textContent = 'Requirements: ' + parts.join(', ');
}

function validatePasswordClient(password) {
    if (!passwordPolicy) return null;
    const errors = [];
    if (password.length < (passwordPolicy.min_length || 8)) {
        errors.push(`at least ${passwordPolicy.min_length || 8} characters`);
    }
    if (passwordPolicy.require_uppercase && !/[A-Z]/.test(password)) {
        errors.push('an uppercase letter');
    }
    if (passwordPolicy.require_lowercase && !/[a-z]/.test(password)) {
        errors.push('a lowercase letter');
    }
    if (passwordPolicy.require_number && !/[0-9]/.test(password)) {
        errors.push('a number');
    }
    if (passwordPolicy.require_special && !/[!@#$%^&*(),.?":{}|<>]/.test(password)) {
        errors.push('a special character');
    }
    if (errors.length > 0) {
        return 'Password must contain: ' + errors.join(', ');
    }
    return null;
}

// Password strength evaluation
function evaluatePasswordStrength(password) {
    if (!password) return { score: 0, label: 'Enter a password', strength: '' };

    let score = 0;
    const checks = {
        length: password.length >= (passwordPolicy?.min_length || 8),
        uppercase: /[A-Z]/.test(password),
        lowercase: /[a-z]/.test(password),
        number: /[0-9]/.test(password),
        special: /[!@#$%^&*(),.?":{}|<>_\-+=\[\]\\;'`~]/.test(password),
    };

    if (checks.length) score++;
    if (checks.uppercase) score++;
    if (checks.lowercase) score++;
    if (checks.number) score++;
    if (checks.special) score++;
    if (password.length >= 12) score++;
    if (password.length >= 16) score++;

    let strength, label;
    if (score <= 2) { strength = 'weak'; label = 'Weak password'; }
    else if (score <= 3) { strength = 'fair'; label = 'Fair password'; }
    else if (score <= 5) { strength = 'good'; label = 'Good password'; }
    else { strength = 'strong'; label = 'Strong password'; }

    return { score, label, strength, checks };
}

function updatePasswordStrength() {
    const password = document.getElementById('user_password')?.value || '';
    const confirmPassword = document.getElementById('user_password_confirm')?.value || '';
    const fill = document.getElementById('user_password_strength_fill');
    const text = document.getElementById('user_password_strength_text');
    const requirements = document.getElementById('user_password_requirements');

    const result = evaluatePasswordStrength(password);

    if (fill) {
        fill.setAttribute('data-strength', result.strength);
    }
    if (text) {
        text.textContent = result.label;
    }

    if (requirements) {
        // Update each requirement indicator
        const reqs = {
            length: password.length >= (passwordPolicy?.min_length || 8),
            uppercase: !passwordPolicy?.require_uppercase || /[A-Z]/.test(password),
            lowercase: !passwordPolicy?.require_lowercase || /[a-z]/.test(password),
            number: !passwordPolicy?.require_number || /[0-9]/.test(password),
            special: !passwordPolicy?.require_special || /[!@#$%^&*(),.?":{}|<>_\-+=\[\]\\;'`~]/.test(password),
            match: password && confirmPassword && password === confirmPassword,
        };

        requirements.querySelectorAll('.requirement').forEach(el => {
            const reqType = el.getAttribute('data-req');
            // Hide requirement if policy doesn't require it
            if (reqType === 'uppercase' && !passwordPolicy?.require_uppercase) {
                el.style.display = 'none';
                return;
            }
            if (reqType === 'lowercase' && !passwordPolicy?.require_lowercase) {
                el.style.display = 'none';
                return;
            }
            if (reqType === 'number' && !passwordPolicy?.require_number) {
                el.style.display = 'none';
                return;
            }
            if (reqType === 'special' && !passwordPolicy?.require_special) {
                el.style.display = 'none';
                return;
            }
            el.style.display = '';
            el.classList.toggle('met', reqs[reqType] === true);
        });

        // Update min length text
        const lengthReq = requirements.querySelector('[data-req="length"]');
        if (lengthReq) {
            const minLen = passwordPolicy?.min_length || 8;
            lengthReq.innerHTML = `<span class="req-icon"></span> Minimum ${minLen} characters`;
        }
    }
}

async function openUserModal(editMode = false) {
    const modal = document.getElementById('user_modal');
    if (!modal) return;

    // Load password policy if not cached
    if (!passwordPolicy) await loadPasswordPolicy();

    // Populate tenant select from cached tenants with "Add Tenant" option
    const sel = document.getElementById('user_tenant');
    if (sel) {
        populateTenantDropdown(sel, {
            placeholder: '(Global / Server)',
            showAddOption: true
        });
    }

    // Clear edit mode
    modal.removeAttribute('data-edit-id');

    // Set title and button text
    document.getElementById('user_modal_title').textContent = 'Add User';
    document.getElementById('user_submit').textContent = 'Create User';

    // Reset fields
    document.getElementById('user_username').value = '';
    document.getElementById('user_email').value = '';
    document.getElementById('user_password').value = '';
    document.getElementById('user_password_confirm').value = '';
    document.getElementById('user_role').value = 'viewer';
    document.getElementById('user_tenant').value = '';
    document.getElementById('user_error').textContent = '';

    // Show password section for new users, hide change password toggle
    const changePwToggle = document.getElementById('user_change_password_toggle');
    const changePwCheckbox = document.getElementById('user_change_password');
    const pwFields = document.getElementById('user_password_fields');
    const pwRequired = document.getElementById('user_password_required');
    const pwConfirmRequired = document.getElementById('user_password_confirm_required');

    if (changePwToggle) changePwToggle.style.display = 'none';
    if (changePwCheckbox) changePwCheckbox.checked = false;
    if (pwFields) pwFields.classList.remove('collapsed');
    if (pwRequired) pwRequired.style.display = '';
    if (pwConfirmRequired) pwConfirmRequired.style.display = '';

    // Reset password strength
    updatePasswordStrength();

    modal.style.display = 'flex';
    document.getElementById('user_username').focus();
}

async function submitCreateUser() {
    const modal = document.getElementById('user_modal');
    const username = document.getElementById('user_username').value.trim();
    const email = document.getElementById('user_email').value.trim();
    const password = document.getElementById('user_password').value;
    const confirmPassword = document.getElementById('user_password_confirm').value;
    const role = document.getElementById('user_role').value || 'viewer';
    const tenant = document.getElementById('user_tenant').value || '';
    const errEl = document.getElementById('user_error');
    const editId = modal.getAttribute('data-edit-id');
    const changePwCheckbox = document.getElementById('user_change_password');
    const isChangingPassword = !editId || (changePwCheckbox && changePwCheckbox.checked);

    errEl.textContent = '';

    if (!username) {
        errEl.textContent = 'Username is required';
        return;
    }

    // Password required for new users, optional for edit (only if checkbox checked)
    if (!editId && !password) {
        errEl.textContent = 'Password is required for new users';
        return;
    }

    // Validate password if changing it
    if (isChangingPassword && password) {
        if (!passwordPolicy) await loadPasswordPolicy();
        const pwError = validatePasswordClient(password);
        if (pwError) {
            errEl.textContent = pwError;
            return;
        }
        if (password !== confirmPassword) {
            errEl.textContent = 'Passwords do not match';
            return;
        }
    }

    try {
        const payload = { username, role };
        if (email) payload.email = email;
        if (isChangingPassword && password) payload.password = password;
        if (tenant) payload.tenant_id = tenant;

        let r;
        if (editId) {
            // update existing
            r = await fetch('/api/v1/users/' + encodeURIComponent(editId), { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
        } else {
            r = await fetch('/api/v1/users', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
        }
        if (!r.ok) {
            const txt = await r.text();
            throw new Error(txt || 'Request failed');
        }
        await r.json();
        closeUserModal();
        window.__pm_shared.showToast(editId ? 'User updated' : 'User created', 'success');
        loadUsers();
    } catch (err) {
        errEl.textContent = (err && err.message) ? err.message : 'Failed to save user';
    }
}

// Open modal for editing an existing user
async function openUserEditModal(id) {
    try {
        // Load password policy if not cached
        if (!passwordPolicy) await loadPasswordPolicy();

        // Populate tenant select with "Add Tenant" option
        const sel = document.getElementById('user_tenant');
        if (sel) {
            populateTenantDropdown(sel, {
                placeholder: '(Global / Server)',
                showAddOption: true
            });
        }

        const r = await fetch('/api/v1/users/' + encodeURIComponent(id));
        if (!r.ok) throw new Error(await r.text());
        const u = await r.json();
        const modal = document.getElementById('user_modal');

        // Set title and button for edit mode
        document.getElementById('user_modal_title').textContent = 'Edit User';
        document.getElementById('user_submit').textContent = 'Save Changes';

        // Populate fields
        document.getElementById('user_username').value = u.username || '';
        document.getElementById('user_email').value = u.email || '';
        document.getElementById('user_password').value = '';
        document.getElementById('user_password_confirm').value = '';
        document.getElementById('user_role').value = u.role || 'viewer';
        document.getElementById('user_tenant').value = u.tenant_id || '';
        document.getElementById('user_error').textContent = '';

        // Store editing id on modal
        modal.setAttribute('data-edit-id', id);

        // Show change password toggle, hide password fields by default
        const changePwToggle = document.getElementById('user_change_password_toggle');
        const changePwCheckbox = document.getElementById('user_change_password');
        const pwFields = document.getElementById('user_password_fields');
        const pwRequired = document.getElementById('user_password_required');
        const pwConfirmRequired = document.getElementById('user_password_confirm_required');

        if (changePwToggle) changePwToggle.style.display = '';
        if (changePwCheckbox) changePwCheckbox.checked = false;
        if (pwFields) pwFields.classList.add('collapsed');
        if (pwRequired) pwRequired.style.display = 'none';
        if (pwConfirmRequired) pwConfirmRequired.style.display = 'none';

        // Reset password strength
        updatePasswordStrength();

        modal.style.display = 'flex';
    } catch (err) {
        window.__pm_shared.showAlert('Failed to load user: ' + (err.message || err), 'Error', true, false);
    }
}

async function loadTenants() {
    const el = document.getElementById('tenants_list');
    if (!el) return;
    el.innerHTML = '<div style="color:var(--muted)">Loading tenants...</div>';
    try {
        const r = await fetch('/api/v1/tenants');
        if (!r.ok) throw new Error(await r.text());
        const data = await r.json();
        // Cache tenants for use in other UI flows (e.g. add-agent modal)
        updateTenantDirectory(Array.isArray(data) ? data : []);
        syncSSOTenants(data);

        // Store in view model and apply filters
        tenantsVM.items = Array.isArray(data) ? data : [];
        tenantsVM.stats.total = tenantsVM.items.length;
        tenantsVM.loaded = true;
        applyTenantFilters();

        notifyManagedSettingsTenantDirectory(data);
    } catch (err) {
        el.innerHTML = '<div style="color:var(--danger)">Error loading tenants: ' + escapeHtml(err.message || err) + '</div>';
    }
}

function tenantDisplayNameById(tenantId) {
    if (!tenantId) return '';
    const cached = getTenantInfo(tenantId);
    if (cached) {
        return cached.name || cached.display_name || cached.business_unit || tenantId;
    }
    const list = Array.isArray(window._tenants) ? window._tenants : [];
    const match = list.find(t => normalizeTenantId(t) === tenantId);
    if (match) {
        return match.name || match.display_name || tenantId;
    }
    return tenantId;
}

function formatTenantDisplay(tenantId) {
    if (!tenantId) return 'Global';
    return tenantDisplayNameById(tenantId) || tenantId;
}

function renderTenants(list) {
    const el = document.getElementById('tenants_list');
    if (!el) return;
    if (!Array.isArray(list) || list.length === 0) {
        el.innerHTML = '<div class="muted-text">No tenants yet. Click New Tenant to add one.</div>';
        return;
    }
    const rows = list.map(t => {
        const rawId = t.id || t.uuid || '';
        const idAttr = escapeHtml(rawId);
        const idDisplay = rawId ? idAttr : '<span class="muted-text">(none)</span>';
        const businessLines = [
            `<div class="table-primary">${escapeHtml(t.name || '—')}</div>`,
            t.business_unit ? `<div class="muted-text">${escapeHtml(t.business_unit)}</div>` : '',
            t.description ? `<div class="muted-text">${escapeHtml(t.description)}</div>` : ''
        ].join('');
        const contactEmail = t.contact_email ? `<a href="mailto:${encodeURIComponent(t.contact_email)}">${escapeHtml(t.contact_email)}</a>` : '';
        const contactLines = [
            t.contact_name ? `<div>${escapeHtml(t.contact_name)}</div>` : '',
            contactEmail ? `<div>${contactEmail}</div>` : '',
            t.contact_phone ? `<div class="muted-text">${escapeHtml(t.contact_phone)}</div>` : ''
        ].join('');
        const metaLines = [
            `<div>Tenant ID: ${idDisplay}</div>`,
            t.login_domain ? `<div class="muted-text">Login domain: ${escapeHtml(t.login_domain)}</div>` : '',
            t.billing_code ? `<div class="muted-text">Billing: ${escapeHtml(t.billing_code)}</div>` : '',
            t.address ? `<div class="muted-text" style="white-space:pre-line;">${escapeHtml(t.address)}</div>` : '',
            t.created_at ? `<div class="muted-text">Created ${escapeHtml(formatDateTime(t.created_at))}</div>` : ''
        ].join('');
        return `
            <tr class="tenant-row" data-tenant-id="${idAttr}">
                <td>
                    <div class="tenant-expand-cell">
                        <button class="expand-btn" data-action="toggle-sites" data-tenant="${idAttr}" title="Expand sites">
                            <svg class="expand-icon" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                                <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" fill="none"/>
                            </svg>
                        </button>
                        ${businessLines}
                    </div>
                </td>
                <td>${contactLines || '<span class="muted-text">No contact info</span>'}</td>
                <td>${metaLines}</td>
                <td class="actions-col">
                    <div class="table-actions">
                        <button data-action="create-token" data-tenant="${idAttr}">Create Token</button>
                        <button data-action="view-tokens" data-tenant="${idAttr}">Tokens</button>
                        <button data-action="tenant-settings" data-tenant="${idAttr}">Settings</button>
                        <button data-action="tenant-notifications" data-tenant="${idAttr}" data-tenant-name="${escapeHtml(t.name || '')}">Notifications</button>
                        <button data-action="edit-tenant" data-tenant="${idAttr}">Edit</button>
                        <button data-action="delete-tenant" data-tenant="${idAttr}" data-tenant-name="${escapeHtml(t.name || '')}" class="btn-danger">Delete</button>
                    </div>
                </td>
            </tr>
            <tr class="sites-expansion-row hidden" data-tenant-expansion="${idAttr}">
                <td colspan="4">
                    <div class="sites-expansion-content" data-sites-content="${idAttr}">
                        <div class="muted-text">Loading sites...</div>
                    </div>
                </td>
            </tr>
        `;
    }).join('\n');

    el.innerHTML = `
        <div class="table-wrapper">
            <table class="simple-table tenants-table">
                <thead>
                    <tr>
                        <th>Tenant</th>
                        <th>Contact</th>
                        <th>Details</th>
                        <th class="actions-col">Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${rows}
                </tbody>
            </table>
        </div>
    `;

    // Wire up expand buttons
    el.querySelectorAll('button[data-action="toggle-sites"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenantId = b.getAttribute('data-tenant');
            await toggleTenantSites(tenantId, b);
        });
    });

    el.querySelectorAll('button[data-action="create-token"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenant = b.getAttribute('data-tenant');
            await handleCreateToken(tenant);
        });
    });
    el.querySelectorAll('button[data-action="view-tokens"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenant = b.getAttribute('data-tenant');
            await showTokensList(tenant);
        });
    });
    el.querySelectorAll('button[data-action="tenant-settings"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenantId = b.getAttribute('data-tenant') || '';
            await openFleetSettingsForTenant(tenantId);
        });
    });
    el.querySelectorAll('button[data-action="tenant-notifications"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenantId = b.getAttribute('data-tenant') || '';
            const tenantName = b.getAttribute('data-tenant-name') || tenantId;
            await openTenantNotificationsModal(tenantId, tenantName);
        });
    });
    el.querySelectorAll('button[data-action="edit-tenant"]').forEach(b => {
        b.addEventListener('click', () => {
            const tenantId = b.getAttribute('data-tenant') || '';
            const tenant = (window._tenants || []).find(t => (t.id || t.uuid || '') === tenantId);
            openTenantModal(tenant || null);
        });
    });
    el.querySelectorAll('button[data-action="delete-tenant"]').forEach(b => {
        b.addEventListener('click', async () => {
            const tenantId = b.getAttribute('data-tenant') || '';
            const tenantName = b.getAttribute('data-tenant-name') || tenantId;
            await handleDeleteTenant(tenantId, tenantName);
        });
    });
}

let tenantNotificationsVM = { tenantId: '', wired: false };

async function openTenantNotificationsModal(tenantId, tenantName) {
    const modal = document.getElementById('tenant_notifications_modal');
    if (!modal || !tenantId) return;
    tenantNotificationsVM.tenantId = tenantId;
    wireTenantNotificationsModal();

    const titleEl = document.getElementById('tenant_notifications_title');
    if (titleEl) titleEl.textContent = `Notifications — ${tenantName || tenantId}`;
    setTenantNotificationsError('');

    try {
        const rec = await fetchJSON(`/api/v1/tenants/${encodeURIComponent(tenantId)}/notifications`);
        applyTenantNotificationsToForm(rec || {});
    } catch (err) {
        window.__pm_shared.error('Failed to load tenant notification settings', err);
        setTenantNotificationsError('Failed to load notification settings.');
        applyTenantNotificationsToForm({});
    }
    modal.style.display = 'flex';
}

function wireTenantNotificationsModal() {
    if (tenantNotificationsVM.wired) return;
    tenantNotificationsVM.wired = true;
    const close = () => {
        const modal = document.getElementById('tenant_notifications_modal');
        if (modal) modal.style.display = 'none';
    };
    document.getElementById('tenant_notifications_close_x')?.addEventListener('click', close);
    document.getElementById('tenant_notifications_cancel')?.addEventListener('click', close);
    document.getElementById('tenant_notifications_save')?.addEventListener('click', async () => {
        if (await saveTenantNotifications()) close();
    });
    document.getElementById('tenant_notifications_test')?.addEventListener('click', sendTenantNotificationTest);
}

function applyTenantNotificationsToForm(rec) {
    const set = (id, value) => { const el = document.getElementById(id); if (el) el.value = value; };
    const check = (id, value) => { const el = document.getElementById(id); if (el) el.checked = Boolean(value); };
    check('tenant_notify_enabled', rec.enabled);
    set('tenant_notify_recipients', Array.isArray(rec.recipients) ? rec.recipients.join(', ') : '');
    check('tenant_notify_critical', rec.notify_on_critical !== false);
    check('tenant_notify_warning', rec.notify_on_warning);
    check('tenant_notify_summary', rec.daily_summary_enabled);
    set('tenant_notify_summary_time', rec.daily_summary_time || '08:00');
    set('tenant_notify_summary_tz', rec.daily_summary_timezone || 'Local');
}

function readTenantNotificationsForm() {
    const val = (id) => (document.getElementById(id)?.value || '').trim();
    const checked = (id) => Boolean(document.getElementById(id)?.checked);
    const recipients = val('tenant_notify_recipients')
        .split(/[,;\n]/)
        .map(s => s.trim())
        .filter(Boolean);
    return {
        enabled: checked('tenant_notify_enabled'),
        recipients,
        notify_on_critical: checked('tenant_notify_critical'),
        notify_on_warning: checked('tenant_notify_warning'),
        daily_summary_enabled: checked('tenant_notify_summary'),
        daily_summary_time: val('tenant_notify_summary_time') || '08:00',
        daily_summary_timezone: val('tenant_notify_summary_tz') || 'Local',
    };
}

function setTenantNotificationsError(message) {
    const el = document.getElementById('tenant_notifications_error');
    if (el) el.textContent = message || '';
}

async function saveTenantNotifications() {
    const tenantId = tenantNotificationsVM.tenantId;
    if (!tenantId) return false;
    setTenantNotificationsError('');
    try {
        await fetchJSON(`/api/v1/tenants/${encodeURIComponent(tenantId)}/notifications`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(readTenantNotificationsForm()),
        });
        window.__pm_shared.showToast('Tenant notification settings saved.', 'success');
        return true;
    } catch (err) {
        window.__pm_shared.error('Failed to save tenant notification settings', err);
        setTenantNotificationsError((err && err.message) || 'Failed to save notification settings.');
        return false;
    }
}

async function sendTenantNotificationTest() {
    const tenantId = tenantNotificationsVM.tenantId;
    if (!tenantId) return;
    setTenantNotificationsError('');
    try {
        const resp = await fetchJSON(`/api/v1/tenants/${encodeURIComponent(tenantId)}/notifications/test`, { method: 'POST' });
        window.__pm_shared.showToast(`Test notification sent to ${(resp && resp.sent) || 0} recipient(s).`, 'success');
    } catch (err) {
        setTenantNotificationsError((err && err.message) || 'Failed to send test notification. Save your changes first.');
    }
}

function renderTenantsFiltered() {
    const list = tenantsVM.filtered;
    const el = document.getElementById('tenants_list');
    if (!el) return;

    if (!Array.isArray(list) || list.length === 0) {
        if (tenantsVM.stats.total === 0) {
            el.innerHTML = '<div class="muted-text">No tenants yet. Click + New Tenant to add one.</div>';
        } else {
            el.innerHTML = '<div class="muted-text">No tenants match your filters.</div>';
        }
        return;
    }

    // Use the existing renderTenants for the actual rendering
    renderTenants(list);
}

async function createTenant(body) {
    const r = await fetch('/api/v1/tenants', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
}

async function updateTenant(id, body) {
    const r = await fetch('/api/v1/tenants/' + encodeURIComponent(id), { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
}

async function deleteTenant(id, force = false) {
    const url = '/api/v1/tenants/' + encodeURIComponent(id) + (force ? '?force=true' : '');
    const r = await fetch(url, { method: 'DELETE' });
    if (r.status === 204) return { success: true };
    if (r.status === 409) {
        // Conflict - has agents
        const data = await r.json();
        return { success: false, conflict: true, ...data };
    }
    if (!r.ok) throw new Error(await r.text());
    return { success: true };
}

async function handleDeleteTenant(tenantId, tenantName) {
    // Initial confirmation
    const confirmed = await window.__pm_shared.showConfirm(
        `Are you sure you want to delete the tenant "${tenantName}"?\n\nThis will permanently remove all sites, join tokens, and tenant settings.\n\nThis action cannot be undone.`,
        'Delete Tenant'
    );
    if (!confirmed) return;

    try {
        const result = await deleteTenant(tenantId, false);
        if (result.success) {
            window.__pm_shared.showToast('Tenant deleted successfully', 'success');
            await loadTenants();
            return;
        }
        if (result.conflict) {
            // Has agents - ask if they want to force
            const forceConfirmed = await window.__pm_shared.showConfirm(
                `This tenant has ${result.agent_count} agent(s) assigned.\n\nIf you proceed, these agents will be orphaned (their tenant assignment will be cleared, but they will not be deleted).\n\nAre you sure you want to continue?`,
                'Delete Anyway'
            );
            if (!forceConfirmed) return;

            const forceResult = await deleteTenant(tenantId, true);
            if (forceResult.success) {
                window.__pm_shared.showToast(`Tenant deleted. ${result.agent_count} agent(s) orphaned.`, 'warning');
                await loadTenants();
                return;
            }
        }
    } catch (e) {
        console.error('Failed to delete tenant:', e);
        window.__pm_shared.showToast('Failed to delete tenant: ' + e.message, 'error');
    }
}

// ====== Tenant Sites Expandable Rows ======
async function toggleTenantSites(tenantId, btn) {
    const row = btn.closest('tr');
    const sitesRow = row.nextElementSibling;
    if (!sitesRow || !sitesRow.classList.contains('sites-expansion-row')) return;

    const container = sitesRow.querySelector('.sites-expansion-content');
    const isExpanded = !sitesRow.classList.contains('hidden');

    if (isExpanded) {
        // Collapse
        sitesRow.classList.add('hidden');
        btn.classList.remove('expanded');
    } else {
        // Expand - load sites if not loaded
        sitesRow.classList.remove('hidden');
        btn.classList.add('expanded');

        if (!container.hasAttribute('data-loaded')) {
            container.innerHTML = '<div class="loading-text">Loading sites...</div>';
            try {
                const sites = await fetchSitesForTenant(tenantId);
                const agents = await fetchAgentsForTenant(tenantId);
                container.innerHTML = renderSitesTree(tenantId, sites, agents);
                container.setAttribute('data-loaded', 'true');
                wireSitesTreeEvents(container, tenantId);
            } catch (e) {
                container.innerHTML = `<div class="error-text">Failed to load: ${e.message}</div>`;
            }
        }
    }
}

async function fetchSitesForTenant(tenantId) {
    const r = await fetch(`/api/v1/tenants/${encodeURIComponent(tenantId)}/sites`);
    if (!r.ok) throw new Error(await r.text());
    const data = await r.json();
    return data || [];
}

async function fetchAgentsForTenant(tenantId) {
    // Fetch agents assigned to this tenant
    const r = await fetch('/api/v1/agents/list');
    if (!r.ok) throw new Error(await r.text());
    const data = await r.json();
    // Filter agents by tenant - API returns array directly
    return (data || []).filter(a => a.tenant_id === tenantId);
}

function renderSitesTree(tenantId, sites, agents) {
    // Escape a value for safe interpolation inside a single-quoted attribute
    // JS string literal (e.g. onclick="fn('${...}')"). Escape backslashes
    // before quotes, otherwise the backslash inserted to escape a quote would
    // itself be re-escaped.
    const escapeAttrJsString = (s) => String(s == null ? '' : s).replace(/\\/g, '\\\\').replace(/'/g, "\\'");

    if (sites.length === 0 && agents.length === 0) {
        return `
            <div class="sites-tree-empty">
                <span>No sites configured.</span>
                <button class="btn btn-xs btn-primary" onclick="openSiteModal('${escapeAttrJsString(tenantId)}', null)">+ Add Site</button>
            </div>
        `;
    }

    // Build a map of site -> agents
    const siteAgents = {};
    const unassignedAgents = [];
    agents.forEach(a => {
        const siteIds = a.site_ids || [];
        if (siteIds.length === 0) {
            unassignedAgents.push(a);
        } else {
            siteIds.forEach(sid => {
                if (!siteAgents[sid]) siteAgents[sid] = [];
                siteAgents[sid].push(a);
            });
        }
    });

    let html = '<div class="sites-tree">';

    const escapedTenantId = escapeAttrJsString(tenantId);

    // Toolbar
    html += `<div class="sites-tree-toolbar">
        <button class="btn btn-xs btn-primary" onclick="openSiteModal('${escapedTenantId}', null)">+ Add Site</button>
    </div>`;

    // Sites with their agents
    sites.forEach(site => {
        const siteAgentList = siteAgents[site.id] || [];
        // Escape backslashes first, then single quotes to prevent injection via \'
        const escapedSiteName = escapeHtml(site.name).replace(/\\/g, '\\\\').replace(/'/g, "\\'");
        const escapedSiteId = escapeAttrJsString(site.id);
        html += `
            <div class="site-node" data-site-id="${site.id}">
                <div class="site-header">
                    <span class="site-icon">📍</span>
                    <span class="site-name">${escapeHtml(site.name)}</span>
                    <span class="site-meta">${siteAgentList.length} agents, ${site.device_count || 0} devices</span>
                    <div class="site-actions">
                        <button class="btn btn-xs" onclick="openSiteModal('${escapedTenantId}', '${escapedSiteId}')">Edit</button>
                        <button class="btn btn-xs btn-danger" onclick="deleteSiteInline('${escapedTenantId}', '${escapedSiteId}', '${escapedSiteName}')">×</button>
                    </div>
                </div>
                <div class="site-agents">
                    ${siteAgentList.map(a => `
                        <div class="agent-leaf">
                            <span class="agent-icon">🖥️</span>
                            <span class="agent-name">${escapeHtml(a.name || a.hostname || a.agent_id || 'Agent ' + a.id)}</span>
                            <span class="agent-status ${a.status || 'unknown'}">${a.status || 'unknown'}</span>
                        </div>
                    `).join('')}
                    ${siteAgentList.length === 0 ? '<div class="no-agents-text">No agents assigned</div>' : ''}
                </div>
            </div>
        `;
    });

    // Unassigned agents
    if (unassignedAgents.length > 0) {
        html += `
            <div class="site-node unassigned-node">
                <div class="site-header">
                    <span class="site-icon">📦</span>
                    <span class="site-name">Unassigned Agents</span>
                    <span class="site-meta">${unassignedAgents.length} agents</span>
                </div>
                <div class="site-agents">
                    ${unassignedAgents.map(a => `
                        <div class="agent-leaf">
                            <span class="agent-icon">🖥️</span>
                            <span class="agent-name">${escapeHtml(a.name || a.hostname || a.agent_id || 'Agent ' + a.id)}</span>
                            <span class="agent-status ${a.status || 'unknown'}">${a.status || 'unknown'}</span>
                        </div>
                    `).join('')}
                </div>
            </div>
        `;
    }

    html += '</div>';
    return html;
}

function wireSitesTreeEvents(container, tenantId) {
    // Events are wired via onclick attributes for simplicity
}

async function deleteSiteInline(tenantId, siteId, siteName) {
    const displayName = siteName || siteId;
    const confirmed = await window.__pm_shared.showConfirm(
        `Delete site "${displayName}"? This will remove all agent assignments.`,
        'Delete Site'
    );
    if (!confirmed) return;
    try {
        const r = await fetch(`/api/v1/tenants/${encodeURIComponent(tenantId)}/sites/${encodeURIComponent(siteId)}`, { method: 'DELETE' });
        if (!r.ok) throw new Error(await r.text());
        window.__pm_shared.showToast('Site deleted', 'success');
        // Refresh the tree
        await refreshTenantSitesTree(tenantId);
    } catch (e) {
        window.__pm_shared.showToast('Failed to delete site: ' + e.message, 'error');
    }
}

async function refreshTenantSitesTree(tenantId) {
    // Find the tenant row and reload sites
    const rows = document.querySelectorAll('#tenants_content tr[data-tenant-id]');
    for (const row of rows) {
        if (row.getAttribute('data-tenant-id') === tenantId) {
            const sitesRow = row.nextElementSibling;
            if (sitesRow && sitesRow.classList.contains('sites-expansion-row')) {
                const container = sitesRow.querySelector('.sites-expansion-content');
                if (container) {
                    container.removeAttribute('data-loaded');
                    // Refresh if expanded
                    if (!sitesRow.classList.contains('hidden')) {
                        container.innerHTML = '<div class="loading-text">Loading sites...</div>';
                        const sites = await fetchSitesForTenant(tenantId);
                        const agents = await fetchAgentsForTenant(tenantId);
                        container.innerHTML = renderSitesTree(tenantId, sites, agents);
                        container.setAttribute('data-loaded', 'true');
                    }
                }
            }
            break;
        }
    }
}

// Global function called from onclick handlers in tree
window.openSiteModal = async function (tenantId, siteId) {
    currentSitesTenantId = tenantId;
    await openSiteEditModal(siteId);
};

// Global function for inline delete
window.deleteSiteInline = deleteSiteInline;

// ====== Sites Management ======
let currentSitesTenantId = null;
let currentSiteEditId = null;
let currentSiteFilterRules = [];

async function openSitesListModal(tenantId) {
    currentSitesTenantId = tenantId;
    const modal = document.getElementById('sites_list_modal');
    if (!modal) return;

    const tenant = (window._tenants || []).find(t => (t.id || t.uuid || '') === tenantId);
    const tenantName = tenant ? tenant.name : tenantId;

    document.getElementById('sites_list_modal_title').textContent = `Sites - ${tenantName}`;
    document.getElementById('sites_list_subtitle').textContent = `Manage sites for ${tenantName}`;
    document.getElementById('sites_list_content').innerHTML = '<div class="muted-text">Loading sites...</div>';

    modal.style.display = 'flex';

    await loadSitesList(tenantId);
}

async function loadSitesList(tenantId) {
    const container = document.getElementById('sites_list_content');
    try {
        const r = await fetch(`/api/v1/tenants/${encodeURIComponent(tenantId)}/sites`);
        if (!r.ok) throw new Error(await r.text());
        const sites = await r.json();
        renderSitesList(sites || []);
    } catch (err) {
        container.innerHTML = `<div style="color:var(--danger);">Failed to load sites: ${escapeHtml(err.message || err)}</div>`;
    }
}

function renderSitesList(sites) {
    const container = document.getElementById('sites_list_content');
    if (!sites || sites.length === 0) {
        container.innerHTML = '<div class="muted-text">No sites defined yet. Click "Add Site" to create one.</div>';
        return;
    }

    const rows = sites.map(site => {
        const agentBadge = `<span class="site-agents-badge">${site.agent_count || 0} agent${site.agent_count !== 1 ? 's' : ''}</span>`;
        const rulesBadge = site.filter_rules && site.filter_rules.length > 0
            ? `<span class="site-rules-badge">${site.filter_rules.length} rule${site.filter_rules.length !== 1 ? 's' : ''}</span>`
            : '';
        return `
            <tr>
                <td class="site-name-cell">${escapeHtml(site.name)}</td>
                <td>${escapeHtml(site.address || '-')}</td>
                <td>${agentBadge} ${rulesBadge}</td>
                <td class="actions-col">
                    <div class="table-actions">
                        <button data-action="edit-site" data-site-id="${escapeHtml(site.id)}">Edit</button>
                        <button data-action="delete-site" data-site-id="${escapeHtml(site.id)}" data-site-name="${escapeHtml(site.name)}">Delete</button>
                    </div>
                </td>
            </tr>
        `;
    }).join('');

    container.innerHTML = `
        <table class="sites-table">
            <thead>
                <tr>
                    <th>Site Name</th>
                    <th>Address</th>
                    <th>Agents / Rules</th>
                    <th class="actions-col">Actions</th>
                </tr>
            </thead>
            <tbody>${rows}</tbody>
        </table>
    `;

    container.querySelectorAll('button[data-action="edit-site"]').forEach(b => {
        b.addEventListener('click', async () => {
            const siteId = b.getAttribute('data-site-id');
            await openSiteEditModal(siteId);
        });
    });

    container.querySelectorAll('button[data-action="delete-site"]').forEach(b => {
        b.addEventListener('click', async () => {
            const siteId = b.getAttribute('data-site-id');
            const siteName = b.getAttribute('data-site-name');
            const confirmed = await window.__pm_shared.showConfirm(`Delete site "${siteName}"? This will remove all agent assignments.`, 'Delete Site');
            if (confirmed) {
                await deleteSite(siteId);
            }
        });
    });
}

function closeSitesListModal() {
    const modal = document.getElementById('sites_list_modal');
    if (modal) modal.style.display = 'none';
    currentSitesTenantId = null;
}

async function openSiteEditModal(siteId) {
    currentSiteEditId = siteId || null;
    currentSiteFilterRules = [];

    const modal = document.getElementById('site_modal');
    if (!modal) return;

    // Set title and button
    if (siteId) {
        document.getElementById('site_modal_title').textContent = 'Edit Site';
        document.getElementById('site_save').textContent = 'Save Changes';
    } else {
        document.getElementById('site_modal_title').textContent = 'New Site';
        document.getElementById('site_save').textContent = 'Create Site';
    }

    // Reset form
    document.getElementById('site_name').value = '';
    document.getElementById('site_address').value = '';
    document.getElementById('site_description').value = '';
    document.getElementById('site_error').textContent = '';
    document.getElementById('site_filter_rules').innerHTML = '';

    // Load agents for this tenant
    await loadSiteAgentsList([]);

    if (siteId) {
        try {
            const r = await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites/${encodeURIComponent(siteId)}`);
            if (!r.ok) throw new Error(await r.text());
            const site = await r.json();

            document.getElementById('site_name').value = site.name || '';
            document.getElementById('site_address').value = site.address || '';
            document.getElementById('site_description').value = site.description || '';

            currentSiteFilterRules = site.filter_rules || [];
            renderSiteFilterRules();

            // Load assigned agents
            const agentsR = await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites/${encodeURIComponent(siteId)}/agents`);
            if (agentsR.ok) {
                const agentsData = await agentsR.json();
                await loadSiteAgentsList(agentsData.agent_ids || []);
            }
        } catch (err) {
            document.getElementById('site_error').textContent = 'Failed to load site: ' + (err.message || err);
        }
    }

    modal.style.display = 'flex';
}

async function loadSiteAgentsList(selectedAgentIds) {
    const container = document.getElementById('site_agents_list');
    container.innerHTML = '<div class="muted-text">Loading agents...</div>';

    try {
        const r = await fetch('/api/v1/agents/list');
        if (!r.ok) throw new Error(await r.text());
        const agents = await r.json() || [];

        // Filter to agents belonging to this tenant (or no tenant)
        const tenantAgents = agents.filter(a =>
            !a.tenant_id || a.tenant_id === currentSitesTenantId
        );

        if (tenantAgents.length === 0) {
            container.innerHTML = '<div class="muted-text">No agents available for this tenant.</div>';
            return;
        }

        const selectedSet = new Set(selectedAgentIds);
        const items = tenantAgents.map(agent => {
            const checked = selectedSet.has(agent.agent_id) ? 'checked' : '';
            const status = agent.status || 'unknown';
            return `
                <label class="site-agent-item">
                    <input type="checkbox" value="${escapeHtml(agent.agent_id)}" ${checked} />
                    <span class="agent-name">${escapeHtml(getAgentDisplayName(agent))}</span>
                    <span class="agent-meta">${escapeHtml(status)}</span>
                </label>
            `;
        }).join('');

        container.innerHTML = items;
    } catch (err) {
        container.innerHTML = `<div style="color:var(--danger);">Failed to load agents: ${escapeHtml(err.message || err)}</div>`;
    }
}

function renderSiteFilterRules() {
    const container = document.getElementById('site_filter_rules');
    if (!currentSiteFilterRules || currentSiteFilterRules.length === 0) {
        container.innerHTML = '';
        return;
    }

    const ruleTypes = [
        { value: 'ip_range', label: 'IP Range (CIDR)', placeholder: '192.168.1.0/24' },
        { value: 'ip_prefix', label: 'IP Prefix', placeholder: '192.168.1.' },
        { value: 'hostname_pattern', label: 'Hostname Pattern', placeholder: 'printer-*' },
        { value: 'serial_pattern', label: 'Serial Pattern', placeholder: 'HP*' }
    ];

    const html = currentSiteFilterRules.map((rule, index) => {
        const typeOptions = ruleTypes.map(t =>
            `<option value="${t.value}" ${rule.type === t.value ? 'selected' : ''}>${t.label}</option>`
        ).join('');
        const placeholder = ruleTypes.find(t => t.value === rule.type)?.placeholder || '';

        return `
            <div class="site-filter-rule" data-index="${index}">
                <select class="rule-type">${typeOptions}</select>
                <input type="text" class="rule-pattern" value="${escapeHtml(rule.pattern)}" placeholder="${placeholder}" autocomplete="off" data-1p-ignore data-lpignore="true" />
                <button type="button" class="remove-rule-btn" title="Remove rule">&times;</button>
            </div>
        `;
    }).join('');

    container.innerHTML = html;

    // Wire up change/remove handlers
    container.querySelectorAll('.site-filter-rule').forEach(el => {
        const index = parseInt(el.getAttribute('data-index'), 10);
        el.querySelector('.rule-type').addEventListener('change', e => {
            currentSiteFilterRules[index].type = e.target.value;
        });
        el.querySelector('.rule-pattern').addEventListener('input', e => {
            currentSiteFilterRules[index].pattern = e.target.value;
        });
        el.querySelector('.remove-rule-btn').addEventListener('click', () => {
            currentSiteFilterRules.splice(index, 1);
            renderSiteFilterRules();
        });
    });
}

function addSiteFilterRule() {
    currentSiteFilterRules.push({ type: 'ip_prefix', pattern: '' });
    renderSiteFilterRules();
}

function closeSiteModal() {
    const modal = document.getElementById('site_modal');
    if (modal) modal.style.display = 'none';
    currentSiteEditId = null;
    currentSiteFilterRules = [];
}

async function saveSite() {
    const name = document.getElementById('site_name').value.trim();
    const address = document.getElementById('site_address').value.trim();
    const description = document.getElementById('site_description').value.trim();
    const errEl = document.getElementById('site_error');

    errEl.textContent = '';

    if (!name) {
        errEl.textContent = 'Site name is required';
        return;
    }

    // Collect selected agents
    const agentCheckboxes = document.querySelectorAll('#site_agents_list input[type="checkbox"]:checked');
    const selectedAgentIds = Array.from(agentCheckboxes).map(cb => cb.value);

    // Filter out empty pattern rules
    const filterRules = currentSiteFilterRules.filter(r => r.pattern && r.pattern.trim());

    const payload = {
        name,
        address,
        description,
        filter_rules: filterRules
    };

    try {
        let siteId = currentSiteEditId;

        if (currentSiteEditId) {
            // Update existing site
            const r = await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites/${encodeURIComponent(currentSiteEditId)}`, {
                method: 'PUT',
                headers: { 'content-type': 'application/json' },
                body: JSON.stringify(payload)
            });
            if (!r.ok) throw new Error(await r.text());
        } else {
            // Create new site
            const r = await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites`, {
                method: 'POST',
                headers: { 'content-type': 'application/json' },
                body: JSON.stringify(payload)
            });
            if (!r.ok) throw new Error(await r.text());
            const newSite = await r.json();
            siteId = newSite.id;
        }

        // Update agent assignments
        await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites/${encodeURIComponent(siteId)}/agents`, {
            method: 'PUT',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ agent_ids: selectedAgentIds })
        });

        closeSiteModal();
        window.__pm_shared.showToast(currentSiteEditId ? 'Site updated' : 'Site created', 'success');
        // Refresh both the list modal (if open) and the tree view
        await loadSitesList(currentSitesTenantId);
        await refreshTenantSitesTree(currentSitesTenantId);
    } catch (err) {
        errEl.textContent = err.message || 'Failed to save site';
    }
}

async function deleteSite(siteId) {
    try {
        const r = await fetch(`/api/v1/tenants/${encodeURIComponent(currentSitesTenantId)}/sites/${encodeURIComponent(siteId)}`, {
            method: 'DELETE'
        });
        if (!r.ok) throw new Error(await r.text());
        window.__pm_shared.showToast('Site deleted', 'success');
        await loadSitesList(currentSitesTenantId);
        await refreshTenantSitesTree(currentSitesTenantId);
    } catch (err) {
        window.__pm_shared.showAlert('Failed to delete site: ' + (err.message || err), 'Error', true, false);
    }
}

// Wire up sites modal event listeners
function initSitesUI() {
    // Sites list modal
    const sitesListModal = document.getElementById('sites_list_modal');
    if (sitesListModal) {
        document.getElementById('sites_list_modal_close_x').addEventListener('click', closeSitesListModal);
        document.getElementById('sites_list_add_btn').addEventListener('click', () => openSiteEditModal(null));
    }

    // Site edit modal
    const siteModal = document.getElementById('site_modal');
    if (siteModal) {
        document.getElementById('site_modal_close_x').addEventListener('click', closeSiteModal);
        document.getElementById('site_cancel').addEventListener('click', closeSiteModal);
        document.getElementById('site_save').addEventListener('click', saveSite);
        document.getElementById('site_add_rule_btn').addEventListener('click', addSiteFilterRule);
    }
}

async function handleCreateToken(tenantID) {
    // Open the unified Add Agent modal and preselect the tenant
    try {
        openAddAgentModal({ tenantID });
    } catch (err) {
        window.__pm_shared.showAlert('Failed to open Add Agent modal: ' + (err && err.message ? err.message : err), 'Error', true, false);
    }
}

async function showTokensList(tenantID) {
    try {
        const r = await fetch('/api/v1/join-tokens?tenant_id=' + encodeURIComponent(tenantID));
        if (!r.ok) throw new Error(await r.text());
        const tokens = await r.json();
        renderTokenModal(tenantID, tokens);
    } catch (err) {
        window.__pm_shared.showAlert('Failed to load tokens: ' + (err.message || err), 'Error', true, false);
    }
}

function renderTokenModal(tenantID, tokens) {
    if (!Array.isArray(tokens) || tokens.length === 0) {
        window.__pm_shared.showAlert('No tokens for tenant: ' + escapeHtml(tenantID), 'Tokens', false, false);
        return;
    }

    let html = '<div style="max-height: 400px; overflow-y: auto;">';
    html += '<table style="width: 100%; border-collapse: collapse; font-size: 13px;">';
    html += '<thead><tr style="background: var(--bg-secondary); text-align: left;">';
    html += '<th style="padding: 8px; border-bottom: 1px solid var(--border);">ID</th>';
    html += '<th style="padding: 8px; border-bottom: 1px solid var(--border);">Type</th>';
    html += '<th style="padding: 8px; border-bottom: 1px solid var(--border);">Status</th>';
    html += '<th style="padding: 8px; border-bottom: 1px solid var(--border);">Used At</th>';
    html += '<th style="padding: 8px; border-bottom: 1px solid var(--border);">Expires</th>';
    html += '</tr></thead><tbody>';

    tokens.forEach(t => {
        const isRevoked = t.revoked;
        const isUsed = !!t.used_at;
        const isExpired = t.expires_at && new Date(t.expires_at) < new Date();

        let status = '<span style="color: var(--success);">Active</span>';
        if (isRevoked) status = '<span style="color: var(--danger);">Revoked</span>';
        else if (isUsed && t.one_time) status = '<span style="color: var(--muted);">Used</span>';
        else if (isExpired) status = '<span style="color: var(--warning);">Expired</span>';

        html += `<tr style="border-bottom: 1px solid var(--border);">`;
        html += `<td style="padding: 8px; font-family: monospace;">${escapeHtml(t.id)}</td>`;
        html += `<td style="padding: 8px;">${t.one_time ? 'One-time' : 'Reusable'}</td>`;
        html += `<td style="padding: 8px;">${status}</td>`;
        html += `<td style="padding: 8px;">${t.used_at ? window.__pm_shared.formatDateTime(t.used_at) : '-'}</td>`;
        html += `<td style="padding: 8px;">${t.expires_at ? window.__pm_shared.formatDateTime(t.expires_at) : 'Never'}</td>`;
        html += `</tr>`;
    });
    html += '</tbody></table></div>';

    // Ask user if they want to revoke a token via input modal
    window.__pm_shared.showAlert(html, 'Tokens for tenant: ' + escapeHtml(tenantID), false, false, true);
    showInputModal('Revoke token', 'Enter the token ID to revoke (leave empty to cancel)', '').then(id => {
        if (!id) return;
        revokeToken(id.trim()).then(() => {
            window.__pm_shared.showToast('Revoked ' + id.trim(), 'success');
            showTokensList(tenantID);
        }).catch(err => {
            window.__pm_shared.showAlert('Failed to revoke: ' + (err.message || err), 'Error', true, false);
        });
    }).catch(() => { });
}

async function revokeToken(id) {
    const r = await fetch('/api/v1/join-token/revoke', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id }) });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
}

// escapeHtml is now in utils/formatters.js

// compareVersions, formatBytes, formatDateTime, formatRelativeTime, formatNumber
// are now in utils/formatters.js

function initAgentsUI() {
    if (agentsVM.uiInitialized) {
        return;
    }
    agentsVM.uiInitialized = true;

    initFilterDrawer('agents_sidebar', 'agents_filters_open', 'agents_sidebar_toggle');

    const searchInput = document.getElementById('agents_search');
    if (searchInput) {
        searchInput.value = agentsVM.filters.query;
        searchInput.addEventListener('input', debounce((event) => {
            agentsVM.filters.query = (event.target.value || '').trim();
            applyAgentFilters();
        }, 200));
    }

    const versionSelect = document.getElementById('agents_version_filter');
    if (versionSelect) {
        versionSelect.addEventListener('change', (event) => {
            agentsVM.filters.version = event.target.value || '';
            applyAgentFilters();
        });
    }

    const platformSelect = document.getElementById('agents_platform_filter');
    if (platformSelect) {
        platformSelect.addEventListener('change', (event) => {
            agentsVM.filters.platform = event.target.value || '';
            applyAgentFilters();
        });
    }

    const tenantSelect = document.getElementById('agents_tenant_filter');
    if (tenantSelect) {
        tenantSelect.value = agentsVM.filters.tenantId;
        tenantSelect.addEventListener('change', (event) => {
            agentsVM.filters.tenantId = event.target.value || '';
            applyAgentFilters();
        });
    }

    const sortSelect = document.getElementById('agents_sort_select');
    if (sortSelect) {
        sortSelect.value = agentsVM.filters.sortKey;
        sortSelect.addEventListener('change', (event) => {
            setAgentSort(event.target.value, agentsVM.filters.sortDir);
        });
    }

    const sortDirBtn = document.getElementById('agents_sort_dir_btn');
    if (sortDirBtn) {
        sortDirBtn.addEventListener('click', () => {
            const nextDir = agentsVM.filters.sortDir === 'asc' ? 'desc' : 'asc';
            setAgentSort(agentsVM.filters.sortKey, nextDir);
        });
    }

    const viewToggle = document.getElementById('agents_view_toggle');
    if (viewToggle) {
        viewToggle.addEventListener('click', (event) => {
            const btn = event.target.closest('[data-view]');
            if (!btn) return;
            setAgentsView(btn.getAttribute('data-view'));
        });
    }

    const statusFilter = document.getElementById('agents_status_filter');
    if (statusFilter) {
        statusFilter.addEventListener('click', (event) => {
            const btn = event.target.closest('[data-status]');
            if (!btn) return;
            toggleAgentStatusFilter(btn.getAttribute('data-status'));
        });
    }

    const resetBtn = document.getElementById('agents_reset_filters');
    if (resetBtn) {
        resetBtn.addEventListener('click', resetAgentFilters);
    }

    // Check for updates toggle
    const checkUpdatesToggle = document.getElementById('agents_check_updates_toggle');
    if (checkUpdatesToggle) {
        // Load saved preference from localStorage
        const savedPref = localStorage.getItem('agents_check_updates_on_load');
        if (savedPref !== null) {
            agentsVM.checkUpdatesOnLoad = savedPref === 'true';
        }
        checkUpdatesToggle.checked = agentsVM.checkUpdatesOnLoad;
        checkUpdatesToggle.addEventListener('change', (event) => {
            agentsVM.checkUpdatesOnLoad = event.target.checked;
            localStorage.setItem('agents_check_updates_on_load', event.target.checked ? 'true' : 'false');
        });
    }

    // Check all for updates button
    const checkUpdatesBtn = document.getElementById('agents_check_updates_btn');
    if (checkUpdatesBtn) {
        checkUpdatesBtn.addEventListener('click', () => {
            checkAgentsForUpdates();
        });
    }

    const chips = document.getElementById('agents_active_filters');
    if (chips && !chips.dataset.bound) {
        chips.dataset.bound = 'true';
        chips.addEventListener('click', (event) => {
            const btn = event.target.closest('button[data-filter]');
            if (!btn) return;
            handleAgentFilterChipRemove(btn.getAttribute('data-filter'));
        });
    }

    const table = document.getElementById('agents_table');
    if (table) {
        const head = table.querySelector('thead');
        if (head && !head.dataset.bound) {
            head.dataset.bound = 'true';
            head.addEventListener('click', handleAgentTableSortClick);
        }
        // Add click handler for clickable rows (file-explorer style selection)
        const tbody = table.querySelector('tbody');
        if (tbody && !tbody.dataset.rowClickBound) {
            tbody.dataset.rowClickBound = 'true';
            tbody.addEventListener('click', (event) => {
                // Don't trigger row click if clicking on a button or actions column
                if (event.target.closest('button') || event.target.closest('.table-actions') || event.target.closest('.actions-col')) {
                    return;
                }
                const row = event.target.closest('tr.agent-row-clickable');
                if (!row) return;
                const agentId = row.getAttribute('data-agent-id');
                if (agentId) {
                    // File-explorer style: click selects, double-click opens details
                    handleAgentSelection(agentId, event);
                }
            });
            // Double-click opens agent details
            tbody.addEventListener('dblclick', (event) => {
                if (event.target.closest('button') || event.target.closest('.table-actions') || event.target.closest('.actions-col')) {
                    return;
                }
                const row = event.target.closest('tr.agent-row-clickable');
                if (!row) return;
                const agentId = row.getAttribute('data-agent-id');
                if (agentId) {
                    viewAgentDetails(agentId);
                }
            });
        }
    }

    // Add click handler for clickable agent cards (file-explorer style selection)
    const cardsContainer = document.getElementById('agents_cards');
    if (cardsContainer && !cardsContainer.dataset.cardClickBound) {
        cardsContainer.dataset.cardClickBound = 'true';
        cardsContainer.addEventListener('click', (event) => {
            // Don't trigger card click if clicking on a button or actions area
            if (event.target.closest('button') || event.target.closest('.device-card-actions')) {
                return;
            }
            const card = event.target.closest('.agent-card-clickable');
            if (!card) return;
            const agentId = card.getAttribute('data-agent-id');
            if (agentId) {
                // File-explorer style: click selects, double-click opens details
                handleAgentSelection(agentId, event);
            }
        });
        // Double-click opens agent details
        cardsContainer.addEventListener('dblclick', (event) => {
            if (event.target.closest('button') || event.target.closest('.device-card-actions')) {
                return;
            }
            const card = event.target.closest('.agent-card-clickable');
            if (!card) return;
            const agentId = card.getAttribute('data-agent-id');
            if (agentId) {
                viewAgentDetails(agentId);
            }
        });
    }

    syncAgentsViewToggle();
    syncAgentSortControls();
    syncAgentQuickFilters();
    syncTenantFilterOptions('agents');
    initAgentsTableCustomizer();

    // Initialize context menu for agents table and cards
    if (window.PMContextMenu) {
        const agentsTable = document.getElementById('agents_table');
        if (agentsTable) {
            window.PMContextMenu.initAgentContextMenu(agentsTable);
        }
        const agentsCards = document.getElementById('agents_cards');
        if (agentsCards) {
            window.PMContextMenu.initAgentContextMenu(agentsCards);
        }
    }
}

function initAgentsTableCustomizer() {
    if (agentsVM.tableCustomizer) return;

    // Only initialize if TableCustomizer is available
    if (typeof window.TableCustomizer === 'undefined') {
        console.warn('TableCustomizer not available');
        return;
    }

    // Create customizer instance
    agentsVM.tableCustomizer = new window.TableCustomizer('agents', {
        columnDefs: window.AGENTS_COLUMN_DEFINITIONS || [],
        persistConfig: true,
        enableResize: true,
        enableReorder: true,
        enableColumnMenu: true,
        enableExport: true,
        onSort: (sortState) => {
            // Sync with agentsVM filters
            if (sortState.key) {
                agentsVM.filters.sortKey = sortState.key;
                agentsVM.filters.sortDir = sortState.dir;
                syncAgentSortControls();
                applyAgentFilters();
            }
        },
        onColumnChange: () => {
            // Re-render table when columns change
            renderAgentsTableHeader();
            if (agentsVM.view === 'table') {
                renderAgentTable(agentsVM.filtered);
            }
        },
        onExport: () => {
            // Export current filtered data
            if (agentsVM.tableCustomizer) {
                const timestamp = new Date().toISOString().split('T')[0];
                agentsVM.tableCustomizer.exportToCSV(agentsVM.filtered, `printmaster-agents-${timestamp}.csv`);
                window.__pm_shared?.showToast?.('Agents exported to CSV', 'success');
            }
        }
    });

    // Render toolbar
    const toolbarContainer = document.getElementById('agents_table_customizer_toolbar');
    if (toolbarContainer) {
        toolbarContainer.innerHTML = agentsVM.tableCustomizer.renderToolbar();
        agentsVM.tableCustomizer.bindToolbarEvents(toolbarContainer);
    }

    // Render initial header
    renderAgentsTableHeader();

    // Expose helper functions on window for use by column renderers
    window.renderAgentStatusBadge = renderAgentStatusBadge;
    window.renderAgentVersionCell = renderAgentVersionCell;
    window.getAgentDisplayName = getAgentDisplayName;
}

function renderAgentsLoading() {
    const cards = document.getElementById('agents_cards');
    if (cards) {
        cards.classList.remove('hidden');
        cards.innerHTML = '<div class="muted-text">Loading agents…</div>';
    }
    const wrapper = document.getElementById('agents_table_wrapper');
    if (wrapper) {
        const tbody = wrapper.querySelector('tbody');
        if (tbody) {
            const visibleColumns = agentsVM.tableCustomizer
                ? agentsVM.tableCustomizer.getVisibleColumns().length
                : 8;
            tbody.innerHTML = `<tr><td colspan="${visibleColumns}" class="muted-text">Loading agents…</td></tr>`;
        }
    }
    const metrics = document.getElementById('agents_overview_metrics');
    if (metrics && !agentsVM.metrics.summary) {
        metrics.innerHTML = '<div class="metric-card loading">Loading agent metrics…</div>';
    }
}

function renderAgentsError(error) {
    const message = error && error.message ? error.message : 'Unknown error';
    const cards = document.getElementById('agents_cards');
    if (cards) {
        cards.classList.remove('hidden');
        cards.innerHTML = `<div class="error-text">Failed to load agents: ${escapeHtml(message)}</div>`;
    }
    const wrapper = document.getElementById('agents_table_wrapper');
    if (wrapper) {
        const tbody = wrapper.querySelector('tbody');
        if (tbody) {
            const visibleColumns = agentsVM.tableCustomizer
                ? agentsVM.tableCustomizer.getVisibleColumns().length
                : 8;
            tbody.innerHTML = `<tr><td colspan="${visibleColumns}" class="error-text">Failed to load agents: ${escapeHtml(message)}</td></tr>`;
        }
    }
    const stats = document.getElementById('agents_stats');
    if (stats) {
        stats.innerHTML = `<div class="error-text">Failed to load agents: ${escapeHtml(message)}</div>`;
    }
}

function refreshAgentMetrics() {
    if (!Array.isArray(agentsVM.items) || agentsVM.items.length === 0) {
        agentsVM.metrics.summary = null;
        renderAgentsOverview();
        return;
    }
    const now = Date.now();
    if (agentsVM.metrics.summary && agentsVM.metrics.lastFetched && (now - agentsVM.metrics.lastFetched.getTime()) < AGENTS_METRICS_MAX_AGE_MS) {
        renderAgentsOverview();
        return;
    }
    agentsVM.metrics.summary = computeAgentMetrics(agentsVM.items);
    agentsVM.metrics.lastFetched = new Date();
    renderAgentsOverview();
}

function computeAgentMetrics(list) {
    const summary = {
        total: list.length,
        active: 0,
        degraded: 0,
        offline: 0,
        versions: {},
        platforms: {},
    };
    list.forEach(agent => {
        const meta = agent.__meta || {};
        const statusKey = meta.statusKey || 'offline';
        summary[statusKey] = (summary[statusKey] || 0) + 1;
        const version = meta.versionLabel || agent.version || 'Unknown';
        summary.versions[version] = (summary.versions[version] || 0) + 1;
        const platform = meta.platformLabel || agent.platform || 'Unknown';
        summary.platforms[platform] = (summary.platforms[platform] || 0) + 1;
    });
    const versionEntries = Object.entries(summary.versions).sort((a, b) => b[1] - a[1]);
    summary.primaryVersion = versionEntries.length ? versionEntries[0][0] : 'Unknown';
    summary.primaryVersionShare = versionEntries.length ? (versionEntries[0][1] / Math.max(1, summary.total)) : 0;
    summary.outdated = summary.total - (versionEntries.length ? versionEntries[0][1] : 0);
    return summary;
}

function renderAgentsOverview() {
    const container = document.getElementById('agents_overview_metrics');
    if (!container) return;
    if (!agentsVM.metrics.summary) {
        container.innerHTML = '<div class="metric-card loading">No agent metrics yet.</div>';
        return;
    }
    const summary = agentsVM.metrics.summary;
    const platforms = Object.entries(summary.platforms || {}).sort((a, b) => b[1] - a[1]).slice(0, 3);
    container.innerHTML = `
        <div class="metric-card">
            <div class="card-title">Agents Online</div>
            <div class="metric-kpi-value">${formatNumber(summary.active || 0)}</div>
            <div class="metric-kpi-label">Active of ${formatNumber(summary.total)}</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Connection Mix</div>
            <div class="metric-kpi-value">${formatNumber(summary.degraded || 0)}</div>
            <div class="metric-kpi-label">Degraded (HTTP fallback)</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Version Alignment</div>
            <div class="metric-kpi-value">${escapeHtml(summary.primaryVersion || 'Unknown')}</div>
            <div class="metric-kpi-label">${Math.round((summary.primaryVersionShare || 0) * 100)}% on this build</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Top Platforms</div>
            <div class="metric-kpi-value">${platforms.length ? escapeHtml(platforms[0][0]) : '—'}</div>
            <div class="metric-kpi-label">Most common OS</div>
        </div>
    `;
}

function refreshAgentFilters() {
    const versions = new Set();
    const platforms = new Set();
    agentsVM.items.forEach(agent => {
        if (agent.version) {
            versions.add(agent.version);
        }
        if (agent.platform) {
            platforms.add(agent.platform);
        }
    });
    const versionSelect = document.getElementById('agents_version_filter');
    if (versionSelect) {
        const current = agentsVM.filters.version;
        const options = ['<option value="">All Versions</option>', ...Array.from(versions).sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' })).map(v => `<option value="${escapeHtml(v)}">${escapeHtml(v)}</option>`)].join('');
        versionSelect.innerHTML = options;
        if (current && versions.has(current)) {
            versionSelect.value = current;
        } else {
            versionSelect.value = '';
            agentsVM.filters.version = '';
        }
    }
    const platformSelect = document.getElementById('agents_platform_filter');
    if (platformSelect) {
        const current = agentsVM.filters.platform;
        const options = ['<option value="">All Platforms</option>', ...Array.from(platforms).sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' })).map(p => `<option value="${escapeHtml(p)}">${escapeHtml(p)}</option>`)].join('');
        platformSelect.innerHTML = options;
        if (current && platforms.has(current)) {
            platformSelect.value = current;
        } else {
            platformSelect.value = '';
            agentsVM.filters.platform = '';
        }
    }
}

function applyAgentFilters() {
    if (!Array.isArray(agentsVM.items)) {
        return;
    }
    const totalStatuses = buildAgentStatusCounts();
    const filteredStatuses = buildAgentStatusCounts();
    const filtered = [];
    agentsVM.items.forEach(agent => {
        const meta = agent.__meta || {};
        const statusKey = meta.statusKey || 'offline';
        if (totalStatuses[statusKey] !== undefined) {
            totalStatuses[statusKey] += 1;
        }
        if (matchesAgentFilters(agent, agentsVM.filters)) {
            filtered.push(agent);
            if (filteredStatuses[statusKey] !== undefined) {
                filteredStatuses[statusKey] += 1;
            }
        }
    });
    agentsVM.filtered = sortAgents(filtered);
    agentsVM.stats.total = agentsVM.items.length;
    agentsVM.stats.filtered = agentsVM.filtered.length;
    agentsVM.stats.totalStatuses = totalStatuses;
    agentsVM.stats.filteredStatuses = filteredStatuses;
    renderAgentsInlineStats();
    renderAgentsActiveFilters();
    syncAgentQuickFilters();
    if (agentsVM.view === 'table') {
        renderAgentTable(agentsVM.filtered);
    } else {
        renderAgentCards(agentsVM.filtered);
    }
    syncAgentTableSortIndicators();
}

function matchesAgentFilters(agent, filters) {
    const meta = agent.__meta || {};
    const query = (filters.query || '').toLowerCase();
    if (query && (!meta.search || meta.search.indexOf(query) === -1)) {
        return false;
    }
    if (filters.version && (agent.version || '') !== filters.version) {
        return false;
    }
    if (filters.platform && (agent.platform || '') !== filters.platform) {
        return false;
    }
    const tenantId = agent.tenant_id || meta.tenantId || '';
    if (filters.tenantId && tenantId !== filters.tenantId) {
        return false;
    }
    if (filters.statuses && filters.statuses.size > 0 && !filters.statuses.has(meta.statusKey || 'offline')) {
        return false;
    }
    return true;
}

function sortAgents(list) {
    const key = agentsVM.filters.sortKey || 'last_seen';
    const dir = agentsVM.filters.sortDir === 'asc' ? 1 : -1;
    return list.slice().sort((a, b) => {
        const aVal = getAgentSortValue(a, key);
        const bVal = getAgentSortValue(b, key);
        if (aVal < bVal) return -1 * dir;
        if (aVal > bVal) return 1 * dir;
        const aName = (a.name || a.hostname || a.agent_id || '').toLowerCase();
        const bName = (b.name || b.hostname || b.agent_id || '').toLowerCase();
        if (aName < bName) return -1;
        if (aName > bName) return 1;
        return 0;
    });
}

function getAgentSortValue(agent, key) {
    const meta = agent.__meta || {};
    switch (key) {
        case 'name':
            return getAgentDisplayName(agent).toLowerCase();
        case 'status':
            return AGENT_STATUS_ORDER[meta.statusKey || 'offline'] || 0;
        case 'version':
            return (agent.version || '').toLowerCase();
        case 'platform':
            return (agent.platform || '').toLowerCase();
        case 'tenant':
            return formatTenantDisplay(agent.tenant_id || meta.tenantId || '').toLowerCase();
        case 'last_seen':
        default:
            return meta.lastSeenMs || 0;
    }
}

function renderAgentsInlineStats() {
    const container = document.getElementById('agents_stats');
    if (!container) return;
    const statuses = agentsVM.stats.filteredStatuses || {};
    container.innerHTML = `
        <div><strong>Total:</strong> ${formatNumber(agentsVM.stats.total || 0)}</div>
        <div><strong>Showing:</strong> ${formatNumber(agentsVM.stats.filtered || 0)}</div>
        <div>
            <span class="status-pill healthy">Active ${formatNumber(statuses.active || 0)}</span>
            <span class="status-pill warning">Degraded ${formatNumber(statuses.degraded || 0)}</span>
            <span class="status-pill error">Offline ${formatNumber(statuses.offline || 0)}</span>
        </div>
    `;
}

function renderAgentsActiveFilters() {
    const container = document.getElementById('agents_active_filters');
    if (!container) return;
    const chips = [];
    const filters = agentsVM.filters;
    if (filters.query) {
        chips.push(buildFilterChip('Search', filters.query, 'search'));
    }
    if (filters.version) {
        chips.push(buildFilterChip('Version', filters.version, 'version'));
    }
    if (filters.platform) {
        chips.push(buildFilterChip('Platform', filters.platform, 'platform'));
    }
    if (filters.tenantId) {
        chips.push(buildFilterChip('Tenant', formatTenantDisplay(filters.tenantId), 'tenant'));
    }
    if (filters.statuses && filters.statuses.size > 0 && filters.statuses.size < AGENT_STATUS_KEYS.length) {
        chips.push(buildFilterChip('Status', Array.from(filters.statuses).map(s => AGENT_STATUS_LABELS[s] || s).join(', '), 'statuses'));
    }
    if (chips.length === 0) {
        container.innerHTML = '';
        container.classList.add('hidden');
        return;
    }
    container.classList.remove('hidden');
    container.innerHTML = chips.join('');
}

function handleAgentFilterChipRemove(filterKey) {
    switch (filterKey) {
        case 'search':
            agentsVM.filters.query = '';
            const searchInput = document.getElementById('agents_search');
            if (searchInput) searchInput.value = '';
            break;
        case 'version':
            agentsVM.filters.version = '';
            const versionSelect = document.getElementById('agents_version_filter');
            if (versionSelect) versionSelect.value = '';
            break;
        case 'platform':
            agentsVM.filters.platform = '';
            const platformSelect = document.getElementById('agents_platform_filter');
            if (platformSelect) platformSelect.value = '';
            break;
        case 'tenant':
            agentsVM.filters.tenantId = '';
            const tenantSelect = document.getElementById('agents_tenant_filter');
            if (tenantSelect) tenantSelect.value = '';
            break;
        case 'statuses':
            agentsVM.filters.statuses = new Set(AGENT_STATUS_KEYS);
            break;
        default:
            return;
    }
    applyAgentFilters();
}

function syncAgentQuickFilters() {
    document.querySelectorAll('#agents_status_filter [data-status]').forEach(btn => {
        const key = btn.getAttribute('data-status');
        const active = agentsVM.filters.statuses.has(key);
        btn.classList.toggle('active', active);
        const baseLabel = btn.getAttribute('data-label') || btn.textContent.trim();
        const count = agentsVM.stats.totalStatuses?.[key] || 0;
        btn.innerHTML = `${escapeHtml(baseLabel)} <span class="pill-count">${formatNumber(count)}</span>`;
    });
}

function toggleAgentStatusFilter(statusKey) {
    if (!AGENT_STATUS_KEYS.includes(statusKey)) return;
    const next = new Set(agentsVM.filters.statuses || AGENT_STATUS_KEYS);
    if (next.has(statusKey)) {
        next.delete(statusKey);
    } else {
        next.add(statusKey);
    }
    if (next.size === 0) {
        AGENT_STATUS_KEYS.forEach(key => next.add(key));
    }
    agentsVM.filters.statuses = next;
    applyAgentFilters();
}

function resetAgentFilters() {
    agentsVM.filters.query = '';
    agentsVM.filters.version = '';
    agentsVM.filters.platform = '';
    agentsVM.filters.tenantId = '';
    agentsVM.filters.statuses = new Set(AGENT_STATUS_KEYS);
    const searchInput = document.getElementById('agents_search');
    if (searchInput) searchInput.value = '';
    const versionSelect = document.getElementById('agents_version_filter');
    if (versionSelect) versionSelect.value = '';
    const platformSelect = document.getElementById('agents_platform_filter');
    if (platformSelect) platformSelect.value = '';
    const tenantSelect = document.getElementById('agents_tenant_filter');
    if (tenantSelect) tenantSelect.value = '';
    applyAgentFilters();
}

function setAgentsView(view) {
    const nextView = AGENTS_VIEW_OPTIONS.includes(view) ? view : 'cards';
    if (agentsVM.view === nextView) {
        return;
    }
    agentsVM.view = nextView;
    persistUIState(SERVER_UI_STATE_KEYS.AGENTS_VIEW, nextView);
    syncAgentsViewToggle();
    if (agentsVM.view === 'table') {
        renderAgentTable(agentsVM.filtered);
    } else {
        renderAgentCards(agentsVM.filtered);
    }
}

function syncAgentsViewToggle() {
    const toggle = document.getElementById('agents_view_toggle');
    if (!toggle) return;
    toggle.querySelectorAll('[data-view]').forEach(btn => {
        const view = btn.getAttribute('data-view');
        const active = view === agentsVM.view;
        btn.classList.toggle('active', active);
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
}

function setAgentSort(key, dir) {
    const nextKey = AGENTS_SORT_KEYS.includes(key) ? key : 'last_seen';
    const nextDir = dir === 'asc' ? 'asc' : 'desc';
    if (agentsVM.filters.sortKey === nextKey && agentsVM.filters.sortDir === nextDir) {
        return;
    }
    agentsVM.filters.sortKey = nextKey;
    agentsVM.filters.sortDir = nextDir;
    persistUIState(SERVER_UI_STATE_KEYS.AGENTS_SORT_KEY, nextKey);
    persistUIState(SERVER_UI_STATE_KEYS.AGENTS_SORT_DIR, nextDir);
    syncAgentSortControls();
    applyAgentFilters();
}

function syncAgentSortControls() {
    const sortSelect = document.getElementById('agents_sort_select');
    if (sortSelect && sortSelect.value !== agentsVM.filters.sortKey) {
        sortSelect.value = agentsVM.filters.sortKey;
    }
    const sortDirBtn = document.getElementById('agents_sort_dir_btn');
    const sortDirIcon = document.getElementById('agents_sort_dir_icon');
    if (sortDirBtn) {
        sortDirBtn.dataset.dir = agentsVM.filters.sortDir;
        sortDirBtn.setAttribute('aria-label', agentsVM.filters.sortDir === 'asc' ? 'Sort ascending' : 'Sort descending');
    }
    if (sortDirIcon) {
        sortDirIcon.textContent = agentsVM.filters.sortDir === 'asc' ? '↑' : '↓';
    }
}

function syncAgentTableSortIndicators() {
    const head = document.querySelector('#agents_table thead');
    if (!head) return;
    head.querySelectorAll('th[data-sort-key]').forEach(th => {
        const key = th.getAttribute('data-sort-key');
        if (key === agentsVM.filters.sortKey) {
            th.classList.add('sorted');
            th.setAttribute('aria-sort', agentsVM.filters.sortDir === 'asc' ? 'ascending' : 'descending');
        } else {
            th.classList.remove('sorted');
            th.removeAttribute('aria-sort');
        }
    });
}

function handleAgentTableSortClick(event) {
    const target = event.target.closest('th[data-sort-key]');
    if (!target) {
        return;
    }
    const key = target.getAttribute('data-sort-key');
    if (!key) {
        return;
    }
    const nextDir = (agentsVM.filters.sortKey === key && agentsVM.filters.sortDir === 'asc') ? 'desc' : 'asc';
    setAgentSort(key, nextDir);
}

function renderAgentCards(agents) {
    const cards = document.getElementById('agents_cards');
    const wrapper = document.getElementById('agents_table_wrapper');
    if (!cards) return;
    if (wrapper) {
        wrapper.classList.add('hidden');
    }
    cards.classList.remove('hidden');
    if (!agents || agents.length === 0) {
        cards.innerHTML = '<div class="muted-text">No agents match the current filters.</div>';
        return;
    }
    cards.innerHTML = agents.map(agent => renderAgentCard(agent)).join('');
}

function renderAgentVersionCell(agent, forTable = false) {
    const currentVersion = agent.version || '';
    const latestVersion = agentsVM.latestVersion;
    const displayVersion = escapeHtml(currentVersion || 'N/A');
    const agentId = agent.agent_id || '';

    // Check if there's an active update for this agent
    const updateState = agentsVM.updateState[agentId];
    if (updateState) {
        const rawStatus = updateState.status || '';
        const status = (() => {
            switch (rawStatus) {
                case 'pending':
                    return 'checking';
                case 'staging':
                case 'applying':
                    return 'ready';
                case 'succeeded':
                    return 'complete';
                case 'rolled_back':
                    return 'failed';
                default:
                    return rawStatus;
            }
        })();

        // Calculate smooth progress percentage based on phase
        const smoothProgress = getSmoothedUpdateProgress(agentId, status, updateState);

        // Show progress button for active states
        if (status === 'checking' || status === 'downloading' || status === 'ready' || status === 'restarting' || status === 'verifying') {
            const canCancel = status !== 'ready' && status !== 'restarting' && status !== 'verifying';
            const cancelBtn = canCancel
                ? `<button class="update-btn cancel" data-action="cancel-update" data-agent-id="${escapeHtml(agentId)}" title="Cancel update">✕</button>`
                : '';
            // Progress button with fill effect
            const progressBtn = `<button class="update-btn progress-btn" data-agent-id="${escapeHtml(agentId)}" disabled style="--progress: ${smoothProgress}%">${Math.round(smoothProgress)}%</button>`;
            const content = `${progressBtn}${cancelBtn}`;
            if (forTable) {
                return `<div style="display:flex;align-items:center;gap:6px;">${displayVersion} ${content}</div>`;
            }
            return `${displayVersion} ${content}`;
        }

        // Show failed state briefly
        if (status === 'failed') {
            const errorMsg = updateState.error || 'Failed';
            const content = `<span class="update-error" title="${escapeHtml(errorMsg)}">✕ Failed</span>`;
            if (forTable) {
                return `<div style="display:flex;align-items:center;gap:6px;">${displayVersion} ${content}</div>`;
            }
            return `${displayVersion} ${content}`;
        }

        // Skipped update (policy or already current)
        if (status === 'skipped') {
            const content = `<span class="update-progress">Skipped</span>`;
            if (forTable) {
                return `<div style="display:flex;align-items:center;gap:6px;">${displayVersion} ${content}</div>`;
            }
            return `${displayVersion} ${content}`;
        }

        // Show complete state briefly
        if (status === 'complete') {
            const content = `<span class="update-complete">✓ Updated</span>`;
            if (forTable) {
                return `<div style="display:flex;align-items:center;gap:6px;">${displayVersion} ${content}</div>`;
            }
            return `${displayVersion} ${content}`;
        }
    }

    // Check if update is available (normal state) - only show if latest is actually newer
    if (latestVersion && currentVersion && currentVersion !== 'N/A' && compareVersions(latestVersion, currentVersion) > 0) {
        // Check for WebSocket connection using connection_type field
        const connectionType = (agent.connection_type || '').toLowerCase();
        const canUpdate = connectionType === 'ws';
        const tooltip = canUpdate ? `Update available: ${latestVersion}` : 'Agent not connected via WebSocket';
        const buttonClass = canUpdate ? 'update-btn' : 'update-btn disabled';
        const updateBtn = `<button class="${buttonClass}" data-action="update-agent" data-agent-id="${escapeHtml(agentId)}" title="${escapeHtml(tooltip)}" ${canUpdate ? '' : 'disabled'}>↑ ${escapeHtml(latestVersion)}</button>`;
        if (forTable) {
            return `<div style="display:flex;align-items:center;gap:6px;">${displayVersion} ${updateBtn}</div>`;
        }
        return `${displayVersion} ${updateBtn}`;
    }
    return displayVersion;
}

// Calculate smoothed progress for update animations
// Phases: checking (0-5%), downloading (5-55%), ready/installing (55-85%), restarting (85-95%), verifying (95-99%)
function getSmoothedUpdateProgress(agentId, status, updateState) {
    const now = Date.now();
    const startTime = updateState.timestamp || now;
    const elapsed = now - startTime;

    // Initialize or get animation state
    if (!agentsVM.updateAnimations) {
        agentsVM.updateAnimations = {};
    }
    let anim = agentsVM.updateAnimations[agentId];
    if (!anim || anim.status !== status) {
        // Status changed, start new animation from current display progress or phase start
        const phaseStart = getPhaseStartPercent(status);
        const previousProgress = anim ? anim.displayProgress : phaseStart;
        anim = {
            status,
            startTime: now,
            startProgress: Math.max(previousProgress, phaseStart),
            displayProgress: Math.max(previousProgress, phaseStart)
        };
        agentsVM.updateAnimations[agentId] = anim;
    }

    const phaseEnd = getPhaseEndPercent(status);
    const phaseDuration = getPhaseDuration(status);

    // For downloading, use actual progress from agent (scaled to 5-55% range)
    if (status === 'downloading') {
        const rawProgress = updateState.progress || 0;
        const targetProgress = 5 + (rawProgress * 0.5); // Scale 0-100% to 5-55%
        // Smooth towards target
        const progressDiff = targetProgress - anim.displayProgress;
        anim.displayProgress += progressDiff * 0.3; // Ease towards target
        return Math.min(anim.displayProgress, phaseEnd);
    }

    // For other phases, animate time-based towards phase end
    const phaseElapsed = now - anim.startTime;
    const phaseProgress = Math.min(phaseElapsed / phaseDuration, 1);
    // Ease out cubic for smooth deceleration at phase end
    const easedProgress = 1 - Math.pow(1 - phaseProgress, 3);
    const targetProgress = anim.startProgress + (phaseEnd - anim.startProgress) * easedProgress * 0.95; // Don't quite reach end

    // Smooth update
    const diff = targetProgress - anim.displayProgress;
    anim.displayProgress += diff * 0.2;

    return Math.min(Math.max(anim.displayProgress, 0), 99);
}

function getPhaseStartPercent(status) {
    switch (status) {
        case 'checking': return 0;
        case 'downloading': return 5;
        case 'ready': return 55;
        case 'restarting': return 85;
        case 'verifying': return 95;
        default: return 0;
    }
}

function getPhaseEndPercent(status) {
    switch (status) {
        case 'checking': return 5;
        case 'downloading': return 55;
        case 'ready': return 85;
        case 'restarting': return 95;
        case 'verifying': return 99;
        default: return 100;
    }
}

function getPhaseDuration(status) {
    switch (status) {
        case 'checking': return 3000; // 3s for checking
        case 'downloading': return 30000; // 30s typical download
        case 'ready': return 10000; // 10s for install/staging
        case 'restarting': return 15000; // 15s for restart
        case 'verifying': return 5000; // 5s for verification
        default: return 10000;
    }
}

function renderAgentsTableHeader() {
    const thead = document.getElementById('agents_table_header');
    if (!thead) return;

    if (!agentsVM.tableCustomizer) {
        // Fallback to static headers if customizer not available
        // Actions column removed - using context menu instead (right-click)
        thead.innerHTML = `
            <th data-sort-key="name">Agent</th>
            <th data-sort-key="tenant">Tenant</th>
            <th data-sort-key="status">Status</th>
            <th data-sort-key="connection">Connection</th>
            <th data-sort-key="platform">Platform</th>
            <th data-sort-key="version">Version</th>
            <th data-sort-key="last_seen">Last Seen</th>
        `;
        return;
    }

    // Use table customizer to render dynamic headers
    thead.innerHTML = agentsVM.tableCustomizer.renderHeader();

    // Bind header events (sorting, resize handles)
    const table = document.getElementById('agents_table');
    if (table) {
        const theadElement = table.querySelector('thead');
        if (theadElement) {
            agentsVM.tableCustomizer.bindHeaderEvents(theadElement);
        }
    }
}

function renderAgentTable(agents) {
    const cards = document.getElementById('agents_cards');
    const wrapper = document.getElementById('agents_table_wrapper');
    if (!wrapper) return;
    if (cards) {
        cards.classList.add('hidden');
    }
    wrapper.classList.remove('hidden');
    const tbody = wrapper.querySelector('tbody');
    if (!tbody) return;

    // Calculate visible column count for colspan
    const visibleColumns = agentsVM.tableCustomizer
        ? agentsVM.tableCustomizer.getVisibleColumns().length
        : 8;

    if (!agents || agents.length === 0) {
        tbody.innerHTML = `<tr><td colspan="${visibleColumns}" class="muted-text">No agents match the current filters.</td></tr>`;
        return;
    }

    // Use table customizer if available
    if (agentsVM.tableCustomizer) {
        const rows = agents.map(agent => {
            const meta = agent.__meta || {};
            return `
                <tr data-agent-id="${escapeHtml(agent.agent_id || '')}" class="agent-row-clickable" title="Click to view details, right-click for actions">
                    ${agentsVM.tableCustomizer.renderRow(agent, meta)}
                </tr>
            `;
        }).join('');
        tbody.innerHTML = rows;
        return;
    }

    // Fallback to original rendering if no customizer
    // Actions column removed - using context menu instead (right-click)
    const rows = agents.map(agent => {
        const meta = agent.__meta || {};
        const tenantLabel = formatTenantDisplay(agent.tenant_id || meta.tenantId || '');
        return `
            <tr data-agent-id="${escapeHtml(agent.agent_id || '')}" class="agent-row-clickable" title="Click to view details, right-click for actions">
                <td>
                    <div class="table-primary">${escapeHtml(getAgentDisplayName(agent))}</div>
                    <div class="muted-text">${escapeHtml(agent.hostname || '')}</div>
                </td>
                <td>${escapeHtml(tenantLabel)}</td>
                <td>${renderAgentStatusBadge(meta)}</td>
                <td>${escapeHtml(agent.platform || 'Unknown')}</td>
                <td>${renderAgentVersionCell(agent, true)}</td>
                <td title="${escapeHtml(meta.lastSeenTooltip || 'Never')}">${escapeHtml(meta.lastSeenRelative || 'Never')}</td>
            </tr>
        `;
    }).join('');
    tbody.innerHTML = rows;
}

function renderAgentCard(agent) {
    const meta = agent.__meta || {};
    const registeredDate = agent.registered_at ? new Date(agent.registered_at) : null;
    const statusColor = AGENT_STATUS_COLORS[meta.statusKey || 'offline'] || 'var(--muted)';
    const tenantLabel = formatTenantDisplay(agent.tenant_id || meta.tenantId || '');
    return `
        <div class="device-card agent-card-clickable" data-agent-id="${escapeHtml(agent.agent_id || '')}" data-agent-name="${escapeHtml(getAgentDisplayName(agent))}" title="Click to view details, right-click for actions">
            <div class="device-card-header">
                <div>
                    <div style="display:flex;align-items:center;gap:8px">
                        <div class="device-card-title">${escapeHtml(getAgentDisplayName(agent))}</div>
                        <span class="agent-joined-bubble" style="margin-left:8px;display:${registeredDate ? 'inline-flex' : 'none'};align-items:center;padding:2px 6px;border-radius:12px;background:var(--panel);font-size:12px;color:var(--muted);border:1px solid var(--border);">${registeredDate ? 'Joined' : ''}</span>
                    </div>
                    <div class="device-card-subtitle">
                        <span class="copyable" data-copy="${escapeHtml(agent.hostname || '')}" title="Click to copy Hostname">${escapeHtml(agent.hostname || 'N/A')}</span>
                        <span style="margin-left:8px;color:var(--muted);font-size:12px;" class="copyable" data-copy="${escapeHtml(agent.agent_id || '')}" title="Click to copy Agent ID">${escapeHtml(agent.agent_id || '')}</span>
                    </div>
                </div>
            </div>
            <div class="device-card-info">
                <div class="device-card-row">
                    <span class="device-card-label">Status</span>
                    <span class="device-card-value agent-status-value">${renderAgentStatusBadge(meta)}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">IP Address</span>
                    <span class="device-card-value copyable" data-copy="${escapeHtml(agent.ip || '')}" title="Click to copy">${escapeHtml(agent.ip || 'N/A')}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Platform</span>
                    <span class="device-card-value">${escapeHtml(agent.platform || 'Unknown')}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Tenant</span>
                    <span class="device-card-value">${escapeHtml(tenantLabel)}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Version</span>
                    <span class="device-card-value agent-version-cell">${renderAgentVersionCell(agent, false)}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Last Seen</span>
                    <span class="device-card-value agent-last-seen" title="${escapeHtml(meta.lastSeenTooltip || 'Never')}">${escapeHtml(meta.lastSeenRelative || 'Never')}</span>
                </div>
                ${registeredDate ? `
                <div class="device-card-row">
                    <span class="device-card-label">Registered</span>
                    <span class="device-card-value" title="${registeredDate.toLocaleString()}">${registeredDate.toLocaleDateString()}</span>
                </div>` : ''}
            </div>
            <div class="device-card-hint muted-text" style="font-size:11px;padding:8px 12px;text-align:center;border-top:1px solid var(--border);">
                Right-click for actions
            </div>
        </div>
    `;
}

function renderAgentStatusBadge(meta) {
    const code = meta.statusKey || 'offline';
    const label = AGENT_STATUS_LABELS[code] || meta.statusLabel || 'Unknown';
    const tone = code === 'active' ? 'healthy' : code === 'offline' ? 'error' : 'warning';
    return `<span class="status-pill ${tone}">${escapeHtml(label)}</span>`;
}

function findAgentCardElement(agentId) {
    const cards = document.getElementById('agents_cards');
    if (!cards) return null;
    const safeId = (typeof CSS !== 'undefined' && CSS.escape) ? CSS.escape(agentId || '') : String(agentId || '').replace(/\\/g, '\\\\').replace(/"/g, '\\"');
    return cards.querySelector(`[data-agent-id="${safeId}"]`);
}

function setAgentJoined(agentId, joined) {
    const card = findAgentCardElement(agentId);
    if (!card) return;
    const bubble = card.querySelector('.agent-joined-bubble');
    if (!bubble) return;
    if (joined) {
        bubble.style.display = 'inline-flex';
        bubble.textContent = 'Joined';
    } else {
        bubble.style.display = 'none';
        bubble.textContent = '';
    }
}

function upsertAgentRecord(record) {
    if (!record) return;
    if (!Array.isArray(agentsVM.items)) {
        agentsVM.items = [];
    }
    let updated = false;
    agentsVM.items = agentsVM.items.map(agent => {
        if (agent.agent_id && record.agent_id && agent.agent_id === record.agent_id) {
            updated = true;
            return enrichSingleAgent({ ...agent, ...record });
        }
        return agent;
    });
    if (!updated) {
        agentsVM.items.push(enrichSingleAgent(record));
    }
    agentsVM.stats.total = agentsVM.items.length;
    patchAgentDirectory(record);
    refreshAgentFilters();
    refreshAgentMetrics();
    applyAgentFilters();
}

function updateAgentConnection(agentId, connType) {
    const index = agentsVM.items.findIndex(agent => agent.agent_id === agentId);
    if (index === -1) {
        loadAgents(true);
        return;
    }
    const next = enrichSingleAgent({ ...agentsVM.items[index], connection_type: connType });
    agentsVM.items.splice(index, 1, next);
    patchAgentDirectory(next);
    refreshAgentMetrics();
    applyAgentFilters();
}

function updateAgentHeartbeat(agentId, status, lastSeen) {
    const index = agentsVM.items.findIndex(agent => agent.agent_id === agentId);
    if (index === -1) {
        loadAgents(true);
        return;
    }
    const updates = { ...agentsVM.items[index], status: status || agentsVM.items[index].status };
    if (lastSeen) {
        updates.last_seen = lastSeen;
    }
    const next = enrichSingleAgent(updates);
    agentsVM.items.splice(index, 1, next);
    patchAgentDirectory(next);
    refreshAgentMetrics();
    applyAgentFilters();
}

// Fetch a single agent's data and update the list
async function fetchSingleAgent(agentId) {
    try {
        const response = await fetch(`/api/v1/agents/${agentId}`);
        if (!response.ok) {
            window.__pm_shared.warn('Failed to fetch single agent:', response.status);
            return null;
        }
        const agentData = await response.json();
        if (agentData) {
            const index = agentsVM.items.findIndex(a => a.agent_id === agentId);
            const enriched = enrichSingleAgent(agentData);
            if (index !== -1) {
                agentsVM.items.splice(index, 1, enriched);
            } else {
                agentsVM.items.push(enriched);
            }
            patchAgentDirectory(enriched);
            refreshAgentMetrics();
            applyAgentFilters();
            refreshAgentVersionCell(agentId);
            return enriched;
        }
    } catch (err) {
        window.__pm_shared.warn('Error fetching single agent:', err);
    }
    return null;
}

// Handle agent reconnecting after an update restart
async function handleAgentReconnectAfterUpdate(agentId, updateState) {
    const agentName = agentsVM.items.find(a => a.agent_id === agentId)?.name || agentId;

    // Transition to "verifying" state
    agentsVM.updateState[agentId] = {
        ...updateState,
        status: 'verifying',
        message: 'Agent reconnected, verifying update...',
        timestamp: Date.now()
    };
    refreshAgentVersionCell(agentId);

    // Small delay to let agent settle after restart
    await new Promise(r => setTimeout(r, 1500));

    // Fetch fresh agent data
    const updatedAgent = await fetchSingleAgent(agentId);
    if (!updatedAgent) {
        // Couldn't fetch - clear state with warning
        window.__pm_shared.showToast(`${agentName}: Reconnected but couldn't verify update`, 'warning');
        delete agentsVM.updateState[agentId];
        refreshAgentVersionCell(agentId);
        return;
    }

    const newVersion = updatedAgent.version;
    const previousVersion = updateState.previousVersion;
    const targetVersion = updateState.targetVersion;

    // Check if version changed
    if (previousVersion && newVersion && newVersion !== previousVersion) {
        // Version changed - update succeeded!
        const versionMatch = targetVersion && newVersion === targetVersion;
        window.__pm_shared.showToast(
            `${agentName}: Update complete! ${previousVersion} → ${newVersion}`,
            'success'
        );
        agentsVM.updateState[agentId] = {
            ...updateState,
            status: 'complete',
            message: versionMatch ? 'Update verified' : `Updated to ${newVersion}`,
            timestamp: Date.now()
        };
        refreshAgentVersionCell(agentId);
        // Clear state after showing success
        setTimeout(() => {
            delete agentsVM.updateState[agentId];
            refreshAgentVersionCell(agentId);
        }, 3000);
    } else if (previousVersion && newVersion === previousVersion) {
        // Same version - update may have failed or was a no-op
        window.__pm_shared.showToast(
            `${agentName}: Reconnected with same version (${newVersion})`,
            'warning'
        );
        delete agentsVM.updateState[agentId];
        refreshAgentVersionCell(agentId);
    } else {
        // Couldn't determine - clear state
        window.__pm_shared.showToast(`${agentName}: Reconnected (version: ${newVersion || 'unknown'})`, 'info');
        delete agentsVM.updateState[agentId];
        refreshAgentVersionCell(agentId);
    }
}

function enrichAgents(list) {
    if (!Array.isArray(list)) return [];
    return list.map(item => enrichSingleAgent(item));
}

function enrichSingleAgent(agent) {
    if (!agent || typeof agent !== 'object') {
        return agent;
    }
    // Derive status from connection_type: ws=active, http=degraded, none/missing=offline
    const statusKey = normalizeAgentStatusFromConnection(agent.connection_type);
    const statusLabel = AGENT_STATUS_LABELS[statusKey] || statusKey;
    const lastSeenIso = agent.last_seen || agent.last_heartbeat || agent.updated_at;
    const lastSeenDate = lastSeenIso ? new Date(lastSeenIso) : null;
    const tenantId = agent.tenant_id || '';
    const tenantLabel = tenantId ? tenantDisplayNameById(tenantId) : '';
    return {
        ...agent,
        __meta: {
            statusKey,
            statusLabel,
            versionLabel: agent.version || 'Unknown',
            platformLabel: agent.platform || 'Unknown',
            lastSeenRelative: lastSeenDate ? formatRelativeTime(lastSeenDate) : 'Never',
            lastSeenTooltip: lastSeenDate ? lastSeenDate.toLocaleString() : 'Never',
            lastSeenMs: lastSeenDate ? lastSeenDate.getTime() : 0,
            tenantId,
            search: buildAgentSearchBlob(agent, tenantLabel || tenantId),
        }
    };
}

function buildAgentSearchBlob(agent, tenantLabel) {
    const parts = [
        agent.agent_id,
        agent.name,
        agent.hostname,
        agent.ip,
        agent.platform,
        agent.version,
        agent.connection_type,
        tenantLabel,
        agent.tenant_id,
    ].filter(Boolean);
    return parts.join(' ').toLowerCase();
}

function normalizeAgentStatusFromConnection(connectionType) {
    const conn = (connectionType || '').toLowerCase();
    if (conn === 'ws' || conn.includes('websocket')) {
        return 'active';
    }
    if (conn === 'http' || conn.includes('http')) {
        return 'degraded';
    }
    return 'offline';
}

function buildAgentStatusCounts() {
    const map = {};
    AGENT_STATUS_KEYS.forEach(key => { map[key] = 0; });
    return map;
}

// Device helpers for server UI
function addOrUpdateDeviceCard(device) {
    const container = document.getElementById('devices_cards');
    if (!container) return;
    const serial = device.serial || '';
    if (!serial) {
        // fallback: reload full devices
        loadDevices();
        return;
    }
    const existing = container.querySelector(`[data-serial="${serial}"]`);
    const cardHtml = renderServerDeviceCard(device);
    if (existing) {
        existing.outerHTML = cardHtml;
    } else {
        // insert at top
        container.insertAdjacentHTML('afterbegin', cardHtml);
    }
}

// ====== Selection Helpers (file-explorer style) ======

/**
 * Updates visual selection state for agent rows/cards
 */
function updateAgentSelectionUI() {
    // Update table rows
    document.querySelectorAll('tr.agent-row-clickable').forEach(row => {
        const agentId = row.getAttribute('data-agent-id');
        if (agentsVM.selection.selectedIds.has(agentId)) {
            row.classList.add('agent-row-selected');
        } else {
            row.classList.remove('agent-row-selected');
        }
    });
    // Update cards
    document.querySelectorAll('.agent-card-clickable').forEach(card => {
        const agentId = card.getAttribute('data-agent-id');
        if (agentsVM.selection.selectedIds.has(agentId)) {
            card.classList.add('agent-card-selected');
        } else {
            card.classList.remove('agent-card-selected');
        }
    });
}

/**
 * Updates visual selection state for device rows/cards
 */
function updateDeviceSelectionUI() {
    // Update table rows
    document.querySelectorAll('tr.device-row-clickable').forEach(row => {
        const serial = row.getAttribute('data-serial');
        const ip = row.getAttribute('data-ip');
        const id = serial || ip;
        if (devicesVM.selection.selectedIds.has(id)) {
            row.classList.add('device-row-selected');
        } else {
            row.classList.remove('device-row-selected');
        }
    });
    // Update cards
    document.querySelectorAll('.device-card-clickable').forEach(card => {
        const serial = card.getAttribute('data-serial');
        const ip = card.getAttribute('data-ip');
        const id = serial || ip;
        if (devicesVM.selection.selectedIds.has(id)) {
            card.classList.add('device-card-selected');
        } else {
            card.classList.remove('device-card-selected');
        }
    });
}

/**
 * Handles agent selection with file-explorer style modifiers
 * @param {string} agentId - The agent ID being clicked
 * @param {MouseEvent} event - The click event for modifier key detection
 */
function handleAgentSelection(agentId, event) {
    const selection = agentsVM.selection;

    if (event.shiftKey && selection.lastSelected) {
        // Shift+click: select range
        const filtered = agentsVM.filtered;
        const ids = filtered.map(a => a.id);
        const lastIdx = ids.indexOf(selection.lastSelected);
        const currIdx = ids.indexOf(agentId);

        if (lastIdx !== -1 && currIdx !== -1) {
            const start = Math.min(lastIdx, currIdx);
            const end = Math.max(lastIdx, currIdx);
            // Clear selection if not ctrl pressed, then select range
            if (!event.ctrlKey && !event.metaKey) {
                selection.selectedIds.clear();
            }
            for (let i = start; i <= end; i++) {
                selection.selectedIds.add(ids[i]);
            }
        }
    } else if (event.ctrlKey || event.metaKey) {
        // Ctrl+click: toggle individual selection
        if (selection.selectedIds.has(agentId)) {
            selection.selectedIds.delete(agentId);
        } else {
            selection.selectedIds.add(agentId);
        }
        selection.lastSelected = agentId;
    } else {
        // Normal click: clear selection, select only this one
        selection.selectedIds.clear();
        selection.selectedIds.add(agentId);
        selection.lastSelected = agentId;
    }

    updateAgentSelectionUI();
}

/**
 * Handles device selection with file-explorer style modifiers
 * @param {string} deviceId - The device ID (serial or IP) being clicked
 * @param {MouseEvent} event - The click event for modifier key detection
 */
function handleDeviceSelection(deviceId, event) {
    const selection = devicesVM.selection;

    if (event.shiftKey && selection.lastSelected) {
        // Shift+click: select range
        const filtered = devicesVM.filtered;
        const ids = filtered.map(d => d.serial || d.ip);
        const lastIdx = ids.indexOf(selection.lastSelected);
        const currIdx = ids.indexOf(deviceId);

        if (lastIdx !== -1 && currIdx !== -1) {
            const start = Math.min(lastIdx, currIdx);
            const end = Math.max(lastIdx, currIdx);
            // Clear selection if not ctrl pressed, then select range
            if (!event.ctrlKey && !event.metaKey) {
                selection.selectedIds.clear();
            }
            for (let i = start; i <= end; i++) {
                selection.selectedIds.add(ids[i]);
            }
        }
    } else if (event.ctrlKey || event.metaKey) {
        // Ctrl+click: toggle individual selection
        if (selection.selectedIds.has(deviceId)) {
            selection.selectedIds.delete(deviceId);
        } else {
            selection.selectedIds.add(deviceId);
        }
        selection.lastSelected = deviceId;
    } else {
        // Normal click: clear selection, select only this one
        selection.selectedIds.clear();
        selection.selectedIds.add(deviceId);
        selection.lastSelected = deviceId;
    }

    updateDeviceSelectionUI();
}

/**
 * Clears all agent selections
 */
function clearAgentSelection() {
    agentsVM.selection.selectedIds.clear();
    agentsVM.selection.lastSelected = null;
    updateAgentSelectionUI();
}

/**
 * Clears all device selections
 */
function clearDeviceSelection() {
    devicesVM.selection.selectedIds.clear();
    devicesVM.selection.lastSelected = null;
    updateDeviceSelectionUI();
}

// ====== Agent Details ======
async function viewAgentDetails(agentId) {
    try {
        // Show modal overlay immediately with loading state
        const overlay = document.getElementById('agent_details_overlay');
        const body = document.getElementById('agent_details_body');
        const title = document.getElementById('agent_details_title');

        overlay.style.display = 'flex';
        body.innerHTML = '<div style="color:var(--muted);text-align:center;padding:40px;">Loading agent details...</div>';
        title.textContent = 'Agent Details';

        const response = await fetch(`/api/v1/agents/${agentId}`);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const agent = await response.json();
        renderAgentDetailsModal(agent);
    } catch (error) {
        window.__pm_shared.error('Failed to load agent details:', error);
        const body = document.getElementById('agent_details_body');
        body.innerHTML = `<div style="color:var(--error);text-align:center;padding:40px;">Failed to load agent details: ${escapeHtml(error.message)}</div>`;
        window.__pm_shared.showToast('Failed to load agent details', 'error');
    }
}

function renderAgentDetailsModal(agent) {
    const title = document.getElementById('agent_details_title');
    const body = document.getElementById('agent_details_body');

    title.textContent = `Agent: ${getAgentDisplayName(agent)}`;

    const lastSeenDate = agent.last_seen ? new Date(agent.last_seen) : null;
    const registeredDate = agent.registered_at ? new Date(agent.registered_at) : null;
    const lastHeartbeatDate = agent.last_heartbeat ? new Date(agent.last_heartbeat) : null;
    const lastDeviceSyncDate = agent.last_device_sync ? new Date(agent.last_device_sync) : null;
    const lastMetricsSyncDate = agent.last_metrics_sync ? new Date(agent.last_metrics_sync) : null;
    const connectionType = (agent.connection_type || '').toLowerCase();
    const commandEnabled = connectionType === 'ws';
    const commandDisabledAttr = commandEnabled ? '' : 'disabled title="Requires active WebSocket connection"';
    const commandHint = commandEnabled
        ? 'Commands are delivered instantly over the active WebSocket tunnel.'
        : 'Agent must be connected via WebSocket to receive remote commands.';

    // Calculate uptime
    let uptimeText = 'N/A';
    if (registeredDate && lastSeenDate) {
        const uptimeMs = lastSeenDate - registeredDate;
        const days = Math.floor(uptimeMs / (1000 * 60 * 60 * 24));
        const hours = Math.floor((uptimeMs % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60));
        uptimeText = `${days}d ${hours}h`;
    }

    const statusColors = {
        'active': 'var(--success)',
        'degraded': 'var(--warning)',
        'offline': 'var(--error)'
    };
    const statusColor = statusColors[agent.status] || 'var(--muted)';

    body.innerHTML = `
        <div class="agent-details-grid">
            <!-- Basic Info -->
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">Basic Information</h4>
                    <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    <div class="device-card-row">
                        <span class="device-card-label">Agent ID</span>
                        <span class="device-card-value copyable" data-copy="${agent.agent_id}" title="Click to copy">
                            ${agent.agent_id}
                        </span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">Name</span>
                        <span class="device-card-value" id="agent_details_name_display">${agent.name || ''}</span>
                        <span style="margin-left:8px;"><button id="agent_details_edit_name_btn">Edit</button></span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">Hostname</span>
                        <span class="device-card-value copyable" data-copy="${agent.hostname || ''}" title="Click to copy">
                            ${agent.hostname || 'N/A'}
                        </span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">IP Address</span>
                        <span class="device-card-value copyable" data-copy="${agent.ip || ''}" title="Click to copy">
                            ${agent.ip || 'N/A'}
                        </span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">Status</span>
                        <span class="device-card-value" style="color:${statusColor}">
                            ● ${agent.status || 'unknown'}
                        </span>
                    </div>
                </div>
            </div>
            
            <!-- System Info -->
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">System Information</h4>
                <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    <div class="device-card-row">
                        <span class="device-card-label">Platform</span>
                        <span class="device-card-value">${agent.platform || 'Unknown'}</span>
                    </div>
                    ${agent.os_version ? `
                    <div class="device-card-row">
                        <span class="device-card-label">OS Version</span>
                        <span class="device-card-value">${agent.os_version}</span>
                    </div>
                    ` : ''}
                    ${agent.architecture ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Architecture</span>
                        <span class="device-card-value">${agent.architecture}</span>
                    </div>
                    ` : ''}
                    ${agent.num_cpu ? `
                    <div class="device-card-row">
                        <span class="device-card-label">CPUs</span>
                        <span class="device-card-value">${agent.num_cpu}</span>
                    </div>
                    ` : ''}
                    ${agent.total_memory_mb ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Memory</span>
                        <span class="device-card-value">${(agent.total_memory_mb / 1024).toFixed(2)} GB</span>
                    </div>
                    ` : ''}
                </div>
            </div>
            
            <!-- Version Info -->
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">Version Information</h4>
                <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    <div class="device-card-row">
                        <span class="device-card-label">Agent Version</span>
                        <span class="device-card-value">${agent.version || 'N/A'}</span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">Protocol Version</span>
                        <span class="device-card-value">${agent.protocol_version || 'N/A'}</span>
                    </div>
                    ${agent.go_version ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Go Version</span>
                        <span class="device-card-value">${agent.go_version}</span>
                    </div>
                    ` : ''}
                    ${agent.build_type ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Build Type</span>
                        <span class="device-card-value">${agent.build_type}</span>
                    </div>
                    ` : ''}
                    ${agent.git_commit && agent.git_commit !== 'unknown' ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Git Commit</span>
                        <span class="device-card-value copyable" data-copy="${agent.git_commit}" title="Click to copy">
                            ${agent.git_commit.substring(0, 8)}...
                        </span>
                    </div>
                    ` : ''}
                </div>
            </div>
            
            <!-- Activity -->
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">Activity</h4>
                <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    ${registeredDate ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Registered</span>
                        <span class="device-card-value" title="${registeredDate.toLocaleString()}">
                            ${registeredDate.toLocaleDateString()}
                        </span>
                    </div>
                    ` : ''}
                    ${lastSeenDate ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Seen</span>
                        <span class="device-card-value" title="${lastSeenDate.toLocaleString()}">
                            ${lastSeenDate.toLocaleString()}
                        </span>
                    </div>
                    ` : ''}
                    ${lastHeartbeatDate ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Heartbeat</span>
                        <span class="device-card-value" title="${lastHeartbeatDate.toLocaleString()}">
                            ${lastHeartbeatDate.toLocaleString()}
                        </span>
                    </div>
                    ` : ''}
                    <div class="device-card-row">
                        <span class="device-card-label">Uptime</span>
                        <span class="device-card-value">${uptimeText}</span>
                    </div>
                </div>
            </div>
            
            <!-- Data Sync -->
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">Data Synchronization</h4>
                <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    <div class="device-card-row">
                        <span class="device-card-label">Devices</span>
                        <span class="device-card-value">${agent.device_count || 0}</span>
                    </div>
                    ${lastDeviceSyncDate ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Device Sync</span>
                        <span class="device-card-value" title="${lastDeviceSyncDate.toLocaleString()}">
                            ${lastDeviceSyncDate.toLocaleString()}
                        </span>
                    </div>
                    ` : `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Device Sync</span>
                        <span class="device-card-value" style="color:var(--muted);">Never</span>
                    </div>
                    `}
                    ${lastMetricsSyncDate ? `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Metrics Sync</span>
                        <span class="device-card-value" title="${lastMetricsSyncDate.toLocaleString()}">
                            ${lastMetricsSyncDate.toLocaleString()}
                        </span>
                    </div>
                    ` : `
                    <div class="device-card-row">
                        <span class="device-card-label">Last Metrics Sync</span>
                        <span class="device-card-value" style="color:var(--muted);">Never</span>
                    </div>
                    `}
                </div>
            </div>
        </div>
        
        <!-- WS Diagnostics -->
        <div style="margin-top:16px;">
            <div class="panel">
                <h4 style="margin-top:0;color:var(--highlight);font-size:14px;">WebSocket Diagnostics</h4>
                <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
                    <div class="device-card-row">
                        <span class="device-card-label">Ping Failures</span>
                        <span class="device-card-value">${agent.ws_ping_failures || 0}</span>
                    </div>
                    <div class="device-card-row">
                        <span class="device-card-label">Disconnect Events</span>
                        <span class="device-card-value">${agent.ws_disconnect_events || 0}</span>
                    </div>
                    <div style="color:var(--muted);font-size:12px;">These counts are diagnostics from the server's WebSocket subsystem. They help indicate flaky connections or network issues.</div>
                </div>
            </div>
        </div>
        <!-- Action Buttons -->
        <div style="margin-top: 20px; display: flex; gap: 10px; justify-content: flex-end; flex-wrap: wrap;">
            <button id="agent_check_update_btn" data-agent-id="${agent.agent_id}" ${commandDisabledAttr}>
                Check for Update
            </button>
            <button id="agent_force_update_btn" data-agent-id="${agent.agent_id}" ${commandDisabledAttr}>
                Force Reinstall
            </button>
            <button data-action="open-agent" data-agent-id="${agent.agent_id}" ${commandDisabledAttr}>
                Open Agent UI
            </button>
        </div>
        <div class="agent-update-feedback">
            <div id="agent_update_hint" class="agent-update-hint${commandEnabled ? '' : ' error'}">${escapeHtml(commandHint)}</div>
            <div id="agent_update_status" class="agent-update-status" role="status" aria-live="polite"></div>
        </div>
    `;
    // Attach inline editor handlers now that DOM nodes are present
    try { _attachAgentDetailsNameEditor(agent); } catch (e) { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('attach editor failed', e); }
    // Attach check for update handler
    try { _attachAgentUpdateHandler(agent); } catch (e) { window.__pm_shared && window.__pm_shared.warn && window.__pm_shared.warn('attach update handler failed', e); }
}

// After rendering the agent details modal we attach a small inline handler
// to allow editing the agent's user-friendly name. This toggles an input
// in the modal and sends a POST to update the name on the server, then
// updates the UI card in-place.
function _attachAgentDetailsNameEditor(agent) {
    try {
        const editBtn = document.getElementById('agent_details_edit_name_btn');
        if (!editBtn) return;
        editBtn.addEventListener('click', () => {
            const displayEl = document.getElementById('agent_details_name_display');
            if (!displayEl) return;
            const current = displayEl.textContent || '';
            // Create edit UI with proper mobile-friendly input styling
            // Use flex-start to prevent mobile cursor position bugs with flex-end
            displayEl.innerHTML = `<span style="display:flex;align-items:center;gap:6px;justify-content:flex-start;width:100%;">
                <input id="agent_details_name_input" value="${(agent.name || '').replace(/"/g, '&quot;')}" 
                    style="flex:1;min-width:120px;max-width:200px;text-align:left;direction:ltr;font-size:14px;padding:4px 8px;" 
                    autocomplete="off" data-1p-ignore data-lpignore="true" />
                <button id="agent_details_save_name">Save</button>
                <button id="agent_details_cancel_name">Cancel</button>
            </span>`;

            // Focus the input and select all text so user can immediately type to replace
            const inputEl = document.getElementById('agent_details_name_input');
            if (inputEl) {
                inputEl.focus();
                inputEl.select();
            }

            const saveBtn = document.getElementById('agent_details_save_name');
            const cancelBtn = document.getElementById('agent_details_cancel_name');
            if (cancelBtn) cancelBtn.addEventListener('click', () => { displayEl.textContent = current; });

            if (saveBtn) saveBtn.addEventListener('click', async () => {
                const input = document.getElementById('agent_details_name_input');
                if (!input) return;
                const newName = input.value.trim();
                try {
                    const res = await fetch(`/api/v1/agents/${encodeURIComponent(agent.agent_id)}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ name: newName })
                    });
                    if (!res.ok) throw new Error('HTTP ' + res.status);
                    const updated = await res.json();
                    // Update modal display
                    const title = document.getElementById('agent_details_title');
                    const nameDisplay = document.getElementById('agent_details_name_display');
                    if (nameDisplay) nameDisplay.textContent = updated.name || '';
                    if (title) title.textContent = `Agent: ${updated.name || updated.hostname || updated.agent_id}`;
                    // Update agent card in list
                    try { upsertAgentRecord(updated); } catch (e) { }
                    window.__pm_shared.showToast('Agent name updated', 'success');
                } catch (err) {
                    window.__pm_shared.showToast('Failed to update agent name', 'error');
                    // restore display
                    displayEl.textContent = current;
                }
            });
        });
    } catch (e) {
        window.__pm_shared.warn('Failed to attach agent details name editor', e);
    }
}

function _attachAgentUpdateHandler(agent) {
    try {
        const canSendCommands = (agent.connection_type || '').toLowerCase() === 'ws';
        const statusEl = document.getElementById('agent_update_status');
        const requireWsMessage = 'Requires active WebSocket connection';

        const setStatus = (message, tone = 'info') => {
            if (!statusEl) return;
            statusEl.textContent = message || '';
            statusEl.classList.remove('status-info', 'status-success', 'status-error');
            if (!message) {
                return;
            }
            const cls = tone === 'success' ? 'status-success' : tone === 'error' ? 'status-error' : 'status-info';
            statusEl.classList.add(cls);
        };

        const checkBtn = document.getElementById('agent_check_update_btn');
        if (checkBtn) {
            const resetCheckButton = () => {
                checkBtn.disabled = !canSendCommands;
                checkBtn.textContent = 'Check for Update';
                if (!canSendCommands) {
                    checkBtn.title = requireWsMessage;
                } else {
                    checkBtn.removeAttribute('title');
                }
            };
            resetCheckButton();
            checkBtn.addEventListener('click', async () => {
                if (!canSendCommands) {
                    setStatus('Connect via WebSocket to send update commands.', 'error');
                    return;
                }
                checkBtn.disabled = true;
                checkBtn.textContent = 'Checking...';
                setStatus('Contacting agent…', 'info');
                try {
                    const res = await fetch(`/api/v1/agents/command/${encodeURIComponent(agent.agent_id)}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ command: 'check_update' })
                    });
                    if (!res.ok) {
                        const txt = await res.text();
                        throw new Error(txt || 'Request failed');
                    }
                    const data = await res.json();
                    if (data.success) {
                        const summary = data.message || 'Update check triggered';
                        window.__pm_shared.showToast(summary, 'success');
                        setStatus(`${summary} at ${new Date().toLocaleTimeString()}`, 'success');
                    } else {
                        const msg = data.error || 'Failed to trigger update check';
                        window.__pm_shared.showToast(msg, 'error');
                        setStatus(msg, 'error');
                    }
                } catch (err) {
                    window.__pm_shared.showToast('Failed to send command: ' + (err.message || err), 'error');
                    setStatus('Failed to send command: ' + (err.message || err), 'error');
                } finally {
                    resetCheckButton();
                }
            });
        }

        const forceBtn = document.getElementById('agent_force_update_btn');
        if (forceBtn) {
            const resetForceButton = (label) => {
                forceBtn.disabled = !canSendCommands;
                forceBtn.textContent = label || 'Force Reinstall';
                if (!canSendCommands) {
                    forceBtn.title = requireWsMessage;
                } else {
                    forceBtn.removeAttribute('title');
                }
            };
            resetForceButton();
            forceBtn.addEventListener('click', async () => {
                if (!window.confirm('Force reinstall this agent? The service will restart and may temporarily disconnect.')) {
                    return;
                }
                if (!canSendCommands) {
                    setStatus('Connect via WebSocket to send update commands.', 'error');
                    return;
                }
                forceBtn.disabled = true;
                const previousLabel = forceBtn.textContent;
                forceBtn.textContent = 'Forcing...';
                setStatus('Contacting agent…', 'info');
                try {
                    const res = await fetch(`/api/v1/agents/command/${encodeURIComponent(agent.agent_id)}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            command: 'force_update',
                            data: { reason: 'server_ui_force_reinstall' }
                        })
                    });
                    if (!res.ok) {
                        const txt = await res.text();
                        throw new Error(txt || 'Request failed');
                    }
                    const data = await res.json();
                    if (data.success) {
                        const summary = data.message || 'Forced reinstall triggered';
                        window.__pm_shared.showToast(summary, 'success');
                        setStatus(`${summary} at ${new Date().toLocaleTimeString()}`, 'success');
                    } else {
                        const msg = data.error || 'Failed to force reinstall';
                        window.__pm_shared.showToast(msg, 'error');
                        setStatus(msg, 'error');
                    }
                } catch (err) {
                    window.__pm_shared.showToast('Failed to send force reinstall: ' + (err.message || err), 'error');
                    setStatus('Failed to send force reinstall: ' + (err.message || err), 'error');
                } finally {
                    resetForceButton(previousLabel);
                }
            });
        }
    } catch (e) {
        window.__pm_shared.warn('Failed to attach agent update handler', e);
    }
}

// ====== Update Agent (from agent list/cards) ======

// Handle update progress events from SSE
function handleAgentUpdateProgress(data) {
    const agentId = data.agent_id;
    if (!agentId) return;

    // Normalize status values emitted by agent auto-update pipeline
    const rawStatus = (data.status || 'unknown').toLowerCase();
    const status = (() => {
        switch (rawStatus) {
            case 'pending':
                return 'checking';
            case 'staging':
            case 'applying':
                return 'ready'; // installing phase
            case 'succeeded':
                return 'complete';
            case 'rolled_back':
                return 'failed';
            case 'skipped':
                return 'skipped';
            default:
                return rawStatus;
        }
    })();

    const progress = data.progress || 0;
    const message = data.message || '';
    const targetVersion = data.target_version || '';
    const errorMsg = data.error || '';

    // Update state tracking - preserve previousVersion and _shownToasts if already set
    const existingState = agentsVM.updateState[agentId] || {};
    const agent = agentsVM.items.find(a => a.agent_id === agentId);
    const previousVersion = existingState.previousVersion || agent?.version || '';
    const shownToasts = existingState._shownToasts || {};

    agentsVM.updateState[agentId] = {
        status,
        progress,
        message,
        targetVersion,
        previousVersion,
        error: errorMsg,
        timestamp: Date.now(),
        _shownToasts: shownToasts  // Preserve toast tracking across updates
    };

    // Start animation loop if an update is active
    if (status === 'checking' || status === 'downloading' || status === 'ready' ||
        status === 'restarting' || status === 'verifying') {
        startUpdateProgressAnimation();
    }

    // Show toast notifications for key events
    const agentName = agent?.name || agent?.hostname || agentId;

    switch (status) {
        case 'checking':
            // No toast for checking, just UI update
            break;
        case 'downloading':
            // Only show "Downloading..." toast once per update cycle
            if (!shownToasts.downloading) {
                window.__pm_shared.showToast(`${agentName}: Downloading update...`, 'info');
                shownToasts.downloading = true;
            }
            break;
        case 'ready':
            if (!shownToasts.ready) {
                window.__pm_shared.showToast(`${agentName}: Update downloaded, preparing to install...`, 'info');
                shownToasts.ready = true;
            }
            break;
        case 'restarting':
            if (!shownToasts.restarting) {
                window.__pm_shared.showToast(`${agentName}: Restarting to apply update...`, 'info');
                shownToasts.restarting = true;
            }
            // Store previous version for comparison when agent reconnects
            if (agent?.version && !agentsVM.updateState[agentId].previousVersion) {
                agentsVM.updateState[agentId].previousVersion = agent.version;
            }
            // Fallback: if we never hear back after restart, clear the state and refresh
            if (!shownToasts.restartTimeout) {
                shownToasts.restartTimeout = true;
                setTimeout(() => {
                    const st = agentsVM.updateState[agentId];
                    if (st && st.status === 'restarting') {
                        window.__pm_shared.showToast(`${agentName}: Update timed out waiting for reconnect`, 'warning');
                        delete agentsVM.updateState[agentId];
                        refreshAgentVersionCell(agentId);
                        // Fetch just this agent's data instead of all agents
                        fetchSingleAgent(agentId);
                    }
                }, 30000); // Increased to 30s to allow for slower restarts
            }
            break;
        case 'complete':
            window.__pm_shared.showToast(`${agentName}: Update complete!`, 'success');
            // Clear state after a delay to let UI update
            setTimeout(() => {
                delete agentsVM.updateState[agentId];
                if (agentsVM.updateAnimations) delete agentsVM.updateAnimations[agentId];
                refreshAgentVersionCell(agentId);
            }, 3000);
            // Reload agents to get new version
            setTimeout(() => loadAgents(), 2000);
            break;
        case 'failed':
            window.__pm_shared.showToast(`${agentName}: Update failed - ${errorMsg || message}`, 'error');
            // Clear state after showing error
            setTimeout(() => {
                delete agentsVM.updateState[agentId];
                if (agentsVM.updateAnimations) delete agentsVM.updateAnimations[agentId];
                refreshAgentVersionCell(agentId);
            }, 5000);
            break;
        case 'idle':
            // Agent returned to idle, clear any pending state
            delete agentsVM.updateState[agentId];
            if (agentsVM.updateAnimations) delete agentsVM.updateAnimations[agentId];
            break;
    }

    // Refresh the version cell for this agent
    refreshAgentVersionCell(agentId);
}

// Refresh the version cell display for a specific agent
function refreshAgentVersionCell(agentId) {
    const agent = agentsVM.items.find(a => a.agent_id === agentId);
    if (!agent) return;

    // Update in table view
    const tableRow = document.querySelector(`tr[data-agent-id="${agentId}"]`);
    if (tableRow) {
        // First try to find by data-column-id (table customizer)
        let versionCell = tableRow.querySelector('td[data-column-id="version"]');
        if (!versionCell) {
            // Fallback: try to find by position (legacy non-customizer render)
            // In the fallback renderer, version is the 6th column (index 5)
            const cells = tableRow.querySelectorAll('td');
            if (cells.length >= 6) {
                versionCell = cells[5];
            }
        }
        if (versionCell) {
            versionCell.innerHTML = renderAgentVersionCell(agent, true);
        }
    }
    // Update in card view
    const card = document.querySelector(`.device-card[data-agent-id="${agentId}"]`);
    if (card) {
        const versionSpan = card.querySelector('.agent-version-cell');
        if (versionSpan) {
            versionSpan.innerHTML = renderAgentVersionCell(agent, false);
        }
    }
}

// Animation loop for smooth progress updates
let updateProgressAnimationFrame = null;
function startUpdateProgressAnimation() {
    if (updateProgressAnimationFrame) return; // Already running

    function animate() {
        // Check if any updates are in progress
        const activeUpdates = Object.keys(agentsVM.updateState || {}).filter(id => {
            const state = agentsVM.updateState[id];
            const status = state?.status;
            return status === 'checking' || status === 'downloading' || status === 'ready' ||
                status === 'restarting' || status === 'verifying' ||
                status === 'pending' || status === 'staging' || status === 'applying';
        });

        if (activeUpdates.length === 0) {
            // No active updates, stop animation
            updateProgressAnimationFrame = null;
            // Clean up animation state
            if (agentsVM.updateAnimations) {
                agentsVM.updateAnimations = {};
            }
            return;
        }

        // Update progress for each active update
        activeUpdates.forEach(agentId => {
            // Update the progress button directly without full re-render
            const progressBtn = document.querySelector(`.update-btn.progress-btn[data-agent-id="${agentId}"]`);
            if (progressBtn) {
                const updateState = agentsVM.updateState[agentId];
                const rawStatus = updateState?.status || '';
                const status = (() => {
                    switch (rawStatus) {
                        case 'pending': return 'checking';
                        case 'staging':
                        case 'applying': return 'ready';
                        default: return rawStatus;
                    }
                })();
                const smoothProgress = getSmoothedUpdateProgress(agentId, status, updateState);
                progressBtn.style.setProperty('--progress', `${smoothProgress}%`);
                progressBtn.textContent = `${Math.round(smoothProgress)}%`;
            }
        });

        // Continue animation
        updateProgressAnimationFrame = requestAnimationFrame(animate);
    }

    updateProgressAnimationFrame = requestAnimationFrame(animate);
}

// Stop animation when no updates are active
function stopUpdateProgressAnimation() {
    if (updateProgressAnimationFrame) {
        cancelAnimationFrame(updateProgressAnimationFrame);
        updateProgressAnimationFrame = null;
    }
}

// Cancel an in-progress update
async function cancelAgentUpdate(agentId) {
    if (!agentId) return;

    const state = agentsVM.updateState[agentId];
    if (!state || state.status === 'restarting') {
        window.__pm_shared.showToast('Cannot cancel update at this stage', 'warning');
        return;
    }

    try {
        const response = await fetch(`/api/v1/agents/command/${agentId}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'cancel_update' })
        });

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(`HTTP ${response.status}: ${errorText}`);
        }

        window.__pm_shared.showToast('Cancel request sent', 'info');
    } catch (error) {
        window.__pm_shared.error('Failed to cancel update:', error);
        window.__pm_shared.showToast('Failed to cancel update: ' + (error.message || error), 'error');
    }
}

async function updateAgent(agentId) {
    if (!agentId) {
        window.__pm_shared.showToast('No agent ID provided', 'error');
        return;
    }

    const channel = await showAgentUpdateChannelConfirm();
    if (channel === null) return;

    // Set initial updating state
    agentsVM.updateState[agentId] = {
        status: 'checking',
        progress: 0,
        message: 'Sending update command...',
        targetVersion: channel ? '' : (agentsVM.latestVersion || ''),
        error: '',
        timestamp: Date.now()
    };
    refreshAgentVersionCell(agentId);
    // Start smooth animation loop
    startUpdateProgressAnimation();

    try {
        const response = await fetch(`/api/v1/agents/command/${agentId}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(channel === 'fleet' ? {
                command: 'force_update',
                data: { reason: 'server_ui_force_fleet_channel' }
            } : channel ? {
                command: 'install_channel',
                data: { channel, reason: 'server_ui_channel_install' }
            } : { command: 'check_update' })
        });

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(`HTTP ${response.status}: ${errorText}`);
        }

        const result = await response.json();
        if (result.success) {
            // Update state to show we're waiting for agent response
            agentsVM.updateState[agentId] = {
                ...agentsVM.updateState[agentId],
                message: 'Waiting for agent...',
            };
            refreshAgentVersionCell(agentId);
        } else {
            // Command failed
            agentsVM.updateState[agentId] = {
                ...agentsVM.updateState[agentId],
                status: 'failed',
                error: result.message || 'Unknown error',
            };
            refreshAgentVersionCell(agentId);
            window.__pm_shared.showToast('Update command failed: ' + (result.message || 'unknown'), 'warning');
            setTimeout(() => {
                delete agentsVM.updateState[agentId];
                refreshAgentVersionCell(agentId);
            }, 5000);
        }
    } catch (error) {
        window.__pm_shared.error('Failed to send update command:', error);
        agentsVM.updateState[agentId] = {
            ...agentsVM.updateState[agentId],
            status: 'failed',
            error: error.message || String(error),
        };
        refreshAgentVersionCell(agentId);
        window.__pm_shared.showToast('Failed to send update command: ' + (error.message || error), 'error');
        setTimeout(() => {
            delete agentsVM.updateState[agentId];
            refreshAgentVersionCell(agentId);
        }, 5000);
    }
}

function showAgentUpdateChannelConfirm() {
    return new Promise(resolve => {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.style.display = 'flex';
        overlay.innerHTML = `
            <div class="modal-content" style="max-width:540px;" role="dialog" aria-modal="true" aria-labelledby="agent_update_channel_title">
                <div class="modal-header"><h3 id="agent_update_channel_title">Update Agent</h3><button class="modal-close-x" title="Close">&times;</button></div>
                <div class="modal-body">
                    <label class="field"><span>Release channel</span><select id="agent_update_channel">
                        <option value="">Configured channel (respect update policy)</option>
                        <option value="fleet">Force install Fleet channel (supports older Agents)</option>
                        <option value="stable">Stable</option>
                        <option value="beta">Beta / Release Candidate</option>
                        <option value="dev">Development</option>
                    </select></label>
                    <p class="muted-text">An explicit channel installs its latest cached release for this Agent platform, even if this means reinstalling or downgrading. Maintenance windows and version pins are bypassed; scheduled channel settings stay unchanged.</p>
                    <p class="muted-text">Set persistent Agent Update Channel under Fleet Settings → Features. To bootstrap older Agents (including 0.31.1), save dev there, then choose Force install Fleet channel. Beta/dev requires prerelease intake and cached artifacts. Explicit Stable/Beta/Dev choices still require an updated Agent.</p>
                </div>
                <div class="modal-footer"><button class="modal-button modal-button-secondary" data-action="cancel">Cancel</button><button class="modal-button modal-button-danger" data-action="confirm">Update Agent</button></div>
            </div>`;
        document.body.appendChild(overlay);
        const finish = value => { overlay.remove(); resolve(value); };
        overlay.querySelector('[data-action="confirm"]').addEventListener('click', () => finish(overlay.querySelector('select').value));
        overlay.querySelector('[data-action="cancel"]').addEventListener('click', () => finish(null));
        overlay.querySelector('.modal-close-x').addEventListener('click', () => finish(null));
        overlay.addEventListener('click', event => { if (event.target === overlay) finish(null); });
    });
}

// Expose server-specific agent UI helpers to the shared namespace so the
// delegated card handlers (loaded earlier) call the rich renderer instead
// of the generic fallback in `common/web/shared.js` which shows raw JSON.
try {
    window.__pm_shared = window.__pm_shared || {};
    window.__pm_shared.viewAgentDetails = viewAgentDetails;
    window.__pm_shared.renderAgentDetailsModal = renderAgentDetailsModal;
    // Also expose delete/open helpers if present so shared callers use server implementations
    window.__pm_shared.deleteAgent = window.__pm_shared.deleteAgent || deleteAgent;
    window.__pm_shared.openAgentUI = window.__pm_shared.openAgentUI || openAgentUI;
    window.__pm_shared.updateAgent = window.__pm_shared.updateAgent || updateAgent;
    window.__pm_shared.cancelAgentUpdate = window.__pm_shared.cancelAgentUpdate || cancelAgentUpdate;
    // Always override device helpers so cards and shared UI use the server proxy endpoint
    window.__pm_shared.openDeviceUI = openDeviceUI;
    window.__pm_shared.openDeviceMetrics = window.__pm_shared.openDeviceMetrics || openDeviceMetrics;
    window.__pm_shared.openFleetSettingsForTenant = openFleetSettingsForTenant;
    window.__pm_shared.openFleetSettingsForAgent = openFleetSettingsForAgent;
} catch (e) { console.warn('Failed to expose server UI helpers to shared namespace', e); }

// ====== Delete Agent ======
async function deleteAgent(agentId, displayName) {
    window.__pm_shared.log('deleteAgent called:', agentId, displayName);

    const confirmed = await window.__pm_shared.showConfirm(
        `Are you sure you want to delete agent "${displayName}"?\n\nThis will permanently remove the agent and all its associated devices and metrics. This action cannot be undone.`,
        'Delete Agent',
        true
    );

    window.__pm_shared.log('User confirmed:', confirmed);

    if (!confirmed) {
        return;
    }

    try {
        window.__pm_shared.log('Sending DELETE request to:', `/api/v1/agents/${agentId}`);
        const response = await fetch(`/api/v1/agents/${agentId}`, {
            method: 'DELETE'
        });

        window.__pm_shared.log('Response status:', response.status, response.statusText);

        if (!response.ok) {
            const errorText = await response.text();
            window.__pm_shared.error('Delete failed:', errorText);
            throw new Error(`HTTP ${response.status}: ${errorText}`);
        }

        const result = await response.json();
        window.__pm_shared.log('Delete successful:', result);

        window.__pm_shared.showToast(`Agent "${displayName}" deleted successfully`, 'success');

        // Remove agent card with animation
        const card = document.querySelector(`[data-agent-id="${agentId}"]`);
        if (card) {
            card.classList.add('removing');
            setTimeout(() => {
                // Reload agents list
                loadAgents();
            }, 400); // Match animation duration
        } else {
            // Card not found, just reload
            loadAgents();
        }
    } catch (error) {
        window.__pm_shared.error('Failed to delete agent:', error);
        window.__pm_shared.showToast(`Failed to delete agent: ${error.message}`, 'error');
    }
}

// ====== Restart Agent ======
async function restartAgent(agentId, displayName) {
    window.__pm_shared.log('restartAgent called:', agentId, displayName);

    const confirmed = await window.__pm_shared.showConfirm(
        `Are you sure you want to restart agent "${displayName || agentId}"?\n\nThe agent will temporarily disconnect and should reconnect within a few seconds.`,
        'Restart Agent',
        { confirmText: 'Restart', confirmClass: 'btn-warning' }
    );

    if (!confirmed) {
        return;
    }

    try {
        const response = await fetch(`/api/v1/agents/command/${agentId}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'restart' })
        });

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(`HTTP ${response.status}: ${errorText}`);
        }

        const result = await response.json();
        if (result.success) {
            window.__pm_shared.showToast('Restart command sent. Agent will reconnect shortly.', 'success');
        } else {
            window.__pm_shared.showToast('Failed to send restart command: ' + (result.message || 'unknown'), 'warning');
        }
    } catch (error) {
        window.__pm_shared.error('Failed to restart agent:', error);
        window.__pm_shared.showToast(`Failed to restart agent: ${error.message}`, 'error');
    }
}

// Expose restartAgent to shared namespace
try {
    window.__pm_shared.restartAgent = restartAgent;
} catch (e) { console.warn('Failed to expose restartAgent to shared namespace', e); }

// ====== Delete Device ======
async function requestDeviceDelete(serial, agentId, options) {
    const response = await fetch('/api/v1/devices/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            serial,
            agent_id: agentId,
            delete_metrics: options.deleteMetrics,
            delete_from_agent: options.deleteFromAgent
        })
    });
    if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${(await response.text()).trim()}`);
    }
    return response.json();
}

/**
 * Delete a device from the server (and optionally from the agent).
 * Shows a confirmation modal with options to delete metrics history
 * and to also delete from the agent's database.
 */
async function deleteDevice(serial, agentId) {
    window.__pm_shared.log('deleteDevice called:', serial, agentId);

    // Create custom confirmation modal with checkboxes
    const result = await showDeleteDeviceConfirm(serial, agentId);

    if (!result.confirmed) {
        window.__pm_shared.log('Delete device cancelled');
        return;
    }

    try {
        window.__pm_shared.log('Sending delete request:', {
            serial,
            agent_id: agentId,
            delete_metrics: result.deleteMetrics,
            delete_from_agent: result.deleteFromAgent
        });

        const responseData = await requestDeviceDelete(serial, agentId, result);
        window.__pm_shared.log('Delete successful:', responseData);

        let message = `Device "${serial}" deleted successfully`;
        if (responseData.deleted_from_agent) {
            message += ' (also removed from agent)';
        }
        if (responseData.deleted_metrics) {
            message += ' with metrics history';
        }
        window.__pm_shared.showToast(message, 'success');

        // Remove device from UI with animation
        const card = document.querySelector(`[data-serial="${serial}"]`);
        const row = document.querySelector(`tr[data-serial="${serial}"]`);
        const target = card || row;

        if (target) {
            target.classList.add('removing');
            setTimeout(() => {
                // Reload devices list
                loadDevices();
            }, 400);
        } else {
            // Element not found, just reload
            loadDevices();
        }

        // Close any open device modal
        const modal = document.getElementById('printer_details_modal');
        if (modal && modal.style.display !== 'none') {
            modal.style.display = 'none';
        }

    } catch (error) {
        window.__pm_shared.error('Failed to delete device:', error);
        window.__pm_shared.showToast(`Failed to delete device: ${error.message}`, 'error');
    }
}

/**
 * Show custom delete device confirmation modal with checkboxes
 * @param {string} serial - Device serial number
 * @param {string} agentId - ID of the agent that owns the device
 * @param {number} count - Number of devices for a bulk confirmation
 * @returns {Promise<{confirmed: boolean, deleteMetrics: boolean, deleteFromAgent: boolean}>}
 */
function showDeleteDeviceConfirm(serial, agentId, count = 1) {
    return new Promise((resolve) => {
        // Helper to escape HTML
        const safeEscape = (s) => (typeof escapeHtml === 'function' ? escapeHtml(s) : String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;"));

        const wrapper = document.createElement('div');
        wrapper.className = 'modal-overlay';
        wrapper.style.display = 'flex';
        const uid = 'delete_device_confirm_' + Date.now();
        wrapper.id = uid;

        const hasAgent = agentId && agentId !== '';
        const title = count > 1 ? `Delete ${count} Devices` : 'Delete Device';
        const subject = count > 1 ? `${count} selected devices` : `device <strong>${safeEscape(serial)}</strong>`;
        const deleteMetricsId = `${uid}_delete_metrics`;
        const deleteFromAgentId = `${uid}_delete_from_agent`;

        wrapper.innerHTML = `
            <div class="modal-content" style="max-width:520px;">
                <div class="modal-header">
                    <h3 class="modal-title">${title}</h3>
                    <button class="modal-close-x" title="Close">&times;</button>
                </div>
                <div class="modal-body">
                    <p style="margin-bottom:16px;">Are you sure you want to delete ${subject}?</p>
                    <p style="margin-bottom:16px;color:var(--text-muted);font-size:13px;">This will permanently remove ${count > 1 ? 'the devices' : 'the device'} from the server database.</p>
                    
                    <div style="background:var(--bg-tertiary);border-radius:8px;padding:12px;margin-bottom:12px;">
                        <div style="display:flex;align-items:flex-start;gap:10px;cursor:pointer;margin-bottom:10px;width:100%;">
                            <input type="checkbox" id="${deleteMetricsId}" style="margin-top:3px;cursor:pointer;flex-shrink:0;">
                            <label for="${deleteMetricsId}" style="cursor:pointer;">
                                <span style="font-weight:500;">Also delete metrics history</span>
                                <div style="font-size:12px;color:var(--text-muted);margin-top:2px;">
                                    Remove all historical page counts, toner levels, and other metrics data for ${count > 1 ? 'these devices' : 'this device'}
                                </div>
                            </label>
                        </div>
                        
                        ${hasAgent ? `
                        <div style="display:flex;align-items:flex-start;gap:10px;cursor:pointer;width:100%;">
                            <input type="checkbox" id="${deleteFromAgentId}" style="margin-top:3px;cursor:pointer;flex-shrink:0;">
                            <label for="${deleteFromAgentId}" style="cursor:pointer;">
                                <span style="font-weight:500;">Also delete from agent</span>
                                <div style="font-size:12px;color:var(--text-muted);margin-top:2px;">
                                    Remove ${count > 1 ? 'the devices from their agents’ local databases' : "the device from the agent's local database"} as well. Devices may be re-discovered on the next scan.
                                </div>
                            </label>
                        </div>
                        ` : `
                        <div style="font-size:12px;color:var(--text-muted);font-style:italic;">
                            ${count > 1 ? 'These devices have no associated agents, so they' : 'This device has no associated agent, so it'} will only be deleted from the server.
                        </div>
                        `}
                    </div>
                </div>
                <div class="modal-footer">
                    <button class="modal-button modal-button-secondary" data-action="cancel">Cancel</button>
                    <button class="modal-button modal-button-danger" data-action="confirm">${title}</button>
                </div>
            </div>
        `;
        document.body.appendChild(wrapper);

        const btnConfirm = wrapper.querySelector('[data-action="confirm"]');
        const btnCancel = wrapper.querySelector('[data-action="cancel"]');
        const closeX = wrapper.querySelector('.modal-close-x');
        const chkMetrics = wrapper.querySelector(`#${deleteMetricsId}`);
        const chkAgent = wrapper.querySelector(`#${deleteFromAgentId}`);

        function cleanup() {
            try { btnConfirm && btnConfirm.removeEventListener('click', onConfirm); } catch (e) { }
            try { btnCancel && btnCancel.removeEventListener('click', onCancel); } catch (e) { }
            try { closeX && closeX.removeEventListener('click', onCancel); } catch (e) { }
            try { wrapper.removeEventListener('click', onBackdrop); } catch (e) { }
            // Hide immediately so Playwright sees it as gone, then remove on next frame
            // to let WebKit's backdrop-filter compositor layer settle before the next
            // action (synchronous removal causes brief layout instability in WebKit).
            wrapper.style.visibility = 'hidden';
            wrapper.style.pointerEvents = 'none';
            requestAnimationFrame(() => {
                try { wrapper.parentNode && wrapper.parentNode.removeChild(wrapper); } catch (e) { }
            });
        }

        function onConfirm() {
            const deleteMetrics = chkMetrics ? chkMetrics.checked : false;
            const deleteFromAgent = chkAgent ? chkAgent.checked : false;
            cleanup();
            resolve({ confirmed: true, deleteMetrics, deleteFromAgent });
        }

        function onCancel() {
            cleanup();
            resolve({ confirmed: false, deleteMetrics: false, deleteFromAgent: false });
        }

        function onBackdrop(e) {
            if (e.target === wrapper) onCancel();
        }

        btnConfirm && btnConfirm.addEventListener('click', onConfirm);
        btnCancel && btnCancel.addEventListener('click', onCancel);
        closeX && closeX.addEventListener('click', onCancel);
        wrapper.addEventListener('click', onBackdrop);
    });
}

// Expose deleteDevice to shared namespace
try {
    window.__pm_shared.deleteDevice = deleteDevice;
    window.__pm_shared.showDeleteDeviceConfirm = showDeleteDeviceConfirm;
    window.__pm_shared.requestDeviceDelete = requestDeviceDelete;
    window.__pm_shared.fetchDevices = () => loadDevices();
} catch (e) { console.warn('Failed to expose deleteDevice to shared namespace', e); }

// ====== Devices Management ======
function initDevicesUI() {
    if (devicesVM.uiInitialized) {
        return;
    }
    devicesVM.uiInitialized = true;

    // Initialize Table Customizer
    initDevicesTableCustomizer();

    initFilterDrawer('devices_sidebar', 'devices_filters_open', 'devices_sidebar_toggle');

    const searchInput = document.getElementById('devices_search');
    if (searchInput) {
        searchInput.value = devicesVM.filters.query;
        const handleSearch = debounce((event) => {
            devicesVM.filters.query = (event.target.value || '').trim();
            applyDeviceFilters();
        }, 200);
        searchInput.addEventListener('input', handleSearch);
    }

    const agentSelect = document.getElementById('devices_agent_filter');
    if (agentSelect) {
        agentSelect.value = devicesVM.filters.agentId;
        agentSelect.addEventListener('change', (event) => {
            devicesVM.filters.agentId = event.target.value || '';
            applyDeviceFilters();
        });
    }

    const tenantSelect = document.getElementById('devices_tenant_filter');
    if (tenantSelect) {
        tenantSelect.value = devicesVM.filters.tenantId;
        tenantSelect.addEventListener('change', (event) => {
            devicesVM.filters.tenantId = event.target.value || '';
            applyDeviceFilters();
        });
    }

    const manufacturerSelect = document.getElementById('devices_manufacturer_filter');
    if (manufacturerSelect) {
        manufacturerSelect.addEventListener('change', (event) => {
            devicesVM.filters.manufacturer = event.target.value || '';
            applyDeviceFilters();
        });
    }

    const sortSelect = document.getElementById('devices_sort_select');
    if (sortSelect) {
        sortSelect.value = devicesVM.filters.sortKey;
        sortSelect.addEventListener('change', (event) => {
            setDeviceSort(event.target.value, devicesVM.filters.sortDir);
        });
    }

    const sortDirBtn = document.getElementById('devices_sort_dir_btn');
    if (sortDirBtn) {
        sortDirBtn.addEventListener('click', () => {
            const nextDir = devicesVM.filters.sortDir === 'asc' ? 'desc' : 'asc';
            setDeviceSort(devicesVM.filters.sortKey, nextDir);
        });
    }

    const viewToggle = document.getElementById('devices_view_toggle');
    if (viewToggle) {
        viewToggle.addEventListener('click', (event) => {
            const btn = event.target.closest('[data-view]');
            if (!btn) return;
            setDevicesView(btn.getAttribute('data-view'));
        });
    }

    const overview = document.getElementById('devices_overview_metrics');
    if (overview) {
        overview.addEventListener('click', (event) => {
            const card = event.target.closest('button[data-summary-action]');
            if (!card) return;
            switch (card.dataset.summaryAction) {
                case 'all-devices':
                    resetDeviceFilters();
                    break;
                case 'agents': {
                    const trigger = document.getElementById('devices_filters_open');
                    if (trigger?.getAttribute('aria-expanded') !== 'true') trigger?.click();
                    requestAnimationFrame(() => document.getElementById('devices_agent_filter')?.focus());
                    break;
                }
                case 'pages': {
                    const direction = devicesVM.filters.sortKey === 'page_count' && devicesVM.filters.sortDir === 'desc' ? 'asc' : 'desc';
                    setDeviceSort('page_count', direction);
                    break;
                }
            }
        });
    }

    const statusFilter = document.getElementById('devices_status_filter');
    if (statusFilter) {
        statusFilter.addEventListener('click', (event) => {
            const btn = event.target.closest('[data-status]');
            if (!btn) return;
            toggleStatusFilter(btn.getAttribute('data-status'));
        });
    }

    const consumableFilter = document.getElementById('devices_consumable_filter');
    if (consumableFilter) {
        consumableFilter.addEventListener('click', (event) => {
            const btn = event.target.closest('[data-band]');
            if (!btn) return;
            toggleConsumableFilter(btn.getAttribute('data-band'));
        });
    }

    const resetBtn = document.getElementById('devices_reset_filters');
    if (resetBtn) {
        resetBtn.addEventListener('click', resetDeviceFilters);
    }

    const chips = document.getElementById('devices_active_filters');
    if (chips && !chips.dataset.bound) {
        chips.dataset.bound = 'true';
        chips.addEventListener('click', (event) => {
            const btn = event.target.closest('button[data-filter]');
            if (!btn) return;
            handleFilterChipRemove(btn.getAttribute('data-filter'));
        });
    }

    const table = document.getElementById('devices_table');
    if (table) {
        const head = table.querySelector('thead');
        if (head && !head.dataset.bound) {
            head.dataset.bound = 'true';
            // Bind customizer header events (includes sorting and resizing)
            if (devicesVM.tableCustomizer) {
                devicesVM.tableCustomizer.bindHeaderEvents(head);
            }
        }
        // Add click handler for clickable rows (file-explorer style selection)
        const tbody = table.querySelector('tbody');
        if (tbody && !tbody.dataset.rowClickBound) {
            tbody.dataset.rowClickBound = 'true';
            tbody.addEventListener('click', (event) => {
                // Don't trigger row click if clicking on a button or actions column
                if (event.target.closest('button') || event.target.closest('.table-actions') || event.target.closest('.actions-col')) {
                    return;
                }
                const row = event.target.closest('tr.device-row-clickable');
                if (!row) return;
                const serial = row.getAttribute('data-serial');
                const ip = row.getAttribute('data-ip');
                const deviceId = serial || ip;
                if (deviceId) {
                    // File-explorer style: click selects, double-click opens details
                    handleDeviceSelection(deviceId, event);
                }
            });
            // Double-click opens device details
            tbody.addEventListener('dblclick', (event) => {
                if (event.target.closest('button') || event.target.closest('.table-actions') || event.target.closest('.actions-col')) {
                    return;
                }
                const row = event.target.closest('tr.device-row-clickable');
                if (!row) return;
                const serial = row.getAttribute('data-serial');
                const ip = row.getAttribute('data-ip');
                const lookup = serial || ip;
                if (lookup) {
                    window.__pm_shared.showPrinterDetails(lookup, 'saved');
                }
            });
        }
    }

    // Add click handler for clickable device cards (file-explorer style selection)
    const cardsContainer = document.getElementById('devices_cards');
    if (cardsContainer && !cardsContainer.dataset.cardClickBound) {
        cardsContainer.dataset.cardClickBound = 'true';
        cardsContainer.addEventListener('click', (event) => {
            // Don't trigger card click if clicking on a button or actions area
            if (event.target.closest('button') || event.target.closest('.device-card-actions')) {
                return;
            }
            const card = event.target.closest('.device-card-clickable');
            if (!card) return;
            const serial = card.getAttribute('data-serial');
            const ip = card.getAttribute('data-ip');
            const deviceId = serial || ip;
            if (deviceId) {
                // File-explorer style: click selects, double-click opens details
                handleDeviceSelection(deviceId, event);
            }
        });
        // Double-click opens device details
        cardsContainer.addEventListener('dblclick', (event) => {
            if (event.target.closest('button') || event.target.closest('.device-card-actions')) {
                return;
            }
            const card = event.target.closest('.device-card-clickable');
            if (!card) return;
            const serial = card.getAttribute('data-serial');
            const ip = card.getAttribute('data-ip');
            const lookup = serial || ip;
            if (lookup) {
                window.__pm_shared.showPrinterDetails(lookup, 'saved');
            }
        });
    }

    syncDevicesViewToggle();
    syncDeviceSortControls();
    syncDevicesAgentFilterOptions();
    refreshDeviceFilters();
    syncDeviceQuickFilters();
    renderDevicesOverview();
    syncTenantFilterOptions('devices');

    [table, cardsContainer].filter(Boolean).forEach(container => {
        container.addEventListener('keydown', event => {
            if (event.target.closest('button, input, select, a')) return;
            const device = event.target.closest('.device-row-clickable, .device-card-clickable');
            if (!device) return;
            if (event.key === 'Enter') {
                event.preventDefault();
                showPrinterDetails(device.dataset.serial, 'saved');
            } else if (event.key === ' ') {
                event.preventDefault();
                handleDeviceSelection(device.dataset.serial, event);
            }
        });
    });

    // Initialize context menu for devices table and cards
    if (window.PMContextMenu) {
        const devicesTable = document.getElementById('devices_table');
        if (devicesTable) {
            window.PMContextMenu.initDeviceContextMenu(devicesTable);
        }
        const devicesCards = document.getElementById('devices_cards');
        if (devicesCards) {
            window.PMContextMenu.initDeviceContextMenu(devicesCards);
        }
    }
}

/**
 * Initialize the devices table customizer
 */
function initDevicesTableCustomizer() {
    if (devicesVM.tableCustomizer) return;

    // Only initialize if TableCustomizer is available
    if (typeof window.TableCustomizer === 'undefined') {
        console.warn('TableCustomizer not available');
        return;
    }

    // Create customizer instance
    devicesVM.tableCustomizer = new window.TableCustomizer('devices', {
        columnDefs: window.DEVICES_COLUMN_DEFINITIONS || [],
        persistConfig: true,
        enableResize: true,
        enableReorder: true,
        enableColumnMenu: true,
        enableExport: true,
        onSort: (sortState) => {
            // Sync with devicesVM filters
            if (sortState.key) {
                devicesVM.filters.sortKey = sortState.key;
                devicesVM.filters.sortDir = sortState.dir;
                persistUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_KEY, sortState.key);
                persistUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_DIR, sortState.dir);
                syncDeviceSortControls();
                applyDeviceFilters();
            }
        },
        onColumnChange: () => {
            // Re-render table when columns change
            renderDevicesTableHeader();
            if (devicesVM.view === 'table') {
                renderDeviceTable(devicesVM.filtered);
            }
        },
        onExport: () => {
            // Export current filtered data
            if (devicesVM.tableCustomizer) {
                const timestamp = new Date().toISOString().split('T')[0];
                devicesVM.tableCustomizer.exportToCSV(devicesVM.filtered, `printmaster-devices-${timestamp}.csv`);
                window.__pm_shared?.showToast?.('Devices exported to CSV', 'success');
            }
        }
    });

    // Render toolbar
    const toolbarContainer = document.getElementById('devices_table_customizer_toolbar');
    if (toolbarContainer) {
        toolbarContainer.innerHTML = devicesVM.tableCustomizer.renderToolbar();
        devicesVM.tableCustomizer.bindToolbarEvents(toolbarContainer);
    }

    // Render initial header
    renderDevicesTableHeader();

    // Expose helper functions on shared for use by column renderers
    window.__pm_shared.renderDeviceStatusBadge = renderDeviceStatusBadge;
    window.__pm_shared.renderTonerBars = renderTonerBars;
}

let devicesLoader = null;
let devicesRefreshTimer = null;
const deviceSupplyBands = new Map();
let devicesDemand = [];
let devicesPaintFrame = null;

// Re-evaluate age without fetching full inventory or metrics. A printer must
// expire even when no new device_updated event arrives after it powers off.
setInterval(() => {
    if (devicesVM.loaded && isDevicesTabActive()) applyDeviceFilters(true);
}, 60 * 1000);

async function fetchDeviceInventory(path, keys, { signal }) {
    const response = await fetch('/api/v1/devices/' + path, keys ? {
        method: 'POST', signal, headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ serials: keys }),
    } : { signal });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.json();
}

function initDevicesLoader() {
    if (devicesLoader) return;
    devicesLoader = window.PrintMasterProgressive.createLoader({
        fetchIndex: context => fetchDeviceInventory('index', null, context),
        fetchRows: (keys, context) => fetchDeviceInventory('rows', keys, context),
        fetchMetrics: (keys, context) => fetchDeviceInventory('metrics/query', keys, context),
        getKey: device => device.serial,
        batchSize: 30, cacheLimit: 300, concurrency: 2,
        onChange: event => {
            if (event.type === 'reset') {
                cleanupDevicesInfiniteScroll();
                deviceSupplyBands.clear(); devicesDemand = [];
                if (devicesPaintFrame) cancelAnimationFrame(devicesPaintFrame);
                devicesPaintFrame = null;
                devicesVM.items = []; devicesVM.filtered = []; devicesVM.loaded = false;
                devicesVM.selection.selectedIds.clear();
                devicesVM.selection.lastSelected = null;
                renderDevicesOverview();
                return;
            }
            if (event.type === 'index') {
                const state = devicesLoader.getIndexState();
                devicesVM.loading = state.status === 'loading';
                devicesVM.error = state.error;
                if (state.status === 'loading') renderDevicesLoading();
                else if (state.status === 'error') renderDevicesError(state.error);
                else if (state.status === 'ready') {
                    devicesVM.items = enrichDevices(devicesLoader.getIndex());
                    devicesVM.loaded = true;
                    refreshDeviceFilters(); syncDevicesAgentFilterOptions();
                    applyDeviceFilters(); renderDevicesOverview();
                }
                renderDeviceLoadingStatus();
                return;
            }
            if (event.type === 'metrics') {
                event.keys.forEach(key => {
                    const state = devicesLoader.getState('metrics', key);
                    if (state.status === 'ready' || state.status === 'missing') {
                        const metrics = devicesLoader.getMetrics(key);
                        deviceSupplyBands.set(key, classifyConsumableBand({}, getDeviceConsumableLevels({ toner_levels: metrics?.toner_levels })));
                    }
                });
            }
            if (!devicesPaintFrame) devicesPaintFrame = requestAnimationFrame(() => {
                devicesPaintFrame = null;
                if (needsGlobalDeviceSupplies()) applyDeviceFilters(true);
                else patchVisibleDeviceRows();
                renderDeviceLoadingStatus();
            });
        },
    });
    document.getElementById('devices_retry_loading')?.addEventListener('click', () => {
        if (devicesVM.error) loadDevices(true);
        else {
            const failed = devicesVM.items.map(device => device.serial).filter(key =>
                ['rows', 'metrics'].some(kind => devicesLoader.getState(kind, key).status === 'error'));
            devicesLoader.retry(failed);
        }
    });
}

function hasDeviceSupplyFilter() {
    return devicesVM.filters.consumables.size > 0 && devicesVM.filters.consumables.size < DEVICE_CONSUMABLE_KEYS.length;
}

function needsGlobalDeviceSupplies() {
    return hasDeviceSupplyFilter() || devicesVM.filters.sortKey === 'consumables';
}

function deviceEmptyMessage() {
    if (needsGlobalDeviceSupplies() && devicesVM.items.some(device => !deviceSupplyBands.has(device.serial))) {
        return 'Checking supplies… Results incomplete; pending devices are not classified as unknown.';
    }
    return 'No devices match the current filters.';
}

function getProgressiveDevice(device) {
    if (!devicesLoader || !device) return device;
    const key = device.serial;
    const row = devicesLoader.getRow(key);
    const metrics = devicesLoader.getMetrics(key);
    const metricState = devicesLoader.getState('metrics', key).status;
    // raw_data is inventory metadata, NOT a trustworthy latest supply snapshot.
    const merged = { ...device, ...row, raw_data: { ...(row?.raw_data || {}) }, toner_levels: metrics?.toner_levels || {} };
    for (const name of Object.keys(merged.raw_data)) {
        if (/toner|ink|supply|consumable|color_pages|mono_pages|scan_count|copy_pages|print_pages|fax_pages|duplex_sheets/i.test(name)) delete merged.raw_data[name];
    }
    if (metrics) Object.assign(merged.raw_data, metrics);
    const enriched = enrichSingleDevice(merged);
    enriched.__meta.rowState = devicesLoader.getState('rows', key).status;
    enriched.__meta.metricsState = metricState;
    enriched.__meta.consumable = deviceSupplyBands.get(key) || { code: 'pending', label: 'Not loaded' };
    if (metricState !== 'ready') {
        enriched.__meta.tonerData = [];
        enriched.__meta.tonerData.pendingState = enriched.__meta.rowState === 'missing' ? 'missing' : metricState;
    }
    return enriched;
}

function patchVisibleDeviceRows() {
    const container = document.getElementById(devicesVM.view === 'table' ? 'devices_table' : 'devices_cards');
    if (!container) return;
    const records = new Map(devicesVM.items.map(device => [device.serial, device]));
    container.querySelectorAll('[data-serial].device-row-clickable, [data-serial].device-card-clickable').forEach(element => {
        const record = records.get(element.dataset.serial);
        if (!record) return;
        const device = getProgressiveDevice(record);
        // Keep row/card identity and focused nodes stable across async hydration.
        element.dataset.hydrated = device.__meta.rowState === 'ready' ? 'true' : 'false';
        if (devicesVM.view === 'table') {
            element.querySelectorAll('td[data-column-id]').forEach(cell => {
                const def = window.DEVICES_COLUMN_DEFINITIONS.find(column => column.id === cell.dataset.columnId);
                if (def) cell.innerHTML = def.render(device, device.__meta);
            });
        } else {
            const template = document.createElement('template');
            template.innerHTML = renderServerDeviceCard(device);
            element.innerHTML = template.content.firstElementChild.innerHTML;
        }
    });
    // A frame boundary guarantees rows paint before requesting beyond-page-count metrics.
    const epoch = devicesLoader.getGeneration();
    requestAnimationFrame(() => {
        if (epoch === devicesLoader.getGeneration()) devicesLoader.markRendered(devicesDemand);
    });
}

function renderDeviceLoadingStatus() {
    const status = document.getElementById('devices_loading_status');
    const retry = document.getElementById('devices_retry_loading');
    if (!status || !devicesLoader) return;
    const failures = devicesVM.items.filter(device => ['rows', 'metrics'].some(kind => devicesLoader.getState(kind, device.serial).status === 'error'));
    const pending = needsGlobalDeviceSupplies() ? devicesVM.items.filter(device => !deviceSupplyBands.has(device.serial)).length : 0;
    status.textContent = devicesVM.loading ? 'Loading device index…' : devicesVM.error ? 'Device index unavailable.' :
        failures.length ? `${failures.length} devices could not finish loading. Retry available (maximum two attempts per refresh).` :
        pending ? `Checking supplies: ${pending} devices pending. Results incomplete until checks finish.` : '';
    retry?.classList.toggle('hidden', !devicesVM.error && !failures.length);
}

async function loadDevices(force = false) {
    initDevicesUI();
    if (devicesVM.loading && !force) {
        return;
    }
    initDevicesLoader();
    // Directories enrich names/tenant controls later; never gate inventory on them.
    ensureAgentDirectory();
    ensureTenantDirectory();
    await devicesLoader.load();
}

function renderDevicesLoading() {
    const cards = document.getElementById('devices_cards');
    if (cards) {
        cards.classList.remove('hidden');
        cards.innerHTML = '<div class="muted-text">Loading devices…</div>';
    }
    const wrapper = document.getElementById('devices_table_wrapper');
    if (wrapper) {
        const tbody = wrapper.querySelector('tbody');
        if (tbody) {
            const visibleColCount = devicesVM.tableCustomizer?.getVisibleColumns()?.length || 12;
            tbody.innerHTML = `<tr><td colspan="${visibleColCount}" class="muted-text">Loading devices…</td></tr>`;
        }
    }
}

function renderDevicesError(error) {
    const message = error && error.message ? error.message : 'Unknown error';
    const cards = document.getElementById('devices_cards');
    if (cards) {
        cards.classList.remove('hidden');
        cards.innerHTML = `<div class="error-text">Failed to load devices: ${escapeHtml(message)}</div>`;
    }
    const wrapper = document.getElementById('devices_table_wrapper');
    if (wrapper) {
        const tbody = wrapper.querySelector('tbody');
        if (tbody) {
            const visibleColCount = devicesVM.tableCustomizer?.getVisibleColumns()?.length || 12;
            tbody.innerHTML = `<tr><td colspan="${visibleColCount}" class="error-text">Failed to load devices: ${escapeHtml(message)}</td></tr>`;
        }
    }
}

function renderDevicesOverview() {
    const container = document.getElementById('devices_overview_metrics');
    if (!container) return;
    // Devices overview uses the authorized index, not fleet-global metrics APIs.
    if (!devicesVM.loaded) {
        container.innerHTML = '<div class="metric-card loading">Loading authorized inventory…</div>';
        return;
    }
    const items = devicesVM.items;
    const pages = items.reduce((total, device) => total + (Number(device.page_count) || 0), 0);
    container.innerHTML = `
        <button type="button" class="metric-card devices-summary-card" data-summary-action="agents" aria-label="Filter devices by agent">
            <div class="card-title">Agents</div>
            <div class="metric-kpi-value">${formatNumber(new Set(items.map(device => device.agent_id).filter(Boolean)).size)}</div>
            <div class="metric-kpi-label">In authorized inventory</div>
            <span class="devices-summary-hint">Choose agent →</span>
        </button>
        <button type="button" class="metric-card devices-summary-card" data-summary-action="all-devices" aria-label="Show all devices and clear filters">
            <div class="card-title">Devices</div>
            <div class="metric-kpi-value">${formatNumber(items.length)}</div>
            <div class="metric-kpi-label">Authorized inventory</div>
            <span class="devices-summary-hint">Clear filters →</span>
        </button>
        <button type="button" class="metric-card devices-summary-card" data-summary-action="pages" aria-label="Sort devices by total pages">
            <div class="card-title">Total Pages</div>
            <div class="metric-kpi-value">${formatNumber(pages)}</div>
            <div class="metric-kpi-label">Latest indexed counts</div>
            <span class="devices-summary-hint">Sort by pages →</span>
        </button>
    `;
}

function refreshDeviceFilters() {
    const manufacturerSelect = document.getElementById('devices_manufacturer_filter');
    if (!manufacturerSelect) return;
    const manufacturers = Array.from(new Set((devicesVM.items || []).map(d => (d.manufacturer || '').trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
    let options = '<option value="">All Manufacturers</option>';
    manufacturers.forEach(name => {
        options += `<option value="${escapeHtml(name)}">${escapeHtml(name)}</option>`;
    });
    manufacturerSelect.innerHTML = options;
    if (devicesVM.filters.manufacturer && manufacturers.includes(devicesVM.filters.manufacturer)) {
        manufacturerSelect.value = devicesVM.filters.manufacturer;
    } else {
        manufacturerSelect.value = '';
        if (devicesVM.filters.manufacturer) {
            devicesVM.filters.manufacturer = '';
        }
    }
}

function syncDevicesAgentFilterOptions() {
    if (!devicesVM.uiInitialized) return;
    const select = document.getElementById('devices_agent_filter');
    if (!select) return;
    const known = new Map(agentDirectory.items.map(agent => [agent.agent_id, agent]));
    devicesVM.items.forEach(device => {
        if (device.agent_id && !known.has(device.agent_id)) known.set(device.agent_id, { agent_id: device.agent_id });
    });
    const agents = [...known.values()].sort((a, b) => {
        const aName = (a.name || a.hostname || a.agent_id || '').toLowerCase();
        const bName = (b.name || b.hostname || b.agent_id || '').toLowerCase();
        if (aName < bName) return -1;
        if (aName > bName) return 1;
        return 0;
    });
    let options = '<option value="">All Agents</option>';
    agents.forEach(agent => {
        const label = getAgentDisplayName(agent);
        options += `<option value="${escapeHtml(agent.agent_id)}">${escapeHtml(label)}</option>`;
    });
    select.innerHTML = options;
    select.value = devicesVM.filters.agentId || '';
}

function applyDeviceFilters(preservePage = false) {
    if (!devicesVM.loaded) return;
    if (!Array.isArray(devicesVM.items)) {
        return;
    }
    const filters = devicesVM.filters;
    if (devicesLoader) {
        devicesVM.items = enrichDevices(devicesLoader.getIndex());
        devicesVM.items.forEach(device => {
            device.__meta.consumable = deviceSupplyBands.get(device.serial) || { code: 'pending', label: 'Not loaded' };
        });
        if (needsGlobalDeviceSupplies() && isDevicesTabActive()) {
            devicesLoader.requestMetrics(devicesVM.items.filter(device => !deviceSupplyBands.has(device.serial)).map(device => device.serial));
        } else devicesLoader.clearMetricQueue();
    }
    const totalStatuses = createStatusCountMap();
    const filteredStatuses = createStatusCountMap();
    const filtered = [];
    devicesVM.items.forEach(device => {
        const statusKey = device.__meta?.status?.code || 'healthy';
        if (totalStatuses[statusKey] !== undefined) {
            totalStatuses[statusKey] += 1;
        }
        if (matchesDeviceFilters(device, filters)) {
            filtered.push(device);
            if (filteredStatuses[statusKey] !== undefined) {
                filteredStatuses[statusKey] += 1;
            }
        }
    });
    devicesVM.filtered = sortDevices(filtered);
    devicesVM.stats.filtered = devicesVM.filtered.length;
    devicesVM.stats.total = devicesVM.items.length;
    devicesVM.stats.totalStatuses = totalStatuses;
    devicesVM.stats.filteredStatuses = filteredStatuses;
    renderDevicesStats();
    renderDevicesActiveFilters();
    syncDeviceQuickFilters();
    const displayed = preservePage ? devicesVM.render.displayed : 0;
    if (preservePage) devicesVM.render.pageSize = Math.max(30, displayed);
    if (devicesVM.view === 'table') {
        renderDeviceTable(devicesVM.filtered);
    } else {
        renderDeviceCards(devicesVM.filtered);
    }
    devicesVM.render.pageSize = 30;
    updateDeviceSelectionUI();
    renderDeviceLoadingStatus();
    syncDeviceTableSortIndicators();
}

function matchesDeviceFilters(device, filters) {
    if (!device || !device.__meta) return true;
    const meta = device.__meta;
    const query = (filters.query || '').toLowerCase();
    if (query && (!meta.search || meta.search.indexOf(query) === -1)) {
        return false;
    }
    if (filters.agentId && device.agent_id !== filters.agentId) {
        return false;
    }
    const tenantId = meta.tenantId || device.tenant_id || '';
    if (filters.tenantId && tenantId !== filters.tenantId) {
        return false;
    }
    if (filters.manufacturer && (device.manufacturer || '').trim() !== filters.manufacturer) {
        return false;
    }
    if (filters.statuses && filters.statuses.size > 0 && !filters.statuses.has(meta.status?.code || 'healthy')) {
        return false;
    }
    if (hasDeviceSupplyFilter() && !filters.consumables.has(meta.consumable?.code)) {
        return false;
    }
    return true;
}

function sortDevices(list) {
    const key = devicesVM.filters.sortKey || 'last_seen';
    const dir = devicesVM.filters.sortDir === 'asc' ? 1 : -1;
    const sorted = list.slice();
    sorted.sort((a, b) => {
        const aVal = getDeviceSortValue(a, key);
        const bVal = getDeviceSortValue(b, key);
        if (aVal < bVal) return -1 * dir;
        if (aVal > bVal) return 1 * dir;
        const aSerial = (a.serial || '').toLowerCase();
        const bSerial = (b.serial || '').toLowerCase();
        if (aSerial < bSerial) return -1;
        if (aSerial > bSerial) return 1;
        return 0;
    });
    return sorted;
}

function getDeviceSortValue(device, key) {
    const meta = device.__meta || {};
    switch (key) {
        case 'page_count':
            return Number(device.page_count) || 0;
        case 'consumables':
            return DEVICE_CONSUMABLE_ORDER[meta.consumable?.code] || 0;
        case 'manufacturer':
            return ((device.manufacturer || '') + ' ' + (device.model || '')).toLowerCase();
        case 'agent':
            return (meta.agentName || '').toLowerCase();
        case 'tenant':
            return formatTenantDisplay(meta.tenantId || device.tenant_id || '').toLowerCase();
        case 'status':
            return DEVICE_STATUS_ORDER[meta.status?.code || 'healthy'] || 0;
        case 'location':
            return (meta.location || '').toLowerCase();
        case 'ip':
            return buildSortableIpValue(device.ip);
        case 'last_seen':
        default:
            return meta.lastSeenMs || 0;
    }
}

function buildSortableIpValue(rawValue) {
    if (!rawValue) return 'zzz';
    let value = String(rawValue).trim();
    if (!value) return 'zzz';

    if (value.startsWith('[') && value.includes(']')) {
        value = value.slice(1, value.indexOf(']'));
    }

    const ipv4PortMatch = value.match(/^(\d{1,3}(?:\.\d{1,3}){3})(?::\d+)?$/);
    const ipv4Candidate = ipv4PortMatch ? ipv4PortMatch[1] : value;
    if (/^\d{1,3}(\.\d{1,3}){3}$/.test(ipv4Candidate)) {
        const octets = ipv4Candidate.split('.').map(part => {
            const num = parseInt(part, 10);
            if (!Number.isFinite(num) || num < 0 || num > 255) {
                return null;
            }
            return String(num).padStart(3, '0');
        });
        if (!octets.includes(null)) {
            return 'v4-' + octets.join('.');
        }
    }

    return 'v6-' + value.toLowerCase();
}

function renderDeviceCards(devices, append = false) {
    const cards = document.getElementById('devices_cards');
    const tableWrapper = document.getElementById('devices_table_wrapper');
    if (!cards) return;
    if (tableWrapper) {
        tableWrapper.classList.add('hidden');
    }
    cards.classList.remove('hidden');

    if (!devices || devices.length === 0) {
        cards.innerHTML = `<div class="muted-text">${deviceEmptyMessage()}</div>`;
        cleanupDevicesInfiniteScroll();
        return;
    }

    // Progressive rendering - only render a page at a time
    if (!append) {
        devicesVM.render.displayed = 0;
        cards.innerHTML = '';
    }

    const startIdx = devicesVM.render.displayed;
    const endIdx = Math.min(startIdx + devicesVM.render.pageSize, devices.length);
    const pageDevices = devices.slice(startIdx, endIdx);

    // Remove existing sentinel
    const existingSentinel = document.getElementById('devices_load_more_sentinel');
    if (existingSentinel) existingSentinel.remove();

    // Render this page
    const html = pageDevices.map(device => renderServerDeviceCard(getProgressiveDevice(device))).join('');
    cards.insertAdjacentHTML('beforeend', html);
    devicesVM.render.displayed = endIdx;

    // Add sentinel if more items available
    if (endIdx < devices.length) {
        const sentinel = document.createElement('div');
        sentinel.id = 'devices_load_more_sentinel';
        sentinel.className = 'devices-load-sentinel';
        sentinel.innerHTML = '<button type="button" class="ghost-btn" data-load-more>Load more devices</button>';
        sentinel.querySelector('button').addEventListener('click', loadMoreDevices);
        cards.appendChild(sentinel);
    }
    setupDevicesInfiniteScroll();
    updateDeviceSelectionUI();
}

/**
 * Render the devices table header using the customizer
 */
function renderDevicesTableHeader() {
    const headerRow = document.getElementById('devices_table_header');
    if (!headerRow) return;

    if (devicesVM.tableCustomizer) {
        headerRow.innerHTML = devicesVM.tableCustomizer.renderHeader();
        // Re-bind header events for sorting/resizing
        const thead = headerRow.closest('thead');
        if (thead) {
            devicesVM.tableCustomizer.bindHeaderEvents(thead);
        }
    } else {
        // Fallback to static header
        // Actions column removed - using context menu instead (right-click)
        headerRow.innerHTML = `
            <th data-sort-key="manufacturer">Device</th>
            <th data-sort-key="status">Status</th>
            <th data-sort-key="consumables">Consumables</th>
            <th data-sort-key="agent">Agent</th>
            <th data-sort-key="tenant">Tenant</th>
            <th data-sort-key="ip">Network</th>
            <th data-sort-key="location">Location</th>
            <th data-sort-key="last_seen">Last Seen</th>
        `;
    }
}

function renderDeviceTable(devices, append = false) {
    const cards = document.getElementById('devices_cards');
    const wrapper = document.getElementById('devices_table_wrapper');
    if (!wrapper) return;
    if (cards) {
        cards.classList.add('hidden');
    }
    wrapper.classList.remove('hidden');

    // Initialize horizontal scroll indicators for the table
    const tableWrapper = wrapper.querySelector('.table-wrapper');
    if (tableWrapper) {
        initTableScrollIndicators(tableWrapper);
    }

    const tbody = wrapper.querySelector('tbody');
    if (!tbody) return;

    // Get visible columns count for colspan
    const visibleColCount = devicesVM.tableCustomizer?.getVisibleColumns()?.length || 9;

    if (!devices || devices.length === 0) {
        tbody.innerHTML = `<tr><td colspan="${visibleColCount}" class="muted-text">${deviceEmptyMessage()}</td></tr>`;
        cleanupDevicesInfiniteScroll();
        return;
    }

    // Progressive rendering - only render a page at a time
    if (!append) {
        devicesVM.render.displayed = 0;
        tbody.innerHTML = '';
    }

    const startIdx = devicesVM.render.displayed;
    const endIdx = Math.min(startIdx + devicesVM.render.pageSize, devices.length);
    const pageDevices = devices.slice(startIdx, endIdx);

    // Remove existing sentinel
    const existingSentinel = document.getElementById('devices_load_more_sentinel');
    if (existingSentinel) existingSentinel.remove();

    // Use customizer to render rows if available
    const rows = pageDevices.map(record => {
        const device = getProgressiveDevice(record);
        const meta = device.__meta || {};
        const serial = escapeHtml(device.serial || '');
        const ip = escapeHtml(device.ip || '');

        let rowContent;
        if (devicesVM.tableCustomizer) {
            rowContent = devicesVM.tableCustomizer.renderRow(device, meta);
        } else {
            // Fallback to legacy rendering
            const tenantLabel = formatTenantDisplay(meta.tenantId || device.tenant_id || '');
            rowContent = `
                <td>
                    <div class="table-primary">${escapeHtml((device.manufacturer || 'Unknown') + ' ' + (device.model || ''))}</div>
                    <div class="muted-text">Serial ${escapeHtml(device.serial || '—')}</div>
                </td>
                <td>
                    ${renderDeviceStatusBadge(meta.status)}
                </td>
                <td>
                    ${renderTonerBars(meta.tonerData)}
                </td>
                <td>${escapeHtml(meta.agentName || 'Unassigned')}</td>
                <td>${escapeHtml(tenantLabel)}</td>
                <td>
                    <div class="table-primary">${escapeHtml(device.ip || 'N/A')}</div>
                    ${device.hostname ? `<div class="muted-text">${escapeHtml(device.hostname)}</div>` : ''}
                </td>
                <td>${escapeHtml(meta.location || '—')}</td>
                <td title="${escapeHtml(meta.lastSeenTooltip || 'Never')}">${escapeHtml(meta.lastSeenRelative || 'Never')}</td>
            `;
        }

        return `<tr tabindex="0" data-hydrated="${meta.rowState === 'ready'}" data-serial="${serial}" data-ip="${ip}" data-agent-id="${escapeHtml(device.agent_id || '')}" class="device-row-clickable" title="Click to view details, right-click for actions">${rowContent}</tr>`;
    }).join('');

    tbody.insertAdjacentHTML('beforeend', rows);
    devicesVM.render.displayed = endIdx;

    // Add sentinel row if more items available
    if (endIdx < devices.length) {
        const sentinelRow = document.createElement('tr');
        sentinelRow.id = 'devices_load_more_sentinel';
        sentinelRow.className = 'devices-load-sentinel';
        sentinelRow.innerHTML = `<td colspan="${visibleColCount}" style="text-align:center;padding:16px;"><button type="button" class="ghost-btn" data-load-more>Load more devices</button></td>`;
        sentinelRow.querySelector('button').addEventListener('click', loadMoreDevices);
        tbody.appendChild(sentinelRow);
    }
    setupDevicesInfiniteScroll();
    updateDeviceSelectionUI();
}

// Setup IntersectionObserver for devices infinite scroll
function setupDevicesInfiniteScroll() {
    cleanupDevicesInfiniteScroll();
    const sentinel = document.getElementById('devices_load_more_sentinel');
    const container = document.getElementById(devicesVM.view === 'table' ? 'devices_table' : 'devices_cards');
    if (!container || !devicesLoader) return;
    const elements = [...container.querySelectorAll('.device-row-clickable, .device-card-clickable')];
    devicesVM.render.observer = window.PrintMasterProgressive.observeViewport({
        elements, sentinel, getKey: element => element.dataset.serial,
        onDemand: keys => {
            devicesDemand = keys;
            devicesLoader.setDemand(keys);
            const epoch = devicesLoader.getGeneration();
            requestAnimationFrame(() => {
                if (epoch === devicesLoader.getGeneration()) devicesLoader.markRendered(keys.filter(key =>
                    elements.some(element => element.dataset.serial === key && element.dataset.hydrated === 'true')));
            });
        },
        onMore: loadMoreDevices,
    });
}

// Cleanup the devices infinite scroll observer
function cleanupDevicesInfiniteScroll() {
    if (devicesVM.render.observer) {
        devicesVM.render.observer.disconnect();
        devicesVM.render.observer = null;
    }
}

// Load more devices for infinite scroll
function loadMoreDevices() {
    if (devicesVM.render.displayed >= devicesVM.filtered.length) return;
    if (devicesVM.view === 'table') {
        renderDeviceTable(devicesVM.filtered, true);
    } else {
        renderDeviceCards(devicesVM.filtered, true);
    }
}

function renderServerDeviceCard(device) {
    const meta = device.__meta || {};
    const serial = escapeHtml(device.serial || '—');
    const agentId = escapeHtml(device.agent_id || '');
    const networkLabel = escapeHtml(device.ip || 'N/A');
    const hostname = device.hostname ? ` • ${escapeHtml(device.hostname)}` : '';
    const asset = device.asset_number ? `<span class="device-card-chip">Asset ${escapeHtml(device.asset_number)}</span>` : '';
    const location = escapeHtml(meta.location || '—');
    const tenantLabel = escapeHtml(formatTenantDisplay(meta.tenantId || device.tenant_id || ''));
    const lastSeenText = escapeHtml(meta.lastSeenRelative || 'Never');
    const lastSeenTitle = escapeHtml(meta.lastSeenTooltip || 'Never');
    const agentName = escapeHtml(meta.agentName || 'Unassigned');
    const capabilityBadges = renderDeviceCapabilityBadges(device);
    return `
        <div tabindex="0" data-hydrated="${meta.rowState === 'ready'}" class="device-card device-card-clickable" data-serial="${serial}" data-ip="${escapeHtml(device.ip || '')}" data-agent-id="${agentId}" data-mac="${escapeHtml(device.mac || '')}" data-source="saved" title="Click to view details, right-click for actions">
            <div class="device-card-header">
                <div>
                    <div class="device-card-title">${escapeHtml(device.manufacturer || 'Unknown')} ${escapeHtml(device.model || '')}</div>
                    <div class="device-card-subtitle">Serial ${serial} ${asset}</div>
                    <div class="device-card-subtitle">${agentName}</div>
                    ${capabilityBadges ? `<div class="device-card-capabilities">${capabilityBadges}</div>` : ''}
                </div>
                <div class="device-card-status">
                    ${renderDeviceStatusBadge(meta.status)}
                    ${renderTonerBars(meta.tonerData)}
                </div>
            </div>
            <div class="device-card-info">
                <div class="device-card-row">
                    <span class="device-card-label">Network</span>
                    <span class="device-card-value copyable" data-copy="${escapeHtml(device.ip || '')}">${networkLabel}${hostname}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Tenant</span>
                    <span class="device-card-value">${tenantLabel}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Location</span>
                    <span class="device-card-value">${location}</span>
                </div>
                <div class="device-card-row">
                    <span class="device-card-label">Last Seen</span>
                    <span class="device-card-value" title="${lastSeenTitle}">${lastSeenText}</span>
                </div>
            </div>
            <div class="device-card-hint muted-text" style="font-size:11px;padding:8px 12px;text-align:center;border-top:1px solid var(--border);">
                Right-click for actions
            </div>
        </div>
    `;
}

function renderDeviceStatusBadge(statusMeta) {
    const code = statusMeta?.code || 'healthy';
    const label = statusMeta?.label || code;
    return `<span class="status-pill ${code}">${escapeHtml(label)}</span>`;
}

function renderDeviceCapabilityBadges(device) {
    const badges = [];
    const rd = device.raw_data || {};

    // Device type badge (most descriptive)
    if (rd.device_type) {
        badges.push(`<span class="capability-badge type">${escapeHtml(rd.device_type)}</span>`);
    } else {
        // Fallback to individual capabilities
        if (rd.is_color) {
            badges.push('<span class="capability-badge color">Color</span>');
        } else if (rd.is_mono) {
            badges.push('<span class="capability-badge mono">Mono</span>');
        }
    }

    // Function capabilities (only show if device_type not set, to avoid redundancy)
    if (!rd.device_type) {
        if (rd.is_copier) badges.push('<span class="capability-badge function">Copier</span>');
        if (rd.is_scanner) badges.push('<span class="capability-badge function">Scanner</span>');
        if (rd.is_fax) badges.push('<span class="capability-badge function">Fax</span>');
    }

    // Technology badge
    if (rd.is_laser) {
        badges.push('<span class="capability-badge tech">Laser</span>');
    } else if (rd.is_inkjet) {
        badges.push('<span class="capability-badge tech">Inkjet</span>');
    }

    // Duplex badge
    if (rd.has_duplex) {
        badges.push('<span class="capability-badge feature">Duplex</span>');
    }

    return badges.join('');
}

function renderDeviceConsumableBadge(consumableMeta) {
    if (!consumableMeta) return '';
    let text = DEVICE_CONSUMABLE_LABELS[consumableMeta.code || 'unknown'] || 'Unknown';
    if (typeof consumableMeta.level === 'number') {
        text += ` ${consumableMeta.level}%`;
    }
    return `<span class="consumable-pill" data-band="${consumableMeta.code || 'unknown'}">${escapeHtml(text)}</span>`;
}

// Map toner names to CSS colors
// Toner color mapping, ink/toner filtering, and toner-bar rendering now live
// in common/web/cards.js (window.__pm_shared_cards.getDeviceTonerBarData /
// renderTonerBars) so agent and server share identical coloring and
// mono/color filtering behavior.
function getDeviceTonerData(device) {
    return window.__pm_shared_cards.getDeviceTonerBarData(device);
}

function renderTonerBars(tonerData) {
    if (tonerData?.pendingState) return `<span class="muted-text" role="status">${tonerData.pendingState === 'error' ? 'Supplies unavailable' : tonerData.pendingState === 'missing' ? 'No supply snapshot' : 'Loading supplies…'}</span>`;
    return window.__pm_shared_cards.renderTonerBars(tonerData);
}

function renderDevicesStats() {
    const container = document.getElementById('devices_stats');
    if (!container) return;
    const total = devicesVM.stats.total || 0;
    const filtered = devicesVM.stats.filtered || 0;
    const statuses = devicesVM.stats.filteredStatuses || {};
    container.innerHTML = `
        <div><strong>Total:</strong> ${formatNumber(total)}</div>
        <div><strong>Showing:</strong> ${formatNumber(filtered)}</div>
        <div>
            <span class="status-pill healthy">Healthy ${formatNumber(statuses.healthy || 0)}</span>
            <span class="status-pill warning">Warning ${formatNumber(statuses.warning || 0)}</span>
            <span class="status-pill error">Error ${formatNumber(statuses.error || 0)}</span>
            <span class="status-pill jam">Jam ${formatNumber(statuses.jam || 0)}</span>
            <span class="status-pill offline">Offline ${formatNumber(statuses.offline || 0)}</span>
            <span class="status-pill unknown">Unknown ${formatNumber(statuses.unknown || 0)}</span>
        </div>
    `;
}

function renderDevicesActiveFilters() {
    const container = document.getElementById('devices_active_filters');
    if (!container) return;
    const chips = [];
    const filters = devicesVM.filters;
    if (filters.query) {
        chips.push(buildFilterChip('Search', filters.query, 'search'));
    }
    if (filters.agentId) {
        const agent = getAgentInfo(filters.agentId);
        const label = agent ? getAgentDisplayName(agent) : filters.agentId;
        chips.push(buildFilterChip('Agent', label, 'agent'));
    }
    if (filters.tenantId) {
        chips.push(buildFilterChip('Tenant', formatTenantDisplay(filters.tenantId), 'tenant'));
    }
    if (filters.manufacturer) {
        chips.push(buildFilterChip('Manufacturer', filters.manufacturer, 'manufacturer'));
    }
    if (filters.statuses.size > 0 && filters.statuses.size < DEVICE_STATUS_KEYS.length) {
        chips.push(buildFilterChip('Status', Array.from(filters.statuses).join(', '), 'statuses'));
    }
    if (filters.consumables.size > 0 && filters.consumables.size < DEVICE_CONSUMABLE_KEYS.length) {
        chips.push(buildFilterChip('Consumables', Array.from(filters.consumables).join(', '), 'consumables'));
    }
    if (chips.length === 0) {
        container.innerHTML = '';
        container.classList.add('hidden');
        return;
    }
    container.classList.remove('hidden');
    container.innerHTML = chips.join('');
}

function buildFilterChip(label, value, key) {
    return `<span class="filter-chip">${escapeHtml(label)}: ${escapeHtml(value)} <button type="button" data-filter="${key}" aria-label="Remove ${escapeHtml(label)} filter">×</button></span>`;
}

function handleFilterChipRemove(filterKey) {
    switch (filterKey) {
        case 'search': {
            devicesVM.filters.query = '';
            const searchInput = document.getElementById('devices_search');
            if (searchInput) searchInput.value = '';
            break;
        }
        case 'agent': {
            devicesVM.filters.agentId = '';
            const agentSelect = document.getElementById('devices_agent_filter');
            if (agentSelect) agentSelect.value = '';
            break;
        }
        case 'tenant': {
            devicesVM.filters.tenantId = '';
            const tenantSelect = document.getElementById('devices_tenant_filter');
            if (tenantSelect) tenantSelect.value = '';
            break;
        }
        case 'manufacturer': {
            devicesVM.filters.manufacturer = '';
            const manufacturerSelect = document.getElementById('devices_manufacturer_filter');
            if (manufacturerSelect) manufacturerSelect.value = '';
            break;
        }
        case 'statuses':
            devicesVM.filters.statuses = new Set(DEVICE_STATUS_KEYS);
            break;
        case 'consumables':
            devicesVM.filters.consumables = new Set(DEVICE_CONSUMABLE_KEYS);
            break;
        default:
            return;
    }
    applyDeviceFilters();
}

function syncDeviceQuickFilters() {
    const statusSet = devicesVM.filters.statuses;
    document.querySelectorAll('#devices_status_filter [data-status]').forEach(btn => {
        const key = btn.getAttribute('data-status');
        const active = !statusSet || statusSet.has(key);
        btn.classList.toggle('active', active);
        const baseLabel = btn.getAttribute('data-label') || btn.textContent.trim();
        const count = devicesVM.stats.totalStatuses?.[key] || 0;
        btn.innerHTML = `${escapeHtml(baseLabel)} <span class="pill-count">${formatNumber(count)}</span>`;
    });

    const consumableSet = devicesVM.filters.consumables;
    document.querySelectorAll('#devices_consumable_filter [data-band]').forEach(btn => {
        const key = btn.getAttribute('data-band');
        const active = !consumableSet || consumableSet.has(key);
        btn.classList.toggle('active', active);
        const baseLabel = btn.getAttribute('data-label') || btn.textContent.trim();
        btn.innerHTML = `${escapeHtml(baseLabel)}`;
    });
}

function toggleStatusFilter(statusKey) {
    if (!DEVICE_STATUS_KEYS.includes(statusKey)) return;
    const set = new Set(devicesVM.filters.statuses || DEVICE_STATUS_KEYS);
    if (set.has(statusKey)) {
        set.delete(statusKey);
    } else {
        set.add(statusKey);
    }
    if (set.size === 0) {
        DEVICE_STATUS_KEYS.forEach(key => set.add(key));
    }
    devicesVM.filters.statuses = set;
    applyDeviceFilters();
}

function toggleConsumableFilter(bandKey) {
    if (!DEVICE_CONSUMABLE_KEYS.includes(bandKey)) return;
    const set = new Set(devicesVM.filters.consumables || DEVICE_CONSUMABLE_KEYS);
    if (set.has(bandKey)) {
        set.delete(bandKey);
    } else {
        set.add(bandKey);
    }
    if (set.size === 0) {
        DEVICE_CONSUMABLE_KEYS.forEach(key => set.add(key));
    }
    devicesVM.filters.consumables = set;
    applyDeviceFilters();
}

function resetDeviceFilters() {
    devicesVM.filters.query = '';
    devicesVM.filters.agentId = '';
    devicesVM.filters.tenantId = '';
    devicesVM.filters.manufacturer = '';
    devicesVM.filters.statuses = new Set(DEVICE_STATUS_KEYS);
    devicesVM.filters.consumables = new Set(DEVICE_CONSUMABLE_KEYS);
    const searchInput = document.getElementById('devices_search');
    if (searchInput) searchInput.value = '';
    const agentSelect = document.getElementById('devices_agent_filter');
    if (agentSelect) agentSelect.value = '';
    const tenantSelect = document.getElementById('devices_tenant_filter');
    if (tenantSelect) tenantSelect.value = '';
    const manufacturerSelect = document.getElementById('devices_manufacturer_filter');
    if (manufacturerSelect) manufacturerSelect.value = '';
    applyDeviceFilters();
}

function setDevicesView(view) {
    const nextView = DEVICES_VIEW_OPTIONS.includes(view) ? view : 'cards';
    if (devicesVM.view === nextView) {
        return;
    }
    devicesVM.view = nextView;
    persistUIState(SERVER_UI_STATE_KEYS.DEVICES_VIEW, nextView);
    syncDevicesViewToggle();
    applyDeviceFilters();
}

function syncDevicesViewToggle() {
    const toggle = document.getElementById('devices_view_toggle');
    if (!toggle) return;
    toggle.querySelectorAll('[data-view]').forEach(btn => {
        const view = btn.getAttribute('data-view');
        const active = view === devicesVM.view;
        btn.classList.toggle('active', active);
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
}

function setDeviceSort(key, dir) {
    const nextKey = DEVICES_SORT_KEYS.includes(key) ? key : 'last_seen';
    const nextDir = dir === 'asc' ? 'asc' : 'desc';
    if (devicesVM.filters.sortKey === nextKey && devicesVM.filters.sortDir === nextDir) {
        return;
    }
    devicesVM.filters.sortKey = nextKey;
    devicesVM.filters.sortDir = nextDir;
    persistUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_KEY, nextKey);
    persistUIState(SERVER_UI_STATE_KEYS.DEVICES_SORT_DIR, nextDir);
    syncDeviceSortControls();
    applyDeviceFilters();
}

function syncDeviceSortControls() {
    const sortSelect = document.getElementById('devices_sort_select');
    if (sortSelect && sortSelect.value !== devicesVM.filters.sortKey) {
        sortSelect.value = devicesVM.filters.sortKey;
    }
    const sortDirBtn = document.getElementById('devices_sort_dir_btn');
    const sortDirIcon = document.getElementById('devices_sort_dir_icon');
    if (sortDirBtn) {
        sortDirBtn.dataset.dir = devicesVM.filters.sortDir;
        sortDirBtn.setAttribute('aria-label', devicesVM.filters.sortDir === 'asc' ? 'Sort ascending' : 'Sort descending');
    }
    if (sortDirIcon) {
        sortDirIcon.textContent = devicesVM.filters.sortDir === 'asc' ? '↑' : '↓';
    }
}

function syncDeviceTableSortIndicators() {
    const head = document.querySelector('#devices_table thead');
    if (!head) return;

    // Update customizer sort state if available
    if (devicesVM.tableCustomizer) {
        devicesVM.tableCustomizer.sortState.key = devicesVM.filters.sortKey;
        devicesVM.tableCustomizer.sortState.dir = devicesVM.filters.sortDir;
    }

    head.querySelectorAll('th[data-sort-key]').forEach(th => {
        const key = th.getAttribute('data-sort-key');
        if (key === devicesVM.filters.sortKey) {
            th.classList.add('sorted');
            th.setAttribute('aria-sort', devicesVM.filters.sortDir === 'asc' ? 'ascending' : 'descending');
        } else {
            th.classList.remove('sorted');
            th.removeAttribute('aria-sort');
        }
    });
}

function handleDeviceTableSortClick(event) {
    const target = event.target.closest('th[data-sort-key]');
    if (!target) {
        return;
    }
    const key = target.getAttribute('data-sort-key');
    if (!key) {
        return;
    }
    const nextDir = (devicesVM.filters.sortKey === key && devicesVM.filters.sortDir === 'asc') ? 'desc' : 'asc';
    setDeviceSort(key, nextDir);
}

function upsertDeviceRecord(record) {
    if (!record) {
        return;
    }
    if (!Array.isArray(devicesVM.items)) {
        devicesVM.items = [];
    }
    const identifier = (item) => item.serial || item.device_id || item.id || item.uuid;
    const recordId = identifier(record);
    let updated = false;
    devicesVM.items = devicesVM.items.map(device => {
        const id = identifier(device);
        if (recordId && id && id === recordId) {
            updated = true;
            return enrichSingleDevice({ ...device, ...record });
        }
        if (!recordId && device.ip && record.ip && device.ip === record.ip) {
            updated = true;
            return enrichSingleDevice({ ...device, ...record });
        }
        return device;
    });
    if (!updated) {
        devicesVM.items.push(enrichSingleDevice(record));
    }
    devicesVM.stats.total = devicesVM.items.length;
    devicesVM.loaded = true;
    refreshDeviceFilters();
}

function enrichDevices(list) {
    if (!Array.isArray(list)) return [];
    return list.map(item => enrichSingleDevice(item));
}

function enrichSingleDevice(device) {
    if (!device || typeof device !== 'object') {
        return device;
    }
    const agent = getAgentInfo(device.agent_id);
    const agentName = agent ? getAgentDisplayName(agent) : (device.agent_id || '');
    const tenantId = device.tenant_id || (agent && agent.tenant_id) || '';
    const tenantLabel = tenantId ? tenantDisplayNameById(tenantId) : '';
    const lastSeenIso = device.last_seen || device.lastSeen || device.last_seen_at || device.updated_at || device.last_metrics_at;
    const lastSeenDate = lastSeenIso ? new Date(lastSeenIso) : null;
    const location = device.location || device.site || device.department || device.building || '';
    const tonerLevels = getDeviceConsumableLevels(device);
    const tonerData = getDeviceTonerData(device);
    const status = classifyDeviceStatus(device);
    const consumable = classifyConsumableBand(device, tonerLevels);
    return {
        ...device,
        __meta: {
            agentName,
            tenantId,
            search: buildDeviceSearchBlob(device, agentName, tenantLabel || tenantId),
            location,
            status,
            consumable,
            tonerData,
            lastSeenRelative: lastSeenDate ? formatRelativeTime(lastSeenDate) : 'Never',
            lastSeenTooltip: lastSeenDate ? lastSeenDate.toLocaleString() : 'Never',
            lastSeenMs: lastSeenDate ? lastSeenDate.getTime() : 0,
        }
    };
}

function buildDeviceSearchBlob(device, agentName, tenantLabel) {
    const parts = [
        device.serial,
        device.ip,
        device.hostname,
        device.manufacturer,
        device.model,
        device.asset_number,
        device.location,
        agentName,
        tenantLabel,
        device.tenant_id,
    ].filter(Boolean);
    return parts.join(' ').toLowerCase();
}

function classifyDeviceStatus(device, now = Date.now()) {
    const meta = { code: 'healthy', label: 'Healthy' };
    const severity = (device.status_severity || device.health_state || '').toLowerCase();
    const composite = [device.status, device.state, device.health, device.connection_state, device.spooler_status, ...(device.status_messages || [])].filter(Boolean).join(' ').toLowerCase();
    // Reachability takes precedence over cached printer faults. Upload time and
    // metrics time are not evidence that the physical printer is still alive.
    const seen = Date.parse(device.last_seen || device.lastSeen || device.last_seen_at || '');
    if (composite.includes('offline') || composite.includes('down')) {
        return { code: 'offline', label: 'Offline' };
    }
    if (!Number.isFinite(seen) || seen <= 0 || seen > now + 60 * 1000) {
        return { code: 'unknown', label: 'Unknown' };
    }
    if (now - seen >= DEVICE_OFFLINE_AFTER_MS) {
        return { code: 'offline', label: 'Offline' };
    }
    if (composite.includes('jam')) {
        return { code: 'jam', label: 'Paper Jam' };
    }
    if (severity.includes('error') || composite.includes('error')) {
        return { code: 'error', label: 'Error' };
    }
    if (severity.includes('warn') || composite.includes('warn') || composite.includes('degraded')) {
        return { code: 'warning', label: 'Warning' };
    }
    if (composite.includes('ready') || composite.includes('idle')) {
        return { code: 'healthy', label: 'Ready' };
    }
    return meta;
}

function classifyConsumableBand(device, tonerLevels) {
    if (!tonerLevels || tonerLevels.length === 0) {
        return { code: 'unknown', label: 'Unknown' };
    }
    const min = Math.min(...tonerLevels);
    const code = bandForPercentage(min);
    return { code, label: DEVICE_CONSUMABLE_LABELS[code] || 'Unknown', level: min };
}

function getDeviceConsumableLevels(device) {
    return window.__pm_shared_cards.getDeviceTonerBarData(device).map(t => t.level);
}

function bandForPercentage(value) {
    if (typeof value !== 'number') return 'unknown';
    if (value <= 10) return 'critical';
    if (value <= 25) return 'low';
    if (value <= 60) return 'medium';
    return 'high';
}

function getAgentInfo(agentId) {
    if (!agentId) {
        return null;
    }
    if (agentDirectory.byId.has(agentId)) {
        return agentDirectory.byId.get(agentId);
    }
    return null;
}

async function ensureAgentDirectory(force = false) {
    const now = Date.now();
    if (!force && agentDirectory.items.length > 0 && (now - agentDirectory.lastFetched) < 30000) {
        return agentDirectory.items;
    }
    try {
        const response = await fetch('/api/v1/agents/list');
        if (!response.ok) {
            throw new Error('HTTP ' + response.status);
        }
        const agents = await response.json();
        updateAgentDirectory(Array.isArray(agents) ? agents : []);
        return agentDirectory.items;
    } catch (err) {
        window.__pm_shared.warn('ensureAgentDirectory failed', err);
        return agentDirectory.items;
    }
}

function updateAgentDirectory(list) {
    if (!Array.isArray(list)) {
        return;
    }
    agentDirectory.items = list.slice();
    agentDirectory.byId = new Map();
    agentDirectory.items.forEach(agent => {
        if (agent && agent.agent_id) {
            agentDirectory.byId.set(agent.agent_id, agent);
        }
    });
    agentDirectory.lastFetched = Date.now();
    syncDevicesAgentFilterOptions();
    if (devicesVM.loaded) applyDeviceFilters(true);
}

function patchAgentDirectory(agent) {
    if (!agent || !agent.agent_id) {
        return;
    }
    if (!agentDirectory.byId) {
        agentDirectory.byId = new Map();
    }
    if (!agentDirectory.items) {
        agentDirectory.items = [];
    }
    agentDirectory.byId.set(agent.agent_id, { ...agentDirectory.byId.get(agent.agent_id), ...agent });
    let replaced = false;
    agentDirectory.items = agentDirectory.items.map(existing => {
        if (existing && existing.agent_id === agent.agent_id) {
            replaced = true;
            return { ...existing, ...agent };
        }
        return existing;
    });
    if (!replaced) {
        agentDirectory.items.push(agent);
    }
    agentDirectory.lastFetched = Date.now();
    syncDevicesAgentFilterOptions();
}

function normalizeTenantId(record) {
    if (!record) return '';
    return record.id || record.uuid || record.tenant_id || '';
}

function getTenantInfo(tenantId) {
    if (!tenantId || !tenantDirectory.byId) {
        return null;
    }
    return tenantDirectory.byId.get(tenantId) || null;
}

async function ensureTenantDirectory(force = false) {
    const now = Date.now();
    if (!force && tenantDirectory.items.length > 0 && (now - tenantDirectory.lastFetched) < 60000) {
        return tenantDirectory.items;
    }
    try {
        const response = await fetch('/api/v1/tenants');
        if (!response.ok) {
            throw new Error('HTTP ' + response.status);
        }
        const tenants = await response.json();
        updateTenantDirectory(Array.isArray(tenants) ? tenants : []);
        return tenantDirectory.items;
    } catch (err) {
        if (window.__pm_shared && typeof window.__pm_shared.warn === 'function') {
            window.__pm_shared.warn('ensureTenantDirectory failed', err);
        }
        return tenantDirectory.items;
    }
}

function updateTenantDirectory(list) {
    if (!Array.isArray(list)) {
        return;
    }
    tenantDirectory.items = list.slice();
    tenantDirectory.byId = new Map();
    tenantDirectory.items.forEach(tenant => {
        const id = normalizeTenantId(tenant);
        if (id) {
            tenantDirectory.byId.set(id, tenant);
        }
    });
    tenantDirectory.lastFetched = Date.now();
    window._tenants = tenantDirectory.items;
    syncTenantFilterOptions('agents');
    syncTenantFilterOptions('devices');
    applyAgentFilters();
    applyDeviceFilters(true);
}

function syncTenantFilterOptions(scope) {
    const selectId = scope === 'agents' ? 'agents_tenant_filter' : 'devices_tenant_filter';
    const select = document.getElementById(selectId);
    if (!select) return;
    const filterValue = scope === 'agents' ? agentsVM.filters.tenantId : devicesVM.filters.tenantId;
    const options = ['<option value="">All Tenants</option>'];
    let hasMatch = false;
    const sorted = tenantDirectory.items.slice().sort((a, b) => {
        const aName = (a && (a.name || normalizeTenantId(a) || '')).toLowerCase();
        const bName = (b && (b.name || normalizeTenantId(b) || '')).toLowerCase();
        if (aName < bName) return -1;
        if (aName > bName) return 1;
        return 0;
    });
    sorted.forEach(tenant => {
        const id = normalizeTenantId(tenant);
        if (!id) return;
        const label = tenant.name || tenant.display_name || id;
        const selected = filterValue && id === filterValue ? ' selected' : '';
        if (selected) {
            hasMatch = true;
        }
        options.push(`<option value="${escapeHtml(id)}"${selected}>${escapeHtml(label)}</option>`);
    });
    if (filterValue && !hasMatch) {
        options.push(`<option value="${escapeHtml(filterValue)}" selected>${escapeHtml(filterValue)}</option>`);
    }
    select.innerHTML = options.join('');
    select.value = filterValue || '';
}

function createStatusCountMap() {
    const map = {};
    DEVICE_STATUS_KEYS.forEach(key => {
        map[key] = 0;
    });
    return map;
}

// Show printer details modal by finding the device in the cached list first, then falling back to API
async function showPrinterDetails(ipOrSerial, source) {
    if (!ipOrSerial) return;
    source = source || 'saved';
    let device = null;
    if (devicesVM.items && devicesVM.items.length > 0) {
        device = devicesVM.items.find(d => d.ip === ipOrSerial || d.serial === ipOrSerial);
    }
    if (!device) {
        try {
            const res = await fetch('/api/v1/devices/index');
            if (!res.ok) throw new Error('Failed to fetch devices');
            const devices = await res.json();
            if (Array.isArray(devices)) {
                device = devices.find(d => (d.ip && d.ip === ipOrSerial) || (d.serial && d.serial === ipOrSerial));
            }
        } catch (err) {
            window.__pm_shared.error('Fallback device fetch failed', err);
        }
    }
    if (!device) {
        window.__pm_shared.showToast('Device not found', 'error');
        return;
    }
    try {
        const epoch = devicesLoader?.getGeneration();
        const rows = await fetchDeviceInventory('rows', [device.serial], {});
        const metrics = await fetchDeviceInventory('metrics/query', [device.serial], {});
        if (epoch !== devicesLoader?.getGeneration()) return;
        if (!rows.length) throw new Error('Device no longer available');
        device = { ...rows[0], toner_levels: metrics[0]?.toner_levels || {}, raw_data: { ...(rows[0].raw_data || {}), ...metrics[0] } };
    } catch (error) {
        window.__pm_shared.showToast('Device details unavailable: ' + error.message, 'error');
        return;
    }
    const normalized = device.printer_info ? { ...device.printer_info, serial: device.serial || device.printer_info.serial } : device;
    window.__pm_shared_cards.showPrinterDetailsData(normalized, source, null);
}

// ====== Utility Functions ======
function copyToClipboard(text) {
    if (!text) return;

    navigator.clipboard.writeText(text).then(() => {
        window.__pm_shared.showToast('Copied to clipboard', 'success', 1500);
    }).catch(err => {
        window.__pm_shared.error('Failed to copy:', err);
    });
}

// ====== Proxy Functions ======
function openAgentUI(agentId) {
    // Open agent's web UI through WebSocket proxy in a new window
    // Ensure agentId is URL-encoded to avoid embedding spaces or unsafe chars
    const proxyUrl = `/api/v1/proxy/agent/${encodeURIComponent(agentId)}/`;
    window.open(proxyUrl, `agent-ui-${encodeURIComponent(agentId)}`, 'width=1200,height=800');
}

function openDeviceUI(serialNumber) {
    // Open device's web UI through WebSocket proxy in a new window
    const proxyUrl = `/api/v1/proxy/device/${encodeURIComponent(serialNumber)}/`;
    window.open(proxyUrl, `device-ui-${encodeURIComponent(serialNumber)}`, 'width=1200,height=800');
}

// Open the shared metrics modal for a device
function openDeviceMetrics(serial) {
    if (!serial) return;
    if (typeof window.showMetricsModal === 'function') {
        window.showMetricsModal({ serial });
    } else {
        // Fallback: navigate to devices list or show a toast
        window.__pm_shared.showToast('Metrics UI not available', 'error');
    }
}

// ====== Managed Settings UI ======
const SETTINGS_SECTION_LABELS = {
    discovery: 'Discovery',
    snmp: 'SNMP',
    features: 'Features',
    spooler: 'Local Printer Tracking',
    logging: 'Logging',
    web: 'Web Server'
};
const SETTINGS_SECTION_ORDER = ['discovery', 'snmp', 'features', 'spooler', 'logging', 'web'];

// Subsection groupings for discovery section
// Fields are grouped in order - any field not listed goes to "Other"
const DISCOVERY_SUBSECTIONS = [
    {
        key: 'ip_scanning',
        label: 'IP Scanning',
        fields: ['discovery.ip_scanning_enabled', 'discovery.subnet_scan', 'discovery.manual_ranges', 'discovery.ranges_text', 'discovery.concurrency']
    },
    {
        key: 'probe_methods',
        label: 'Probe Methods',
        fields: ['discovery.arp_enabled', 'discovery.icmp_enabled', 'discovery.tcp_enabled', 'discovery.snmp_enabled', 'discovery.mdns_enabled']
    },
    {
        key: 'auto_discovery',
        label: 'Automatic Discovery',
        fields: ['discovery.auto_discover_enabled', 'discovery.autosave_discovered_devices', 'discovery.show_discover_button_anyway', 'discovery.show_discovered_devices_anyway']
    },
    {
        key: 'passive_listeners',
        label: 'Passive Listeners',
        fields: ['discovery.passive_discovery_enabled', 'discovery.auto_discover_live_mdns', 'discovery.auto_discover_live_wsd', 'discovery.auto_discover_live_ssdp', 'discovery.auto_discover_live_snmptrap', 'discovery.auto_discover_live_llmnr']
    },
    {
        key: 'metrics',
        label: 'Metrics Collection',
        fields: ['discovery.metrics_rescan_enabled', 'discovery.metrics_rescan_interval_minutes']
    }
];

const DEFAULT_UPDATE_POLICY_SPEC = {
    update_check_days: 7,
    version_pin_strategy: 'minor',
    allow_major_upgrade: false,
    target_version: '',
    collect_telemetry: true,
    maintenance_window: {
        enabled: false,
        timezone: 'UTC',
        start_hour: 0,
        start_min: 0,
        end_hour: 6,
        end_min: 0,
        days_of_week: []
    },
    rollout_control: {
        staggered: true,
        max_concurrent: 0,
        batch_size: 0,
        delay_between_waves: 300,
        jitter_seconds: 60,
        emergency_abort: true
    }
};

const POLICY_VERSION_PIN_OPTIONS = [
    { value: 'major', label: 'Major (stay on v0.x)' },
    { value: 'minor', label: 'Minor (stay on v0.9.x)' },
    { value: 'patch', label: 'Patch (stay on v0.9.14)' }
];

const POLICY_DAYS_OF_WEEK = [
    { value: 0, label: 'Sun' },
    { value: 1, label: 'Mon' },
    { value: 2, label: 'Tue' },
    { value: 3, label: 'Wed' },
    { value: 4, label: 'Thu' },
    { value: 5, label: 'Fri' },
    { value: 6, label: 'Sat' }
];

const settingsUIState = {
    initialized: false,
    loading: false,
    loadingPromise: null,
    scope: 'global',
    schema: null,
    groupedFields: {},
    globalSnapshot: null,
    globalDraft: null,
    globalDirty: false,
    globalSettingsDirty: false,
    // Managed sections control (which categories are server-managed)
    managedSections: new Set(['discovery', 'snmp', 'features', 'spooler']),
    originalManagedSections: new Set(['discovery', 'snmp', 'features', 'spooler']),
    managedSectionsDirty: false,
    tenantList: [],
    selectedTenantId: '',
    tenantSnapshot: null,
    tenantDraft: null,
    tenantOverridesDraft: {},
    tenantEnforcedSections: new Set(),
    originalTenantEnforcedSections: new Set(),
    tenantEnforcedSectionsDirty: false,
    tenantDirty: false,
    tenantSettingsDirty: false,

    agentList: [],
    selectedAgentId: '',
    agentSnapshot: null,
    agentBaseSnapshot: null,
    agentDraft: null,
    agentOverridesDraft: {},
    agentEnforcedSections: new Set(),
    agentDirty: false,
    agentSettingsDirty: false,

    saving: false,
    eventsBound: false,
    lockedKeys: new Set(), // Keys locked by environment variables
    updatePolicy: {
        global: createPolicyState(),
        tenant: createPolicyState()
    }
};

function resolveTenantId(record) {
    if (!record) return '';
    return record.id || record.uuid || record.tenant_id || '';
}

function normalizeTenantList(list) {
    if (!Array.isArray(list)) return [];
    const normalized = [];
    list.forEach(item => {
        const id = resolveTenantId(item);
        if (!id) {
            return;
        }
        normalized.push({ ...item, id });
    });
    return normalized;
}

function resolveAgentId(record) {
    if (!record) return '';
    return record.agent_id || record.agentId || record.id || '';
}

function normalizeAgentList(list) {
    if (!Array.isArray(list)) return [];
    const normalized = [];
    list.forEach(item => {
        const id = resolveAgentId(item);
        if (!id) {
            return;
        }
        normalized.push({ ...item, id });
    });
    return normalized;
}

function createPolicyState() {
    return {
        policy: clonePolicySpec(DEFAULT_UPDATE_POLICY_SPEC),
        originalPolicy: clonePolicySpec(DEFAULT_UPDATE_POLICY_SPEC),
        enabled: false,
        originalEnabled: false,
        dirty: false,
        loaded: false
    };
}

function clonePolicySpec(spec) {
    return JSON.parse(JSON.stringify(spec || DEFAULT_UPDATE_POLICY_SPEC));
}

function normalizePolicySpec(spec) {
    const normalized = clonePolicySpec(DEFAULT_UPDATE_POLICY_SPEC);
    if (!spec || typeof spec !== 'object') {
        return normalized;
    }
    if (Number.isFinite(Number(spec.update_check_days))) {
        normalized.update_check_days = Number(spec.update_check_days);
    }
    if (typeof spec.version_pin_strategy === 'string') {
        const value = spec.version_pin_strategy.toLowerCase();
        normalized.version_pin_strategy = POLICY_VERSION_PIN_OPTIONS.some(opt => opt.value === value) ? value : normalized.version_pin_strategy;
    }
    if (typeof spec.allow_major_upgrade === 'boolean') {
        normalized.allow_major_upgrade = spec.allow_major_upgrade;
    }
    if (typeof spec.target_version === 'string') {
        normalized.target_version = spec.target_version;
    }
    if (typeof spec.collect_telemetry === 'boolean') {
        normalized.collect_telemetry = spec.collect_telemetry;
    }
    if (spec.maintenance_window && typeof spec.maintenance_window === 'object') {
        const mw = spec.maintenance_window;
        if (typeof mw.enabled === 'boolean') normalized.maintenance_window.enabled = mw.enabled;
        if (typeof mw.timezone === 'string') normalized.maintenance_window.timezone = mw.timezone;
        if (Number.isFinite(Number(mw.start_hour))) normalized.maintenance_window.start_hour = Number(mw.start_hour);
        if (Number.isFinite(Number(mw.start_min))) normalized.maintenance_window.start_min = Number(mw.start_min);
        if (Number.isFinite(Number(mw.end_hour))) normalized.maintenance_window.end_hour = Number(mw.end_hour);
        if (Number.isFinite(Number(mw.end_min))) normalized.maintenance_window.end_min = Number(mw.end_min);
        if (Array.isArray(mw.days_of_week)) {
            normalized.maintenance_window.days_of_week = normalizePolicyDays(mw.days_of_week);
        }
    }
    if (spec.rollout_control && typeof spec.rollout_control === 'object') {
        const rc = spec.rollout_control;
        if (typeof rc.staggered === 'boolean') normalized.rollout_control.staggered = rc.staggered;
        if (Number.isFinite(Number(rc.max_concurrent))) normalized.rollout_control.max_concurrent = Number(rc.max_concurrent);
        if (Number.isFinite(Number(rc.batch_size))) normalized.rollout_control.batch_size = Number(rc.batch_size);
        if (Number.isFinite(Number(rc.delay_between_waves))) normalized.rollout_control.delay_between_waves = Number(rc.delay_between_waves);
        if (Number.isFinite(Number(rc.jitter_seconds))) normalized.rollout_control.jitter_seconds = Number(rc.jitter_seconds);
        if (typeof rc.emergency_abort === 'boolean') normalized.rollout_control.emergency_abort = rc.emergency_abort;
    }
    return normalized;
}

function normalizePolicyDays(days) {
    if (!Array.isArray(days)) {
        return [];
    }
    const normalized = Array.from(new Set(days.map(val => Number(val)).filter(val => Number.isFinite(val) && val >= 0 && val <= 6))).sort((a, b) => a - b);
    return normalized;
}

function getPolicyState(scope) {
    if (!settingsUIState.updatePolicy) {
        settingsUIState.updatePolicy = { global: createPolicyState(), tenant: createPolicyState() };
    }
    return settingsUIState.updatePolicy[scope] || null;
}

function applyPolicySnapshot(scope, enabled, policySpec) {
    const state = getPolicyState(scope);
    if (!state) return;
    state.policy = clonePolicySpec(policySpec);
    state.originalPolicy = clonePolicySpec(policySpec);
    state.enabled = !!enabled;
    state.originalEnabled = !!enabled;
    state.dirty = false;
    state.loaded = true;
    syncSettingsDirtyFlags();
}

function recomputePolicyDirty(scope) {
    const state = getPolicyState(scope);
    if (!state) return;
    const policyChanged = state.enabled && !deepEqual(state.policy, state.originalPolicy);
    const enabledChanged = state.enabled !== state.originalEnabled;
    state.dirty = policyChanged || enabledChanged;
    syncSettingsDirtyFlags();
}

function resetPolicyDraft(scope) {
    const state = getPolicyState(scope);
    if (!state) return;
    state.policy = clonePolicySpec(state.originalPolicy);
    state.enabled = state.originalEnabled;
    state.dirty = false;
    syncSettingsDirtyFlags();
}

function syncSettingsDirtyFlags() {
    const policy = settingsUIState.updatePolicy || {};
    const globalPolicyDirty = policy.global ? policy.global.dirty : false;
    const tenantPolicyDirty = policy.tenant ? policy.tenant.dirty : false;
    // Include managedSectionsDirty in globalDirty check
    settingsUIState.globalDirty = !!(settingsUIState.globalSettingsDirty || globalPolicyDirty || settingsUIState.managedSectionsDirty);
    settingsUIState.tenantDirty = !!(settingsUIState.tenantSettingsDirty || settingsUIState.tenantEnforcedSectionsDirty || tenantPolicyDirty);
    settingsUIState.agentDirty = !!(settingsUIState.agentSettingsDirty);
}

function getSettingsPayload(record) {
    if (!record) return {};
    return record.settings || record.Settings || {};
}

function getOverridesPayload(record) {
    if (!record) return {};
    return record.overrides || record.Overrides || {};
}

function getUpdatedAt(record) {
    if (!record) return null;
    return record.updated_at || record.updatedAt || record.UpdatedAt || null;
}

function getUpdatedBy(record) {
    if (!record) return '';
    return record.updated_by || record.updatedBy || record.UpdatedBy || '';
}

function getOverridesUpdatedAt(record) {
    if (!record) return null;
    return record.overrides_updated_at || record.overridesUpdatedAt || record.OverridesUpdatedAt || null;
}

function getOverridesUpdatedBy(record) {
    if (!record) return '';
    return record.overrides_updated_by || record.overridesUpdatedBy || record.OverridesUpdatedBy || '';
}

function resolveFieldValue(field, value) {
    if (value === undefined || value === null) {
        if (field && Object.prototype.hasOwnProperty.call(field, 'default')) {
            return field.default;
        }
    }
    return value;
}

function updateSettingsTenantDirectory(rawList) {
    const normalized = normalizeTenantList(rawList);
    const previousSelection = settingsUIState.selectedTenantId;
    settingsUIState.tenantList = normalized;
    const selectionStillValid = previousSelection && normalized.some(t => t.id === previousSelection);
    if (!selectionStillValid) {
        settingsUIState.selectedTenantId = normalized.length ? normalized[0].id : '';
    }
    if (!settingsUIState.selectedTenantId) {
        settingsUIState.tenantSnapshot = null;
        settingsUIState.tenantDraft = null;
        settingsUIState.tenantOverridesDraft = {};
        settingsUIState.tenantSettingsDirty = false;
        applyPolicySnapshot('tenant', false, DEFAULT_UPDATE_POLICY_SPEC);
        syncSettingsDirtyFlags();
    }
    if (!settingsUIState.initialized) {
        return;
    }
    if (settingsUIState.scope === 'tenant' && !selectionStillValid && settingsUIState.selectedTenantId) {
        loadTenantSnapshot(settingsUIState.selectedTenantId)
            .then(() => renderSettingsUI())
            .catch(err => reportSettingsError('Failed to refresh tenant overrides', err));
        return;
    }
    renderSettingsUI();
}

function notifyManagedSettingsTenantDirectory(list) {
    updateSettingsTenantDirectory(list);
}

async function initSettingsUI() {
    const panel = document.getElementById('managed_settings_panel');
    if (!panel) return;
    if (settingsUIState.loading) {
        return settingsUIState.loadingPromise;
    }
    if (settingsUIState.initialized) {
        // For tenant-scoped users, always start on tenant scope (not global)
        if (isTenantScopedUser() && settingsUIState.scope === 'global') {
            settingsUIState.scope = 'tenant';
        }
        renderSettingsUI();
        return;
    }
    settingsUIState.loading = true;
    settingsUIState.loadingPromise = (async () => {
        try {
            // For tenant-scoped users, default to tenant scope
            if (isTenantScopedUser()) {
                settingsUIState.scope = 'tenant';
            }
            await bootstrapSettingsUI();
            settingsUIState.initialized = true;
            renderSettingsUI();
        } catch (err) {
            renderSettingsError(err);
        } finally {
            settingsUIState.loading = false;
        }
    })();
    return settingsUIState.loadingPromise;
}

async function bootstrapSettingsUI() {
    await loadSettingsSchema();
    await loadSettingsSources(); // Fetch locked keys
    await loadGlobalSettingsSnapshot();
    await loadGlobalUpdatePolicy();
    await loadTenantDirectory();
    await loadAgentDirectoryForSettings();
    if (settingsUIState.tenantList.length > 0) {
        settingsUIState.selectedTenantId = settingsUIState.tenantList[0].id;
        if (settingsUIState.selectedTenantId) {
            await loadTenantSnapshot(settingsUIState.selectedTenantId);
        }
    }
}

async function loadSettingsSchema() {
    try {
        const schema = await fetchJSON('/api/v1/settings/schema');
        settingsUIState.schema = schema;
        settingsUIState.groupedFields = groupSchemaFields(schema && Array.isArray(schema.fields) ? schema.fields : []);
    } catch (err) {
        if (err && err.status === 404) {
            throw new Error('Managed settings are disabled on this server build. Enable tenancy/features to use this tab.');
        }
        throw err;
    }
}

async function loadSettingsSources() {
    try {
        const sources = await fetchJSON('/api/v1/server/settings/sources');
        settingsUIState.lockedKeys = new Set(sources.locked_keys || []);
        settingsUIState.effectiveValues = sources.effective_values || {};
    } catch (err) {
        // If endpoint doesn't exist or errors, assume no locks
        settingsUIState.lockedKeys = new Set();
        settingsUIState.effectiveValues = {};
    }
}

function groupSchemaFields(fields) {
    const groups = {};
    fields.forEach(field => {
        if (!field || !field.path) return;
        const scope = (field.scope || '').toLowerCase();
        if (scope === 'agent') {
            return;
        }
        const section = field.path.split('.')[0];
        if (!groups[section]) {
            groups[section] = [];
        }
        groups[section].push(field);
    });
    return groups;
}

function orderedSettingsSections() {
    const sections = [];
    const seen = new Set();
    SETTINGS_SECTION_ORDER.forEach(sectionKey => {
        const group = settingsUIState.groupedFields[sectionKey];
        if (group && group.length) {
            sections.push(sectionKey);
            seen.add(sectionKey);
        }
    });
    Object.keys(settingsUIState.groupedFields).sort().forEach(sectionKey => {
        const group = settingsUIState.groupedFields[sectionKey];
        if (!seen.has(sectionKey) && group && group.length) {
            sections.push(sectionKey);
        }
    });
    return sections;
}

async function loadGlobalSettingsSnapshot() {
    const snapshot = await fetchJSON('/api/v1/settings/global');
    settingsUIState.globalSnapshot = snapshot;
    settingsUIState.globalDraft = cloneSettings(getSettingsPayload(snapshot));
    settingsUIState.globalSettingsDirty = false;
    // Sync managed sections from snapshot
    const managedArr = (snapshot && Array.isArray(snapshot.managed_sections))
        ? snapshot.managed_sections
        : ['discovery', 'snmp', 'features', 'spooler'];
    settingsUIState.managedSections = new Set(managedArr);
    settingsUIState.originalManagedSections = new Set(managedArr);
    settingsUIState.managedSectionsDirty = false;
    syncSettingsDirtyFlags();
}

async function loadGlobalUpdatePolicy() {
    const state = getPolicyState('global');
    if (!state) return;
    try {
        const resp = await fetchJSON('/api/v1/update-policies/global');
        const policy = resp && resp.policy ? normalizePolicySpec(resp.policy) : clonePolicySpec(DEFAULT_UPDATE_POLICY_SPEC);
        applyPolicySnapshot('global', true, policy);
    } catch (err) {
        if (err && err.status === 404) {
            applyPolicySnapshot('global', false, DEFAULT_UPDATE_POLICY_SPEC);
            return;
        }
        throw err;
    }
}

async function loadTenantDirectory() {
    try {
        // For tenant-scoped users, they can't access /api/v1/tenants
        // Instead, use their tenant_ids from auth and fetch individual tenant details
        if (isTenantScopedUser()) {
            const userTenantIds = getUserTenantIds();
            if (userTenantIds.length === 0) {
                settingsUIState.tenantList = [];
                return;
            }
            // Build tenant list from user's allowed tenants
            // We only need id and name for the dropdown
            const tenantList = [];
            for (const tid of userTenantIds) {
                try {
                    // Try to fetch tenant details - may not work for all tenant-scoped users
                    const tenant = await fetchJSON(`/api/v1/tenants/${tid}`);
                    tenantList.push(tenant);
                } catch (fetchErr) {
                    // If we can't fetch details, create a basic entry with just the ID
                    tenantList.push({ id: tid, name: tid });
                }
            }
            updateSettingsTenantDirectory(tenantList);
            return;
        }

        const tenants = await fetchJSON('/api/v1/tenants');
        updateSettingsTenantDirectory(tenants);
    } catch (err) {
        if (err && (err.status === 403 || err.status === 404)) {
            // For 403, try to use user's tenant_ids as fallback
            if (isTenantScopedUser()) {
                const userTenantIds = getUserTenantIds();
                const fallbackList = userTenantIds.map(tid => ({ id: tid, name: tid }));
                updateSettingsTenantDirectory(fallbackList);
                return;
            }
            settingsUIState.tenantList = [];
            return;
        }
        throw err;
    }
}

async function loadAgentDirectoryForSettings() {
    try {
        const agents = await fetchJSON('/api/v1/agents/list');
        const normalized = normalizeAgentList(Array.isArray(agents) ? agents : []);
        const previousSelection = settingsUIState.selectedAgentId;
        settingsUIState.agentList = normalized;
        const selectionStillValid = previousSelection && normalized.some(a => a.id === previousSelection);
        if (!selectionStillValid) {
            settingsUIState.selectedAgentId = normalized.length ? normalized[0].id : '';
        }
        if (!settingsUIState.selectedAgentId) {
            settingsUIState.agentSnapshot = null;
            settingsUIState.agentBaseSnapshot = null;
            settingsUIState.agentDraft = null;
            settingsUIState.agentOverridesDraft = {};
            settingsUIState.agentEnforcedSections = new Set();
            settingsUIState.agentSettingsDirty = false;
            syncSettingsDirtyFlags();
        }
    } catch (err) {
        if (err && (err.status === 403 || err.status === 404)) {
            settingsUIState.agentList = [];
            return;
        }
        throw err;
    }
}

async function loadTenantSnapshot(tenantId) {
    if (!tenantId) {
        settingsUIState.tenantSnapshot = null;
        settingsUIState.tenantDraft = null;
        settingsUIState.tenantOverridesDraft = {};
        settingsUIState.tenantSettingsDirty = false;
        settingsUIState.tenantEnforcedSections = new Set();
        settingsUIState.originalTenantEnforcedSections = new Set();
        settingsUIState.tenantEnforcedSectionsDirty = false;
        if (settingsUIState.updatePolicy && settingsUIState.updatePolicy.tenant) {
            settingsUIState.updatePolicy.tenant = createPolicyState();
        }
        syncSettingsDirtyFlags();
        return;
    }
    const snapshot = await fetchJSON(`/api/v1/settings/tenants/${encodeURIComponent(tenantId)}`);
    settingsUIState.tenantSnapshot = snapshot;
    const baseline = getSettingsPayload(settingsUIState.globalSnapshot);
    const tenantSettings = snapshot ? getSettingsPayload(snapshot) : baseline;
    settingsUIState.tenantDraft = cloneSettings(Object.keys(tenantSettings).length ? tenantSettings : baseline);
    settingsUIState.tenantOverridesDraft = cloneSettings(getOverridesPayload(snapshot));
    const enforcedArr = (snapshot && Array.isArray(snapshot.enforced_sections)) ? snapshot.enforced_sections : [];
    settingsUIState.tenantEnforcedSections = new Set(enforcedArr);
    settingsUIState.originalTenantEnforcedSections = new Set(enforcedArr);
    settingsUIState.tenantEnforcedSectionsDirty = false;
    settingsUIState.tenantSettingsDirty = false;
    syncSettingsDirtyFlags();
    await loadTenantUpdatePolicy(tenantId);
}

async function loadAgentSnapshot(agentId) {
    if (!agentId) {
        settingsUIState.agentSnapshot = null;
        settingsUIState.agentBaseSnapshot = null;
        settingsUIState.agentDraft = null;
        settingsUIState.agentOverridesDraft = {};
        settingsUIState.agentEnforcedSections = new Set();
        settingsUIState.agentSettingsDirty = false;
        syncSettingsDirtyFlags();
        return;
    }

    const snapshot = await fetchJSON(`/api/v1/settings/agents/${encodeURIComponent(agentId)}`);
    settingsUIState.agentSnapshot = snapshot;
    settingsUIState.agentOverridesDraft = cloneSettings(getOverridesPayload(snapshot));

    const tenantId = (snapshot && (snapshot.tenant_id || snapshot.tenantId)) ? (snapshot.tenant_id || snapshot.tenantId) : '';
    const enforcedArr = (snapshot && Array.isArray(snapshot.enforced_sections)) ? snapshot.enforced_sections : [];
    settingsUIState.agentEnforcedSections = new Set(enforcedArr);

    // Base snapshot is the resolved tenant snapshot (no agent overrides), or global when unassigned.
    if (tenantId) {
        try {
            settingsUIState.agentBaseSnapshot = await fetchJSON(`/api/v1/settings/tenants/${encodeURIComponent(tenantId)}`);
        } catch (err) {
            settingsUIState.agentBaseSnapshot = settingsUIState.globalSnapshot;
        }
    } else {
        settingsUIState.agentBaseSnapshot = settingsUIState.globalSnapshot;
    }

    const baseSettings = getSettingsPayload(settingsUIState.agentBaseSnapshot) || {};
    const effectiveSettings = snapshot ? getSettingsPayload(snapshot) : baseSettings;
    settingsUIState.agentDraft = cloneSettings(Object.keys(effectiveSettings).length ? effectiveSettings : baseSettings);
    settingsUIState.agentSettingsDirty = false;
    syncSettingsDirtyFlags();
}

async function loadTenantUpdatePolicy(tenantId) {
    const state = getPolicyState('tenant');
    if (!state) return;
    if (!tenantId) {
        applyPolicySnapshot('tenant', false, DEFAULT_UPDATE_POLICY_SPEC);
        return;
    }
    try {
        const resp = await fetchJSON(`/api/v1/update-policies/${encodeURIComponent(tenantId)}`);
        const policy = resp && resp.policy ? normalizePolicySpec(resp.policy) : clonePolicySpec(DEFAULT_UPDATE_POLICY_SPEC);
        applyPolicySnapshot('tenant', true, policy);
    } catch (err) {
        if (err && err.status === 404) {
            applyPolicySnapshot('tenant', false, DEFAULT_UPDATE_POLICY_SPEC);
            return;
        }
        throw err;
    }
}

function renderSettingsUI() {
    bindSettingsEvents();
    renderScopeButtons();
    updateTenantSelect();
    updateAgentSelect();
    renderSettingsForm();
    renderOverrideSummary();
    updateActionButtons();
    updateLastUpdatedMeta();
}

function renderSettingsError(err) {
    const root = document.getElementById('settings_form_root');
    if (root) {
        let message = 'Managed settings are unavailable.';
        if (err) {
            if (err.status === 403) {
                message = 'You do not have permission to view managed settings.';
            } else if (err.status === 404) {
                message = 'Managed settings are disabled on this server build.';
            } else if (err.message) {
                message = err.message;
            }
        }
        root.innerHTML = `<div class="error-text">${escapeHtml(message)}</div>`;
        window.__pm_shared.showToast(message, 'error', 5000);
    }
    const actions = document.querySelector('.settings-actions');
    if (actions) {
        actions.style.display = 'none';
    }
}

function bindSettingsEvents() {
    if (settingsUIState.eventsBound) return;
    const formRoot = document.getElementById('settings_form_root');
    if (formRoot) {
        formRoot.addEventListener('input', handleSettingsFieldChange);
        formRoot.addEventListener('change', handleSettingsFieldChange);
        formRoot.addEventListener('click', handleSettingsFieldClick);
        formRoot.addEventListener('input', handlePolicyFieldChange);
        formRoot.addEventListener('change', handlePolicyFieldChange);
    }
    const saveBtn = document.getElementById('settings_save_btn');
    if (saveBtn) saveBtn.addEventListener('click', handleSettingsSave);
    const discardBtn = document.getElementById('settings_discard_btn');
    if (discardBtn) discardBtn.addEventListener('click', handleDiscardChanges);
    const resetBtn = document.getElementById('settings_reset_overrides_btn');
    if (resetBtn) resetBtn.addEventListener('click', resetTenantOverrides);

    const resetAgentBtn = document.getElementById('settings_reset_agent_overrides_btn');
    if (resetAgentBtn) resetAgentBtn.addEventListener('click', resetAgentOverrides);

    document.querySelectorAll('.settings-scope-btn').forEach(btn => {
        btn.addEventListener('click', () => handleSettingsScopeChange(btn.dataset.scope));
    });
    const tenantSelect = document.getElementById('settings_tenant_select');
    if (tenantSelect) tenantSelect.addEventListener('change', handleTenantSelect);

    const agentSelect = document.getElementById('settings_agent_select');
    if (agentSelect) agentSelect.addEventListener('change', handleAgentSelect);
    settingsUIState.eventsBound = true;
}

function renderScopeButtons() {
    const tenantScoped = isTenantScopedUser();
    document.querySelectorAll('.settings-scope-btn').forEach(btn => {
        const scope = btn.dataset.scope || 'global';

        // Hide Global scope button for tenant-scoped users
        if (scope === 'global' && tenantScoped) {
            btn.style.display = 'none';
            return;
        }
        btn.style.display = '';

        // Update button text for tenant-scoped users
        if (scope === 'tenant' && tenantScoped) {
            btn.textContent = 'Defaults';
        }

        btn.classList.toggle('active', scope === settingsUIState.scope);
    });
}

function updateTenantSelect() {
    const select = document.getElementById('settings_tenant_select');
    if (!select) return;
    select.innerHTML = '';
    if (!settingsUIState.tenantList.length) {
        const opt = document.createElement('option');
        opt.value = '';
        opt.textContent = 'No tenants available';
        select.appendChild(opt);
        select.disabled = true;
        return;
    }
    select.disabled = false;
    settingsUIState.tenantList.forEach(tenant => {
        const opt = document.createElement('option');
        const tenantId = resolveTenantId(tenant);
        opt.value = tenantId;
        opt.textContent = tenant.name || tenantId;
        if (tenantId === settingsUIState.selectedTenantId) {
            opt.selected = true;
        }
        select.appendChild(opt);
    });
}

function updateAgentSelect() {
    const select = document.getElementById('settings_agent_select');
    if (!select) return;
    select.innerHTML = '';
    if (!settingsUIState.agentList.length) {
        const opt = document.createElement('option');
        opt.value = '';
        opt.textContent = 'No agents available';
        select.appendChild(opt);
        select.disabled = true;
        return;
    }
    select.disabled = false;
    settingsUIState.agentList.forEach(agent => {
        const opt = document.createElement('option');
        const agentId = resolveAgentId(agent);
        const label = getAgentDisplayName(agent, agentId);
        opt.value = agentId;
        opt.textContent = label;
        if (agentId === settingsUIState.selectedAgentId) {
            opt.selected = true;
        }
        select.appendChild(opt);
    });
}

function renderSettingsForm() {
    const root = document.getElementById('settings_form_root');
    if (!root) return;
    if (!settingsUIState.schema || !settingsUIState.globalDraft) {
        root.innerHTML = '<div class="muted-text">Managed settings are initializing…</div>';
        return;
    }
    const scope = settingsUIState.scope;
    let draft;
    if (scope === 'global') {
        draft = settingsUIState.globalDraft;
    } else if (scope === 'tenant') {
        draft = settingsUIState.tenantDraft || settingsUIState.globalDraft;
    } else {
        const base = getSettingsPayload(settingsUIState.agentBaseSnapshot || settingsUIState.globalSnapshot) || {};
        draft = settingsUIState.agentDraft || cloneSettings(Object.keys(base).length ? base : settingsUIState.globalDraft);
    }
    root.innerHTML = '';

    // Add section management controls at top when in global scope
    if (scope === 'global') {
        const controlPanel = renderManagedSectionsPanel();
        if (controlPanel) {
            root.appendChild(controlPanel);
        }
    } else if (scope === 'tenant') {
        const enforcementPanel = renderTenantEnforcementPanel();
        if (enforcementPanel) {
            root.appendChild(enforcementPanel);
        }
    }

    orderedSettingsSections().forEach(sectionKey => {
        const fields = settingsUIState.groupedFields[sectionKey];
        if (!fields || !fields.length) {
            return;
        }
        // Check if this section is managed (only relevant for global scope)
        const isSectionManaged = settingsUIState.managedSections.has(sectionKey);
        const isSectionEnforced = scope === 'agent' && settingsUIState.agentEnforcedSections && settingsUIState.agentEnforcedSections.has(sectionKey);

        const sectionEl = document.createElement('div');
        sectionEl.className = 'settings-section-panel';
        if (scope === 'global' && !isSectionManaged) {
            sectionEl.classList.add('section-disabled');
        }
        if (scope === 'agent' && (!isSectionManaged || isSectionEnforced)) {
            sectionEl.classList.add('section-disabled');
        }
        const header = document.createElement('div');
        header.className = 'settings-section-header';
        let managedBadge = '';
        if ((scope === 'global' || scope === 'agent') && !isSectionManaged) {
            managedBadge = '<span class="section-status-badge agent-controlled">Agent Controlled</span>';
        } else if (scope === 'agent' && isSectionEnforced) {
            managedBadge = '<span class="section-status-badge agent-controlled">Tenant Enforced</span>';
        }
        header.innerHTML = `<h4>${escapeHtml(SETTINGS_SECTION_LABELS[sectionKey] || sectionKey)}</h4>${managedBadge}`;
        sectionEl.appendChild(header);

        const list = document.createElement('div');
        list.className = 'settings-field-list';

        // Use subsections for discovery, otherwise render flat list
        if (sectionKey === 'discovery') {
            renderDiscoveryWithSubsections(list, fields, draft, scope, isSectionManaged, isSectionEnforced);
        } else {
            fields.forEach(field => {
                const value = getValueByPath(draft, field.path);
                const row = renderSettingsFieldRow(field, value, scope, isSectionManaged, isSectionEnforced);
                if (row) {
                    list.appendChild(row);
                }
            });
        }
        sectionEl.appendChild(list);
        root.appendChild(sectionEl);
    });
    if (!root.children.length) {
        root.innerHTML = '<div class="muted-text">No server-managed settings are available in this build.</div>';
    }
    if (scope === 'global' || scope === 'tenant') {
        refreshPolicyPanel();
    }
}

/**
 * Render the managed sections control panel
 */
function renderManagedSectionsPanel() {
    const panel = document.createElement('div');
    panel.className = 'managed-sections-panel';
    panel.innerHTML = `
        <div class="managed-sections-header">
            <h4>Section Management</h4>
            <span class="managed-sections-hint">Control which settings categories are centrally managed vs agent-controlled</span>
        </div>
        <div class="managed-sections-toggles">
            ${renderManagedSectionToggle('discovery', 'Discovery', 'IP scanning, probe methods, and auto-discovery behavior')}
            ${renderManagedSectionToggle('snmp', 'SNMP', 'Community strings and SNMP protocol settings')}
            ${renderManagedSectionToggle('features', 'Features', 'Feature flags and optional capabilities')}
            ${renderManagedSectionToggle('spooler', 'Local Printers', 'USB/local printer tracking via OS spooler')}
        </div>
    `;
    // Bind toggle events
    panel.querySelectorAll('.managed-section-toggle input').forEach(input => {
        input.addEventListener('change', (e) => {
            handleManagedSectionToggle(e.target.dataset.section, e.target.checked);
        });
    });
    return panel;
}

function renderTenantEnforcementPanel() {
    const panel = document.createElement('div');
    panel.className = 'managed-sections-panel';
    const canEdit = userCan('settings.fleet.write');
    const hasTenant = !!settingsUIState.selectedTenantId;
    panel.innerHTML = `
        <div class="managed-sections-header">
            <h4>Agent Override Locks</h4>
            <span class="managed-sections-hint">Lock a category so agents in this tenant cannot override it.</span>
        </div>
        <div class="managed-sections-toggles">
            ${renderTenantEnforcementToggle('discovery', 'Discovery', 'Prevent per-agent changes to discovery behavior')}
            ${renderTenantEnforcementToggle('snmp', 'SNMP', 'Prevent per-agent changes to SNMP settings')}
            ${renderTenantEnforcementToggle('features', 'Features', 'Prevent per-agent changes to feature flags')}
            ${renderTenantEnforcementToggle('spooler', 'Local Printers', 'Prevent per-agent changes to spooler settings')}
        </div>
    `;
    panel.querySelectorAll('.tenant-enforcement-toggle input').forEach(input => {
        input.addEventListener('change', (e) => {
            handleTenantEnforcementToggle(e.target.dataset.section, e.target.checked);
        });
        input.disabled = !canEdit || !hasTenant;
    });
    if (!hasTenant) {
        panel.classList.add('section-disabled');
    }
    return panel;
}

function renderTenantEnforcementToggle(sectionKey, label, description) {
    const isEnforced = settingsUIState.tenantEnforcedSections && settingsUIState.tenantEnforcedSections.has(sectionKey);
    return `
        <label class="managed-section-toggle tenant-enforcement-toggle ${isEnforced ? 'active' : ''}">
            <div class="toggle-content">
                <span class="toggle-label">${escapeHtml(label)}</span>
                <span class="toggle-description">${escapeHtml(description)}</span>
            </div>
            <div class="toggle-switch">
                <input type="checkbox" data-section="${sectionKey}" ${isEnforced ? 'checked' : ''}>
                <span class="toggle-slider"></span>
            </div>
        </label>
    `;
}

function handleTenantEnforcementToggle(sectionKey, isEnforced) {
    if (!settingsUIState.selectedTenantId) {
        return;
    }
    if (!settingsUIState.tenantEnforcedSections) {
        settingsUIState.tenantEnforcedSections = new Set();
    }
    if (isEnforced) {
        settingsUIState.tenantEnforcedSections.add(sectionKey);
    } else {
        settingsUIState.tenantEnforcedSections.delete(sectionKey);
    }
    const originalSet = settingsUIState.originalTenantEnforcedSections || new Set();
    const currentSet = settingsUIState.tenantEnforcedSections;
    const changed = originalSet.size !== currentSet.size ||
        [...originalSet].some(s => !currentSet.has(s)) ||
        [...currentSet].some(s => !originalSet.has(s));
    settingsUIState.tenantEnforcedSectionsDirty = changed;
    syncSettingsDirtyFlags();
    renderOverrideSummary();
    updateActionButtons();
}

function renderManagedSectionToggle(sectionKey, label, description) {
    const isManaged = settingsUIState.managedSections.has(sectionKey);
    const canEdit = userCan('settings.fleet.write');
    return `
        <label class="managed-section-toggle ${isManaged ? 'active' : ''}">
            <div class="toggle-content">
                <span class="toggle-label">${escapeHtml(label)}</span>
                <span class="toggle-description">${escapeHtml(description)}</span>
            </div>
            <div class="toggle-switch">
                <input type="checkbox" data-section="${sectionKey}" ${isManaged ? 'checked' : ''} ${canEdit ? '' : 'disabled'}>
                <span class="toggle-slider"></span>
            </div>
        </label>
    `;
}

function handleManagedSectionToggle(sectionKey, isManaged) {
    if (isManaged) {
        settingsUIState.managedSections.add(sectionKey);
    } else {
        settingsUIState.managedSections.delete(sectionKey);
    }
    // Check if managed sections changed from original
    const originalSet = settingsUIState.originalManagedSections;
    const currentSet = settingsUIState.managedSections;
    const changed = originalSet.size !== currentSet.size ||
        [...originalSet].some(s => !currentSet.has(s)) ||
        [...currentSet].some(s => !originalSet.has(s));
    settingsUIState.managedSectionsDirty = changed;
    syncSettingsDirtyFlags();
    // Re-render to update section disabled states
    renderSettingsForm();
    updateActionButtons();
}

/**
 * Render discovery fields organized into logical subsections
 */
function renderDiscoveryWithSubsections(container, fields, draft, scope, isSectionManaged = true, isSectionEnforced = false) {
    const fieldMap = {};
    fields.forEach(f => { fieldMap[f.path] = f; });
    const rendered = new Set();

    DISCOVERY_SUBSECTIONS.forEach((subsection, idx) => {
        const subsectionFields = subsection.fields
            .map(path => fieldMap[path])
            .filter(f => f && !rendered.has(f.path));

        if (!subsectionFields.length) return;

        // Add subsection header
        const subHeader = document.createElement('div');
        subHeader.className = 'settings-subsection-header';
        subHeader.textContent = subsection.label;
        container.appendChild(subHeader);

        // Add fields in this subsection
        subsectionFields.forEach(field => {
            rendered.add(field.path);
            const value = getValueByPath(draft, field.path);
            const row = renderSettingsFieldRow(field, value, scope, isSectionManaged, isSectionEnforced);
            if (row) {
                container.appendChild(row);
            }
        });
    });

    // Render any remaining fields not in a subsection
    const remaining = fields.filter(f => !rendered.has(f.path));
    if (remaining.length) {
        const subHeader = document.createElement('div');
        subHeader.className = 'settings-subsection-header';
        subHeader.textContent = 'Other';
        container.appendChild(subHeader);

        remaining.forEach(field => {
            const value = getValueByPath(draft, field.path);
            const row = renderSettingsFieldRow(field, value, scope, isSectionManaged, isSectionEnforced);
            if (row) {
                container.appendChild(row);
            }
        });
    }

    // After rendering, update visibility of dependent fields
    updateDependentFieldVisibility(container);
}

/**
 * Show/hide fields that depend on another field's boolean value.
 * Fields with data-depends-on are hidden when the dependency field is unchecked.
 */
function updateDependentFieldVisibility(container) {
    if (!container) container = document.getElementById('settings_form_root');
    if (!container) return;

    const dependentRows = container.querySelectorAll('[data-depends-on]');
    dependentRows.forEach(row => {
        const dependsOnPath = row.dataset.dependsOn;
        // Find the input for the dependency field
        const depInput = container.querySelector(`input[data-settings-path="${dependsOnPath}"]`);
        if (depInput && depInput.type === 'checkbox') {
            row.style.display = depInput.checked ? '' : 'none';
        }
    });
}

function renderSettingsFieldRow(field, value, scope, isSectionManaged = true, isSectionEnforced = false) {
    const row = document.createElement('div');
    row.className = 'settings-field-row';
    row.dataset.fieldType = (field.type || 'text').toLowerCase();
    row.dataset.settingsPath = field.path;

    // Field dependencies: show/hide based on another field's value
    const FIELD_DEPENDENCIES = {
        'discovery.ranges_text': 'discovery.manual_ranges'
    };
    if (FIELD_DEPENDENCIES[field.path]) {
        row.dataset.dependsOn = FIELD_DEPENDENCIES[field.path];
    }

    // Check if this field is locked by environment variable
    const isLocked = settingsUIState.lockedKeys.has(field.path);
    // Section not managed means fields are read-only indicators
    const sectionNotManaged = (scope === 'global' || scope === 'agent') && !isSectionManaged;
    const sectionTenantEnforced = scope === 'agent' && !!isSectionEnforced;

    // For locked fields, use the effective runtime value instead of DB value
    let displayValue = value;
    if (isLocked && settingsUIState.effectiveValues && settingsUIState.effectiveValues.hasOwnProperty(field.path)) {
        displayValue = settingsUIState.effectiveValues[field.path];
    }

    const label = document.createElement('div');
    label.className = 'settings-field-label';
    label.innerHTML = `
        <div class="field-title">${escapeHtml(field.title || field.path)}</div>
        <div class="field-description">${escapeHtml(field.description || '')}</div>
    `;

    const control = document.createElement('div');
    control.className = 'settings-field-control';
    const inputFragment = createInputForField(field, displayValue);
    if (!inputFragment || !inputFragment.input || !inputFragment.element) {
        return null;
    }
    const { input, element } = inputFragment;
    const canEdit = userCan('settings.fleet.write');
    // Disable input if user can't edit, no tenant selected (for tenant scope), locked by env, OR section not managed
    input.disabled = !canEdit ||
        (scope === 'tenant' && !settingsUIState.selectedTenantId) ||
        (scope === 'agent' && !settingsUIState.selectedAgentId) ||
        isLocked ||
        sectionNotManaged ||
        sectionTenantEnforced;
    control.appendChild(element);

    // Show lock badge if locked by environment variable
    if (isLocked) {
        const lockBadge = document.createElement('span');
        lockBadge.className = 'settings-badge locked';
        lockBadge.textContent = '🔒 ENV';
        lockBadge.title = 'This setting is set by an environment variable and cannot be changed through managed settings';
        control.appendChild(lockBadge);
    }

    if (scope === 'tenant' || scope === 'agent') {
        const isOverride = hasOverride(pathToArray(field.path));
        const badge = document.createElement('span');
        badge.className = `settings-badge ${isOverride ? 'override' : 'inherited'}`;
        badge.textContent = isOverride ? 'Override' : 'Inherited';
        control.appendChild(badge);
        if (isOverride && canEdit && !isLocked && !sectionNotManaged && !sectionTenantEnforced) {
            const inheritBtn = document.createElement('button');
            inheritBtn.type = 'button';
            inheritBtn.className = 'ghost-btn inherit-btn';
            inheritBtn.textContent = 'Inherit';
            inheritBtn.dataset.inheritPath = field.path;
            control.appendChild(inheritBtn);
        }
    }

    row.appendChild(label);
    row.appendChild(control);
    return row;
}

function refreshPolicyPanel() {
    const root = document.getElementById('settings_form_root');
    if (!root) return;
    const existing = document.getElementById('auto_update_policy_section');
    if (existing) {
        existing.remove();
    }
    renderUpdatePolicySection(root);
}

function renderUpdatePolicySection(root) {
    if (!root) return;
    const panel = document.createElement('div');
    panel.className = 'settings-section-panel auto-update-policy';
    panel.id = 'auto_update_policy_section';

    const header = document.createElement('div');
    header.className = 'settings-section-header';
    header.innerHTML = `<h4>Auto-Update Policy</h4><p>Control how often agents check for updates, which versions they target, and how rollouts are staged.</p>`;
    panel.appendChild(header);

    const body = document.createElement('div');
    body.className = 'settings-field-list auto-update-field-list';
    const scope = settingsUIState.scope;
    const policyState = getPolicyState(scope);
    const canEdit = userCan('settings.fleet.write');

    if (scope === 'tenant' && !settingsUIState.selectedTenantId) {
        body.innerHTML = '<div class="muted-text">Select a customer to manage auto-update overrides.</div>';
        panel.appendChild(body);
        root.appendChild(panel);
        return;
    }

    if (!policyState || !policyState.loaded) {
        body.innerHTML = '<div class="muted-text">Loading auto-update policy…</div>';
        panel.appendChild(body);
        root.appendChild(panel);
        return;
    }

    const toggleRow = document.createElement('div');
    toggleRow.className = 'settings-field-row';
    const toggleLabel = document.createElement('div');
    toggleLabel.className = 'settings-field-label';
    const toggleTitle = scope === 'global' ? 'Enforce auto-update policy' : 'Override global policy';
    const toggleDescription = scope === 'global'
        ? 'Applies to every tenant unless a specific override is configured.'
        : 'Only configure when this customer needs a different cadence than the global defaults.';
    toggleLabel.innerHTML = `<div class="field-title">${escapeHtml(toggleTitle)}</div><div class="field-description">${escapeHtml(toggleDescription)}</div>`;
    const toggleControl = document.createElement('div');
    toggleControl.className = 'settings-field-control';
    const toggle = document.createElement('label');
    toggle.className = 'mini-toggle-container settings-toggle';
    const toggleInput = document.createElement('input');
    toggleInput.type = 'checkbox';
    toggleInput.checked = !!policyState.enabled;
    toggleInput.disabled = !canEdit;
    toggleInput.dataset.policyToggle = 'enabled';
    toggleInput.dataset.policyScope = scope;
    const toggleState = document.createElement('span');
    toggleState.className = 'settings-toggle-state';
    toggleState.textContent = policyState.enabled ? 'Enabled' : 'Disabled';
    toggleInput.addEventListener('change', () => {
        toggleState.textContent = toggleInput.checked ? 'Enabled' : 'Disabled';
    });
    toggle.appendChild(toggleInput);
    toggle.appendChild(toggleState);
    toggleControl.appendChild(toggle);
    toggleRow.appendChild(toggleLabel);
    toggleRow.appendChild(toggleControl);
    body.appendChild(toggleRow);

    if (!policyState.enabled) {
        const inheritMsg = document.createElement('div');
        inheritMsg.className = 'muted-text';
        inheritMsg.textContent = scope === 'global'
            ? 'No global auto-update policy is currently enforced. Agents will rely on their local override settings.'
            : 'This customer currently inherits the global auto-update policy.';
        body.appendChild(inheritMsg);
        panel.appendChild(body);
        root.appendChild(panel);
        return;
    }

    appendPolicyInputs(body, scope, policyState, canEdit);
    panel.appendChild(body);
    root.appendChild(panel);
}

function appendPolicyInputs(container, scope, policyState, canEdit) {
    const policy = policyState.policy || DEFAULT_UPDATE_POLICY_SPEC;
    const disabled = !canEdit || !policyState.enabled;

    container.appendChild(buildPolicyRow('Check cadence (days)', 'Set to 0 to pause unattended update checks.',
        createPolicyNumberInput(scope, 'update_check_days', policy.update_check_days, disabled, 0, 365)));

    container.appendChild(buildPolicyRow('Version pin strategy', 'Controls whether agents stay on major, minor, or patch lines.',
        createPolicySelectInput(scope, 'version_pin_strategy', policy.version_pin_strategy, disabled, POLICY_VERSION_PIN_OPTIONS)));

    container.appendChild(buildPolicyRow('Allow major upgrades', 'When disabled, agents will not cross major version boundaries unless forced manually.',
        createPolicyCheckboxInput(scope, 'allow_major_upgrade', policy.allow_major_upgrade, disabled)));

    container.appendChild(buildPolicyRow('Target version (optional)', 'Provide an exact semantic version to pin the fleet. Leave blank to follow the latest allowed version.',
        createPolicyTextInput(scope, 'target_version', policy.target_version, disabled)));

    container.appendChild(buildPolicyRow('Collect telemetry during rollout', 'Allows the server to gather anonymized update metrics for dashboards.',
        createPolicyCheckboxInput(scope, 'collect_telemetry', policy.collect_telemetry, disabled)));

    container.appendChild(buildPolicySubheader('Maintenance Window'));
    const mwDisabled = disabled;
    container.appendChild(buildPolicyRow('Window enabled', 'Restrict updates to a specific time window in the tenant\'s timezone.',
        createPolicyCheckboxInput(scope, 'maintenance_window.enabled', policy.maintenance_window.enabled, mwDisabled)));

    const maintenanceInputsDisabled = mwDisabled || !policy.maintenance_window.enabled;
    container.appendChild(buildPolicyRow('Timezone', 'IANA timezone such as UTC or America/New_York.',
        createPolicyTextInput(scope, 'maintenance_window.timezone', policy.maintenance_window.timezone, maintenanceInputsDisabled)));

    const startWrapper = document.createElement('div');
    startWrapper.className = 'policy-inline-inputs';
    startWrapper.appendChild(createPolicyNumberInput(scope, 'maintenance_window.start_hour', policy.maintenance_window.start_hour, maintenanceInputsDisabled, 0, 23));
    startWrapper.appendChild(document.createTextNode(' : '));
    startWrapper.appendChild(createPolicyNumberInput(scope, 'maintenance_window.start_min', policy.maintenance_window.start_min, maintenanceInputsDisabled, 0, 59));
    container.appendChild(buildPolicyRow('Start time (HH:MM)', '24-hour format.', startWrapper));

    const endWrapper = document.createElement('div');
    endWrapper.className = 'policy-inline-inputs';
    endWrapper.appendChild(createPolicyNumberInput(scope, 'maintenance_window.end_hour', policy.maintenance_window.end_hour, maintenanceInputsDisabled, 0, 23));
    endWrapper.appendChild(document.createTextNode(' : '));
    endWrapper.appendChild(createPolicyNumberInput(scope, 'maintenance_window.end_min', policy.maintenance_window.end_min, maintenanceInputsDisabled, 0, 59));
    container.appendChild(buildPolicyRow('End time (HH:MM)', '24-hour format.', endWrapper));

    container.appendChild(buildPolicyRow('Days of week', 'Select one or more days for maintenance.',
        createPolicyDaysControl(scope, policy.maintenance_window.days_of_week, maintenanceInputsDisabled)));

    container.appendChild(buildPolicySubheader('Rollout Control'));
    const rolloutDisabled = disabled;
    container.appendChild(buildPolicyRow('Staggered rollout', 'Disabling pushes updates to all agents simultaneously.',
        createPolicyCheckboxInput(scope, 'rollout_control.staggered', policy.rollout_control.staggered, rolloutDisabled)));
    container.appendChild(buildPolicyRow('Max concurrent agents', 'Limit the number of agents updating at the same time (0 = auto).',
        createPolicyNumberInput(scope, 'rollout_control.max_concurrent', policy.rollout_control.max_concurrent, rolloutDisabled, 0, 10000)));
    container.appendChild(buildPolicyRow('Batch size', 'Number of agents per wave when staggering.',
        createPolicyNumberInput(scope, 'rollout_control.batch_size', policy.rollout_control.batch_size, rolloutDisabled, 0, 10000)));
    container.appendChild(buildPolicyRow('Delay between waves (seconds)', 'Pause between staggered batches.',
        createPolicyNumberInput(scope, 'rollout_control.delay_between_waves', policy.rollout_control.delay_between_waves, rolloutDisabled, 0, 86400)));
    container.appendChild(buildPolicyRow('Jitter (seconds)', 'Randomized delay added to reduce thundering herds.',
        createPolicyNumberInput(scope, 'rollout_control.jitter_seconds', policy.rollout_control.jitter_seconds, rolloutDisabled, 0, 3600)));
    container.appendChild(buildPolicyRow('Emergency abort available', 'Allow admins to stop an in-flight rollout from the UI.',
        createPolicyCheckboxInput(scope, 'rollout_control.emergency_abort', policy.rollout_control.emergency_abort, rolloutDisabled)));
}

function buildPolicyRow(label, description, controlElement) {
    const row = document.createElement('div');
    row.className = 'settings-field-row';
    const labelEl = document.createElement('div');
    labelEl.className = 'settings-field-label';
    labelEl.innerHTML = `<div class="field-title">${escapeHtml(label)}</div><div class="field-description">${escapeHtml(description || '')}</div>`;
    const control = document.createElement('div');
    control.className = 'settings-field-control';
    control.appendChild(controlElement);
    row.appendChild(labelEl);
    row.appendChild(control);
    return row;
}

function buildPolicySubheader(title) {
    const divider = document.createElement('div');
    divider.className = 'policy-subheader';
    divider.textContent = title;
    return divider;
}

function createPolicyNumberInput(scope, path, value, disabled, min, max) {
    const input = document.createElement('input');
    input.type = 'number';
    input.value = value === null || value === undefined ? '' : value;
    if (min !== undefined) input.min = min;
    if (max !== undefined) input.max = max;
    input.dataset.policyPath = path;
    input.dataset.policyType = 'number';
    input.dataset.policyScope = scope;
    input.disabled = !!disabled;
    return input;
}

function createPolicyTextInput(scope, path, value, disabled) {
    const input = document.createElement('input');
    input.type = 'text';
    input.value = value === null || value === undefined ? '' : value;
    input.dataset.policyPath = path;
    input.dataset.policyType = 'text';
    input.dataset.policyScope = scope;
    input.disabled = !!disabled;
    return input;
}

function createPolicySelectInput(scope, path, value, disabled, options) {
    const select = document.createElement('select');
    (options || []).forEach(option => {
        const opt = document.createElement('option');
        opt.value = option.value;
        opt.textContent = option.label;
        if (option.value === value) {
            opt.selected = true;
        }
        select.appendChild(opt);
    });
    select.dataset.policyPath = path;
    select.dataset.policyType = 'text';
    select.dataset.policyScope = scope;
    select.disabled = !!disabled;
    return select;
}

function createPolicyCheckboxInput(scope, path, checked, disabled) {
    const toggle = document.createElement('label');
    toggle.className = 'mini-toggle-container settings-toggle';
    const input = document.createElement('input');
    input.type = 'checkbox';
    input.checked = !!checked;
    input.disabled = !!disabled;
    input.dataset.policyPath = path;
    input.dataset.policyType = 'bool';
    input.dataset.policyScope = scope;
    const state = document.createElement('span');
    state.className = 'settings-toggle-state';
    state.textContent = checked ? 'Enabled' : 'Disabled';
    input.addEventListener('change', () => {
        state.textContent = input.checked ? 'Enabled' : 'Disabled';
    });
    toggle.appendChild(input);
    toggle.appendChild(state);
    return toggle;
}

function createPolicyDaysControl(scope, selectedDays, disabled) {
    const wrapper = document.createElement('div');
    wrapper.className = 'policy-days-container';
    wrapper.classList.toggle('disabled', !!disabled);
    const daySet = new Set(Array.isArray(selectedDays) ? selectedDays : []);
    POLICY_DAYS_OF_WEEK.forEach(day => {
        const chip = document.createElement('label');
        chip.className = 'policy-day-chip';
        const input = document.createElement('input');
        input.type = 'checkbox';
        input.checked = daySet.has(day.value);
        input.disabled = !!disabled;
        input.dataset.policyScope = scope;
        input.dataset.policyDay = String(day.value);
        chip.appendChild(input);
        const text = document.createElement('span');
        text.textContent = day.label;
        chip.appendChild(text);
        syncPolicyDayChipState(input);
        wrapper.appendChild(chip);
    });
    return wrapper;
}

function syncPolicyDayChipState(input) {
    if (!input) return;
    const chip = input.closest('.policy-day-chip');
    if (!chip) return;
    chip.classList.toggle('selected', !!input.checked);
    chip.classList.toggle('disabled', input.disabled);
}

function handlePolicyFieldChange(event) {
    const target = event.target;
    if (!target || !target.dataset) {
        return;
    }
    if (target.dataset.policyToggle) {
        handlePolicyToggleChange(target);
        return;
    }
    if (Object.prototype.hasOwnProperty.call(target.dataset, 'policyDay')) {
        handlePolicyDayToggle(target);
        return;
    }
    if (!target.dataset.policyPath) {
        return;
    }
    const scope = resolvePolicyScope(target.dataset.policyScope);
    const state = getPolicyState(scope);
    if (!state || !state.policy) {
        return;
    }
    const type = target.dataset.policyType || target.type || 'text';
    const value = readInputValue(target, type);
    setNestedValue(state.policy, target.dataset.policyPath, value);
    recomputePolicyDirty(scope);
    updateActionButtons();
    if (target.dataset.policyPath === 'maintenance_window.enabled') {
        refreshPolicyPanel();
    }
}

function handlePolicyToggleChange(input) {
    const scope = resolvePolicyScope(input.dataset.policyScope);
    const state = getPolicyState(scope);
    if (!state) return;
    state.enabled = !!input.checked;
    recomputePolicyDirty(scope);
    updateActionButtons();
    refreshPolicyPanel();
}

function handlePolicyDayToggle(input) {
    const scope = resolvePolicyScope(input.dataset.policyScope);
    const state = getPolicyState(scope);
    if (!state) return;
    const rawValue = Number(input.dataset.policyDay);
    if (!Number.isFinite(rawValue)) {
        return;
    }
    const current = getValueByPath(state.policy, 'maintenance_window.days_of_week');
    const next = new Set(Array.isArray(current) ? current : []);
    if (input.checked) {
        next.add(rawValue);
    } else {
        next.delete(rawValue);
    }
    setNestedValue(state.policy, 'maintenance_window.days_of_week', Array.from(next).sort((a, b) => a - b));
    recomputePolicyDirty(scope);
    updateActionButtons();
    syncPolicyDayChipState(input);
}

function resolvePolicyScope(scopeHint) {
    if (scopeHint === 'global' || scopeHint === 'tenant') {
        return scopeHint;
    }
    return settingsUIState.scope === 'tenant' ? 'tenant' : 'global';
}

function createInputForField(field, value) {
    const type = (field.type || 'text').toLowerCase();
    const resolvedValue = resolveFieldValue(field, value);
    let input;
    let element;
    if (type === 'bool') {
        input = document.createElement('input');
        input.type = 'checkbox';
        input.checked = !!resolvedValue;
        const toggle = document.createElement('label');
        toggle.className = 'mini-toggle-container settings-toggle';
        toggle.title = field.title || field.path;
        const state = document.createElement('span');
        state.className = 'settings-toggle-state';
        state.textContent = input.checked ? 'Enabled' : 'Disabled';
        input.addEventListener('change', () => {
            state.textContent = input.checked ? 'Enabled' : 'Disabled';
        });
        toggle.appendChild(input);
        toggle.appendChild(state);
        element = toggle;
    } else if (type === 'number') {
        input = document.createElement('input');
        input.type = 'number';
        input.value = resolvedValue === null || resolvedValue === undefined ? '' : resolvedValue;
        if (field.min !== undefined) input.min = field.min;
        if (field.max !== undefined) input.max = field.max;
        element = input;
    } else if (type === 'select' && Array.isArray(field.enum)) {
        input = document.createElement('select');
        field.enum.forEach(optionValue => {
            const opt = document.createElement('option');
            opt.value = optionValue;
            opt.textContent = field.path === 'features.agent_update_channel' && optionValue === '' ? 'Use Agent configuration' : optionValue;
            if (optionValue === resolvedValue) {
                opt.selected = true;
            }
            input.appendChild(opt);
        });
        element = input;
    } else if (type === 'textarea') {
        input = document.createElement('textarea');
        input.value = resolvedValue === null || resolvedValue === undefined ? '' : resolvedValue;
        input.rows = 4;
        input.className = 'settings-textarea';
        input.placeholder = field.description || '';
        element = input;
    } else {
        input = document.createElement('input');
        input.type = 'text';
        input.value = resolvedValue === null || resolvedValue === undefined ? '' : resolvedValue;
        element = input;
    }
    input.dataset.settingsPath = field.path;
    input.dataset.fieldType = (field.type || 'text').toLowerCase();
    return { input, element };
}

function handleSettingsFieldChange(event) {
    const target = event.target;
    if (!target || !target.dataset || !target.dataset.settingsPath) {
        return;
    }
    const path = target.dataset.settingsPath;
    const fieldType = target.dataset.fieldType || 'text';
    const newValue = readInputValue(target, fieldType);
    if (settingsUIState.scope === 'global') {
        updateGlobalDraft(path, newValue);
    } else if (settingsUIState.scope === 'tenant') {
        updateTenantDraft(path, newValue);
    } else {
        updateAgentDraft(path, newValue);
    }

    // Update visibility of fields that depend on this checkbox
    if (fieldType === 'bool') {
        updateDependentFieldVisibility();
    }
}

function handleSettingsFieldClick(event) {
    const target = event.target;
    if (target && target.dataset && target.dataset.inheritPath) {
        event.preventDefault();
        if (settingsUIState.scope === 'tenant') {
            clearTenantOverride(target.dataset.inheritPath);
        } else if (settingsUIState.scope === 'agent') {
            clearAgentOverride(target.dataset.inheritPath);
        }
    }
}

function readInputValue(input, fieldType) {
    switch (fieldType) {
        case 'bool':
            return !!input.checked;
        case 'number':
            return input.value === '' ? null : Number(input.value);
        default:
            return input.value;
    }
}

function updateGlobalDraft(path, value) {
    setNestedValue(settingsUIState.globalDraft, path, value);
    const baseline = getSettingsPayload(settingsUIState.globalSnapshot);
    settingsUIState.globalSettingsDirty = !deepEqual(settingsUIState.globalDraft, baseline);
    syncSettingsDirtyFlags();
    updateActionButtons();
}

function updateTenantDraft(path, value) {
    if (!settingsUIState.tenantDraft) {
        settingsUIState.tenantDraft = cloneSettings(settingsUIState.globalDraft);
    }
    setNestedValue(settingsUIState.tenantDraft, path, value);
    const baseValue = getValueByPath(getSettingsPayload(settingsUIState.globalSnapshot), path);
    if (valuesEqual(value, baseValue)) {
        deleteNestedValue(settingsUIState.tenantOverridesDraft, path);
    } else {
        setNestedValue(settingsUIState.tenantOverridesDraft, path, value);
    }
    const originalOverrides = getOverridesPayload(settingsUIState.tenantSnapshot);
    settingsUIState.tenantSettingsDirty = !deepEqual(settingsUIState.tenantOverridesDraft, originalOverrides);
    renderOverrideSummary();
    syncSettingsDirtyFlags();
    updateActionButtons();
}

function updateAgentDraft(path, value) {
    const section = String(path || '').split('.')[0];
    if (settingsUIState.agentEnforcedSections && settingsUIState.agentEnforcedSections.has(section)) {
        return;
    }
    if (!settingsUIState.agentDraft) {
        const baseSettings = getSettingsPayload(settingsUIState.agentBaseSnapshot) || {};
        settingsUIState.agentDraft = cloneSettings(baseSettings);
    }
    setNestedValue(settingsUIState.agentDraft, path, value);
    const baseValue = getValueByPath(getSettingsPayload(settingsUIState.agentBaseSnapshot), path);
    if (valuesEqual(value, baseValue)) {
        deleteNestedValue(settingsUIState.agentOverridesDraft, path);
    } else {
        setNestedValue(settingsUIState.agentOverridesDraft, path, value);
    }
    const originalOverrides = getOverridesPayload(settingsUIState.agentSnapshot);
    settingsUIState.agentSettingsDirty = !deepEqual(settingsUIState.agentOverridesDraft, originalOverrides);
    renderOverrideSummary();
    syncSettingsDirtyFlags();
    updateActionButtons();
}

function clearTenantOverride(path) {
    if (!settingsUIState.tenantDraft) return;
    deleteNestedValue(settingsUIState.tenantOverridesDraft, path);
    const baseValue = getValueByPath(getSettingsPayload(settingsUIState.globalSnapshot), path);
    setNestedValue(settingsUIState.tenantDraft, path, baseValue);
    const originalOverrides = getOverridesPayload(settingsUIState.tenantSnapshot);
    settingsUIState.tenantSettingsDirty = !deepEqual(settingsUIState.tenantOverridesDraft, originalOverrides);
    renderSettingsForm();
    renderOverrideSummary();
    updateActionButtons();
}

function clearAgentOverride(path) {
    if (!settingsUIState.agentDraft) return;
    deleteNestedValue(settingsUIState.agentOverridesDraft, path);
    const baseValue = getValueByPath(getSettingsPayload(settingsUIState.agentBaseSnapshot), path);
    setNestedValue(settingsUIState.agentDraft, path, baseValue);
    const originalOverrides = getOverridesPayload(settingsUIState.agentSnapshot);
    settingsUIState.agentSettingsDirty = !deepEqual(settingsUIState.agentOverridesDraft, originalOverrides);
    syncSettingsDirtyFlags();
    renderSettingsForm();
    renderOverrideSummary();
    updateActionButtons();
}

function handleSettingsScopeChange(scope) {
    if (!scope || scope === settingsUIState.scope) {
        return;
    }

    // Prevent tenant-scoped users from accessing global scope
    if (scope === 'global' && isTenantScopedUser()) {
        return;
    }

    settingsUIState.scope = scope;
    renderSettingsUI();

    if (scope === 'tenant' && settingsUIState.selectedTenantId && !settingsUIState.tenantSnapshot) {
        loadTenantSnapshot(settingsUIState.selectedTenantId).then(() => {
            renderSettingsUI();
        }).catch(err => {
            reportSettingsError('Failed to load tenant settings', err);
        });
    }

    if (scope === 'agent' && settingsUIState.selectedAgentId && !settingsUIState.agentSnapshot) {
        loadAgentSnapshot(settingsUIState.selectedAgentId).then(() => {
            renderSettingsUI();
        }).catch(err => {
            reportSettingsError('Failed to load agent overrides', err);
        });
    }
}

function handleTenantSelect(event) {
    const tenantId = event.target.value;
    settingsUIState.selectedTenantId = tenantId;
    loadTenantSnapshot(tenantId).then(() => {
        renderSettingsUI();
    }).catch(err => {
        reportSettingsError('Failed to load tenant settings', err);
    });
}

function handleAgentSelect(event) {
    const agentId = event.target.value;
    settingsUIState.selectedAgentId = agentId;
    loadAgentSnapshot(agentId).then(() => {
        renderSettingsUI();
    }).catch(err => {
        reportSettingsError('Failed to load agent overrides', err);
    });
}

async function handleSettingsSave(event) {
    event.preventDefault();
    if (!userCan('settings.fleet.write')) {
        window.__pm_shared.showToast('You do not have permission to update settings', 'error');
        return;
    }
    settingsUIState.saving = true;
    updateActionButtons();
    try {
        if (settingsUIState.scope === 'global') {
            await saveGlobalSettings();
        } else if (settingsUIState.scope === 'tenant') {
            await saveTenantSettings();
        } else {
            await saveAgentSettings();
        }
    } catch (err) {
        reportSettingsError('Failed to save settings', err);
    } finally {
        settingsUIState.saving = false;
        updateActionButtons();
    }
}

async function saveGlobalSettings() {
    if (!settingsUIState.globalDirty) {
        return;
    }
    const pending = [];
    const settingsChanged = !!settingsUIState.globalSettingsDirty;
    const managedSectionsChanged = !!settingsUIState.managedSectionsDirty;
    const policyState = getPolicyState('global');
    const policyChanged = !!(policyState && policyState.dirty);

    // If settings or managed sections changed, save both together
    if (settingsChanged || managedSectionsChanged) {
        const payload = {
            ...settingsUIState.globalDraft,
            managed_sections: Array.from(settingsUIState.managedSections)
        };
        pending.push(fetchJSON('/api/v1/settings/global', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        }));
    }
    if (policyChanged) {
        pending.push(savePolicyChanges('global'));
    }
    if (!pending.length) {
        return;
    }
    await Promise.all(pending);
    if (settingsChanged || managedSectionsChanged) {
        await loadGlobalSettingsSnapshot();
        if (settingsUIState.selectedTenantId) {
            await loadTenantSnapshot(settingsUIState.selectedTenantId);
        }
    }
    if (policyChanged) {
        await loadGlobalUpdatePolicy();
    }
    renderSettingsUI();
    window.__pm_shared.showToast('Global settings saved', 'success');
}

async function saveTenantSettings() {
    if (!settingsUIState.selectedTenantId) {
        window.__pm_shared.showToast('Select a tenant to edit overrides', 'error');
        return;
    }
    const tenantId = settingsUIState.selectedTenantId;
    if (!settingsUIState.tenantDirty) {
        return;
    }
    const pending = [];
    const settingsChanged = !!settingsUIState.tenantSettingsDirty;
    const enforcementChanged = !!settingsUIState.tenantEnforcedSectionsDirty;
    const policyState = getPolicyState('tenant');
    const policyChanged = !!(policyState && policyState.dirty);
    if (settingsChanged || enforcementChanged) {
        const overrides = cloneSettings(settingsUIState.tenantOverridesDraft);
        const enforced_sections = Array.from(settingsUIState.tenantEnforcedSections || []);
        const hasOverrides = flattenOverrides(overrides).length > 0;
        const hasEnforcement = enforced_sections.length > 0;
        if (!hasOverrides && !hasEnforcement) {
            pending.push(fetchJSON(`/api/v1/settings/tenants/${encodeURIComponent(tenantId)}`, { method: 'DELETE' }));
        } else {
            pending.push(fetchJSON(`/api/v1/settings/tenants/${encodeURIComponent(tenantId)}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ overrides, enforced_sections })
            }));
        }
    }
    if (policyChanged) {
        pending.push(savePolicyChanges('tenant', tenantId));
    }
    if (!pending.length) {
        return;
    }
    await Promise.all(pending);
    await loadTenantSnapshot(tenantId);
    renderSettingsUI();
    window.__pm_shared.showToast('Tenant configuration saved', 'success');
}

async function saveAgentSettings() {
    if (!settingsUIState.selectedAgentId) {
        window.__pm_shared.showToast('Select an agent to edit overrides', 'error');
        return;
    }
    const agentId = settingsUIState.selectedAgentId;
    if (!settingsUIState.agentDirty) {
        return;
    }
    const overrides = cloneSettings(settingsUIState.agentOverridesDraft);
    (settingsUIState.agentEnforcedSections || new Set()).forEach(section => {
        delete overrides[section];
    });
    const hasOverrides = flattenOverrides(overrides).length > 0;
    if (!hasOverrides) {
        try {
            await fetchJSON(`/api/v1/settings/agents/${encodeURIComponent(agentId)}`, { method: 'DELETE' });
        } catch (err) {
            if (!err || err.status !== 404) {
                throw err;
            }
        }
    } else {
        await fetchJSON(`/api/v1/settings/agents/${encodeURIComponent(agentId)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(overrides)
        });
    }
    await loadAgentSnapshot(agentId);
    renderSettingsUI();
    window.__pm_shared.showToast('Agent overrides saved', 'success');
}

async function savePolicyChanges(scope, tenantId) {
    const state = getPolicyState(scope);
    if (!state || !state.dirty) {
        return;
    }
    let endpoint = '/api/v1/update-policies/global';
    if (scope === 'tenant') {
        if (!tenantId) {
            throw new Error('Tenant ID is required to save tenant policy overrides');
        }
        endpoint = `/api/v1/update-policies/${encodeURIComponent(tenantId)}`;
    }
    if (!state.enabled) {
        try {
            await fetchJSON(endpoint, { method: 'DELETE' });
        } catch (err) {
            if (!err || err.status !== 404) {
                throw err;
            }
        }
        return;
    }
    await fetchJSON(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ policy: clonePolicySpec(state.policy) })
    });
}

function handleDiscardChanges(event) {
    event.preventDefault();
    if (settingsUIState.scope === 'global') {
        settingsUIState.globalDraft = cloneSettings(getSettingsPayload(settingsUIState.globalSnapshot));
        settingsUIState.globalSettingsDirty = false;
        resetPolicyDraft('global');
    } else if (settingsUIState.scope === 'tenant') {
        const tenantSettings = settingsUIState.tenantSnapshot
            ? getSettingsPayload(settingsUIState.tenantSnapshot)
            : getSettingsPayload(settingsUIState.globalSnapshot);
        settingsUIState.tenantDraft = cloneSettings(tenantSettings);
        settingsUIState.tenantOverridesDraft = cloneSettings(getOverridesPayload(settingsUIState.tenantSnapshot));
        settingsUIState.tenantSettingsDirty = false;
        settingsUIState.tenantEnforcedSections = new Set(settingsUIState.originalTenantEnforcedSections || []);
        settingsUIState.tenantEnforcedSectionsDirty = false;
        resetPolicyDraft('tenant');
    } else {
        const agentSettings = settingsUIState.agentSnapshot
            ? getSettingsPayload(settingsUIState.agentSnapshot)
            : getSettingsPayload(settingsUIState.agentBaseSnapshot);
        settingsUIState.agentDraft = cloneSettings(agentSettings);
        settingsUIState.agentOverridesDraft = cloneSettings(getOverridesPayload(settingsUIState.agentSnapshot));
        settingsUIState.agentSettingsDirty = false;
    }
    syncSettingsDirtyFlags();
    renderSettingsUI();
}

async function resetAgentOverrides(event) {
    event.preventDefault();
    if (!settingsUIState.selectedAgentId) {
        return;
    }
    if (!confirm('Clear all overrides for this agent?')) {
        return;
    }
    try {
        await fetchJSON(`/api/v1/settings/agents/${encodeURIComponent(settingsUIState.selectedAgentId)}`, { method: 'DELETE' });
        await loadAgentSnapshot(settingsUIState.selectedAgentId);
        renderSettingsUI();
        window.__pm_shared.showToast('Agent now inherits tenant defaults', 'success');
    } catch (err) {
        reportSettingsError('Failed to clear agent overrides', err);
    }
}

async function resetTenantOverrides(event) {
    event.preventDefault();
    if (!settingsUIState.selectedTenantId) {
        return;
    }
    if (!confirm('Clear all overrides for this tenant?')) {
        return;
    }
    try {
        await fetchJSON(`/api/v1/settings/tenants/${encodeURIComponent(settingsUIState.selectedTenantId)}`, { method: 'DELETE' });
        await loadTenantSnapshot(settingsUIState.selectedTenantId);
        renderSettingsUI();
        window.__pm_shared.showToast('Tenant now inherits global defaults', 'success');
    } catch (err) {
        reportSettingsError('Failed to clear tenant overrides', err);
    }
}

function renderOverrideSummary() {
    const container = document.getElementById('settings_override_list');
    const titleEl = document.getElementById('settings_summary_title');
    if (!container) return;

    // In global scope, show managed sections summary
    if (settingsUIState.scope === 'global') {
        if (titleEl) titleEl.textContent = 'Management Summary';
        const managedArr = Array.from(settingsUIState.managedSections);
        const allSections = ['discovery', 'snmp', 'features'];
        const agentControlled = allSections.filter(s => !settingsUIState.managedSections.has(s));

        if (agentControlled.length === 0) {
            container.innerHTML = `
                <div class="override-summary-count">All sections centrally managed</div>
                <div class="override-summary-empty">
                    <span style="font-size:12px;">Agents will receive server-defined settings for all categories.</span>
                </div>
            `;
        } else {
            const cards = agentControlled.map(section => {
                const label = SETTINGS_SECTION_LABELS[section] || section;
                return `
                    <div class="override-card">
                        <div class="override-card-path">Agent-Controlled</div>
                        <div class="override-card-value">${escapeHtml(label)}</div>
                    </div>
                `;
            }).join('');
            container.innerHTML = `
                <div class="override-summary-count">${agentControlled.length} section${agentControlled.length > 1 ? 's' : ''} controlled locally by agents</div>
                ${cards}
            `;
        }
        return;
    }

    // Tenant/Agent scope: show override details
    titleEl.textContent = 'Override Summary';
    const scope = settingsUIState.scope;
    if (scope === 'tenant' && !settingsUIState.selectedTenantId) {
        container.innerHTML = '<div class="override-summary-empty"><span>Select a tenant to view override details.</span></div>';
        return;
    }
    if (scope === 'agent' && !settingsUIState.selectedAgentId) {
        container.innerHTML = '<div class="override-summary-empty"><span>Select an agent to view override details.</span></div>';
        return;
    }
    const overrides = flattenOverrides(scope === 'agent' ? settingsUIState.agentOverridesDraft : settingsUIState.tenantOverridesDraft);
    if (!overrides.length) {
        container.innerHTML = scope === 'agent'
            ? '<div class="override-summary-empty"><span>No overrides. This agent inherits tenant defaults.</span></div>'
            : '<div class="override-summary-empty"><span>No overrides. This tenant inherits all global defaults.</span></div>';
        return;
    }

    // Group overrides by section for better organization
    const grouped = {};
    overrides.forEach(item => {
        const section = item.path.split('.')[0] || 'other';
        if (!grouped[section]) {
            grouped[section] = [];
        }
        grouped[section].push(item);
    });

    let html = `<div class="override-summary-count">${overrides.length} override${overrides.length > 1 ? 's' : ''} active</div>`;

    Object.entries(grouped).forEach(([section, items]) => {
        const sectionLabel = SETTINGS_SECTION_LABELS[section] || section;
        html += `<div style="font-size:11px;text-transform:uppercase;color:var(--muted);margin:12px 0 6px;letter-spacing:0.05em;">${escapeHtml(sectionLabel)}</div>`;
        items.forEach(item => {
            let valueClass = '';
            let displayValue = String(item.value);
            if (typeof item.value === 'boolean') {
                valueClass = item.value ? 'bool-true' : 'bool-false';
                displayValue = item.value ? '✓ Enabled' : '✗ Disabled';
            }
            html += `
                <div class="override-card">
                    <div class="override-card-path">${escapeHtml(item.path)}</div>
                    <div class="override-card-value ${valueClass}">${escapeHtml(displayValue)}</div>
                </div>
            `;
        });
    });

    container.innerHTML = html;
}

function updateActionButtons() {
    const saveBtn = document.getElementById('settings_save_btn');
    const discardBtn = document.getElementById('settings_discard_btn');
    const resetBtn = document.getElementById('settings_reset_overrides_btn');
    const resetAgentBtn = document.getElementById('settings_reset_agent_overrides_btn');
    const status = document.getElementById('settings_status');
    const canEdit = userCan('settings.fleet.write');
    const dirty = settingsUIState.scope === 'global'
        ? settingsUIState.globalDirty
        : (settingsUIState.scope === 'tenant' ? settingsUIState.tenantDirty : settingsUIState.agentDirty);
    if (saveBtn) {
        saveBtn.disabled = !canEdit || settingsUIState.saving || !dirty;
    }
    if (discardBtn) {
        discardBtn.disabled = !dirty;
    }
    if (resetBtn) {
        const hasOverrides = flattenOverrides(settingsUIState.tenantOverridesDraft).length > 0;
        resetBtn.classList.toggle('hidden', settingsUIState.scope !== 'tenant');
        resetBtn.disabled = !canEdit || !hasOverrides || settingsUIState.saving;
    }
    if (resetAgentBtn) {
        const hasAgentOverrides = flattenOverrides(settingsUIState.agentOverridesDraft).length > 0;
        resetAgentBtn.disabled = !canEdit || settingsUIState.saving || !hasAgentOverrides;
    }
    const tenantControls = document.getElementById('settings_tenant_controls');
    if (tenantControls) {
        const showTenantControls = settingsUIState.scope === 'tenant' && settingsUIState.tenantList.length > 0;
        const tenantScoped = isTenantScopedUser();
        tenantControls.classList.toggle('hidden', !showTenantControls);

        // For tenant-scoped users with only one tenant, hide the dropdown but show controls
        const tenantSelect = document.getElementById('settings_tenant_select');
        const tenantSelectLabel = tenantSelect?.parentElement;
        if (tenantSelectLabel && tenantScoped && settingsUIState.tenantList.length === 1) {
            // Replace dropdown with static tenant name display
            tenantSelectLabel.style.display = 'none';
        } else if (tenantSelectLabel) {
            tenantSelectLabel.style.display = '';
        }
    }
    const agentControls = document.getElementById('settings_agent_controls');
    if (agentControls) {
        agentControls.classList.toggle('hidden', settingsUIState.scope !== 'agent' || settingsUIState.agentList.length === 0);
    }
    if (status) {
        if (settingsUIState.saving) {
            status.textContent = 'Saving…';
        } else if (dirty) {
            status.textContent = 'Unsaved changes';
        } else {
            status.textContent = '';
        }
    }
}

function updateLastUpdatedMeta() {
    const el = document.getElementById('settings_last_updated');
    if (!el) return;
    let text = '';
    if (settingsUIState.scope === 'global' && settingsUIState.globalSnapshot) {
        const snap = settingsUIState.globalSnapshot;
        const updatedAt = getUpdatedAt(snap);
        if (updatedAt) {
            text = `Updated ${formatRelativeTime(updatedAt)} by ${escapeHtml(getUpdatedBy(snap) || 'system')}`;
        }
    } else if (settingsUIState.scope === 'tenant' && settingsUIState.tenantSnapshot) {
        const snap = settingsUIState.tenantSnapshot;
        const overridesUpdatedAt = getOverridesUpdatedAt(snap);
        if (overridesUpdatedAt) {
            text = `Overrides updated ${formatRelativeTime(overridesUpdatedAt)} by ${escapeHtml(getOverridesUpdatedBy(snap) || 'system')}`;
        } else {
            text = 'Inheriting global defaults';
        }
    } else if (settingsUIState.scope === 'agent' && settingsUIState.agentSnapshot) {
        const snap = settingsUIState.agentSnapshot;
        const overridesUpdatedAt = getOverridesUpdatedAt(snap);
        if (overridesUpdatedAt) {
            text = `Overrides updated ${formatRelativeTime(overridesUpdatedAt)} by ${escapeHtml(getOverridesUpdatedBy(snap) || 'system')}`;
        } else {
            text = 'Inheriting tenant defaults';
        }
    }
    el.textContent = text;
}

function flattenOverrides(overrides, prefix = '', acc = []) {
    if (!overrides || typeof overrides !== 'object') {
        return acc;
    }
    Object.keys(overrides).forEach(key => {
        const path = prefix ? `${prefix}.${key}` : key;
        const value = overrides[key];
        if (value && typeof value === 'object' && !Array.isArray(value)) {
            flattenOverrides(value, path, acc);
        } else {
            acc.push({ path, value });
        }
    });
    return acc;
}

function hasOverride(pathParts) {
    let cursor = settingsUIState.scope === 'agent' ? settingsUIState.agentOverridesDraft : settingsUIState.tenantOverridesDraft;
    for (let i = 0; i < pathParts.length; i++) {
        const part = pathParts[i];
        if (!cursor || typeof cursor !== 'object' || !(part in cursor)) {
            return false;
        }
        cursor = cursor[part];
    }
    return true;
}

// Keys that would let a crafted path reach/mutate Object.prototype (prototype pollution).
const UNSAFE_PATH_KEYS = new Set(['__proto__', 'constructor', 'prototype']);

function pathToArray(path) {
    return (path || '').split('.').filter(key => !UNSAFE_PATH_KEYS.has(key));
}

function readNested(obj, parts) {
    let cursor = obj;
    for (let i = 0; i < parts.length; i++) {
        if (!cursor) return undefined;
        cursor = cursor[parts[i]];
    }
    return cursor;
}

function setNestedValue(obj, path, value) {
    if (!obj) return;
    const parts = pathToArray(path);
    if (parts.length === 0) return;
    let cursor = obj;
    for (let i = 0; i < parts.length - 1; i++) {
        const key = parts[i];
        if (typeof cursor[key] !== 'object' || cursor[key] === null) {
            cursor[key] = {};
        }
        cursor = cursor[key];
    }
    cursor[parts[parts.length - 1]] = value;
}

function deleteNestedValue(obj, path) {
    if (!obj) return;
    const parts = pathToArray(path);
    const stack = [];
    let cursor = obj;
    for (let i = 0; i < parts.length - 1; i++) {
        const key = parts[i];
        if (typeof cursor[key] !== 'object' || cursor[key] === null) {
            return;
        }
        stack.push([cursor, key]);
        cursor = cursor[key];
    }
    delete cursor[parts[parts.length - 1]];
    for (let i = stack.length - 1; i >= 0; i--) {
        const [parent, key] = stack[i];
        if (parent[key] && Object.keys(parent[key]).length === 0) {
            delete parent[key];
        }
    }
}

function getValueByPath(obj, path) {
    return readNested(obj, pathToArray(path));
}

function valuesEqual(a, b) {
    if (typeof a === 'number' && typeof b === 'number') {
        return Number(a) === Number(b);
    }
    if (typeof a === 'boolean' || typeof b === 'boolean') {
        return !!a === !!b;
    }
    return a === b;
}

function cloneSettings(obj) {
    return obj ? JSON.parse(JSON.stringify(obj)) : {};
}

function deepEqual(a, b) {
    if (a === b) {
        return true;
    }
    if (Number.isNaN(a) && Number.isNaN(b)) {
        return true;
    }
    if (Array.isArray(a) || Array.isArray(b)) {
        if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) {
            return false;
        }
        for (let i = 0; i < a.length; i++) {
            if (!deepEqual(a[i], b[i])) {
                return false;
            }
        }
        return true;
    }
    if (a && b && typeof a === 'object' && typeof b === 'object') {
        const keysA = Object.keys(a);
        const keysB = Object.keys(b);
        if (keysA.length !== keysB.length) {
            return false;
        }
        for (const key of keysA) {
            if (!deepEqual(a[key], b[key])) {
                return false;
            }
        }
        return true;
    }
    return false;
}

async function fetchJSON(url, options = {}) {
    const response = await fetch(url, options);
    if (!response.ok) {
        const err = new Error(`HTTP ${response.status}`);
        err.status = response.status;
        try {
            err.body = await response.text();
        } catch (_) {
            err.body = '';
        }
        throw err;
    }
    if (response.status === 204) {
        return null;
    }
    const text = await response.text();
    return text ? JSON.parse(text) : null;
}

function reportSettingsError(message, err) {
    window.__pm_shared.error(message, err);
    let detail = '';
    if (err) {
        const extra = err.body || err.message;
        if (extra) {
            detail = ': ' + String(extra).slice(0, 200);
        }
    }
    window.__pm_shared.showToast(message + detail, 'error', 5000);
}

// ====== Logs Management ======
function initAuditFilterControls() {
    if (auditFiltersInitialized) {
        return;
    }
    auditFiltersInitialized = true;

    const searchInput = document.getElementById('audit_search_filter');
    if (searchInput) {
        const handler = debounce(() => {
            auditFilterState.search = (searchInput.value || '').trim().toLowerCase();
            applyAuditFilters();
        }, 200);
        searchInput.addEventListener('input', handler);
    }

    const actionInput = document.getElementById('audit_action_filter');
    if (actionInput) {
        const handler = debounce(() => {
            auditFilterState.action = (actionInput.value || '').trim().toLowerCase();
            applyAuditFilters();
        }, 200);
        actionInput.addEventListener('input', handler);
    }

    const tenantInput = document.getElementById('audit_tenant_filter');
    if (tenantInput) {
        const handler = debounce(() => {
            auditFilterState.tenant = (tenantInput.value || '').trim().toLowerCase();
            applyAuditFilters();
        }, 200);
        tenantInput.addEventListener('input', handler);
    }

    const severityContainer = document.getElementById('audit_severity_filter');
    if (severityContainer) {
        const checkboxes = Array.from(severityContainer.querySelectorAll('.audit-severity-option'));
        const update = () => updateAuditSeverityState(checkboxes);
        checkboxes.forEach(cb => {
            toggleSeverityPillState(cb);
            cb.addEventListener('change', update);
        });
    }

    const resetBtn = document.getElementById('audit_clear_filters_btn');
    if (resetBtn) {
        resetBtn.addEventListener('click', (ev) => {
            ev.preventDefault();
            resetAuditFilters();
        });
    }

    const liveToggle = document.getElementById('audit_live_toggle');
    if (liveToggle) {
        liveToggle.addEventListener('change', () => {
            toggleAuditLiveUpdates(Boolean(liveToggle.checked));
        });
    }
}

function updateAuditSeverityState(checkboxes) {
    const selected = new Set();
    checkboxes.forEach(cb => {
        toggleSeverityPillState(cb);
        if (cb.checked) {
            selected.add((cb.value || '').toLowerCase());
        }
    });
    if (selected.size === 0) {
        checkboxes.forEach(cb => {
            cb.checked = true;
            toggleSeverityPillState(cb);
            selected.add((cb.value || '').toLowerCase());
        });
        if (window.__pm_shared && typeof window.__pm_shared.showToast === 'function') {
            window.__pm_shared.showToast('Select at least one severity to filter', 'info');
        }
    }
    auditFilterState.severities = selected;
    applyAuditFilters();
}

function toggleSeverityPillState(checkbox) {
    if (!checkbox) return;
    const pill = checkbox.closest('.audit-severity-pill');
    if (pill) {
        pill.classList.toggle('active', checkbox.checked);
    }
}

function resetAuditFilters() {
    const actionInput = document.getElementById('audit_action_filter');
    const tenantInput = document.getElementById('audit_tenant_filter');
    const searchInput = document.getElementById('audit_search_filter');
    if (actionInput) actionInput.value = '';
    if (tenantInput) tenantInput.value = '';
    if (searchInput) searchInput.value = '';
    auditFilterState.action = '';
    auditFilterState.tenant = '';
    auditFilterState.search = '';
    const severityCheckboxes = document.querySelectorAll('.audit-severity-option');
    severityCheckboxes.forEach(cb => {
        cb.checked = true;
        toggleSeverityPillState(cb);
    });
    auditFilterState.severities = new Set(AUDIT_SEVERITY_VALUES);
    applyAuditFilters();
}

function setAuditEntries(entries) {
    auditDataLoaded = true;
    auditLogEntries = Array.isArray(entries) ? entries : [];
    updateAuditActionSuggestions(auditLogEntries);
    applyAuditFilters();
}

function hasActiveAuditFilters() {
    const severities = auditFilterState.severities instanceof Set ? auditFilterState.severities : new Set(AUDIT_SEVERITY_VALUES);
    const allSeveritiesSelected = severities.size === AUDIT_SEVERITY_VALUES.length;
    return Boolean(auditFilterState.search || auditFilterState.action || auditFilterState.tenant || !allSeveritiesSelected);
}

function applyAuditFilters() {
    const entries = Array.isArray(auditLogEntries) ? auditLogEntries : [];
    const severitySet = auditFilterState.severities instanceof Set && auditFilterState.severities.size > 0
        ? auditFilterState.severities
        : new Set(AUDIT_SEVERITY_VALUES);
    const actionQuery = auditFilterState.action;
    const tenantQuery = auditFilterState.tenant;
    const searchTokens = auditFilterState.search ? auditFilterState.search.split(/\s+/).filter(Boolean) : [];

    const filtered = entries.filter(entry => {
        const severity = String(entry && entry.severity ? entry.severity : 'info').toLowerCase();
        if (severitySet.size > 0 && !severitySet.has(severity)) {
            return false;
        }

        if (actionQuery && !(String(entry.action || '').toLowerCase().includes(actionQuery))) {
            return false;
        }

        if (tenantQuery) {
            const tenantMatches = [
                entry.tenant_id,
                entry.metadata && (entry.metadata.tenant_name || entry.metadata.tenant_display || entry.metadata.tenant),
            ].filter(Boolean).map(v => String(v).toLowerCase());
            if (!tenantMatches.some(val => val.includes(tenantQuery))) {
                return false;
            }
        }

        if (searchTokens.length > 0) {
            const haystack = buildAuditSearchHaystack(entry);
            if (!searchTokens.every(token => haystack.includes(token))) {
                return false;
            }
        }

        return true;
    });

    // Store filtered entries for progressive rendering and reset display state
    auditRenderState.filteredEntries = filtered;
    auditRenderState.displayed = 0;
    renderAuditLogs(filtered, { filtersActive: hasActiveAuditFilters() });
    updateAuditSummary(entries.length, filtered.length);
}

function buildAuditSearchHaystack(entry) {
    if (!entry) return '';
    let metadataBlob = '';
    if (entry.metadata) {
        try {
            metadataBlob = JSON.stringify(entry.metadata);
        } catch (err) {
            metadataBlob = '';
        }
    }
    return [
        entry.severity,
        entry.actor_name,
        entry.actor_id,
        entry.actor_type,
        entry.action,
        entry.target_type,
        entry.target_id,
        entry.tenant_id,
        entry.details,
        entry.ip_address,
        entry.user_agent,
        entry.request_id,
        metadataBlob,
    ].filter(Boolean).join(' ').toLowerCase();
}

function updateAuditSummary(total, filtered) {
    const summary = document.getElementById('audit_summary');
    if (!summary) return;
    if (!auditDataLoaded) {
        summary.setAttribute('hidden', 'hidden');
        return;
    }
    const countsEl = document.getElementById('audit_summary_counts');
    if (countsEl) {
        if (total === filtered) {
            countsEl.innerHTML = `<strong>${filtered}</strong> ${filtered === 1 ? 'entry' : 'entries'}`;
        } else {
            countsEl.innerHTML = `<strong>${filtered}</strong> of ${total} entries`;
        }
    }
    summary.removeAttribute('hidden');
}

function setAuditLastUpdated(date = new Date()) {
    auditLastUpdated = date;
    const updatedEl = document.getElementById('audit_summary_updated');
    if (!updatedEl) return;
    const isoValue = date instanceof Date ? date.toISOString() : date;
    const relative = formatRelativeTime(isoValue);
    const exact = date instanceof Date ? date.toLocaleTimeString() : String(date);
    updatedEl.textContent = `Updated ${relative} (${exact})`;
}

function updateAuditActionSuggestions(entries) {
    const dataList = document.getElementById('audit_action_suggestions');
    if (!dataList) return;
    dataList.innerHTML = '';
    const unique = new Set();
    entries.forEach(entry => {
        if (entry && entry.action) {
            unique.add(entry.action);
        }
    });
    Array.from(unique).sort().slice(0, 50).forEach(action => {
        const option = document.createElement('option');
        option.value = action;
        dataList.appendChild(option);
    });
}

function toggleAuditLiveUpdates(enabled) {
    auditLiveRequested = enabled;
    const toggle = document.getElementById('audit_live_toggle');
    if (toggle && toggle.checked !== enabled) {
        toggle.checked = enabled;
    }
    syncAuditLiveTimer();
    if (enabled && activeLogView === 'audit') {
        loadAuditLogs({ silent: true });
    }
}

function syncAuditLiveTimer() {
    if (auditAutoRefreshHandle) {
        clearInterval(auditAutoRefreshHandle);
        auditAutoRefreshHandle = null;
    }
    if (auditLiveRequested && activeLogView === 'audit') {
        auditAutoRefreshHandle = setInterval(() => {
            loadAuditLogs({ silent: true });
        }, AUDIT_AUTO_REFRESH_INTERVAL_MS);
    }
    updateAuditLiveStatus();
}

function updateAuditLiveStatus() {
    const statusEl = document.getElementById('audit_live_status');
    if (!statusEl) return;
    if (!auditLiveRequested) {
        statusEl.textContent = 'Auto-refresh off';
        return;
    }
    if (activeLogView !== 'audit') {
        statusEl.textContent = 'Auto-refresh paused';
        return;
    }
    statusEl.textContent = auditAutoRefreshHandle ? 'Auto-refresh on' : 'Auto-refresh ready';
}

// Copy current logs to clipboard
async function copyLogs() {
    try {
        const response = await fetch('/api/logs');
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        const lines = data.logs || [];
        const text = lines.join('\n');
        await navigator.clipboard.writeText(text);
        window.__pm_shared.showToast('Logs copied to clipboard', 'success', 1500);
    } catch (e) {
        window.__pm_shared.error('Copy logs failed:', e);
        window.__pm_shared.showToast('Failed to copy logs: ' + e.message, 'error');
    }
}

// Download logs as file
async function downloadLogs() {
    try {
        const response = await fetch('/api/logs');
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        const lines = data.logs || [];
        const text = lines.join('\n');
        const blob = new Blob([text], { type: 'text/plain' });
        const filename = `server-logs-${new Date().toISOString().slice(0, 19).replace(/[T:]/g, '-')}.log`;
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
        window.__pm_shared.showToast('Logs downloaded', 'success');
    } catch (e) {
        window.__pm_shared.error('Download logs failed:', e);
        window.__pm_shared.showToast('Failed to download logs: ' + e.message, 'error');
    }
}

// Clear server logs (rotate)
async function clearLogs() {
    try {
        const confirmed = await window.__pm_shared.showConfirm(
            'Clear server logs? This will rotate the current log file.',
            'Clear Logs'
        );
        if (!confirmed) return;

        const resp = await fetch('/api/logs/clear', { method: 'POST' });
        if (!resp.ok) {
            const text = await resp.text();
            window.__pm_shared.showToast('Clear logs failed: ' + text, 'error');
            return;
        }

        // Clear the display and reset state
        currentLogLines = [];
        logsState.entries = [];
        logsState.total = 0;
        logsState.offset = 0;
        logsState.hasMore = false;
        logsState.hasPrevious = false;
        cleanupLogsInfiniteScroll();

        const logEl = document.getElementById('log');
        if (logEl) {
            logEl.innerHTML = '<span style="color:#586e75">(logs cleared - waiting for new entries)</span>';
        }
        const tbody = document.getElementById('log_table_body');
        if (tbody) {
            tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:#586e75">(logs cleared - waiting for new entries)</td></tr>';
        }
        updateLogsShowingCount(0);

        window.__pm_shared.showToast('Logs cleared and rotated', 'success');
    } catch (e) {
        window.__pm_shared.error('Clear logs failed:', e);
        window.__pm_shared.showToast('Failed to clear logs: ' + e.message, 'error');
    }
}

async function loadLogs(options = {}) {
    const { append = false, prepend = false } = options;

    if (logsState.loading) return;
    logsState.loading = true;

    try {
        // Build query params
        const params = new URLSearchParams();
        params.set('limit', String(logsState.limit));

        // Calculate offset based on append/prepend mode
        let requestOffset = logsState.offset;
        if (append && logsState.entries.length > 0) {
            // Loading older logs - offset is after current entries
            requestOffset = logsState.entries.length;
        } else if (prepend) {
            // Loading newer logs - offset is 0 (newest first)
            requestOffset = 0;
        }
        params.set('offset', String(requestOffset));

        // Add level filter
        const levelFilter = document.getElementById('log_level_filter');
        const level = levelFilter ? levelFilter.value : '';
        if (level) {
            params.set('level', level);
        }

        // Add search filter
        const searchFilter = document.getElementById('log_search_filter');
        const search = searchFilter ? searchFilter.value.trim() : '';
        if (search) {
            params.set('search', search);
        }

        const response = await fetch(`/api/logs?${params.toString()}`);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const data = await response.json();
        const newEntries = (data.logs || []).map(parseLogLine);

        // Update state
        logsState.total = data.total || 0;
        logsState.hasMore = data.has_more || false;
        logsState.hasPrevious = data.has_previous || false;

        if (append && logsState.entries.length > 0) {
            // Append older logs to the end
            logsState.entries = [...logsState.entries, ...newEntries];
            // Cull from the beginning (newest) if too many
            if (logsState.entries.length > logsState.maxLoaded) {
                const excess = logsState.entries.length - logsState.maxLoaded;
                logsState.entries = logsState.entries.slice(excess);
                logsState.hasPrevious = true; // We culled newer logs
            }
        } else if (prepend && logsState.entries.length > 0) {
            // Prepend newer logs to the beginning
            logsState.entries = [...newEntries, ...logsState.entries];
            // Cull from the end (oldest) if too many
            if (logsState.entries.length > logsState.maxLoaded) {
                logsState.entries = logsState.entries.slice(0, logsState.maxLoaded);
                logsState.hasMore = true; // We culled older logs
            }
        } else {
            // Fresh load
            logsState.entries = newEntries;
            logsState.offset = 0;
        }

        // Update currentLogLines for compatibility
        currentLogLines = logsState.entries;

        // Render (pass append flag to skip auto-scroll)
        renderLogs({ logs: logsState.entries.map(e => e.raw), append });

        // Setup infinite scroll observer (only if more to load)
        if (logsState.hasMore) {
            setupLogsInfiniteScroll();
        }

    } catch (error) {
        window.__pm_shared.error('Failed to load logs:', error);
        const logEl = document.getElementById('log');
        if (logEl) {
            logEl.textContent = 'Failed to load logs: ' + error.message;
        }
    } finally {
        logsState.loading = false;
    }
}

async function loadAuditLogs(options) {
    if (!userCan('audit.logs.read')) {
        return;
    }

    const opts = options || {};
    const silent = Boolean(opts.silent);

    const container = document.getElementById('audit_logs_table');
    if (!container) {
        window.__pm_shared.warn('loadAuditLogs: container not found');
        return;
    }

    const params = new URLSearchParams();
    const timeFilter = document.getElementById('audit_time_filter');
    const hours = timeFilter ? parseInt(timeFilter.value, 10) : 24;
    if (hours && hours > 0) {
        params.set('hours', String(hours));
    }

    const actorFilter = document.getElementById('audit_actor_filter');
    const actorValue = actorFilter ? actorFilter.value.trim() : '';
    if (actorValue) {
        params.set('actor_id', actorValue);
    }

    if (!silent) {
        container.innerHTML = '<div class="muted-text">Loading audit log...</div>';
    }

    const queryString = params.toString();
    const endpoint = queryString ? `/api/audit/logs?${queryString}` : '/api/audit/logs';

    try {
        const response = await fetch(endpoint);
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }

        const payload = await response.json();
        let entries = [];
        if (payload && Array.isArray(payload.entries)) {
            entries = payload.entries;
        } else if (Array.isArray(payload)) {
            entries = payload;
        }
        setAuditEntries(entries);
        setAuditLastUpdated(new Date());
    } catch (error) {
        window.__pm_shared.error('Failed to load audit logs:', error);
        if (!silent) {
            const message = escapeHtml(error && error.message ? error.message : String(error));
            container.innerHTML = `<div class="error-text">Failed to load audit logs: ${message}</div>`;
        }
    }
}

// ====== Metrics ======
const serverMetricsVM = {
    timeseries: null,
    loading: false,
    error: null,
};

async function loadMetrics(force) {
    const tab = document.querySelector('[data-tab="metrics"]');
    if (!tab) return;
    if (metricsVM.loading && !force) return;

    const since = new Date(Date.now() - getMetricsRangeWindow(metricsVM.range));
    const params = new URLSearchParams({ since: since.toISOString() });

    // Add filter params if set
    if (metricsVM.filters.tenantId) {
        params.set('tenant_id', metricsVM.filters.tenantId);
    }
    if (metricsVM.filters.agentId) {
        params.set('agent_id', metricsVM.filters.agentId);
    }
    if (metricsVM.filters.deviceSerial) {
        params.set('device_serial', metricsVM.filters.deviceSerial);
    }

    // Server time-series params - request all series for comprehensive dashboards
    const tsParams = new URLSearchParams({
        start: since.toISOString(),
        end: new Date().toISOString(),
        resolution: 'auto',
        series: 'goroutines,heap_alloc,db_size,total_pages,color_pages,mono_pages,scan_count,toner_high,toner_medium,toner_low,toner_critical,ws_connections,agents,devices,devices_online,devices_error,agents_ws,agents_http,agents_offline',
    });

    metricsVM.loading = true;
    serverMetricsVM.loading = true;
    renderMetricsLoading();

    // Determine if we should load server metrics (only for global admins)
    const loadServerMetrics = isGlobalAdmin();

    try {
        const fetchPromises = [
            fetch('/api/metrics'),
            fetch(`/api/metrics/aggregated?${params.toString()}`),
        ];
        // Only fetch server time-series for global admins
        if (loadServerMetrics) {
            fetchPromises.push(fetch(`/api/metrics/timeseries?${tsParams.toString()}`));
        }

        const responses = await Promise.all(fetchPromises);
        const [summaryResp, aggregatedResp] = responses;
        const timeseriesResp = loadServerMetrics ? responses[2] : null;

        if (!summaryResp.ok) {
            throw new Error('Summary request failed: HTTP ' + summaryResp.status);
        }
        if (!aggregatedResp.ok) {
            throw new Error('Aggregated request failed: HTTP ' + aggregatedResp.status);
        }

        metricsVM.summary = await summaryResp.json();
        metricsVM.aggregated = await aggregatedResp.json();
        metricsVM.lastFetched = new Date();
        metricsVM.error = null;

        // Load server time-series data (non-blocking on error, only for admins)
        if (timeseriesResp && timeseriesResp.ok) {
            serverMetricsVM.timeseries = await timeseriesResp.json();
            serverMetricsVM.error = null;
        } else {
            serverMetricsVM.timeseries = null;
        }

        renderMetricsDashboard();
    } catch (err) {
        metricsVM.error = err;
        renderMetricsError(err);
    } finally {
        metricsVM.loading = false;
        serverMetricsVM.loading = false;
    }
}

// Helper to generate chart skeleton loading HTML
function chartSkeletonHTML(text, count = 1) {
    const skeleton = `<div class="metric-chart-card loading"><div class="chart-skeleton"><div class="chart-spinner"></div><span class="chart-loading-text">${text}</span></div></div>`;
    return count > 1 ? Array(count).fill(skeleton).join('') : skeleton;
}

function renderMetricsLoading() {
    const statsEl = document.getElementById('metrics_stats');
    const chartsEl = document.getElementById('metrics_chart_grid');
    const consumablesChartsEl = document.getElementById('metrics_consumables_charts');
    const agentFleetChartsEl = document.getElementById('metrics_agent_fleet_charts');
    const serverChartsEl = document.getElementById('metrics_server_charts');
    const serverEl = document.getElementById('metrics_server_panel');
    const consumablesEl = document.getElementById('metrics_consumables');
    if (statsEl && !metricsVM.summary) {
        statsEl.innerHTML = '<div class="metric-card loading">Loading metrics…</div>';
    }
    if (chartsEl && !metricsVM.aggregated) {
        chartsEl.innerHTML = chartSkeletonHTML('Loading throughput data…', 3);
    }
    if (consumablesChartsEl && !serverMetricsVM.timeseries) {
        consumablesChartsEl.innerHTML = chartSkeletonHTML('Loading consumables history…', 2);
    }
    if (agentFleetChartsEl && !serverMetricsVM.timeseries) {
        agentFleetChartsEl.innerHTML = chartSkeletonHTML('Loading agent fleet data…', 2);
    }
    if (serverChartsEl && !serverMetricsVM.timeseries) {
        serverChartsEl.innerHTML = chartSkeletonHTML('Loading server time-series…', 3);
    }
    if (serverEl && !metricsVM.aggregated) {
        serverEl.innerHTML = '<div class="metric-card loading">Collecting server stats…</div>';
    }
    if (consumablesEl && !metricsVM.aggregated) {
        consumablesEl.innerHTML = '<div class="card-title">Consumables</div><div class="muted-text">Loading…</div>';
    }
}

function renderMetricsDashboard() {
    // Hide/show server-only sections based on role
    updateMetricsServerVisibility();

    renderMetricsOverview(metricsVM.summary, metricsVM.aggregated);
    renderFleetCharts(metricsVM.aggregated);
    renderConsumablesTimeSeriesCharts(serverMetricsVM.timeseries);
    renderAgentFleetCharts(serverMetricsVM.timeseries);

    // Only render server metrics for global admins
    if (isGlobalAdmin()) {
        renderServerTimeSeriesCharts(serverMetricsVM.timeseries);
        renderServerPanel(metricsVM.aggregated?.server);
    }

    renderConsumables(metricsVM.aggregated?.fleet);
    renderMetricsActivity(metricsVM.aggregated);
    updateMetricsRangeButtons();
}

/**
 * Show/hide server metrics sections based on user role.
 * Server runtime stats are only visible to global admins.
 */
function updateMetricsServerVisibility() {
    const serverSection = document.getElementById('metrics_server_section');
    const serverPanel = document.getElementById('metrics_server_panel');
    const showServer = isGlobalAdmin();

    if (serverSection) {
        serverSection.style.display = showServer ? '' : 'none';
    }
    if (serverPanel) {
        serverPanel.style.display = showServer ? '' : 'none';
    }
}

function renderMetricsOverview(summary, aggregated) {
    const container = document.getElementById('metrics_stats');
    if (!container) return;
    if (!summary || !aggregated) {
        container.innerHTML = '<div class="metric-card loading">Waiting for fleet data…</div>';
        return;
    }

    const totals = aggregated?.fleet?.totals || {};
    const statuses = aggregated?.fleet?.statuses || {};
    const history = aggregated?.fleet?.history?.total_impressions || aggregated?.fleet?.history?.TotalImpressions || [];
    const throughput = calculateThroughput(history);
    const rangeLabel = metricsRangeLabel(metricsVM.range);

    container.innerHTML = `
        <div class="metric-card">
            <div class="card-title">Agents</div>
            <div class="metric-kpi-value">${formatNumber(totals.agents || summary.agents_count || 0)}</div>
            <div class="metric-kpi-label">Connected</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Devices</div>
            <div class="metric-kpi-value">${formatNumber(totals.devices || summary.devices_count || 0)}</div>
            <div class="metric-kpi-label">Managed across fleet</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Throughput (${rangeLabel})</div>
            <div class="metric-kpi-value">${formatNumber(Math.round(throughput))}</div>
            <div class="metric-kpi-label">Estimated pages per hour</div>
        </div>
        <div class="metric-card">
            <div class="card-title">Alerts</div>
            ${renderMetricsStatusChips(statuses)}
            <div class="metric-footnote">${metricsVM.lastFetched ? 'Updated ' + formatRelativeTime(metricsVM.lastFetched) : ''}</div>
        </div>
    `;
}

function renderMetricsStatusChips(statuses) {
    const error = statuses?.error || 0;
    const warn = statuses?.warning || 0;
    const jam = statuses?.jam || 0;
    return `
        <div class="metric-status-chips">
            <span class="metric-chip error">Errors <strong>${formatNumber(error)}</strong></span>
            <span class="metric-chip warn">Warnings <strong>${formatNumber(warn)}</strong></span>
            <span class="metric-chip jam">Jams <strong>${formatNumber(jam)}</strong></span>
        </div>
    `;
}

function renderFleetCharts(aggregated) {
    const grid = document.getElementById('metrics_chart_grid');
    if (!grid) return;
    const history = aggregated?.fleet?.history;
    const totals = aggregated?.fleet?.totals || {};
    if (!history) {
        grid.innerHTML = `
            <div class="metric-chart-card">
                <div class="card-title">Total Impressions</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>No fleet history yet</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Color vs Mono</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>No fleet history yet</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Scan Volume</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>No fleet history yet</span>
                </div>
            </div>`;
        return;
    }

    // Helper to compute cumulative series from rate data
    // Takes rate points and lifetime total, works backwards to compute cumulative at each point
    const toCumulativeSeries = (ratePoints, lifetimeTotal) => {
        if (!Array.isArray(ratePoints) || ratePoints.length === 0) return [];
        // Sum all deltas to get total printed during this window
        const windowTotal = ratePoints.reduce((sum, pt) => sum + (pt.value || 0), 0);
        // Starting cumulative is (lifetime - window total)
        let cumulative = lifetimeTotal - windowTotal;
        return ratePoints.map(pt => {
            cumulative += pt.value || 0;
            return { time: pt.time, value: cumulative };
        });
    };

    const totalRatePoints = toSeriesPoints(history.total_impressions || history.TotalImpressions);
    const colorRatePoints = toSeriesPoints(history.color_impressions || history.ColorImpressions);
    const monoRatePoints = toSeriesPoints(history.mono_impressions || history.MonoImpressions);
    const scanRatePoints = toSeriesPoints(history.scan_volume || history.ScanVolume);

    const cards = [
        {
            id: 'fleet_total_chart',
            title: 'Total Impressions',
            rateSeries: [{ label: 'Hourly Rate', color: FLEET_SERIES_COLORS[0], points: totalRatePoints }],
            cumulativeSeries: [{ label: 'Cumulative', color: '#9f7aea', points: toCumulativeSeries(totalRatePoints, totals.page_count || 0) }],
        },
        {
            id: 'fleet_color_mono_chart',
            title: 'Color vs Mono',
            rateSeries: [
                { label: 'Color/hr', color: FLEET_SERIES_COLORS[1], points: colorRatePoints },
                { label: 'Mono/hr', color: FLEET_SERIES_COLORS[2], points: monoRatePoints },
            ],
            cumulativeSeries: [
                { label: 'Color Total', color: '#00bcd4', points: toCumulativeSeries(colorRatePoints, totals.color_pages || 0) },
                { label: 'Mono Total', color: '#78909c', points: toCumulativeSeries(monoRatePoints, totals.mono_pages || 0) },
            ],
        },
        {
            id: 'fleet_scan_chart',
            title: 'Scan Volume',
            rateSeries: [{ label: 'Scans/hr', color: FLEET_SERIES_COLORS[3], points: scanRatePoints }],
            cumulativeSeries: [{ label: 'Total Scans', color: '#26a69a', points: toCumulativeSeries(scanRatePoints, totals.scan_count || 0) }],
        },
    ];

    grid.innerHTML = cards.map(card => `
        <div class="metric-chart-card data-loading">
            <div class="card-title">${card.title}</div>
            <canvas id="${card.id}" class="metric-chart-canvas" height="220"></canvas>
        </div>
    `).join('');

    // Small delay for DOM to settle, then draw and reveal
    requestAnimationFrame(() => {
        cards.forEach(card => {
            const canvas = document.getElementById(card.id);
            if (canvas) {
                drawFleetChartDualAxis(canvas, card.rateSeries, card.cumulativeSeries, { label: card.title });
                // Remove loading class to trigger fade-in
                canvas.closest('.metric-chart-card')?.classList.remove('data-loading');
            }
        });
    });
}

// Server Runtime Time-Series Charts - Netdata-style full-width

// Helper to normalize chart series points from {t, v} to {time, value} format
// Backend sends compact {t, v} but our chart functions expect {time, value}
// Returns null if input is falsy/empty so fallback can work with || operator
function normalizeChartSeriesPoints(points) {
    if (!Array.isArray(points) || points.length === 0) return null;
    // Check if already in correct format
    if (points[0].time !== undefined) return points;
    // Transform from {t, v} to {time, value}
    return points.map(p => ({ time: p.t, value: p.v }));
}

const SERVER_SERIES_COLORS = {
    goroutines: '#4299e1',
    heap_alloc: '#48bb78',
    db_size: '#ed8936',
    ws_connections: '#9f7aea',
    total_pages: '#4a5568',
    color_pages: '#00bcd4',
    mono_pages: '#718096',
    scan_volume: '#38b2ac',
    toner_low: '#ecc94b',
    toner_critical: '#f56565',
    toner_high: '#4299e1',
    toner_medium: '#48bb78',
    agents: '#9f7aea',
    devices: '#ed8936',
    devices_online: '#48bb78',
    devices_error: '#f56565',
    agents_ws: '#48bb78',      // Green for WebSocket connected
    agents_http: '#ecc94b',    // Yellow for HTTP fallback
    agents_offline: '#f56565', // Red for offline
};

/**
 * Helper to render chart cards without flashing.
 * Reuses existing canvas elements when possible to avoid DOM thrashing during live updates.
 * @param {HTMLElement} grid - Container element
 * @param {Array} cards - Array of {id, title, series, formatY?} objects
 * @param {Function} drawFn - Chart drawing function (drawFleetChart or custom)
 * @param {string} noDataHtml - HTML to show when no data
 */
function renderChartCardsSmooth(grid, cards, drawFn, noDataHtml) {
    if (!grid) return;

    // Filter to cards with actual data
    const validCards = cards.filter(card =>
        card.series && card.series.some(s => s.points && s.points.length > 0)
    );

    if (validCards.length === 0) {
        grid.innerHTML = noDataHtml;
        return;
    }

    // Check if we can reuse existing canvases (same card IDs in same order)
    const existingIds = Array.from(grid.querySelectorAll('canvas.metric-chart-canvas')).map(c => c.id);
    const newIds = validCards.map(c => c.id);
    const canReuse = existingIds.length === newIds.length && existingIds.every((id, i) => id === newIds[i]);

    if (!canReuse) {
        // Need to rebuild DOM structure
        grid.innerHTML = validCards.map(card => `
            <div class="metric-chart-card">
                <div class="card-title">${card.title}</div>
                <canvas id="${card.id}" class="metric-chart-canvas" height="220"></canvas>
            </div>
        `).join('');
    }

    // Draw charts (reuses existing canvas elements if structure matches)
    requestAnimationFrame(() => {
        validCards.forEach(card => {
            const canvas = document.getElementById(card.id);
            if (canvas) {
                drawFn(canvas, card.series, {
                    label: card.title,
                    formatY: card.formatY,
                });
            }
        });
    });
}

// Consumables Time-Series Charts - Historical view of toner levels across fleet
function renderConsumablesTimeSeriesCharts(timeseries) {
    const grid = document.getElementById('metrics_consumables_charts');
    if (!grid) return;

    if (!timeseries || !timeseries.snapshots || timeseries.snapshots.length === 0) {
        grid.innerHTML = `
            <div class="metric-chart-card">
                <div class="card-title">Consumables Distribution Over Time</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Low & Critical Consumables Trend</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>`;
        return;
    }

    const chartSeries = timeseries.chart_series || {};
    const snapshots = timeseries.snapshots || [];

    const buildSeriesFromSnapshots = (key, accessor) => {
        return snapshots.map(s => ({
            time: new Date(s.timestamp).getTime(),
            value: accessor(s),
        })).filter(p => p.value !== undefined && p.value !== null);
    };

    const cards = [
        {
            id: 'consumables_history_chart',
            title: 'Consumables Distribution Over Time',
            series: [
                {
                    label: 'High (>50%)',
                    color: SERVER_SERIES_COLORS.toner_high,
                    points: normalizeChartSeriesPoints(chartSeries.toner_high) || buildSeriesFromSnapshots('toner_high', s => s.fleet?.toner_high),
                },
                {
                    label: 'Medium (25-50%)',
                    color: SERVER_SERIES_COLORS.toner_medium,
                    points: normalizeChartSeriesPoints(chartSeries.toner_medium) || buildSeriesFromSnapshots('toner_medium', s => s.fleet?.toner_medium),
                },
                {
                    label: 'Low (10-25%)',
                    color: SERVER_SERIES_COLORS.toner_low,
                    points: normalizeChartSeriesPoints(chartSeries.toner_low) || buildSeriesFromSnapshots('toner_low', s => s.fleet?.toner_low),
                },
                {
                    label: 'Critical (<10%)',
                    color: SERVER_SERIES_COLORS.toner_critical,
                    points: normalizeChartSeriesPoints(chartSeries.toner_critical) || buildSeriesFromSnapshots('toner_critical', s => s.fleet?.toner_critical),
                },
            ],
        },
        {
            id: 'consumables_alerts_chart',
            title: 'Low & Critical Consumables Trend',
            series: [
                {
                    label: 'Low',
                    color: SERVER_SERIES_COLORS.toner_low,
                    points: normalizeChartSeriesPoints(chartSeries.toner_low) || buildSeriesFromSnapshots('toner_low', s => s.fleet?.toner_low),
                },
                {
                    label: 'Critical',
                    color: SERVER_SERIES_COLORS.toner_critical,
                    points: normalizeChartSeriesPoints(chartSeries.toner_critical) || buildSeriesFromSnapshots('toner_critical', s => s.fleet?.toner_critical),
                },
            ],
        },
    ];

    const noDataHtml = `
        <div class="metric-chart-card">
            <div class="card-title">Consumables Distribution Over Time</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>
        <div class="metric-chart-card">
            <div class="card-title">Low & Critical Consumables Trend</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>`;

    renderChartCardsSmooth(grid, cards, drawFleetChart, noDataHtml);
}

// Agent Fleet Time-Series Charts - Historical view of agent fleet health
function renderAgentFleetCharts(timeseries) {
    const grid = document.getElementById('metrics_agent_fleet_charts');
    if (!grid) return;

    if (!timeseries || !timeseries.snapshots || timeseries.snapshots.length === 0) {
        grid.innerHTML = `
            <div class="metric-chart-card">
                <div class="card-title">Agent & Device Count</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Device Status</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>`;
        return;
    }

    const chartSeries = timeseries.chart_series || {};
    const snapshots = timeseries.snapshots || [];

    const buildSeriesFromSnapshots = (key, accessor) => {
        return snapshots.map(s => ({
            time: new Date(s.timestamp).getTime(),
            value: accessor(s),
        })).filter(p => p.value !== undefined && p.value !== null);
    };

    const cards = [
        {
            id: 'agent_count_chart',
            title: 'Agent & Device Count',
            series: [
                {
                    label: 'Agents',
                    color: SERVER_SERIES_COLORS.agents,
                    points: normalizeChartSeriesPoints(chartSeries.agents) || buildSeriesFromSnapshots('agents', s => s.fleet?.total_agents),
                },
                {
                    label: 'Devices',
                    color: SERVER_SERIES_COLORS.devices,
                    points: normalizeChartSeriesPoints(chartSeries.devices) || buildSeriesFromSnapshots('devices', s => s.fleet?.total_devices),
                },
            ],
        },
        {
            id: 'device_health_chart',
            title: 'Device Health',
            series: [
                {
                    label: 'Online',
                    color: SERVER_SERIES_COLORS.devices_online,
                    points: normalizeChartSeriesPoints(chartSeries.devices_online) || buildSeriesFromSnapshots('devices_online', s => s.fleet?.devices_online),
                },
                {
                    label: 'Errors',
                    color: SERVER_SERIES_COLORS.devices_error,
                    points: normalizeChartSeriesPoints(chartSeries.devices_error) || buildSeriesFromSnapshots('devices_error', s => s.fleet?.devices_error),
                },
            ],
        },
    ];

    const noDataHtml = `
        <div class="metric-chart-card">
            <div class="card-title">Agent & Device Count</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>
        <div class="metric-chart-card">
            <div class="card-title">Device Health</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>`;

    renderChartCardsSmooth(grid, cards, drawFleetChart, noDataHtml);
}

function renderServerTimeSeriesCharts(timeseries) {
    const grid = document.getElementById('metrics_server_charts');
    if (!grid) return;

    if (!timeseries || !timeseries.snapshots || timeseries.snapshots.length === 0) {
        grid.innerHTML = `
            <div class="metric-chart-card">
                <div class="card-title">Goroutines</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Memory (Heap)</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>
            <div class="metric-chart-card">
                <div class="card-title">Database Size</div>
                <div class="no-data-placeholder">
                    <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                    <span>Collecting data…</span>
                </div>
            </div>`;
        return;
    }

    const chartSeries = timeseries.chart_series || {};
    const snapshots = timeseries.snapshots || [];

    // Build chart data from snapshots if chart_series not provided
    const buildSeriesFromSnapshots = (key, accessor) => {
        return snapshots.map(s => ({
            time: new Date(s.timestamp).getTime(),
            value: accessor(s),
        })).filter(p => p.value !== undefined && p.value !== null);
    };

    const cards = [
        {
            id: 'server_goroutines_chart',
            title: 'Goroutines',
            series: [{
                label: 'Goroutines',
                color: SERVER_SERIES_COLORS.goroutines,
                points: normalizeChartSeriesPoints(chartSeries.goroutines) || buildSeriesFromSnapshots('goroutines', s => s.server?.goroutines)
            }],
        },
        {
            id: 'server_memory_chart',
            title: 'Memory (Heap)',
            series: [{
                label: 'Heap Alloc',
                color: SERVER_SERIES_COLORS.heap_alloc,
                points: normalizeChartSeriesPoints(chartSeries.heap_alloc) || buildSeriesFromSnapshots('heap_alloc', s => s.server?.heap_alloc_mb),
            }],
            formatY: v => formatBytes(v * 1024 * 1024), // heap_alloc_mb is in MB
        },
        {
            id: 'server_db_chart',
            title: 'Database Size',
            series: [{
                label: 'DB Size',
                color: SERVER_SERIES_COLORS.db_size,
                points: normalizeChartSeriesPoints(chartSeries.db_size) || buildSeriesFromSnapshots('db_size', s => s.server?.db_size_bytes),
            }],
            formatY: formatBytes,
        },
        {
            id: 'server_ws_chart',
            title: 'Agent Connections',
            series: [
                {
                    label: 'WebSocket',
                    color: SERVER_SERIES_COLORS.agents_ws,
                    points: normalizeChartSeriesPoints(chartSeries.agents_ws) || buildSeriesFromSnapshots('agents_ws', s => s.fleet?.agents_ws),
                },
                {
                    label: 'HTTP',
                    color: SERVER_SERIES_COLORS.agents_http,
                    points: normalizeChartSeriesPoints(chartSeries.agents_http) || buildSeriesFromSnapshots('agents_http', s => s.fleet?.agents_http),
                },
                {
                    label: 'Offline',
                    color: SERVER_SERIES_COLORS.agents_offline,
                    points: normalizeChartSeriesPoints(chartSeries.agents_offline) || buildSeriesFromSnapshots('agents_offline', s => s.fleet?.agents_offline),
                },
            ],
        },
        {
            id: 'server_pages_chart',
            title: 'Fleet Page Counts',
            series: [
                {
                    label: 'Total',
                    color: SERVER_SERIES_COLORS.total_pages,
                    points: normalizeChartSeriesPoints(chartSeries.total_pages) || buildSeriesFromSnapshots('total_pages', s => s.fleet?.total_pages),
                },
                {
                    label: 'Color',
                    color: SERVER_SERIES_COLORS.color_pages,
                    points: normalizeChartSeriesPoints(chartSeries.color_pages) || buildSeriesFromSnapshots('color_pages', s => s.fleet?.color_pages),
                },
                {
                    label: 'Mono',
                    color: SERVER_SERIES_COLORS.mono_pages,
                    points: normalizeChartSeriesPoints(chartSeries.mono_pages) || buildSeriesFromSnapshots('mono_pages', s => s.fleet?.mono_pages),
                },
            ],
        },
        {
            id: 'server_toner_chart',
            title: 'Toner Levels',
            series: [
                {
                    label: 'Low',
                    color: SERVER_SERIES_COLORS.toner_low,
                    points: normalizeChartSeriesPoints(chartSeries.toner_low) || buildSeriesFromSnapshots('toner_low', s => s.fleet?.toner_low),
                },
                {
                    label: 'Critical',
                    color: SERVER_SERIES_COLORS.toner_critical,
                    points: normalizeChartSeriesPoints(chartSeries.toner_critical) || buildSeriesFromSnapshots('toner_critical', s => s.fleet?.toner_critical),
                },
            ],
        },
    ];

    const noDataHtml = `
        <div class="metric-chart-card">
            <div class="card-title">Goroutines</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>
        <div class="metric-chart-card">
            <div class="card-title">Memory (Heap)</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>
        <div class="metric-chart-card">
            <div class="card-title">Database Size</div>
            <div class="no-data-placeholder">
                <svg class="no-data-icon" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M3 3v18h18"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>
                <span>Collecting data…</span>
            </div>
        </div>`;

    renderChartCardsSmooth(grid, cards, drawFleetChart, noDataHtml);
}

function renderServerPanel(server) {
    const panel = document.getElementById('metrics_server_panel');
    if (!panel) return;
    if (!server) {
        panel.innerHTML = '<div class="metric-card loading">Server metrics unavailable.</div>';
        return;
    }

    const runtime = server.runtime || {};
    const memory = runtime.memory || {};
    const db = server.database || {};
    const uptime = formatDuration(server.uptime_seconds);
    const artifactsBytes = db.release_artifacts_bytes || db.release_bytes || 0;
    const cacheBytes = artifactsBytes;

    panel.innerHTML = `
        <div class="metric-card">
            <div class="card-title">Server Runtime</div>
            <div class="metric-kpi-value" style="font-size:18px;">${escapeHtml(server.hostname || 'PrintMaster')}</div>
            <div class="metric-kpi-label">Up ${uptime}, ${escapeHtml(runtime.go_version || 'Go')}</div>
            <ul style="list-style:none;padding:0;margin:12px 0 0;font-size:13px;line-height:1.6;">
                <li>Goroutines: <strong>${formatNumber(runtime.num_goroutine || 0)}</strong></li>
                <li>Heap: <strong>${formatBytes(memory.heap_alloc_bytes || memory.heap_alloc || 0)}</strong></li>
                <li>Total Alloc: <strong>${formatBytes(memory.total_alloc_bytes || memory.total_alloc || 0)}</strong></li>
            </ul>
        </div>
        <div class="metric-card">
            <div class="card-title">Database</div>
            ${db ? `
                <ul style="list-style:none;padding:0;margin:0;font-size:13px;line-height:1.6;">
                    <li>Agents: <strong>${formatNumber(db.agents || 0)}</strong></li>
                    <li>Devices: <strong>${formatNumber(db.devices || 0)}</strong></li>
                    <li>Metrics rows: <strong>${formatNumber(db.metrics_snapshots || 0)}</strong></li>
                    <li>Sessions: <strong>${formatNumber(db.sessions || 0)}</strong></li>
                    <li>Users: <strong>${formatNumber(db.users || 0)}</strong></li>
                    <li>Audit entries: <strong>${formatNumber(db.audit_entries || 0)}</strong></li>
                    <li>Artifacts: <strong>${formatNumber(db.release_artifacts || 0)}</strong> (${formatBytes(artifactsBytes)})</li>
                    <li>Total cache size: <strong>${formatBytes(cacheBytes)}</strong></li>
                </ul>
            ` : '<div class="muted-text">No DB stats available.</div>'}
        </div>
    `;
}

// Consumables donut chart colors matching the tier semantics
const CONSUMABLE_TIER_COLORS = {
    critical: '#f56565', // Red for critical
    low: '#ecc94b',      // Yellow/amber for low
    medium: '#48bb78',   // Green for medium
    high: '#4299e1',     // Blue for high
    unknown: '#718096',  // Gray for unknown
};

function renderConsumables(fleet) {
    const card = document.getElementById('metrics_consumables');
    if (!card) return;
    card.innerHTML = '<div class="card-title">Consumables</div>';
    if (!fleet || !fleet.consumables) {
        card.innerHTML += '<div class="muted-text">No consumable data yet.</div>';
        return;
    }
    const totals = fleet.totals || {};
    const consumables = fleet.consumables;
    const totalDevices = (consumables.critical || 0) + (consumables.low || 0) + (consumables.medium || 0) + (consumables.high || 0) + (consumables.unknown || 0);

    if (totalDevices === 0) {
        card.innerHTML += '<div class="muted-text">No devices with consumable data.</div>';
        return;
    }

    const tiers = [
        { key: 'critical', label: 'Critical (<10%)', value: consumables.critical || 0, color: CONSUMABLE_TIER_COLORS.critical },
        { key: 'low', label: 'Low (10-25%)', value: consumables.low || 0, color: CONSUMABLE_TIER_COLORS.low },
        { key: 'medium', label: 'Medium (25-50%)', value: consumables.medium || 0, color: CONSUMABLE_TIER_COLORS.medium },
        { key: 'high', label: 'High (>50%)', value: consumables.high || 0, color: CONSUMABLE_TIER_COLORS.high },
        { key: 'unknown', label: 'Unknown', value: consumables.unknown || 0, color: CONSUMABLE_TIER_COLORS.unknown },
    ];

    // Build donut chart container with legend
    card.innerHTML += `
        <div class="consumables-chart-container">
            <canvas id="consumables_donut_chart" width="160" height="160"></canvas>
            <div class="consumables-legend">
                ${tiers.map(tier => `
                    <div class="consumables-legend-item">
                        <span class="consumables-legend-swatch" style="background:${tier.color};"></span>
                        <span class="consumables-legend-label">${tier.label}:</span>
                        <strong>${formatNumber(tier.value)}</strong>
                        <span class="consumables-legend-pct">(${Math.round((tier.value / totalDevices) * 100)}%)</span>
                    </div>
                `).join('')}
            </div>
        </div>
    `;

    // Draw the donut chart
    const canvas = document.getElementById('consumables_donut_chart');
    if (canvas) {
        drawConsumablesDonut(canvas, tiers, totalDevices);
    }
}

function drawConsumablesDonut(canvas, tiers, total) {
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const dpr = window.devicePixelRatio || 1;
    const size = 160;
    canvas.width = size * dpr;
    canvas.height = size * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, size, size);

    const centerX = size / 2;
    const centerY = size / 2;
    const outerRadius = 70;
    const innerRadius = 45;

    let startAngle = -Math.PI / 2; // Start from top

    // Draw segments
    tiers.forEach(tier => {
        if (tier.value === 0) return;
        const sliceAngle = (tier.value / total) * Math.PI * 2;
        const endAngle = startAngle + sliceAngle;

        ctx.beginPath();
        ctx.arc(centerX, centerY, outerRadius, startAngle, endAngle);
        ctx.arc(centerX, centerY, innerRadius, endAngle, startAngle, true);
        ctx.closePath();
        ctx.fillStyle = tier.color;
        ctx.fill();

        startAngle = endAngle;
    });

    // Draw center text showing total
    ctx.fillStyle = 'rgba(255,255,255,0.9)';
    ctx.font = 'bold 20px sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(formatNumber(total), centerX, centerY - 6);
    ctx.font = '10px sans-serif';
    ctx.fillStyle = 'rgba(255,255,255,0.6)';
    ctx.fillText('devices', centerX, centerY + 10);
}

function renderMetricsActivity(aggregated) {
    const card = document.getElementById('metrics_activity');
    if (!card) return;
    card.innerHTML = '<div class="card-title">Activity</div>';
    if (!aggregated || !aggregated.fleet) {
        card.innerHTML += '<div class="muted-text">No activity yet.</div>';
        return;
    }

    const totals = aggregated.fleet.totals || {};
    const history = aggregated.fleet.history?.total_impressions || [];
    const lastPoint = history[history.length - 1];
    const periodTotal = history.reduce((sum, pt) => sum + Number(pt?.value || 0), 0);
    const lastTimestamp = lastPoint ? formatDateTime(lastPoint.timestamp) : 'n/a';

    card.innerHTML += `
        <div style="display:flex;flex-direction:column;gap:8px;font-size:13px;">
            <div>Total lifetime pages: <strong>${formatNumber(totals.page_count || 0)}</strong></div>
            <div>Period pages (${metricsRangeLabel(metricsVM.range)}): <strong>${formatNumber(periodTotal)}</strong></div>
            <div>Last metric: <strong>${escapeHtml(lastTimestamp)}</strong></div>
        </div>
    `;
}

function renderMetricsError(err) {
    const statsEl = document.getElementById('metrics_stats');
    if (statsEl) {
        statsEl.innerHTML = `<div class="metric-card" style="color:var(--danger);">Failed to load metrics: ${escapeHtml(err?.message || err)}</div>`;
    }
    const chartsEl = document.getElementById('metrics_chart_grid');
    if (chartsEl) {
        chartsEl.innerHTML = '<div class="metric-chart-card" style="color:var(--danger);">Unable to render charts.</div>';
    }
}

function getMetricsRangeWindow(range) {
    return METRICS_RANGE_WINDOWS[range] || METRICS_RANGE_WINDOWS[METRICS_DEFAULT_RANGE];
}

function metricsRangeLabel(range) {
    switch (range) {
        case '5m': return '5 minutes';
        case '15m': return '15 minutes';
        case '30m': return '30 minutes';
        case '1h': return '1 hour';
        case '6h': return '6 hours';
        case '12h': return '12 hours';
        case '24h': return '24 hours';
        case '7d': return '7 days';
        case '30d': return '30 days';
        case '90d': return '90 days';
        case '365d': return '1 year';
        default: return range;
    }
}

function initMetricsRangeControls() {
    const controls = document.getElementById('metrics_range_controls');
    if (!controls || controls._metricsBound) return;
    controls._metricsBound = true;
    controls.addEventListener('click', (evt) => {
        const btn = evt.target.closest('[data-range]');
        if (!btn) return;
        setMetricsRange(btn.getAttribute('data-range'));
    });
    updateMetricsRangeButtons();
}

/**
 * Initialize metrics filter controls (tenant, agent, device dropdowns).
 * Cascading filter pattern:
 * - Global admins: Show tenant filter first → agent filter appears when tenant selected → device filter appears when agent selected
 * - Non-admin users: Show agent filter first (scoped to their tenant) → device filter appears when agent selected
 */
function initMetricsFilterControls() {
    const filtersContainer = document.getElementById('metrics_filters');
    const tenantSelect = document.getElementById('metrics_tenant_filter');
    const agentSelect = document.getElementById('metrics_agent_filter');
    const deviceSelect = document.getElementById('metrics_device_filter');

    if (!filtersContainer) return;
    if (filtersContainer._filtersBound) return;
    filtersContainer._filtersBound = true;

    // Cascading filter setup: hide downstream filters initially
    if (isGlobalAdmin()) {
        // Global admins start with tenant filter only
        if (tenantSelect) {
            tenantSelect.addEventListener('change', onMetricsTenantChange);
        }
        if (agentSelect) {
            agentSelect.style.display = 'none'; // Hidden until tenant selected
            agentSelect.addEventListener('change', onMetricsAgentChange);
        }
    } else {
        // Non-global users: hide tenant filter, show agent filter
        if (tenantSelect) {
            tenantSelect.style.display = 'none';
        }
        if (agentSelect) {
            agentSelect.addEventListener('change', onMetricsAgentChange);
        }
    }

    // Device filter always hidden until agent selected
    if (deviceSelect) {
        deviceSelect.style.display = 'none';
        deviceSelect.addEventListener('change', onMetricsDeviceChange);
    }

    // Initial population of filters
    populateMetricsFilters();
}

/**
 * Populate metrics filter dropdowns with available options.
 * Cascading pattern:
 * - Global admins: Populate tenant only; agent filter populated when tenant selected
 * - Non-admin users: Populate agent filter immediately (scoped to their tenant)
 */
async function populateMetricsFilters() {
    const tenantSelect = document.getElementById('metrics_tenant_filter');
    const agentSelect = document.getElementById('metrics_agent_filter');
    const deviceSelect = document.getElementById('metrics_device_filter');

    // Populate tenant filter (global admins only)
    if (tenantSelect && isGlobalAdmin()) {
        try {
            const tenantsResp = await fetch('/api/v1/tenants');
            if (tenantsResp.ok) {
                const tenants = await tenantsResp.json();
                tenantSelect.innerHTML = '<option value="">All Tenants</option>';
                (tenants || []).forEach(t => {
                    const opt = document.createElement('option');
                    opt.value = t.id;
                    opt.textContent = t.name || t.id;
                    tenantSelect.appendChild(opt);
                });
            }
        } catch (err) {
            window.__pm_shared.warn('Failed to load tenants for metrics filter', err);
        }
        // Agent filter stays hidden until tenant is selected (for global admins)
        return;
    }

    // For non-global users: populate agent filter immediately (scoped to their tenant)
    if (agentSelect) {
        try {
            const agentsResp = await fetch('/api/v1/agents/list');
            if (agentsResp.ok) {
                const agents = await agentsResp.json();
                agentSelect.innerHTML = '<option value="">All Agents</option>';
                (agents || []).forEach(a => {
                    const opt = document.createElement('option');
                    opt.value = a.agent_id;
                    opt.textContent = a.name || a.hostname || a.agent_id;
                    agentSelect.appendChild(opt);
                });
            }
        } catch (err) {
            window.__pm_shared.warn('Failed to load agents for metrics filter', err);
        }
    }

    // Device filter starts empty - populated when agent is selected
    if (deviceSelect) {
        deviceSelect.innerHTML = '<option value="">All Devices</option>';
        deviceSelect.disabled = true;
    }
}

function onMetricsTenantChange(evt) {
    const tenantId = evt.target.value;
    metricsVM.filters.tenantId = tenantId;
    metricsVM.filters.agentId = '';
    metricsVM.filters.deviceSerial = '';

    // Update visual state
    evt.target.classList.toggle('has-value', !!tenantId);

    // Reset agent/device filters
    const agentSelect = document.getElementById('metrics_agent_filter');
    const deviceSelect = document.getElementById('metrics_device_filter');
    if (agentSelect) {
        agentSelect.value = '';
        agentSelect.classList.remove('has-value');
        // Show agent filter when tenant selected, hide when "All Tenants"
        agentSelect.style.display = tenantId ? '' : 'none';
    }
    if (deviceSelect) {
        deviceSelect.value = '';
        deviceSelect.innerHTML = '<option value="">All Devices</option>';
        deviceSelect.disabled = true;
        deviceSelect.classList.remove('has-value');
        // Hide device filter when tenant changes
        deviceSelect.style.display = 'none';
    }

    // Reload metrics with new filter
    loadMetrics(true);

    // Re-populate agent filter for selected tenant
    if (tenantId) {
        populateAgentFilterForTenant(tenantId);
    }
}

async function populateAgentFilterForTenant(tenantId) {
    const agentSelect = document.getElementById('metrics_agent_filter');
    if (!agentSelect) return;

    try {
        let url = '/api/v1/agents/list';
        if (tenantId) {
            url += `?tenant_id=${encodeURIComponent(tenantId)}`;
        }
        const resp = await fetch(url);
        if (resp.ok) {
            const agents = await resp.json();
            agentSelect.innerHTML = '<option value="">All Agents</option>';
            (agents || []).forEach(a => {
                const opt = document.createElement('option');
                opt.value = a.agent_id;
                opt.textContent = a.name || a.hostname || a.agent_id;
                agentSelect.appendChild(opt);
            });
        }
    } catch (err) {
        window.__pm_shared.warn('Failed to reload agents for tenant', err);
    }
}

function onMetricsAgentChange(evt) {
    const agentId = evt.target.value;
    metricsVM.filters.agentId = agentId;
    metricsVM.filters.deviceSerial = '';

    // Update visual state
    evt.target.classList.toggle('has-value', !!agentId);

    const deviceSelect = document.getElementById('metrics_device_filter');
    if (deviceSelect) {
        deviceSelect.value = '';
        deviceSelect.classList.remove('has-value');

        if (agentId) {
            // Show and populate device filter for selected agent
            deviceSelect.style.display = '';
            populateDeviceFilterForAgent(agentId);
        } else {
            // Hide device filter when "All Agents" is selected
            deviceSelect.style.display = 'none';
            deviceSelect.innerHTML = '<option value="">All Devices</option>';
            deviceSelect.disabled = true;
        }
    }

    // Reload metrics with new filter
    loadMetrics(true);
}

async function populateDeviceFilterForAgent(agentId) {
    const deviceSelect = document.getElementById('metrics_device_filter');
    if (!deviceSelect) return;

    deviceSelect.disabled = false;
    deviceSelect.innerHTML = '<option value="">All Devices</option>';

    try {
        // Fetch all devices and filter by agent_id client-side
        const resp = await fetch('/api/v1/devices/index');
        if (resp.ok) {
            const data = await resp.json();
            const devices = Array.isArray(data) ? data : (data.devices || []);
            // Filter to only devices belonging to the selected agent
            const agentDevices = devices.filter(d => d.agent_id === agentId);
            agentDevices.forEach(d => {
                const opt = document.createElement('option');
                opt.value = d.serial;
                opt.textContent = d.model || d.serial;
                if (d.ip) {
                    opt.textContent += ` (${d.ip})`;
                }
                deviceSelect.appendChild(opt);
            });

            if (agentDevices.length === 0) {
                deviceSelect.innerHTML = '<option value="">No devices</option>';
                deviceSelect.disabled = true;
            }
        }
    } catch (err) {
        window.__pm_shared.warn('Failed to load devices for agent', err);
        deviceSelect.disabled = true;
    }
}

function onMetricsDeviceChange(evt) {
    const serial = evt.target.value;
    metricsVM.filters.deviceSerial = serial;

    // Update visual state
    evt.target.classList.toggle('has-value', !!serial);

    // Reload metrics with new filter
    loadMetrics(true);
}

function setMetricsRange(range) {
    if (!range || range === metricsVM.range) return;
    metricsVM.range = range;
    updateMetricsRangeButtons();
    loadMetrics(true);
}

function updateMetricsRangeButtons() {
    const controls = document.getElementById('metrics_range_controls');
    if (!controls) return;
    controls.querySelectorAll('[data-range]').forEach(btn => {
        if (btn.getAttribute('data-range') === metricsVM.range) {
            btn.classList.add('active');
        } else {
            btn.classList.remove('active');
        }
    });
}

function isMetricsTabActive() {
    const tab = document.querySelector('[data-tab="metrics"]');
    return tab && !tab.classList.contains('hidden');
}

/**
 * Handle live metrics snapshot from SSE for real-time chart updates.
 * Appends the new snapshot to the timeseries data and re-renders charts.
 */
function handleLiveMetricsSnapshot(snapshot) {
    // Only update if on metrics tab and we have existing data to append to
    if (!isMetricsTabActive() || !serverMetricsVM.timeseries) {
        return;
    }

    const ts = serverMetricsVM.timeseries;
    const snapshots = ts.snapshots || [];
    const chartSeries = ts.chart_series || {};

    // Convert snapshot timestamp to milliseconds for chart compatibility
    const timestampMs = new Date(snapshot.timestamp).getTime();
    const fleet = snapshot.fleet || {};
    const server = snapshot.server || {};

    // Append to snapshots array
    snapshots.push({
        timestamp: snapshot.timestamp,
        tier: snapshot.tier || 'raw',
        fleet: fleet,
        server: server,
    });

    // Append to chart_series arrays for each metric
    // Backend uses compact {t, v} format, so we append in the same format
    const appendPoint = (key, value) => {
        if (chartSeries[key] && Array.isArray(chartSeries[key])) {
            // Use the same format as existing points (compact {t, v})
            chartSeries[key].push({ t: timestampMs, v: value ?? 0 });
        }
    };

    // Fleet metrics
    appendPoint('agents', fleet.total_agents);
    appendPoint('devices', fleet.total_devices);
    appendPoint('devices_online', fleet.devices_online);
    appendPoint('devices_offline', fleet.devices_offline);
    appendPoint('devices_error', fleet.devices_error);
    appendPoint('agents_ws', fleet.agents_ws);
    appendPoint('agents_http', fleet.agents_http);
    appendPoint('agents_offline', fleet.agents_offline);
    appendPoint('toner_high', fleet.toner_high);
    appendPoint('toner_medium', fleet.toner_medium);
    appendPoint('toner_low', fleet.toner_low);
    appendPoint('toner_critical', fleet.toner_critical);
    appendPoint('toner_unknown', fleet.toner_unknown);
    appendPoint('total_pages', fleet.total_pages);
    appendPoint('color_pages', fleet.color_pages);
    appendPoint('mono_pages', fleet.mono_pages);
    appendPoint('scan_count', fleet.scan_count);
    appendPoint('ws_connections', fleet.ws_connections);

    // Server metrics
    appendPoint('goroutines', server.goroutines);
    appendPoint('heap_alloc', server.heap_alloc_mb);
    appendPoint('db_size', server.db_size_bytes);

    // Prune old data points outside current time window
    const rangeWindow = getMetricsRangeWindow(metricsVM.range);
    const cutoff = Date.now() - rangeWindow;

    // Prune snapshots
    while (snapshots.length > 0 && new Date(snapshots[0].timestamp).getTime() < cutoff) {
        snapshots.shift();
    }

    // Prune chart_series (points use {t, v} format where t is timestamp in ms)
    for (const key in chartSeries) {
        const arr = chartSeries[key];
        if (Array.isArray(arr)) {
            while (arr.length > 0 && arr[0].t < cutoff) {
                arr.shift();
            }
        }
    }

    // Re-render the time-series charts (throttled to avoid excessive redraws)
    throttledRenderLiveMetrics();
}

// Throttle live chart updates to max once per second for smooth performance
let _liveMetricsRenderTimer = null;
function throttledRenderLiveMetrics() {
    if (_liveMetricsRenderTimer) return;
    _liveMetricsRenderTimer = setTimeout(() => {
        _liveMetricsRenderTimer = null;
        renderConsumablesTimeSeriesCharts(serverMetricsVM.timeseries);
        renderAgentFleetCharts(serverMetricsVM.timeseries);
        renderServerTimeSeriesCharts(serverMetricsVM.timeseries);
    }, 1000);
}

function isDevicesTabActive() {
    const tab = document.querySelector('[data-tab="devices"]');
    return tab && !tab.classList.contains('hidden');
}

function toSeriesPoints(arr) {
    if (!Array.isArray(arr)) return [];
    return arr.map(pt => {
        const timestamp = pt?.timestamp || pt?.Timestamp;
        const value = Number(pt?.value ?? pt?.Value ?? 0);
        const timeMs = timestamp ? new Date(timestamp).getTime() : NaN;
        return (Number.isFinite(timeMs)) ? { time: timeMs, value } : null;
    }).filter(Boolean);
}

function calculateThroughput(series) {
    if (!Array.isArray(series) || series.length === 0) return 0;
    const total = series.reduce((sum, pt) => sum + Number(pt?.value || pt?.Value || 0), 0);
    const hours = getMetricsRangeWindow(metricsVM.range) / (60 * 60 * 1000);
    return hours > 0 ? total / hours : 0;
}

// Chart functions (drawFleetChart, drawFleetChartDualAxis) are now in utils/charts.js
// formatDurationSec, formatDateShort, formatTimeShort are now in utils/formatters.js

function renderLogs(logs) {
    // Parse and normalize log lines
    let lines = [];
    let isAppend = false;
    if (logs && logs.logs && Array.isArray(logs.logs)) {
        lines = logs.logs;
        isAppend = Boolean(logs.append);
    } else if (Array.isArray(logs)) {
        lines = logs;
    } else if (typeof logs === 'string') {
        lines = logs.split('\n').filter(l => l.trim());
    }

    // Parse log lines into structured entries (if not already parsed)
    // Check if first item is already a parsed entry (has 'raw' property)
    if (lines.length > 0 && typeof lines[0] === 'object' && lines[0].raw !== undefined) {
        currentLogLines = lines; // Already parsed
    } else {
        currentLogLines = lines.map(parseLogLine);
    }

    // Sync with logsState if entries came from loadLogs
    if (logsState.entries.length > 0) {
        currentLogLines = logsState.entries;
    }

    // Render based on current view mode (pass isAppend to skip auto-scroll)
    if (activeLogViewMode === 'table') {
        renderLogsTable(currentLogLines, isAppend);
    } else {
        renderLogsRaw(currentLogLines, isAppend);
    }
}

/**
 * Parse a single log line into structured components
 * Format: 2006-01-02T15:04:05-07:00 [LEVEL] message key=value key=value
 */
function parseLogLine(line) {
    if (!line || typeof line !== 'string') {
        return { raw: String(line || ''), timestamp: null, level: '', message: line || '', context: {} };
    }

    const entry = { raw: line, timestamp: null, level: '', message: '', context: {} };

    // Match timestamp at start: ISO 8601 format
    const timestampMatch = line.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[+-]\d{2}:\d{2}|Z)?)\s*/);
    if (timestampMatch) {
        try {
            entry.timestamp = new Date(timestampMatch[1]);
        } catch (e) {
            entry.timestamp = null;
        }
        line = line.slice(timestampMatch[0].length);
    }

    // Match level in brackets: [ERROR], [WARN], [INFO], [DEBUG], [TRACE]
    const levelMatch = line.match(/^\[(\w+)\]\s*/);
    if (levelMatch) {
        entry.level = levelMatch[1].toUpperCase();
        line = line.slice(levelMatch[0].length);
    }

    // Extract key=value context pairs from the end
    // Work backwards to find context pairs
    const contextPairs = [];
    const kvPattern = /\s+(\w+)=("[^"]*"|\S+)$/;
    let remaining = line;
    let match;
    while ((match = remaining.match(kvPattern)) !== null) {
        let value = match[2];
        // Remove quotes if present
        if (value.startsWith('"') && value.endsWith('"')) {
            value = value.slice(1, -1);
        }
        contextPairs.unshift({ key: match[1], value: value });
        remaining = remaining.slice(0, match.index);
    }

    entry.message = remaining.trim();
    contextPairs.forEach(pair => {
        entry.context[pair.key] = pair.value;
    });

    return entry;
}

function renderLogsTable(entries, isAppend = false) {
    const tbody = document.getElementById('log_table_body');
    if (!tbody) {
        window.__pm_shared.warn('renderLogsTable: tbody not found');
        return;
    }

    // Update total count from server state
    const entryCountEl = document.getElementById('logs_entry_count');
    if (entryCountEl) {
        entryCountEl.textContent = logsState.total || (entries ? entries.length : 0);
    }

    if (!entries || entries.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" class="log-table-empty">No logs available</td></tr>';
        updateLogsShowingCount(0);
        return;
    }

    // Server-side filtering is already applied, just render all entries
    updateLogsShowingCount(entries.length);

    // Build rows with load-more sentinel at the end (for loading older logs)
    const rows = entries.map(entry => {
        const timeHtml = entry.timestamp
            ? `<span class="log-time-date">${formatDateShort(entry.timestamp)}</span>${formatTimeShort(entry.timestamp)}`
            : '<span class="log-time">—</span>';

        const levelClass = entry.level ? `log-level-${entry.level.toLowerCase()}` : '';
        const levelHtml = entry.level
            ? `<span class="log-level ${levelClass}">${escapeHtml(entry.level)}</span>`
            : '';

        const contextHtml = Object.keys(entry.context).length > 0
            ? Object.entries(entry.context).map(([k, v]) =>
                `<span class="log-context-tag"><span class="tag-key">${escapeHtml(k)}</span>=<span class="tag-value">${escapeHtml(String(v))}</span></span>`
            ).join('')
            : '';

        return `<tr>
            <td class="log-time">${timeHtml}</td>
            <td>${levelHtml}</td>
            <td class="log-message">${escapeHtml(entry.message)}</td>
            <td class="log-context">${contextHtml}</td>
        </tr>`;
    });

    // Add load-more sentinel at the end if more logs are available, or end indicator
    if (logsState.hasMore) {
        rows.push(`<tr id="logs_load_more_sentinel" class="logs-load-sentinel">
            <td colspan="4" style="text-align:center;padding:16px;">
                <div class="loading-spinner" style="display:inline-block;margin-right:8px;"></div>
                <span class="muted-text">Loading older logs...</span>
            </td>
        </tr>`);
    } else if (entries.length > 0) {
        rows.push(`<tr class="logs-end-marker">
            <td colspan="4" style="text-align:center;padding:12px;color:var(--muted);font-style:italic;">
                — End of logs —
            </td>
        </tr>`);
    }

    tbody.innerHTML = rows.join('');

    // Auto-scroll to bottom if not paused and NOT appending older logs
    if (!isAppend) {
        const pauseCheckbox = document.getElementById('pause_autoscroll');
        if (!pauseCheckbox || !pauseCheckbox.checked) {
            const container = document.getElementById('log_table_container');
            if (container) {
                container.scrollTop = container.scrollHeight;
            }
        }
    }
}

function renderLogsRaw(entries, isAppend = false) {
    const container = document.getElementById('log');
    if (!container) {
        window.__pm_shared.warn('renderLogsRaw: log element not found');
        return;
    }

    // Update total count from server state
    const entryCountEl = document.getElementById('logs_entry_count');
    if (entryCountEl) {
        entryCountEl.textContent = logsState.total || (entries ? entries.length : 0);
    }

    if (!entries || entries.length === 0) {
        container.textContent = 'No logs available';
        updateLogsShowingCount(0);
        return;
    }

    // Server-side filtering is already applied
    updateLogsShowingCount(entries.length);

    // Build content with load-more indicator or end marker
    let content = entries.map(e => e.raw).join('\n');
    if (logsState.hasMore) {
        content += '\n\n--- Scroll down to load older logs ---';
    } else if (entries.length > 0) {
        content += '\n\n— End of logs —';
    }
    container.textContent = content;

    // Auto-scroll to bottom if not paused and NOT appending older logs
    if (!isAppend) {
        const pauseCheckbox = document.getElementById('pause_autoscroll');
        if (!pauseCheckbox || !pauseCheckbox.checked) {
            container.scrollTop = container.scrollHeight;
        }
    }
}

function updateLogsShowingCount(count) {
    const showingCountEl = document.getElementById('logs_showing_count');
    if (showingCountEl) {
        showingCountEl.textContent = count;
    }
}

// Setup IntersectionObserver for logs infinite scroll (load older logs when sentinel becomes visible)
function setupLogsInfiniteScroll() {
    cleanupLogsInfiniteScroll();

    const sentinel = document.getElementById('logs_load_more_sentinel');
    if (!sentinel || !logsState.hasMore) return;

    logsState.scrollObserver = new IntersectionObserver((entries) => {
        entries.forEach(entry => {
            if (entry.isIntersecting && !logsState.loading && logsState.hasMore) {
                // Enforce cooldown to prevent runaway requests
                const now = Date.now();
                if (now - logsState.lastLoadTime < logsState.cooldownMs) {
                    return;
                }
                logsState.lastLoadTime = now;
                // Load older logs
                loadLogs({ append: true });
            }
        });
    }, {
        root: document.getElementById('log_table_container'),
        rootMargin: '100px',
        threshold: 0.1
    });

    logsState.scrollObserver.observe(sentinel);
}

function cleanupLogsInfiniteScroll() {
    if (logsState.scrollObserver) {
        logsState.scrollObserver.disconnect();
        logsState.scrollObserver = null;
    }
}

// formatDateShort and formatTimeShort are now in utils/formatters.js

function renderAuditLogs(entries, options = {}) {
    const container = document.getElementById('audit_logs_table');
    if (!container) {
        window.__pm_shared.warn('renderAuditLogs: container not found');
        return;
    }

    const append = Boolean(options.append);

    if (!Array.isArray(entries) || entries.length === 0) {
        const message = options.filtersActive
            ? 'No audit entries match the current filters.'
            : 'No audit entries in this window.';
        container.innerHTML = `<div class="muted-text" style="padding:12px;">${message}</div>`;
        cleanupAuditInfiniteScroll();
        return;
    }

    // Progressive rendering - only render a page at a time
    if (!append) {
        auditRenderState.displayed = 0;
        // Initialize the table structure
        container.innerHTML = `
            <table class="simple-table">
                <thead>
                    <tr>
                        <th>Timestamp</th>
                        <th>Actor</th>
                        <th>Action</th>
                        <th>Target</th>
                        <th>Details</th>
                    </tr>
                </thead>
                <tbody></tbody>
            </table>
        `;
    }

    const tbody = container.querySelector('tbody');
    if (!tbody) return;

    const startIdx = auditRenderState.displayed;
    const endIdx = Math.min(startIdx + auditRenderState.pageSize, entries.length);
    const pageEntries = entries.slice(startIdx, endIdx);

    // Remove existing sentinel
    const existingSentinel = document.getElementById('audit_load_more_sentinel');
    if (existingSentinel) existingSentinel.remove();

    const rows = pageEntries.map(entry => {
        const ts = escapeHtml(formatDateTime(entry.timestamp));
        const rel = escapeHtml(formatRelativeTime(entry.timestamp));
        const actorName = entry.actor_name || entry.actor_id || '—';
        const actorMetaParts = [];
        if (entry.actor_type) {
            actorMetaParts.push((entry.actor_type || '').toUpperCase());
        }
        if (entry.actor_id) {
            actorMetaParts.push(entry.actor_id);
        }
        const actorMeta = actorMetaParts.length ? `<div class="audit-actor-meta">${escapeHtml(actorMetaParts.join(' • '))}</div>` : '';

        const severity = String(entry.severity || 'info').toLowerCase();
        const severityBadge = `<span class="badge ${getSeverityBadgeClass(severity)}">${escapeHtml(severity.toUpperCase())}</span>`;

        const actionLabel = escapeHtml(entry.action || '—');

        const targetPrimaryParts = [];
        if (entry.target_type) targetPrimaryParts.push(entry.target_type);
        if (entry.target_id) targetPrimaryParts.push(entry.target_id);
        const targetPrimary = targetPrimaryParts.length ? escapeHtml(targetPrimaryParts.join(' • ')) : '—';
        const tenantTag = entry.tenant_id ? `<div class="audit-target-meta">Tenant: ${escapeHtml(entry.tenant_id)}</div>` : '';

        const detailLines = [];
        if (entry.details) {
            detailLines.push(entry.details);
        }
        if (entry.ip_address) {
            detailLines.push(`IP: ${entry.ip_address}`);
        }
        if (entry.user_agent) {
            detailLines.push(`User-Agent: ${entry.user_agent}`);
        }
        if (entry.request_id) {
            detailLines.push(`Request: ${entry.request_id}`);
        }
        const metadataText = formatAuditMetadata(entry.metadata);
        const detailText = detailLines.length ? escapeHtml(detailLines.join('\n')) : '—';
        const metadataBlock = metadataText ? `<pre class="audit-metadata">${escapeHtml(metadataText)}</pre>` : '';

        return `
            <tr>
                <td>
                    <div class="table-primary">${ts}</div>
                    <div class="muted-text">${rel}</div>
                </td>
                <td>
                    <div class="table-primary">${escapeHtml(actorName)}</div>
                    ${actorMeta}
                </td>
                <td>
                    <div class="table-primary">${actionLabel}</div>
                    <div class="audit-detail-meta">${severityBadge}</div>
                </td>
                <td>
                    <div class="table-primary">${targetPrimary}</div>
                    ${tenantTag}
                </td>
                <td>
                    <div class="audit-details">${detailText}</div>
                    ${metadataBlock}
                </td>
            </tr>
        `;
    }).join('');

    tbody.insertAdjacentHTML('beforeend', rows);
    auditRenderState.displayed = endIdx;

    // Add sentinel row if more items available
    if (endIdx < entries.length) {
        const sentinelRow = document.createElement('tr');
        sentinelRow.id = 'audit_load_more_sentinel';
        sentinelRow.className = 'audit-load-sentinel';
        sentinelRow.innerHTML = '<td colspan="5" style="text-align:center;padding:16px;"><div class="loading-spinner" style="display:inline-block;margin-right:8px;"></div><span class="muted-text">Loading more audit entries...</span></td>';
        tbody.appendChild(sentinelRow);
        setupAuditInfiniteScroll();
    } else {
        cleanupAuditInfiniteScroll();
    }
}

// Setup IntersectionObserver for audit logs infinite scroll
function setupAuditInfiniteScroll() {
    cleanupAuditInfiniteScroll();

    const sentinel = document.getElementById('audit_load_more_sentinel');
    if (!sentinel) return;

    auditRenderState.observer = new IntersectionObserver((entries) => {
        entries.forEach(entry => {
            if (entry.isIntersecting && auditRenderState.displayed < auditRenderState.filteredEntries.length) {
                loadMoreAuditLogs();
            }
        });
    }, {
        root: null,
        rootMargin: '200px',
        threshold: 0
    });

    auditRenderState.observer.observe(sentinel);
}

// Cleanup the audit logs infinite scroll observer
function cleanupAuditInfiniteScroll() {
    if (auditRenderState.observer) {
        auditRenderState.observer.disconnect();
        auditRenderState.observer = null;
    }
}

// Load more audit logs for infinite scroll
function loadMoreAuditLogs() {
    renderAuditLogs(auditRenderState.filteredEntries, { append: true, filtersActive: hasActiveAuditFilters() });
}

function getSeverityBadgeClass(severity) {
    switch (severity) {
        case 'error':
            return 'badge-error';
        case 'warn':
        case 'warning':
            return 'badge-warn';
        default:
            return 'badge-info';
    }
}

function formatAuditMetadata(metadata) {
    if (!metadata || typeof metadata !== 'object') {
        return '';
    }
    try {
        return JSON.stringify(metadata, null, 2);
    } catch (err) {
        window.__pm_shared.warn('formatAuditMetadata failed', err);
        return '';
    }
}

// ====== Modal Handlers ======
// Agent Details Modal (overlay)
document.getElementById('agent_details_close_x')?.addEventListener('click', () => {
    document.getElementById('agent_details_overlay').style.display = 'none';
});
document.getElementById('agent_details_close')?.addEventListener('click', () => {
    document.getElementById('agent_details_overlay').style.display = 'none';
});

// Click outside modal to close
window.addEventListener('click', (event) => {
    const agentOverlay = document.getElementById('agent_details_overlay');
    const confirmModal = document.getElementById('confirm_modal');

    if (event.target === agentOverlay) {
        agentOverlay.style.display = 'none';
    }
    if (event.target === confirmModal) {
        confirmModal.style.display = 'none';
    }
});

// NOTE: Delegated click handler for data-action buttons is in common/web/cards.js
// That handler calls window.__pm_shared.* functions which are exported below.
// Do NOT add a duplicate handler here - it causes actions to fire twice.

// Toggle visibility of advanced settings controls
function toggleAdvancedSettings() {
    try {
        const enabled = document.getElementById('settings_advanced_toggle')?.checked || false;
        // Elements marked as advanced-setting should be shown/hidden
        document.querySelectorAll('.advanced-setting').forEach(el => {
            if (enabled) {
                el.style.display = '';
            } else {
                el.style.display = 'none';
            }
        });

        // Textareas or other advanced inputs may use a dedicated class
        document.querySelectorAll('.advanced-setting-textarea').forEach(el => {
            if (enabled) el.style.display = '';
            else el.style.display = 'none';
        });

        // Persist preference
        try { localStorage.setItem('settings_advanced', enabled ? 'true' : 'false'); } catch (e) { }
    } catch (e) {
        window.__pm_shared.error('toggleAdvancedSettings failed', e);
        throw e;
    }
}

// ====== Add Agent Modal UI ======
function initAddAgentUI() {
    if (addAgentUIInitialized) return;
    addAgentUIInitialized = true;
    // Wire header Join button if present
    const joinBtn = document.getElementById('join_token_btn');
    if (joinBtn) joinBtn.addEventListener('click', () => openAddAgentModal({}));

    // Wire modal chrome
    const closeX = document.getElementById('add_agent_close_x');
    const cancelBtn = document.getElementById('add_agent_cancel');
    const primaryBtn = document.getElementById('add_agent_primary');

    if (closeX) closeX.addEventListener('click', closeAddAgentModal);
    if (cancelBtn) cancelBtn.addEventListener('click', closeAddAgentModal);
    if (primaryBtn) primaryBtn.addEventListener('click', handleAddAgentPrimary);
}

function openAddAgentModal(opts) {
    // opts: { tenantID?: string }
    window._addAgentState = {
        step: 1,
        tenantID: opts && opts.tenantID ? opts.tenantID : null,
        ttl: 60,
        one_time: true,
        token: null,
        mode: 'token',
        platform: 'windows',
        format: 'zip',
        arch: 'amd64'
    };
    renderAddAgentStep(1);
    const modal = document.getElementById('add_agent_modal');
    if (modal) { modal.style.display = 'flex'; document.body.style.overflow = 'hidden'; }
}

function closeAddAgentModal() {
    const modal = document.getElementById('add_agent_modal');
    if (modal) { modal.style.display = 'none'; document.body.style.overflow = ''; }
    // Clean up floating dropdown if it exists
    const dropdown = document.getElementById('add_agent_options_dropdown');
    if (dropdown) dropdown.remove();
    try { delete window._addAgentState; } catch (e) { }
}

async function handleAddAgentPrimary() {
    const st = window._addAgentState || { step: 1 };
    if (st.step === 1) {
        // Read form values
        const tenantSel = document.getElementById('add_agent_tenant');
        const ttlEl = document.getElementById('add_agent_ttl');
        const oneTimeEl = document.getElementById('add_agent_one_time');
        const tenantID = tenantSel ? tenantSel.value : (st.tenantID || null);
        const ttl = ttlEl ? parseInt(ttlEl.value, 10) || 60 : 60;
        const one_time = oneTimeEl ? oneTimeEl.checked : true;
        const platformSel = document.getElementById('add_agent_platform');
        const formatSel = document.getElementById('add_agent_format');
        const archSel = document.getElementById('add_agent_arch');
        const platform = platformSel ? platformSel.value : (st.platform || 'windows');
        const format = formatSel ? formatSel.value : (st.format || 'zip');
        const arch = archSel ? archSel.value : (st.arch || 'amd64');

        st.tenantID = tenantID;
        st.ttl = ttl;
        st.one_time = one_time;
        st.platform = platform;
        st.format = format;
        st.arch = arch;

        // Basic validation
        if (!tenantID) { window.__pm_shared.showAlert('Please select a tenant (customer) to assign this token to.', 'Missing tenant', true, false); return; }

        // Decide whether user wants a raw token or a generated bootstrap script
        const selectedActionEl = document.querySelector('input[name="add_agent_action"]:checked');
        const action = selectedActionEl ? selectedActionEl.value : 'token';
        if (action === 'token') {
            // Create join token
            try {
                const payload = { tenant_id: tenantID, ttl_minutes: ttl, one_time: one_time };
                const r = await fetch('/api/v1/join-token', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
                if (!r.ok) throw new Error(await r.text());
                const data = await r.json();
                st.token = data.token;
                st.script = null;
                st.mode = 'token';
                st.step = 2;
                window._addAgentState = st;
                renderAddAgentStep(2);
            } catch (err) {
                window.__pm_shared.showAlert('Failed to create join token: ' + (err && err.message ? err.message : err), 'Error', true, false);
            }
        } else if (action === 'script') {
            // Generate bootstrap script via server packages API
            try {
                const payload = { tenant_id: tenantID, platform: platform, installer_type: 'script', ttl_minutes: ttl };
                const r = await fetch('/api/v1/packages', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
                if (!r.ok) {
                    throw new Error(await r.text());
                }
                // Handle JSON or plain text responses
                const ct = (r.headers.get('content-type') || '').toLowerCase();
                let scriptText = '';
                let filename = 'bootstrap.sh';
                let downloadURL = null;
                let oneLiner = null;
                if (ct.includes('application/json')) {
                    const data = await r.json();
                    scriptText = data.script || '';
                    filename = data.filename || filename;
                    downloadURL = data.download_url || null;
                    oneLiner = data.one_liner || null;
                } else {
                    scriptText = await r.text();
                    const cd = r.headers.get('content-disposition');
                    if (cd) {
                        const m = cd.match(/filename="?([^";]+)"?/);
                        if (m && m[1]) filename = m[1];
                    } else if (platform === 'windows') filename = 'bootstrap.ps1';
                    else if (platform === 'darwin') filename = 'bootstrap.sh';
                }

                st.script = scriptText;
                st.scriptFilename = filename;
                st.scriptDownloadURL = downloadURL;
                st.oneLiner = oneLiner;
                st.token = null;
                st.mode = 'script';
                st.step = 2;
                window._addAgentState = st;
                renderAddAgentStep(2);
            } catch (err) {
                window.__pm_shared.showAlert('Failed to generate bootstrap script: ' + (err && err.message ? err.message : err), 'Error', true, false);
            }
        } else if (action === 'email') {
            // Send bootstrap script via email
            const emailEl = document.getElementById('add_agent_email');
            const emailAddr = emailEl ? emailEl.value.trim() : '';
            if (!emailAddr) {
                window.__pm_shared.showAlert('Please enter a recipient email address.', 'Missing email', true, false);
                return;
            }
            // Basic email validation
            if (!emailAddr.includes('@') || !emailAddr.includes('.')) {
                window.__pm_shared.showAlert('Please enter a valid email address.', 'Invalid email', true, false);
                return;
            }
            try {
                const payload = { tenant_id: tenantID, platform: platform, email: emailAddr, ttl_minutes: ttl };
                const r = await fetch('/api/v1/packages/send-email', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(payload) });
                if (!r.ok) {
                    const errText = await r.text();
                    throw new Error(errText);
                }
                const data = await r.json();
                st.emailSent = true;
                st.emailTo = emailAddr;
                st.mode = 'email';
                st.step = 2;
                window._addAgentState = st;
                renderAddAgentStep(2);
            } catch (err) {
                window.__pm_shared.showAlert('Failed to send deployment email: ' + (err && err.message ? err.message : err), 'Error', true, false);
            }
        } else {
            window.__pm_shared.showAlert('Unsupported onboarding option selected.', 'Error', true, false);
        }
    } else if (st.step === 2) {
        // Done
        closeAddAgentModal();
        // Optionally refresh tokens/tenants list
        try { loadTenants(); } catch (e) { }
    }
}

function renderAddAgentStep(step) {
    const indicator = document.getElementById('add_agent_step_indicator');
    const content = document.getElementById('add_agent_content');
    const primaryBtn = document.getElementById('add_agent_primary');
    if (!content) return;
    if (indicator) {
        if (step === 1) {
            indicator.textContent = 'Step 1/2 — Create onboarding asset';
        } else {
            const mode = (window._addAgentState && window._addAgentState.mode) ? window._addAgentState.mode : 'token';
            let label = 'Token (shown once)';
            if (mode === 'script') label = 'Bootstrap script';
            else if (mode === 'email') label = 'Email sent';
            indicator.textContent = 'Step 2/2 — ' + label;
        }
    }

    if (step === 1) {
        // Tenant select - handled by populateTenantDropdown after content is set
        const state = window._addAgentState || {};
        const ttlValue = state.ttl && state.ttl > 0 ? state.ttl : 60;
        const oneTimeChecked = state.one_time === undefined ? true : !!state.one_time;
        const defaultPlatform = state.platform || 'windows';
        const platformOptions = [
            { value: 'linux', label: 'Linux' },
            { value: 'windows', label: 'Windows' },
            { value: 'darwin', label: 'macOS' }
        ].map(opt => `<option value="${escapeHtml(opt.value)}" ${opt.value === defaultPlatform ? 'selected' : ''}>${escapeHtml(opt.label)}</option>`).join('\n');
        const formatOptions = [
            { value: 'zip', label: 'ZIP archive' },
            { value: 'tar.gz', label: 'TAR.GZ archive' }
        ].map(opt => `<option value="${escapeHtml(opt.value)}">${escapeHtml(opt.label)}</option>`).join('\n');
        const archOptions = [
            { value: 'amd64', label: 'x86_64 / amd64' },
            { value: 'arm64', label: 'ARM64 / Apple Silicon' }
        ].map(opt => `<option value="${escapeHtml(opt.value)}">${escapeHtml(opt.label)}</option>`).join('\n');

        content.innerHTML = `
            <div style="display:flex;flex-direction:column;gap:8px;">
                <label style="font-weight:600">Customer (tenant)</label>
                <select id="add_agent_tenant" style="padding:8px;border-radius:4px;border:1px solid var(--border);">
                </select>

                <label style="font-weight:600">Join token TTL (minutes)</label>
                <input id="add_agent_ttl" type="number" value="${escapeHtml(String(ttlValue))}" min="1" style="padding:8px;border-radius:4px;border:1px solid var(--border);width:120px;" autocomplete="off" data-1p-ignore data-lpignore="true" />

                <label style="display:flex;align-items:center;gap:8px;">
                    <input id="add_agent_one_time" type="checkbox" ${oneTimeChecked ? 'checked' : ''} />
                    <span style="color:var(--muted)">One-time (single-use) token</span>
                </label>

                <div style="margin-top:8px;">
                    <div style="font-weight:600;margin-bottom:6px;">Onboarding method</div>
                    <label style="display:flex;align-items:center;gap:8px;"><input type="radio" name="add_agent_action" value="token" ${state.mode === 'token' || !state.mode ? 'checked' : ''} /> Show raw token</label>
                    <label style="display:flex;align-items:center;gap:8px;"><input type="radio" name="add_agent_action" value="script" ${state.mode === 'script' ? 'checked' : ''} /> Generate bootstrap script</label>
                    <label style="display:flex;align-items:center;gap:8px;"><input type="radio" name="add_agent_action" value="email" ${state.mode === 'email' ? 'checked' : ''} /> Send via email</label>
                    <div id="add_agent_platform_row" style="margin-top:8px;display:none;">
                        <label style="font-weight:600">Target platform</label>
                        <select id="add_agent_platform" style="padding:8px;border-radius:4px;border:1px solid var(--border);width:180px;">
                            ${platformOptions}
                        </select>
                    </div>
                    <div id="add_agent_email_row" style="margin-top:8px;display:none;">
                        <label style="font-weight:600">Recipient email</label>
                        <input id="add_agent_email" type="email" placeholder="user@example.com" style="padding:8px;border-radius:4px;border:1px solid var(--border);width:280px;" autocomplete="off" data-1p-ignore data-lpignore="true" />
                        <div style="color:var(--muted);font-size:12px;margin-top:4px;">The recipient will receive an HTML email with the installation one-liner and full script.</div>
                    </div>
                </div>

                <div style="color:var(--muted);font-size:13px">Tokens and scripts inherit this TTL.</div>
            </div>
        `;

        // Populate tenant dropdown with "Add Tenant" option
        const tenantSelect = content.querySelector('#add_agent_tenant');
        if (tenantSelect) {
            populateTenantDropdown(tenantSelect, {
                placeholder: '-- select customer --',
                selectedId: state.tenantID || '',
                showAddOption: true
            });
            // Also track state change when tenant is selected (but not for "Add Tenant")
            tenantSelect.addEventListener('change', () => {
                if (tenantSelect.value !== '__add_new_tenant__') {
                    state.tenantID = tenantSelect.value;
                }
            });
        }

        const ttlInput = content.querySelector('#add_agent_ttl');
        if (ttlInput) {
            ttlInput.addEventListener('change', () => {
                const parsed = parseInt(ttlInput.value, 10);
                state.ttl = (isNaN(parsed) || parsed <= 0) ? 60 : parsed;
                ttlInput.value = state.ttl;
            });
        }

        const oneTimeInput = content.querySelector('#add_agent_one_time');
        if (oneTimeInput) {
            oneTimeInput.checked = oneTimeChecked;
            oneTimeInput.addEventListener('change', () => { state.one_time = oneTimeInput.checked; });
        }

        const platformSelect = content.querySelector('#add_agent_platform');
        if (platformSelect) {
            platformSelect.value = defaultPlatform;
            state.platform = platformSelect.value;
            platformSelect.addEventListener('change', () => {
                state.platform = platformSelect.value;
            });
        }

        const actionRadios = content.querySelectorAll('input[name="add_agent_action"]');
        const platRow = content.querySelector('#add_agent_platform_row');
        const emailRow = content.querySelector('#add_agent_email_row');
        const updatePrimaryLabel = () => {
            const sel = content.querySelector('input[name="add_agent_action"]:checked');
            if (!sel || !primaryBtn) return;
            if (sel.value === 'script') primaryBtn.textContent = 'Generate Script';
            else if (sel.value === 'email') primaryBtn.textContent = 'Send Email';
            else primaryBtn.textContent = 'Create Token';
        };
        const updateFieldVisibility = () => {
            const sel = content.querySelector('input[name="add_agent_action"]:checked');
            const mode = sel ? sel.value : 'token';
            if (platRow) platRow.style.display = (mode === 'script' || mode === 'email') ? '' : 'none';
            if (emailRow) emailRow.style.display = mode === 'email' ? '' : 'none';
        };
        actionRadios.forEach(r => r.addEventListener('change', () => {
            state.mode = r.value;
            updatePrimaryLabel();
            updateFieldVisibility();
        }));
        updatePrimaryLabel();
        updateFieldVisibility();
    } else {
        // Step 2: show token, script, or email confirmation
        const token = (window._addAgentState && window._addAgentState.token) ? window._addAgentState.token : '';
        const mode = (window._addAgentState && window._addAgentState.mode) ? window._addAgentState.mode : 'token';
        const script = (window._addAgentState && window._addAgentState.script) ? window._addAgentState.script : null;
        const filename = (window._addAgentState && window._addAgentState.scriptFilename) ? window._addAgentState.scriptFilename : 'bootstrap';
        const emailSent = (window._addAgentState && window._addAgentState.emailSent) ? window._addAgentState.emailSent : false;
        const emailTo = (window._addAgentState && window._addAgentState.emailTo) ? window._addAgentState.emailTo : '';

        if (emailSent && mode === 'email') {
            content.innerHTML = `
                <div style="display:flex;flex-direction:column;gap:12px;align-items:center;text-align:center;padding:20px;">
                    <div style="font-size:48px;">✉️</div>
                    <h3 style="margin:0;color:var(--text);">Email Sent Successfully</h3>
                    <p style="color:var(--muted);margin:0;">Agent deployment instructions have been sent to:</p>
                    <div style="font-family:monospace;padding:12px 24px;background:var(--panel);border-radius:6px;border:1px solid var(--border);font-weight:600;">${escapeHtml(emailTo)}</div>
                    <p style="color:var(--muted);font-size:13px;margin:0;">The email contains the one-liner command and full bootstrap script for the selected platform. The recipient can follow the instructions to install and register the agent.</p>
                </div>
            `;
        } else if (script && mode === 'script') {
            const oneLiner = (window._addAgentState && window._addAgentState.oneLiner) ? window._addAgentState.oneLiner : null;
            content.innerHTML = `
                <div style="display:flex;flex-direction:column;gap:12px;">
                    ${oneLiner ? `<div style="font-family:monospace;padding:12px;background:var(--panel);border-radius:6px;border:1px dashed var(--border);word-break:break-all;">${escapeHtml(oneLiner)}</div>` : `<div style="font-family:monospace;white-space:pre-wrap;padding:12px;background:var(--panel);border-radius:6px;border:1px dashed var(--border);">${escapeHtml(script)}</div>`}
                    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;">
                        <button id="add_agent_copy" class="modal-button" style="flex:1;min-width:200px;font-weight:600;">${oneLiner ? 'Copy one-liner' : 'Copy script'}</button>
                        ${oneLiner ? `<button id="add_agent_more_options" class="modal-button modal-button-secondary" style="padding:8px 12px;">More options ▾</button>` : `<button id="add_agent_download" class="modal-button modal-button-secondary">Download script</button>`}
                    </div>
                    <div style="color:var(--muted);font-size:13px">This script was generated for the selected platform. Download or copy it and execute it on the target machine to install and register the agent.</div>
                    ${oneLiner ? `<div id="add_agent_full_script" style="display:none;margin-top:8px;font-family:monospace;white-space:pre-wrap;padding:12px;background:var(--panel);border-radius:6px;border:1px dashed var(--border);">${escapeHtml(script)}</div>` : ''}
                </div>
            `;
            // Create floating dropdown for more options (appended to body to escape modal overflow)
            let existingDropdown = document.getElementById('add_agent_options_dropdown');
            if (existingDropdown) existingDropdown.remove();
            if (oneLiner) {
                const dropdown = document.createElement('div');
                dropdown.id = 'add_agent_options_dropdown';
                dropdown.style.cssText = 'display:none;position:fixed;background:var(--bg);border:1px solid var(--border);border-radius:6px;box-shadow:0 4px 12px rgba(0,0,0,0.5);z-index:100000;min-width:180px;';
                dropdown.innerHTML = `
                    <button id="add_agent_download" style="display:block;width:100%;text-align:left;padding:10px 14px;background:none;border:none;color:var(--text);cursor:pointer;font-size:14px;">Download script</button>
                    ${(window._addAgentState && window._addAgentState.scriptDownloadURL) ? `<a id="add_agent_download_url" href="${escapeHtml(window._addAgentState.scriptDownloadURL)}" target="_blank" style="display:block;width:100%;text-align:left;padding:10px 14px;background:none;border:none;color:var(--text);cursor:pointer;font-size:14px;text-decoration:none;border-top:1px solid var(--border);">Open hosted URL</a>` : ''}
                    <button id="add_agent_show_full" style="display:block;width:100%;text-align:left;padding:10px 14px;background:none;border:none;color:var(--text);cursor:pointer;font-size:14px;border-top:1px solid var(--border);">Show full script</button>
                `;
                document.body.appendChild(dropdown);
            }
            const copyBtn = document.getElementById('add_agent_copy');
            if (copyBtn) copyBtn.addEventListener('click', () => {
                const textToCopy = oneLiner ? oneLiner : script;
                navigator.clipboard?.writeText(textToCopy).then(() => { window.__pm_shared.showToast((oneLiner ? 'One-liner' : 'Script') + ' copied to clipboard', 'success'); }).catch(err => { window.__pm_shared.showAlert('Failed to copy: ' + (err && err.message ? err.message : err), 'Error', true, false); });
            });
            // Wire up dropdown toggle for more options
            const moreOptionsBtn = document.getElementById('add_agent_more_options');
            const dropdown = document.getElementById('add_agent_options_dropdown');
            if (moreOptionsBtn && dropdown) {
                const positionDropdown = () => {
                    const rect = moreOptionsBtn.getBoundingClientRect();
                    dropdown.style.top = (rect.bottom + 4) + 'px';
                    dropdown.style.left = rect.left + 'px';
                };
                moreOptionsBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    if (dropdown.style.display === 'none') {
                        positionDropdown();
                        dropdown.style.display = 'block';
                    } else {
                        dropdown.style.display = 'none';
                    }
                });
                // Close dropdown when clicking outside
                document.addEventListener('click', (e) => {
                    if (!moreOptionsBtn.contains(e.target) && !dropdown.contains(e.target)) {
                        dropdown.style.display = 'none';
                    }
                });
                // Add hover effect to dropdown items
                dropdown.querySelectorAll('button, a').forEach(item => {
                    item.addEventListener('mouseenter', () => { item.style.background = 'var(--panel)'; });
                    item.addEventListener('mouseleave', () => { item.style.background = 'none'; });
                });
            }
            const dlBtn = document.getElementById('add_agent_download');
            if (dlBtn) dlBtn.addEventListener('click', () => {
                const blob = new Blob([script], { type: 'application/octet-stream' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a'); a.href = url; a.download = filename; document.body.appendChild(a); a.click(); a.remove(); URL.revokeObjectURL(url);
                if (dropdown) dropdown.style.display = 'none';
            });
            const showFull = document.getElementById('add_agent_show_full');
            if (showFull) {
                showFull.addEventListener('click', () => {
                    const full = document.getElementById('add_agent_full_script');
                    if (!full) return;
                    if (full.style.display === 'none') {
                        full.style.display = 'block'; showFull.textContent = 'Hide full script';
                    } else { full.style.display = 'none'; showFull.textContent = 'Show full script'; }
                    if (dropdown) dropdown.style.display = 'none';
                });
            }
        } else {
            content.innerHTML = `
                <div style="display:flex;flex-direction:column;gap:12px;">
                    <div style="font-family:monospace;white-space:pre-wrap;padding:12px;background:var(--panel);border-radius:6px;border:1px dashed var(--border);">${escapeHtml(token)}</div>
                    <div style="display:flex;gap:8px;">
                        <button id="add_agent_copy" class="modal-button modal-button-secondary">Copy token</button>
                        <button id="add_agent_download" class="modal-button">Download token</button>
                    </div>
                    <div style="color:var(--muted);font-size:13px">This token is shown only once. After you close this modal the raw token cannot be retrieved again from the server.</div>
                </div>
            `;
            // Wire copy/download for token
            const copyBtn = document.getElementById('add_agent_copy');
            if (copyBtn) copyBtn.addEventListener('click', copyTokenToClipboard);
            const dlBtn = document.getElementById('add_agent_download');
            if (dlBtn) dlBtn.addEventListener('click', () => {
                const blob = new Blob([token], { type: 'text/plain' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url; a.download = 'join-token.txt'; document.body.appendChild(a); a.click(); a.remove(); URL.revokeObjectURL(url);
            });
        }
        if (primaryBtn) primaryBtn.textContent = 'Done';
    }
}

function copyTokenToClipboard() {
    const token = (window._addAgentState && window._addAgentState.token) ? window._addAgentState.token : '';
    if (!token) return;
    navigator.clipboard?.writeText(token).then(() => {
        window.__pm_shared.showToast('Token copied to clipboard', 'success');
    }).catch(err => {
        window.__pm_shared.showAlert('Failed to copy token: ' + (err && err.message ? err.message : err), 'Error', true, false);
    });
}

// Update the compact time filter display label from slider index
function updateTimeFilter(index) {
    const labels = ['1m', '2m', '5m', '10m', '15m', '30m', '1h', '2h', '3h', '6h', '12h', '1d', '3d', 'All Time'];
    let idx = parseInt(index, 10);
    if (isNaN(idx) || idx < 0) idx = labels.length - 1;
    if (idx >= labels.length) idx = labels.length - 1;
    const el = document.getElementById('time_filter_value');
    if (el) el.textContent = labels[idx];
}

// Wire up printer details modal close buttons and backdrop
(function wirePrinterModal() {
    const detailsOverlay = document.getElementById('printer_details_overlay');
    const modalCloseBtn = document.querySelector('#printer_details_actions button');
    const printerDetailsCloseX = document.getElementById('printer_details_close_x');

    function closePrinterDetailsModal() {
        if (detailsOverlay) {
            detailsOverlay.style.display = 'none';
            document.body.style.overflow = '';
            try { delete detailsOverlay.dataset.currentPrinterIp; } catch (e) { }
        }
    }

    if (modalCloseBtn) modalCloseBtn.addEventListener('click', closePrinterDetailsModal);
    if (printerDetailsCloseX) printerDetailsCloseX.addEventListener('click', closePrinterDetailsModal);
    if (detailsOverlay) {
        detailsOverlay.addEventListener('click', function (e) {
            if (e.target === detailsOverlay) closePrinterDetailsModal();
        });
    }
})();

// Wire up alerting and reports modals
(function wireAlertingModals() {
    // Helper to close modal
    function closeModal(modal) {
        if (modal) modal.style.display = 'none';
    }

    // Helper to wire a modal's close buttons
    function wireModalClose(modalId, closeXId, cancelId) {
        const modal = document.getElementById(modalId);
        const closeX = document.getElementById(closeXId);
        const cancel = document.getElementById(cancelId);

        if (closeX) closeX.addEventListener('click', () => closeModal(modal));
        if (cancel) cancel.addEventListener('click', () => closeModal(modal));
        if (modal) {
            modal.addEventListener('click', (e) => {
                if (e.target === modal) closeModal(modal);
            });
        }
    }

    // Alert Rule Modal
    wireModalClose('alert_rule_modal', 'alert_rule_modal_close_x', 'alert_rule_cancel');
    const alertRuleSaveBtn = document.getElementById('alert_rule_save');
    if (alertRuleSaveBtn) alertRuleSaveBtn.addEventListener('click', saveAlertRule);

    // Notification Channel Modal
    wireModalClose('notification_channel_modal', 'notification_channel_modal_close_x', 'channel_cancel');
    const channelSaveBtn = document.getElementById('channel_save');
    if (channelSaveBtn) channelSaveBtn.addEventListener('click', saveNotificationChannel);
    const channelTypeSelect = document.getElementById('channel_type');
    if (channelTypeSelect) channelTypeSelect.addEventListener('change', updateChannelConfigSection);

    // Wire up channel type cards click handlers
    document.querySelectorAll('.channel-type-card').forEach(card => {
        card.addEventListener('click', () => {
            const type = card.dataset.type;
            if (type && channelTypeSelect) {
                channelTypeSelect.value = type;
                updateChannelConfigSection();
            }
        });
    });

    // Escalation Policy Modal
    wireModalClose('escalation_policy_modal', 'escalation_policy_modal_close_x', 'escalation_cancel');
    const escalationSaveBtn = document.getElementById('escalation_save');
    if (escalationSaveBtn) escalationSaveBtn.addEventListener('click', saveEscalationPolicy);

    // Maintenance Window Modal
    wireModalClose('maintenance_window_modal', 'maintenance_window_modal_close_x', 'maintenance_cancel');
    const maintenanceSaveBtn = document.getElementById('maintenance_save');
    if (maintenanceSaveBtn) maintenanceSaveBtn.addEventListener('click', saveMaintenanceWindow);

    // Scheduled Report Modal
    wireModalClose('scheduled_report_modal', 'scheduled_report_modal_close_x', 'schedule_cancel');
    const scheduleSaveBtn = document.getElementById('schedule_save');
    if (scheduleSaveBtn) scheduleSaveBtn.addEventListener('click', saveScheduledReport);

    // Schedule frequency change handler - show/hide day fields
    const frequencySelect = document.getElementById('schedule_frequency');
    if (frequencySelect) {
        frequencySelect.addEventListener('change', () => {
            const freq = frequencySelect.value;
            const dayField = document.getElementById('schedule_day_field');
            const dayOfMonthField = document.getElementById('schedule_day_of_month_field');

            if (dayField) dayField.style.display = freq === 'weekly' ? 'block' : 'none';
            if (dayOfMonthField) dayOfMonthField.style.display = freq === 'monthly' ? 'block' : 'none';
        });
    }

    // Report Download Modal
    wireModalClose('report_download_modal', 'report_download_close_x', 'report_download_close');
})();

