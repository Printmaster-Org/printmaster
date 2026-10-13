package agent

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"

	"printmaster/common/util"

	"github.com/gosnmp/gosnmp"
)

// helpers and shared types have been moved to agent/helpers.go and agent/types.go

// See scanner_api.go for migration: Discover, LiveDiscoveryDetect, CollectMetrics

// pingWithExec attempts to ping using the system ping command as a fallback.
func pingWithExec(ip string, logFn func(string)) bool {
	pingPath, err := exec.LookPath("ping")
	if err != nil {
		if logFn != nil {
			logFn("ping executable not found in PATH")
		}
		return false
	}
	// Try a sequence of argument variants per platform to handle flag differences.
	var attempts [][]string
	switch runtime.GOOS {
	case "windows":
		attempts = [][]string{{"-n", "1", "-w", "1000", ip}}
	case "darwin":
		// macOS/BSD ping flags vary; try a few common variants then fall back to minimal.
		attempts = [][]string{{"-c", "1", "-W", "1000", ip}, {"-c", "1", ip}}
	default:
		// Linux: try per-packet timeout (-W), deadline (-w), then minimal
		attempts = [][]string{{"-c", "1", "-W", "1", ip}, {"-c", "1", "-w", "1", ip}, {"-c", "1", ip}}
	}

	for _, args := range attempts {
		if logFn != nil {
			logFn("Running system ping: ping " + strings.Join(args, " "))
		}
		cmd := exec.Command(pingPath, args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			if logFn != nil {
				logFn("system ping succeeded: " + ip)
			}
			return true
		}
		if logFn != nil {
			logFn("system ping attempt failed: " + err.Error() + "; output: " + string(out))
		}
	}
	return false
}

// Replaced by new scanner: Discover() in scanner_api.go

// GetLocalSubnets returns the subnet containing the default gateway.
// This is more reliable than enumerating all interfaces because it prioritizes
// the actual network route used for internet connectivity.
func GetLocalSubnets() ([]net.IPNet, error) {
	subnet, err := getDefaultGatewaySubnet()
	if err != nil {
		return nil, err
	}
	return []net.IPNet{subnet}, nil
}

// getDefaultGatewaySubnet finds the subnet that contains the default gateway
func getDefaultGatewaySubnet() (net.IPNet, error) {
	var gateway string
	var err error

	// Get default gateway IP based on OS
	switch runtime.GOOS {
	case "windows":
		gateway, err = getWindowsDefaultGateway()
	case "linux", "darwin":
		gateway, err = getUnixDefaultGateway()
	default:
		return net.IPNet{}, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	if err != nil || gateway == "" {
		return net.IPNet{}, fmt.Errorf("failed to find default gateway: %w", err)
	}

	// Find the local interface that can reach this gateway
	gatewayIP := net.ParseIP(gateway)
	if gatewayIP == nil {
		return net.IPNet{}, fmt.Errorf("invalid gateway IP: %s", gateway)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return net.IPNet{}, err
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}

			// Check if gateway is in this subnet
			if ipnet.Contains(gatewayIP) {
				// Normalize to network base address
				base := ipnet.IP.To4().Mask(ipnet.Mask)
				return net.IPNet{IP: base, Mask: ipnet.Mask}, nil
			}
		}
	}

	return net.IPNet{}, fmt.Errorf("could not find interface for gateway %s", gateway)
}

// getWindowsDefaultGateway finds the default gateway on Windows
func getWindowsDefaultGateway() (string, error) {
	cmd := exec.Command("route", "print", "0.0.0.0")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for default route (0.0.0.0)
		if strings.HasPrefix(line, "0.0.0.0") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				// Format: 0.0.0.0  0.0.0.0  <gateway>  <interface>
				return fields[2], nil
			}
		}
	}

	return "", fmt.Errorf("default gateway not found in route table")
}

