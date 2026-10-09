package releases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"printmaster/common/logger"
	"printmaster/common/updatepolicy"
	"printmaster/server/storage"
)

const (
	defaultRepoOwner    = "printmaster-org"
	defaultRepoName     = "printmaster"
	defaultPollInterval = 4 * time.Hour
	defaultMaxReleases  = 6
)

var safeSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SyncProgress represents the current state of a release sync operation.
type SyncProgress struct {
	Phase           string `json:"phase"`             // "fetching", "processing", "downloading", "complete", "error"
	Message         string `json:"message"`           // Human-readable status
	TotalFiles      int    `json:"total_files"`       // Total number of files to download
	CompletedFiles  int    `json:"completed_files"`   // Number of files downloaded
	TotalBytes      int64  `json:"total_bytes"`       // Total bytes to download
	CompletedBytes  int64  `json:"completed_bytes"`   // Bytes downloaded so far
	CurrentFile     string `json:"current_file"`      // Name of file currently being downloaded
	CurrentFileSize int64  `json:"current_file_size"` // Size of current file
	PercentComplete int    `json:"percent_complete"`  // 0-100 overall progress
	Error           string `json:"error,omitempty"`   // Error message if phase is "error"
}

// ProgressCallback is called during sync to report progress.
type ProgressCallback func(progress SyncProgress)

// Options control IntakeWorker behavior.
type Options struct {
	CacheDir          string
	PollInterval      time.Duration
	RepoOwner         string
	RepoName          string
	BaseAPIURL        string
	GitHubToken       string
	HTTPClient        *http.Client
	MaxReleases       int
	RetentionVersions int  // 0 = disabled (keep all), N = keep N versions per component
	IncludePrerelease bool // If true, include prerelease/dev builds in synced releases
	UserAgent         string
	ManifestManager   *Manager
}

