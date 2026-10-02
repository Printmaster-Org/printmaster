package updatepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"printmaster/common/updatepolicy"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
	"printmaster/server/tenancy"
)

const globalPolicyAlias = "global"

// Store captures the persistence operations needed by the update policy API.
type Store interface {
	GetFleetUpdatePolicy(context.Context, string) (*storage.FleetUpdatePolicy, error)
	UpsertFleetUpdatePolicy(context.Context, *storage.FleetUpdatePolicy) error
	DeleteFleetUpdatePolicy(context.Context, string) error
	ListFleetUpdatePolicies(context.Context) ([]*storage.FleetUpdatePolicy, error)
}

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

// API exposes HTTP handlers for fleet update policies.
type API struct {
	store         Store
	authWrap      func(http.HandlerFunc) http.HandlerFunc
	authorizer    func(*http.Request, authz.Action, authz.ResourceRef) error
	actorResolver func(*http.Request) string
	auditLogger   func(*http.Request, *storage.AuditEntry)
}

// NewAPI builds a new fleet update policy API instance.
// Returns an error if store is nil.
func NewAPI(store Store, opts APIOptions) (*API, error) {
	if store == nil {
		return nil, errors.New("update policy API requires a store")
	}
	return &API{
		store:         store,
		authWrap:      opts.AuthMiddleware,
		authorizer:    opts.Authorizer,
		actorResolver: opts.ActorResolver,
		auditLogger:   opts.AuditLogger,
	}, nil
}

// RegisterRoutes wires all update policy endpoints when the feature is enabled.
func (api *API) RegisterRoutes(cfg RouteConfig) {
	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("update-policy", nil)
	}
	if !cfg.FeatureEnabled {
		return
	}

	mux := cfg.Mux
	if mux == nil {
		mux = http.DefaultServeMux
	}

	wrap := api.wrap
	mux.HandleFunc("/api/v1/update-policies", wrap(api.handleListPolicies))
	mux.HandleFunc("/api/v1/update-policies/", wrap(api.handlePolicyRoute))

	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("update-policy", api.tenantSubresourceHandler())
	}
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

func (api *API) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionTenantsRead, authz.ResourceRef{}) {
		return
	}
	policies, err := api.store.ListFleetUpdatePolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list policies")
		return
	}
	sort.Slice(policies, func(i, j int) bool {
		return strings.Compare(displayPolicyTenantID(policies[i].TenantID), displayPolicyTenantID(policies[j].TenantID)) < 0
	})
	resp := make([]policyResponse, 0, len(policies))
	for _, policy := range policies {
		resp = append(resp, toPolicyResponse(policy))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (api *API) handlePolicyRoute(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimPrefix(r.URL.Path, "/api/v1/update-policies/")
	tenantID = strings.Trim(tenantID, "/")
	if tenantID == "" {
		http.NotFound(w, r)
		return
	}
	// Disallow nested subpaths for now.
	if strings.Contains(tenantID, "/") {
		http.NotFound(w, r)
		return
	}
	api.handleTenantPolicy(w, r, tenantID)
}

func (api *API) tenantSubresourceHandler() tenancy.TenantSubresourceHandler {
	return func(w http.ResponseWriter, r *http.Request, tenantID, rest string) {
		if strings.TrimSpace(rest) != "" {
			http.NotFound(w, r)
			return
		}
		api.handleTenantPolicy(w, r, tenantID)
	}
}

func (api *API) handleTenantPolicy(w http.ResponseWriter, r *http.Request, tenantRef string) {
	trimmed := strings.TrimSpace(tenantRef)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, "tenant id required")
		return
	}
	realTenantID, isGlobal := normalizePolicyTenantID(trimmed)
	if realTenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleTenantPolicyGet(w, r, realTenantID, isGlobal)
	case http.MethodPut:
		api.handleTenantPolicyPut(w, r, realTenantID, isGlobal)
	case http.MethodDelete:
		api.handleTenantPolicyDelete(w, r, realTenantID, isGlobal)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (api *API) handleTenantPolicyGet(w http.ResponseWriter, r *http.Request, tenantID string, isGlobal bool) {
	resource := authz.ResourceRef{}
	action := authz.ActionTenantsRead
	if !isGlobal {
		resource = authz.ResourceRef{TenantIDs: []string{tenantID}}
	} else {
		action = authz.ActionSettingsFleetRead
	}
	if !api.authorize(w, r, action, resource) {
		return
	}
	policy, err := api.store.GetFleetUpdatePolicy(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch policy")
		return
	}
	if policy == nil {
		writeError(w, http.StatusNotFound, "policy not configured")
		return
	}
	writeJSON(w, http.StatusOK, toPolicyResponse(policy))
}

