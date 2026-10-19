package updatestate

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// GitHub uses the Contents API's existing-blob SHA precondition. The token must
// be separately provisioned for this repository only; GITHUB_TOKEN cannot grant
// cross-repository write access. No caller-supplied API host is accepted.
type GitHub struct {
	repository string
	token      string
	client     *http.Client
}

func NewGitHub(ctx context.Context, repository, token string) (*GitHub, error) {
	if !repositoryName.MatchString(repository) || strings.HasSuffix(repository, ".git") {
		return nil, fmt.Errorf("explicit owner/repository required for publication custody")
	}
	g := &GitHub{repository: repository, token: token, client: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if err := g.checkProtection(ctx); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *GitHub) request(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com/repos/"+g.repository+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("construct GitHub custody request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "PrintMaster-publication-custody")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	response, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub custody request failed")
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return fmt.Errorf("GitHub custody response unreadable/oversized")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Do not echo response bodies, request headers or credential-bearing errors.
		return fmt.Errorf("GitHub custody HTTP %d; no automatic retry or missing-state fallback", response.StatusCode)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("invalid GitHub custody response")
	}
	return nil
}

func (g *GitHub) checkProtection(ctx context.Context) error {
	var protection struct {
		EnforceAdmins struct {
			Enabled bool `json:"enabled"`
		} `json:"enforce_admins"`
		ForcePushes *struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_force_pushes"`
		Deletions *struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_deletions"`
		Linear struct {
			Enabled bool `json:"enabled"`
		} `json:"required_linear_history"`
		Restrictions *struct {
			Users []json.RawMessage `json:"users"`
			Teams []json.RawMessage `json:"teams"`
			Apps  []json.RawMessage `json:"apps"`
		} `json:"restrictions"`
	}
	if err := g.request(ctx, http.MethodGet, "/branches/main/protection", nil, &protection); err != nil {
		return fmt.Errorf("read independent authority branch protection: %w", err)
	}
	if !protection.EnforceAdmins.Enabled || protection.ForcePushes == nil || protection.Deletions == nil ||
		protection.ForcePushes.Enabled || protection.Deletions.Enabled ||
		!protection.Linear.Enabled || protection.Restrictions == nil ||
		len(protection.Restrictions.Users)+len(protection.Restrictions.Teams)+len(protection.Restrictions.Apps) == 0 {
		return fmt.Errorf("authority main must enforce administrators, linear history, restricted writers and prohibit force-push/deletion")
	}
	return nil
}

func (g *GitHub) Read(ctx context.Context) ([]byte, string, error) {
	if err := g.checkProtection(ctx); err != nil {
		return nil, "", err
	}
	var file struct {
		Type     string `json:"type"`
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	if err := g.request(ctx, http.MethodGet, "/contents/state.json?ref=main", nil, &file); err != nil {
		return nil, "", err
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || file.Type != "file" || file.Encoding != "base64" || file.Size != len(data) ||
		len(data) == 0 || len(data) > 16<<10 || !validHash(file.SHA, 40) {
		return nil, "", fmt.Errorf("invalid GitHub authority file")
	}
	hash := sha1.New()
	fmt.Fprintf(hash, "blob %d\x00", len(data))
	hash.Write(data)
	if hex.EncodeToString(hash.Sum(nil)) != file.SHA {
		return nil, "", fmt.Errorf("GitHub authority blob hash mismatch")
	}
	return data, file.SHA, nil
}

func (g *GitHub) Replace(ctx context.Context, revision string, data []byte) error {
	if g.token == "" || !validHash(revision, 40) || len(data) == 0 || len(data) > 16<<10 {
		return fmt.Errorf("scoped GitHub write token, existing revision and bounded state required")
	}
	if err := g.checkProtection(ctx); err != nil {
		return err
	}
	var result struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	err := g.request(ctx, http.MethodPut, "/contents/state.json", struct {
		Message string `json:"message"`
		Content string `json:"content"`
		SHA     string `json:"sha"`
		Branch  string `json:"branch"`
	}{
		Message: "chore(updates): advance public publication custody",
		Content: base64.StdEncoding.EncodeToString(data), SHA: revision, Branch: "main",
	}, &result)
	if err != nil {
		return err
	}
	if !validHash(result.Content.SHA, 40) {
		return fmt.Errorf("GitHub authority write response lacks blob revision")
	}
	return nil
}
