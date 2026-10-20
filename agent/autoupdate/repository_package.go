package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"printmaster/common/updatetrust"
)

type packageFileVerifier interface {
	AuthorizeFile(context.Context, updatetrust.Target, *os.File) (updatetrust.Authorization, error)
}

type repositoryCommand func(context.Context, time.Duration, string, []string, string, ...string) ([]byte, error)

var stablePackageVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var packageRPMRelease = regexp.MustCompile(`^1(\.[A-Za-z0-9_]+)*$`)

// acquireRepositoryPackage only downloads and inspects. Neither repository
// resolution nor a successful authorization grants privileged install authority.
func (m *Manager) acquireRepositoryPackage(ctx context.Context, manifest *UpdateManifest, run repositoryCommand) (authorization updatetrust.Authorization, result error) {
	verifier, ok := m.publisherVerifier.(packageFileVerifier)
	if !ok {
		return authorization, fmt.Errorf("%w: exact package-file verification is unavailable", ErrPublisherTrust)
	}
	if manifest == nil || m.platform != "linux" || m.operationChannel(ctx) != "stable" ||
		manifest.Channel != "stable" || m.packageName != "printmaster-agent" ||
		!stablePackageVersion.MatchString(manifest.Version) {
		return authorization, fmt.Errorf("%w: repository acquisition requires a Stable Linux printmaster-agent release", ErrPublisherTrust)
	}
	var arch string
	switch m.arch {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	case "arm":
		arch = "armhf"
	default:
		return authorization, fmt.Errorf("%w: unsupported package architecture %q", ErrPublisherTrust, m.arch)
	}
	format, tool := "deb", "apt-get"
	args := []string{"download", m.packageName + ":" + arch + "=" + manifest.Version}
	if m.packageManager == "dnf" || m.packageManager == "yum" {
		format = "rpm"
		arch = map[string]string{"amd64": "x86_64", "arm64": "aarch64", "arm": "armv7hl"}[m.arch]
		tool = "dnf"
		if m.packageManager == "yum" {
			tool = "yumdownloader"
		}
		args = []string{"download", "--destdir=.", m.packageName + "-" + manifest.Version + "." + arch}
		if tool == "yumdownloader" {
			args = args[1:]
		}
	} else if m.packageManager != "apt" {
		return authorization, fmt.Errorf("%w: unsupported repository manager %q", ErrPublisherTrust, m.packageManager)
	}
	dir, err := os.MkdirTemp(filepath.Join(m.stateDir, downloadDirName), "repository-")
	if err != nil {
		return authorization, fmt.Errorf("create private repository workspace: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			authorization = updatetrust.Authorization{}
			result = errors.Join(result, fmt.Errorf("clean repository workspace: %w", err))
		}
	}()
	output, err := run(ctx, 10*time.Minute, dir, nil, tool, args...)
	if err != nil {
		return authorization, fmt.Errorf("download exact repository package (native download tooling required): %w: %s", err, output)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return authorization, err
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() || filepath.Ext(entries[0].Name()) != "."+format {
		return authorization, fmt.Errorf("%w: repository must return exactly one regular .%s package", ErrPublisherTrust, format)
	}
	path := filepath.Join(dir, entries[0].Name())
	if err := validateInstallPath(path); err != nil {
		return authorization, fmt.Errorf("unsafe repository artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return authorization, err
	}
	queryTool := "dpkg-deb"
	queryArgs := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", path}
	if format == "rpm" {
		queryTool = "rpm"
		queryArgs = []string{"-qp", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", path}
	}
	output, err = run(ctx, 30*time.Second, dir, nil, queryTool, queryArgs...)
	if err != nil {
		return authorization, fmt.Errorf("inspect native package identity: %w: %s", err, output)
	}
	fields := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n"), "\n")
	wantFields := 3
	if format == "rpm" {
		wantFields = 5
	}
	if len(fields) != wantFields || fields[0] != m.packageName || fields[1] != manifest.Version || fields[2] != arch {
		return authorization, fmt.Errorf("%w: native package name/version/architecture mismatch", ErrPublisherTrust)
	}
	if format == "rpm" && (fields[3] != "0" || !packageRPMRelease.MatchString(fields[4])) {
		return authorization, fmt.Errorf("%w: unexpected RPM epoch or release", ErrPublisherTrust)
	}
	if err := ctx.Err(); err != nil {
		return authorization, err
	}
	file, err := os.Open(path)
	if err != nil {
		return authorization, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			authorization = updatetrust.Authorization{}
			result = errors.Join(result, fmt.Errorf("close repository package: %w", err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return authorization, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return authorization, fmt.Errorf("%w: empty or nonregular package", ErrPublisherTrust)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, info.Size())); err != nil {
		return authorization, fmt.Errorf("hash repository package: %w", err)
	}
	target := updatetrust.Target{Component: "agent", Channel: "stable", Version: manifest.Version,
		Platform: "linux", Arch: m.arch, Format: format, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: info.Size()}
	authorization, err = verifier.AuthorizeFile(ctx, target, file)
	if err != nil {
		return updatetrust.Authorization{}, fmt.Errorf("%w: authenticate repository package: %w", ErrPublisherTrust, err)
	}
	if authorization.Target != target {
		return updatetrust.Authorization{}, fmt.Errorf("%w: verifier returned another package identity", ErrPublisherTrust)
	}
	return authorization, nil
}
