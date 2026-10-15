package metadata

import "testing"

func TestParseMetadata(t *testing.T) {
	for _, body := range []string{
		`{"serial":"S","serial_override":"bad"}`, `{"serial":"S","model":null}`,
		`{"serial":"S","dns_servers":"192.0.2.53"}`, `{"serial":"S","dns_servers":[1]}`,
		`{"serial":"S","ip":"not-ip"}`, `{"serial":"S","status_messages":[]}`, `{"serial":"S"}`,
	} {
		if _, _, err := Parse([]byte(body)); err == nil {
			t.Errorf("accepted invalid edit: %s", body)
		}
	}
	if _, patch, err := Parse([]byte(`{"serial":"S","location":"","dns_servers":[],"mac_address":"00:11:22:33:44:55"}`)); err != nil || len(patch) != 3 {
		t.Fatalf("valid blank/array edit: %v, %v", patch, err)
	}
}
