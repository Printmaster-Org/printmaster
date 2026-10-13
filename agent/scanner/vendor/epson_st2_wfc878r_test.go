package vendor

import (
	"reflect"
	"testing"
)

// Real epPrtDeviceStatus frame captured from an Epson WF-C878R (idle, 4 inks).
const wfC878RST2 = "@BDC ST2\r\nu\x00\x01\x01\x04\x06\x02\x01\x01\n\x03\x11\x00\x04\r\x010\x0e\x01\x0f\x0f\r\x03\x05\x03:\x04\x02=\x03\x01:\x01\x000\x10\x0f\x01\bN\x02\x19N\x042N\x062N\x0f2N\x13\x01\x01\x19\f\x00\x00\x00\x00\x00unknown$\x02\x00\x006\x14\xff\xff\xff\xff\xff\xff\xff\xff\xa96\x00\x00\xd16\x00\x00\x11\x01\x00\x009\x01\x00@\nX6G8000214E\x04\x02\x02\x02\x02F\x01\x02"

func TestST2WFC878RFrame(t *testing.T) {
	s, err := ParseST2Response([]byte(wfC878RST2))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Yellow", "Magenta", "Cyan", "Black"}; !reflect.DeepEqual(s.InkOrder, want) {
		t.Fatalf("InkOrder = %v, want %v", s.InkOrder, want)
	}
	if want := []int{2, 2, 2, 2}; !reflect.DeepEqual(s.InkReplacements, want) {
		t.Fatalf("InkReplacements = %v, want %v", s.InkReplacements, want)
	}
	if s.SerialNumber != "X6G8000214" {
		t.Errorf("serial = %q", s.SerialNumber)
	}

	m := s.ToMetrics()
	want := map[string]interface{}{
		"ink_yellow":                   float64(58),
		"ink_magenta":                  float64(61),
		"ink_cyan":                     float64(58),
		"ink_black":                    float64(48),
		"yellow_ink_replacements":      2,
		"magenta_ink_replacements":     2,
		"cyan_ink_replacements":        2,
		"black_ink_replacements":       2,
		"maintenance_box_replacements": 2,
		"epson_status_code":            "04",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %#v, want %#v", k, m[k], v)
		}
	}
	if got := m["status_messages"]; !reflect.DeepEqual(got, []string{"Ready: Idle (Epson status 4)"}) {
		t.Errorf("status_messages = %v", got)
	}
}

func TestST2InkReplacementsRequireMatchingInkCount(t *testing.T) {
	s := &ST2Status{StatusCode: -1, InkOrder: []string{"Black", "Cyan"}, InkReplacements: []int{1, 2, 3}}
	for k := range s.ToMetrics() {
		if len(k) > len("_ink_replacements") && k[len(k)-len("_ink_replacements"):] == "_ink_replacements" {
			t.Fatalf("emitted %s despite ink/count mismatch", k)
		}
	}
}
