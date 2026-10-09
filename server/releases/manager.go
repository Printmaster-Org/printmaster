package releases

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"printmaster/common/logger"
	"printmaster/common/updatepolicy"
	"printmaster/server/storage"

	"github.com/Masterminds/semver"
)

const defaultManifestVersion = "1.0"

// ManifestPayload describes the signed manifest structure distributed to agents.
type ManifestPayload struct {
	ManifestVersion string `json:"manifest_version"`
	Component       string `json:"component"`
	Version         string `json:"version"`
	MinorLine       string `json:"minor_line"`
	Platform        string `json:"platform"`
	Arch            string `json:"arch"`
	// Format is the artifact packaging (binary or msi). It is part of the
	// signed payload so a binary manifest cannot be replayed for an MSI.
	Format      string    `json:"format"`
	Channel     string    `json:"channel"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
	SourceURL   string    `json:"source_url"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

// ManagerOptions tweak manifest manager behavior.
type ManagerOptions struct {
	ManifestVersion string
	Now             func() time.Time
}

// Manager owns manifest signing and signing-key lifecycle.
type Manager struct {
	store           storage.Store
	log             *logger.Logger
	manifestVersion string
	now             func() time.Time
	mu              sync.Mutex
}

// NewManager constructs a release manifest manager.
func NewManager(store storage.Store, log *logger.Logger, opts ManagerOptions) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("storage store is required")
	}
	version := strings.TrimSpace(opts.ManifestVersion)
	if version == "" {
		version = defaultManifestVersion
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &Manager{
		store:           store,
		log:             log,
		manifestVersion: version,
		now:             nowFn,
	}, nil
}

