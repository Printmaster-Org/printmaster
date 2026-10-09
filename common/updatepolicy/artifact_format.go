package updatepolicy

import (
	"path"
	"strings"
)

// ArtifactFormat identifies the payload shape of a published release asset.
//
// A single release publishes several assets for the same component, version,
// platform, and architecture (for example a raw Windows executable and an MSI,
// or a raw Linux binary plus .deb/.rpm packages). The format is therefore part
// of an artifact's identity: the Server caches, signs, and serves each format
// separately, and Agents request the format that matches how they were
// installed. Without it, assets overwrite each other and an Agent can receive
// a payload its installer path cannot apply.
type ArtifactFormat string

const (
	// ArtifactFormatBinary is a standalone executable that replaces itself in place.
	ArtifactFormatBinary ArtifactFormat = "binary"
	// ArtifactFormatMSI is a Windows Installer package applied with msiexec.
	ArtifactFormatMSI ArtifactFormat = "msi"
	// ArtifactFormatDeb is a Debian package. Delivered by the APT repository, never by fleet updates.
	ArtifactFormatDeb ArtifactFormat = "deb"
	// ArtifactFormatRPM is an RPM package. Delivered by the DNF/YUM repository, never by fleet updates.
	ArtifactFormatRPM ArtifactFormat = "rpm"
)

// ParseArtifactFormat normalizes a wire value. An empty value means binary so
// requests from Agents and Servers that predate format negotiation keep their
// original meaning. Unknown values are rejected rather than guessed.
func ParseArtifactFormat(raw string) (ArtifactFormat, bool) {
	switch ArtifactFormat(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ArtifactFormatBinary:
		return ArtifactFormatBinary, true
	case ArtifactFormatMSI:
		return ArtifactFormatMSI, true
	case ArtifactFormatDeb:
		return ArtifactFormatDeb, true
	case ArtifactFormatRPM:
		return ArtifactFormatRPM, true
	default:
		return "", false
	}
}

// ArtifactFormatFromFilename classifies an asset by its file extension. It is
// the single source of truth used by release intake and by the storage
// migration that reclassifies artifacts cached before formats existed. It
// accepts bare names, filesystem paths, and URLs.
func ArtifactFormatFromFilename(name string) ArtifactFormat {
	name = strings.TrimSpace(name)
	if idx := strings.IndexAny(name, "?#"); idx >= 0 {
		name = name[:idx]
	}
	name = strings.ReplaceAll(name, "\\", "/")
	switch strings.ToLower(path.Ext(path.Base(name))) {
	case ".msi":
		return ArtifactFormatMSI
	case ".deb":
		return ArtifactFormatDeb
	case ".rpm":
		return ArtifactFormatRPM
	default:
		return ArtifactFormatBinary
	}
}

// FleetDeliverable reports whether the Server distributes this format through
// signed fleet update manifests. OS packages are excluded because the APT and
// DNF/YUM repositories own their delivery, signing, and dependency handling.
func (f ArtifactFormat) FleetDeliverable() bool {
	return f == ArtifactFormatBinary || f == ArtifactFormatMSI
}

// String returns the wire value.
func (f ArtifactFormat) String() string {
	return string(f)
}
