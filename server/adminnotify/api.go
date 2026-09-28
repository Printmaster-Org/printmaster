package adminnotify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	authz "printmaster/server/authz"
	"printmaster/server/storage"
	"printmaster/server/tenancy"
)

// APIOptions provides cross-cutting infrastructure for the HTTP layer.
type APIOptions struct {
	AuthMiddleware func(http.HandlerFunc) http.HandlerFunc
	Authorizer     func(*http.Request, authz.Action, authz.ResourceRef) error
	ActorResolver  func(*http.Request) string
	AuditLogger    func(*http.Request, *storage.AuditEntry)
}

// RouteConfig controls how HTTP handlers are registered.
type RouteConfig struct {
	Mux                 *http.ServeMux
	FeatureEnabled      bool
	RegisterTenantAlias bool
}

// API exposes HTTP handlers for tenant-level notification settings.
// Fleet-level settings are served by the server settings endpoint.
type API struct {
	store         Store
	notifier      *Notifier
	authWrap      func(http.HandlerFunc) http.HandlerFunc
	authorizer    func(*http.Request, authz.Action, authz.ResourceRef) error
	actorResolver func(*http.Request) string
	auditLogger   func(*http.Request, *storage.AuditEntry)
}

// NewAPI builds the tenant notification settings API.
func NewAPI(store Store, notifier *Notifier, opts APIOptions) (*API, error) {
	if store == nil {
		return nil, errors.New("notification API requires a store")
	}
	return &API{
		store:         store,
		notifier:      notifier,
		authWrap:      opts.AuthMiddleware,
		authorizer:    opts.Authorizer,
		actorResolver: opts.ActorResolver,
		auditLogger:   opts.AuditLogger,
	}, nil
}

// RegisterRoutes wires notification settings endpoints onto the mux.
func (api *API) RegisterRoutes(cfg RouteConfig) {
	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("notifications", nil)
	}
	if !cfg.FeatureEnabled {
		return
	}
	mux := cfg.Mux
	if mux == nil {
		mux = http.DefaultServeMux
	}
	mux.HandleFunc("/api/v1/notification-settings/", api.wrap(api.handleRoute))
	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("notifications", api.tenantSubresourceHandler())
	}
}

func (api *API) wrap(handler http.HandlerFunc) http.HandlerFunc {
	if api.authWrap == nil {
		return handler
	}
	return api.authWrap(handler)
}

func (api *API) tenantSubresourceHandler() tenancy.TenantSubresourceHandler {
	return func(w http.ResponseWriter, r *http.Request, tenantID, rest string) {
		api.handleTenantSettings(w, r, tenantID, strings.Trim(rest, "/"))
	}
}

func (api *API) handleRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/notification-settings/"), "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	tenantID, rest, _ := strings.Cut(path, "/")
	api.handleTenantSettings(w, r, tenantID, rest)
}

func (api *API) handleTenantSettings(w http.ResponseWriter, r *http.Request, tenantID, rest string) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant id required")
		return
	}
	switch rest {
	case "":
		switch r.Method {
		case http.MethodGet:
			api.handleGet(w, r, tenantID)
		case http.MethodPut:
			api.handlePut(w, r, tenantID)
		case http.MethodDelete:
			api.handleDelete(w, r, tenantID)
		default:
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		}
	case "test":
		if r.Method != http.MethodPost {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		api.handleTest(w, r, tenantID)
	default:
		http.NotFound(w, r)
	}
}

func (api *API) handleGet(w http.ResponseWriter, r *http.Request, tenantID string) {
	if !api.authorize(w, r, authz.ActionTenantsRead, authz.ResourceRef{TenantIDs: []string{tenantID}}) {
		return
	}
	rec, err := api.store.GetTenantNotificationSettings(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification settings")
		return
	}
	if rec == nil {
		rec = DefaultTenantSettings(tenantID)
	}
	writeJSON(w, http.StatusOK, rec)
}

func (api *API) handlePut(w http.ResponseWriter, r *http.Request, tenantID string) {
	if !api.authorize(w, r, authz.ActionTenantsWrite, authz.ResourceRef{TenantIDs: []string{tenantID}}) {
		return
	}
	var payload storage.NotificationSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	payload.TenantID = tenantID
	payload.UpdatedBy = api.actorLabel(r)
	if err := ValidateSettings(&payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := api.store.UpsertTenantNotificationSettings(r.Context(), &payload); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save notification settings")
		return
	}
	api.audit(r, &storage.AuditEntry{
		TenantID:   tenantID,
		Action:     "settings.notifications.update",
		TargetType: "tenant",
		TargetID:   tenantID,
		Details:    fmt.Sprintf("recipients=%d enabled=%t daily_summary=%t", len(payload.Recipients), payload.Enabled, payload.DailySummaryEnabled),
		Severity:   storage.AuditSeverityInfo,
	})
	writeJSON(w, http.StatusOK, payload)
}

func (api *API) handleDelete(w http.ResponseWriter, r *http.Request, tenantID string) {
	if !api.authorize(w, r, authz.ActionTenantsWrite, authz.ResourceRef{TenantIDs: []string{tenantID}}) {
		return
	}
	if err := api.store.DeleteTenantNotificationSettings(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete notification settings")
		return
	}
	api.audit(r, &storage.AuditEntry{
		TenantID:   tenantID,
		Action:     "settings.notifications.delete",
		TargetType: "tenant",
		TargetID:   tenantID,
		Severity:   storage.AuditSeverityInfo,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (api *API) handleTest(w http.ResponseWriter, r *http.Request, tenantID string) {
	if !api.authorize(w, r, authz.ActionTenantsWrite, authz.ResourceRef{TenantIDs: []string{tenantID}}) {
		return
	}
	sent, err := api.notifier.SendTest(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sent": sent})
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
	return true
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

// DefaultTenantSettings returns the notification defaults for a tenant that has
// never saved settings.
func DefaultTenantSettings(tenantID string) *storage.NotificationSettings {
	return &storage.NotificationSettings{
		TenantID:             tenantID,
		Enabled:              false,
		Recipients:           []string{},
		NotifyOnCritical:     true,
		NotifyOnWarning:      false,
		DailySummaryEnabled:  false,
		DailySummaryTime:     "08:00",
		DailySummaryTimezone: "Local",
	}
}

// ValidateSettings normalizes and checks a settings payload before persistence.
func ValidateSettings(rec *storage.NotificationSettings) error {
	rec.Recipients = normalizeRecipients(rec.Recipients)
	if rec.Enabled && len(rec.Recipients) == 0 {
		return errors.New("at least one valid recipient email is required when notifications are enabled")
	}
	if strings.TrimSpace(rec.DailySummaryTime) == "" {
		rec.DailySummaryTime = "08:00"
	}
	if !ValidSummaryTime(rec.DailySummaryTime) {
		return errors.New("daily_summary_time must be in HH:MM format")
	}
	if strings.TrimSpace(rec.DailySummaryTimezone) == "" {
		rec.DailySummaryTimezone = "Local"
	}
	if !ValidTimezone(rec.DailySummaryTimezone) {
		return errors.New("daily_summary_timezone is not a known timezone")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
