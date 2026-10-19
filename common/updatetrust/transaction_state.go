package updatetrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkPublicationHighWater(workspace, records string, allowedPins ...string) error {
	entries, err := os.ReadDir(records)
	if err != nil {
		return err
	}
	var latest publicationRecord
	versions := make(map[int64]string)
	for _, entry := range entries {
		if entry.Name() == ".publisher.lock" {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("unexpected publication record file %s", entry.Name())
		}
		data, err := readPublisherMetadata(filepath.Join(records, entry.Name()))
		if err != nil {
			return err
		}
		var record publicationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.TimestampVersion <= 0 || entry.Name() != record.TimestampSHA256+".json" ||
			len(record.TimestampSHA256) != 64 {
			return fmt.Errorf("invalid publication high-water record")
		}
		if previous, ok := versions[record.TimestampVersion]; ok && record.TimestampSHA256 != previous {
			return fmt.Errorf("conflicting publication records at the same timestamp version")
		}
		versions[record.TimestampVersion] = record.TimestampSHA256
		if record.TimestampVersion > latest.TimestampVersion {
			latest = record
		}
	}
	if latest.TimestampVersion == 0 {
		return nil
	}
	if len(allowedPins) != 0 {
		for _, pin := range allowedPins {
			if latest.TimestampSHA256 == pin {
				return nil
			}
		}
		return fmt.Errorf("pending publication conflicts with latest retained record")
	}
	current, err := optionalTimestamp(workspace)
	if err != nil {
		return err
	}
	if current != latest.TimestampSHA256 {
		return fmt.Errorf("workspace does not match latest retained publication record; resume pending work or use authorized recovery")
	}
	return nil
}
