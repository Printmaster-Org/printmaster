package vendor

import (
	"fmt"
	"strings"
)

// Epson inkjet status, error and warning code tables, taken from Epson Device
// Admin (Constants.InkjetStatus/InkjetErrors/InkjetWarnings) with English text
// from its localisation resources. They decode ST2 fields 0x01, 0x02 and 0x04.

// statusCodes maps the ST2 status byte (EDA STATUS_DIC).
var statusCodes = map[byte]string{
	0:  "Error",
	1:  "Self printing",
	2:  "Busy",
	3:  "Standby",
	4:  "Idle",
	5:  "Printing cancelled",
	6:  "Ink drying",
	7:  "Cleaning",
	8:  "Factory shipping state",
	9:  "Motor drive off",
	10: "Shutting down",
	11: "Waiting for paper initialization trigger",
	12: "Initializing paper",
	13: "Changing black ink type",
	14: "Adjusting heater temperature",
	15: "Initializing",
	16: "Updating firmware",
	17: "Initial ink filling",
	18: "Mixing ink",
	19: "Sleeping",
}

// st2ReadyStatuses are statuses where the printer can accept work.
var st2ReadyStatuses = map[byte]bool{3: true, 4: true, 19: true}

// errorCodes maps the ST2 error byte (EDA ERROR_DIC). EDA treats code 0 as a
// fatal error; it is only meaningful when the status byte is 0 (Error).
var errorCodes = map[byte]string{
	0:   "Fatal error",
	1:   "Printing data from another port",
	2:   "Cover open",
	3:   "Release lever position error",
	4:   "Paper jam",
	5:   "Ink out",
	6:   "Paper out",
	7:   "Running setting mode",
	8:   "Unknown error",
	9:   "Incomplete paper change",
	10:  "Paper size error",
	11:  "Ribbon jam",
	12:  "Paper size, paper type, or paper path error",
	13:  "Thick paper lever position error",
	14:  "Paper eject error",
	16:  "Waste ink overflow",
	17:  "Waiting for carriage return after tear off",
	18:  "Double feed",
	19:  "Print head too hot",
	20:  "Paper cut error",
	21:  "Paper set lever released",
	22:  "Cleaning not possible",
	23:  "Paper not recognized",
	24:  "Paper skew",
	25:  "Cleaning count exceeded",
	26:  "Ink cover open",
	27:  "Ink cartridge error",
	28:  "Cutter error",
	29:  "Cutter jam",
	30:  "Ink color error",
	31:  "Cutter cover open",
	32:  "Ink lever released",
	33:  "Return ink lever",
	34:  "Maintenance box missing",
	35:  "Ink cartridge combination error",
	36:  "Command error",
	37:  "Rear cover open",
	38:  "Multi-sensor gain error",
	39:  "Auto adjustment failed",
	40:  "Cleaning failed",
	41:  "Paper tray missing",
	42:  "Card loaded incorrectly",
	43:  "CD/DVD not recognized",
	44:  "Built-in cartridge waste ink overflow",
	45:  "Maintenance box missing (No. 2)",
	46:  "Waste ink overflow (No. 2)",
	47:  "Battery voltage error",
	48:  "Battery temperature error",
	49:  "Battery empty",
	50:  "Shut off",
	51:  "Initial ink fill failed",
	52:  "Print pack end",
	53:  "Internal temperature error",
	54:  "Maintenance box cover open",
	55:  "Scanner unit open",
	56:  "Guide error",
	57:  "Waste ink overflow (No. 3)",
	58:  "Maintenance box missing (No. 3)",
	59:  "Ink cover cannot open",
	60:  "Not enough free space in maintenance box",
	61:  "Ink cartridge expended",
	62:  "Take-up error",
	63:  "Paper unwinding error",
	64:  "Maintenance cover open (service call)",
	65:  "Maintenance error (tube)",
	66:  "Spot color device error",
	67:  "Take-up device error",
	68:  "CD/DVD media error",
	69:  "CD/DVD media error (maintenance)",
	70:  "LCD position error",
	71:  "Cannot print in black and white",
	72:  "Insufficient memory",
	73:  "Unsupported two-sided paper",
	74:  "Maintenance box near end",
	75:  "Printer or driver mismatch",
	79:  "Copying",
	80:  "Storage error",
	88:  "Print head may be uncapped; run a nozzle check",
	89:  "Maintenance roller out of order",
	102: "Loaded paper size does not match setting",
	103: "Output tray full",
	110: "Finisher error",
	111: "Out of staples",
	112: "Duplex print error",
}

