package main

import (
	context "context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	oidclib "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	adminnotify "printmaster/server/adminnotify"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

var (
	oidcCache = struct {
		mu        sync.Mutex
		providers map[string]*oidclib.Provider
	}{providers: make(map[string]*oidclib.Provider)}

	slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)
)

type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
}

type oidcProviderPayload struct {
	Slug         string   `json:"slug"`
	DisplayName  string   `json:"display_name"`
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	Icon         string   `json:"icon"`
	ButtonText   string   `json:"button_text"`
	ButtonStyle  string   `json:"button_style"`
	AutoLogin    bool     `json:"auto_login"`
	TenantID     string   `json:"tenant_id"`
	DefaultRole  string   `json:"default_role"`
}

func handleAuthOptions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	resolution := resolveTenantForAuthRequest(ctx, r)
	tenantID := ""
	if resolution != nil {
		tenantID = resolution.id
	}

	providers, err := serverStore.ListOIDCProviders(ctx, tenantID)
	if err != nil {
		serverLogger.Error("Failed to load OIDC providers for login", "tenant_id", tenantID, "error", err)
		http.Error(w, "failed to load providers", http.StatusInternalServerError)
		return
	}

	visible := make([]map[string]interface{}, 0, len(providers))
	for _, p := range providers {
		if tenantID == "" {
			if p.TenantID != "" {
				continue
			}
		} else if p.TenantID != "" && p.TenantID != tenantID {
			continue
		}
		visible = append(visible, publicProviderDTO(p))
	}

	resp := map[string]interface{}{
		"local_login": true,
		"providers":   visible,
	}
	if resolution != nil && resolution.id != "" {
		resp["tenant_id"] = resolution.id
		if resolution.name != "" {
			resp["tenant_name"] = resolution.name
		}
		if resolution.source != "" {
			resp["tenant_source"] = resolution.source
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleTenantLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if serverStore == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	var payload struct {
		Hint string `json:"hint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	ctx := context.Background()
	res := resolveTenantByHintValue(ctx, payload.Hint, "lookup")
	resp := map[string]interface{}{
		"match": false,
	}
	if res != nil && res.id != "" {
		resp["match"] = true
		resp["tenant_id"] = res.id
		if res.name != "" {
			resp["tenant_name"] = res.name
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleOIDCProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !authorizeOrReject(w, r, authz.ActionSSOProvidersRead, authz.ResourceRef{}) {
			return
		}
		ctx := context.Background()
		tenantID := strings.TrimSpace(r.URL.Query().Get("tenant"))
		providers, err := serverStore.ListOIDCProviders(ctx, tenantID)
		if err != nil {
			serverLogger.Error("Failed to list OIDC providers", "tenant_id", tenantID, "error", err)
			http.Error(w, "failed to list providers", http.StatusInternalServerError)
			return
		}
		list := make([]map[string]interface{}, 0, len(providers))
		for _, p := range providers {
			list = append(list, adminProviderDTO(p))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		if !authorizeOrReject(w, r, authz.ActionSSOProvidersWrite, authz.ResourceRef{}) {
			return
		}
		var payload oidcProviderPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		provider, err := buildProviderFromPayload(&payload, nil, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := context.Background()
		if err := serverStore.CreateOIDCProvider(ctx, provider); err != nil {
			serverLogger.Error("Failed to create OIDC provider", "slug", provider.Slug, "issuer", provider.Issuer, "error", err)
			http.Error(w, fmt.Sprintf("failed to create provider: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminProviderDTO(provider))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleOIDCProvider(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/sso/providers/")
	slug := strings.Trim(rest, "/")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	ctx := context.Background()
	existing, err := serverStore.GetOIDCProvider(ctx, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !authorizeOrReject(w, r, authz.ActionSSOProvidersRead, authz.ResourceRef{}) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminProviderDTO(existing))
	case http.MethodPut:
		if !authorizeOrReject(w, r, authz.ActionSSOProvidersWrite, authz.ResourceRef{}) {
			return
		}
		var payload oidcProviderPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		updated, err := buildProviderFromPayload(&payload, existing, slug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := serverStore.UpdateOIDCProvider(ctx, updated); err != nil {
			serverLogger.Error("Failed to update OIDC provider", "slug", slug, "error", err)
			http.Error(w, fmt.Sprintf("failed to update provider: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminProviderDTO(updated))
	case http.MethodDelete:
		if !authorizeOrReject(w, r, authz.ActionSSOProvidersWrite, authz.ResourceRef{}) {
			return
		}
		if err := serverStore.DeleteOIDCProvider(ctx, slug); err != nil {
			serverLogger.Error("Failed to delete OIDC provider", "slug", slug, "error", err)
			http.Error(w, fmt.Sprintf("failed to delete provider: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/auth/oidc/start/")
	slug := strings.Trim(rest, "/")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	ctx := context.Background()
	provider, err := serverStore.GetOIDCProvider(ctx, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	effectiveTenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if provider.TenantID != "" {
		if effectiveTenant == "" {
			effectiveTenant = provider.TenantID
		} else if effectiveTenant != provider.TenantID {
			serverLogger.Warn("OIDC tenant mismatch attempted", "slug", slug, "provider_tenant", provider.TenantID, "requested_tenant", effectiveTenant)
			http.Error(w, "tenant mismatch", http.StatusForbidden)
			return
		}
	}

	redirectURL := sanitizeRedirectTarget(r.URL.Query().Get("redirect"))
	if isAgentCallbackURL(r.URL.Query().Get("redirect")) {
		// Persist the explicit target in the existing state-bound redirect URL.
		var err error
		redirectURL, _, err = agentCallbackTarget(r.URL.Query().Get("redirect"), r.URL.Query().Get("agent_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	state, err := randomURLSafe(24)
	if err != nil {
		serverLogger.Error("Failed to generate OIDC state", "slug", slug, "error", err)
		http.Error(w, "failed to create state", http.StatusInternalServerError)
		return
	}
	nonce, err := randomURLSafe(24)
	if err != nil {
		serverLogger.Error("Failed to generate OIDC nonce", "slug", slug, "error", err)
		http.Error(w, "failed to create nonce", http.StatusInternalServerError)
		return
	}

	op, err := cachedOIDCProvider(ctx, provider.Issuer)
	if err != nil {
		serverLogger.Error("Failed to load OIDC issuer", "slug", slug, "issuer", provider.Issuer, "error", err)
		reportOIDCIssuerFailure(ctx, provider, err)
		http.Error(w, "failed to load issuer", http.StatusBadGateway)
		return
	}
	clearAdminAlert(ctx, oidcDiscoveryAlertKey(provider.Slug), provider.TenantID)

	oauthConfig := buildOAuthConfig(r, provider, op)

	sess := &storage.OIDCSession{
		ID:           state,
		ProviderSlug: provider.Slug,
		TenantID:     effectiveTenant,
		Nonce:        nonce,
		State:        state,
		RedirectURL:  redirectURL,
		CreatedAt:    time.Now().UTC(),
	}
	if err := serverStore.CreateOIDCSession(ctx, sess); err != nil {
		serverLogger.Error("Failed to persist OIDC session state", "slug", slug, "error", err)
		http.Error(w, "failed to persist state", http.StatusInternalServerError)
		return
	}

	authURL := oauthConfig.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		serverLogger.Warn("OIDC callback missing parameters", "has_state", state != "", "has_code", code != "", "error_param", r.URL.Query().Get("error"), "error_description", r.URL.Query().Get("error_description"))
		http.Redirect(w, r, "/login?error=oidc_invalid", http.StatusFound)
		return
	}

	ctx := context.Background()
	sess, err := serverStore.GetOIDCSession(ctx, state)
	if err != nil {
		serverLogger.Warn("OIDC session lookup failed", "state", state[:min(len(state), 16)]+"...", "error", err)
		http.Redirect(w, r, "/login?error=oidc_state", http.StatusFound)
		return
	}
	defer serverStore.DeleteOIDCSession(ctx, sess.ID)

	provider, err := serverStore.GetOIDCProvider(ctx, sess.ProviderSlug)
	if err != nil {
		serverLogger.Error("OIDC provider lookup failed", "slug", sess.ProviderSlug, "error", err)
		http.Redirect(w, r, "/login?error=oidc_provider", http.StatusFound)
		return
	}

	op, err := cachedOIDCProvider(ctx, provider.Issuer)
	if err != nil {
		serverLogger.Error("OIDC issuer discovery failed during callback", "slug", provider.Slug, "issuer", provider.Issuer, "error", err)
		reportOIDCIssuerFailure(ctx, provider, err)
		http.Redirect(w, r, "/login?error=oidc_discovery", http.StatusFound)
		return
	}
	clearAdminAlert(ctx, oidcDiscoveryAlertKey(provider.Slug), provider.TenantID)

	oauthConfig := buildOAuthConfig(r, provider, op)
	secretHint := maskSecret(provider.ClientSecret)
	serverLogger.Debug("OIDC token exchange starting", "slug", provider.Slug, "redirect_url", oauthConfig.RedirectURL, "token_endpoint", op.Endpoint().TokenURL, "client_id", provider.ClientID, "secret_hint", secretHint)
	token, err := oauthConfig.Exchange(ctx, code)
	if err != nil {
		serverLogger.Error("OIDC token exchange failed", "slug", provider.Slug, "issuer", provider.Issuer, "redirect_url", oauthConfig.RedirectURL, "client_id", provider.ClientID, "secret_hint", secretHint, "error", err)
		reportOIDCCredentialFailure(ctx, provider, err)
		http.Redirect(w, r, "/login?error=oidc_exchange", http.StatusFound)
		return
	}
	clearAdminAlert(ctx, oidcCredentialAlertKey(provider.Slug), provider.TenantID)

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		serverLogger.Error("OIDC response missing id_token", "slug", provider.Slug, "token_type", token.TokenType, "has_access_token", token.AccessToken != "")
		http.Redirect(w, r, "/login?error=oidc_token", http.StatusFound)
		return
	}

	verifier := op.Verifier(&oidclib.Config{ClientID: provider.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		serverLogger.Error("OIDC id_token verification failed", "slug", provider.Slug, "client_id", provider.ClientID, "error", err)
		http.Redirect(w, r, "/login?error=oidc_verify", http.StatusFound)
		return
	}
	if idToken.Nonce != sess.Nonce {
		serverLogger.Warn("OIDC nonce mismatch", "slug", provider.Slug, "expected", sess.Nonce[:min(len(sess.Nonce), 8)]+"...", "got", idToken.Nonce[:min(len(idToken.Nonce), 8)]+"...")
		http.Redirect(w, r, "/login?error=oidc_nonce", http.StatusFound)
		return
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		serverLogger.Error("OIDC claims parsing failed", "slug", provider.Slug, "subject", idToken.Subject, "error", err)
		http.Redirect(w, r, "/login?error=oidc_claims", http.StatusFound)
		return
	}
	claims.Subject = idToken.Subject

	user, err := resolveOIDCUser(ctx, provider, &claims)
	if err != nil {
		logError("OIDC user resolution failed", "error", err, "provider", provider.Slug)
		http.Redirect(w, r, "/login?error=oidc_user", http.StatusFound)
		return
	}

	if _, err := createSessionCookie(w, r, user.ID); err != nil {
		logError("OIDC session creation failed", "error", err)
		http.Redirect(w, r, "/login?error=oidc_session", http.StatusFound)
		return
	}
	rememberUserTenantHint(w, r, user)

	redirectURL := sess.RedirectURL
	if redirectURL == "" {
		redirectURL = "/"
	}

	// Check if this is an agent callback redirect
	if isAgentCallbackURL(redirectURL) {
		serverLogger.Debug("OIDC callback detected agent redirect", "user_id", user.ID, "callback_url", redirectURL)
		var err error
		redirectURL, err = issueOIDCAgentRedirect(ctx, user, redirectURL)
		if err != nil {
			http.Redirect(w, r, "/login?error=oidc_agent", http.StatusFound)
			return
		}
	}

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// oidcCredentialAlertKey returns the latch key for a provider's credential health.
func oidcCredentialAlertKey(slug string) string {
	return "oidc.credentials." + strings.ToLower(strings.TrimSpace(slug))
}

func oidcDiscoveryAlertKey(slug string) string {
	return "oidc.discovery." + strings.ToLower(strings.TrimSpace(slug))
}

func reportOIDCIssuerFailure(ctx context.Context, provider *storage.OIDCProvider, discoveryErr error) {
	if provider == nil || discoveryErr == nil {
		return
	}
	raiseAdminAlert(ctx, adminnotify.Event{
		Key:      oidcDiscoveryAlertKey(provider.Slug),
		Severity: storage.AlertSeverityWarning,
		Title:    fmt.Sprintf("SSO identity provider unavailable: %q", providerLabel(provider)),
		Message:  fmt.Sprintf("PrintMaster could not load the OIDC discovery document from %s. Users may be unable to start or complete single sign-on until the identity provider is reachable and its issuer URL is valid.", provider.Issuer),
		Details:  discoveryErr.Error(),
		TenantID: provider.TenantID,
	})
}

// reportOIDCCredentialFailure raises a latched admin alert when an OIDC token
// exchange fails, calling out expired client secrets explicitly since they
// silently break every login for the provider.
func reportOIDCCredentialFailure(ctx context.Context, provider *storage.OIDCProvider, exchangeErr error) {
	if provider == nil || exchangeErr == nil {
		return
	}
	detail := exchangeErr.Error()
	lower := strings.ToLower(detail)

	severity := storage.AlertSeverityWarning
	title := fmt.Sprintf("SSO login failing for %q", providerLabel(provider))
	message := fmt.Sprintf("PrintMaster could not exchange the authorization code with %s. Users cannot sign in with this provider until it is fixed.", provider.Issuer)

	switch {
	case strings.Contains(lower, "expired") && strings.Contains(lower, "secret"),
		strings.Contains(lower, "aadsts7000222"):
		severity = storage.AlertSeverityCritical
		title = fmt.Sprintf("SSO client secret expired for %q", providerLabel(provider))
		message = fmt.Sprintf("The client secret for %q (client_id %s) has expired, so every single sign-on attempt is failing. Issue a new secret at the identity provider and update it in PrintMaster under Settings > Authentication.", providerLabel(provider), provider.ClientID)
	case strings.Contains(lower, "invalid_client"):
		severity = storage.AlertSeverityCritical
		title = fmt.Sprintf("SSO client credentials rejected for %q", providerLabel(provider))
		message = fmt.Sprintf("The identity provider rejected the client credentials for %q (client_id %s). Verify the client ID and secret in PrintMaster under Settings > Authentication.", providerLabel(provider), provider.ClientID)
	}

	raiseAdminAlert(ctx, adminnotify.Event{
		Key:      oidcCredentialAlertKey(provider.Slug),
		Severity: severity,
		Title:    title,
		Message:  message,
		Details:  detail,
		TenantID: provider.TenantID,
	})
}

func providerLabel(provider *storage.OIDCProvider) string {
	if name := strings.TrimSpace(provider.DisplayName); name != "" {
		return name
	}
	return provider.Slug
}

// isAgentCallbackURL checks if a URL is an agent authentication callback
func isAgentCallbackURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Path == "/api/v1/auth/callback"
}

// issueOIDCAgentRedirect uses only the target persisted at login start, never
// callback request parameters. Recheck current ownership before issuing a grant.
func issueOIDCAgentRedirect(ctx context.Context, user *storage.User, redirect string) (string, error) {
	callback, agentID, err := agentCallbackTarget(redirect, "")
	if err != nil {
		return "", err
	}
	agent, err := serverStore.GetAgent(ctx, agentID)
	if err != nil || agent == nil || !newPrincipal(user).CanAccessTenant(agent.TenantID) {
		return "", errors.New("callback target inaccessible")
	}
	act := generateAgentCallbackToken(user, agentID, callback)
	if act == nil {
		return "", errors.New("failed to issue callback grant")
	}
	u, _ := url.Parse(callback)
	q := u.Query()
	q.Set("token", act.Token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type tenantResolution struct {
	id     string
	name   string
	source string
}

func resolveTenantForAuthRequest(ctx context.Context, r *http.Request) *tenantResolution {
	if serverStore == nil || r == nil {
		return nil
	}
	if value := strings.TrimSpace(r.URL.Query().Get("tenant")); value != "" {
		res := &tenantResolution{id: value, source: "query"}
		res.ensureName(ctx)
		return res
	}
	for _, candidate := range []struct {
		value  string
		source string
	}{
		{r.URL.Query().Get("domain_hint"), "domain_hint"},
		{r.URL.Query().Get("login_hint"), "login_hint"},
	} {
		if res := resolveTenantByHintValue(ctx, candidate.value, candidate.source); res != nil {
			return res
		}
	}
	for _, hostCandidate := range []struct {
		value  string
		source string
	}{
		{r.Host, "host"},
		{firstForwardedHost(r.Header.Get("X-Forwarded-Host")), "forwarded_host"},
	} {
		if res := resolveTenantByHintValue(ctx, hostCandidate.value, hostCandidate.source); res != nil {
			return res
		}
	}
	if c, err := r.Cookie(tenantHintCookieName); err == nil {
		if val := strings.TrimSpace(c.Value); val != "" {
			res := &tenantResolution{id: val, source: "cookie"}
			res.ensureName(ctx)
			return res
		}
	}
	return nil
}

func resolveTenantByHintValue(ctx context.Context, raw, source string) *tenantResolution {
	if serverStore == nil {
		return nil
	}
	norm := storage.NormalizeTenantDomain(raw)
	if norm == "" {
		return nil
	}
	tenant, err := serverStore.FindTenantByDomain(ctx, norm)
	if err != nil || tenant == nil {
		return nil
	}
	return &tenantResolution{id: tenant.ID, name: tenant.Name, source: source}
}

func firstForwardedHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, ","); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

func (tr *tenantResolution) ensureName(ctx context.Context) {
	if tr == nil || tr.name != "" || tr.id == "" || serverStore == nil {
		return
	}
	tenant, err := serverStore.GetTenant(ctx, tr.id)
	if err == nil && tenant != nil {
		tr.name = tenant.Name
	}
}

func adminProviderDTO(p *storage.OIDCProvider) map[string]interface{} {
	return map[string]interface{}{
		"slug":         p.Slug,
		"display_name": p.DisplayName,
		"issuer":       p.Issuer,
		"client_id":    p.ClientID,
		"has_secret":   p.ClientSecret != "",
		"scopes":       p.Scopes,
		"icon":         p.Icon,
		"button_text":  p.ButtonText,
		"button_style": p.ButtonStyle,
		"auto_login":   p.AutoLogin,
		"tenant_id":    p.TenantID,
		"default_role": p.DefaultRole,
		"created_at":   p.CreatedAt,
		"updated_at":   p.UpdatedAt,
	}
}

func publicProviderDTO(p *storage.OIDCProvider) map[string]interface{} {
	return map[string]interface{}{
		"slug":         p.Slug,
		"display_name": p.DisplayName,
		"button_text":  firstNonEmpty(p.ButtonText, p.DisplayName),
		"button_style": p.ButtonStyle,
		"icon":         p.Icon,
		"auto_login":   p.AutoLogin,
		"tenant_id":    p.TenantID,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func buildProviderFromPayload(payload *oidcProviderPayload, existing *storage.OIDCProvider, forcedSlug string) (*storage.OIDCProvider, error) {
	if payload == nil {
		return nil, fmt.Errorf("payload required")
	}

	result := &storage.OIDCProvider{}
	if existing != nil {
		*result = *existing
	}

	slug := forcedSlug
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(payload.Slug))
	}
	if slug == "" {
		slug = slugify(payload.DisplayName)
	}
	slug = strings.TrimSpace(slug)
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("slug must be 3-64 characters (lowercase letters, numbers, hyphen)")
	}
	result.Slug = slug

	display := strings.TrimSpace(payload.DisplayName)
	if display == "" {
		return nil, fmt.Errorf("display_name required")
	}
	result.DisplayName = display

	issuer := strings.TrimSpace(payload.Issuer)
	if existing == nil && issuer == "" {
		return nil, fmt.Errorf("issuer required")
	}
	if issuer != "" {
		result.Issuer = issuer
	}

	clientID := strings.TrimSpace(payload.ClientID)
	if existing == nil && clientID == "" {
		return nil, fmt.Errorf("client_id required")
	}
	if clientID != "" {
		result.ClientID = clientID
	}

	secret := strings.TrimSpace(payload.ClientSecret)
	if secret != "" {
		result.ClientSecret = secret
	} else if existing == nil {
		return nil, fmt.Errorf("client_secret required")
	}

	result.Scopes = normalizeScopes(payload.Scopes)
	result.Icon = strings.TrimSpace(payload.Icon)
	result.ButtonStyle = strings.TrimSpace(payload.ButtonStyle)
	result.ButtonText = firstNonEmpty(strings.TrimSpace(payload.ButtonText), result.DisplayName)
	result.AutoLogin = payload.AutoLogin
	result.TenantID = strings.TrimSpace(payload.TenantID)

	role := strings.ToLower(strings.TrimSpace(payload.DefaultRole))
	switch role {
	case "admin", "operator", "viewer", "user", "":
		// Legacy user intentionally normalizes to operator; explicit/omitted
		// viewer defaults never acquire write permissions.
	default:
		return nil, fmt.Errorf("invalid default_role")
	}
	result.DefaultRole = storage.NormalizeRole(role)

	return result, nil
}

func slugify(input string) string {
	s := strings.ToLower(strings.TrimSpace(input))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "provider"
	}
	if len(s) < 3 {
		s = fmt.Sprintf("%s-%s", s, "oidc")
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func normalizeScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"openid", "profile", "email"}
	}
	set := map[string]struct{}{}
	for _, scope := range scopes {
		trimmed := strings.TrimSpace(scope)
		if trimmed == "" {
			continue
		}
		set[trimmed] = struct{}{}
	}
	set["openid"] = struct{}{}
	ordered := make([]string, 0, len(set))
	if _, ok := set["openid"]; ok {
		ordered = append(ordered, "openid")
		delete(set, "openid")
	}
	for _, candidate := range []string{"profile", "email"} {
		if _, ok := set[candidate]; ok {
			ordered = append(ordered, candidate)
			delete(set, candidate)
		}
	}
	for scope := range set {
		ordered = append(ordered, scope)
	}
	return ordered
}

func cachedOIDCProvider(ctx context.Context, issuer string) (*oidclib.Provider, error) {
	oidcCache.mu.Lock()
	if provider, ok := oidcCache.providers[issuer]; ok {
		oidcCache.mu.Unlock()
		return provider, nil
	}
	oidcCache.mu.Unlock()

	provider, err := oidclib.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	oidcCache.mu.Lock()
	oidcCache.providers[issuer] = provider
	oidcCache.mu.Unlock()
	return provider, nil
}

func buildOAuthConfig(r *http.Request, provider *storage.OIDCProvider, op *oidclib.Provider) *oauth2.Config {
	redirect := buildExternalURL(r) + "/auth/oidc/callback"
	return &oauth2.Config{
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
		RedirectURL:  redirect,
		Endpoint:     op.Endpoint(),
		Scopes:       provider.Scopes,
	}
}

func buildExternalURL(r *http.Request) string {
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func sanitizeRedirectTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || (!u.IsAbs() && u.Host != "") || strings.ContainsAny(raw, "\\\r\n\t") {
		return "/"
	}
	if u.IsAbs() {
		if !isAgentCallbackURL(raw) {
			return "/"
		}
		if _, _, err := agentCallbackTarget(raw, ""); err != nil {
			return "/"
		}
		return u.String()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// maskSecret returns a hint about a secret for logging (length + first/last 2 chars)
func maskSecret(s string) string {
	if s == "" {
		return "(empty)"
	}
	n := len(s)
	if n <= 4 {
		return fmt.Sprintf("len=%d", n)
	}
	return fmt.Sprintf("len=%d [%s...%s]", n, s[:2], s[n-2:])
}

func resolveOIDCUser(ctx context.Context, provider *storage.OIDCProvider, claims *oidcClaims) (*storage.User, error) {
	if provider == nil || claims == nil || strings.TrimSpace(claims.Subject) == "" {
		return nil, errors.New("OIDC provider and subject required")
	}
	link, err := serverStore.GetOIDCLink(ctx, provider.Slug, claims.Subject)
	if err == nil {
		user, err := serverStore.GetUserByID(ctx, link.UserID)
		if err == nil {
			return user, nil
		}
		// If user was deleted but link still exists, fall through to create new user
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// Clean up orphaned link
		_ = serverStore.DeleteOIDCLink(ctx, provider.Slug, claims.Subject)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email != "" {
		_, err := serverStore.GetUserByEmail(ctx, email)
		if err == nil {
			// Email, even verified, is not authority to attach a new subject to
			// an existing account. Preserve explicitly prelinked identities above.
			return nil, errors.New("existing email requires explicit OIDC identity linking")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	// Unverified email is not a usable account ownership attribute.
	if !claims.EmailVerified {
		email = ""
	}

	username := deriveUsername(claims)
	role := provider.DefaultRole
	if provider.TenantID != "" && role == storage.RoleAdmin {
		return nil, errors.New("tenant OIDC provider cannot provision a global administrator")
	}
	if role != storage.RoleAdmin && role != storage.RoleOperator {
		role = storage.RoleViewer
	}

	var user *storage.User
	for i := 0; i < 5; i++ {
		candidate := username
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", username, i)
		}
		candidate = strings.Trim(candidate, "-")
		if candidate == "" {
			candidate = fmt.Sprintf("%s-user", provider.Slug)
		}
		tempUser := &storage.User{
			Username:  candidate,
			Role:      role,
			TenantIDs: []string{},
			Email:     email,
		}
		if provider.TenantID != "" {
			tempUser.TenantIDs = []string{provider.TenantID}
			tempUser.TenantID = provider.TenantID
		}
		pass, err := randomURLSafe(18)
		if err != nil {
			return nil, err
		}
		if err := serverStore.CreateUser(ctx, tempUser, pass); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				continue
			}
			return nil, err
		}
		user = tempUser
		break
	}
	if user == nil {
		return nil, fmt.Errorf("failed to create unique username")
	}

	if err := serverStore.CreateOIDCLink(ctx, &storage.OIDCLink{ProviderSlug: provider.Slug, Subject: claims.Subject, Email: email, UserID: user.ID}); err != nil {
		return nil, err
	}

	logInfo("OIDC auto-provisioned user", "username", user.Username, "provider", provider.Slug, "tenant_id", user.TenantID)

	return user, nil
}

func deriveUsername(claims *oidcClaims) string {
	if claims == nil {
		return "user"
	}
	if claims.PreferredUsername != "" {
		return normalizeUsername(claims.PreferredUsername)
	}
	if claims.Email != "" {
		parts := strings.Split(claims.Email, "@")
		return normalizeUsername(parts[0])
	}
	if claims.Name != "" {
		return normalizeUsername(strings.ReplaceAll(claims.Name, " ", "."))
	}
	return "user"
}

func normalizeUsername(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(raw, "")
	if raw == "" {
		return "user"
	}
	if len(raw) > 64 {
		raw = raw[:64]
	}
	return raw
}