// getUnixDefaultGateway finds the default gateway on Linux/macOS
func getUnixDefaultGateway() (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("route", "-n", "get", "default")
	} else {
		cmd = exec.Command("ip", "route", "show", "default")
	}

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// Linux: "default via 192.168.1.1 ..."
		if fields[0] == "default" && len(fields) >= 3 && fields[1] == "via" {
			return fields[2], nil
		}

		// macOS: "gateway: 192.168.1.1"
		if fields[0] == "gateway:" && len(fields) >= 2 {
			return fields[1], nil
		}
	}

	return "", fmt.Errorf("default gateway not found in route table")
}

// getCommonSerialOIDs returns the most common printer serial number OID
func getCommonSerialOIDs() []string {
	return []string{
		"1.3.6.1.2.1.43.5.1.1.17.1", // Printer-MIB::prtGeneralSerialNumber (most common)
	}
}

// getVendorSpecificSerialOIDs returns serial OIDs for a specific manufacturer
func getVendorSpecificSerialOIDs(manufacturer string) []string {
	if manufacturer == "" {
		return nil
	}

	vendor := strings.ToLower(manufacturer)
	switch {
	case strings.Contains(vendor, "hp") || strings.Contains(vendor, "hewlett"):
		return []string{"1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.3.0"}
	case strings.Contains(vendor, "canon"):
		return []string{"1.3.6.1.4.1.1602.1.2.1.4.1.1.8.1"}
	case strings.Contains(vendor, "samsung"):
		return []string{"1.3.6.1.4.1.236.11.5.11.55.1.1.1.1"}
	case strings.Contains(vendor, "kyocera"):
		return []string{"1.3.6.1.4.1.1347.43.5.1.1.28.1"} // kcprtSerialNumber
	case strings.Contains(vendor, "oki"):
		return []string{"1.3.6.1.4.1.2001.1.1.1.1.11.1.1.1.0"}
	case strings.Contains(vendor, "ricoh"):
		return []string{"1.3.6.1.4.1.367.3.2.1.1.1.4.0"}
	case strings.Contains(vendor, "brother"):
		return []string{"1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.1.0"}
	case strings.Contains(vendor, "lexmark"):
		return []string{"1.3.6.1.4.1.641.2.1.2.1.2.1"}
	case strings.Contains(vendor, "epson"):
		return []string{"1.3.6.1.4.1.1248.1.1.3.1.3.8.0"}
	default:
		return nil
	}
}

// getAllSerialOIDs returns all serial number OIDs to try (common + all vendor-specific)
func getAllSerialOIDs() []string {
	oids := getCommonSerialOIDs()
	vendors := []string{"hp", "canon", "samsung", "kyocera", "oki", "ricoh", "brother", "lexmark", "epson"}
	for _, v := range vendors {
		oids = append(oids, getVendorSpecificSerialOIDs(v)...)
	}
	return oids
}

// Replaced by LiveDiscoveryDetect in scanner_api.go

func init() {
	// No-op init. Avoid calling deprecated rand.Seed; use local rand.New when
	// a deterministic seeded generator is required in the future.
}

