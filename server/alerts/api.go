// Package alerts provides HTTP API handlers for the alerting system.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

// Store captures the persistence operations needed by the alerts API.
type Store interface {
	// Alert CRUD
	CreateAlert(context.Context, *storage.Alert) (int64, error)
	GetAlert(context.Context, int64) (*storage.Alert, error)
	ListActiveAlerts(context.Context, storage.AlertFilters) ([]storage.Alert, error)
	UpdateAlertStatus(context.Context, int64, storage.AlertStatus) error
	AcknowledgeAlert(context.Context, int64, string) error
	ResolveAlert(context.Context, int64) error

	// Alert Rules CRUD
	CreateAlertRule(context.Context, *storage.AlertRule) (int64, error)
	GetAlertRule(context.Context, int64) (*storage.AlertRule, error)
	ListAlertRules(context.Context) ([]storage.AlertRule, error)
	UpdateAlertRule(context.Context, *storage.AlertRule) error
	DeleteAlertRule(context.Context, int64) error

	// Notification Channels CRUD
	CreateNotificationChannel(context.Context, *storage.NotificationChannel) (int64, error)
	GetNotificationChannel(context.Context, int64) (*storage.NotificationChannel, error)
	ListNotificationChannels(context.Context) ([]storage.NotificationChannel, error)
	UpdateNotificationChannel(context.Context, *storage.NotificationChannel) error
	DeleteNotificationChannel(context.Context, int64) error

	// Escalation Policies CRUD
	CreateEscalationPolicy(context.Context, *storage.EscalationPolicy) (int64, error)
	GetEscalationPolicy(context.Context, int64) (*storage.EscalationPolicy, error)
	ListEscalationPolicies(context.Context) ([]storage.EscalationPolicy, error)
	UpdateEscalationPolicy(context.Context, *storage.EscalationPolicy) error
	DeleteEscalationPolicy(context.Context, int64) error

	// Maintenance Windows CRUD
	CreateAlertMaintenanceWindow(context.Context, *storage.AlertMaintenanceWindow) (int64, error)
	GetAlertMaintenanceWindow(context.Context, int64) (*storage.AlertMaintenanceWindow, error)
	ListAlertMaintenanceWindows(context.Context) ([]storage.AlertMaintenanceWindow, error)
	UpdateAlertMaintenanceWindow(context.Context, *storage.AlertMaintenanceWindow) error
	GetActiveAlertMaintenanceWindows(context.Context) ([]storage.AlertMaintenanceWindow, error)
	DeleteAlertMaintenanceWindow(context.Context, int64) error

	// Alert Settings
	GetAlertSettings(context.Context) (*storage.AlertSettings, error)
	SaveAlertSettings(context.Context, *storage.AlertSettings) error

	// Alert Summary
	GetAlertSummary(context.Context) (*storage.AlertSummary, error)
}

// APIOptions provides cross-cutting infrastructure for the HTTP layer.
type APIOptions struct {
	AuthMiddleware func(http.HandlerFunc) http.HandlerFunc
	Authorizer     func(*http.Request, authz.Action, authz.ResourceRef) error
	// ScopeResolver must derive scope from the authenticated principal, never
	// query parameters. AllTenants is reserved for an explicit global admin.
	// Missing/erroring resolvers fail closed; an empty restricted scope owns nothing.
	ScopeResolver func(*http.Request) (TenantScope, error)
	ActorResolver func(*http.Request) string
	AuditLogger   func(*http.Request, *storage.AuditEntry)
	Notifier      *Notifier
}

// TenantScope separates unrestricted administration from an empty tenant set.
type TenantScope struct {
	AllTenants bool
	TenantIDs  []string
}

type tenantScopeKey struct{}

func requestScope(r *http.Request) TenantScope {
	scope, _ := r.Context().Value(tenantScopeKey{}).(TenantScope)
	return scope
}