// warningCodes maps ST2 warning bytes (EDA WARNING_DIC). "%s" is replaced
// with the ink color resolved by st2WarningInkIndex.
var warningCodes = map[byte]string{
	0:   "Paper low (paper path 1)",
	1:   "Paper low (paper path 2)",
	32:  "Replace maintenance box soon (No. 1)",
	33:  "Maintenance call",
	34:  "Replace maintenance box soon (No. 2)",
	35:  "Maintenance box cover open",
	36:  "Replace maintenance box soon (No. 3)",
	48:  "Main tank ink low (Black)",
	49:  "Main tank ink low (Cyan)",
	50:  "Main tank ink low (Magenta)",
	51:  "Main tank ink low (Yellow)",
	52:  "Main tank ink low (Light Cyan)",
	53:  "Main tank ink low (Light Magenta)",
	54:  "Main tank ink low (Dark Yellow)",
	55:  "Main tank ink low (Black 2)",
	56:  "Main tank ink low (Black 3)",
	57:  "Main tank ink low (Black 4)",
	64:  "Ink cover open",
	65:  "Replace maintenance box soon",
	66:  "Replace maintenance box soon",
	67:  "Shut off",
	68:  "Printing in black and white only",
	69:  "Nozzles cleaned but carriage cannot return",
	70:  "Continuous AID verification failed",
	80:  "Ink low",
	96:  "Battery nearly empty",
	97:  "Foreign object detected by paper width sensor",
	98:  "Cutter maintenance due",
	99:  "NVRAM memory warning",
	112: "Ink sedimentation warning",
}

func init() {
	for c := byte(16); c <= 31; c++ {
		warningCodes[c] = "Ink low (%s)"
	}
	for c := byte(81); c <= 90; c++ {
		warningCodes[c] = "Cannot clean, ink low (%s)"
	}
	for c := byte(113); c <= 122; c++ {
		warningCodes[c] = "Ink low 2 (%s)"
	}
	warningCodes[145] = "Ink cartridge must be replaced (%s)"
	warningCodes[146] = "Ink cartridge must be replaced (%s)"
}

// st2IgnoredWarnings are informational codes EDA does not treat as warnings.
func st2IgnoredWarning(code byte) bool { return code >= 129 && code <= 138 }

// st2WarningInkIndex returns the 0-based position in the ST2 ink list that a
// color-specific warning refers to, or -1 (EDA GetColorDescriptionForWarningCode).
func st2WarningInkIndex(code byte) int {
	switch {
	case code >= 16 && code <= 31:
		return int(code & 0x0f)
	case code >= 81 && code <= 90, code >= 113 && code <= 122, code == 145, code == 146:
		return int(code&0x0f) - 1
	}
	return -1
}

func st2StatusText(code byte) string {
	if text, ok := statusCodes[code]; ok {
		return text
	}
	return fmt.Sprintf("Unknown status (%d)", code)
}

func st2ErrorText(code byte) string {
	if text, ok := errorCodes[code]; ok {
		return text
	}
	return fmt.Sprintf("Unknown error (%d)", code)
}

// st2WarningText renders a warning, naming the ink color when known.
func st2WarningText(code byte, inkOrder []string) string {
	text, ok := warningCodes[code]
	if !ok {
		return fmt.Sprintf("Unknown warning (%d)", code)
	}
	if !strings.Contains(text, "%s") {
		return text
	}
	color := ""
	if i := st2WarningInkIndex(code); i >= 0 && i < len(inkOrder) {
		color = inkOrder[i]
	}
	if color == "" {
		return strings.TrimSpace(strings.ReplaceAll(text, "(%s)", ""))
	}
	return fmt.Sprintf(text, color)
}
