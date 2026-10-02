package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	pmsettings "printmaster/common/settings"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
	"printmaster/server/tenancy"
)

// APIOptions carry cross-cutting concerns required by the HTTP layer.
type APIOptions struct {
	AuthMiddleware    func(http.HandlerFunc) http.HandlerFunc
	Authorizer        func(*http.Request, authz.Action, authz.ResourceRef) error
	ActorResolver     func(*http.Request) string
	AuditLogger       func(*http.Request, *storage.AuditEntry)
	LockedKeysChecker func() map[string]bool // Returns map of keys locked by env vars
}

// RouteConfig controls how HTTP routes are registered.
type RouteConfig struct {
	Mux                 *http.ServeMux
	FeatureEnabled      bool
	RegisterTenantAlias bool
}

// API exposes HTTP handlers for server-controlled settings.
type API struct {
	store             Store
	resolver          *Resolver
	authWrap          func(http.HandlerFunc) http.HandlerFunc
	authorizer        func(*http.Request, authz.Action, authz.ResourceRef) error
	actorResolver     func(*http.Request) string
	auditLogger       func(*http.Request, *storage.AuditEntry)
	lockedKeysChecker func() map[string]bool
}

// NewAPI builds an API backed by the provided store/resolver.
// Returns an error if store is nil.
func NewAPI(store Store, resolver *Resolver, opts APIOptions) (*API, error) {
	if store == nil {
		return nil, errors.New("settings API requires a store")
	}
	if resolver == nil {
		var err error
		resolver, err = NewResolver(store)
		if err != nil {
			return nil, fmt.Errorf("failed to create resolver: %w", err)
		}
	}
	return &API{
		store:             store,
		resolver:          resolver,
		authWrap:          opts.AuthMiddleware,
		authorizer:        opts.Authorizer,
		actorResolver:     opts.ActorResolver,
		auditLogger:       opts.AuditLogger,
		lockedKeysChecker: opts.LockedKeysChecker,
	}, nil
}

// RegisterRoutes wires the HTTP handlers onto the mux based on the provided config.
func (api *API) RegisterRoutes(cfg RouteConfig) {
	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("settings", nil)
	}
	if !cfg.FeatureEnabled {
		return
	}

	mux := cfg.Mux
	if mux == nil {
		mux = http.DefaultServeMux
	}
	wrap := api.wrap

	mux.HandleFunc("/api/v1/settings/schema", wrap(api.handleSchema))
	mux.HandleFunc("/api/v1/settings/global", wrap(api.handleGlobal))
	mux.HandleFunc("/api/v1/settings/tenants/", wrap(api.handleTenantSettingsRoute))
	mux.HandleFunc("/api/v1/settings/agents/", wrap(api.handleAgentSettingsRoute))

	if cfg.RegisterTenantAlias {
		tenancy.RegisterTenantSubresource("settings", api.tenantSubresourceHandler())
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

// checkLockedKeys returns a list of keys in the payload that are locked by environment variables.
// Returns nil if no keys are locked or if lock checking is disabled.
func (api *API) checkLockedKeys(payload map[string]interface{}) []string {
	if api.lockedKeysChecker == nil {
		return nil
	}
	lockedKeys := api.lockedKeysChecker()
	if len(lockedKeys) == 0 {
		return nil
	}

	var conflicts []string
	// Flatten payload keys and check against locked set
	// Settings keys typically look like "smtp.enabled", "server.http_port", etc.
	for key := range payload {
		if lockedKeys[key] {
			conflicts = append(conflicts, key)
		}
	}
	return conflicts
}

func (api *API) handleSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionSettingsFleetRead, authz.ResourceRef{}) {
		return
	}
	writeJSON(w, http.StatusOK, pmsettings.DefaultSchema())
}