// owns requires complete ownership: shared records containing a foreign tenant
// must not expose credentials or be editable through a partial intersection.
func (scope TenantScope) owns(ids ...string) bool {
	if scope.AllTenants {
		return true
	}
	if len(ids) == 0 {
		return false // empty ownership means global, not caller-owned
	}
	for _, id := range ids {
		found := false
		for _, allowed := range scope.TenantIDs {
			if id != "" && id == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// RouteConfig controls how HTTP handlers are registered.
type RouteConfig struct {
	Mux            *http.ServeMux
	FeatureEnabled bool
}

// API exposes HTTP handlers for the alerting system.
type API struct {
	store         Store
	notifier      *Notifier
	authWrap      func(http.HandlerFunc) http.HandlerFunc
	authorizer    func(*http.Request, authz.Action, authz.ResourceRef) error
	scopeResolver func(*http.Request) (TenantScope, error)
	actorResolver func(*http.Request) string
	auditLogger   func(*http.Request, *storage.AuditEntry)
}

// NewAPI builds a new alerts API instance.
func NewAPI(store Store, opts APIOptions) (*API, error) {
	if store == nil {
		return nil, errors.New("alerts API requires a store")
	}
	return &API{
		store:         store,
		notifier:      opts.Notifier,
		authWrap:      opts.AuthMiddleware,
		authorizer:    opts.Authorizer,
		scopeResolver: opts.ScopeResolver,
		actorResolver: opts.ActorResolver,
		auditLogger:   opts.AuditLogger,
	}, nil
}

// RegisterRoutes wires all alert endpoints.
func (api *API) RegisterRoutes(cfg RouteConfig) {
	if !cfg.FeatureEnabled {
		return
	}

	mux := cfg.Mux
	if mux == nil {
		mux = http.DefaultServeMux
	}

	wrap := api.wrap

	// Alert summary (dashboard)
	mux.HandleFunc("/api/v1/alerts/summary", wrap(api.handleAlertSummary))

	// Alerts CRUD
	mux.HandleFunc("/api/v1/alerts", wrap(api.handleAlerts))
	mux.HandleFunc("/api/v1/alerts/", wrap(api.handleAlertRoute))

	// Alert Rules CRUD
	mux.HandleFunc("/api/v1/alert-rules", wrap(api.handleAlertRules))
	mux.HandleFunc("/api/v1/alert-rules/", wrap(api.handleAlertRuleRoute))

	// Notification Channels CRUD
	mux.HandleFunc("/api/v1/notification-channels", wrap(api.handleNotificationChannels))
	mux.HandleFunc("/api/v1/notification-channels/test", wrap(api.handleTestNotificationChannel))
	mux.HandleFunc("/api/v1/notification-channels/", wrap(api.handleNotificationChannelRoute))

	// Escalation Policies CRUD
	mux.HandleFunc("/api/v1/escalation-policies", wrap(api.handleEscalationPolicies))
	mux.HandleFunc("/api/v1/escalation-policies/", wrap(api.handleEscalationPolicyRoute))

	// Maintenance Windows CRUD
	mux.HandleFunc("/api/v1/maintenance-windows", wrap(api.handleMaintenanceWindows))
	mux.HandleFunc("/api/v1/maintenance-windows/", wrap(api.handleMaintenanceWindowRoute))

	// Alert Settings
	mux.HandleFunc("/api/v1/alert-settings", wrap(api.handleAlertSettings))
}

func (api *API) wrap(handler http.HandlerFunc) http.HandlerFunc {
	if api.authWrap == nil {
		return handler
	}
	return api.authWrap(handler)
}

func (api *API) authorize(w http.ResponseWriter, r *http.Request, action authz.Action, resource authz.ResourceRef) bool {
	if api.authorizer == nil {
		http.Error(w, "authorization not configured", http.StatusInternalServerError)
		return false
	}
	if err := api.authorizer(r, action, resource); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, authz.ErrUnauthorized) {
			status = http.StatusUnauthorized
		}
		http.Error(w, http.StatusText(status), status)
		return false
	}
	if _, ok := r.Context().Value(tenantScopeKey{}).(TenantScope); !ok {
		if api.scopeResolver == nil {
			writeError(w, http.StatusInternalServerError, "tenant scope not configured")
			return false
		}
		scope, err := api.scopeResolver(r)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, authz.ErrUnauthorized) {
				status = http.StatusUnauthorized
			}
			writeError(w, status, http.StatusText(status))
			return false
		}
		scope.TenantIDs = append([]string(nil), scope.TenantIDs...)
		*r = *r.WithContext(context.WithValue(r.Context(), tenantScopeKey{}, scope))
	}
	scope := requestScope(r)
	if !scope.AllTenants && len(scope.TenantIDs) > 0 {
		if err := api.authorizer(r, action, authz.ResourceRef{TenantIDs: scope.TenantIDs}); err != nil {
			writeError(w, http.StatusForbidden, "tenant not permitted")
			return false
		}
	}
	// Cross-tenant/global access requires both explicit scope and admin policy.
	if scope.AllTenants {
		adminAction := authz.ActionSettingsServerRead
		if action == authz.ActionSettingsAlertsWrite {
			adminAction = authz.ActionSettingsServerWrite
		}
		if err := api.authorizer(r, adminAction, authz.ResourceRef{}); err != nil {
			writeError(w, http.StatusForbidden, "global administrator required")
			return false
		}
	}
	if tenant := r.URL.Query().Get("tenant_id"); tenant != "" {
		if !scope.owns(tenant) {
			writeError(w, http.StatusForbidden, "tenant not permitted")
			return false
		}
		if err := api.authorizer(r, action, authz.ResourceRef{TenantIDs: []string{tenant}}); err != nil {
			writeError(w, http.StatusForbidden, "tenant not permitted")
			return false
		}
	}
	// These records have no persisted tenant ownership. Do not invent a schema
	// or infer policy ownership from channel references that can change later.
	if r.URL.Path == "/api/v1/alert-settings" ||
		strings.HasPrefix(r.URL.Path, "/api/v1/escalation-policies") ||
		r.URL.Path == "/api/v1/notification-channels/test" {
		if !scope.AllTenants {
			writeError(w, http.StatusForbidden, "global administrator required")
			return false
		}
	}
	return true
}

func (api *API) requireTenants(w http.ResponseWriter, r *http.Request, ids ...string) bool {
	if !requestScope(r).owns(ids...) {
		writeError(w, http.StatusForbidden, "tenant not permitted")
		return false
	}
	action := authz.ActionSettingsAlertsRead
	if r.Method != http.MethodGet {
		action = authz.ActionSettingsAlertsWrite
	}
	return api.authorize(w, r, action, authz.ResourceRef{TenantIDs: ids})
}

// checkRecord loads ownership before any mutation or notification side effect.
// Global records and records partly owned by another tenant are hidden.
func (api *API) checkRecord(w http.ResponseWriter, r *http.Request, kind string, id int64) bool {
	var ids []string
	var err error
	found := false
	switch kind {
	case "alert":
		var record *storage.Alert
		record, err = api.store.GetAlert(r.Context(), id)
		if record != nil {
			found, ids = true, []string{record.TenantID}
		}
	case "rule":
		var record *storage.AlertRule
		record, err = api.store.GetAlertRule(r.Context(), id)
		if record != nil {
			found, ids = true, record.TenantIDs
			if record.Scope == storage.AlertScopeFleet && !requestScope(r).AllTenants {
				ids = nil
			}
		}
	case "channel":
		var record *storage.NotificationChannel
		record, err = api.store.GetNotificationChannel(r.Context(), id)
		if record != nil {
			found, ids = true, record.TenantIDs
		}
	case "window":
		var record *storage.AlertMaintenanceWindow
		record, err = api.store.GetAlertMaintenanceWindow(r.Context(), id)
		if record != nil {
			found, ids = true, []string{record.TenantID}
			if (record.Scope == "" || record.Scope == storage.AlertScopeFleet) && !requestScope(r).AllTenants {
				ids = nil
			}
		}
	case "policy":
		var record *storage.EscalationPolicy
		record, err = api.store.GetEscalationPolicy(r.Context(), id)
		found = record != nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check record ownership")
		return false
	}
	if !found {
		http.NotFound(w, r)
		return false
	}
	return api.recordVisible(w, r, ids...)
}

