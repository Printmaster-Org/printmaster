package releases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"printmaster/common/updatepolicy"
	authz "printmaster/server/authz"
	"printmaster/server/storage"
)

// APIOptions provide infrastructure concerns for HTTP handlers.
type APIOptions struct {
	AuthMiddleware func(http.HandlerFunc) http.HandlerFunc
	Authorizer     func(*http.Request, authz.Action, authz.ResourceRef) error
}

type manifestService interface {
	ListSigningKeys(ctx context.Context, limit int) ([]*storage.SigningKey, error)
	RotateSigningKey(ctx context.Context, notes string) (*storage.SigningKey, error)
	RegenerateManifests(ctx context.Context) (int, error)
	ListManifests(ctx context.Context, component string, limit int) ([]*storage.ReleaseManifest, error)
	GetManifest(ctx context.Context, component, version, platform, arch, format string) (*storage.ReleaseManifest, error)
}

// API exposes administrative release/signing endpoints.
type API struct {
	manager    manifestService
	authWrap   func(http.HandlerFunc) http.HandlerFunc
	authorizer func(*http.Request, authz.Action, authz.ResourceRef) error
}

// NewAPI builds a release API instance bound to the provided manager.
func NewAPI(manager manifestService, opts APIOptions) *API {
	if manager == nil {
		panic("release API requires a manager")
	}
	return &API{
		manager:    manager,
		authWrap:   opts.AuthMiddleware,
		authorizer: opts.Authorizer,
	}
}

// RegisterRoutes wires HTTP handlers into the mux.
func (api *API) RegisterRoutes(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	wrap := api.wrap
	mux.HandleFunc("/api/v1/releases/signing-keys", wrap(api.handleSigningKeys))
	mux.HandleFunc("/api/v1/releases/signing-keys/rotate", wrap(api.handleRotateSigningKey))
	mux.HandleFunc("/api/v1/releases/manifests", wrap(api.handleManifests))
	mux.HandleFunc("/api/v1/releases/manifests/regenerate", wrap(api.handleRegenerateManifests))
}

func (api *API) wrap(handler http.HandlerFunc) http.HandlerFunc {
	if api.authWrap == nil {
		return handler
	}
	return api.authWrap(handler)
}

func (api *API) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) bool {
	if api.authorizer == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return false
	}
	if err := api.authorizer(r, action, authz.ResourceRef{}); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, authz.ErrUnauthorized) {
			status = http.StatusUnauthorized
		}
		http.Error(w, http.StatusText(status), status)
		return false
	}
	return true
}

func (api *API) handleSigningKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionReleasesRead) {
		return
	}
	keys, err := api.manager.ListSigningKeys(r.Context(), 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list signing keys")
		return
	}
	resp := make([]signingKeyResponse, 0, len(keys))
	for _, key := range keys {
		resp = append(resp, signingKeyResponseFrom(key))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": resp})
}

func (api *API) handleRotateSigningKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionReleasesWrite) {
		return
	}
	var payload rotateKeyRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := api.manager.RotateSigningKey(r.Context(), payload.Notes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate signing key")
		return
	}
	regenerated := 0
	if payload.RegenerateManifests {
		regenerated, err = api.manager.RegenerateManifests(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to regenerate manifests")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"key":               signingKeyResponseFrom(key),
		"regenerated_count": regenerated,
	})
}

func (api *API) handleManifests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionReleasesRead) {
		return
	}
	q := r.URL.Query()
	component := strings.TrimSpace(q.Get("component"))
	version := strings.TrimSpace(q.Get("version"))
	platform := strings.TrimSpace(q.Get("platform"))
	arch := strings.TrimSpace(q.Get("arch"))
	// format selects binary (default) or msi when both exist for a platform.
	format, ok := updatepolicy.ParseArtifactFormat(q.Get("format"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported artifact format")
		return
	}

	if component != "" && version != "" && platform != "" && arch != "" {
		manifest, err := api.manager.GetManifest(r.Context(), component, version, platform, arch, format.String())
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeError(w, status, "manifest not found")
			return
		}
		writeJSON(w, http.StatusOK, manifestResponseFrom(manifest))
		return
	}

	limit := 20
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	manifests, err := api.manager.ListManifests(r.Context(), component, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list manifests")
		return
	}
	resp := make([]manifestResponse, 0, len(manifests))
	for _, manifest := range manifests {
		resp = append(resp, manifestResponseFrom(manifest))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"manifests": resp})
}

func (api *API) handleRegenerateManifests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !api.authorize(w, r, authz.ActionReleasesWrite) {
		return
	}
	count, err := api.manager.RegenerateManifests(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to regenerate manifests")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"regenerated_count": count})
}

type rotateKeyRequest struct {
	Notes               string `json:"notes"`
	RegenerateManifests bool   `json:"regenerate_manifests"`
}

type signingKeyResponse struct {
	ID        string     `json:"id"`
	Algorithm string     `json:"algorithm"`
	PublicKey string     `json:"public_key"`
	Active    bool       `json:"active"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	Notes     string     `json:"notes,omitempty"`
}

type manifestResponse struct {
	Component    string          `json:"component"`
	Version      string          `json:"version"`
	Platform     string          `json:"platform"`
	Arch         string          `json:"arch"`
	Format       string          `json:"format"`
	Channel      string          `json:"channel"`
	Manifest     json.RawMessage `json:"manifest"`
	Signature    string          `json:"signature"`
	SigningKeyID string          `json:"signing_key_id"`
	GeneratedAt  time.Time       `json:"generated_at"`
}

func signingKeyResponseFrom(key *storage.SigningKey) signingKeyResponse {
	resp := signingKeyResponse{
		ID:        key.ID,
		Algorithm: key.Algorithm,
		PublicKey: key.PublicKey,
		Active:    key.Active,
		CreatedAt: key.CreatedAt,
		Notes:     key.Notes,
	}
	if !key.RotatedAt.IsZero() {
		ts := key.RotatedAt.UTC()
		resp.RotatedAt = &ts
	}
	return resp
}

func manifestResponseFrom(manifest *storage.ReleaseManifest) manifestResponse {
	payload := json.RawMessage(manifest.ManifestJSON)
	return manifestResponse{
		Component:    manifest.Component,
		Version:      manifest.Version,
		Platform:     manifest.Platform,
		Arch:         manifest.Arch,
		Format:       manifest.Format,
		Channel:      manifest.Channel,
		Manifest:     payload,
		Signature:    manifest.Signature,
		SigningKeyID: manifest.SigningKeyID,
		GeneratedAt:  manifest.GeneratedAt,
	}
}

func decodeJSON(r *http.Request, v interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
