package autoupdate

import (
	"context"
	"errors"
	"fmt"

	"printmaster/common/updatepolicy"
)

// Release channels that the Agent understands. Stable is the only channel
// published to every install method.
const (
	channelStable = "stable"
	channelBeta   = "beta"
	channelDev    = "dev"
)

// errChannelUnsupported marks an explicit request for a channel that this
// install method cannot receive, so telemetry reports CHANNEL_UNSUPPORTED
// instead of a Server or apply failure.
var errChannelUnsupported = errors.New("update channel not available for this install method")

// stableOnlyInstallMethod names the install method when it can only follow
// Stable, or returns "" when every channel is deliverable.
//
// Beta and Dev releases publish raw binaries and Docker images only: the
// APT/DNF repositories carry Stable packages and CI builds MSIs for Stable
// tags only. Package-managed and MSI installs must stay on Stable so they
// never request a version their installer cannot obtain.
func (m *Manager) stableOnlyInstallMethod() string {
	switch {
	case m.usePackageManager && m.packageName != "":
		return m.packageManager + "-managed package"
	case m.useMSI:
		return "MSI"
	default:
		return ""
	}
}

// requestedChannel is the channel chosen by an explicit operation, the
// managed Fleet setting, or local configuration, in that order.
func (m *Manager) requestedChannel(ctx context.Context) string {
	if channel, _ := ctx.Value(manualUpdateChannelKey{}).(string); channel != "" {
		return channel
	}
	if m.channelProvider != nil {
		if channel := m.channelProvider(); isKnownChannel(channel) {
			return channel
		}
	}
	return m.channel
}

// operationChannel is the channel this install actually follows: the
// requested channel, constrained to Stable for Stable-only install methods.
func (m *Manager) operationChannel(ctx context.Context) string {
	if m.stableOnlyInstallMethod() != "" {
		return channelStable
	}
	return m.requestedChannel(ctx)
}

// channelNote explains why the followed channel differs from the requested
// one, for status consumers. It is empty when no constraint applies.
func (m *Manager) channelNote(ctx context.Context) string {
	method := m.stableOnlyInstallMethod()
	requested := m.requestedChannel(ctx)
	if method == "" || requested == channelStable || requested == "" {
		return ""
	}
	return fmt.Sprintf("%s installs only receive Stable releases; %s is selected but Stable is used. Reinstall with the raw binary to follow %s.",
		method, channelLabel(requested), channelLabel(requested))
}

// manifestContext prepares the request context for a manifest fetch.
// Stable-only installs pin the request to Stable as an explicit channel so a
// Fleet-managed Beta or Dev setting on the Server cannot override it.
func (m *Manager) manifestContext(ctx context.Context) context.Context {
	method := m.stableOnlyInstallMethod()
	if method == "" {
		return ctx
	}
	if requested := m.requestedChannel(ctx); requested != channelStable && requested != "" {
		m.logWarn("Install method only receives Stable releases; following Stable instead",
			"install_method", method, "requested_channel", requested)
	}
	return updatepolicy.WithExplicitChannel(ctx)
}

// validateExplicitChannel rejects an operator's explicit Beta or Dev request
// on a Stable-only install instead of silently installing Stable.
func (m *Manager) validateExplicitChannel(channel string) error {
	method := m.stableOnlyInstallMethod()
	if method == "" || channel == "" || channel == channelStable {
		return nil
	}
	return fmt.Errorf("%w: %s installs only receive Stable releases, so %s cannot be installed; reinstall with the raw binary to follow %s",
		errChannelUnsupported, method, channelLabel(channel), channelLabel(channel))
}

func isKnownChannel(channel string) bool {
	return channel == channelStable || channel == channelBeta || channel == channelDev
}

func channelLabel(channel string) string {
	switch channel {
	case channelStable:
		return "Stable"
	case channelBeta:
		return "Beta"
	case channelDev:
		return "Dev"
	default:
		return channel
	}
}