func (api *API) recordVisible(w http.ResponseWriter, r *http.Request, ids ...string) bool {
	if !requestScope(r).owns(ids...) {
		http.NotFound(w, r)
		return false
	}
	return api.requireTenants(w, r, ids...)
}

func visibleTenants(r *http.Request, ids ...string) bool {
	if !requestScope(r).owns(ids...) {
		return false
	}
	requested := r.URL.Query().Get("tenant_id")
	if requested == "" {
		return true
	}
	for _, id := range ids {
		if id == requested {
			return true
		}
	}
	return false
}

func visibleRule(r *http.Request, rule storage.AlertRule) bool {
	return (rule.Scope != storage.AlertScopeFleet || requestScope(r).AllTenants) && visibleTenants(r, rule.TenantIDs...)
}

func visibleWindow(r *http.Request, window storage.AlertMaintenanceWindow) bool {
	return ((window.Scope != "" && window.Scope != storage.AlertScopeFleet) || requestScope(r).AllTenants) && visibleTenants(r, window.TenantID)
}

// Target lookups are optional capabilities of Store. If a request references
// fleet objects and the store cannot verify ownership, fail closed.
func (api *API) checkTargets(w http.ResponseWriter, r *http.Request, tenant, site, agent, device string) bool {
	return api.checkTargetTenants(w, r, []string{tenant}, site, agent, device)
}

func (api *API) checkTargetTenants(w http.ResponseWriter, r *http.Request, tenants []string, site, agent, device string) bool {
	check := func(owner string) bool {
		if !api.requireTenants(w, r, owner) {
			return false
		}
		if len(tenants) > 0 && !(len(tenants) == 1 && tenants[0] == "" && requestScope(r).AllTenants) {
			for _, tenant := range tenants {
				if owner == tenant {
					return true
				}
			}
			writeError(w, http.StatusBadRequest, "target does not belong to requested tenants")
			return false
		}
		return true
	}
	if device != "" {
		store, ok := api.store.(interface {
			GetDevice(context.Context, string) (*storage.Device, error)
		})
		if !ok {
			writeError(w, http.StatusInternalServerError, "device ownership lookup unavailable")
			return false
		}
		record, err := store.GetDevice(r.Context(), device)
		if err != nil || record == nil {
			http.NotFound(w, r)
			return false
		}
		if agent != "" && agent != record.AgentID {
			writeError(w, http.StatusBadRequest, "device does not belong to requested agent")
			return false
		}
		agent = record.AgentID
		if agent == "" {
			writeError(w, http.StatusForbidden, "device ownership unavailable")
			return false
		}
	}
	if agent != "" {
		store, ok := api.store.(interface {
			GetAgent(context.Context, string) (*storage.Agent, error)
		})
		if !ok {
			writeError(w, http.StatusInternalServerError, "agent ownership lookup unavailable")
			return false
		}
		record, err := store.GetAgent(r.Context(), agent)
		if err != nil || record == nil {
			http.NotFound(w, r)
			return false
		}
		if !check(record.TenantID) {
			return false
		}
	}
	if site != "" {
		store, ok := api.store.(interface {
			GetSite(context.Context, string) (*storage.Site, error)
		})
		if !ok {
			writeError(w, http.StatusInternalServerError, "site ownership lookup unavailable")
			return false
		}
		record, err := store.GetSite(r.Context(), site)
		if err != nil || record == nil {
			http.NotFound(w, r)
			return false
		}
		if !check(record.TenantID) {
			return false
		}
	}
	return true
}

func (api *API) checkRule(w http.ResponseWriter, r *http.Request, rule *storage.AlertRule) bool {
	if !api.requireTenants(w, r, rule.TenantIDs...) {
		return false
	}
	if rule.Scope == storage.AlertScopeFleet && !requestScope(r).AllTenants {
		writeError(w, http.StatusForbidden, "global administrator required")
		return false
	}
	for _, site := range rule.SiteIDs {
		if !api.checkTargetTenants(w, r, rule.TenantIDs, site, "", "") {
			return false
		}
	}
	for _, agent := range rule.AgentIDs {
		if !api.checkTargetTenants(w, r, rule.TenantIDs, "", agent, "") {
			return false
		}
	}
	for _, channel := range rule.ChannelIDs {
		record, err := api.store.GetNotificationChannel(r.Context(), channel)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check channel ownership")
			return false
		}
		if record == nil {
			http.NotFound(w, r)
			return false
		}
		if !api.recordVisible(w, r, record.TenantIDs...) {
			return false
		}
		if !requestScope(r).AllTenants && !channelCoversRule(record.TenantIDs, rule) {
			writeError(w, http.StatusForbidden, "channel ownership incompatible with rule")
			return false
		}
	}
	if rule.EscalationPolicyID != nil && !api.checkRecord(w, r, "policy", *rule.EscalationPolicyID) {
		return false
	}
	return true
}

