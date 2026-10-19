package updatetrust

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// PublicReader retrieves pinned public bytes, without B2 or Agent credentials.
type PublicReader struct {
	base   string
	client *http.Client
}

func NewPublicReader(base string) (*PublicReader, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(u.Path, "..") {
		return nil, fmt.Errorf("public metadata base must be explicit HTTPS without credentials/query/fragment/traversal")
	}
	return &PublicReader{base: strings.TrimRight(base, "/"), client: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (r *PublicReader) GetPinned(ctx context.Context, name, pin string) ([]byte, error) {
	// Restore uses the same content-addressed recovery layout. Public storage
	// exposes metadata at its ordinary TUF paths and one sealed inventory.
	parts := strings.Split(name, "/")
	path := ""
	if len(parts) == 2 && parts[0] == "records" && validPin(strings.TrimSuffix(parts[1], ".json")) &&
		strings.HasSuffix(parts[1], ".json") {
		path = name
	} else if len(parts) == 3 && parts[0] == "publications" && validPin(parts[1]) {
		if parts[2] == "record.json" {
			path = "records/" + parts[1] + ".json"
		} else if ValidMetadataName(parts[2]) {
			path = parts[2]
		}
	}
	if path == "" || !validPin(pin) {
		return nil, fmt.Errorf("invalid public pinned metadata request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	response, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("public metadata request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("public metadata HTTP %d for %s", response.StatusCode, path)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxMetadataBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > MaxMetadataBytes ||
		fmt.Sprintf("%x", sha256.Sum256(data)) != pin {
		return nil, fmt.Errorf("public metadata length/hash/read failure for %s", path)
	}
	return data, nil
}

// MirrorPublication validates a completed independent publication, then commits
// public metadata timestamp-last. Run under an exclusive scheduled task; Agents
// still enforce their independently embedded TUF root.
func MirrorPublication(ctx context.Context, directory string, root []byte, identity PublicationIdentity, source PinnedPublicationReader) (result error) {
	if identity.RootSHA256 != digest(root) || identity.Version <= 0 || source == nil {
		return fmt.Errorf("mirror requires independently selected identity and matching trusted root")
	}
	if err := ordinaryAncestors(directory); err != nil {
		return err
	}
	lock := filepath.Join(directory, ".publisher.lock")
	if err := writeExclusivePublication(lock, []byte("mirror\n")); err != nil {
		return fmt.Errorf("mirror locked; never automatically break stale locks: %w", err)
	}
	defer func() { result = errors.Join(result, os.Remove(lock)) }()
	current, err := optionalTimestamp(directory)
	if err != nil {
		return err
	}
	if current != "" {
		data, err := readPublisherMetadata(filepath.Join(directory, "timestamp.json"))
		if err != nil {
			return err
		}
		version, err := metadataTimestampVersion(data)
		if err != nil || version > identity.Version || (version == identity.Version && current != identity.TimestampSHA256) {
			return fmt.Errorf("mirror rollback/conflicting timestamp rejected")
		}
	}
	stage, err := os.MkdirTemp(directory, ".mirror-")
	if err != nil {
		return err
	}
	// Only this freshly created, uniquely named staging tree is removed.
	defer func() { result = errors.Join(result, os.RemoveAll(stage)) }()
	workspace, records := filepath.Join(stage, "workspace"), filepath.Join(stage, "records")
	for _, path := range []string{workspace, records} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
	}
	if err := RestorePublication(ctx, RestoreOptions{Directory: workspace, RecordDirectory: records, Root: root,
		ExpectedTimestampSHA256: identity.TimestampSHA256, ExpectedRecordSHA256: identity.RecordSHA256,
		ExpectedTimestampVersion: identity.Version, ExternalSerialization: true, Recovery: source}); err != nil {
		return err
	}
	_, files, _, err := readSealedPublication(workspace, root, time.Now())
	if err != nil {
		return err
	}
	return copyPublication(directory, files, true)
}

// CheckMirrorFreshness checks the actual mirrored signed timestamp, not the
// workflow's last-success time. The required margin is a release-policy input.
func CheckMirrorFreshness(directory string, root []byte, margin time.Duration) error {
	if margin < time.Hour || margin > 7*24*time.Hour {
		return fmt.Errorf("freshness margin must be between one hour and seven days")
	}
	if err := ValidateBootstrapRoot(root, time.Now().Add(margin)); err != nil {
		return err
	}
	data, err := readPublisherMetadata(filepath.Join(directory, "timestamp.json"))
	if err != nil {
		return err
	}
	ts, err := metadata.Timestamp().FromBytes(data)
	if err != nil {
		return err
	}
	if !ts.Signed.Expires.After(time.Now().Add(margin)) {
		return fmt.Errorf("publisher timestamp expires within required freshness margin %s", margin)
	}
	return nil
}