type IntakeWorker struct {
	store             storage.Store
	log               *logger.Logger
	cacheDir          string
	pollInterval      time.Duration
	repoOwner         string
	repoName          string
	baseAPIURL        string
	client            *http.Client
	maxReleases       int
	retentionVersions int
	includePrerelease bool
	token             string
	userAgent         string
	manifests         *Manager
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string    `json:"name"`
	BrowserDownloadURL string    `json:"browser_download_url"`
	Size               int64     `json:"size"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type artifactDescriptor struct {
	component string
	version   string
	platform  string
	arch      string
	// format distinguishes assets that share a platform/arch, such as the
	// Windows .exe and .msi, so each gets its own cache entry and manifest.
	format   updatepolicy.ArtifactFormat
	fileName string
}

// NewIntakeWorker wires release intake with sane defaults.
func NewIntakeWorker(store storage.Store, log *logger.Logger, opts Options) (*IntakeWorker, error) {
	if store == nil {
		return nil, fmt.Errorf("store cannot be nil")
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "printmaster", "release-cache")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}

	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}

	repoOwner := opts.RepoOwner
	if repoOwner == "" {
		repoOwner = defaultRepoOwner
	}
	repoName := opts.RepoName
	if repoName == "" {
		repoName = defaultRepoName
	}

	baseAPI := strings.TrimRight(opts.BaseAPIURL, "/")
	if baseAPI == "" {
		baseAPI = "https://api.github.com"
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	maxReleases := opts.MaxReleases
	if maxReleases <= 0 {
		maxReleases = defaultMaxReleases
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = "printmaster-release-intake"
	}

	return &IntakeWorker{
		store:             store,
		log:               log,
		cacheDir:          cacheDir,
		pollInterval:      poll,
		repoOwner:         repoOwner,
		repoName:          repoName,
		baseAPIURL:        baseAPI,
		client:            client,
		maxReleases:       maxReleases,
		retentionVersions: opts.RetentionVersions,
		includePrerelease: opts.IncludePrerelease,
		token:             strings.TrimSpace(opts.GitHubToken),
		userAgent:         userAgent,
		manifests:         opts.ManifestManager,
	}, nil
}

// Run starts the periodic release intake loop.
func (w *IntakeWorker) Run(ctx context.Context) {
	w.logInfo("release intake worker started", "cache_dir", w.cacheDir, "interval", w.pollInterval.String())
	if err := w.RunOnce(ctx); err != nil {
		w.logWarn("initial release intake failed", "error", err)
	}

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logInfo("release intake worker stopping")
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil {
				w.logWarn("release intake iteration failed", "error", err)
			}
		}
	}
}

// RunOnce executes a single fetch cycle without progress reporting. The
// periodic loop and tests use it; the UI-triggered sync uses
// RunOnceWithProgress. Both share one implementation.
func (w *IntakeWorker) RunOnce(ctx context.Context) error {
	return w.runOnceWithProgress(ctx, nil)
}

// RunOnceWithProgress executes a single fetch cycle with progress reporting.
func (w *IntakeWorker) RunOnceWithProgress(ctx context.Context, onProgress ProgressCallback) error {
	return w.runOnceWithProgress(ctx, onProgress)
}

// artifactPlan represents a single release asset selected for the cache.
type artifactPlan struct {
	desc      artifactDescriptor
	rel       ghRelease
	asset     ghAsset
	cached    *storage.ReleaseArtifact // existing verified cache entry, if any
	needsWork bool                     // true when the asset must be downloaded
}

func (w *IntakeWorker) runOnceWithProgress(ctx context.Context, onProgress ProgressCallback) error {
	report := func(p SyncProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	// Phase 1: Fetch release metadata
	report(SyncProgress{
		Phase:   "fetching",
		Message: "Fetching release list from GitHub...",
	})

	releases, err := w.fetchReleases(ctx)
	if err != nil {
		report(SyncProgress{
			Phase:   "error",
			Message: "Failed to fetch releases",
			Error:   err.Error(),
		})
		return err
	}

	// Phase 2: Plan work - determine which artifacts need downloading
	report(SyncProgress{
		Phase:   "processing",
		Message: fmt.Sprintf("Analyzing %d releases...", len(releases)),
	})

	plans := w.planArtifacts(ctx, releases)

	var filesToDownload int
	var bytesToDownload int64
	for _, p := range plans {
		if p.needsWork {
			filesToDownload++
			bytesToDownload += p.asset.Size
		}
	}

	report(SyncProgress{
		Phase:      "processing",
		Message:    fmt.Sprintf("Found %d artifacts (%d need downloading)", len(plans), filesToDownload),
		TotalFiles: filesToDownload,
		TotalBytes: bytesToDownload,
	})

	// Phase 3: Refresh cached artifacts and download missing ones
	var completedFiles int
	var completedBytes int64

	for _, p := range plans {
		if !p.needsWork {
			w.refreshCachedArtifact(ctx, p)
			continue
		}

		pct := 0
		if bytesToDownload > 0 {
			pct = int((completedBytes * 100) / bytesToDownload)
		}
		report(SyncProgress{
			Phase:           "downloading",
			Message:         fmt.Sprintf("Downloading %s...", p.asset.Name),
			TotalFiles:      filesToDownload,
			CompletedFiles:  completedFiles,
			TotalBytes:      bytesToDownload,
			CompletedBytes:  completedBytes,
			CurrentFile:     p.asset.Name,
			CurrentFileSize: p.asset.Size,
			PercentComplete: pct,
		})

		var onDownload func(int64)
		if onProgress != nil {
			onDownload = func(downloaded int64) {
				pct := 0
				if bytesToDownload > 0 {
					pct = int(((completedBytes + downloaded) * 100) / bytesToDownload)
				}
				report(SyncProgress{
					Phase:           "downloading",
					Message:         fmt.Sprintf("Downloading %s... (%d%%)", p.asset.Name, pct),
					TotalFiles:      filesToDownload,
					CompletedFiles:  completedFiles,
					TotalBytes:      bytesToDownload,
					CompletedBytes:  completedBytes + downloaded,
					CurrentFile:     p.asset.Name,
					CurrentFileSize: p.asset.Size,
					PercentComplete: pct,
				})
			}
		}

		if err := w.cacheArtifact(ctx, p, onDownload); err != nil {
			w.logWarn("release artifact caching failed",
				"asset", p.asset.Name, "component", p.desc.component, "version", p.desc.version, "format", p.desc.format, "error", err)
			continue
		}
		completedFiles++
		completedBytes += p.asset.Size
	}

	w.pruneIfConfigured(ctx)

	message := fmt.Sprintf("Sync complete: %d files downloaded", completedFiles)
	if filesToDownload == 0 {
		message = "All artifacts up to date"
	}
	report(SyncProgress{
		Phase:           "complete",
		Message:         message,
		TotalFiles:      filesToDownload,
		CompletedFiles:  completedFiles,
		TotalBytes:      bytesToDownload,
		CompletedBytes:  completedBytes,
		PercentComplete: 100,
	})
	w.logInfo("Release intake complete", "artifacts", len(plans), "downloaded", completedFiles, "pending_failed", filesToDownload-completedFiles)
	return nil
}

// planArtifacts selects the release assets the fleet update cache should hold
// and records whether each is already cached. Every skip is logged with its
// reason so operators can see why an asset is not offered to Agents.
func (w *IntakeWorker) planArtifacts(ctx context.Context, releases []ghRelease) []artifactPlan {
	w.logInfo("Processing releases from GitHub", "count", len(releases), "include_prerelease", w.includePrerelease)

	var plans []artifactPlan
	processed := map[string]int{}
	for _, rel := range releases {
		component, version := parseTag(rel.TagName)
		switch {
		case component == "" || version == "":
			w.logDebug("Skipping release with unparseable tag", "tag", rel.TagName)
			continue
		case rel.Draft:
			w.logDebug("Skipping draft release", "tag", rel.TagName)
			continue
		case (rel.Prerelease || channelFromVersion(version) != "stable") && !w.includePrerelease:
			w.logDebug("Skipping prerelease (include_prerelease disabled)", "tag", rel.TagName)
			continue
		case processed[component] >= w.maxReleases:
			continue
		}
		processed[component]++

		for _, asset := range rel.Assets {
			if asset.BrowserDownloadURL == "" {
				w.logDebug("Skipping asset without download URL", "asset", asset.Name)
				continue
			}
			desc, ok := buildDescriptor(component, version, asset.Name)
			if !ok {
				w.logDebug("Skipping asset that does not match release naming", "asset", asset.Name)
				continue
			}
			if !desc.format.FleetDeliverable() {
				w.logDebug("Skipping OS package; package repositories deliver it",
					"asset", asset.Name, "format", desc.format)
				continue
			}

			plan := artifactPlan{desc: desc, rel: rel, asset: asset, needsWork: true}
			existing, err := w.store.GetReleaseArtifact(ctx, desc.component, desc.version, desc.platform, desc.arch, desc.format.String())
			switch {
			case err == nil && existing != nil && fileExists(existing.CachePath) && existing.SHA256 != "":
				plan.cached = existing
				plan.needsWork = false
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				w.logWarn("Release artifact lookup failed; will re-download",
					"asset", asset.Name, "format", desc.format, "error", err)
			}
			plans = append(plans, plan)
		}
	}
	w.logInfo("Release planning complete", "releases_per_component", processed, "artifacts", len(plans))
	return plans
}

// refreshCachedArtifact keeps metadata for an already-downloaded artifact in
// sync with GitHub (notes, URL, size, channel) and ensures its manifest.
func (w *IntakeWorker) refreshCachedArtifact(ctx context.Context, p artifactPlan) {
	existing := p.cached
	channel := channelFromVersion(p.desc.version)
	if existing.SourceURL != p.asset.BrowserDownloadURL || existing.ReleaseNotes != p.rel.Body ||
		existing.SizeBytes != p.asset.Size || existing.Channel != channel {
		existing.Channel = channel
		existing.SourceURL = p.asset.BrowserDownloadURL
		existing.ReleaseNotes = p.rel.Body
		existing.SizeBytes = p.asset.Size
		existing.PublishedAt = p.rel.PublishedAt
		if err := w.store.UpsertReleaseArtifact(ctx, existing); err != nil {
			w.logWarn("release artifact metadata refresh failed",
				"component", existing.Component, "version", existing.Version, "format", existing.Format, "error", err)
			return
		}
	}
	w.ensureManifest(ctx, existing)
}

// cacheArtifact downloads an asset, records it, and signs its manifest.
func (w *IntakeWorker) cacheArtifact(ctx context.Context, p artifactPlan, onProgress func(int64)) error {
	cachePath, sha, size, err := w.downloadArtifactWithProgress(ctx, p.desc, p.asset.BrowserDownloadURL, p.asset.Size, onProgress)
	if err != nil {
		return err
	}
	record := &storage.ReleaseArtifact{
		Component:    p.desc.component,
		Version:      p.desc.version,
		Platform:     p.desc.platform,
		Arch:         p.desc.arch,
		Format:       p.desc.format.String(),
		Channel:      channelFromVersion(p.desc.version),
		SourceURL:    p.asset.BrowserDownloadURL,
		CachePath:    cachePath,
		SHA256:       sha,
		SizeBytes:    size,
		ReleaseNotes: p.rel.Body,
		PublishedAt:  p.rel.PublishedAt,
		DownloadedAt: time.Now().UTC(),
	}
	if err := w.store.UpsertReleaseArtifact(ctx, record); err != nil {
		return fmt.Errorf("save artifact: %w", err)
	}
	w.logInfo("cached release artifact", "component", record.Component, "version", record.Version,
		"platform", record.Platform, "arch", record.Arch, "format", record.Format, "bytes", size)
	w.ensureManifest(ctx, record)
	return nil
}

func (w *IntakeWorker) pruneIfConfigured(ctx context.Context) {
	if w.retentionVersions > 0 {
		for _, comp := range []string{"agent", "server"} {
			if err := w.pruneOldArtifacts(ctx, comp); err != nil {
				w.logWarn("artifact pruning failed", "component", comp, "error", err)
			}
		}
	}
}

// downloadArtifactWithProgress downloads an artifact into the cache, hashing it
// as it streams. onProgress may be nil.
func (w *IntakeWorker) downloadArtifactWithProgress(ctx context.Context, desc artifactDescriptor, downloadURL string, expectedSize int64, onProgress func(downloaded int64)) (string, string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("User-Agent", w.userAgent)
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("download failed: %s", resp.Status)
	}

	componentDir, err := buildCacheDir(w.cacheDir, desc)
	if err != nil {
		return "", "", 0, err
	}
	if err := os.MkdirAll(componentDir, 0o755); err != nil {
		return "", "", 0, err
	}

	tempFile, err := os.CreateTemp(componentDir, "download-*.tmp")
	if err != nil {
		return "", "", 0, err
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tempFile, hasher)

	// Use a progress-tracking reader
	var written int64
	buf := make([]byte, 32*1024) // 32KB buffer
	lastReport := time.Now()

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := writer.Write(buf[:n])
			if writeErr != nil {
				return "", "", 0, writeErr
			}
			written += int64(n)

			// Report progress at most every 100ms to avoid flooding
			if onProgress != nil && time.Since(lastReport) > 100*time.Millisecond {
				onProgress(written)
				lastReport = time.Now()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return "", "", 0, readErr
		}
	}

	// Final progress report
	if onProgress != nil {
		onProgress(written)
	}

	if err := tempFile.Sync(); err != nil {
		return "", "", 0, err
	}
	if err := tempFile.Close(); err != nil {
		return "", "", 0, err
	}

	finalPath := filepath.Join(componentDir, desc.fileName)
	_ = os.Remove(finalPath)
	if err := os.Rename(tempFile.Name(), finalPath); err != nil {
		return "", "", 0, err
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))
	return finalPath, checksum, written, nil
}

func (w *IntakeWorker) fetchReleases(ctx context.Context) ([]ghRelease, error) {
	perPage := w.maxReleases * 3
	if perPage < 10 {
		perPage = 10
	}
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d", w.baseAPIURL, w.repoOwner, w.repoName, perPage)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", w.userAgent)
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("github api error: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

func (w *IntakeWorker) ensureManifest(ctx context.Context, artifact *storage.ReleaseArtifact) {
	if artifact == nil {
		return
	}
	// Repair beta manifests cached as stable by older intake implementations.
	if channel := channelFromVersion(artifact.Version); artifact.Channel != channel {
		artifact.Channel = channel
		if err := w.store.UpsertReleaseArtifact(ctx, artifact); err != nil {
			w.logWarn("release channel repair failed", "version", artifact.Version, "error", err)
			return
		}
	}
	if w.manifests == nil {
		return
	}
	if _, err := w.manifests.EnsureManifestForArtifact(ctx, artifact); err != nil {
		w.logWarn("manifest generation failed", "component", artifact.Component, "version", artifact.Version, "platform", artifact.Platform, "arch", artifact.Arch, "error", err)
	}
}

func buildCacheDir(root string, desc artifactDescriptor) (string, error) {
	for _, part := range []string{desc.component, "v" + desc.version, desc.platform + "-" + desc.arch} {
		if !safeSegmentPattern.MatchString(part) {
			return "", fmt.Errorf("unsafe path segment: %s", part)
		}
	}
	return filepath.Join(root, desc.component, "v"+desc.version, desc.platform+"-"+desc.arch), nil
}

func parseTag(tag string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(tag), "-", 2)
	if len(parts) != 2 {
		return "", ""
	}
	component := strings.ToLower(parts[0])
	version := strings.TrimPrefix(parts[1], "v")
	if version == "" {
		return "", ""
	}
	if component != "agent" && component != "server" {
		return "", ""
	}
	return component, version
}

// buildDescriptor identifies the platform, architecture, and artifact format of
// a release asset. It only parses names; deciding whether the fleet update
// cache should hold the asset is the caller's job (see FleetDeliverable).
func buildDescriptor(component, version, assetName string) (artifactDescriptor, bool) {
	desc, ok := parseAssetName(component, version, assetName)
	if !ok {
		return artifactDescriptor{}, false
	}
	desc.format = updatepolicy.ArtifactFormatFromFilename(assetName)
	return desc, true
}

// parseAssetName matches the three release naming schemes (raw binary/MSI,
// Debian package, RPM package).
func parseAssetName(component, version, assetName string) (artifactDescriptor, bool) {
	// Primary pattern: printmaster-{component}-v{version}-{platform}-{arch}[.ext]
	// e.g., printmaster-agent-v0.29.1-linux-amd64
	prefix := fmt.Sprintf("printmaster-%s-v%s-", component, version)
	if strings.HasPrefix(assetName, prefix) {
		remainder := strings.TrimPrefix(assetName, prefix)
		core := remainder
		if idx := strings.Index(core, "."); idx != -1 {
			core = core[:idx]
		}
		parts := strings.Split(core, "-")
		if len(parts) >= 2 {
			platform := strings.ToLower(parts[0])
			arch := strings.ToLower(parts[1])
			if safeSegmentPattern.MatchString(platform) && safeSegmentPattern.MatchString(arch) {
				return artifactDescriptor{
					component: component,
					version:   version,
					platform:  platform,
					arch:      arch,
					fileName:  assetName,
				}, true
			}
		}
	}

	// Debian package pattern: printmaster-{component}_{version}_{arch}.deb
	// e.g., printmaster-agent_0.29.1_amd64.deb
	debPrefix := fmt.Sprintf("printmaster-%s_%s_", component, version)
	if strings.HasPrefix(assetName, debPrefix) && strings.HasSuffix(assetName, ".deb") {
		remainder := strings.TrimPrefix(assetName, debPrefix)
		arch := strings.TrimSuffix(remainder, ".deb")
		if safeSegmentPattern.MatchString(arch) {
			return artifactDescriptor{
				component: component,
				version:   version,
				platform:  "linux",
				arch:      arch,
				fileName:  assetName,
			}, true
		}
	}

	// RPM package pattern: printmaster-{component}-{version}-{release}.{dist}.{arch}.rpm
	// e.g., printmaster-agent-0.29.1-1.fc43.x86_64.rpm
	rpmPrefix := fmt.Sprintf("printmaster-%s-%s-", component, version)
	if strings.HasPrefix(assetName, rpmPrefix) && strings.HasSuffix(assetName, ".rpm") {
		remainder := strings.TrimPrefix(assetName, rpmPrefix)
		remainder = strings.TrimSuffix(remainder, ".rpm")
		// Format: {release}.{dist}.{arch}
		parts := strings.Split(remainder, ".")
		if len(parts) >= 3 {
			arch := parts[len(parts)-1]
			// Normalize RPM arch names to standard
			if arch == "x86_64" {
				arch = "amd64"
			} else if arch == "aarch64" {
				arch = "arm64"
			}
			if safeSegmentPattern.MatchString(arch) {
				return artifactDescriptor{
					component: component,
					version:   version,
					platform:  "linux",
					arch:      arch,
					fileName:  assetName,
				}, true
			}
		}
	}

	return artifactDescriptor{}, false
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// pruneOldArtifacts removes artifacts for versions beyond retention threshold
func (w *IntakeWorker) pruneOldArtifacts(ctx context.Context, component string) error {
	artifacts, err := w.store.ListArtifactsForPruning(ctx, component, w.retentionVersions)
	if err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return nil
	}

	var deletedCount int
	var freedBytes int64
	for _, art := range artifacts {
		// Remove cached file from disk
		if art.CachePath != "" {
			if rmErr := os.Remove(art.CachePath); rmErr != nil && !os.IsNotExist(rmErr) {
				w.logWarn("failed to remove cached artifact file", "path", art.CachePath, "error", rmErr)
			}
		}
		// Remove from database
		if err := w.store.DeleteReleaseArtifact(ctx, art.ID); err != nil {
			w.logWarn("failed to delete artifact record", "id", art.ID, "component", art.Component, "version", art.Version, "error", err)
			continue
		}
		deletedCount++
		freedBytes += art.SizeBytes
	}

	if deletedCount > 0 {
		w.logInfo("pruned old release artifacts",
			"component", component,
			"deleted", deletedCount,
			"freed_bytes", freedBytes,
			"retention", w.retentionVersions)
	}
	return nil
}

func (w *IntakeWorker) logInfo(msg string, kv ...interface{}) {
	if w.log != nil {
		w.log.Info(msg, kv...)
	}
}

func (w *IntakeWorker) logDebug(msg string, kv ...interface{}) {
	if w.log != nil {
		w.log.Debug(msg, kv...)
	}
}

func (w *IntakeWorker) logWarn(msg string, kv ...interface{}) {
	if w.log != nil {
		w.log.Warn(msg, kv...)
	}
}

// channelFromVersion determines the update channel based on the version string.
// Development builds remain separate from beta/rc/other prereleases.
func channelFromVersion(version string) string {
	version = strings.ToLower(strings.TrimSpace(version))
	version = strings.SplitN(version, "+", 2)[0]
	prerelease := strings.SplitN(version, "-", 2)
	if len(prerelease) == 1 {
		return "stable"
	}
	if prerelease[1] == "dev" || strings.HasPrefix(prerelease[1], "dev.") {
		return "dev"
	}
	return "beta"
}