// channelCoversRule prevents a caller owning multiple tenants from attaching a
// rule to a channel owned only by a different tenant. Global/fleet rules cannot
// depend on a restricted channel without an explicit administrator override.
func channelCoversRule(tenants []string, rule *storage.AlertRule) bool {
	return rule.Scope != storage.AlertScopeFleet && (TenantScope{TenantIDs: tenants}).owns(rule.TenantIDs...)
}

// checkChannelDependents protects legacy references as well as new ones. A
// channel's own ownership is insufficient: changing credentials, disabling it,
// moving its ownership, or deleting it also affects every referencing record.
// Policies have no persisted tenant ownership and remain global/admin-only.
// Include disabled dependents; they can be enabled later. Never use visibility
// filters (including tenant_id) to discover reverse references.
func (api *API) checkChannelDependents(w http.ResponseWriter, r *http.Request, id int64, replacement *storage.NotificationChannel) bool {
	if requestScope(r).AllTenants {
		return true
	}
	rules, err := api.store.ListAlertRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check channel dependencies")
		return false
	}
	for _, rule := range rules {
		for _, channel := range rule.ChannelIDs {
			if channel != id {
				continue
			}
			if rule.Scope == storage.AlertScopeFleet || !requestScope(r).owns(rule.TenantIDs...) {
				writeError(w, http.StatusForbidden, "channel dependencies not permitted")
				return false
			}
			if !api.requireTenants(w, r, rule.TenantIDs...) {
				return false
			}
			if replacement != nil && !channelCoversRule(replacement.TenantIDs, &rule) {
				writeError(w, http.StatusForbidden, "channel ownership incompatible with rule")
				return false
			}
			break
		}
	}
	policies, err := api.store.ListEscalationPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check channel dependencies")
		return false
	}
	for _, policy := range policies {
		for _, step := range policy.Steps {
			for _, channel := range step.ChannelIDs {
				if channel == id {
					writeError(w, http.StatusForbidden, "channel dependencies not permitted")
					return false
				}
			}
		}
	}
	return true
}

func (api *API) checkPolicyChannels(w http.ResponseWriter, r *http.Request, policy *storage.EscalationPolicy) bool {
	for _, step := range policy.Steps {
		for _, channel := range step.ChannelIDs {
			if !api.checkRecord(w, r, "channel", channel) {
				return false
			}
		}
	}
	return true
}

func (api *API) checkWindow(w http.ResponseWriter, r *http.Request, window *storage.AlertMaintenanceWindow) bool {
	// Empty/fleet scope suppresses the entire fleet in the evaluator, even when
	// a tenant_id is supplied. It must never be created by a restricted caller.
	if (window.Scope == "" || window.Scope == storage.AlertScopeFleet) && !requestScope(r).AllTenants {
		writeError(w, http.StatusForbidden, "global administrator required")
		return false
	}
	return api.requireTenants(w, r, window.TenantID) &&
		api.checkTargets(w, r, window.TenantID, window.SiteID, window.AgentID, window.DeviceSerial)
}

func (api *API) scopedSummary(r *http.Request) (*storage.AlertSummary, error) {
	if requestScope(r).AllTenants && r.URL.Query().Get("tenant_id") == "" {
		return api.store.GetAlertSummary(r.Context())
	}
	// ListActiveAlerts cannot supply resolved/suppressed counts. Real stores
	// implement ListAlerts; alternate stores must supply this capability rather
	// than accidentally falling back to a global summary.
	store, ok := api.store.(interface {
		ListAlerts(context.Context, storage.AlertFilters) ([]*storage.Alert, error)
	})
	if !ok {
		return nil, errors.New("scoped alert history unavailable")
	}
	filter := storage.AlertFilters{TenantID: r.URL.Query().Get("tenant_id")}
	if scope := requestScope(r); filter.TenantID == "" && !scope.AllTenants && len(scope.TenantIDs) == 1 {
		filter.TenantID = scope.TenantIDs[0]
	}
	alerts, err := store.ListAlerts(r.Context(), filter)
	if err != nil {
		return nil, err
	}
	summary := &storage.AlertSummary{
		AlertsByType: make(map[string]int), AlertsByScope: make(map[string]int),
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, alert := range alerts {
		if alert == nil || !visibleTenants(r, alert.TenantID) {
			continue
		}
		switch alert.Status {
		case storage.AlertStatusActive:
			summary.ActiveCount++
			summary.AlertsByType[alert.Type]++
			summary.AlertsByScope[alert.Scope]++
			switch alert.Severity {
			case storage.AlertSeverityCritical:
				summary.CriticalCount++
			case storage.AlertSeverityWarning:
				summary.WarningCount++
			case storage.AlertSeverityInfo:
				summary.InfoCount++
			}
		case storage.AlertStatusAcknowledged:
			summary.AcknowledgedCount++
		case storage.AlertStatusSuppressed:
			summary.SuppressedCount++
		case storage.AlertStatusResolved:
			if alert.ResolvedAt != nil && !alert.ResolvedAt.Before(today) {
				summary.ResolvedTodayCount++
			}
		}
	}
	rules, err := api.store.ListAlertRules(r.Context())
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if rule.Enabled && visibleRule(r, rule) {
			summary.ActiveRules++
		}
	}
	channels, err := api.store.ListNotificationChannels(r.Context())
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if channel.Enabled && visibleTenants(r, channel.TenantIDs...) {
			summary.ActiveChannels++
		}
	}
	windows, err := api.store.GetActiveAlertMaintenanceWindows(r.Context())
	if err != nil {
		return nil, err
	}
	for _, window := range windows {
		if visibleWindow(r, window) {
			summary.HasMaintenance = true
			break
		}
	}
	// Global quiet-hours/settings state is intentionally not exposed to scoped
	// callers. Scope breakdown fields remain zero, matching the existing store.
	return summary, nil
}

