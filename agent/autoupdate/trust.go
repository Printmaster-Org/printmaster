package autoupdate

import (
	"context"
	"errors"
	"fmt"

	"printmaster/common/updatetrust"
)

// ErrPublisherTrust is an authorization failure, not a Server connectivity error.
var ErrPublisherTrust = errors.New("update publisher authorization failed")

func (m *Manager) authorizeManifest(ctx context.Context, manifest *UpdateManifest) error {
	if m.publisherVerifier == nil {
		return fmt.Errorf("%w: pinned publisher trust has not been provisioned; update blocked", ErrPublisherTrust)
	}
	if manifest == nil {
		return fmt.Errorf("%w: missing candidate", ErrPublisherTrust)
	}
	if err := validateManifestFormat(manifest, m.artifactFormat()); err != nil {
		return fmt.Errorf("%w: %w", ErrPublisherTrust, err)
	}
	channel := m.operationChannel(ctx)
	if manifest.Component != "agent" || manifest.Platform != m.platform ||
		manifest.Arch != m.arch || manifest.Channel != channel {
		return fmt.Errorf("%w: candidate identity does not match agent/%s/%s/%s",
			ErrPublisherTrust, channel, m.platform, m.arch)
	}
	target := updatetrust.Target{
		Component: "agent", Channel: channel, Version: manifest.Version,
		Platform: m.platform, Arch: m.arch, Format: m.artifactFormat().String(),
		SHA256: manifest.SHA256, SizeBytes: manifest.SizeBytes,
	}
	if err := m.publisherVerifier.Verify(ctx, target); err != nil {
		return fmt.Errorf("%w: %w", ErrPublisherTrust, err)
	}
	m.logDebug("Update publisher authorization succeeded", "version", target.Version,
		"channel", target.Channel, "platform", target.Platform, "arch", target.Arch, "format", target.Format)
	return nil
}
