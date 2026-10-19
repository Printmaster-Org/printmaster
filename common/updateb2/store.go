package updateb2

import (
	"bytes"
	"context"
	"crypto/sha1" // B2's upload checksum is transport-only, never publisher trust.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const objectLimit = 16 << 20

// Store is a bounded B2 Native API adapter, not a cross-host lock or a signer.
// Callers must serialize all writers externally, including owner/manual tools.
type Store struct {
	checker
	auth     authorization
	bucketID string
	recovery bool
	readOnly bool
}

type StoreOptions struct {
	Bucket, S3Endpoint, KeyID, ApplicationKey string
	Recovery                                  bool
}

func NewStore(ctx context.Context, opts StoreOptions) (*Store, error) {
	return newStore(ctx, opts, false)
}

// NewRecoveryReader accepts only read-only credentials and cannot upload.
func NewRecoveryReader(ctx context.Context, opts StoreOptions) (*Store, error) {
	opts.Recovery = true
	return newStore(ctx, opts, true)
}

func newStore(ctx context.Context, opts StoreOptions, readOnly bool) (*Store, error) {
	return (checker{client: newHTTPClient()}).openStore(ctx, opts, readOnly)
}

func (c checker) openStore(ctx context.Context, opts StoreOptions, readOnly bool) (*Store, error) {
	if opts.Bucket == "" || !validB2URL(opts.S3Endpoint) || opts.KeyID == "" || opts.ApplicationKey == "" {
		return nil, fmt.Errorf("B2 delivery requires bucket, HTTPS S3 endpoint and private credentials")
	}
	auth, err := c.inspect(ctx, opts.Bucket, opts.S3Endpoint, opts.KeyID, opts.ApplicationKey, opts.Recovery, !readOnly)
	if err != nil {
		return nil, err
	}
	if !validB2URL(auth.APIInfo.Storage.DownloadURL) {
		return nil, fmt.Errorf("unexpected B2 download endpoint")
	}
	if readOnly {
		capabilities := make(map[string]bool)
		for _, name := range auth.APIInfo.Storage.Allowed.Capabilities {
			capabilities[name] = true
		}
		if !capabilities["listFiles"] || !capabilities["readFiles"] || !capabilities["readFileRetentions"] {
			return nil, fmt.Errorf("recovery reader requires listFiles, readFiles and readFileRetentions")
		}
	}
	return &Store{checker: c, auth: auth, bucketID: auth.APIInfo.Storage.Allowed.Buckets[0].ID, recovery: opts.Recovery, readOnly: readOnly}, nil
}

type fileInfo struct {
	ID        string `json:"fileId"`
	BucketID  string `json:"bucketId"`
	Name      string `json:"fileName"`
	Action    string `json:"action"`
	Length    int64  `json:"contentLength"`
	Uploaded  int64  `json:"uploadTimestamp"`
	Retention struct {
		Readable bool `json:"isClientAuthorizedToRead"`
		Value    struct {
			Mode  string `json:"mode"`
			Until int64  `json:"retainUntilTimestamp"`
		} `json:"value"`
	} `json:"fileRetention"`
}

func validObjectName(name string) bool {
	if name == "" || len(name) > 1024 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (s *Store) call(ctx context.Context, operation string, input, output interface{}) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode B2 request")
	}
	return s.request(ctx, s.auth.APIInfo.Storage.URL+"/b2api/v4/"+operation, "", "", s.auth.Token, body, output)
}

func (s *Store) validateInfo(info fileInfo, name string) error {
	if info.ID == "" || info.BucketID != s.bucketID || info.Name != name || info.Action != "upload" ||
		info.Length < 1 || info.Length > objectLimit {
		return fmt.Errorf("B2 object identity/action/length mismatch; hidden versions require owner review")
	}
	if s.recovery {
		now := time.Now().UnixMilli()
		year := (365 * 24 * time.Hour).Milliseconds()
		if !info.Retention.Readable || info.Retention.Value.Mode != "governance" ||
			info.Uploaded <= 0 || info.Uploaded > now+time.Minute.Milliseconds() ||
			info.Retention.Value.Until <= now || info.Retention.Value.Until < info.Uploaded+year {
			return fmt.Errorf("recovery object lacks readable, unexpired Governance retention covering 365 days from upload")
		}
	}
	return nil
}

func (s *Store) download(ctx context.Context, info fileInfo) ([]byte, error) {
	endpoint := s.auth.APIInfo.Storage.DownloadURL + "/b2api/v4/b2_download_file_by_id?fileId=" + url.QueryEscape(info.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("construct B2 download")
	}
	req.Header.Set("Authorization", s.auth.Token)
	data, err := s.do(req, objectLimit)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Length {
		return nil, fmt.Errorf("B2 download length mismatch")
	}
	return data, nil
}

// Get reads the exact latest retained version. A hide marker is an error, not
// permission to silently recreate an apparently missing object.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	if !validObjectName(name) {
		return nil, fmt.Errorf("invalid B2 object name")
	}
	var listing struct {
		Files []fileInfo `json:"files"`
	}
	err := s.call(ctx, "b2_list_file_versions", map[string]interface{}{
		"bucketId": s.bucketID, "prefix": name, "startFileName": name, "maxFileCount": 1,
	}, &listing)
	if err != nil {
		return nil, err
	}
	if listing.Files == nil || len(listing.Files) > 1 {
		return nil, fmt.Errorf("B2 version listing is missing its file set")
	}
	if len(listing.Files) == 0 || listing.Files[0].Name != name {
		return nil, fs.ErrNotExist
	}
	if err := s.validateInfo(listing.Files[0], name); err != nil {
		return nil, err
	}
	return s.download(ctx, listing.Files[0])
}