func (api *API) actorLabel(r *http.Request) string {
	if api.actorResolver == nil {
		return "system"
	}
	if name := strings.TrimSpace(api.actorResolver(r)); name != "" {
		return name
	}
	return "system"
}

func (api *API) audit(r *http.Request, entry *storage.AuditEntry) {
	if api.auditLogger == nil || entry == nil {
		return
	}
	api.auditLogger(r, entry)
}

// ============================================================================
// Alert Summary Handler
// ============================================================================

func (api *API) handleAlertSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	summary, err := api.scopedSummary(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get alert summary")
		return
	}

	writeJSON(w, http.StatusOK, summary)
}

// ============================================================================
// Alerts Handlers
// ============================================================================

func (api *API) handleAlerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListAlerts(w, r)
	case http.MethodPost:
		api.handleCreateAlert(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	filters := storage.AlertFilters{}

	// Parse query parameters
	if status := r.URL.Query().Get("status"); status != "" {
		filters.Status = storage.AlertStatus(status)
	}
	if severity := r.URL.Query().Get("severity"); severity != "" {
		filters.Severity = storage.AlertSeverity(severity)
	}
	if scope := r.URL.Query().Get("scope"); scope != "" {
		filters.Scope = storage.AlertScope(scope)
	}
	if alertType := r.URL.Query().Get("type"); alertType != "" {
		filters.Type = storage.AlertType(alertType)
	}
	if tenantID := r.URL.Query().Get("tenant_id"); tenantID != "" {
		filters.TenantID = tenantID
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil && l > 0 {
			filters.Limit = l
		}
	}
	if offset := r.URL.Query().Get("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil && o >= 0 {
			filters.Offset = o
		}
	}
	// Use the store's tenant predicate when the caller owns exactly one tenant.
	// Multiple tenant sets are filtered below before computing pagination.
	if scope := requestScope(r); filters.TenantID == "" && !scope.AllTenants && len(scope.TenantIDs) == 1 {
		filters.TenantID = scope.TenantIDs[0]
	}

	// Get total count for pagination (without limit/offset)
	countFilters := filters
	countFilters.Limit = 0
	countFilters.Offset = 0
	allAlerts, err := api.store.ListActiveAlerts(r.Context(), countFilters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count alerts")
		return
	}
	// Scope before pagination and totals; never fetch a global page then filter.
	alerts := make([]storage.Alert, 0, len(allAlerts))
	for _, alert := range allAlerts {
		if visibleTenants(r, alert.TenantID) {
			alerts = append(alerts, alert)
		}
	}
	totalCount := len(alerts)
	start := 0
	// Preserve legacy semantics: offset is applied only with a positive limit.
	if filters.Limit > 0 {
		start = filters.Offset
	}
	if start > totalCount {
		start = totalCount
	}
	end := totalCount
	if filters.Limit > 0 && filters.Limit < end-start {
		end = start + filters.Limit
	}
	alerts = alerts[start:end]

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts":      alerts,
		"count":       len(alerts),
		"total_count": totalCount,
		"offset":      filters.Offset,
		"limit":       filters.Limit,
		"has_more":    filters.Limit > 0 && filters.Offset+len(alerts) < totalCount,
	})
}

func (api *API) handleCreateAlert(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var alert storage.Alert
	if err := decodeJSON(r.Body, &alert); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate required fields
	if alert.Type == "" {
		writeError(w, http.StatusBadRequest, "alert type is required")
		return
	}
	if alert.Severity == "" {
		alert.Severity = storage.AlertSeverityWarning
	}
	if alert.Scope == "" {
		alert.Scope = storage.AlertScopeDevice
	}
	if alert.Status == "" {
		alert.Status = storage.AlertStatusActive
	}
	if alert.Scope == storage.AlertScopeFleet && !requestScope(r).AllTenants {
		writeError(w, http.StatusForbidden, "global administrator required")
		return
	}

	if !api.requireTenants(w, r, alert.TenantID) ||
		!api.checkTargets(w, r, alert.TenantID, alert.SiteID, alert.AgentID, alert.DeviceSerial) {
		return
	}
	if alert.RuleID != 0 && !api.checkRecord(w, r, "rule", alert.RuleID) {
		return
	}
	if alert.ParentAlertID != nil && !api.checkRecord(w, r, "alert", *alert.ParentAlertID) {
		return
	}
	id, err := api.store.CreateAlert(r.Context(), &alert)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create alert")
		return
	}

	alert.ID = id
	api.audit(r, &storage.AuditEntry{
		Action:     "alert.create",
		TargetType: "alert",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Created alert: %s (%s)", alert.Title, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusCreated, alert)
}

func (api *API) handleAlertRoute(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path: /api/v1/alerts/{id}[/action]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/alerts/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid alert id")
		return
	}

	// Check for action subpath
	if len(parts) == 2 {
		action := parts[1]
		switch action {
		case "acknowledge":
			api.handleAcknowledgeAlert(w, r, id)
		case "resolve":
			api.handleResolveAlert(w, r, id)
		default:
			http.NotFound(w, r)
		}
		return
	}

	// Direct alert operations
	switch r.Method {
	case http.MethodGet:
		api.handleGetAlert(w, r, id)
	case http.MethodDelete:
		api.handleDeleteAlert(w, r, id)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetAlert(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	alert, err := api.store.GetAlert(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get alert")
		return
	}
	if alert == nil {
		http.NotFound(w, r)
		return
	}
	if !api.recordVisible(w, r, alert.TenantID) {
		return
	}

	writeJSON(w, http.StatusOK, alert)
}

func (api *API) handleDeleteAlert(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	// Resolve the alert (soft delete - mark as resolved)
	if !api.checkRecord(w, r, "alert", id) {
		return
	}
	if err := api.store.ResolveAlert(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete alert")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert.delete",
		TargetType: "alert",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Deleted alert (%s)", api.actorLabel(r)),
	})

	w.WriteHeader(http.StatusNoContent)
}

func (api *API) handleAcknowledgeAlert(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "alert", id) {
		return
	}
	acknowledgedBy := api.actorLabel(r)
	if err := api.store.AcknowledgeAlert(r.Context(), id, acknowledgedBy); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to acknowledge alert")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert.acknowledge",
		TargetType: "alert",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Acknowledged alert (%s)", acknowledgedBy),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (api *API) handleResolveAlert(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "alert", id) {
		return
	}
	if err := api.store.ResolveAlert(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve alert")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert.resolve",
		TargetType: "alert",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Resolved alert (%s)", api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// ============================================================================
// Alert Rules Handlers
// ============================================================================

func (api *API) handleAlertRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListAlertRules(w, r)
	case http.MethodPost:
		api.handleCreateAlertRule(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	// Note: filtering is done client-side for now since ListAlertRules doesn't take options
	rules, err := api.store.ListAlertRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list alert rules")
		return
	}

	// Optional client-side filtering
	enabledOnly := r.URL.Query().Get("enabled") == "true"
	alertType := r.URL.Query().Get("type")

	filtered := make([]storage.AlertRule, 0, len(rules))
	for _, rule := range rules {
		if !visibleRule(r, rule) {
			continue
		}
		if enabledOnly && !rule.Enabled {
			continue
		}
		if alertType != "" && string(rule.Type) != alertType {
			continue
		}
		filtered = append(filtered, rule)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rules": filtered,
		"count": len(filtered),
	})
}

