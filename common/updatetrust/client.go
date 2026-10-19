// Package updatetrust binds PrintMaster release identities to publisher TUF
// targets. The TUF library owns signatures, rotation, expiry and rollback checks;
// fleet policy and installation/recovery remain outside this package.
package updatetrust

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Target identifies the exact release authorized by publisher metadata.
// Channel is part of the signed target path, not trusted Server-side labeling.
type Target struct {
	Component string `json:"component"`
	Channel   string `json:"channel"`
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Format    string `json:"format"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

func (t Target) Path() (string, error) {
	for _, part := range []string{t.Component, t.Channel, t.Version, t.Platform, t.Arch, t.Format} {
		if !segment.MatchString(part) || part == "." || part == ".." {
			return "", fmt.Errorf("invalid publisher target identity")
		}
	}
	return strings.Join([]string{t.Component, t.Channel, t.Version, t.Platform, t.Arch, t.Format}, "/"), nil
}

type Options struct {
	TrustedRoot []byte
	MetadataURL string
	CacheDir    string // Exclusive metadata cache; preserve its sibling .state.json record.
	HTTPClient  *http.Client
}

type Client struct {
	refreshLock chan struct{}
	options     Options
}

func NewClient(opts Options) (*Client, error) {
	if len(opts.TrustedRoot) == 0 {
		return nil, fmt.Errorf("publisher TUF root has not been provisioned")
	}
	root, err := metadata.Root().FromBytes(opts.TrustedRoot)
	if err != nil {
		return nil, fmt.Errorf("parse publisher root: %w", err)
	}
	if err := root.VerifyDelegate(metadata.ROOT, root); err != nil {
		return nil, fmt.Errorf("verify publisher root: %w", err)
	}
	u, err := url.Parse(opts.MetadataURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid publisher metadata URL")
	}
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("persistent TUF cache directory required")
	}
	opts.CacheDir, err = filepath.Abs(opts.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("resolve publisher metadata directory: %w", err)
	}
	if filepath.Dir(opts.CacheDir) == opts.CacheDir {
		return nil, fmt.Errorf("publisher metadata directory cannot be a filesystem root")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	opts.TrustedRoot = append([]byte(nil), opts.TrustedRoot...)
	return &Client{options: opts, refreshLock: make(chan struct{}, 1)}, nil
}

// Verify refreshes trusted metadata before authorizing the Server's exact
// candidate. There is deliberately no legacy or expired-metadata fallback.
func (c *Client) Verify(ctx context.Context, target Target) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case c.refreshLock <- struct{}{}:
		defer func() { <-c.refreshLock }()
	case <-ctx.Done():
		return fmt.Errorf("wait for publisher verification: %w", ctx.Err())
	}
	path, err := target.Path()
	if err != nil {
		return err
	}
	hash, err := hex.DecodeString(target.SHA256)
	if err != nil || len(hash) != 32 || target.SizeBytes <= 0 {
		return fmt.Errorf("invalid publisher target size or SHA-256")
	}
	if err := c.prepareTrustState(); err != nil {
		return err
	}
	root := c.options.TrustedRoot
	cached, err := os.ReadFile(filepath.Join(c.options.CacheDir, "root.json"))
	if err == nil {
		// Locally cached roots were authenticated by earlier refreshes. Local
		// filesystem protection is part of the TUF client's trust boundary.
		// A newer root shipped with a binary must supersede older cached trust.
		cachedRoot, err := metadata.Root().FromBytes(cached)
		if err != nil {
			return fmt.Errorf("parse trusted root cache: %w", err)
		}
		if err := cachedRoot.VerifyDelegate("root", cachedRoot); err != nil {
			return fmt.Errorf("verify trusted root cache: %w", err)
		}
		shippedRoot, err := metadata.Root().FromBytes(root)
		if err != nil {
			return err
		}
		if cachedRoot.Signed.Version > shippedRoot.Signed.Version {
			root = cached
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read trusted root cache: %w", err)
	}
	cfg, err := config.New(c.options.MetadataURL, root)
	if err != nil {
		return err
	}
	cfg.LocalMetadataDir = c.options.CacheDir
	cfg.LocalTargetsDir = filepath.Join(c.options.CacheDir, "targets")
	cfg.Fetcher = &contextFetcher{ctx: ctx, client: c.options.HTTPClient}
	// Refresh may advance authenticated root/timestamp state before a later
	// network or target failure. Preserve that state even when authorization
	// fails; otherwise retries could lose the library's rollback protection.
	defer func() {
		result = errors.Join(result, c.checkpointTrustState())
	}()
	client, err := updater.New(cfg)
	if err != nil {
		return fmt.Errorf("load publisher trust: %w", err)
	}
	if err := client.Refresh(); err != nil {
		return fmt.Errorf("refresh publisher metadata: %w", err)
	}
	info, err := client.GetTargetInfo(path)
	if err != nil {
		return fmt.Errorf("authorize publisher target %s: %w", path, err)
	}
	if info.Length != target.SizeBytes || !strings.EqualFold(hex.EncodeToString(info.Hashes["sha256"]), target.SHA256) {
		return fmt.Errorf("publisher target %s does not match offered artifact", path)
	}
	return nil
}

// The library fetcher does not accept a caller context. This adapter preserves
// cancellation and size limits without changing the TUF verification workflow.
type contextFetcher struct {
	ctx    context.Context
	client *http.Client
}

func (f *contextFetcher) DownloadFile(source string, maxLength int64, _ time.Duration) ([]byte, error) {
	if maxLength <= 0 || maxLength > MaxMetadataBytes {
		return nil, fmt.Errorf("invalid TUF metadata length limit %d", maxLength)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: resp.StatusCode, URL: source}
	}
	if resp.ContentLength > maxLength {
		return nil, fmt.Errorf("publisher metadata exceeds maximum length")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLength+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxLength {
		return nil, fmt.Errorf("publisher metadata exceeds maximum length")
	}
	return data, nil
}