// GetPinned is explicit recovery: unlike ordinary Get, it may inspect behind
// hide markers/newer poisoned versions, but accepts only the independently
// pinned bytes from a still-retained valid recovery version.
func (s *Store) GetPinned(ctx context.Context, name, expectedSHA256 string) ([]byte, error) {
	expected, err := hex.DecodeString(expectedSHA256)
	if !s.recovery || !validObjectName(name) || err != nil || len(expected) != sha256.Size {
		return nil, fmt.Errorf("pinned recovery requires a recovery store, valid name and SHA256")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cursor := ""
	seen := make(map[string]bool)
	var downloaded int64
	for page := 0; page < 10; page++ {
		var listing struct {
			Files    []fileInfo `json:"files"`
			NextName string     `json:"nextFileName"`
			NextID   string     `json:"nextFileId"`
		}
		input := map[string]interface{}{"bucketId": s.bucketID, "prefix": name, "startFileName": name, "maxFileCount": 10}
		if cursor != "" {
			input["startFileId"] = cursor
		}
		if err := s.call(ctx, "b2_list_file_versions", input, &listing); err != nil {
			return nil, err
		}
		if listing.Files == nil || len(listing.Files) > 10 {
			return nil, fmt.Errorf("invalid pinned recovery version listing")
		}
		for _, info := range listing.Files {
			if info.Name != name {
				if strings.HasPrefix(info.Name, name) && info.Name > name {
					return nil, fmt.Errorf("pinned recovery version not found")
				}
				return nil, fmt.Errorf("unexpected recovery listing identity")
			}
			if info.ID == "" || info.BucketID != s.bucketID || seen[info.ID] {
				return nil, fmt.Errorf("invalid or repeated recovery version")
			}
			seen[info.ID] = true
			if info.Action == "hide" {
				continue
			}
			if info.Action != "upload" || info.Length < 1 || info.Length > objectLimit {
				return nil, fmt.Errorf("invalid recovery version action/length")
			}
			downloaded += info.Length
			if downloaded > 64<<20 {
				return nil, fmt.Errorf("pinned recovery scan exceeds 64 MiB download limit")
			}
			data, err := s.download(ctx, info)
			if err != nil {
				return nil, err
			}
			actual := sha256.Sum256(data)
			if bytes.Equal(actual[:], expected) {
				if err := s.validateInfo(info, name); err != nil {
					return nil, err
				}
				return data, nil
			}
		}
		if listing.NextName == "" && listing.NextID == "" {
			return nil, fmt.Errorf("pinned recovery version not found")
		}
		if listing.NextName != name || listing.NextID == "" || listing.NextID == cursor ||
			seen[listing.NextID] || len(listing.Files) == 0 {
			return nil, fmt.Errorf("invalid pinned recovery pagination")
		}
		cursor = listing.NextID
	}
	return nil, fmt.Errorf("pinned recovery exceeds 100-version scan limit; owner review required")
}

// Put verifies the uploaded version's identity, retention and exact bytes by
// file ID. It never retries automatically after an ambiguous upload outcome.
func (s *Store) Put(ctx context.Context, name string, data []byte) error {
	if s.readOnly {
		return fmt.Errorf("read-only recovery store cannot upload")
	}
	if !validObjectName(name) || len(data) == 0 || len(data) > objectLimit {
		return fmt.Errorf("invalid B2 upload name or length")
	}
	var upload struct {
		URL      string `json:"uploadUrl"`
		Token    string `json:"authorizationToken"`
		BucketID string `json:"bucketId"`
	}
	if err := s.call(ctx, "b2_get_upload_url", map[string]string{"bucketId": s.bucketID}, &upload); err != nil {
		return err
	}
	u, err := url.Parse(upload.URL)
	if err != nil || u == nil {
		return fmt.Errorf("invalid B2 upload endpoint")
	}
	host := u.Hostname()
	trustedHost := strings.HasSuffix(host, ".backblazeb2.com") ||
		(strings.HasPrefix(host, "pod-") && strings.HasSuffix(host, ".backblaze.com"))
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Host != host ||
		!trustedHost || u.Fragment != "" || u.Path != "/b2api/v4/b2_upload_file" ||
		upload.Token == "" || upload.BucketID != s.bucketID {
		return fmt.Errorf("unexpected B2 upload endpoint or missing token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upload.URL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("construct B2 upload")
	}
	sum := sha1.Sum(data)
	req.Header.Set("Authorization", upload.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bz-File-Name", name)
	req.Header.Set("X-Bz-Content-Sha1", hex.EncodeToString(sum[:]))
	req.Header.Set("X-Bz-Info-b2-cache-control", "no-store")
	response, err := s.do(req, responseLimit)
	if err != nil {
		return err
	}
	var uploaded fileInfo
	if err := json.Unmarshal(response, &uploaded); err != nil || uploaded.ID == "" {
		return fmt.Errorf("invalid B2 upload receipt; preserve local publication and retry explicitly")
	}
	var actual fileInfo
	if err := s.call(ctx, "b2_get_file_info", map[string]string{"fileId": uploaded.ID}, &actual); err != nil {
		return err
	}
	if actual.ID != uploaded.ID {
		return fmt.Errorf("B2 upload receipt/version mismatch")
	}
	if err := s.validateInfo(actual, name); err != nil {
		return err
	}
	readBack, err := s.download(ctx, actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(readBack, data) {
		return fmt.Errorf("B2 uploaded version read-back mismatch")
	}
	return nil
}
