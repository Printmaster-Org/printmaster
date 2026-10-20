package autoupdate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"printmaster/common/updatepolicy"
	"printmaster/common/updatetrust"
)

func nativePackageArch(goarch, format string) (string, error) {
	arches := map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "armhf"}
	if format == "rpm" {
		arches = map[string]string{"amd64": "x86_64", "arm64": "aarch64", "arm": "armv7hl"}
	} else if format != "deb" {
		return "", fmt.Errorf("%w: unsupported package format", ErrPublisherTrust)
	}
	arch, ok := arches[goarch]
	if !ok {
		return "", fmt.Errorf("%w: unsupported native package architecture", ErrPublisherTrust)
	}
	return arch, nil
}

func nativePackageFields(output []byte, target updatetrust.Target, installed bool) (string, error) {
	fields := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n"), "\n")
	arch, err := nativePackageArch(target.Arch, target.Format)
	if err != nil {
		return "", err
	}
	count := 3
	if target.Format == "rpm" {
		count = 5
	} else if installed {
		count = 4
	}
	if len(fields) != count || fields[0] != "printmaster-agent" || fields[2] != arch ||
		!stablePackageVersion.MatchString(fields[1]) {
		return "", fmt.Errorf("%w: unexpected native package name/version/architecture", ErrPublisherTrust)
	}
	if target.Format == "rpm" && (fields[3] != "0" || !packageRPMRelease.MatchString(fields[4])) {
		return "", fmt.Errorf("%w: unexpected native RPM epoch/release", ErrPublisherTrust)
	}
	if target.Format == "deb" && installed && fields[3] != "install ok installed" {
		return "", fmt.Errorf("%w: installed Debian package requires repair", ErrPublisherTrust)
	}
	return fields[1], nil
}

// consumeNativePackage is called only inside the root-owned snapshot lifetime.
// Policy comes from trusted helper configuration, never a service-user receipt.
// No dependency resolver, network refresh, downgrade or alternate package is used.
func consumeNativePackage(ctx context.Context, snapshot string, authorization updatetrust.Authorization,
	policy func() (updatepolicy.PolicySpec, error), run repositoryCommand) error {
	target := authorization.Target
	if policy == nil || target.Component != "agent" || target.Platform != "linux" || target.Channel != "stable" ||
		!stablePackageVersion.MatchString(target.Version) || !filepath.IsAbs(snapshot) ||
		filepath.Ext(snapshot) != "."+target.Format {
		return fmt.Errorf("%w: invalid native package installation request", ErrPublisherTrust)
	}
	if _, err := nativePackageArch(target.Arch, target.Format); err != nil {
		return err
	}
	dir := filepath.Dir(snapshot)
	query := func(installed bool) (string, error) {
		tool := "/usr/bin/dpkg-deb"
		args := []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", snapshot}
		if installed {
			tool = "/usr/bin/dpkg-query"
			args = []string{"--show", "--showformat=${Package}\n${Version}\n${Architecture}\n${Status}\n", "printmaster-agent"}
		}
		if target.Format == "rpm" {
			tool = "/usr/bin/rpm"
			args = []string{"-qp", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", snapshot}
			if installed {
				args = []string{"-q", "--queryformat", "%{NAME}\n%{VERSION}\n%{ARCH}\n%{EPOCHNUM}\n%{RELEASE}\n", "printmaster-agent"}
			}
		}
		output, err := run(ctx, 30*time.Second, dir, nil, tool, args...)
		if err != nil {
			return "", fmt.Errorf("inspect native package (installed=%t): %w: %s", installed, err, output)
		}
		return nativePackageFields(output, target, installed)
	}
	candidate, err := query(false)
	if err != nil {
		return err
	}
	if candidate != target.Version {
		return fmt.Errorf("%w: authenticated package version differs from native metadata", ErrPublisherTrust)
	}
	current, err := query(true)
	if err != nil {
		return err
	}
	spec, err := policy()
	if err != nil {
		return fmt.Errorf("read trusted package installation policy: %w", err)
	}
	if spec.RolloutControl.EmergencyAbort {
		return fmt.Errorf("%w: trusted policy has emergency abort enabled", ErrApplicationPolicy)
	}
	currentSemver, err := parseSemverVersion(current)
	if err != nil {
		return fmt.Errorf("%w: installed package version cannot be ordered", ErrApplicationPolicy)
	}
	if current == candidate {
		return fmt.Errorf("%w: same-version package reinstall requires owner-managed repair", ErrApplicationPolicy)
	}
	m := &Manager{currentVersion: current, currentSemver: currentSemver, channel: "stable",
		policyProvider: &nativePackagePolicy{spec: spec}}
	allowed, err := m.applicationPolicy(ctx, &UpdateManifest{Version: target.Version, Channel: "stable"}, true)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: package is not applicable", ErrApplicationPolicy)
	}
	// Recheck the protected snapshot immediately before crossing into package
	// scripts. Only root can alter it at this point.
	if err := (ArtifactIdentity{SHA256: target.SHA256, SizeBytes: target.SizeBytes}).verify(snapshot); err != nil {
		return fmt.Errorf("%w: package snapshot changed before installation: %w", ErrPublisherTrust, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tool, args := "/usr/bin/dpkg", []string{"--refuse-downgrade", "--install", "--", snapshot}
	if target.Format == "rpm" {
		tool, args = "/usr/bin/rpm", []string{"--upgrade", "--", snapshot}
	}
	output, err := run(ctx, 10*time.Minute, dir, nil, tool, args...)
	if err != nil {
		return fmt.Errorf("native package installation failed; inspect package database before recovery: %w: %s", err, output)
	}
	installed, err := query(true)
	if err != nil {
		return err
	}
	if installed != candidate {
		return fmt.Errorf("native installer returned success but installed version is %q, expected %q", installed, candidate)
	}
	return nil
}

type nativePackagePolicy struct{ spec updatepolicy.PolicySpec }

func (p *nativePackagePolicy) EffectivePolicy() (updatepolicy.PolicySpec, updatepolicy.PolicySource, bool) {
	return p.spec, updatepolicy.PolicySourceLocal, true
}
