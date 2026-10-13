package vendor

import (
	"encoding/binary"
	"fmt"
	"strings"

	"printmaster/common/logger"
)

// ST2Status holds the parsed status from an Epson ST2 response frame.
type ST2Status struct {
	// Printer state. StatusCode is -1 when the frame has no status field.
	// ErrorNum is -1 when no error is reported; WarningNums holds raw warning
	// bytes (EDA-ignored informational codes removed).
	StatusCode    int
	StatusText    string
	Ready         bool
	ErrorCode     string
	ErrorNum      int
	WarningCodes  []string
	WarningNums   []int
	SelfPrintCode string

	// InkOrder lists ink names in ST2 cartridge order (used for warnings).
	InkOrder []string

	// Ink levels: map of color name -> level (0-100, -1 for unknown)
	InkLevels map[string]int

	// Maintenance box status: map of box number -> status string
	MaintenanceBoxes map[int]string

	// MaintenanceTankFree holds per-box remaining capacity (0-100 %) from
	// field 0x0d; -1 means unknown. TankIncompatible is set when the printer
	// reports a box previously used in an incompatible printer ('u').
	MaintenanceTankFree []int
	TankIncompatible    bool

	// MaintenanceBoxReplacements holds replacement counts from field 0x46.
	MaintenanceBoxReplacements []int
	// InkReplacements holds per-ink replacement counts from field 0x45, one
	// byte per ink in InkOrder (EDA "<Color>InkReplacements").
	InkReplacements []int

	// Paper counters from field 0x36; -1 when unsupported (0xFFFFFFFF).
	TotalPages int
	ColorPages int
	MonoPages  int

	SerialNumber string

	// CISS is true when field 0x28 reports a continuous ink supply system.
	CISS bool

	// Raw data for debugging
	RawFields map[int][]byte
}

// ST2 frame field types (from epson_print_conf.py status_parser)
const (
	st2FieldStatus           = 0x01 // Printer status code
	st2FieldError            = 0x02 // Error code
	st2FieldSelfPrint        = 0x03 // Self print code
	st2FieldWarning          = 0x04 // Warning codes
	st2FieldPaperPath        = 0x06 // Paper path info
	st2FieldPaperError       = 0x07 // Paper mismatch error
	st2FieldCleaningTime     = 0x0c // Cleaning time info
	st2FieldTanks            = 0x0d // Maintenance tanks
	st2FieldReplaceCartridge = 0x0e // Replace cartridge info
	st2FieldInkInfo          = 0x0f // Ink levels
	st2FieldLoadingPath      = 0x10 // Loading path info
	st2FieldCancelCode       = 0x13 // Cancel code
	st2FieldCutter           = 0x14 // Cutter info
	st2FieldTrayOpen         = 0x18 // Stacker/tray open status
	st2FieldJobName          = 0x19 // Current job name
	st2FieldTemperature      = 0x1c // Temperature info
	st2FieldSerial           = 0x1f // Serial number
	st2FieldPrinterInfo2     = 0x28 // Printer info 2 (CISS flag)
	st2FieldPaperJam         = 0x35 // Paper jam error
	st2FieldPaperCount       = 0x36 // Paper count info
	st2FieldMaintenanceBox   = 0x37 // Maintenance box status
	st2FieldInterfaceStatus  = 0x3d // Printer interface status
	st2FieldSerialInfo       = 0x40 // Serial number info
	st2FieldInkReplacement   = 0x45 // Ink replacement counter
	st2FieldMaintBoxReplace  = 0x46 // Maintenance box replacement counter
)

// Ink cartridge codes (first byte of each 0x0f entry), as interpreted by
// Epson Device Admin. Codes below 64 are named from this table only.
var inkColorIDs = map[byte]string{
	1:  "Black",
	3:  "Cyan",
	4:  "Magenta",
	5:  "Yellow",
	6:  "Light Cyan",
	7:  "Light Magenta",
	8:  "Light Yellow",
	9:  "Dark Yellow",
	10: "Light Black",
	11: "Matte Black",
	12: "Red",
	13: "Violet",
	14: "Gloss Optimizer",
	15: "Light Light Black",
	16: "Orange",
	17: "Green",
	18: "White",
	20: "Metallic Silver",
	22: "Middle Light Black",
	25: "Fluorescent Pink",
	26: "Fluorescent Yellow",
}

