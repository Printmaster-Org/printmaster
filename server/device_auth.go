package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

const (
	deviceAuthRequestTTL       = 10 * time.Minute
	deviceAuthJoinTokenTTL     = 15 // minutes
	deviceAuthCleanupInterval  = 1 * time.Minute
	deviceAuthCodeLength       = 6
	deviceAuthPollTokenEntropy = 32
)

type deviceAuthStatus string

const (
	deviceAuthStatusPending  deviceAuthStatus = "pending"
	deviceAuthStatusApproved deviceAuthStatus = "approved"
	deviceAuthStatusRejected deviceAuthStatus = "rejected"
	deviceAuthStatusExpired  deviceAuthStatus = "expired"
)

type deviceAuthMetadata struct {
	AgentID      string `json:"agent_id"`
	AgentName    string `json:"agent_name"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	Platform     string `json:"platform"`
	ClientIP     string `json:"client_ip"`
}

type deviceAuthRequest struct {
	Code      string
	PollToken string
	Status    deviceAuthStatus

	Metadata     deviceAuthMetadata
	AssignedName string

	RequestedAt time.Time
	ExpiresAt   time.Time

	TenantID   string
	TenantName string

	ApprovedAt time.Time
	ApprovedBy string
	JoinToken  string

	RejectedAt time.Time
	RejectedBy string

	Message string
}

type deviceAuthStore struct {
	mu     sync.Mutex
	byCode map[string]*deviceAuthRequest
	byPoll map[string]*deviceAuthRequest
}

func newDeviceAuthStore() *deviceAuthStore {
	return &deviceAuthStore{
		byCode: make(map[string]*deviceAuthRequest),
		byPoll: make(map[string]*deviceAuthRequest),
	}
}

var (
	deviceAuthStoreMu sync.Mutex
	deviceAuthInst    *deviceAuthStore
)

func ensureDeviceAuthStore() *deviceAuthStore {
	deviceAuthStoreMu.Lock()
	defer deviceAuthStoreMu.Unlock()
	if deviceAuthInst == nil {
		deviceAuthInst = newDeviceAuthStore()
		go deviceAuthInst.cleanupLoop()
	}
	return deviceAuthInst
}

func (s *deviceAuthStore) cleanupLoop() {
	ticker := time.NewTicker(deviceAuthCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupExpired()
	}
}

func (s *deviceAuthStore) cleanupExpired() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, req := range s.byCode {
		if now.After(req.ExpiresAt) {
			delete(s.byCode, key)
			delete(s.byPoll, req.PollToken)
		}
	}
}

func (s *deviceAuthStore) Create(meta deviceAuthMetadata) *deviceAuthRequest {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	req := &deviceAuthRequest{
		Code:         generateDeviceAuthCode(),
		PollToken:    generateDeviceAuthPollToken(),
		Status:       deviceAuthStatusPending,
		Metadata:     meta,
		AssignedName: strings.TrimSpace(meta.AgentName),
		RequestedAt:  now,
		ExpiresAt:    now.Add(deviceAuthRequestTTL),
	}
	s.mu.Lock()
	s.byCode[strings.ToUpper(req.Code)] = req
	s.byPoll[req.PollToken] = req
	clone := *req
	s.mu.Unlock()
	return &clone
}

func (s *deviceAuthStore) snapshot(code string) (*deviceAuthRequest, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.byCode[strings.ToUpper(code)]
	if !ok {
		return nil, false
	}
	if req.Status != deviceAuthStatusExpired && time.Now().UTC().After(req.ExpiresAt) {
		req.Status = deviceAuthStatusExpired
		req.Message = "Request expired"
		delete(s.byCode, strings.ToUpper(code))
		delete(s.byPoll, req.PollToken)
		return nil, false
	}
	clone := *req
	return &clone, true
}

func (s *deviceAuthStore) getByPoll(pollToken string) (*deviceAuthRequest, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.byPoll[pollToken]
	if !ok {
		return nil, false
	}
	if req.Status != deviceAuthStatusExpired && time.Now().UTC().After(req.ExpiresAt) {
		req.Status = deviceAuthStatusExpired
		req.Message = "Request expired"
		delete(s.byCode, strings.ToUpper(req.Code))
		delete(s.byPoll, pollToken)
	}
	clone := *req
	return &clone, ok
}

func (s *deviceAuthStore) approve(code, authorizedTenantID, tenantID, tenantName, agentName, approver string, issue func() (string, error)) (*deviceAuthRequest, error) {
	if s == nil {
		return nil, errors.New("store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.byCode[strings.ToUpper(code)]
	if !ok {
		return nil, errors.New("request not found")
	}
	if time.Now().UTC().After(req.ExpiresAt) {
		req.Status = deviceAuthStatusExpired
		req.Message = "Request expired"
		delete(s.byCode, strings.ToUpper(code))
		delete(s.byPoll, req.PollToken)
		return nil, errors.New("request expired")
	}
	if req.Status != deviceAuthStatusPending || req.TenantID != authorizedTenantID {
		return nil, errors.New("request changed or already completed")
	}
	if req.TenantID != "" && req.TenantID != tenantID {
		return nil, errors.New("request assigned to another tenant")
	}
	// Serialize token issuance with approval/rejection/expiry. Failure leaves
	// the request pending and retryable; losing reviewers never mint a token.
	joinToken, err := issue()
	if err != nil {
		return nil, err
	}
	req.Status = deviceAuthStatusApproved
	req.TenantID = tenantID
	req.TenantName = tenantName
	if strings.TrimSpace(agentName) != "" {
		req.AssignedName = strings.TrimSpace(agentName)
	}
	req.ApprovedAt = time.Now().UTC()
	req.ApprovedBy = approver
	req.JoinToken = joinToken
	req.Message = "Approved"
	clone := *req
	return &clone, nil
}

func (s *deviceAuthStore) reject(code, reason, actor string, authorizedTenantID string) (*deviceAuthRequest, error) {
	if s == nil {
		return nil, errors.New("store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.byCode[strings.ToUpper(code)]
	if !ok {
		return nil, errors.New("request not found")
	}
	if time.Now().UTC().After(req.ExpiresAt) {
		req.Status = deviceAuthStatusExpired
		req.Message = "Request expired"
		delete(s.byCode, strings.ToUpper(code))
		delete(s.byPoll, req.PollToken)
		return nil, errors.New("request expired")
	}
	// Authorization used a snapshot; never mutate a newly assigned or terminal
	// request after that decision (approval and rejection may race).
	if req.TenantID != authorizedTenantID || req.Status != deviceAuthStatusPending {
		return nil, errors.New("request changed or already completed")
	}
	req.Status = deviceAuthStatusRejected
	req.Message = reason
	req.RejectedAt = time.Now().UTC()
	req.RejectedBy = actor
	clone := *req
	return &clone, nil
}

func generateDeviceAuthCode() string {
	alphabet := []rune("23456789ABCDEFGHJKLMNPQRSTUVWXYZ")
	var b strings.Builder
	b.Grow(deviceAuthCodeLength)
	for i := 0; i < deviceAuthCodeLength; i++ {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b.WriteRune(alphabet[idx.Int64()])
	}
	return b.String()
}

func generateDeviceAuthPollToken() string {
	buf := make([]byte, deviceAuthPollTokenEntropy)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("tok_%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func handleAgentDeviceAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if serverStore == nil {
		http.Error(w, "server not ready", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		AgentID      string `json:"agent_id"`
		AgentName    string `json:"agent_name"`
		AgentVersion string `json:"agent_version"`
		Hostname     string `json:"hostname"`
		Platform     string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.AgentID) == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return
	}
	meta := deviceAuthMetadata{
		AgentID:      strings.TrimSpace(in.AgentID),
		AgentName:    strings.TrimSpace(in.AgentName),
		AgentVersion: strings.TrimSpace(in.AgentVersion),
		Hostname:     strings.TrimSpace(in.Hostname),
		Platform:     strings.TrimSpace(in.Platform),
		ClientIP:     extractClientIP(r),
	}
	req := ensureDeviceAuthStore().Create(meta)
	authorizeURL := fmt.Sprintf("%s/device-auth/%s", buildExternalURL(r), url.PathEscape(strings.ToUpper(req.Code)))
	resp := map[string]interface{}{
		"success":       true,
		"code":          req.Code,
		"poll_token":    req.PollToken,
		"expires_at":    req.ExpiresAt.Format(time.RFC3339),
		"authorize_url": authorizeURL,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAgentDeviceAuthPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		PollToken string `json:"poll_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.PollToken) == "" {
		http.Error(w, "poll_token required", http.StatusBadRequest)
		return
	}
	req, ok := ensureDeviceAuthStore().getByPoll(strings.TrimSpace(in.PollToken))
	if !ok || req == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp := map[string]interface{}{
		"success": true,
		"status":  req.Status,
		"code":    req.Code,
		"message": req.Message,
	}
	if req.Status == deviceAuthStatusApproved {
		resp["join_token"] = req.JoinToken
		resp["tenant_id"] = req.TenantID
		resp["agent_name"] = req.AssignedName
	}
	if req.Status == deviceAuthStatusRejected {
		resp["reason"] = req.Message
	}
	if req.Status == deviceAuthStatusExpired {
		resp["reason"] = "expired"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleDeviceAuthRequestRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/device-auth/requests/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	code := strings.ToUpper(parts[0])
	store := ensureDeviceAuthStore()
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		handleDeviceAuthRequestGet(w, r, store, code)
	case len(parts) == 2 && parts[1] == "approve" && r.Method == http.MethodPost:
		handleDeviceAuthApprove(w, r, store, code)
	case len(parts) == 2 && parts[1] == "reject" && r.Method == http.MethodPost:
		handleDeviceAuthReject(w, r, store, code)
	default:
		http.NotFound(w, r)
	}
}

