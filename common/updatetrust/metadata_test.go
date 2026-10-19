package updatetrust

import "testing"

func TestMetadataNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"timestamp.json", "1.root.json", "123.targets.json", "9223372036854775807.snapshot.json"} {
		if !ValidMetadataName(name) {
			t.Errorf("valid name rejected: %s", name)
		}
	}
	for _, name := range []string{
		"", "root.json", "targets.json", "snapshot.json", "0.root.json", "01.root.json",
		"-1.root.json", "9223372036854775808.root.json", "1.timestamp.json", "1.delegated.json",
		"../timestamp.json", `..\timestamp.json`, "nested/timestamp.json", "timestamp.json?token=secret", "targets.pem",
	} {
		if ValidMetadataName(name) {
			t.Errorf("unsafe name accepted: %s", name)
		}
	}
}