// All-in-one ink color table (second byte of each 0x0f entry), used by EDA
// only when the cartridge code is 64 or above.
var inkColorNames = map[byte]string{
	0:  "Black",
	1:  "Cyan",
	2:  "Magenta",
	3:  "Yellow",
	4:  "Light Cyan",
	5:  "Light Magenta",
	6:  "Light Yellow",
	7:  "Dark Yellow",
	8:  "Light Black",
	9:  "Red",
	10: "Violet",
	11: "Gloss Optimizer",
	15: "White",
	17: "Metallic Silver",
}

// st2InkName resolves an ink entry name using EDA's rule.
func st2InkName(code, allInOne byte) string {
	if code < 64 {
		if name, ok := inkColorIDs[code]; ok {
			return name
		}
		return fmt.Sprintf("Unknown %d", code)
	}
	if name, ok := inkColorNames[allInOne]; ok {
		return name
	}
	return fmt.Sprintf("Unknown %d", code)
}

// ParseST2Response parses an Epson ST2 status response frame.
// The response format is: \x00@BDC ST2\r\n<len_lo><len_hi><TLV data...>
// Returns parsed status or error if format is invalid.
func ParseST2Response(data []byte) (*ST2Status, error) {
	if len(data) < 16 {
		if logger.Global != nil {
			logger.Global.TraceTag("epson_st2", "ST2 parse: response too short", "len", len(data))
		}
		return nil, fmt.Errorf("ST2 response too short: %d bytes", len(data))
	}

	// Find the BDC ST2 header - it may not be at the start
	header := []byte("BDC ST2\r\n")
	headerIdx := findBytes(data, header)
	if headerIdx < 0 {
		if logger.Global != nil {
			logger.Global.TraceTag("epson_st2", "ST2 parse: header not found", "len", len(data))
		}
		return nil, fmt.Errorf("ST2 header not found (expected 'BDC ST2\\r\\n')")
	}

	// Skip to after header
	data = data[headerIdx+len(header):]
	if len(data) < 2 {
		if logger.Global != nil {
			logger.Global.TraceTag("epson_st2", "ST2 parse: truncated after header")
		}
		return nil, fmt.Errorf("ST2 response truncated after header")
	}

	// Read payload length (little-endian 16-bit)
	payloadLen := int(binary.LittleEndian.Uint16(data[:2]))
	data = data[2:]

	if len(data) < payloadLen {
		if logger.Global != nil {
			logger.Global.TraceTag("epson_st2", "ST2 parse: payload truncated", "expected", payloadLen, "got", len(data))
		}
		return nil, fmt.Errorf("ST2 payload truncated: expected %d, got %d", payloadLen, len(data))
	}

	if logger.Global != nil {
		logger.Global.TraceTag("epson_st2", "ST2 parse: payload", "payload_len", payloadLen)
	}

	// Trim to payload
	buf := data[:payloadLen]

	status := &ST2Status{
		StatusCode:       -1,
		ErrorNum:         -1,
		InkLevels:        make(map[string]int),
		MaintenanceBoxes: make(map[int]string),
		WarningCodes:     []string{},
		RawFields:        make(map[int][]byte),
		TotalPages:       -1,
		ColorPages:       -1,
		MonoPages:        -1,
	}

	// Error and warning text depend on fields that may appear later in the
	// frame (status byte, ink order), so they are decoded after the loop.
	errorByte := -1
	var warningBytes []byte

	// Parse TLV fields
	for len(buf) >= 2 {
		fieldType := buf[0]
		fieldLen := int(buf[1])
		buf = buf[2:]

		if len(buf) < fieldLen {
			break // Truncated field
		}

		item := buf[:fieldLen]
		buf = buf[fieldLen:]

		// Store raw for debugging
		status.RawFields[int(fieldType)] = item

		// Parse known fields
		switch fieldType {
		case st2FieldStatus:
			if len(item) >= 1 {
				status.StatusCode = int(item[0])
				status.StatusText = st2StatusText(item[0])
				status.Ready = st2ReadyStatuses[item[0]]
			}

		case st2FieldError:
			if len(item) >= 1 {
				errorByte = int(item[0])
			}

		case st2FieldWarning:
			warningBytes = append(warningBytes, item...)

		case st2FieldInkInfo:
			// Format: <colorlen> then repeating <colour><ink_color><level>
			if len(item) < 1 {
				continue
			}
			colorLen := int(item[0])
			if colorLen < 3 {
				colorLen = 3 // Minimum: colour, ink_color, level
			}
			offset := 1
			for offset+colorLen <= len(item) {
				colour := item[offset]
				inkColor := item[offset+1] // All-in-one color ID
				level := int(item[offset+2])

				colorName := st2InkName(colour, inkColor)

				// Normalize level (0-100 range, or -1 for unknown)
				if level > 100 {
					level = -1 // Unknown/invalid
				}

				status.InkLevels[colorName] = level
				status.InkOrder = append(status.InkOrder, colorName)
				offset += colorLen
			}

		case st2FieldMaintenanceBox:
			// Format: <num_bytes> then repeating status bytes
			if len(item) < 1 {
				continue
			}
			numBytes := int(item[0])
			if numBytes < 1 || numBytes > 2 {
				continue
			}
			boxNum := 1
			for i := 1; i < len(item); i += numBytes {
				boxStatus := item[i]
				var statusText string
				switch boxStatus {
				case 0:
					statusText = "OK"
				case 1:
					statusText = "Near Full"
				case 2:
					statusText = "Full"
				default:
					statusText = fmt.Sprintf("Unknown (%d)", boxStatus)
				}
				status.MaintenanceBoxes[boxNum] = statusText
				boxNum++
			}

		case st2FieldSelfPrint:
			if len(item) >= 1 && item[0] == 0 {
				status.SelfPrintCode = "Nozzle test printing"
			}

		case st2FieldTanks:
			// One byte per maintenance box: remaining capacity percent.
			for _, b := range item {
				switch {
				case b == 'u':
					status.TankIncompatible = true
					status.MaintenanceTankFree = append(status.MaintenanceTankFree, -1)
				case b <= 100:
					status.MaintenanceTankFree = append(status.MaintenanceTankFree, int(b))
				default:
					status.MaintenanceTankFree = append(status.MaintenanceTankFree, -1)
				}
			}

		case st2FieldPaperCount:
			// int32 little-endian slots: [4:8]=all, [8:12]=color, [12:16]=mono.
			read := func(off int) int {
				if len(item) < off+4 {
					return -1
				}
				v := binary.LittleEndian.Uint32(item[off : off+4])
				if v == 0xFFFFFFFF {
					return -1
				}
				return int(v)
			}
			status.TotalPages = read(4)
			status.ColorPages = read(8)
			status.MonoPages = read(12)

		case st2FieldSerialInfo:
			status.SerialNumber = strings.TrimRight(string(item), "\x00 ")

		case st2FieldMaintBoxReplace:
			for _, b := range item {
				status.MaintenanceBoxReplacements = append(status.MaintenanceBoxReplacements, int(b))
			}

		case st2FieldInkReplacement:
			for _, b := range item {
				status.InkReplacements = append(status.InkReplacements, int(b))
			}

		case st2FieldPrinterInfo2:
			if len(item) >= 2 {
				status.CISS = item[1]&0x01 != 0
			}
		}
	}

	if errorByte >= 0 && (errorByte != 0 || status.StatusCode == 0) {
		status.ErrorNum = errorByte
		status.ErrorCode = st2ErrorText(byte(errorByte))
	}
	for _, w := range warningBytes {
		if st2IgnoredWarning(w) {
			continue
		}
		status.WarningNums = append(status.WarningNums, int(w))
		status.WarningCodes = append(status.WarningCodes, st2WarningText(w, status.InkOrder))
	}

	return status, nil
}

