package vendor

import (
	"context"
	"fmt"

	"printmaster/agent/featureflags"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// SNMPClient defines the SNMP operations needed by the remote client.
// This is a local interface to avoid import cycles with the scanner package.
// Go's structural typing means scanner.SNMPClient satisfies this interface.
type SNMPClient interface {
	Get(oids []string) (*gosnmp.SnmpPacket, error)
	Walk(rootOid string, walkFn gosnmp.WalkFunc) error
	Close() error
}

// EpsonRemoteClient handles Epson remote-mode SNMP commands.
type EpsonRemoteClient struct {
	client  SNMPClient
	timeout int
}

// NewEpsonRemoteClient creates a client for Epson remote-mode commands.
// The caller is responsible for closing the underlying SNMP client.
func NewEpsonRemoteClient(client SNMPClient, timeoutSeconds int) *EpsonRemoteClient {
	return &EpsonRemoteClient{
		client:  client,
		timeout: timeoutSeconds,
	}
}

// GetStatus fetches the ST2 status frame from an Epson printer.
// It first issues a plain GET of epPrtDeviceStatus (what Epson Device Admin
// uses), then falls back to the remote-mode "st" command for older devices.
func (c *EpsonRemoteClient) GetStatus(ctx context.Context) (*ST2Status, error) {
	if result, err := c.client.Get([]string{oids.EpsonDeviceStatus}); err == nil && result != nil {
		for _, pdu := range result.Variables {
			if data := st2Bytes(pdu.Value); len(data) > 0 {
				if status, perr := ParseST2Response(data); perr == nil {
					return status, nil
				}
			}
		}
	}

	// Build the status command OID
	oid, err := BuildEpsonRemoteOID(EpsonRemoteStatusCommand, nil)
	if err != nil {
		if logger.Global != nil {
			logger.Global.Error("Epson remote: failed to build status OID", "error", err)
		}
		return nil, fmt.Errorf("failed to build status OID: %w", err)
	}

	if logger.Global != nil {
		logger.Global.TraceTag("epson_remote", "Epson remote: fetching status", "oid", oid)
	}

	// Execute SNMP GET
	result, err := c.client.Get([]string{oid})
	if err != nil {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: SNMP GET failed", "oid", oid, "error", err)
		}
		return nil, fmt.Errorf("SNMP GET failed: %w", err)
	}

	if result == nil || len(result.Variables) == 0 {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: no response from status command", "oid", oid)
		}
		return nil, fmt.Errorf("no response from status command")
	}

	// Extract response bytes
	pdu := result.Variables[0]
	var data []byte

	switch v := pdu.Value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: unexpected status response type", "type", fmt.Sprintf("%T", pdu.Value))
		}
		return nil, fmt.Errorf("unexpected response type: %T", pdu.Value)
	}

	if len(data) == 0 {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: empty response from status command", "oid", oid)
		}
		return nil, fmt.Errorf("empty response from status command")
	}

	// Parse ST2 frame
	status, err := ParseST2Response(data)
	if err != nil {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: ST2 parse failed", "error", err, "data_len", len(data))
			logger.Global.TraceTag("epson_remote", "Epson remote: ST2 raw preview", "prefix_hex", fmt.Sprintf("% x", data[:min(len(data), 32)]))
		}
		return nil, fmt.Errorf("failed to parse ST2 response: %w", err)
	}

	if logger.Global != nil {
		logger.Global.Debug("Epson remote: status parsed",
			"status", status.StatusText,
			"ready", status.Ready,
			"ink_colors", len(status.InkLevels),
			"maint_boxes", len(status.MaintenanceBoxes))
	}

	return status, nil
}

// GetDeviceID fetches the IEEE-1284 device identification string.
// Returns key-value pairs like MFG, MDL, CMD, CLS, DES.
func (c *EpsonRemoteClient) GetDeviceID(ctx context.Context) (map[string]string, error) {
	oid, err := BuildEpsonRemoteOID(EpsonRemoteDeviceIDCommand, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build device ID OID: %w", err)
	}

	result, err := c.client.Get([]string{oid})
	if err != nil {
		return nil, fmt.Errorf("SNMP GET failed: %w", err)
	}

	if result == nil || len(result.Variables) == 0 {
		return nil, fmt.Errorf("no response from device ID command")
	}

	pdu := result.Variables[0]
	var data string

	switch v := pdu.Value.(type) {
	case []byte:
		data = string(v)
	case string:
		data = v
	default:
		return nil, fmt.Errorf("unexpected response type: %T", pdu.Value)
	}

	// Parse IEEE-1284 style string: MFG:value;CMD:value;MDL:value;...
	// Skip any binary prefix by finding first alphabetic character
	startIdx := 0
	for i, ch := range data {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
			startIdx = i
			break
		}
	}
	data = data[startIdx:]

	result_map := make(map[string]string)
	for _, pair := range splitSemicolon(data) {
		if pair == "" {
			continue
		}
		parts := splitColon(pair)
		if len(parts) >= 2 {
			key := parts[0]
			value := parts[1]
			// Expand common abbreviations
			switch key {
			case "MFG":
				result_map["Manufacturer"] = value
			case "MDL":
				result_map["Model"] = value
			case "CMD":
				result_map["Commands"] = value
			case "CLS":
				result_map["Class"] = value
			case "DES":
				result_map["Description"] = value
			default:
				result_map[key] = value
			}
		}
	}

	return result_map, nil
}

