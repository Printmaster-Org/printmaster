package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

// RepositoryPackagePolicy is a local administrative ceiling, not a replacement
// for the Agent's fleet policy or publisher authorization. Missing enablement
// fails closed. This file contains no trust root, URL, commands or state paths.
type RepositoryPackagePolicy struct {
	Schema             int                             `json:"schema"`
	Enabled            bool                            `json:"enabled"`
	VersionPinStrategy updatepolicy.VersionPinStrategy `json:"version_pin_strategy"`
	AllowMajorUpgrade  bool                            `json:"allow_major_upgrade"`
	TargetVersion      string                          `json:"target_version"`
	EmergencyAbort     bool                            `json:"emergency_abort"`
}

// InstallPrivilegedRepositoryPackageFromPolicyFile rereads the root-owned
// administrative policy at the final installation boundary. All configuration
// paths/authority come from trusted helper code, never the service request.
// There is still no CLI, sudo permission or automatic-update wiring.
func InstallPrivilegedRepositoryPackageFromPolicyFile(ctx context.Context, root []byte, metadataURL, stateDir, policyPath string,
	source *os.File, target updatetrust.Target) error {
	return InstallPrivilegedRepositoryPackage(ctx, root, metadataURL, stateDir, source, target,
		func() (updatepolicy.PolicySpec, error) { return readRootPackagePolicy(policyPath) })
}

func readRootPackagePolicy(path string) (spec updatepolicy.PolicySpec, result error) {
	data, err := readRootPackageConfiguration(path, 4096)
	if err != nil {
		return spec, err
	}
	return decodeRootPackagePolicy(data)
}

func readRootPackageConfiguration(path string, limit int64) (data []byte, result error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("native package configuration access requires root")
	}
	return readProtectedPackageFile(path, limit)
}

func readProtectedPackageFile(path string, limit int64) (data []byte, result error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("native package configuration requires a clean absolute path")
	}
	if err := checkRootPackageAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// NONBLOCK prevents a substituted FIFO from blocking inspection; NOFOLLOW
	// rejects final symlinks, and descriptor checks bind ownership to the read.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open root package configuration: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { result = errors.Join(result, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0o133 != 0 {
		return nil, fmt.Errorf("native package configuration must be an ordinary root-owned non-executable file without group/other write access")
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read root package configuration: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("native package configuration exceeds %d bytes", limit)
	}
	return data, nil
}

func decodeRootPackagePolicy(data []byte) (updatepolicy.PolicySpec, error) {
	reject := func(message string) (updatepolicy.PolicySpec, error) {
		return updatepolicy.PolicySpec{}, fmt.Errorf("%w: %s", ErrApplicationPolicy, message)
	}
	// Flat schema: reject duplicate keys and null values before typed decoding.
	keys := json.NewDecoder(bytes.NewReader(data))
	token, err := keys.Token()
	if err != nil || token != json.Delim('{') {
		return reject("native package policy must be a JSON object")
	}
	seen := make(map[string]bool)
	for keys.More() {
		token, err := keys.Token()
		if err != nil {
			return reject("malformed native package policy key")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return reject("duplicate native package policy key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return reject("invalid or null native package policy value")
		}
	}
	var policy RepositoryPackagePolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return reject("invalid native package policy fields: " + err.Error())
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return reject("native package policy has trailing data")
	}
	if policy.Schema != 1 || !policy.Enabled || !seen["enabled"] {
		return reject("native package policy requires schema 1 and explicit enablement")
	}
	switch policy.VersionPinStrategy {
	case "", updatepolicy.VersionPinMajor, updatepolicy.VersionPinMinor, updatepolicy.VersionPinPatch:
	default:
		return reject("native package policy version pin strategy is invalid")
	}
	if policy.TargetVersion != "" && !stablePackageVersion.MatchString(policy.TargetVersion) {
		return reject("native package policy target must be a canonical Stable version")
	}
	if policy.EmergencyAbort {
		return reject("native package policy emergency abort is enabled")
	}
	return updatepolicy.PolicySpec{VersionPinStrategy: policy.VersionPinStrategy, AllowMajorUpgrade: policy.AllowMajorUpgrade,
		TargetVersion: policy.TargetVersion}, nil
}