func (api *API) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var rule storage.AlertRule
	if err := decodeJSON(r.Body, &rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate required fields
	if rule.Name == "" {
		writeError(w, http.StatusBadRequest, "rule name is required")
		return
	}
	if rule.Type == "" {
		writeError(w, http.StatusBadRequest, "rule type is required")
		return
	}
	if rule.Severity == "" {
		rule.Severity = storage.AlertSeverityWarning
	}
	if rule.Scope == "" {
		rule.Scope = storage.AlertScopeDevice
	}

	if !api.checkRule(w, r, &rule) {
		return
	}
	rule.CreatedBy = api.actorLabel(r)

	id, err := api.store.CreateAlertRule(r.Context(), &rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create alert rule")
		return
	}

	rule.ID = id
	api.audit(r, &storage.AuditEntry{
		Action:     "alert_rule.create",
		TargetType: "alert_rule",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Created alert rule: %s (%s)", rule.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusCreated, rule)
}

func (api *API) handleAlertRuleRoute(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/alert-rules/")
	idStr = strings.Trim(idStr, "/")
	if idStr == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetAlertRule(w, r, id)
	case http.MethodPut:
		api.handleUpdateAlertRule(w, r, id)
	case http.MethodDelete:
		api.handleDeleteAlertRule(w, r, id)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetAlertRule(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	rule, err := api.store.GetAlertRule(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get alert rule")
		return
	}
	if rule == nil {
		http.NotFound(w, r)
		return
	}
	if !visibleRule(r, *rule) {
		http.NotFound(w, r)
		return
	}
	if !api.recordVisible(w, r, rule.TenantIDs...) {
		return
	}

	writeJSON(w, http.StatusOK, rule)
}

func (api *API) handleUpdateAlertRule(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "rule", id) {
		return
	}
	var rule storage.AlertRule
	if err := decodeJSON(r.Body, &rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !api.checkRule(w, r, &rule) {
		return
	}
	rule.ID = id
	if err := api.store.UpdateAlertRule(r.Context(), &rule); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update alert rule")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert_rule.update",
		TargetType: "alert_rule",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Updated alert rule: %s (%s)", rule.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, rule)
}

func (api *API) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "rule", id) {
		return
	}
	if err := api.store.DeleteAlertRule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete alert rule")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert_rule.delete",
		TargetType: "alert_rule",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Deleted alert rule (%s)", api.actorLabel(r)),
	})

	w.WriteHeader(http.StatusNoContent)
}

// ============================================================================
// Notification Channels Handlers
// ============================================================================

func (api *API) handleNotificationChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListNotificationChannels(w, r)
	case http.MethodPost:
		api.handleCreateNotificationChannel(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	channels, err := api.store.ListNotificationChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list notification channels")
		return
	}

	filtered := make([]storage.NotificationChannel, 0, len(channels))
	for _, channel := range channels {
		if visibleTenants(r, channel.TenantIDs...) {
			filtered = append(filtered, channel)
		}
	}
	channels = filtered
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channels": channels,
		"count":    len(channels),
	})
}

func (api *API) handleCreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var channel storage.NotificationChannel
	if err := decodeJSON(r.Body, &channel); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate required fields
	if channel.Name == "" {
		writeError(w, http.StatusBadRequest, "channel name is required")
		return
	}
	if channel.Type == "" {
		writeError(w, http.StatusBadRequest, "channel type is required")
		return
	}

	if !api.requireTenants(w, r, channel.TenantIDs...) {
		return
	}
	id, err := api.store.CreateNotificationChannel(r.Context(), &channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create notification channel")
		return
	}

	channel.ID = id
	api.audit(r, &storage.AuditEntry{
		Action:     "notification_channel.create",
		TargetType: "notification_channel",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Created notification channel: %s (%s)", channel.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusCreated, channel)
}

func (api *API) handleNotificationChannelRoute(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/notification-channels/")
	idStr = strings.Trim(idStr, "/")
	if idStr == "" {
		http.NotFound(w, r)
		return
	}

	// Handle /api/v1/notification-channels/{id}/test route
	if strings.HasSuffix(idStr, "/test") {
		idStr = strings.TrimSuffix(idStr, "/test")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid channel id")
			return
		}
		api.handleTestExistingChannel(w, r, id)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetNotificationChannel(w, r, id)
	case http.MethodPut:
		api.handleUpdateNotificationChannel(w, r, id)
	case http.MethodDelete:
		api.handleDeleteNotificationChannel(w, r, id)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetNotificationChannel(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	channel, err := api.store.GetNotificationChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "notification channel not found")
		return
	}
	if channel == nil {
		http.NotFound(w, r)
		return
	}
	if !api.recordVisible(w, r, channel.TenantIDs...) {
		return
	}

	writeJSON(w, http.StatusOK, channel)
}

