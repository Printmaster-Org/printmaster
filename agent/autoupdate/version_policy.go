package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	"printmaster/common/updatepolicy"
)

var ErrApplicationPolicy = errors.New("update application policy rejected candidate")

type manualInstallKey struct{}

func isDevVersion(version *semver.Version) bool {
	return version.Prerelease() == "dev" || strings.HasPrefix(version.Prerelease(), "dev.")
}

func sameVersionLine(a, b *semver.Version) bool {
	return a.Major() == b.Major() && a.Minor() == b.Minor() && a.Patch() == b.Patch()
}

// applicationPolicy is shared by discovery and the final installation boundary.
// Manual installs bypass timing, never pins, publisher ordering or downgrades.
func (m *Manager) applicationPolicy(ctx context.Context, manifest *UpdateManifest, manual bool) (bool, error) {
	reject := func(message string) (bool, error) {
		return false, fmt.Errorf("%w: %s", ErrApplicationPolicy, message)
	}
	if manifest == nil || m.currentSemver == nil {
		return reject("installed version cannot be ordered; use an owner-managed installation to migrate")
	}
	target, err := parseSemverVersion(manifest.Version)
	if err != nil {
		return reject("candidate version cannot be ordered")
	}
	if m.policyProvider != nil {
		spec, _, _ := m.policyProvider.EffectivePolicy()
		if spec.TargetVersion != "" {
			if _, err := parseSemverVersion(spec.TargetVersion); err != nil {
				return reject("configured target-version pin is invalid; correct the update policy")
			}
		}
		switch spec.VersionPinStrategy {
		case "", updatepolicy.VersionPinMajor, updatepolicy.VersionPinMinor, updatepolicy.VersionPinPatch:
		default:
			return reject("configured version-pin strategy is invalid; correct the update policy")
		}
		if !m.isVersionAllowed(target, spec) {
			if manual {
				return reject("candidate is outside the configured version pin/upgrade policy")
			}
			return false, nil
		}
	}
	if isDevVersion(target) && isDevVersion(m.currentSemver) &&
		sameVersionLine(target, m.currentSemver) && manifest.Version != m.currentVersion {
		if manifest.Channel != channelDev || m.operationChannel(ctx) != channelDev {
			return reject("unordered Dev replacements require the Dev channel's authenticated ordering")
		}
		if m.publisherVerifier == nil {
			return reject("publisher-authenticated Dev release ordering is unavailable")
		}
		installed, err := fileIdentity(m.binaryPath)
		if err != nil {
			return false, fmt.Errorf("%w: inspect installed Dev artifact: %w", ErrApplicationPolicy, err)
		}
		currentTarget := m.manifestTarget(ctx, manifest)
		currentTarget.Channel, currentTarget.Version = channelDev, m.currentVersion
		currentTarget.SHA256, currentTarget.SizeBytes = installed.SHA256, installed.SizeBytes
		current, err := m.publisherVerifier.Authorize(ctx, currentTarget)
		if err != nil {
			return false, fmt.Errorf("%w: authenticate installed Dev baseline; owner-managed migration may be required: %w", ErrApplicationPolicy, err)
		}
		candidate, err := m.publisherVerifier.Authorize(ctx, m.manifestTarget(ctx, manifest))
		if err != nil {
			return false, fmt.Errorf("%w: authenticate Dev ordering: %w", ErrPublisherTrust, err)
		}
		if current.ReleaseSequence <= 0 || candidate.ReleaseSequence <= current.ReleaseSequence {
			return reject("Dev release sequence is missing or not newer than the authenticated installed build")
		}
		return true, nil
	}
	if target.LessThan(m.currentSemver) {
		return reject("application downgrade is forbidden, including Force and channel switches; only recorded failed-install recovery may restore the previous installation")
	}
	if target.Equal(m.currentSemver) {
		if !manual {
			return false, nil
		}
		if err := m.validateReinstall(ctx, manifest); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (m *Manager) validateReinstall(ctx context.Context, manifest *UpdateManifest) error {
	expected := ArtifactIdentity{SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes}
	if m.useMSI {
		receipt, err := m.readInstallationReceipt()
		if err != nil {
			return fmt.Errorf("%w: same-version MSI reinstall requires a recorded authenticated installation: %w", ErrApplicationPolicy, err)
		}
		if receipt.Authorization.Target != m.manifestTarget(ctx, manifest) {
			return fmt.Errorf("%w: reinstall does not match the recorded MSI identity", ErrApplicationPolicy)
		}
		return nil
	}
	if err := expected.verify(m.binaryPath); err != nil {
		return fmt.Errorf("%w: same-version reinstall must match installed bytes: %w", ErrApplicationPolicy, err)
	}
	return nil
}