// diagnosticWalk performs a limited SNMP walk over the provided root OIDs and
// logs results via logFn. It stops after a reasonable number of entries to avoid
// long-running walks during discovery.
// diagnosticWalk performs a limited SNMP walk over the provided root OIDs and
// returns collected PDUs. It stops after a reasonable number of entries to avoid
// long-running walks during discovery.
// diagnosticWalk performs a limited SNMP walk over the provided root OIDs and
// returns collected PDUs. It accepts a maxEntries limiter (0 means use a
// sensible large default) and an optional list of stopKeywords. If any PDU's
// name or string value contains one of the stopKeywords (case-insensitive)
// the walk will stop early and return what was collected so far.
func diagnosticWalk(snmp SNMPClient, logFn func(string), roots []string, maxEntries int, stopKeywords []string) []gosnmp.SnmpPDU {
	collected := []gosnmp.SnmpPDU{}
	if snmp == nil {
		return collected
	}
	// default limit if caller didn't set one. Increase to 5000 to allow deeper
	// diagnostic walks for stubborn SNMPv1 devices that hide model/serial info
	// deep in enterprise MIBs.
	if maxEntries <= 0 {
		maxEntries = 5000
	}
	count := 0
	// prepare keyword checks
	kws := make([]string, 0, len(stopKeywords))
	for _, k := range stopKeywords {
		if k = strings.TrimSpace(k); k != "" {
			kws = append(kws, strings.ToLower(k))
		}
	}

	// helper to check PDU for any keyword
	containsKeyword := func(pdu gosnmp.SnmpPDU) bool {
		if len(kws) == 0 {
			return false
		}
		// check name
		lname := strings.ToLower(strings.TrimPrefix(pdu.Name, "."))
		for _, kw := range kws {
			if strings.Contains(lname, kw) {
				return true
			}
		}
		// check string value
		if pdu.Type == gosnmp.OctetString {
			if b, ok := pdu.Value.([]byte); ok {
				s := strings.ToLower(util.DecodeOctetString(b))
				for _, kw := range kws {
					if strings.Contains(s, kw) {
						return true
					}
				}
			}
		} else {
			sval := strings.ToLower(fmt.Sprintf("%v", pdu.Value))
			for _, kw := range kws {
				if strings.Contains(sval, kw) {
					return true
				}
			}
		}
		return false
	}

	for _, root := range roots {
		if logFn != nil {
			logFn("Starting diagnostic SNMP walk of " + root)
		}
		err := snmp.Walk(root, func(pdu gosnmp.SnmpPDU) error {
			if logFn != nil {
				logFn(fmt.Sprintf("WALK %s Type=%v Value=%#v", pdu.Name, pdu.Type, pdu.Value))
			}
			collected = append(collected, pdu)
			count++
			// stop if we've hit the count limit
			if maxEntries > 0 && count >= maxEntries {
				return fmt.Errorf("walk limit reached")
			}
			// stop if keyword matched
			if containsKeyword(pdu) {
				return fmt.Errorf("walk stop keyword matched")
			}
			return nil
		})
		if err != nil {
			if logFn != nil {
				// Report walk errors but continue with other roots
				logFn("Diagnostic walk error: " + err.Error())
			}
		}
		if maxEntries > 0 && count >= maxEntries {
			if logFn != nil {
				logFn("Diagnostic walk reached max entries; stopping further walks")
			}
			break
		}
	}
	return collected
}

// DiagnosticWalk is an exported wrapper around diagnosticWalk for callers
// outside the package (e.g., the web UI) that want to perform a limited
// SNMP walk for diagnostics or refresh operations. It does not accept
// stopKeywords and uses a reasonable default.
func DiagnosticWalk(snmp SNMPClient, logFn func(string), roots []string, maxEntries int) []gosnmp.SnmpPDU {
	return diagnosticWalk(snmp, logFn, roots, maxEntries, []string{"pid", "model", "serial", "prtGeneral", "prtMarker", "supply", "toner"})
}

// FullDiagnosticWalk performs a complete SNMP walk without early termination
// stopKeywords. Use this when you want to capture ALL available OIDs including
// vendor-specific enterprise MIBs with detailed impression counters and stats.
func FullDiagnosticWalk(snmp SNMPClient, logFn func(string), roots []string, maxEntries int) []gosnmp.SnmpPDU {
	return diagnosticWalk(snmp, logFn, roots, maxEntries, nil) // No stop keywords
}

// WalkProgress contains progress information for streaming walks
type WalkProgress struct {
	Stage     string `json:"stage"`      // "connecting", "walking", "complete", "error"
	RootIndex int    `json:"root_index"` // Current root being walked (0-based)
	RootCount int    `json:"root_count"` // Total number of roots
	RootOID   string `json:"root_oid"`   // Current root OID being walked
	OIDsFound int    `json:"oids_found"` // Total OIDs collected so far
	MaxOIDs   int    `json:"max_oids"`   // Maximum OIDs to collect
	Percent   int    `json:"percent"`    // Estimated percentage (0-100)
	Message   string `json:"message"`    // Human-readable status
	Error     string `json:"error,omitempty"`
}

// ProgressCallback is called during walks to report progress
type ProgressCallback func(progress WalkProgress)