func (api *API) handleGlobal(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !api.authorize(w, r, authz.ActionSettingsFleetRead, authz.ResourceRef{}) {
			return
		}
		snap, err := api.resolver.ResolveGlobal(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	case http.MethodPut:
		if !api.authorize(w, r, authz.ActionSettingsServerWrite, authz.ResourceRef{}) {
			return
		}
		// Decode wrapper struct that includes both settings and managed_sections
		var wrapper struct {
			pmsettings.Settings
			ManagedSections *[]string `json:"managed_sections,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wrapper); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		payload := wrapper.Settings

		// Check for locked keys (set by environment variables)
		// Convert settings struct to flat map for checking
		payloadMap := make(map[string]interface{})
		payloadBytes, _ := json.Marshal(payload)
		_ = json.Unmarshal(payloadBytes, &payloadMap)

		if lockedKeys := api.checkLockedKeys(payloadMap); len(lockedKeys) > 0 {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":       "cannot modify locked settings",
				"locked_keys": lockedKeys,
				"reason":      "These keys are set by environment variables and cannot be overridden by managed settings",
			})
			return
		}

		if issues := pmsettings.Validate(payload); len(issues) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":             "invalid settings",
				"validation_errors": issues,
			})
			return
		}
		pmsettings.Sanitize(&payload)
		actor := api.actorLabel(r)

		// Validate and normalize managed sections
		var managedSections []string
		if wrapper.ManagedSections == nil {
			managedSections = normalizeManagedSections(nil)
		} else {
			managedSections = normalizeManagedSections(*wrapper.ManagedSections)
		}

		rec := &storage.SettingsRecord{
			SchemaVersion:   pmsettings.SchemaVersion,
			Settings:        payload,
			ManagedSections: managedSections,
			UpdatedBy:       actor,
		}
		if err := api.store.UpsertGlobalSettings(r.Context(), rec); err != nil {
			writeStoreError(w, err)
			return
		}
		snap, err := api.resolver.ResolveGlobal(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.global.update",
			TargetType: "settings",
			TargetID:   "global",
			Details:    fmt.Sprintf("Global settings updated by %s", actor),
			Metadata: map[string]interface{}{
				"schema_version": pmsettings.SchemaVersion,
			},
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (api *API) handleTenantSettingsRoute(w http.ResponseWriter, r *http.Request) {
	tenantID := strings.TrimPrefix(r.URL.Path, "/api/v1/settings/tenants/")
	tenantID = strings.Trim(tenantID, "/")
	if tenantID == "" || strings.Contains(tenantID, "/") {
		http.NotFound(w, r)
		return
	}
	api.handleTenantSettings(w, r, tenantID)
}

func (api *API) handleAgentSettingsRoute(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/settings/agents/")
	agentID = strings.Trim(agentID, "/")
	if agentID == "" || strings.Contains(agentID, "/") {
		http.NotFound(w, r)
		return
	}
	api.handleAgentSettings(w, r, agentID)
}

func (api *API) tenantSubresourceHandler() tenancy.TenantSubresourceHandler {
	return func(w http.ResponseWriter, r *http.Request, tenantID string, rest string) {
		if strings.Trim(rest, "/") != "" {
			http.NotFound(w, r)
			return
		}
		handler := api.wrap(func(w http.ResponseWriter, r *http.Request) {
			api.handleTenantSettings(w, r, tenantID)
		})
		handler(w, r)
	}
}

func (api *API) handleTenantSettings(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant id required")
		return
	}
	resource := authz.ResourceRef{TenantIDs: []string{tenantID}}
	switch r.Method {
	case http.MethodGet:
		if !api.authorize(w, r, authz.ActionSettingsFleetRead, resource) {
			return
		}
		api.writeTenantSnapshot(w, r, tenantID)
	case http.MethodPut:
		if !api.authorize(w, r, authz.ActionSettingsFleetWrite, resource) {
			return
		}
		api.saveTenantOverrides(w, r, tenantID)
	case http.MethodDelete:
		if !api.authorize(w, r, authz.ActionSettingsFleetWrite, resource) {
			return
		}
		if err := api.store.DeleteTenantSettings(r.Context(), tenantID); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.tenant.reset",
			TargetType: "settings",
			TargetID:   tenantID,
			TenantID:   tenantID,
			Details:    fmt.Sprintf("Cleared overrides for tenant %s", tenantID),
		})
		api.writeTenantSnapshot(w, r, tenantID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (api *API) handleAgentSettings(w http.ResponseWriter, r *http.Request, agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent id required")
		return
	}
	agent, err := api.store.GetAgent(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeStoreError(w, err)
		return
	}
	resource := authz.ResourceRef{}
	readAction, writeAction := authz.ActionSettingsServerRead, authz.ActionSettingsServerWrite
	if strings.TrimSpace(agent.TenantID) != "" {
		resource = authz.ResourceRef{TenantIDs: []string{agent.TenantID}}
		readAction, writeAction = authz.ActionSettingsFleetRead, authz.ActionSettingsFleetWrite
	}
	switch r.Method {
	case http.MethodGet:
		if !api.authorize(w, r, readAction, resource) {
			return
		}
		snap, err := api.resolver.ResolveForAgent(r.Context(), agentID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	case http.MethodPut:
		if !api.authorize(w, r, writeAction, resource) {
			return
		}
		api.saveAgentOverrides(w, r, agent)
	case http.MethodDelete:
		if !api.authorize(w, r, writeAction, resource) {
			return
		}
		if err := api.store.DeleteAgentSettings(r.Context(), agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.agent.reset",
			TargetType: "settings",
			TargetID:   agentID,
			TenantID:   agent.TenantID,
			Details:    fmt.Sprintf("Cleared overrides for agent %s", agentID),
		})
		snap, err := api.resolver.ResolveForAgent(r.Context(), agentID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (api *API) writeTenantSnapshot(w http.ResponseWriter, r *http.Request, tenantID string) {
	if err := api.ensureTenantExists(r.Context(), tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeStoreError(w, err)
		return
	}
	snap, err := api.resolver.ResolveForTenant(r.Context(), tenantID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (api *API) saveTenantOverrides(w http.ResponseWriter, r *http.Request, tenantID string) {
	if err := api.ensureTenantExists(r.Context(), tenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeStoreError(w, err)
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "payload required")
		return
	}
	// Back-compat: older clients send the overrides map directly.
	patch := body
	var enforcedSections []string
	var hasEnforced bool
	if raw, ok := body["overrides"]; ok {
		m, ok := raw.(map[string]interface{})
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid overrides")
			return
		}
		patch = m
		if es, ok := body["enforced_sections"]; ok {
			enforcedSections = parseStringSlice(es)
			hasEnforced = true
		}
	} else if es, ok := body["enforced_sections"]; ok {
		enforcedSections = parseStringSlice(es)
		hasEnforced = true
		patch = map[string]interface{}{}
	}
	globalSnap, err := api.resolver.ResolveGlobal(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	existingRec, err := api.store.GetTenantSettings(r.Context(), tenantID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var existing map[string]interface{}
	var existingEnforced []string
	if existingRec != nil {
		existing = existingRec.Overrides
		existingEnforced = existingRec.EnforcedSections
	}
	if !hasEnforced {
		enforcedSections = existingEnforced
	}
	merged := MergeOverrideMaps(existing, patch)
	cleaned, err := CleanOverrides(globalSnap.Settings, merged)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	effective, err := ApplyPatch(globalSnap.Settings, cleaned)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if issues := pmsettings.Validate(effective); len(issues) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":             "invalid settings",
			"validation_errors": issues,
		})
		return
	}
	actor := api.actorLabel(r)
	keys := collectOverrideKeys(cleaned)
	enforcedSections = normalizeSectionList(enforcedSections)
	if len(cleaned) == 0 && len(enforcedSections) == 0 {
		if err := api.store.DeleteTenantSettings(r.Context(), tenantID); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.tenant.reset",
			TargetType: "settings",
			TargetID:   tenantID,
			TenantID:   tenantID,
			Details:    fmt.Sprintf("Cleared overrides for tenant %s", tenantID),
		})
	} else {
		rec := &storage.TenantSettingsRecord{
			TenantID:         tenantID,
			SchemaVersion:    pmsettings.SchemaVersion,
			Overrides:        cleaned,
			EnforcedSections: enforcedSections,
			UpdatedBy:        actor,
		}
		if err := api.store.UpsertTenantSettings(r.Context(), rec); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.tenant.update",
			TargetType: "settings",
			TargetID:   tenantID,
			TenantID:   tenantID,
			Details:    fmt.Sprintf("Updated %d override(s) for tenant %s", len(keys), tenantID),
			Metadata: map[string]interface{}{
				"override_keys":  keys,
				"schema_version": pmsettings.SchemaVersion,
			},
		})
	}
	snap, err := api.resolver.ResolveForTenant(r.Context(), tenantID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (api *API) saveAgentOverrides(w http.ResponseWriter, r *http.Request, agent *storage.Agent) {
	if agent == nil {
		writeError(w, http.StatusBadRequest, "agent required")
		return
	}
	agentID := strings.TrimSpace(agent.AgentID)
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent id required")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "payload required")
		return
	}
	patch := body
	if raw, ok := body["overrides"]; ok {
		m, ok := raw.(map[string]interface{})
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid overrides")
			return
		}
		patch = m
	}
	if len(patch) == 0 {
		writeError(w, http.StatusBadRequest, "payload required")
		return
	}

	// Determine base layer (global or tenant-resolved).
	var baseSnap Snapshot
	var enforced []string
	if strings.TrimSpace(agent.TenantID) == "" {
		gs, err := api.resolver.ResolveGlobal(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		baseSnap = gs
	} else {
		ts, err := api.resolver.ResolveForTenant(r.Context(), agent.TenantID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		baseSnap = ts.Snapshot
		enforced = ts.EnforcedSections
	}

	allowed := allowedAgentOverrideSections(baseSnap.ManagedSections, enforced)
	filtered, blocked := filterOverrideSections(patch, allowed)
	if len(blocked) > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":            "cannot override blocked sections",
			"blocked_sections": blocked,
		})
		return
	}

	existingRec, err := api.store.GetAgentSettings(r.Context(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var existing map[string]interface{}
	if existingRec != nil {
		existing = existingRec.Overrides
	}
	merged := MergeOverrideMaps(existing, filtered)
	merged, _ = filterOverrideSections(merged, allowed)

	cleaned, err := CleanOverrides(baseSnap.Settings, merged)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	effective, err := ApplyPatch(baseSnap.Settings, cleaned)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if issues := pmsettings.Validate(effective); len(issues) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":             "invalid settings",
			"validation_errors": issues,
		})
		return
	}

	actor := api.actorLabel(r)
	keys := collectOverrideKeys(cleaned)
	if len(cleaned) == 0 {
		if err := api.store.DeleteAgentSettings(r.Context(), agentID); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.agent.reset",
			TargetType: "settings",
			TargetID:   agentID,
			TenantID:   agent.TenantID,
			Details:    fmt.Sprintf("Cleared overrides for agent %s", agentID),
		})
	} else {
		rec := &storage.AgentSettingsRecord{
			AgentID:       agentID,
			SchemaVersion: pmsettings.SchemaVersion,
			Overrides:     cleaned,
			UpdatedBy:     actor,
		}
		if err := api.store.UpsertAgentSettings(r.Context(), rec); err != nil {
			writeStoreError(w, err)
			return
		}
		api.audit(r, &storage.AuditEntry{
			Action:     "settings.agent.update",
			TargetType: "settings",
			TargetID:   agentID,
			TenantID:   agent.TenantID,
			Details:    fmt.Sprintf("Updated %d override(s) for agent %s", len(keys), agentID),
			Metadata: map[string]interface{}{
				"override_keys":  keys,
				"schema_version": pmsettings.SchemaVersion,
			},
		})
	}

	snap, err := api.resolver.ResolveForAgent(r.Context(), agentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (api *API) ensureTenantExists(ctx context.Context, tenantID string) error {
	_, err := api.store.GetTenant(ctx, tenantID)
	return err
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeStoreError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error":  "storage operation failed",
		"detail": err.Error(),
	})
}

func collectOverrideKeys(m map[string]interface{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	var walk func(map[string]interface{}, string)
	walk = func(curr map[string]interface{}, prefix string) {
		if curr == nil {
			return
		}
		for k, v := range curr {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if child, ok := v.(map[string]interface{}); ok {
				walk(child, key)
			} else {
				keys = append(keys, key)
			}
		}
	}
	walk(m, "")
	sort.Strings(keys)
	return keys
}

func parseStringSlice(raw interface{}) []string {
	if raw == nil {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	seen := make(map[string]bool)
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ValidManagedSections are the sections that can be server-managed.
var ValidManagedSections = map[string]bool{
	"discovery": true,
	"snmp":      true,
	"features":  true,
	"spooler":   true,
}

// normalizeManagedSections validates and normalizes the managed sections list.
// If empty or nil, returns all sections enabled by default.
func normalizeManagedSections(sections []string) []string {
	if len(sections) == 0 {
		return []string{}
	}
	// Filter to only valid sections
	result := make([]string, 0, len(sections))
	seen := make(map[string]bool)
	for _, s := range sections {
		s = strings.TrimSpace(strings.ToLower(s))
		if ValidManagedSections[s] && !seen[s] {
			result = append(result, s)
			seen[s] = true
		}
	}
	sort.Strings(result)
	return result
}