func (api *API) handleTenantPolicyPut(w http.ResponseWriter, r *http.Request, tenantID string, isGlobal bool) {
	resource := authz.ResourceRef{}
	action := authz.ActionTenantsWrite
	if !isGlobal {
		resource = authz.ResourceRef{TenantIDs: []string{tenantID}}
	} else {
		action = authz.ActionSettingsServerWrite
	}
	if !api.authorize(w, r, action, resource) {
		return
	}
	spec, err := decodePolicyPayload(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec = normalizePolicySpec(spec)
	if issues := validatePolicySpec(spec); len(issues) > 0 {
		writeValidationError(w, issues)
		return
	}
	record := &storage.FleetUpdatePolicy{TenantID: tenantID, PolicySpec: spec}
	if err := api.store.UpsertFleetUpdatePolicy(r.Context(), record); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save policy")
		return
	}
	persisted, err := api.store.GetFleetUpdatePolicy(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload policy")
		return
	}
	api.audit(r, &storage.AuditEntry{
		Action:     "tenant.update_policy.write",
		TargetType: "tenant_update_policy",
		TargetID:   displayPolicyTenantID(tenantID),
		TenantID:   auditTenantID(isGlobal, tenantID),
		Details:    fmt.Sprintf("Updated fleet update policy (%s)", api.actorLabel(r)),
		Metadata: map[string]interface{}{
			"tenant_id":            displayPolicyTenantID(tenantID),
			"update_check_days":    spec.UpdateCheckDays,
			"version_pin_strategy": spec.VersionPinStrategy,
			"allow_major_upgrade":  spec.AllowMajorUpgrade,
		},
	})
	writeJSON(w, http.StatusOK, toPolicyResponse(persisted))
}

func (api *API) handleTenantPolicyDelete(w http.ResponseWriter, r *http.Request, tenantID string, isGlobal bool) {
	resource := authz.ResourceRef{}
	action := authz.ActionTenantsWrite
	if !isGlobal {
		resource = authz.ResourceRef{TenantIDs: []string{tenantID}}
	} else {
		action = authz.ActionSettingsServerWrite
	}
	if !api.authorize(w, r, action, resource) {
		return
	}
	if err := api.store.DeleteFleetUpdatePolicy(r.Context(), tenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete policy")
		return
	}
	api.audit(r, &storage.AuditEntry{
		Action:     "tenant.update_policy.delete",
		TargetType: "tenant_update_policy",
		TargetID:   displayPolicyTenantID(tenantID),
		TenantID:   auditTenantID(isGlobal, tenantID),
		Details:    fmt.Sprintf("Deleted fleet update policy (%s)", api.actorLabel(r)),
	})
	w.WriteHeader(http.StatusNoContent)
}

type policyPayload struct {
	Policy *updatepolicy.PolicySpec `json:"policy"`
}

type policyResponse struct {
	TenantID  string                  `json:"tenant_id"`
	Policy    updatepolicy.PolicySpec `json:"policy"`
	UpdatedAt *time.Time              `json:"updated_at,omitempty"`
}

func normalizePolicyTenantID(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	if strings.EqualFold(trimmed, globalPolicyAlias) || trimmed == storage.GlobalFleetPolicyTenantID {
		return storage.GlobalFleetPolicyTenantID, true
	}
	return trimmed, false
}

func displayPolicyTenantID(value string) string {
	if value == storage.GlobalFleetPolicyTenantID {
		return globalPolicyAlias
	}
	return value
}

func auditTenantID(isGlobal bool, tenantID string) string {
	if isGlobal {
		return ""
	}
	return tenantID
}

func decodePolicyPayload(body io.Reader) (updatepolicy.PolicySpec, error) {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	decoder.DisallowUnknownFields()
	var payload policyPayload
	if err := decoder.Decode(&payload); err != nil {
		return updatepolicy.PolicySpec{}, fmt.Errorf("invalid json: %w", err)
	}
	if payload.Policy == nil {
		return updatepolicy.PolicySpec{}, errors.New("policy field is required")
	}
	return *payload.Policy, nil
}

func toPolicyResponse(policy *storage.FleetUpdatePolicy) policyResponse {
	resp := policyResponse{
		TenantID: displayPolicyTenantID(policy.TenantID),
		Policy:   clonePolicySpec(policy.PolicySpec),
	}
	if !policy.UpdatedAt.IsZero() {
		ts := policy.UpdatedAt.UTC()
		resp.UpdatedAt = &ts
	}
	return resp
}

