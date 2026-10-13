package vendor

import (
	"bytes"
	"testing"
)

func TestParseST2Response_Basic(t *testing.T) {
	t.Parallel()

	// Minimal valid ST2 frame with just status field
	// Header: \x00@BDC ST2\r\n, Length: 3 (little-endian), Field: type=0x01, len=1, data=0x04 (Idle)
	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}
	frame = append(frame, 0x03, 0x00)       // Length = 3
	frame = append(frame, 0x01, 0x01, 0x04) // Status field: type=1, len=1, value=4 (Idle)

	status, err := ParseST2Response(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.StatusCode != 4 {
		t.Errorf("expected StatusCode=4, got %d", status.StatusCode)
	}
	if status.StatusText != "Idle" {
		t.Errorf("expected StatusText='Idle', got '%s'", status.StatusText)
	}
	if !status.Ready {
		t.Error("expected Ready=true for Idle status")
	}
}

func TestParseST2Response_InkLevels(t *testing.T) {
	t.Parallel()

	// Frame with ink info field
	// Ink field format: type=0x0f, len=N, data=<colorlen><colour><ink_color><level>...
	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}

	// Ink field: 4 colors, each 3 bytes (colorlen=3)
	// Black(0x01)=85%, Cyan(0x03)=72%, Magenta(0x04)=68%, Yellow(0x05)=90%
	inkData := []byte{
		0x03,           // colorlen = 3
		0x01, 0x00, 85, // Black, level 85
		0x03, 0x01, 72, // Cyan, level 72
		0x04, 0x02, 68, // Magenta, level 68
		0x05, 0x03, 90, // Yellow, level 90
	}

	// Payload = status TLV (3 bytes) + ink TLV (2 header + inkData)
	payloadLen := 3 + 2 + len(inkData)
	frame = append(frame, byte(payloadLen), 0x00)   // Length (little-endian)
	frame = append(frame, 0x01, 0x01, 0x04)         // Status = Idle (type=0x01, len=1, data=0x04)
	frame = append(frame, 0x0f, byte(len(inkData))) // Ink field header
	frame = append(frame, inkData...)

	status, err := ParseST2Response(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]int{
		"Black":   85,
		"Cyan":    72,
		"Magenta": 68,
		"Yellow":  90,
	}

	for color, expectedLevel := range expected {
		if level, ok := status.InkLevels[color]; !ok {
			t.Errorf("missing ink level for %s", color)
		} else if level != expectedLevel {
			t.Errorf("expected %s=%d, got %d", color, expectedLevel, level)
		}
	}
}

func TestParseST2Response_MaintenanceBox(t *testing.T) {
	t.Parallel()

	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}

	// Maintenance box field: 2 boxes, 1 byte each
	// Box 1: OK (0), Box 2: Near Full (1)
	maintData := []byte{0x01, 0x00, 0x01} // num_bytes=1, box1=0, box2=1

	payloadLen := 3 + 2 + len(maintData)
	frame = append(frame, byte(payloadLen), 0x00)
	frame = append(frame, 0x01, 0x01, 0x04)           // Status = Idle
	frame = append(frame, 0x37, byte(len(maintData))) // Maintenance box field
	frame = append(frame, maintData...)

	status, err := ParseST2Response(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.MaintenanceBoxes[1] != "OK" {
		t.Errorf("expected box 1 = OK, got %s", status.MaintenanceBoxes[1])
	}
	if status.MaintenanceBoxes[2] != "Near Full" {
		t.Errorf("expected box 2 = Near Full, got %s", status.MaintenanceBoxes[2])
	}
}

func TestParseST2Response_ErrorStatus(t *testing.T) {
	t.Parallel()

	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}

	// Status=Error (0x00), Error code=Ink out (0x05)
	payloadLen := 6
	frame = append(frame, byte(payloadLen), 0x00)
	frame = append(frame, 0x01, 0x01, 0x00) // Status = Error
	frame = append(frame, 0x02, 0x01, 0x05) // Error = Ink out

	status, err := ParseST2Response(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.StatusText != "Error" {
		t.Errorf("expected StatusText='Error', got '%s'", status.StatusText)
	}
	if status.Ready {
		t.Error("expected Ready=false for Error status")
	}
	if status.ErrorCode != "Ink out" {
		t.Errorf("expected ErrorCode='Ink out', got '%s'", status.ErrorCode)
	}
}