// FullDiagnosticWalkWithProgress performs a complete SNMP walk with progress reporting.
// The progressFn is called periodically (not for every OID to avoid overhead).
// Returns collected PDUs and any error.
func FullDiagnosticWalkWithProgress(snmp SNMPClient, logFn func(string), roots []string, maxEntries int, progressFn ProgressCallback) []gosnmp.SnmpPDU {
	collected := []gosnmp.SnmpPDU{}
	if snmp == nil {
		if progressFn != nil {
			progressFn(WalkProgress{Stage: "error", Error: "SNMP client is nil"})
		}
		return collected
	}

	if maxEntries <= 0 {
		maxEntries = 5000
	}

	count := 0
	rootCount := len(roots)

	// Estimate typical OIDs per root for progress calculation
	// MIB-2: ~200-500, Printer-MIB: ~100-300, Enterprise: ~500-2000
	// We use a weighted estimate assuming ~2000 total typical
	estimatedTotal := 2000
	if maxEntries < estimatedTotal {
		estimatedTotal = maxEntries
	}

	// Report interval: every 50 OIDs or 2 seconds, whichever comes first
	reportInterval := 50
	lastReport := 0

	for rootIdx, root := range roots {
		if progressFn != nil {
			// Calculate rough percentage based on root progress + OIDs within root
			basePercent := (rootIdx * 100) / rootCount
			progressFn(WalkProgress{
				Stage:     "walking",
				RootIndex: rootIdx,
				RootCount: rootCount,
				RootOID:   root,
				OIDsFound: count,
				MaxOIDs:   maxEntries,
				Percent:   basePercent,
				Message:   fmt.Sprintf("Walking %s (%d OIDs collected)", formatRootName(root), count),
			})
		}

		if logFn != nil {
			logFn("Starting diagnostic SNMP walk of " + root)
		}

		err := snmp.Walk(root, func(pdu gosnmp.SnmpPDU) error {
			if logFn != nil {
				logFn(fmt.Sprintf("WALK %s Type=%v Value=%#v", pdu.Name, pdu.Type, pdu.Value))
			}
			collected = append(collected, pdu)
			count++

			// Report progress periodically
			if progressFn != nil && count-lastReport >= reportInterval {
				lastReport = count
				// Calculate percentage: weighted by roots completed + OIDs within estimated total
				rootProgress := float64(rootIdx) / float64(rootCount)
				oidProgress := float64(count) / float64(estimatedTotal)
				// Blend: 30% root-based, 70% OID-based (OIDs are more granular)
				percent := int((rootProgress*0.3 + oidProgress*0.7) * 100)
				if percent > 95 {
					percent = 95 // Cap at 95% until complete
				}
				progressFn(WalkProgress{
					Stage:     "walking",
					RootIndex: rootIdx,
					RootCount: rootCount,
					RootOID:   root,
					OIDsFound: count,
					MaxOIDs:   maxEntries,
					Percent:   percent,
					Message:   fmt.Sprintf("Collecting SNMP data... %d OIDs", count),
				})
			}

			// Stop if we've hit the count limit
			if maxEntries > 0 && count >= maxEntries {
				return fmt.Errorf("walk limit reached")
			}
			return nil
		})

		if err != nil && logFn != nil {
			logFn("Diagnostic walk error: " + err.Error())
		}

		if maxEntries > 0 && count >= maxEntries {
			if logFn != nil {
				logFn("Diagnostic walk reached max entries; stopping further walks")
			}
			break
		}
	}

	// Final progress report
	if progressFn != nil {
		progressFn(WalkProgress{
			Stage:     "complete",
			RootIndex: rootCount,
			RootCount: rootCount,
			OIDsFound: count,
			MaxOIDs:   maxEntries,
			Percent:   100,
			Message:   fmt.Sprintf("SNMP walk complete: %d OIDs collected", count),
		})
	}

	return collected
}

// formatRootName returns a human-readable name for common OID roots
func formatRootName(oid string) string {
	switch oid {
	case "1.3.6.1.2.1":
		return "MIB-2 (System/Interfaces)"
	case "1.3.6.1.2.1.43":
		return "Printer-MIB"
	case "1.3.6.1.4.1":
		return "Enterprise MIBs"
	default:
		return oid
	}
}