func handleDeviceAuthRequestGet(w http.ResponseWriter, r *http.Request, store *deviceAuthStore, code string) {
	if getPrincipal(r) == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	snapshot, ok := store.snapshot(code)
	if !ok || snapshot == nil {
		http.NotFound(w, r)
		return
	}
	if !authorizeDeviceAuthRequest(w, r, snapshot) {
		return
	}
	tenants, err := listAccessibleTenants(r)
	if err != nil {
		http.Error(w, "failed to list tenants", http.StatusInternalServerError)
		return
	}
	resp := map[string]interface{}{
		"code":         snapshot.Code,
		"status":       snapshot.Status,
		"message":      snapshot.Message,
		"requested_at": snapshot.RequestedAt.Format(time.RFC3339),
		"expires_at":   snapshot.ExpiresAt.Format(time.RFC3339),
		"agent": map[string]string{
			"id":        snapshot.Metadata.AgentID,
			"name":      snapshot.Metadata.AgentName,
			"version":   snapshot.Metadata.AgentVersion,
			"hostname":  snapshot.Metadata.Hostname,
			"platform":  snapshot.Metadata.Platform,
			"client_ip": snapshot.Metadata.ClientIP,
		},
		"tenants": tenants,
	}
	if snapshot.AssignedName != "" {
		resp["assigned_name"] = snapshot.AssignedName
	}
	if !snapshot.ApprovedAt.IsZero() {
		resp["approved_at"] = snapshot.ApprovedAt.Format(time.RFC3339)
		resp["approved_by"] = snapshot.ApprovedBy
	}
	if snapshot.TenantID != "" {
		resp["tenant_id"] = snapshot.TenantID
		resp["tenant_name"] = snapshot.TenantName
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type tenantOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Unassigned pending requests are shared only through possession of their
// code. A tenant-scoped operator must have at least one writable tenant;
// assigned requests always use stored ownership, never a supplied tenant.
func authorizeDeviceAuthRequest(w http.ResponseWriter, r *http.Request, req *deviceAuthRequest) bool {
	principal := getPrincipal(r)
	if principal == nil || principal.User == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return false
	}
	if req.TenantID != "" {
		return authorizeOrReject(w, r, authz.ActionAgentsWrite, authz.ResourceRef{TenantIDs: []string{req.TenantID}})
	}
	if !authorizeOrReject(w, r, authz.ActionAgentsWrite, authz.ResourceRef{}) {
		return false
	}
	if principal.IsAdmin() {
		return true
	}
	tenants, err := listAccessibleTenants(r)
	if err != nil {
		http.Error(w, "failed to list tenants", http.StatusInternalServerError)
		return false
	}
	if req.Status != deviceAuthStatusPending || len(tenants) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func listAccessibleTenants(r *http.Request) ([]tenantOption, error) {
	principal := getPrincipal(r)
	if principal == nil || principal.User == nil {
		return nil, authz.ErrUnauthorized
	}
	if serverStore == nil {
		return nil, errors.New("store unavailable")
	}
	list, err := serverStore.ListTenants(r.Context())
	if err != nil {
		return nil, err
	}
	allowed := make([]tenantOption, 0, len(list))
	for _, t := range list {
		if !principal.CanAccessTenant(t.ID) {
			continue
		}
		allowed = append(allowed, tenantOption{ID: t.ID, Name: t.Name})
	}
	return allowed, nil
}

func handleDeviceAuthApprove(w http.ResponseWriter, r *http.Request, store *deviceAuthStore, code string) {
	principal := getPrincipal(r)
	if principal == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	snapshot, ok := store.snapshot(code)
	if !ok || snapshot == nil {
		http.NotFound(w, r)
		return
	}
	if !authorizeDeviceAuthRequest(w, r, snapshot) {
		return
	}
	var in struct {
		TenantID  string `json:"tenant_id"`
		AgentName string `json:"agent_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.TenantID) == "" {
		http.Error(w, "tenant_id required", http.StatusBadRequest)
		return
	}
	if snapshot.Status != deviceAuthStatusPending || (snapshot.TenantID != "" && snapshot.TenantID != strings.TrimSpace(in.TenantID)) {
		http.Error(w, "request completed or assigned to another tenant", http.StatusBadRequest)
		return
	}
	if !authorizeOrReject(w, r, authz.ActionAgentsWrite, authz.ResourceRef{TenantIDs: []string{strings.TrimSpace(in.TenantID)}}) {
		return
	}
	ctx := r.Context()
	var tenantName string
	if serverStore != nil {
		if tenant, err := serverStore.GetTenant(ctx, strings.TrimSpace(in.TenantID)); err == nil && tenant != nil {
			tenantName = tenant.Name
		}
	}
	if tenantName == "" {
		tenantName = strings.TrimSpace(in.TenantID)
	}
	issuanceFailed := false
	req, err := store.approve(code, snapshot.TenantID, strings.TrimSpace(in.TenantID), tenantName, in.AgentName, principal.User.Username, func() (string, error) {
		_, rawToken, err := serverStore.CreateJoinToken(ctx, strings.TrimSpace(in.TenantID), deviceAuthJoinTokenTTL, true)
		issuanceFailed = err != nil
		return rawToken, err
	})
	if err != nil {
		if issuanceFailed {
			http.Error(w, "failed to create join token", http.StatusInternalServerError)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logRequestAudit(r, &storage.AuditEntry{
		Action:     "device_auth.approve",
		TargetType: "agent_pending",
		TargetID:   req.Metadata.AgentID,
		TenantID:   req.TenantID,
		Details:    fmt.Sprintf("Approved agent %s via device auth", req.Metadata.AgentID),
		Metadata: map[string]interface{}{
			"code":      req.Code,
			"tenant_id": req.TenantID,
		},
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"status":  req.Status,
	})
}

func handleDeviceAuthReject(w http.ResponseWriter, r *http.Request, store *deviceAuthStore, code string) {
	principal := getPrincipal(r)
	if principal == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	snapshot, ok := store.snapshot(code)
	if !ok || snapshot == nil {
		http.NotFound(w, r)
		return
	}
	if !authorizeDeviceAuthRequest(w, r, snapshot) {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "Rejected by operator"
	}
	req, err := store.reject(code, reason, principal.User.Username, snapshot.TenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logRequestAudit(r, &storage.AuditEntry{
		Action:     "device_auth.reject",
		TargetType: "agent_pending",
		TargetID:   req.Metadata.AgentID,
		TenantID:   req.TenantID,
		Details:    fmt.Sprintf("Rejected agent %s via device auth", req.Metadata.AgentID),
		Metadata: map[string]interface{}{
			"code": req.Code,
		},
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"status":  req.Status,
		"message": req.Message,
	})
}

func handleDeviceAuthPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := ensureInteractiveSession(w, r); !ok {
		return
	}
	code := strings.TrimPrefix(r.URL.Path, "/device-auth/")
	code = strings.Trim(code, "/")
	if code == "" {
		http.NotFound(w, r)
		return
	}
	tmpl, err := template.ParseFS(webFS, "web/device_auth.html")
	if err != nil {
		logError("Failed to parse device auth template", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	data := map[string]string{"Code": strings.ToUpper(code)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		logError("Failed to render device auth template", "error", err)
	}
}
