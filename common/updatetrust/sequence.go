package updatetrust

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type releaseCustom struct {
	ReleaseSequence int64 `json:"printmaster_release_sequence"`
}

func releaseSequence(info *metadata.TargetFiles) (int64, error) {
	if info.Custom == nil {
		return 0, nil // Legacy targets have no authenticated application ordering.
	}
	var custom releaseCustom
	if err := json.Unmarshal(*info.Custom, &custom); err != nil || custom.ReleaseSequence < 0 {
		return 0, fmt.Errorf("invalid publisher release sequence")
	}
	return custom.ReleaseSequence, nil
}

func assignReleaseSequence(targets map[string]*metadata.TargetFiles, path string, info *metadata.TargetFiles) error {
	if old := targets[path]; old != nil {
		// Re-publishing identical bytes must not make an old release newer.
		info.Custom = old.Custom
		return nil
	}
	parts := strings.Split(path, "/")
	var highest int64
	for name, target := range targets {
		other := strings.Split(name, "/")
		if len(other) != 6 || other[0] != parts[0] || other[1] != parts[1] ||
			other[3] != parts[3] || other[4] != parts[4] || other[5] != parts[5] {
			continue
		}
		sequence, err := releaseSequence(target)
		if err != nil {
			return fmt.Errorf("read release ordering for %s: %w", name, err)
		}
		if sequence > highest {
			highest = sequence
		}
	}
	if highest == math.MaxInt64 {
		return fmt.Errorf("publisher release sequence exhausted")
	}
	data, err := json.Marshal(releaseCustom{ReleaseSequence: highest + 1})
	if err != nil {
		return err
	}
	custom := json.RawMessage(data)
	info.Custom = &custom
	return nil
}
