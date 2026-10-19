package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const authorizeURL = "https://api.backblazeb2.com/b2api/v4/b2_authorize_account"
const responseLimit = 1 << 20

type authorization struct {
	AccountID string `json:"accountId"`
	Token     string `json:"authorizationToken"`
	APIInfo   struct {
		Storage struct {
			URL     string `json:"apiUrl"`
			S3URL   string `json:"s3ApiUrl"`
			Allowed struct {
				Buckets []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"buckets"`
				Capabilities []string `json:"capabilities"`
				Prefix       string   `json:"namePrefix"`
			} `json:"allowed"`
		} `json:"storageApi"`
	} `json:"apiInfo"`
}

type bucket struct {
	ID        string            `json:"bucketId"`
	Name      string            `json:"bucketName"`
	Type      string            `json:"bucketType"`
	Lifecycle []json.RawMessage `json:"lifecycleRules"`
	Lock      struct {
		Readable bool `json:"isClientAuthorizedToRead"`
		Value    *struct {
			Enabled   *bool `json:"isFileLockEnabled"`
			Retention struct {
				Mode   string `json:"mode"`
				Period *struct {
					Duration int    `json:"duration"`
					Unit     string `json:"unit"`
				} `json:"period"`
			} `json:"defaultRetention"`
		} `json:"value"`
	} `json:"fileLockConfiguration"`
}

type checker struct {
	client *http.Client
}

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Update storage preflight failed:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	flags := flag.NewFlagSet("update-storage-check", flag.ContinueOnError)
	public := flags.String("metadata-bucket", "", "public metadata bucket name")
	recovery := flags.String("recovery-bucket", "", "private recovery bucket name")
	endpoint := flags.String("s3-endpoint", "", "expected HTTPS S3 endpoint")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *public == "" || *recovery == "" || *public == *recovery {
		return fmt.Errorf("distinct -metadata-bucket and -recovery-bucket are required")
	}
	if !validB2URL(*endpoint) {
		return fmt.Errorf("-s3-endpoint must be an HTTPS Backblaze endpoint without a path")
	}
	for _, name := range []string{"B2_METADATA_CHECK_KEY_ID", "B2_METADATA_CHECK_APPLICATION_KEY",
		"B2_RECOVERY_CHECK_KEY_ID", "B2_RECOVERY_CHECK_APPLICATION_KEY"} {
		if strings.TrimSpace(getenv(name)) == "" {
			return fmt.Errorf("required environment variable %s is missing", name)
		}
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		// Never forward Basic credentials or account tokens through redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := checker{client: client}
	for _, check := range []struct {
		name, prefix string
		recovery     bool
	}{
		{*public, "B2_METADATA_CHECK_", false},
		{*recovery, "B2_RECOVERY_CHECK_", true},
	} {
		if err := c.check(ctx, check.name, *endpoint, getenv(check.prefix+"KEY_ID"),
			getenv(check.prefix+"APPLICATION_KEY"), check.recovery); err != nil {
			return fmt.Errorf("%s bucket check: %w", check.prefix, err)
		}
	}
	_, err := fmt.Fprintln(output, "B2 preflight passed: public metadata, private recovery, keep-all lifecycle, recovery governance/365 days; read-only inspection credentials only. No files uploaded or settings changed. Publishing and recovery are not yet qualified.")
	return err
}

func validB2URL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		strings.HasSuffix(host, ".backblazeb2.com") && len(host) > len(".backblazeb2.com") &&
		u.Host == host && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func (c checker) request(ctx context.Context, endpoint, id, secret, token string, body []byte, result interface{}) error {
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct B2 request")
	}
	if body == nil {
		req.SetBasicAuth(id, secret)
	} else {
		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(req)
	if err != nil {
		// Transport errors and service bodies may contain URLs or credentials.
		return fmt.Errorf("B2 request failed (network, TLS, or deadline); check connectivity and credentials")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("B2 request returned HTTP %d; check permissions and bucket configuration", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return fmt.Errorf("read B2 response failed")
	}
	if len(data) > responseLimit {
		return fmt.Errorf("B2 response exceeds size limit")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("invalid B2 JSON response")
	}
	return nil
}

func (c checker) check(ctx context.Context, name, endpoint, id, secret string, recovery bool) error {
	var auth authorization
	if err := c.request(ctx, authorizeURL, id, secret, "", nil, &auth); err != nil {
		return err
	}
	storage := auth.APIInfo.Storage
	if !validB2URL(storage.URL) || storage.S3URL != endpoint || auth.AccountID == "" || auth.Token == "" {
		return fmt.Errorf("authorization response has missing identity or unexpected API/S3 endpoint")
	}
	allowed := storage.Allowed
	if len(allowed.Buckets) != 1 || allowed.Buckets[0].Name != name ||
		allowed.Buckets[0].ID == "" || allowed.Prefix != "" {
		return fmt.Errorf("inspection key must be restricted to exactly the intended bucket, without a filename prefix")
	}
	permitted := map[string]bool{
		"listBuckets": true, "readBucketRetentions": true, "readBucketEncryption": true,
		"listFiles": true, "readFiles": true, "readFileRetentions": true, "readFileLegalHolds": true,
		"readBuckets": true, "readBucketReplications": true, "readBucketNotifications": true,
	}
	found := make(map[string]bool)
	for _, capability := range allowed.Capabilities {
		if !permitted[capability] {
			return fmt.Errorf("inspection key has a capability outside the read-only allowlist; replace it with a restricted inspection key")
		}
		found[capability] = true
	}
	if !found["listBuckets"] || !found["readBucketRetentions"] {
		return fmt.Errorf("inspection key requires listBuckets and readBucketRetentions")
	}
	body, err := json.Marshal(map[string]string{"accountId": auth.AccountID, "bucketId": allowed.Buckets[0].ID})
	if err != nil {
		return err
	}
	var listing struct {
		Buckets []bucket `json:"buckets"`
	}
	if err := c.request(ctx, storage.URL+"/b2api/v4/b2_list_buckets", "", "", auth.Token, body, &listing); err != nil {
		return err
	}
	if len(listing.Buckets) != 1 {
		return fmt.Errorf("B2 did not return exactly the requested bucket")
	}
	b := listing.Buckets[0]
	expectedType := "allPublic"
	if recovery {
		expectedType = "allPrivate"
	}
	if b.ID != allowed.Buckets[0].ID || b.Name != name || b.Type != expectedType {
		return fmt.Errorf("bucket identity or public/private access does not match expected configuration")
	}
	if b.Lifecycle == nil || len(b.Lifecycle) != 0 {
		return fmt.Errorf("bucket must explicitly report no lifecycle rules (keep all versions)")
	}
	if !b.Lock.Readable || b.Lock.Value == nil || b.Lock.Value.Enabled == nil {
		return fmt.Errorf("bucket retention configuration is not readable")
	}
	if !recovery {
		if *b.Lock.Value.Enabled {
			return fmt.Errorf("metadata bucket must have Object Lock disabled for the initial layout")
		}
		return nil
	}
	retention := b.Lock.Value.Retention
	if !*b.Lock.Value.Enabled || retention.Mode != "governance" || retention.Period == nil ||
		retention.Period.Duration != 365 || retention.Period.Unit != "days" {
		return fmt.Errorf("recovery requires Object Lock with default governance retention of 365 days; console-created defaults may be compliance; owner must inspect/change future-upload defaults before any upload")
	}
	return nil
}
