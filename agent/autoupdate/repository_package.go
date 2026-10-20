package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	return m.withRepositoryPackage(ctx, manifest, run, func(*os.File, updatetrust.Authorization) error { return nil })
}

// withRepositoryPackage keeps the authenticated snapshot alive through a
// synchronous handoff. The consumer must not retain the file after returning;
// the privileged receiver still needs its own independent authorization.
func (m *Manager) withRepositoryPackage(ctx context.Context, manifest *UpdateManifest, run repositoryCommand,
	consume func(*os.File, updatetrust.Authorization) error) (authorization updatetrust.Authorization, result error) {
	if consume == nil {
		return authorization, fmt.Errorf("repository package handoff requires a consumer")
	}
	verifier, ok := m.publisherVerifier.(packageFileVerifier)
	if !ok {
		return authorization, fmt.Errorf("%w: exact package-file verification is unavailable", ErrPublisherTrust)
	}
	if manifest == nil || m.platform != "linux" || m.operationChannel(ctx) != "stable" ||
		manifest.Channel != "stable" || m.packageName != "printmaster-agent" ||
		!stablePackageVersion.MatchString(manifest.Version) {
		return authorization, fmt.Errorf("%w: repository acquisition requires a Stable Linux printmaster-agent release", ErrPublisherTrust)
	}
	format, tool := "deb", "apt-get"
	if m.packageManager == "dnf" || m.packageManager == "yum" {
		format = "rpm"
	}
	arch, err := nativePackageArch(m.arch, format)
	if err != nil {
		return authorization, err
	}
	args := []string{"download", m.packageName + ":" + arch + "=" + manifest.Version}
	if m.packageManager == "dnf" || m.packageManager == "yum" {
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
	version, err := nativePackageFields(output, updatetrust.Target{Arch: m.arch, Format: format}, false)
	if err != nil {
		return authorization, err
	}
	if version != manifest.Version {
		return authorization, fmt.Errorf("%w: native package version mismatch", ErrPublisherTrust)
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
	target, err := packageTargetFromFile(ctx, file, updatetrust.Target{Component: "agent", Channel: "stable", Version: manifest.Version,
		Platform: "linux", Arch: m.arch, Format: format})
	if err != nil {
		return authorization, err
	}
	err = withAuthenticatedPackage(ctx, dir, file, target, verifier, func(snapshot string, verified updatetrust.Authorization) (result error) {
		input, err := os.Open(snapshot)
		if err != nil {
			return fmt.Errorf("open authenticated repository package for handoff: %w", err)
		}
		defer func() {
			result = errors.Join(result, input.Close())
		}()
		if err := consume(input, verified); err != nil {
			return fmt.Errorf("handoff authenticated repository package: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		authorization = verified
		return nil
	})
	if err != nil {
		return updatetrust.Authorization{}, err
	}
	return authorization, nil
}