// StatusMessages renders the status as vendor-neutral lines prefixed with
// "Error:", "Warning:" or "Ready:" so the inventory status classifier and
// alerting can act on them. Raw Epson codes are included for support.
func (s *ST2Status) StatusMessages() []string {
	var msgs []string
	if s.ErrorCode != "" {
		msgs = append(msgs, fmt.Sprintf("Error: %s (Epson error %d)", s.ErrorCode, s.ErrorNum))
	} else if s.StatusCode == 0 && s.StatusText != "" {
		msgs = append(msgs, "Error: Printer error (Epson status 0)")
	}
	for i, w := range s.WarningCodes {
		if i < len(s.WarningNums) {
			msgs = append(msgs, fmt.Sprintf("Warning: %s (Epson warning %d)", w, s.WarningNums[i]))
		} else {
			msgs = append(msgs, "Warning: "+w)
		}
	}
	if s.StatusCode > 0 && s.StatusText != "" {
		prefix := "Status"
		if s.Ready {
			prefix = "Ready"
		}
		msgs = append(msgs, fmt.Sprintf("%s: %s (Epson status %d)", prefix, s.StatusText, s.StatusCode))
	}
	return msgs
}

// ToMetrics converts ST2Status to a map suitable for device metrics.
func (s *ST2Status) ToMetrics() map[string]interface{} {
	result := make(map[string]interface{})

	// Printer status
	if s.StatusText != "" {
		result["epson_status"] = s.StatusText
	}
	if s.StatusText != "" && s.StatusCode >= 0 {
		result["epson_status_code"] = fmt.Sprintf("%02x", s.StatusCode)
	}
	if s.ErrorCode != "" {
		result["epson_error_code"] = fmt.Sprintf("%d", s.ErrorNum)
	}
	if msgs := s.StatusMessages(); len(msgs) > 0 {
		result["status_messages"] = msgs
	}
	result["epson_ready"] = s.Ready

	if s.ErrorCode != "" {
		result["epson_error"] = s.ErrorCode
	}

	if len(s.WarningCodes) > 0 {
		result["epson_warnings"] = strings.Join(s.WarningCodes, "; ")
	}

	// Ink levels - normalize to our supply naming convention
	for color, level := range s.InkLevels {
		if level < 0 {
			continue // Skip unknown levels
		}
		result[st2InkKey(color)] = float64(level)
	}

	// Paper counters (numeric, mergeable)
	if s.TotalPages >= 0 {
		result["total_pages"] = s.TotalPages
	}
	if s.ColorPages >= 0 {
		result["color_pages"] = s.ColorPages
	}
	if s.MonoPages >= 0 {
		result["mono_pages"] = s.MonoPages
	}

	// Maintenance box remaining capacity as supply levels
	for i, free := range s.MaintenanceTankFree {
		if free < 0 {
			continue
		}
		result[st2MaintenanceKey("supply_maintenance_box", i)] = float64(free)
	}
	for i, n := range s.MaintenanceBoxReplacements {
		result[st2MaintenanceKey("maintenance_box_replacements", i)] = n
	}
	// Only map ink replacements when the counts line up with the ink list;
	// otherwise the color assignment would be a guess.
	if len(s.InkReplacements) > 0 && len(s.InkReplacements) == len(s.InkOrder) {
		for i, n := range s.InkReplacements {
			key := strings.ToLower(strings.ReplaceAll(s.InkOrder[i], " ", "_")) + "_ink_replacements"
			if _, dup := result[key]; dup {
				key = fmt.Sprintf("%s_%d", key, i+1)
			}
			result[key] = n
		}
	}

	// Maintenance box status
	for boxNum, status := range s.MaintenanceBoxes {
		key := fmt.Sprintf("maintenance_box_%d", boxNum)
		result[key] = status

		// Also provide numeric waste level estimate
		wasteKey := fmt.Sprintf("waste_box_%d_status", boxNum)
		switch status {
		case "OK":
			result[wasteKey] = 0
		case "Near Full":
			result[wasteKey] = 80
		case "Full":
			result[wasteKey] = 100
		}
	}

	return result
}

// st2InkKey builds a normalized ink key: ink_black, ink_light_cyan, etc.
func st2InkKey(color string) string {
	return "ink_" + strings.ToLower(strings.ReplaceAll(color, " ", "_"))
}

// st2MaintenanceKey returns base for the first box and base_N for others.
func st2MaintenanceKey(base string, i int) string {
	if i == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, i+1)
}

// findBytes finds the index of needle in haystack, or -1 if not found.
func findBytes(haystack, needle []byte) int {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		found := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}
