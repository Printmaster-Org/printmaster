package vendor

import (
	"reflect"
	"testing"
)

func st2Frame(payload ...byte) []byte {
	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}
	frame = append(frame, byte(len(payload)), byte(len(payload)>>8))
	return append(frame, payload...)
}

func TestST2StatusMessages_EDACodes(t *testing.T) {
	t.Parallel()

	// Warnings appear before the ink list to verify order-independent decoding.
	frame := st2Frame(
		0x01, 0x01, 0x00, // status: Error
		0x02, 0x01, 0x04, // error: Paper jam
		0x04, 0x04, 0x12, 0x20, 0x53, 0x81, // ink low slot 2, maint box soon, cannot clean slot 2, ignored
		0x0f, 0x0a, 0x03, 0x01, 0x00, 40, 0x03, 0x01, 50, 0x04, 0x02, 5,
	)
	st, err := ParseST2Response(frame)
	if err != nil {
		t.Fatal(err)
	}
	if st.ErrorCode != "Paper jam" || st.ErrorNum != 4 {
		t.Fatalf("error = %q (%d)", st.ErrorCode, st.ErrorNum)
	}
	want := []string{
		"Error: Paper jam (Epson error 4)",
		"Warning: Ink low (Magenta) (Epson warning 18)",
		"Warning: Replace maintenance box soon (No. 1) (Epson warning 32)",
		"Warning: Cannot clean, ink low (Magenta) (Epson warning 83)",
	}
	if got := st.StatusMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages:\n got %q\nwant %q", got, want)
	}
	m := st.ToMetrics()
	if m["epson_status_code"] != "00" || m["epson_error_code"] != "4" {
		t.Fatalf("codes = %v / %v", m["epson_status_code"], m["epson_error_code"])
	}
}

func TestST2StatusMessages_IdleAndZeroErrorField(t *testing.T) {
	t.Parallel()

	st, err := ParseST2Response(st2Frame(0x01, 0x01, 0x04, 0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if st.ErrorCode != "" {
		t.Fatalf("idle frame with error byte 0 should not report an error, got %q", st.ErrorCode)
	}
	want := []string{"Ready: Idle (Epson status 4)"}
	if got := st.StatusMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestST2StatusMessages_FatalWhenErrorStatus(t *testing.T) {
	t.Parallel()

	st, err := ParseST2Response(st2Frame(0x01, 0x01, 0x00, 0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if st.ErrorCode != "Fatal error" {
		t.Fatalf("got %q", st.ErrorCode)
	}
}

func TestST2WarningTextWithoutInkList(t *testing.T) {
	t.Parallel()

	if got := st2WarningText(0x10, nil); got != "Ink low" {
		t.Fatalf("got %q", got)
	}
	if got := st2WarningText(0x91, []string{"Black", "Cyan"}); got != "Ink cartridge must be replaced (Black)" {
		t.Fatalf("got %q", got)
	}
}