// FetchEpsonRemoteMetrics queries an Epson printer using remote-mode commands
// and returns metrics suitable for merging into device data.
// This is the main entry point called by the Epson vendor module.
//
// Parameters:
//   - ctx: Context for cancellation
//   - client: Pre-created SNMP client (caller manages lifecycle)
//   - ip: Target printer IP address (for logging only)
//
// Returns metrics map or nil if remote mode is disabled or fails.
func FetchEpsonRemoteMetrics(ctx context.Context, client SNMPClient, ip string) map[string]interface{} {
	// Check feature flag
	if !featureflags.EpsonRemoteModeEnabled() {
		return nil
	}

	if client == nil {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: no SNMP client provided")
		}
		return nil
	}

	// Create remote client and fetch status
	remote := NewEpsonRemoteClient(client, 5) // timeout not used for existing client

	status, err := remote.GetStatus(ctx)
	if err != nil {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: status fetch failed", "ip", ip, "error", err)
		}
		return nil
	}

	// Convert to metrics
	metrics := status.ToMetrics()

	if logger.Global != nil {
		logger.Global.Debug("Epson remote mode: metrics collected",
			"ip", ip,
			"status", status.StatusText,
			"ink_colors", len(status.InkLevels),
			"metrics_count", len(metrics))
	}

	return metrics
}

// FetchEpsonRemoteMetricsWithIP is a convenience function that creates its own
// SNMP connection and queries an Epson printer using remote-mode commands.
// This is used by ParseWithRemoteMode when no existing client is available.
//
// Parameters:
//   - ctx: Context for cancellation
//   - ip: Target printer IP address
//   - timeoutSeconds: SNMP timeout
//
// Returns metrics map or nil if remote mode is disabled or fails.
func FetchEpsonRemoteMetricsWithIP(ctx context.Context, ip string, timeoutSeconds int) map[string]interface{} {
	// Check feature flag
	if !featureflags.EpsonRemoteModeEnabled() {
		return nil
	}

	if ip == "" {
		return nil
	}

	if logger.Global != nil {
		logger.Global.TraceTag("epson_remote", "Epson remote: creating vendor SNMP client", "ip", ip, "timeout_s", timeoutSeconds)
	}

	// Create our own SNMP client
	client, err := NewVendorSNMPClient(ip, "public", timeoutSeconds)
	if err != nil {
		if logger.Global != nil {
			logger.Global.Warn("Epson remote: failed to create SNMP client", "ip", ip, "error", err)
		}
		return nil
	}
	defer client.Close()

	return FetchEpsonRemoteMetrics(ctx, client, ip)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Helper to split on semicolon without importing strings
func splitSemicolon(s string) []string {
	var result []string
	var current []byte
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			result = append(result, string(current))
			current = nil
		} else {
			current = append(current, s[i])
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}

// Helper to split on colon without importing strings
func splitColon(s string) []string {
	var result []string
	var current []byte
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			result = append(result, string(current))
			current = nil
		} else {
			current = append(current, s[i])
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}

// QueryEpsonRemoteStatus extracts and parses an ST2 frame from already
// collected PDUs. It accepts both the epPrtDeviceStatus OID (plain GET) and
// the remote-mode status command OID.
func QueryEpsonRemoteStatus(pdus []gosnmp.SnmpPDU) *ST2Status {
	statusOID, _ := BuildEpsonRemoteOID(EpsonRemoteStatusCommand, nil)

	for _, pdu := range pdus {
		pduOID := normalizeOID(pdu.Name)
		if pduOID != oids.EpsonDeviceStatus && pduOID != statusOID {
			continue
		}
		if data := st2Bytes(pdu.Value); len(data) > 0 {
			if status, err := ParseST2Response(data); err == nil {
				return status
			}
		}
	}
	return nil
}

func st2Bytes(value interface{}) []byte {
	switch v := value.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}
