package updatetrust

import (
	"regexp"
	"strconv"
)

const MetadataRoute = "/api/v1/agents/update/metadata/"
const MaxMetadataBytes int64 = 16 * 1024 * 1024

var versionedMetadata = regexp.MustCompile(`^([1-9][0-9]*)\.(root|targets|snapshot)\.json$`)

// ValidMetadataName exposes only the top-level consistent-snapshot metadata
// consumed by this client, never targets, local state or publisher key files.
func ValidMetadataName(name string) bool {
	if name == "timestamp.json" {
		return true
	}
	match := versionedMetadata.FindStringSubmatch(name)
	if match == nil {
		return false
	}
	version, err := strconv.ParseInt(match[1], 10, 64)
	return err == nil && version > 0
}
