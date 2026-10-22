package updatetrust

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestClientTrustRecordRejectsAmbiguousAuthority(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	for _, record := range []string{
		`{"version":1,"version":1,"sha256":{"root.json":"` + hash + `"}}`,
		`{"Version":1,"sha256":{"root.json":"` + hash + `"}}`,
		`{"version":1,"sha256":{"root.json":"` + hash + `","root.json":"` + hash + `"}}`,
		`{"version":1,"sha256":{"root.json":"` + hash + `"},"verified_at":null,"verified_at":null}`,
		`{"version":1,"sha256":{"root.json":"` + hash + `"}} {}`,
		`{"version":1,"sha256":null}`,
		`{"version":1,"sha256":{"root.json":"` + hash + `","other.json":"` + hash + `"}}`,
	} {
		if _, err := decodeTrustState([]byte(record)); !errors.Is(err, ErrTrustState) {
			t.Fatalf("ambiguous record accepted: %s: %v", record, err)
		}
	}
	legacy, err := json.Marshal(map[string]any{"version": 1, "sha256": map[string]string{"root.json": hash}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTrustState(legacy); err != nil {
		t.Fatalf("legacy record rejected: %v", err)
	}
}