func (api *API) handleUpdateNotificationChannel(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "channel", id) {
		return
	}
	var req storage.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !api.requireTenants(w, r, req.TenantIDs...) {
		return
	}
	if !api.checkChannelDependents(w, r, id, &req) {
		return
	}
	req.ID = id

	if err := api.store.UpdateNotificationChannel(r.Context(), &req); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update notification channel")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "notification_channel.update",
		TargetType: "notification_channel",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Updated notification channel: %s (%s)", req.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, &req)
}

func (api *API) handleDeleteNotificationChannel(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "channel", id) {
		return
	}
	if !api.checkChannelDependents(w, r, id, nil) {
		return
	}
	if err := api.store.DeleteNotificationChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete notification channel")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "notification_channel.delete",
		TargetType: "notification_channel",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Deleted notification channel (%s)", api.actorLabel(r)),
	})

	w.WriteHeader(http.StatusNoContent)
}

// handleTestNotificationChannel tests a notification channel configuration (without saving).
// POST /api/v1/notification-channels/test
func (api *API) handleTestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if api.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "notification service not available")
		return
	}

	var req struct {
		Type       string `json:"type"`
		Name       string `json:"name"`
		ConfigJSON string `json:"config_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Create a temporary channel for testing
	channel := &storage.NotificationChannel{
		Type:       storage.ChannelType(req.Type),
		Name:       req.Name,
		ConfigJSON: req.ConfigJSON,
	}

	// Create test alert
	testAlert := &storage.Alert{
		Type:     "test",
		Severity: "info",
		Title:    "Test Notification",
		Message:  "This is a test notification from PrintMaster to verify your channel configuration.",
	}

	if err := api.notifier.TestChannel(r.Context(), channel, testAlert); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("test failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "test notification sent"})
}

// handleTestExistingChannel tests an existing notification channel by ID.
// POST /api/v1/notification-channels/{id}/test
func (api *API) handleTestExistingChannel(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "channel", id) {
		return
	}
	if api.notifier == nil {
		writeError(w, http.StatusServiceUnavailable, "notification service not available")
		return
	}

	channel, err := api.store.GetNotificationChannel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "notification channel not found")
		return
	}
	if channel == nil {
		http.NotFound(w, r)
		return
	}
	if !api.recordVisible(w, r, channel.TenantIDs...) {
		return
	}

	// Create test alert
	testAlert := &storage.Alert{
		Type:     "test",
		Severity: "info",
		Title:    "Test Notification",
		Message:  "This is a test notification from PrintMaster to verify your channel configuration.",
	}

	if err := api.notifier.TestChannel(r.Context(), channel, testAlert); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("test failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "test notification sent"})
}

// ============================================================================
// Escalation Policies Handlers
// ============================================================================

func (api *API) handleEscalationPolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListEscalationPolicies(w, r)
	case http.MethodPost:
		api.handleCreateEscalationPolicy(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleListEscalationPolicies(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	policies, err := api.store.ListEscalationPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list escalation policies")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"policies": policies,
		"count":    len(policies),
	})
}

func (api *API) handleCreateEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var policy storage.EscalationPolicy
	if err := decodeJSON(r.Body, &policy); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate required fields
	if policy.Name == "" {
		writeError(w, http.StatusBadRequest, "policy name is required")
		return
	}

	if !api.checkPolicyChannels(w, r, &policy) {
		return
	}
	id, err := api.store.CreateEscalationPolicy(r.Context(), &policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create escalation policy")
		return
	}

	policy.ID = id
	api.audit(r, &storage.AuditEntry{
		Action:     "escalation_policy.create",
		TargetType: "escalation_policy",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Created escalation policy: %s (%s)", policy.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusCreated, policy)
}

func (api *API) handleEscalationPolicyRoute(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/escalation-policies/")
	idStr = strings.Trim(idStr, "/")
	if idStr == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetEscalationPolicy(w, r, id)
	case http.MethodPut:
		api.handleUpdateEscalationPolicy(w, r, id)
	case http.MethodDelete:
		api.handleDeleteEscalationPolicy(w, r, id)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetEscalationPolicy(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "policy", id) {
		return
	}
	policy, err := api.store.GetEscalationPolicy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "escalation policy not found")
		return
	}

	writeJSON(w, http.StatusOK, policy)
}

func (api *API) handleUpdateEscalationPolicy(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "policy", id) {
		return
	}
	var req storage.EscalationPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !api.checkPolicyChannels(w, r, &req) {
		return
	}
	req.ID = id

	if err := api.store.UpdateEscalationPolicy(r.Context(), &req); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update escalation policy")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "escalation_policy.update",
		TargetType: "escalation_policy",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Updated escalation policy: %s (%s)", req.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, &req)
}

func (api *API) handleDeleteEscalationPolicy(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "policy", id) {
		return
	}
	if err := api.store.DeleteEscalationPolicy(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete escalation policy")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "escalation_policy.delete",
		TargetType: "escalation_policy",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Deleted escalation policy (%s)", api.actorLabel(r)),
	})

	w.WriteHeader(http.StatusNoContent)
}

// ============================================================================
// Maintenance Windows Handlers
// ============================================================================

func (api *API) handleMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListMaintenanceWindows(w, r)
	case http.MethodPost:
		api.handleCreateMaintenanceWindow(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleListMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	// Check if only active windows requested
	activeOnly := r.URL.Query().Get("active") == "true"

	var windows []storage.AlertMaintenanceWindow
	var err error

	if activeOnly {
		windows, err = api.store.GetActiveAlertMaintenanceWindows(r.Context())
	} else {
		windows, err = api.store.ListAlertMaintenanceWindows(r.Context())
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list maintenance windows")
		return
	}

	filtered := make([]storage.AlertMaintenanceWindow, 0, len(windows))
	for _, window := range windows {
		if visibleWindow(r, window) {
			filtered = append(filtered, window)
		}
	}
	windows = filtered
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"windows": windows,
		"count":   len(windows),
	})
}

func (api *API) handleCreateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var window storage.AlertMaintenanceWindow
	if err := decodeJSON(r.Body, &window); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate required fields
	if window.Name == "" {
		writeError(w, http.StatusBadRequest, "window name is required")
		return
	}
	if window.StartTime.IsZero() {
		writeError(w, http.StatusBadRequest, "start time is required")
		return
	}
	if window.EndTime.IsZero() {
		writeError(w, http.StatusBadRequest, "end time is required")
		return
	}
	if window.EndTime.Before(window.StartTime) {
		writeError(w, http.StatusBadRequest, "end time must be after start time")
		return
	}

	if !api.checkWindow(w, r, &window) {
		return
	}
	window.CreatedBy = api.actorLabel(r)

	id, err := api.store.CreateAlertMaintenanceWindow(r.Context(), &window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create maintenance window")
		return
	}

	window.ID = id
	api.audit(r, &storage.AuditEntry{
		Action:     "maintenance_window.create",
		TargetType: "maintenance_window",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Created maintenance window: %s (%s)", window.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusCreated, window)
}

func (api *API) handleMaintenanceWindowRoute(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/maintenance-windows/")
	idStr = strings.Trim(idStr, "/")
	if idStr == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid window id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetMaintenanceWindow(w, r, id)
	case http.MethodPut:
		api.handleUpdateMaintenanceWindow(w, r, id)
	case http.MethodDelete:
		api.handleDeleteMaintenanceWindow(w, r, id)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetMaintenanceWindow(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	window, err := api.store.GetAlertMaintenanceWindow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "maintenance window not found")
		return
	}
	if window == nil {
		http.NotFound(w, r)
		return
	}
	if !visibleWindow(r, *window) {
		http.NotFound(w, r)
		return
	}
	if !api.recordVisible(w, r, window.TenantID) {
		return
	}

	writeJSON(w, http.StatusOK, window)
}

func (api *API) handleUpdateMaintenanceWindow(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "window", id) {
		return
	}
	var req storage.AlertMaintenanceWindow
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !api.checkWindow(w, r, &req) {
		return
	}
	req.ID = id

	if err := api.store.UpdateAlertMaintenanceWindow(r.Context(), &req); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update maintenance window")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "maintenance_window.update",
		TargetType: "maintenance_window",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Updated maintenance window: %s (%s)", req.Name, api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, &req)
}

func (api *API) handleDeleteMaintenanceWindow(w http.ResponseWriter, r *http.Request, id int64) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	if !api.checkRecord(w, r, "window", id) {
		return
	}
	if err := api.store.DeleteAlertMaintenanceWindow(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete maintenance window")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "maintenance_window.delete",
		TargetType: "maintenance_window",
		TargetID:   fmt.Sprintf("%d", id),
		Details:    fmt.Sprintf("Deleted maintenance window (%s)", api.actorLabel(r)),
	})

	w.WriteHeader(http.StatusNoContent)
}

// ============================================================================
// Alert Settings Handlers
// ============================================================================

func (api *API) handleAlertSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleGetAlertSettings(w, r)
	case http.MethodPut:
		api.handleSaveAlertSettings(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleGetAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsRead, authz.ResourceRef{}) {
		return
	}

	settings, err := api.store.GetAlertSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get alert settings")
		return
	}

	writeJSON(w, http.StatusOK, settings)
}

func (api *API) handleSaveAlertSettings(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, authz.ActionSettingsAlertsWrite, authz.ResourceRef{}) {
		return
	}

	var settings storage.AlertSettings
	if err := decodeJSON(r.Body, &settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validate quiet hours: if start == end, reject as this is likely a mistake
	// (would cause ALL non-critical alerts to be suppressed 24/7)
	if settings.QuietHours.Enabled &&
		settings.QuietHours.StartTime != "" &&
		settings.QuietHours.StartTime == settings.QuietHours.EndTime {
		writeError(w, http.StatusBadRequest, "quiet hours start and end times cannot be the same (would suppress alerts 24/7)")
		return
	}

	if err := api.store.SaveAlertSettings(r.Context(), &settings); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save alert settings")
		return
	}

	api.audit(r, &storage.AuditEntry{
		Action:     "alert_settings.update",
		TargetType: "alert_settings",
		TargetID:   "global",
		Details:    fmt.Sprintf("Updated alert settings (%s)", api.actorLabel(r)),
	})

	writeJSON(w, http.StatusOK, settings)
}

// ============================================================================
// Helper Functions
// ============================================================================

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log error but can't do much at this point
		_ = err
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func decodeJSON(body io.Reader, v interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20)) // 1MB limit
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

// Ensure time.Time is imported for maintenance window validation
var _ = time.Now