// EnsureActiveKey makes sure there is an active signing key, creating one if missing.
func (m *Manager) EnsureActiveKey(ctx context.Context) (*storage.SigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key, err := m.store.GetActiveSigningKey(ctx)
	if err == nil && key != nil {
		return key, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key, err = m.generateAndActivateKey(ctx, "bootstrap")
	if err != nil {
		return nil, err
	}
	return key, nil
}

// RotateSigningKey generates a new signing key and marks it active.
func (m *Manager) RotateSigningKey(ctx context.Context, notes string) (*storage.SigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, err := m.generateAndActivateKey(ctx, notes)
	if err != nil {
		return nil, err
	}
	m.logInfo("rotated signing key", "key", key.ID)
	sanitized := cloneSigningKey(key, true)
	return sanitized, nil
}

// EnsureManifestForArtifact signs the artifact manifest if it is missing or stale.
func (m *Manager) EnsureManifestForArtifact(ctx context.Context, artifact *storage.ReleaseArtifact) (*storage.ReleaseManifest, error) {
	if artifact == nil {
		return nil, fmt.Errorf("artifact required")
	}
	if artifact.SHA256 == "" {
		return nil, fmt.Errorf("artifact %s/%s missing sha256", artifact.Component, artifact.Version)
	}
	key, err := m.EnsureActiveKey(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := m.store.GetReleaseManifest(ctx, artifact.Component, artifact.Version, artifact.Platform, artifact.Arch, artifact.Format)
	if err == nil && existing != nil && existing.SigningKeyID == key.ID && existing.Signature != "" && existing.Channel == artifact.Channel {
		return existing, nil
	}
	return m.signArtifactWithKey(ctx, artifact, key)
}

// RegenerateManifests re-signs all cached artifacts using the active key.
func (m *Manager) RegenerateManifests(ctx context.Context) (int, error) {
	key, err := m.EnsureActiveKey(ctx)
	if err != nil {
		return 0, err
	}
	manifests, err := m.store.ListReleaseManifests(ctx, "", 0)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	for _, manifest := range manifests {
		artifact, convErr := artifactFromManifest(manifest)
		if convErr != nil {
			return count, convErr
		}
		if _, err := m.signArtifactWithKey(ctx, artifact, key); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// ListSigningKeys returns sanitized signing key metadata.
func (m *Manager) ListSigningKeys(ctx context.Context, limit int) ([]*storage.SigningKey, error) {
	keys, err := m.store.ListSigningKeys(ctx, limit)
	if err != nil {
		return nil, err
	}
	sanitized := make([]*storage.SigningKey, 0, len(keys))
	for _, key := range keys {
		sanitized = append(sanitized, cloneSigningKey(key, true))
	}
	return sanitized, nil
}

// ListManifests returns manifest envelopes stored on the server.
func (m *Manager) ListManifests(ctx context.Context, component string, limit int) ([]*storage.ReleaseManifest, error) {
	return m.store.ListReleaseManifests(ctx, component, limit)
}

// GetManifest loads a manifest for the given tuple. An empty format means
// binary.
func (m *Manager) GetManifest(ctx context.Context, component, version, platform, arch, format string) (*storage.ReleaseManifest, error) {
	return m.store.GetReleaseManifest(ctx, component, version, platform, arch, format)
}

// AgentUpdateManifest is the JSON structure returned to agents for update checks.
type AgentUpdateManifest struct {
	ManifestVersion string    `json:"manifest_version"`
	Component       string    `json:"component"`
	Version         string    `json:"version"`
	MinorLine       string    `json:"minor_line"`
	Platform        string    `json:"platform"`
	Arch            string    `json:"arch"`
	Format          string    `json:"format"`
	Channel         string    `json:"channel"`
	SHA256          string    `json:"sha256"`
	SizeBytes       int64     `json:"size_bytes"`
	SourceURL       string    `json:"source_url"`
	DownloadURL     string    `json:"download_url,omitempty"`
	PublishedAt     time.Time `json:"published_at,omitempty"`
	GeneratedAt     time.Time `json:"generated_at"`
	Signature       string    `json:"signature,omitempty"`
}

// GetLatestManifest returns the newest manifest for the component, platform,
// arch, channel, and artifact format. An empty channel matches every channel;
// an empty format means binary, which is what Agents that predate formats
// expect.
func (m *Manager) GetLatestManifest(ctx context.Context, component, platform, arch, channel, format string) (*AgentUpdateManifest, error) {
	format = normalizedFormat(format)
	// Get all manifests for this component and find the latest matching one
	manifests, err := m.store.ListReleaseManifests(ctx, component, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to list manifests: %w", err)
	}

	m.logDebug("searching for latest manifest",
		"component", component,
		"platform", platform,
		"arch", arch,
		"channel", channel,
		"format", format,
		"total_manifests", len(manifests))

	var latest *storage.ReleaseManifest
	var latestVersion *semver.Version
	matchCount := 0
	for _, manifest := range manifests {
		if manifest.Platform != platform || manifest.Arch != arch || normalizedFormat(manifest.Format) != format {
			continue
		}
		if channel != "" && (manifest.Channel != channel || channelFromVersion(manifest.Version) != channel) {
			continue
		}
		matchCount++
		candidateVersion := parseSemverVersion(manifest.Version)
		m.logDebug("manifest candidate",
			"version", manifest.Version,
			"parsed", candidateVersion != nil,
			"current_latest", func() string {
				if latest != nil {
					return latest.Version
				}
				return "<none>"
			}())

		if latest == nil {
			latest = manifest
			latestVersion = candidateVersion
			continue
		}
		if candidateVersion != nil {
			if latestVersion == nil || candidateVersion.GreaterThan(latestVersion) {
				latest = manifest
				latestVersion = candidateVersion
			}
			continue
		}
		if latestVersion == nil && manifest.Version > latest.Version {
			latest = manifest
		}
	}

	if latest == nil {
		m.logDebug("no matching manifest found", "matched_count", matchCount)
		return nil, fmt.Errorf("no manifest found for %s/%s-%s/%s (%s)", component, platform, arch, channel, format)
	}

	m.logDebug("selected latest manifest", "version", latest.Version, "matched_count", matchCount)

	// Get the corresponding artifact for size info
	artifact, err := m.store.GetReleaseArtifact(ctx, latest.Component, latest.Version, latest.Platform, latest.Arch, format)
	if err != nil {
		m.logDebug("No artifact found for manifest", "component", latest.Component, "version", latest.Version, "format", format, "error", err)
	}

	// Parse minor line from version (e.g., "0.9.16" -> "0.9")
	minorLine := ""
	parts := strings.Split(latest.Version, ".")
	if len(parts) >= 2 {
		minorLine = parts[0] + "." + parts[1]
	}

	result := &AgentUpdateManifest{
		ManifestVersion: latest.ManifestVersion,
		Component:       latest.Component,
		Version:         latest.Version,
		MinorLine:       minorLine,
		Platform:        latest.Platform,
		Arch:            latest.Arch,
		Format:          format,
		Channel:         latest.Channel,
		Signature:       latest.Signature,
		GeneratedAt:     latest.GeneratedAt,
	}

	if artifact != nil {
		result.SHA256 = artifact.SHA256
		result.SizeBytes = artifact.SizeBytes
		result.SourceURL = artifact.SourceURL
	}

	return result, nil
}

func (m *Manager) signArtifactWithKey(ctx context.Context, artifact *storage.ReleaseArtifact, key *storage.SigningKey) (*storage.ReleaseManifest, error) {
	payload, err := m.buildPayload(artifact)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	priv, err := base64.StdEncoding.DecodeString(key.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid signing key material: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("unexpected private key length")
	}
	signature := ed25519.Sign(ed25519.PrivateKey(priv), raw)
	record := &storage.ReleaseManifest{
		Component:       artifact.Component,
		Version:         artifact.Version,
		Platform:        artifact.Platform,
		Arch:            artifact.Arch,
		Format:          payload.Format,
		Channel:         defaultString(artifact.Channel, "stable"),
		ManifestVersion: payload.ManifestVersion,
		ManifestJSON:    string(raw),
		Signature:       base64.StdEncoding.EncodeToString(signature),
		SigningKeyID:    key.ID,
		GeneratedAt:     payload.GeneratedAt,
	}
	if err := m.store.UpsertReleaseManifest(ctx, record); err != nil {
		return nil, err
	}
	signed, err := m.store.GetReleaseManifest(ctx, artifact.Component, artifact.Version, artifact.Platform, artifact.Arch, payload.Format)
	if err == nil {
		m.logDebug("signed release manifest", "component", artifact.Component, "version", artifact.Version, "platform", artifact.Platform, "arch", artifact.Arch, "format", payload.Format)
	}
	return signed, err
}

func (m *Manager) buildPayload(artifact *storage.ReleaseArtifact) (ManifestPayload, error) {
	if artifact.Component == "" || artifact.Version == "" || artifact.Platform == "" || artifact.Arch == "" {
		return ManifestPayload{}, fmt.Errorf("artifact identity incomplete")
	}
	payload := ManifestPayload{
		ManifestVersion: m.manifestVersion,
		Component:       artifact.Component,
		Version:         artifact.Version,
		MinorLine:       computeMinorLine(artifact.Version),
		Platform:        artifact.Platform,
		Arch:            artifact.Arch,
		Format:          normalizedFormat(artifact.Format),
		Channel:         defaultString(artifact.Channel, "stable"),
		SHA256:          artifact.SHA256,
		SizeBytes:       artifact.SizeBytes,
		SourceURL:       artifact.SourceURL,
		GeneratedAt:     m.now(),
	}
	if artifact.PublishedAt.After(time.Time{}) {
		payload.PublishedAt = artifact.PublishedAt.UTC()
	}
	if payload.SHA256 == "" {
		return ManifestPayload{}, fmt.Errorf("artifact sha256 required for %s %s", artifact.Component, artifact.Version)
	}
	if payload.SourceURL == "" {
		return ManifestPayload{}, fmt.Errorf("artifact source url required for %s %s", artifact.Component, artifact.Version)
	}
	return payload, nil
}

func (m *Manager) generateAndActivateKey(ctx context.Context, notes string) (*storage.SigningKey, error) {
	now := m.now()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	id, err := generateSigningKeyID(now)
	if err != nil {
		return nil, err
	}
	record := &storage.SigningKey{
		ID:         id,
		Algorithm:  "ed25519",
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
		PrivateKey: base64.StdEncoding.EncodeToString(priv),
		Notes:      strings.TrimSpace(notes),
		CreatedAt:  now,
	}
	if err := m.store.CreateSigningKey(ctx, record); err != nil {
		return nil, err
	}
	if err := m.store.SetSigningKeyActive(ctx, record.ID); err != nil {
		return nil, err
	}
	return m.store.GetSigningKey(ctx, record.ID)
}

func generateSigningKeyID(now time.Time) (string, error) {
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("sig_%d_%s", now.Unix(), hex.EncodeToString(entropy[:])), nil
}

func computeMinorLine(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "0.0"
	}
	parts := strings.SplitN(version, "-", 2)
	core := parts[0]
	segments := strings.Split(core, ".")
	if len(segments) >= 2 {
		return fmt.Sprintf("%s.%s", segments[0], segments[1])
	}
	if len(segments) == 1 {
		return fmt.Sprintf("%s.0", segments[0])
	}
	return "0.0"
}

func cloneSigningKey(key *storage.SigningKey, scrubPrivate bool) *storage.SigningKey {
	if key == nil {
		return nil
	}
	clone := *key
	if scrubPrivate {
		clone.PrivateKey = ""
	}
	return &clone
}

func defaultString(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func parseSemverVersion(raw string) *semver.Version {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if trimmed == "" {
		return nil
	}
	ver, err := semver.NewVersion(trimmed)
	if err != nil {
		return nil
	}
	return ver
}

func artifactFromManifest(manifest *storage.ReleaseManifest) (*storage.ReleaseArtifact, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest required")
	}
	if manifest.ManifestJSON == "" {
		return nil, fmt.Errorf("manifest payload missing for %s/%s", manifest.Component, manifest.Version)
	}
	var payload ManifestPayload
	if err := json.Unmarshal([]byte(manifest.ManifestJSON), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse manifest json: %w", err)
	}
	artifact := &storage.ReleaseArtifact{
		Component: manifest.Component,
		Version:   manifest.Version,
		Platform:  manifest.Platform,
		Arch:      manifest.Arch,
		// The row's format is authoritative: payloads signed before formats
		// existed have no format, and migrations may have reclassified the row.
		Format:      normalizedFormat(manifest.Format),
		Channel:     defaultString(payload.Channel, manifest.Channel),
		SourceURL:   payload.SourceURL,
		SHA256:      payload.SHA256,
		SizeBytes:   payload.SizeBytes,
		PublishedAt: payload.PublishedAt,
	}
	if artifact.SourceURL == "" {
		return nil, fmt.Errorf("manifest missing source url for %s/%s", artifact.Component, artifact.Version)
	}
	if artifact.SHA256 == "" {
		return nil, fmt.Errorf("manifest missing sha256 for %s/%s", artifact.Component, artifact.Version)
	}
	return artifact, nil
}

// normalizedFormat returns the canonical artifact format, treating empty as
// binary. Unknown values pass through unchanged so storage validation can
// reject them with a precise error instead of silently becoming binary.
func normalizedFormat(raw string) string {
	if format, ok := updatepolicy.ParseArtifactFormat(raw); ok {
		return format.String()
	}
	return strings.TrimSpace(raw)
}

func (m *Manager) logInfo(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Info(msg, args...)
	}
}

func (m *Manager) logDebug(msg string, args ...interface{}) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