func TestParseST2Response_ToMetrics(t *testing.T) {
	t.Parallel()

	status := &ST2Status{
		StatusCode:       4,
		StatusText:       "Idle",
		Ready:            true,
		InkLevels:        map[string]int{"Black": 50, "Light Cyan": 75},
		MaintenanceBoxes: map[int]string{1: "OK", 2: "Near Full"},
	}

	metrics := status.ToMetrics()

	if metrics["epson_status"] != "Idle" {
		t.Errorf("expected epson_status='Idle', got %v", metrics["epson_status"])
	}
	if metrics["epson_ready"] != true {
		t.Errorf("expected epson_ready=true")
	}
	if metrics["ink_black"] != float64(50) {
		t.Errorf("expected ink_black=50, got %v", metrics["ink_black"])
	}
	if metrics["ink_light_cyan"] != float64(75) {
		t.Errorf("expected ink_light_cyan=75, got %v", metrics["ink_light_cyan"])
	}
	if metrics["maintenance_box_1"] != "OK" {
		t.Errorf("expected maintenance_box_1='OK', got %v", metrics["maintenance_box_1"])
	}
	if metrics["waste_box_2_status"] != 80 {
		t.Errorf("expected waste_box_2_status=80, got %v", metrics["waste_box_2_status"])
	}
}

func TestParseST2Response_InvalidHeader(t *testing.T) {
	t.Parallel()

	// Missing BDC ST2 header
	frame := []byte{0x00, 0x01, 0x02, 0x03}
	_, err := ParseST2Response(frame)
	if err == nil {
		t.Error("expected error for invalid header")
	}
}

func TestParseST2Response_TooShort(t *testing.T) {
	t.Parallel()

	frame := []byte{0x00, '@', 'B', 'D', 'C'}
	_, err := ParseST2Response(frame)
	if err == nil {
		t.Error("expected error for truncated frame")
	}
}

func TestFindBytes(t *testing.T) {
	t.Parallel()

	haystack := []byte("hello world")

	if idx := findBytes(haystack, []byte("world")); idx != 6 {
		t.Errorf("expected 6, got %d", idx)
	}
	if idx := findBytes(haystack, []byte("missing")); idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
	if idx := findBytes(haystack, []byte("hello")); idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}

func TestParseST2Response_UnalignedHeader(t *testing.T) {
	t.Parallel()

	// Sometimes the response has garbage before the header
	garbage := []byte{0xFF, 0xFE, 0x00, 0x00}
	frame := []byte{'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}
	frame = append(frame, 0x03, 0x00)       // Length = 3
	frame = append(frame, 0x01, 0x01, 0x03) // Status = Waiting

	fullFrame := append(garbage, frame...)

	status, err := ParseST2Response(fullFrame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if status.StatusText != "Standby" {
		t.Errorf("expected StatusText='Standby', got '%s'", status.StatusText)
	}
}

// Test with a realistic frame similar to what epson_print_conf.py handles
func TestParseST2Response_Realistic(t *testing.T) {
	t.Parallel()

	// Build a realistic frame with multiple fields
	var payload bytes.Buffer

	// Status: Idle (0x04)
	payload.Write([]byte{0x01, 0x01, 0x04})

	// No error
	payload.Write([]byte{0x02, 0x01, 0x00})

	// Ink levels: CMYK at various levels
	inkField := []byte{
		0x03,           // colorlen
		0x01, 0x00, 45, // Black 45%
		0x03, 0x01, 62, // Cyan 62%
		0x04, 0x02, 78, // Magenta 78%
		0x05, 0x03, 33, // Yellow 33%
	}
	payload.WriteByte(0x0f)
	payload.WriteByte(byte(len(inkField)))
	payload.Write(inkField)

	// Maintenance box: OK
	payload.Write([]byte{0x37, 0x02, 0x01, 0x00})

	// Build full frame
	frame := []byte{0x00, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n'}
	payloadBytes := payload.Bytes()
	frame = append(frame, byte(len(payloadBytes)), byte(len(payloadBytes)>>8))
	frame = append(frame, payloadBytes...)

	status, err := ParseST2Response(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all parsed correctly
	if status.StatusText != "Idle" {
		t.Errorf("status: expected 'Idle', got '%s'", status.StatusText)
	}
	if !status.Ready {
		t.Error("expected Ready=true")
	}
	if status.ErrorCode != "" {
		t.Errorf("expected no error, got '%s'", status.ErrorCode)
	}
	if status.InkLevels["Black"] != 45 {
		t.Errorf("Black: expected 45, got %d", status.InkLevels["Black"])
	}
	if status.InkLevels["Cyan"] != 62 {
		t.Errorf("Cyan: expected 62, got %d", status.InkLevels["Cyan"])
	}
	if status.InkLevels["Magenta"] != 78 {
		t.Errorf("Magenta: expected 78, got %d", status.InkLevels["Magenta"])
	}
	if status.InkLevels["Yellow"] != 33 {
		t.Errorf("Yellow: expected 33, got %d", status.InkLevels["Yellow"])
	}
	if status.MaintenanceBoxes[1] != "OK" {
		t.Errorf("maintenance box 1: expected 'OK', got '%s'", status.MaintenanceBoxes[1])
	}

	// Test metrics conversion
	metrics := status.ToMetrics()
	if metrics["ink_black"] != float64(45) {
		t.Errorf("ink_black metric: expected 45, got %v", metrics["ink_black"])
	}
}
