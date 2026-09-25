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
	fileName  string
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
	if err := w.runOnce(ctx); err != nil {
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
			if err := w.runOnce(ctx); err != nil {
				w.logWarn("release intake iteration failed", "error", err)
			}
		}
	}
}

// RunOnce executes a single fetch cycle (exported for tests).
func (w *IntakeWorker) RunOnce(ctx context.Context) error {
	return w.runOnce(ctx)
}

// RunOnceWithProgress executes a single fetch cycle with progress reporting.
func (w *IntakeWorker) RunOnceWithProgress(ctx context.Context, onProgress ProgressCallback) error {
	return w.runOnceWithProgress(ctx, onProgress)
}

// artifactPlan represents a single artifact to be downloaded.
type artifactPlan struct {
	desc      artifactDescriptor
	rel       ghRelease
	asset     ghAsset
	needsWork bool // false if already cached
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

	var plans []artifactPlan
	processed := map[string]int{}

	for _, rel := range releases {
		component, version := parseTag(rel.TagName)
		if component == "" || version == "" {
			continue
		}
		if rel.Draft {
			continue
		}
		if rel.Prerelease && !w.includePrerelease {
			continue
		}
		if processed[component] >= w.maxReleases {
			continue
		}

		for _, asset := range rel.Assets {
			if asset.BrowserDownloadURL == "" {
				continue
			}
			desc, ok := buildDescriptor(component, version, asset.Name)
			if !ok {
				continue
			}

			// Check if already cached
			existing, err := w.store.GetReleaseArtifact(ctx, desc.component, desc.version, desc.platform, desc.arch)
			needsWork := true
			if err == nil && existing != nil && fileExists(existing.CachePath) && existing.SHA256 != "" {
				needsWork = false
			}

			plans = append(plans, artifactPlan{
				desc:      desc,
				rel:       rel,
				asset:     asset,
				needsWork: needsWork,
			})
		}
		processed[component]++
	}

	// Calculate totals
	var totalFiles, filesToDownload int
	var totalBytes, bytesToDownload int64
	for _, p := range plans {
		totalFiles++
		totalBytes += p.asset.Size
		if p.needsWork {
			filesToDownload++
			bytesToDownload += p.asset.Size
		}
	}

	report(SyncProgress{
		Phase:          "processing",
		Message:        fmt.Sprintf("Found %d artifacts (%d need downloading)", totalFiles, filesToDownload),
		TotalFiles:     filesToDownload,
		TotalBytes:     bytesToDownload,
		CompletedFiles: 0,
		CompletedBytes: 0,
	})

	if filesToDownload == 0 {
		report(SyncProgress{
			Phase:           "complete",
			Message:         "All artifacts up to date",
			TotalFiles:      0,
			CompletedFiles:  0,
			TotalBytes:      0,
			CompletedBytes:  0,
			PercentComplete: 100,
		})
		// Still need to ensure manifests and prune
		for _, p := range plans {
			if !p.needsWork {
				existing, _ := w.store.GetReleaseArtifact(ctx, p.desc.component, p.desc.version, p.desc.platform, p.desc.arch)
				if existing != nil {
					w.ensureManifest(ctx, existing)
				}
			}
		}
		w.pruneIfConfigured(ctx)
		return nil
	}

	// Phase 3: Download artifacts
	var completedFiles int
	var completedBytes int64

	for _, p := range plans {
		if !p.needsWork {
			// Already cached, just ensure manifest
			existing, _ := w.store.GetReleaseArtifact(ctx, p.desc.component, p.desc.version, p.desc.platform, p.desc.arch)
			if existing != nil {
				w.ensureManifest(ctx, existing)
			}
			continue
		}

		// Report starting this file
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

		// Download with progress
		cachePath, sha, size, err := w.downloadArtifactWithProgress(ctx, p.desc, p.asset.BrowserDownloadURL, p.asset.Size, func(downloaded int64) {
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
		})

		if err != nil {
			w.logWarn("artifact download failed", "asset", p.asset.Name, "error", err)
			continue
		}

		// Save to database
		record := &storage.ReleaseArtifact{
			Component:    p.desc.component,
			Version:      p.desc.version,
			Platform:     p.desc.platform,
			Arch:         p.desc.arch,
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
			w.logWarn("failed to save artifact", "asset", p.asset.Name, "error", err)
			continue
		}

		w.ensureManifest(ctx, record)
		completedFiles++
		completedBytes += p.asset.Size

		w.logInfo("cached release artifact", "component", p.desc.component, "version", p.desc.version, "platform", p.desc.platform, "arch", p.desc.arch)
	}

	// Prune old artifacts
	w.pruneIfConfigured(ctx)

	report(SyncProgress{
		Phase:           "complete",
		Message:         fmt.Sprintf("Sync complete: %d files downloaded", completedFiles),
		TotalFiles:      filesToDownload,
		CompletedFiles:  completedFiles,
		TotalBytes:      bytesToDownload,
		CompletedBytes:  completedBytes,
		PercentComplete: 100,
	})

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

// downloadArtifactWithProgress downloads an artifact and reports progress.
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

func (w *IntakeWorker) runOnce(ctx context.Context) error {
	releases, err := w.fetchReleases(ctx)
	if err != nil {
		return err
	}

	w.logInfo("Processing releases from GitHub", "count", len(releases), "include_prerelease", w.includePrerelease)

	processed := map[string]int{}
	for _, rel := range releases {
		component, version := parseTag(rel.TagName)
		if component == "" || version == "" {
			w.logInfo("Skipping release with unparseable tag", "tag", rel.TagName)
			continue
		}
		if rel.Draft {
			w.logInfo("Skipping draft", "tag", rel.TagName)
			continue
		}
		if rel.Prerelease && !w.includePrerelease {
			w.logInfo("Skipping prerelease (include_prerelease disabled)", "tag", rel.TagName)
			continue
		}
		if processed[component] >= w.maxReleases {
			continue
		}
		w.logInfo("Processing release", "component", component, "version", version, "assets", len(rel.Assets), "prerelease", rel.Prerelease)
		if err := w.processRelease(ctx, component, version, rel); err != nil {
			w.logWarn("release processing failed", "component", component, "version", version, "error", err)
			continue
		}
		processed[component]++
	}

	w.logInfo("Release processing complete", "processed", processed)

	// Prune old artifacts if retention is configured
	if w.retentionVersions > 0 {
		for _, comp := range []string{"agent", "server"} {
			if err := w.pruneOldArtifacts(ctx, comp); err != nil {
				w.logWarn("artifact pruning failed", "component", comp, "error", err)
			}
		}
	}

	return nil
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

func (w *IntakeWorker) processRelease(ctx context.Context, component, version string, rel ghRelease) error {
	w.logInfo("Processing release assets", "component", component, "version", version, "asset_count", len(rel.Assets))
	matchedAssets := 0
	for _, asset := range rel.Assets {
		if asset.BrowserDownloadURL == "" {
			w.logInfo("Asset has no download URL", "asset", asset.Name)
			continue
		}
		desc, ok := buildDescriptor(component, version, asset.Name)
		if !ok {
			w.logInfo("Asset name didn't match pattern", "asset", asset.Name, "expected_prefix", fmt.Sprintf("printmaster-%s-v%s-", component, version))
			continue
		}
		matchedAssets++
		w.logInfo("Matched asset", "component", component, "version", version, "asset", asset.Name, "platform", desc.platform, "arch", desc.arch)
		if err := w.ensureArtifact(ctx, desc, rel, asset); err != nil {
			w.logWarn("artifact processing failed", "component", component, "version", version, "asset", asset.Name, "error", err)
		}
	}
	w.logInfo("Release processing summary", "component", component, "version", version, "matched_assets", matchedAssets, "total_assets", len(rel.Assets))
	return nil
}

func (w *IntakeWorker) ensureArtifact(ctx context.Context, desc artifactDescriptor, rel ghRelease, asset ghAsset) error {
	existing, err := w.store.GetReleaseArtifact(ctx, desc.component, desc.version, desc.platform, desc.arch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && existing != nil && fileExists(existing.CachePath) && existing.SHA256 != "" {
		needsMetadataUpdate := existing.SourceURL != asset.BrowserDownloadURL || existing.ReleaseNotes != rel.Body || existing.SizeBytes != asset.Size
		if needsMetadataUpdate {
			existing.SourceURL = asset.BrowserDownloadURL
			existing.ReleaseNotes = rel.Body
			existing.SizeBytes = asset.Size
			existing.PublishedAt = rel.PublishedAt
			if err := w.store.UpsertReleaseArtifact(ctx, existing); err != nil {
				return err
			}
		}
		w.ensureManifest(ctx, existing)
		return nil
	}

	cachePath, sha, size, err := w.downloadArtifact(ctx, desc, asset.BrowserDownloadURL)
	if err != nil {
		return err
	}

	record := &storage.ReleaseArtifact{
		Component:    desc.component,
		Version:      desc.version,
		Platform:     desc.platform,
		Arch:         desc.arch,
		Channel:      channelFromVersion(desc.version),
		SourceURL:    asset.BrowserDownloadURL,
		CachePath:    cachePath,
		SHA256:       sha,
		SizeBytes:    size,
		ReleaseNotes: rel.Body,
		PublishedAt:  rel.PublishedAt,
		DownloadedAt: time.Now().UTC(),
	}
	if err := w.store.UpsertReleaseArtifact(ctx, record); err != nil {
		return err
	}
	w.logInfo("cached release artifact", "component", desc.component, "version", desc.version, "platform", desc.platform, "arch", desc.arch)
	w.ensureManifest(ctx, record)
	return nil
}

func (w *IntakeWorker) ensureManifest(ctx context.Context, artifact *storage.ReleaseArtifact) {
	if w.manifests == nil || artifact == nil {
		return
	}
	if _, err := w.manifests.EnsureManifestForArtifact(ctx, artifact); err != nil {
		w.logWarn("manifest generation failed", "component", artifact.Component, "version", artifact.Version, "platform", artifact.Platform, "arch", artifact.Arch, "error", err)
	}
}

func (w *IntakeWorker) downloadArtifact(ctx context.Context, desc artifactDescriptor, downloadURL string) (string, string, int64, error) {
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
	written, err := io.Copy(writer, resp.Body)
	if err != nil {
		return "", "", 0, err
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

func buildDescriptor(component, version, assetName string) (artifactDescriptor, bool) {
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

func (w *IntakeWorker) logWarn(msg string, kv ...interface{}) {
	if w.log != nil {
		w.log.Warn(msg, kv...)
	}
}

// channelFromVersion determines the update channel based on the version string.
// Versions containing "-dev." are assigned to the "dev" channel, all others to "stable".
func channelFromVersion(version string) string {
	if strings.Contains(version, "-dev.") {
		return "dev"
	}
	return "stable"
}
