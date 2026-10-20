package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
)

const (
	RepositoryPackageHelperPath = "/usr/libexec/printmaster-agent-update"
	packageHelperConfigPath     = "/etc/printmaster/update-helper.json"
	packageHelperRootPath       = "/etc/printmaster/update-publisher-root.json"
	packageHelperPolicyPath     = "/etc/printmaster/update-policy.json"
	packageHelperStateDir       = "/var/lib/printmaster-update"
)

// RunRepositoryPackageHelper is the sole installed-command entrypoint. It
// accepts no flags, paths or environment-based configuration. Public root
// provisioning is an explicit administrator operation, independent of Server.
func RunRepositoryPackageHelper(ctx context.Context, args []string, input, output *os.File) error {
	if len(args) != 0 {
		return fmt.Errorf("native package helper accepts no arguments")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("native package helper requires root")
	}
	data, err := readRootPackageConfiguration(packageHelperConfigPath, 4096)
	if err != nil {
		return err
	}
	metadataURL, err := decodePackageHelperConfiguration(data)
	if err != nil {
		return err
	}
	root, err := readRootPackageConfiguration(packageHelperRootPath, 1024*1024)
	if err != nil {
		return err
	}
	return ServePrivilegedRepositoryPackageResponse(ctx, root, metadataURL, packageHelperStateDir, packageHelperPolicyPath, input, output)
}

func decodePackageHelperConfiguration(data []byte) (string, error) {
	// Canonical field matching also rejects case-folded aliases accepted by
	// encoding/json. Whitespace is permitted in administrative configuration.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 2 ||
		fields["schema"] == nil || fields["metadata_url"] == nil {
		return "", fmt.Errorf("native helper configuration requires only schema and metadata_url")
	}
	var config struct {
		Schema      int    `json:"schema"`
		MetadataURL string `json:"metadata_url"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return "", fmt.Errorf("decode native helper configuration: %w", err)
	}
	// Reject duplicate keys, including escaped spellings.
	keys := json.NewDecoder(bytes.NewReader(data))
	if _, err := keys.Token(); err != nil {
		return "", err
	}
	seen := make(map[string]bool)
	for keys.More() {
		token, err := keys.Token()
		if err != nil {
			return "", err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return "", fmt.Errorf("native helper configuration contains duplicate fields")
		}
		seen[key] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return "", err
		}
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", fmt.Errorf("native helper configuration contains trailing data")
	}
	u, err := url.Parse(config.MetadataURL)
	if err != nil || config.Schema != 1 || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("native helper configuration requires schema 1 and an HTTPS metadata URL without credentials, query or fragment")
	}
	return config.MetadataURL, nil
}