func normalizePolicySpec(spec updatepolicy.PolicySpec) updatepolicy.PolicySpec {
	normalized := clonePolicySpec(spec)
	normalized.VersionPinStrategy = normalizePinStrategy(spec.VersionPinStrategy)
	normalized.TargetVersion = strings.TrimSpace(spec.TargetVersion)
	normalized.MaintenanceWindow = normalizeMaintenanceWindow(spec.MaintenanceWindow)
	return normalized
}

func normalizePinStrategy(strategy updatepolicy.VersionPinStrategy) updatepolicy.VersionPinStrategy {
	switch strings.ToLower(strings.TrimSpace(string(strategy))) {
	case string(updatepolicy.VersionPinMajor):
		return updatepolicy.VersionPinMajor
	case string(updatepolicy.VersionPinPatch):
		return updatepolicy.VersionPinPatch
	default:
		return updatepolicy.VersionPinMinor
	}
}

func normalizeMaintenanceWindow(mw updatepolicy.MaintenanceWindow) updatepolicy.MaintenanceWindow {
	normalized := mw
	normalized.Timezone = strings.TrimSpace(mw.Timezone)
	normalized.DaysOfWeek = normalizeDaysOfWeek(mw.DaysOfWeek)
	return normalized
}

func normalizeDaysOfWeek(days []int) []int {
	if len(days) == 0 {
		return nil
	}
	unique := make(map[int]struct{}, len(days))
	for _, d := range days {
		unique[d] = struct{}{}
	}
	out := make([]int, 0, len(unique))
	for day := range unique {
		out = append(out, day)
	}
	sort.Ints(out)
	return out
}

func validatePolicySpec(spec updatepolicy.PolicySpec) []string {
	var issues []string
	if spec.UpdateCheckDays < 0 {
		issues = append(issues, "update_check_days must be >= 0")
	}
	switch spec.VersionPinStrategy {
	case updatepolicy.VersionPinMajor, updatepolicy.VersionPinMinor, updatepolicy.VersionPinPatch:
	default:
		issues = append(issues, "version_pin_strategy must be one of major, minor, patch")
	}
	mw := spec.MaintenanceWindow
	if mw.Enabled {
		if mw.Timezone == "" {
			issues = append(issues, "maintenance_window.timezone is required when enabled")
		}
		if mw.StartHour < 0 || mw.StartHour > 23 {
			issues = append(issues, "maintenance_window.start_hour must be between 0 and 23")
		}
		if mw.StartMin < 0 || mw.StartMin > 59 {
			issues = append(issues, "maintenance_window.start_min must be between 0 and 59")
		}
		if mw.EndHour < 0 || mw.EndHour > 23 {
			issues = append(issues, "maintenance_window.end_hour must be between 0 and 23")
		}
		if mw.EndMin < 0 || mw.EndMin > 59 {
			issues = append(issues, "maintenance_window.end_min must be between 0 and 59")
		}
		if len(mw.DaysOfWeek) == 0 {
			issues = append(issues, "maintenance_window.days_of_week must include at least one day")
		}
		for _, day := range mw.DaysOfWeek {
			if day < 0 || day > 6 {
				issues = append(issues, "maintenance_window.days_of_week must be between 0 and 6")
				break
			}
		}
	}
	rc := spec.RolloutControl
	if rc.MaxConcurrent < 0 {
		issues = append(issues, "rollout_control.max_concurrent must be >= 0")
	}
	if rc.BatchSize < 0 {
		issues = append(issues, "rollout_control.batch_size must be >= 0")
	}
	if rc.DelayBetweenWaves < 0 {
		issues = append(issues, "rollout_control.delay_between_waves must be >= 0")
	}
	if rc.JitterSeconds < 0 {
		issues = append(issues, "rollout_control.jitter_seconds must be >= 0")
	}
	return issues
}

func clonePolicySpec(spec updatepolicy.PolicySpec) updatepolicy.PolicySpec {
	cloned := spec
	if len(spec.MaintenanceWindow.DaysOfWeek) > 0 {
		cloned.MaintenanceWindow.DaysOfWeek = append([]int(nil), spec.MaintenanceWindow.DaysOfWeek...)
	}
	return cloned
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{"error": message})
}

func writeValidationError(w http.ResponseWriter, details []string) {
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"error":   "invalid policy",
		"details": details,
	})
}
