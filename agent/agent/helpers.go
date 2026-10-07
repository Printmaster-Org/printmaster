package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"printmaster/common/util"
)

// AppendScanEvent writes a timestamped single-line audit of scan events to
// logs/scan_events.log. It's best-effort and will not abort scanning on error.
// Exported so other packages (UI/endpoints) can call it.
func AppendScanEvent(msg string) {
	logDir := ensureLogDir()
	fpath := filepath.Join(logDir, "scan_events.log")
	line := time.Now().Format(time.RFC3339) + " " + msg + "\n"
	// best-effort append
	f, err := os.OpenFile(fpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
	// Also send to Info logger
	Info(msg)
}

// pduToString normalizes common SNMP PDU value types into a printable string.
// It prefers decoding OctetString ([]byte) into a UTF-8 string, falling back
// to a generic fmt.Sprintf conversion for other types.
func pduToString(v interface{}) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return util.DecodeOctetString(b)
	}
	return fmt.Sprintf("%v", v)
}
