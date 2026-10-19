package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"printmaster/common/updatetrust"
)

// ErrPublisherTrust is an authorization failure, not a Server connectivity error.
var ErrPublisherTrust = errors.New("update publisher authorization failed")

func (m *Manager) authorizeManifest(ctx context.Context, manifest *UpdateManifest) error {
	_, err := m.publisherAuthorization(ctx, manifest)
	return err
}

func (m *Manager) manifestTarget(ctx context.Context, manifest *UpdateManifest) updatetrust.Target {
	return updatetrust.Target{
		Component: manifest.Component, Channel: m.operationChannel(ctx), Version: manifest.Version,
		Platform: manifest.Platform, Arch: manifest.Arch, Format: m.artifactFormat().String(),
		SHA256: strings.ToLower(manifest.SHA256), SizeBytes: manifest.SizeBytes,
	}
}

func (m *Manager) publisherAuthorization(ctx context.Context, manifest *UpdateManifest) (updatetrust.Authorization, error) {
	if m.publisherVerifier == nil {
		return updatetrust.Authorization{}, fmt.Errorf("%w: pinned publisher trust has not been provisioned; update blocked", ErrPublisherTrust)
	}
	if manifest == nil {
		return updatetrust.Authorization{}, fmt.Errorf("%w: missing candidate", ErrPublisherTrust)
	}
	if err := validateManifestFormat(manifest, m.artifactFormat()); err != nil {
		return updatetrust.Authorization{}, fmt.Errorf("%w: %w", ErrPublisherTrust, err)
	}
	channel := m.operationChannel(ctx)
	if !isKnownChannel(channel) || manifest.Component != "agent" || manifest.Platform != m.platform ||
		manifest.Arch != m.arch || manifest.Channel != channel {
		return updatetrust.Authorization{}, fmt.Errorf("%w: candidate identity does not match agent/%s/%s/%s",
			ErrPublisherTrust, channel, m.platform, m.arch)
	}
	target := m.manifestTarget(ctx, manifest)
	authorization, err := m.publisherVerifier.Authorize(ctx, target)
	if err != nil {
		return updatetrust.Authorization{}, fmt.Errorf("%w: %w", ErrPublisherTrust, err)
	}
	if authorization.Target != target {
		return updatetrust.Authorization{}, fmt.Errorf("%w: publisher returned a different installation identity", ErrPublisherTrust)
	}
	m.logDebug("Update publisher authorization succeeded", "version", target.Version,
		"channel", target.Channel, "platform", target.Platform, "arch", target.Arch, "format", target.Format)
	return authorization, nil
}
