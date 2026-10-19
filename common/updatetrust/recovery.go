package updatetrust

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func verifyRootSuccessor(previous, next *metadata.Metadata[metadata.RootType]) error {
	if previous.Signed.Version == math.MaxInt64 || next.Signed.Version != previous.Signed.Version+1 {
		return fmt.Errorf("publisher transition requires the immediately following root version")
	}
	if err := previous.VerifyDelegate("root", next); err != nil {
		return fmt.Errorf("previous root authorization: %w", err)
	}
	if err := next.VerifyDelegate("root", next); err != nil {
		return fmt.Errorf("replacement root authorization: %w", err)
	}
	return nil
}

// Recovery requires complete, internally consistent root history. The owner's
// authenticated backup/fingerprint records establish historical provenance;
// checking a chain backwards from today's root cannot establish that provenance.
func verifyPublisherRootHistory(directory string, current *metadata.Metadata[metadata.RootType]) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if current.Signed.Version > int64(len(entries)) {
		return fmt.Errorf("incomplete versioned root history")
	}
	var previous *metadata.Metadata[metadata.RootType]
	for version := int64(1); version <= current.Signed.Version; version++ {
		data, err := readPublisherMetadata(filepath.Join(directory, fmt.Sprintf("%d.root.json", version)))
		if err != nil {
			return err
		}
		root, err := validateRootPolicy(data)
		if err != nil {
			return err
		}
		if root.Signed.Version != version {
			return fmt.Errorf("root history filename/version mismatch")
		}
		if previous != nil {
			if err := verifyRootSuccessor(previous, root); err != nil {
				return err
			}
		}
		previous = root
	}
	// Compare signed bodies, since a root may carry an additional authorized
	// old-root signature without changing the trusted keys or policy.
	want, err := json.Marshal(current.Signed)
	if err != nil {
		return err
	}
	got, err := json.Marshal(previous.Signed)
	if err != nil {
		return err
	}
	if string(got) != string(want) {
		return fmt.Errorf("root history does not match independently trusted current root")
	}
	return nil
}
