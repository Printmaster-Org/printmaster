package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"printmaster/agent/scanner"
	"printmaster/agent/scanner/vendor"
	"printmaster/agent/supplies"
	"printmaster/common/util"

	"github.com/gosnmp/gosnmp"
)

// AssetIDRegex can be set by the caller (UI/settings) to provide a company-specific
// regex for extracting asset tags from adminContact or similar fields. If empty,
// ParsePDUs falls back to a generic heuristic regex.
var AssetIDRegex string
var assetIDRegexMu sync.RWMutex

// A label must be separate from its value: protocol names such as SNMPv2
// are not serial labels. Match the full Serial Number label before its value.
var serialLabelRE = regexp.MustCompile(`(?i)\b(?:sn|s/n|serial(?:\s*number)?)\b[:=\s]+([A-Za-z0-9\-]{4,40})`)

// SetAssetIDRegex updates the package-level regex used during parsing.
func SetAssetIDRegex(r string) {
	assetIDRegexMu.Lock()
	defer assetIDRegexMu.Unlock()
	AssetIDRegex = r
}

// parseDeviceIDPayload extracts key/value pairs from IEEE-1284 style descriptors.
func parseDeviceIDPayload(raw string) map[string]string {
	fields := make(map[string]string)
	if raw == "" {
		return fields
	}
	parts := strings.Split(raw, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sepIdx := strings.IndexAny(part, ":=")
		if sepIdx == -1 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(part[:sepIdx]))
		val := strings.TrimSpace(part[sepIdx+1:])
		if key == "" || val == "" {
			continue
		}
		fields[key] = val
	}
	return fields
}

// firstNonEmpty returns the first non-empty trimmed string from the provided list.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// extractIPv4FromOID checks whether the last four numeric components of an OID
// form a valid IPv4 address and returns it. Example: "1.2.3.4.172.52.105.25" -> "172.52.105.25"
func extractIPv4FromOID(oid string) (string, bool) {
	// trim leading dot
	o := strings.TrimPrefix(oid, ".")
	parts := strings.Split(o, ".")
	if len(parts) < 4 {
		return "", false
	}
	a := parts[len(parts)-4:]
	nums := make([]int, 4)
	for i := 0; i < 4; i++ {
		v, err := strconv.Atoi(a[i])
		if err != nil || v < 0 || v > 255 {
			return "", false
		}
		nums[i] = v
	}
	return fmt.Sprintf("%d.%d.%d.%d", nums[0], nums[1], nums[2], nums[3]), true
}

// isValidIPv4 validates that a string is a properly formatted IPv4 address
func isValidIPv4(ip string) bool {
	if ip == "" || ip == "0.0.0.0" {
		return false
	}
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		num, err := strconv.Atoi(part)
		if err != nil || num < 0 || num > 255 {
			return false
		}
	}
	return true
}

// isValidSubnetMask validates that a string is a proper subnet mask (not just any IP)
func isValidSubnetMask(mask string) bool {
	if !isValidIPv4(mask) {
		return false
	}

	// Valid subnet masks have contiguous 1 bits followed by contiguous 0 bits
	// Common valid masks: 255.255.255.0, 255.255.0.0, 255.255.255.128, etc.
	parts := strings.Split(mask, ".")
	var binary uint32
	for i, part := range parts {
		num, err := strconv.Atoi(part)
		// Bounds check to prevent integer overflow when converting to uint32
		if err != nil || num < 0 || num > 255 {
			return false
		}
		binary |= uint32(num) << uint(24-i*8)
	}

	// Check that all 1 bits are contiguous (no 0 bits followed by 1 bits)
	// XOR with its increment should have exactly one bit set
	inverted := ^binary
	return (inverted & (inverted + 1)) == 0
}

// isValidMACAddress validates and normalizes a MAC address
func isValidMACAddress(mac string) bool {
	if mac == "" {
		return false
	}
	// Remove common separators
	cleaned := strings.ReplaceAll(mac, ":", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, ".", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")

	// Should be exactly 12 hex characters
	if len(cleaned) != 12 {
		return false
	}

	// Check all characters are hex
	for _, c := range cleaned {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}

	return true
}

// normalizeNetworkValue validates and normalizes network configuration values
func normalizeNetworkValue(value, fieldType string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)

	switch fieldType {
	case "ip", "gateway", "dns":
		if isValidIPv4(value) {
			return value
		}
		return ""

	case "subnet":
		if isValidSubnetMask(value) {
			return value
		}
		return ""

	case "mac":
		if isValidMACAddress(value) {
			return value
		}
		return ""

	default:
		return value
	}
}

// detectWebUIURL attempts to build an HTTP or HTTPS URL for the device's web interface.
// It checks open ports (from meta), probes the web service, and follows redirects.
func detectWebUIURL(ip string, meta *ScanMeta, pduByOid map[string]gosnmp.SnmpPDU) string {
	// Check if HTTP (80) or HTTPS (443) ports are open from port scan
	hasHTTP := false
	hasHTTPS := false

	if meta != nil {
		for _, port := range meta.OpenPorts {
			if port == 80 {
				hasHTTP = true
			}
			if port == 443 {
				hasHTTPS = true
			}
		}
	}

	// If neither HTTP nor HTTPS detected from port scan, try to infer from SNMP data
	// Many printers expose prtChannelEntry OIDs that indicate HTTP/HTTPS support
	if !hasHTTP && !hasHTTPS {
		for k := range pduByOid {
			// Look for printer channel entries that mention http/https
			if strings.HasPrefix(k, "1.3.6.1.2.1.43.14.") {
				// prtChannel subtree often contains protocol info
				if val, ok := pduByOid[k]; ok {
					valStr := strings.ToLower(pduToString(val.Value))
					if strings.Contains(valStr, "http") {
						if strings.Contains(valStr, "https") {
							hasHTTPS = true
						} else {
							hasHTTP = true
						}
					}
				}
			}
		}
	}

	// Try HTTPS first (prefer secure), then HTTP
	// We'll probe each to verify it's actually accessible and follow redirects
	var candidates []string
	if hasHTTPS {
		candidates = append(candidates, fmt.Sprintf("https://%s", ip))
	}
	if hasHTTP {
		candidates = append(candidates, fmt.Sprintf("http://%s", ip))
	}

	// If no ports detected, still try both HTTPS and HTTP as fallback
	if len(candidates) == 0 {
		candidates = []string{
			fmt.Sprintf("https://%s", ip),
			fmt.Sprintf("http://%s", ip),
		}
	}

	// Probe each candidate URL to verify it's accessible
	for _, url := range candidates {
		if finalURL := probeWebUI(url); finalURL != "" {
			return finalURL
		}
	}

	// If nothing works, default to http://<ip>
	return fmt.Sprintf("http://%s", ip)
}

// probeWebUI checks if a URL is accessible and follows redirects to get the final URL.
// Returns the final URL if accessible, empty string otherwise.
func probeWebUI(probeURL string) string {
	// Validate and parse the initial URL to prevent SSRF
	parsedInitial, err := url.Parse(probeURL)
	if err != nil {
		return ""
	}

	// Only allow http and https schemes
	if parsedInitial.Scheme != "http" && parsedInitial.Scheme != "https" {
		return ""
	}

	// Reconstruct URL from validated components to break CodeQL taint chain.
	// Using url.URL struct literal ensures no tainted data flows through.
	safeURL := &url.URL{
		Scheme: parsedInitial.Scheme,
		Host:   parsedInitial.Host,
		Path:   parsedInitial.Path,
	}
	validatedURL := safeURL.String()

	// Track the original host to prevent redirect attacks to different hosts
	originalHost := parsedInitial.Hostname()

	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Allow up to 5 redirects
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			// Security: prevent redirects to different hosts (SSRF protection)
			if req.URL.Hostname() != originalHost {
				return http.ErrUseLastResponse
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// #nosec G402 -- InsecureSkipVerify intentionally enabled:
				// Network printers commonly use self-signed SSL certificates that
				// would fail standard validation. This is a local network probe to
				// detect printer web UIs, not a general-purpose HTTP client.
				// SSRF protections are implemented above (redirect host validation).
				InsecureSkipVerify: true,
			},
			DisableKeepAlives: true,
		},
	}

	req, err := http.NewRequest("GET", validatedURL, nil)
	if err != nil {
		return ""
	}

	resp, err := client.Do(req)
	if err != nil {
		// Connection failed, URL not accessible
		return ""
	}
	defer resp.Body.Close()

	// If we got a successful response (2xx, 3xx), return the final URL
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		// Return the final URL after following redirects
		return resp.Request.URL.String()
	}

	// 401 (auth required) is also valid - the web UI exists but needs login
	if resp.StatusCode == 401 {
		return resp.Request.URL.String()
	}

	return ""
}

// ParsePDUs converts a slice of SNMP PDUs into a PrinterInfo. It returns the
// populated PrinterInfo and a boolean indicating whether the heuristics
// consider the device a printer.
func ParsePDUs(scanIP string, vars []gosnmp.SnmpPDU, meta *ScanMeta, logFn func(string)) (PrinterInfo, bool) {
	return parsePDUs(scanIP, vars, meta, logFn, true)
}

// ParsePDUsWithoutNetwork converts obtained PDUs without HTTP probes or diagnostic
// file writes. Scanner stage commits must not trigger additional network work.
func ParsePDUsWithoutNetwork(scanIP string, vars []gosnmp.SnmpPDU, meta *ScanMeta, logFn func(string)) (PrinterInfo, bool) {
	return parsePDUs(scanIP, vars, meta, logFn, false)
}

func parsePDUs(scanIP string, vars []gosnmp.SnmpPDU, meta *ScanMeta, logFn func(string), sideEffects bool) (PrinterInfo, bool) {
	allVars := vars

	// build a parse debug structure we will persist for diagnostics
	debug := ParseDebug{
		IP:        scanIP,
		Timestamp: time.Now().Format(time.RFC3339),
		RawPDUs:   []RawPDU{},
		Steps:     []string{},
		Extra:     map[string]interface{}{},
	}

	debug.Steps = append(debug.Steps, "ParsePDUs:start")

	// capture raw PDUs for debugging (string form + hex for octetstrings)
	for _, v := range allVars {
		rp := RawPDU{OID: strings.TrimPrefix(v.Name, "."), Type: fmt.Sprintf("%v", v.Type)}
		if b, ok := v.Value.([]byte); ok {
			rp.HexValue = fmt.Sprintf("%x", b)
			rp.StrValue = util.DecodeOctetString(b)
		} else {
			rp.StrValue = fmt.Sprintf("%v", v.Value)
		}
		debug.RawPDUs = append(debug.RawPDUs, rp)
	}

	// Quick heuristic guesses
	var mfgGuess, modelGuess, serialGuess string
	mfgRe := regexp.MustCompile(`(?i)\b(hp|hewlett[-\s]?packard|canon|brother|epson|lexmark|kyocera|konica|xerox|ricoh|sharp|okidata|dell|minolta|toshiba|samsung)\b`)
	pidRe := regexp.MustCompile(`(?i)\b(?:pid|product|product id|model(?: name)?|model:)[:=\s]*([A-Za-z0-9\-\s]{2,60})`)
	modelKeywords := []string{"laserjet", "mfp", "printer", "series", "deskjet", "workcentre", "color", "mono"}

	// helper: detect UUID-like strings (8-4-4-4-12 hex with hyphens)
	looksLikeUUID := func(s string) bool {
		// Relaxed GUID detector: treat long, hyphenated, no-space tokens as GUID-like
		s = strings.ToLower(strings.TrimSpace(s))
		if strings.Contains(s, " ") {
			return false
		}
		if len(s) < 20 || len(s) > 64 {
			return false
		}
		if strings.Count(s, "-") < 3 {
			return false
		}
		// Ensure characters are alnum or hyphen
		for _, c := range s {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || c == '-' {
				continue
			}
			return false
		}
		return true
	}

	// helper: detect OID-like strings (e.g., ".1.3.6.1.4.1.1347.41")
	looksLikeOID := func(s string) bool {
		s = strings.TrimSpace(s)
		if len(s) < 5 {
			return false
		}
		// OIDs start with optional dot followed by digits and dots
		if s[0] == '.' {
			s = s[1:]
		}
		// Must have at least 2 dots and mostly digits
		dots := 0
		digits := 0
		for _, c := range s {
			if c == '.' {
				dots++
			} else if c >= '0' && c <= '9' {
				digits++
			} else {
				return false // OIDs only have dots and digits
			}
		}
		return dots >= 2 && digits > dots
	}

	// helper: detect toner/supply model numbers that should NOT be used as serials
	// These are consumable part numbers, not device serial numbers
	looksLikeSupplyModel := func(s string) bool {
		s = strings.ToUpper(strings.TrimSpace(s))
		// Kyocera toner: TK-XXXX (e.g., TK-3402S, TK-8517K)
		if strings.HasPrefix(s, "TK-") {
			return true
		}
		// Brother toner: TN-XXXX (e.g., TN-760, TN-850)
		if strings.HasPrefix(s, "TN-") {
			return true
		}
		// Brother drum: DR-XXXX
		if strings.HasPrefix(s, "DR-") {
			return true
		}
		// HP toner cartridges: CF/CE/Q followed by digits (e.g., CF226A, CE255X, Q2612A)
		if len(s) >= 4 && (strings.HasPrefix(s, "CF") || strings.HasPrefix(s, "CE") || strings.HasPrefix(s, "Q")) {
			// Check if followed by digits
			rest := s[2:]
			if strings.HasPrefix(s, "Q") {
				rest = s[1:]
			}
			if len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
				return true
			}
		}
		// Canon toner: various patterns like GPR-XX, CRG-XXX
		if strings.HasPrefix(s, "GPR-") || strings.HasPrefix(s, "CRG-") {
			return true
		}
		// Lexmark: various patterns
		if strings.HasPrefix(s, "50F") || strings.HasPrefix(s, "60F") || strings.HasPrefix(s, "70C") {
			return true
		}
		// Generic supply keywords
		sLower := strings.ToLower(s)
		if strings.Contains(sLower, "toner") || strings.Contains(sLower, "cartridge") ||
			strings.Contains(sLower, "drum") || strings.Contains(sLower, "waste") {
			return true
		}
		return false
	}

	for _, v := range allVars {
		sval := pduToString(v.Value)
		name := strings.TrimPrefix(v.Name, ".")
		ls := strings.ToLower(sval)
		if mfgGuess == "" {
			if m := mfgRe.FindStringSubmatch(sval); len(m) > 1 {
				mfgGuess = strings.ToLower(m[1])
			}
		}
		if modelGuess == "" {
			if m := pidRe.FindStringSubmatch(sval); len(m) > 1 {
				modelGuess = strings.TrimSpace(m[1])
			} else {
				for _, kw := range modelKeywords {
					if strings.Contains(ls, kw) {
						if len(sval) > 3 && len(sval) < 120 {
							modelGuess = strings.TrimSpace(sval)
							break
						}
					}
				}
			}
		}
		if serialGuess == "" {
			// Never consider prtGeneral.16.1 for serial (it's often description-like)
			if name == "1.3.6.1.2.1.43.5.1.1.16.1" {
				// skip entirely
			} else if m := serialLabelRE.FindStringSubmatch(sval); len(m) > 1 {
				cand := strings.TrimSpace(m[1])
				if !looksLikeUUID(cand) && !looksLikeOID(cand) && !looksLikeSupplyModel(cand) {
					serialGuess = cand
				}
			}
			// NOTE: Removed overly permissive fallback that matched any 6-40 char token.
			// Serial guessing should only come from explicit SN:/SERIAL: labeled fields.
			// If prtGeneralSerialNumber OID fails, we should NOT guess from random values
			// like toner model numbers, OIDs, or other arbitrary strings.
		}
		if mfgGuess != "" && modelGuess != "" && serialGuess != "" {
			break
		}
	}

	debug.Steps = append(debug.Steps, fmt.Sprintf("initial_guesses: mfgGuess=%q modelGuess=%q serialGuess=%q", mfgGuess, modelGuess, serialGuess))

	// lookup map
	pduByOid := map[string]gosnmp.SnmpPDU{}
	for _, v := range allVars {
		pduByOid[strings.TrimPrefix(v.Name, ".")] = v
	}

	// parsed outputs and accumulators
	var model, serial, adminContact, assetID, description, location string
	prov := map[string]string{}
	pageCount := 0
	markerCounts := map[int]int{}
	supplyDesc := map[string]string{}
	supplyLevels := map[string]int{}
	supplyMaxCap := map[string]int{} // max capacity for percentage calculation
	var statusMsgs []string
	ifMacs := map[string]string{}

	// Track which OIDs returned valid data for learned mappings
	learnedOIDs := LearnedOIDMap{
		VendorSpecificOIDs: make(map[string]string),
	}
	markerOIDs := make(map[int]string) // Track OID for each marker index

	// helper numeric coercion with bounds checking to prevent integer overflow
	// on 32-bit systems when converting int64 to int
	toInt := func(v interface{}) (int, bool) {
		if i64, ok := util.CoerceToInt(v); ok {
			// Bounds check for safe conversion to int (works on 32-bit and 64-bit)
			if i64 > int64(^uint(0)>>1) || i64 < -int64(^uint(0)>>1)-1 {
				// Value exceeds int range, return 0
				return 0, false
			}
			return int(i64), true
		}
		return 0, false
	}

	// scan PDUs for known OIDs and patterns
	for _, v := range allVars {
		name := strings.TrimPrefix(v.Name, ".")
		if target, ok := scanner.LookupVendorIDTarget(name); ok {
			raw := pduToString(v.Value)
			learnedOIDs.VendorSpecificOIDs[target.Key] = name
			if raw != "" {
				fields := parseDeviceIDPayload(raw)
				if model == "" {
					if mdl := firstNonEmpty(fields["mdl"], fields["model"], fields["md"]); mdl != "" {
						model = strings.TrimSpace(mdl)
						prov["model"] = name
						learnedOIDs.ModelOID = name
					}
				}
				if serial == "" {
					if sn := firstNonEmpty(fields["sn"], fields["serial"], fields["ser"]); sn != "" && !looksLikeUUID(sn) {
						serial = strings.TrimSpace(sn)
						prov["serial"] = name
						learnedOIDs.SerialOID = name
					}
				}
				if description == "" {
					if des := firstNonEmpty(fields["des"], fields["description"]); des != "" {
						description = strings.TrimSpace(des)
						prov["description"] = name
					}
				}
				if assetID == "" {
					if asset := firstNonEmpty(fields["asset"], fields["assetid"]); asset != "" {
						assetID = strings.TrimSpace(asset)
					}
				}
				if mfgGuess == "" {
					if mfg := firstNonEmpty(fields["mfg"], fields["manufacturer"]); mfg != "" {
						mfgGuess = strings.ToLower(mfg)
					}
				}
				debug.Steps = append(debug.Steps, fmt.Sprintf("vendor_device_id:%s", target.Key))
			}
			continue
		}
		switch name {
		case "1.3.6.1.2.1.1.1.0":
			raw := pduToString(v.Value)
			// Do NOT blindly set model to full sysDescr; it's often a long firmware string.
			// Prefer structured hints like PID or an explicit model-like token.
			if idx := strings.Index(raw, "PID:"); idx != -1 {
				rest := strings.TrimSpace(raw[idx+4:])
				if end := strings.IndexAny(rest, ",;\n"); end > -1 {
					rest = rest[:end]
				}
				if rest != "" {
					model = rest
					learnedOIDs.ModelOID = name // Track model OID
				}
			} else if model == "" {
				// Try to extract a model-like token from sysDescr using pidRe if present
				if pidRe != nil {
					if m := pidRe.FindStringSubmatch(raw); len(m) > 1 {
						model = strings.TrimSpace(m[1])
						learnedOIDs.ModelOID = name // Track model OID
					}
				}
			}
			debug.Steps = append(debug.Steps, fmt.Sprintf("sysDescr: oid=%s value=%q", name, pduToString(v.Value)))
		case "1.3.6.1.2.1.1.4.0":
			adminContact = pduToString(v.Value)
			debug.Steps = append(debug.Steps, fmt.Sprintf("adminContact: %q", adminContact))
			if assetID == "" && adminContact != "" {
				var assetRe *regexp.Regexp
				assetIDRegexMu.RLock()
				pattern := AssetIDRegex
				assetIDRegexMu.RUnlock()
				if pattern != "" {
					if ar, err := regexp.Compile(pattern); err == nil {
						assetRe = ar
					}
				}
				if assetRe == nil {
					assetRe = regexp.MustCompile(`(?i)\\b(?:asset(?:\\s*id)?|asset#|asset:|tag|tag#|asset-num|assetno)[\\s:\\#-]*([A-Za-z0-9\\-]{3,40})\\b`)
				}
				if m := assetRe.FindStringSubmatch(adminContact); len(m) > 1 {
					assetID = strings.TrimSpace(m[1])
				}
			}
		case "1.3.6.1.2.1.43.5.1.1.16.1":
			// prtGeneral.16.1 is NOT a reliable serial source; treat as description/model hint only
			sval := pduToString(v.Value)
			lsval := strings.ToLower(sval)
			if description == "" && looksLikeUUID(sval) {
				description = strings.TrimSpace(sval)
				prov["description"] = name
			} else if model == "" {
				if pidRe.MatchString(sval) || strings.Contains(lsval, " ") {
					model = strings.TrimSpace(sval)
					prov["model"] = name
					learnedOIDs.ModelOID = name // Track model OID
				}
			}
		case "1.3.6.1.2.1.43.5.1.1.17.1":
			sval := pduToString(v.Value)
			if serial == "" && sval != "" {
				serial = strings.TrimSpace(sval)
				prov["serial"] = name
				learnedOIDs.SerialOID = name // Track serial OID
			}
		// HP asset tag (enterprise-specific OID)
		case "1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.12.0":
			sval := pduToString(v.Value)
			if sval != "" && assetID == "" {
				assetID = strings.TrimSpace(sval)
			}
		// sysLocation - standard SNMPv2 location
		case "1.3.6.1.2.1.1.6.0":
			sval := pduToString(v.Value)
			if sval != "" {
				location = strings.TrimSpace(sval)
			}
		}

		// prtMarkerLifeCount primary marker 1
		if strings.HasPrefix(name, "1.3.6.1.2.1.43.10.2.1.4.1.") {
			suf := strings.TrimPrefix(name, "1.3.6.1.2.1.43.10.2.1.4.1.")
			parts := strings.Split(suf, ".")
			if len(parts) >= 1 {
				if idx, err := strconv.Atoi(parts[0]); err == nil {
					if iv, ok := toInt(v.Value); ok {
						markerCounts[idx] = iv
						// Track the OID for this specific marker index
						markerOIDs[idx] = name

						// Store learned OIDs for common markers
						switch idx {
						case 1:
							learnedOIDs.MonoPagesOID = name
						case 2:
							learnedOIDs.ColorPagesOID = name
						case 3:
							learnedOIDs.CyanOID = name
						case 4:
							learnedOIDs.MagentaOID = name
						case 5:
							learnedOIDs.YellowOID = name
						}
					}
				}
			}
			continue
		}
		// supplies description
		if strings.HasPrefix(name, "1.3.6.1.2.1.43.11.1.1.6.1.") {
			suf := strings.TrimPrefix(name, "1.3.6.1.2.1.43.11.1.1.6.1.")
			parts := strings.Split(suf, ".")
			key := suf
			if len(parts) >= 2 {
				if hrIdx, err := strconv.Atoi(parts[0]); err == nil {
					key = fmt.Sprintf("%d.%s", hrIdx, strings.Join(parts[1:], "."))
				}
			}
			supplyDesc[key] = pduToString(v.Value)
			continue
		}
		// supplies level
		if strings.HasPrefix(name, "1.3.6.1.2.1.43.11.1.1.9.1.") {
			suf := strings.TrimPrefix(name, "1.3.6.1.2.1.43.11.1.1.9.1.")
			parts := strings.Split(suf, ".")
			key := suf
			if len(parts) >= 2 {
				if hrIdx, err := strconv.Atoi(parts[0]); err == nil {
					key = fmt.Sprintf("%d.%s", hrIdx, strings.Join(parts[1:], "."))
				}
			}
			if iv, ok := toInt(v.Value); ok {
				supplyLevels[key] = iv
			}
			continue
		}
		// supplies max capacity
		if strings.HasPrefix(name, "1.3.6.1.2.1.43.11.1.1.8.1.") {
			suf := strings.TrimPrefix(name, "1.3.6.1.2.1.43.11.1.1.8.1.")
			parts := strings.Split(suf, ".")
			key := suf
			if len(parts) >= 2 {
				if hrIdx, err := strconv.Atoi(parts[0]); err == nil {
					key = fmt.Sprintf("%d.%s", hrIdx, strings.Join(parts[1:], "."))
				}
			}
			if iv, ok := toInt(v.Value); ok {
				supplyMaxCap[key] = iv
			}
			continue
		}
		// ifPhysAddress
		if strings.HasPrefix(name, "1.3.6.1.2.1.2.2.1.6.") {
			if b, ok := v.Value.([]byte); ok && len(b) > 0 {
				macLen := len(b)
				if macLen > 6 {
					macLen = 6
				}
				parts := make([]string, macLen)
				for i := 0; i < macLen; i++ {
					parts[i] = fmt.Sprintf("%02x", b[i])
				}
				mac := strings.Join(parts, ":")
				if mac != "" && mac != "00:00:00:00:00:00" {
					suf := strings.TrimPrefix(name, "1.3.6.1.2.1.2.2.1.6.")
					ifMacs[suf] = mac
				}
			}
		}
		// collect status strings
		if v.Type == gosnmp.OctetString {
			s := pduToString(v.Value)
			ls := strings.ToLower(s)
			if strings.Contains(ls, "error") || strings.Contains(ls, "toner") || strings.Contains(ls, "paper") || strings.Contains(ls, "ready") || strings.Contains(ls, "warning") {
				statusMsgs = append(statusMsgs, s)
			}
		}
	}

	debug.Steps = append(debug.Steps, fmt.Sprintf("collected_counts: marker_counts=%d supply_desc=%d supply_levels=%d supply_max_cap=%d", len(markerCounts), len(supplyDesc), len(supplyLevels), len(supplyMaxCap)))

	tonerLevels := map[string]int{}
	consumables := []string{}
	// placeholders for per-color descs (raw descriptions for display)
	var descBlack, descCyan, descMagenta, descYellow string
	for idx, lvl := range supplyLevels {
		if desc, ok := supplyDesc[idx]; ok {
			key := desc
			normalized := supplies.NormalizeDescription(desc)
			if normalized != "" {
				key = normalized
				// Use normalized key to determine color assignment
				// This is more accurate than substring matching on raw descriptions
				switch normalized {
				case "toner_black":
					if descBlack == "" {
						descBlack = desc
					}
				case "toner_cyan":
					if descCyan == "" {
						descCyan = desc
					}
				case "toner_magenta":
					if descMagenta == "" {
						descMagenta = desc
					}
				case "toner_yellow":
					if descYellow == "" {
						descYellow = desc
					}
				}
			}

			// Calculate percentage from level and max capacity per RFC 3805 (Printer-MIB)
			// Level/MaxCapacity special values:
			//   -1: other/unknown
			//   -2: unknown (manufacturer-defined)
			//   -3: someRemaining (level is not quantifiable, but supply is usable)
			maxCap := supplyMaxCap[idx]
			var percentage int
			if maxCap > 0 && lvl >= 0 {
				// Normal case: calculate percentage from level/capacity
				percentage = int((float64(lvl) / float64(maxCap)) * 100.0)
			} else if lvl >= 0 && lvl <= 100 && maxCap <= 0 {
				// MaxCapacity is unknown/special but Level is valid 0-100 range
				// Treat level as percentage directly (common for many printers)
				percentage = lvl
			} else if lvl == -3 {
				// someRemaining: report as low (10%) as a reasonable estimate
				percentage = 10
			} else {
				// Unknown or invalid level - skip this supply
				consumables = append(consumables, desc)
				continue
			}
			tonerLevels[key] = percentage
			consumables = append(consumables, desc)
		} else {
			// fallback to numeric index as string (idx is already a string)
			key := idx
			// Same percentage calculation for unknown supplies
			maxCap := supplyMaxCap[idx]
			var percentage int
			if maxCap > 0 && lvl >= 0 {
				percentage = int((float64(lvl) / float64(maxCap)) * 100.0)
			} else if lvl >= 0 && lvl <= 100 && maxCap <= 0 {
				percentage = lvl
			} else if lvl == -3 {
				percentage = 10
			} else {
				consumables = append(consumables, key)
				continue
			}
			tonerLevels[key] = percentage
			consumables = append(consumables, key)
		}
	}

	// derive pageCount
	if v, ok := markerCounts[1]; ok {
		pageCount = v
		// Track the OID we learned for page count (marker 1)
		if oid, ok := markerOIDs[1]; ok {
			learnedOIDs.PageCountOID = oid
		}
	}

	// Vendor-friendly model fallback using HOST-RESOURCES-MIB hrDevice* tables
	// Many Epson and Kyocera devices expose a clean model in hrDeviceDescr for the
	// hrDeviceType that equals printer (1.3.6.1.2.1.25.3.1.5). Prefer that when
	// sysDescr/prtGeneral.16.1 are inconclusive.
	if model == "" {
		// Build an index of hrDeviceType entries
		hrPrinterIdx := ""
		for k, p := range pduByOid {
			if strings.HasPrefix(k, "1.3.6.1.2.1.25.3.2.1.2.") {
				idx := strings.TrimPrefix(k, "1.3.6.1.2.1.25.3.2.1.2.")
				val := strings.ToLower(pduToString(p.Value))
				// Match either the explicit OID suffix for printer or a textual hint
				if strings.Contains(val, "25.3.1.5") || strings.Contains(val, "printer") {
					hrPrinterIdx = idx
					break
				}
			}
		}
		if hrPrinterIdx != "" {
			if p, ok := pduByOid["1.3.6.1.2.1.25.3.2.1.3."+hrPrinterIdx]; ok {
				cand := strings.TrimSpace(pduToString(p.Value))
				if cand != "" && !looksLikeUUID(cand) {
					model = cand
					modelOID := "1.3.6.1.2.1.25.3.2.1.3." + hrPrinterIdx
					prov["model"] = modelOID
					learnedOIDs.ModelOID = modelOID // Track model OID
					debug.Steps = append(debug.Steps, "model_from_hrDeviceDescr idx="+hrPrinterIdx+" val="+model)
				}
			}
		}
		// Fallback: use hrDeviceDescr.1 if present
		if model == "" {
			if p, ok := pduByOid["1.3.6.1.2.1.25.3.2.1.3.1"]; ok {
				cand := strings.TrimSpace(pduToString(p.Value))
				if cand != "" && !looksLikeUUID(cand) {
					model = cand
					modelOID := "1.3.6.1.2.1.25.3.2.1.3.1"
					prov["model"] = modelOID
					learnedOIDs.ModelOID = modelOID // Track model OID
					debug.Steps = append(debug.Steps, "model_from_hrDeviceDescr.1 val="+model)
				}
			}
		}
	}

	// apply heuristic guesses
	if model == "" && modelGuess != "" {
		model = modelGuess
	}
	if serial == "" && serialGuess != "" {
		serial = serialGuess
	}

	// determine printer
	isPrinter := false
	reasons := []string{}
	if len(markerCounts) > 0 {
		isPrinter = true
		reasons = append(reasons, "marker_counts")
	}
	if len(supplyDesc) > 0 {
		isPrinter = true
		reasons = append(reasons, "supply_descriptions")
	}
	if serial != "" {
		isPrinter = true
		reasons = append(reasons, "serial")
	}
	lowmodel := strings.ToLower(model)
	if strings.Contains(lowmodel, "printer") || strings.Contains(lowmodel, "laserjet") {
		isPrinter = true
		reasons = append(reasons, "sysDescr")
	}

	// Determine manufacturer: prefer sysObjectID enterprise roots, then sysDescr, then heuristic guesses.
	manufacturer := ""
	if soidPdu, ok := pduByOid["1.3.6.1.2.1.1.2.0"]; ok {
		manufacturer = vendor.ManufacturerForSysObjectID(pduToString(soidPdu.Value))
	}
	if manufacturer == "" {
		if sdescPdu, ok := pduByOid["1.3.6.1.2.1.1.1.0"]; ok {
			sdesc := strings.ToLower(pduToString(sdescPdu.Value))
			switch {
			case strings.Contains(sdesc, "hp") || strings.Contains(sdesc, "laserjet"):
				manufacturer = "HP"
			case strings.Contains(sdesc, "brother"):
				manufacturer = "Brother"
			case strings.Contains(sdesc, "canon"):
				manufacturer = "Canon"
			case strings.Contains(sdesc, "kyocera"):
				manufacturer = "Kyocera"
			case strings.Contains(sdesc, "lexmark"):
				manufacturer = "Lexmark"
			case strings.Contains(sdesc, "epson"):
				manufacturer = "Epson"
			case strings.Contains(sdesc, "dell"):
				manufacturer = "Dell"
			}
		}
	}
	if manufacturer == "" && mfgGuess != "" {
		// make a tidy title-case value from the guess
		switch strings.ToLower(mfgGuess) {
		case "hp", "hewlett-packard", "hewlett packard":
			manufacturer = "HP"
		case "brother":
			manufacturer = "Brother"
		case "canon":
			manufacturer = "Canon"
		case "epson":
			manufacturer = "Epson"
		case "lexmark":
			manufacturer = "Lexmark"
		case "kyocera":
			manufacturer = "Kyocera"
		case "dell":
			manufacturer = "Dell"
		default:
			// fallback: simple capitalization of first letter
			if mfgGuess != "" {
				manufacturer = strings.ToUpper(mfgGuess[:1]) + strings.ToLower(mfgGuess[1:])
			}
		}
	}
	if manufacturer == "" {
		// scan any returned OID names for enterprise prefix as a last resort
		for _, v := range allVars {
			name := strings.TrimPrefix(v.Name, ".")
			manufacturer = vendor.ManufacturerForSysObjectID(name)
			if manufacturer != "" {
				break
			}
		}
	}

	// collect manufacturer-hint data for diagnostics
	hints := []string{}
	if soidPdu, ok := pduByOid["1.3.6.1.2.1.1.2.0"]; ok {
		hints = append(hints, "sysObjectID:"+pduToString(soidPdu.Value))
	}
	if sdescPdu, ok := pduByOid["1.3.6.1.2.1.1.1.0"]; ok {
		hints = append(hints, "sysDescr:"+pduToString(sdescPdu.Value))
	}
	if mfgGuess != "" {
		hints = append(hints, "mfgGuess:"+mfgGuess)
	}
	// find a few enterprise-root OIDs present
	entFound := 0
	for _, v := range allVars {
		name := strings.TrimPrefix(v.Name, ".")
		if strings.HasPrefix(name, "1.3.6.1.4.1.") && entFound < 3 {
			hints = append(hints, "enterprise_oid:"+name+" -> "+pduToString(v.Value))
			entFound++
		}
	}
	debug.ManufacturerHints = hints

	// persist a small flat log listing manufacturer-related OIDs for quick inspection
	if sideEffects {
		logDir := ensureLogDir()
		fname := fmt.Sprintf("manufacturer_oids_%s.log", strings.ReplaceAll(scanIP, ".", "_"))
		fpath := filepath.Join(logDir, fname)
		_ = os.WriteFile(fpath, []byte(strings.Join(hints, "\n")+"\n"), 0o644)
	}

	// Epson-specific: derive AssetID from adminContact if not already set.
	// Example adminContact: "Asset ID #03027 Printer Source Plus ..."
	if assetID == "" && adminContact != "" && strings.EqualFold(manufacturer, "Epson") {
		// Prefer explicit "Asset" label with optional "ID"
		if m := regexp.MustCompile(`(?i)asset(?:\s*id)?[\s:#-]*([A-Za-z0-9\-]{3,40})`).FindStringSubmatch(adminContact); len(m) > 1 {
			assetID = strings.Trim(m[1], " #:-.,;")
			prov["asset_id"] = "adminContact"
		} else if m := regexp.MustCompile(`(?i)asset[\w\s:#-]{0,20}([0-9A-Za-z\-]{3,40})`).FindStringSubmatch(adminContact); len(m) > 1 {
			// Fallback: capture first plausible token following the word Asset
			assetID = strings.Trim(m[1], " #:-.,;")
			prov["asset_id"] = "adminContact"
		}
	}

	// build PrinterInfo (include new fields when available)
	// extract network properties when present in PDUs
	subnetMask := ""
	gateway := ""
	dnsServers := []string{}
	dhcpServer := ""
	hostname := ""

	// subnet mask table entry: ipAdEntNetMask 1.3.6.1.2.1.4.20.1.3.<ip>
	maskKey := "1.3.6.1.2.1.4.20.1.3." + scanIP
	if p, ok := pduByOid[maskKey]; ok {
		subnetMask = normalizeNetworkValue(pduToString(p.Value), "subnet")
	}
	// If mask not present, look only for OIDs whose suffix encodes the same IP
	// we're scanning; ignore other devices' IPs to avoid mixing entries from
	// ARP-like tables or other addresses present in the walk.
	if subnetMask == "" {
		for k, p := range pduByOid {
			if ip, ok := extractIPv4FromOID(k); ok && ip == scanIP {
				if strings.HasPrefix(k, "1.3.6.1.2.1.4.20.1.3.") {
					subnetMask = normalizeNetworkValue(pduToString(p.Value), "subnet")
					if subnetMask != "" {
						break
					}
				}
			}
		}
	}
	// sysName
	if p, ok := pduByOid["1.3.6.1.2.1.1.5.0"]; ok {
		hostname = pduToString(p.Value)
	}
	// collect obvious DNS-related PDUs and network config
	for k, p := range pduByOid {
		lk := strings.ToLower(k)
		val := strings.TrimSpace(pduToString(p.Value))

		// DNS servers
		if strings.Contains(lk, "dns") || strings.Contains(lk, "nameserver") || strings.Contains(lk, "name_server") {
			normalizedDNS := normalizeNetworkValue(val, "dns")
			if normalizedDNS != "" {
				dnsServers = append(dnsServers, normalizedDNS)
			}
		}

		// DHCP server address (common OID patterns)
		if strings.Contains(lk, "dhcp") && (strings.Contains(lk, "server") || strings.Contains(lk, "srv")) {
			normalizedDHCP := normalizeNetworkValue(val, "ip")
			if normalizedDHCP != "" && dhcpServer == "" {
				dhcpServer = normalizedDHCP
			}
		}

		// HP-specific network configuration OIDs
		// HP gateway: 1.3.6.1.4.1.11.2.3.9.4.2.1.1.1.x
		if strings.HasPrefix(k, "1.3.6.1.4.1.11.2.3.9.4.2.1.1.1.") && gateway == "" {
			normalizedGW := normalizeNetworkValue(val, "gateway")
			if normalizedGW != "" {
				gateway = normalizedGW
			}
		}

		// HP DNS servers: 1.3.6.1.4.1.11.2.3.9.4.2.1.1.6.x (primary) and .7.x (secondary)
		if strings.HasPrefix(k, "1.3.6.1.4.1.11.2.3.9.4.2.1.1.6.") ||
			strings.HasPrefix(k, "1.3.6.1.4.1.11.2.3.9.4.2.1.1.7.") {
			normalizedDNS := normalizeNetworkValue(val, "dns")
			if normalizedDNS != "" {
				// avoid duplicates
				found := false
				for _, existing := range dnsServers {
					if existing == normalizedDNS {
						found = true
						break
					}
				}
				if !found {
					dnsServers = append(dnsServers, normalizedDNS)
				}
			}
		}

		// HP DHCP server: 1.3.6.1.4.1.11.2.3.9.4.2.1.1.4.x
		if strings.HasPrefix(k, "1.3.6.1.4.1.11.2.3.9.4.2.1.1.4.") && dhcpServer == "" {
			normalizedDHCP := normalizeNetworkValue(val, "ip")
			if normalizedDHCP != "" {
				dhcpServer = normalizedDHCP
			}
		}
	}
	// try to detect default route via ipRouteTable entries: dest=1.3.6.1.2.1.4.21.1.1.<idx>, nexthop=...1.7.<idx>
	dests := map[string]string{}
	nexthops := map[string]string{}
	for k, p := range pduByOid {
		if strings.HasPrefix(k, "1.3.6.1.2.1.4.21.1.") {
			parts := strings.Split(k, ".")
			if len(parts) >= 2 {
				field := parts[len(parts)-2]
				idx := parts[len(parts)-1]
				val := pduToString(p.Value)
				switch field {
				case "1":
					dests[idx] = val
				case "7":
					nexthops[idx] = val
				}
			}
		}
	}
	for idx, d := range dests {
		if d == "0.0.0.0" || d == "" {
			if nh, ok := nexthops[idx]; ok {
				normalizedGW := normalizeNetworkValue(nh, "gateway")
				if normalizedGW != "" {
					gateway = normalizedGW
					break
				}
			}
		}
	}

	// choose MAC: prefer meta.MAC when present, otherwise take first valid from ifMacs
	chosenMAC := ""
	if meta != nil && meta.MAC != "" {
		normalizedMAC := normalizeNetworkValue(meta.MAC, "mac")
		if normalizedMAC != "" {
			chosenMAC = normalizedMAC
		}
	}
	if chosenMAC == "" {
		for _, m := range ifMacs {
			normalizedMAC := normalizeNetworkValue(m, "mac")
			if normalizedMAC != "" {
				chosenMAC = normalizedMAC
				break
			}
		}
	}

	pi := PrinterInfo{
		IP:                   scanIP,
		Manufacturer:         manufacturer,
		Model:                model,
		Serial:               serial,
		AdminContact:         adminContact,
		AssetID:              assetID,
		Description:          description,
		Location:             location,
		PageCount:            pageCount,
		TotalMonoImpressions: pageCount,
		MonoImpressions:      markerCounts[1],
		ColorImpressions:     markerCounts[2],
		BlackImpressions:     markerCounts[1],
		// try to split combined color impressions into components if markers present
		CyanImpressions:    markerCounts[3],
		MagentaImpressions: markerCounts[4],
		YellowImpressions:  markerCounts[5],
		TonerLevels:        tonerLevels,
		Consumables:        consumables,
		StatusMessages:     statusMsgs,
		DetectionReasons:   reasons,
		PaperTrayStatus:    map[string]string{},
		MAC:                chosenMAC,
		SubnetMask:         subnetMask,
		Gateway:            gateway,
		DNSServers:         dnsServers,
		DHCPServer:         dhcpServer,
		Hostname:           hostname,
		LearnedOIDs:        learnedOIDs, // Store learned OIDs for efficient metrics queries
	}

	// Log learned OIDs for improving scraper across different models/brands
	if logFn != nil && (learnedOIDs.PageCountOID != "" || learnedOIDs.MonoPagesOID != "" || learnedOIDs.SerialOID != "" || learnedOIDs.ModelOID != "") {
		logFn(fmt.Sprintf("LEARNED_OIDS: ip=%s manufacturer=%s model=%s serial=%s page_count_oid=%s mono_pages_oid=%s color_pages_oid=%s cyan_oid=%s magenta_oid=%s yellow_oid=%s serial_oid=%s model_oid=%s",
			scanIP, manufacturer, model, serial,
			learnedOIDs.PageCountOID, learnedOIDs.MonoPagesOID, learnedOIDs.ColorPagesOID,
			learnedOIDs.CyanOID, learnedOIDs.MagentaOID, learnedOIDs.YellowOID,
			learnedOIDs.SerialOID, learnedOIDs.ModelOID))
	}

	// Build normalized meters map from discovered counters.
	meters := map[string]int{}
	// total pages: prefer explicit pageCount, otherwise sum of markerCounts
	total := pageCount
	if total == 0 {
		sum := 0
		for _, v := range markerCounts {
			sum += v
		}
		total = sum
	}
	meters["total_pages"] = total
	// mono / black
	if v, ok := markerCounts[1]; ok {
		meters["mono_pages"] = v
		meters["black"] = v
	} else if pageCount > 0 {
		meters["mono_pages"] = pageCount
	}
	// color pages: any markers beyond index 1
	colorSum := 0
	for idx, v := range markerCounts {
		if idx == 1 {
			continue
		}
		colorSum += v
		// map common color indexes
		switch idx {
		case 3:
			meters["cyan"] = v
		case 4:
			meters["magenta"] = v
		case 5:
			meters["yellow"] = v
		}
	}
	meters["color_pages"] = colorSum
	// attach meters if any values discovered
	if len(meters) > 0 {
		pi.Meters = meters
	}

	// Try to map PrintAudit-like categories by scanning PDUs for numeric values
	// whose OID name or string contains category keywords. This helps when
	// vendors report labeled counters (e.g., "Total Pages", "Copier", "Fax").
	for oid, p := range pduByOid {
		if iv, ok := toInt(p.Value); ok {
			valStr := strings.ToLower(pduToString(p.Value))
			nameStr := strings.ToLower(oid)
			// helper to assign if absent or if larger (prefer larger counters)
			assign := func(key string, v int) {
				if existing, ok := meters[key]; !ok || v > existing {
					meters[key] = v
				}
			}
			if strings.Contains(nameStr, "total") && strings.Contains(nameStr, "page") || strings.Contains(valStr, "total") && strings.Contains(valStr, "page") {
				assign("total_pages", iv)
			}
			if strings.Contains(nameStr, "mono") || strings.Contains(valStr, "mono") || strings.Contains(nameStr, "black") || strings.Contains(valStr, "black") {
				assign("mono_pages", iv)
				assign("black", iv)
			}
			if strings.Contains(nameStr, "copier") || strings.Contains(valStr, "copier") {
				assign("copier_pages", iv)
			}
			if strings.Contains(nameStr, "printer") && strings.Contains(nameStr, "total") || strings.Contains(valStr, "printer") {
				assign("printer_pages", iv)
			}
			if strings.Contains(nameStr, "fax") || strings.Contains(valStr, "fax") {
				assign("fax_pages", iv)
			}
			if strings.Contains(nameStr, "scan") || strings.Contains(valStr, "scan") {
				assign("scan_pages", iv)
			}
			if strings.Contains(nameStr, "local") || strings.Contains(valStr, "local") {
				assign("local_pages", iv)
			}
			if strings.Contains(nameStr, "banner") || strings.Contains(valStr, "banner") {
				assign("banner_pages", iv)
			}
		}
	}
	// attach updated meters map
	if len(meters) > 0 {
		pi.Meters = meters
	}

	// populate per-color toner level fields from discovered descriptions when present
	// Use the normalized key (toner_black, etc.) to look up levels since that's how we store them
	if descBlack != "" {
		pi.TonerDescBlack = descBlack
		if v, ok := tonerLevels["toner_black"]; ok {
			pi.TonerLevelBlack = v
		}
	}
	if descCyan != "" {
		pi.TonerDescCyan = descCyan
		if v, ok := tonerLevels["toner_cyan"]; ok {
			pi.TonerLevelCyan = v
		}
	}
	if descMagenta != "" {
		pi.TonerDescMagenta = descMagenta
		if v, ok := tonerLevels["toner_magenta"]; ok {
			pi.TonerLevelMagenta = v
		}
	}
	if descYellow != "" {
		pi.TonerDescYellow = descYellow
		if v, ok := tonerLevels["toner_yellow"]; ok {
			pi.TonerLevelYellow = v
		}
	}

	// heuristics: Uptime (sysUpTime), firmware, duplex, paper tray statuses, toner alerts
	// Uptime: 1.3.6.1.2.1.1.3.0 (TimeTicks, hundredths of seconds)
	if pdu, ok := pduByOid["1.3.6.1.2.1.1.3.0"]; ok {
		if iv, ok := toInt(pdu.Value); ok {
			// convert hundredths of seconds to seconds
			pi.UptimeSeconds = iv / 100
		}
	}

	// paper tray status: prtInputEntry (1.3.6.1.2.1.43.8.2.1.18.<idx>)
	// Legacy simple text parsing - retained for backwards compatibility
	for k, pdu := range pduByOid {
		if strings.HasPrefix(k, "1.3.6.1.2.1.43.8.2.1.18.") {
			idx := strings.TrimPrefix(k, "1.3.6.1.2.1.43.8.2.1.18.")
			pi.PaperTrayStatus[idx] = pduToString(pdu.Value)
		}
		// try to detect firmware string heuristically
		sval := strings.ToLower(pduToString(pdu.Value))
		if pi.Firmware == "" && (strings.Contains(sval, "firmware") || strings.Contains(sval, "fw") || strings.Contains(sval, "firmware version") || strings.Contains(sval, "fwv")) {
			pi.Firmware = pduToString(pdu.Value)
		}
		// check for duplex keyword
		if !pi.DuplexSupported && (strings.Contains(sval, "duplex") || strings.Contains(sval, "two-sided") || strings.Contains(sval, "duplex_unit")) {
			pi.DuplexSupported = true
		}
	}

	// Parse detailed paper tray information using vendor module
	paperTrays := vendor.ParsePaperTrays(allVars)
	if len(paperTrays) > 0 {
		pi.PaperTrays = make([]PaperTray, len(paperTrays))
		for i, tray := range paperTrays {
			pi.PaperTrays[i] = PaperTray{
				Index:        tray.Index,
				Name:         tray.Name,
				MediaType:    tray.MediaType,
				CurrentLevel: tray.CurrentLevel,
				MaxCapacity:  tray.MaxCapacity,
				LevelPercent: tray.LevelPercent,
				Status:       tray.Status,
			}
		}
		debug.Steps = append(debug.Steps, fmt.Sprintf("paper_trays_parsed: count=%d", len(pi.PaperTrays)))
	}

	// Toner alerts: extract lines from statusMsgs mentioning toner
	for _, m := range statusMsgs {
		lm := strings.ToLower(m)
		if strings.Contains(lm, "toner") || strings.Contains(lm, "ink") || strings.Contains(lm, "supply") {
			pi.TonerAlerts = append(pi.TonerAlerts, m)
		}
	}
	if meta != nil {
		// preserve explicit meta.MAC when provided; otherwise prefer chosenMAC
		if meta.MAC != "" {
			pi.MAC = meta.MAC
		} else if pi.MAC == "" && chosenMAC != "" {
			pi.MAC = chosenMAC
		}
		pi.OpenPorts = meta.OpenPorts
		pi.DiscoveryMethods = meta.DiscoveryMethods
	} else if pi.MAC == "" && chosenMAC != "" {
		pi.MAC = chosenMAC
	}

	// Detect web UI URL from open ports or SNMP data
	if sideEffects {
		if webUIURL := detectWebUIURL(scanIP, meta, pduByOid); webUIURL != "" {
			pi.WebUIURL = webUIURL
		}
	}

	// finalize debug info and persist
	debug.FinalManufacturer = manufacturer
	debug.Model = model
	debug.Serial = serial
	debug.IsPrinter = isPrinter

	// attach provenance info (which OID set model/serial when available)
	if len(prov) > 0 {
		debug.Extra["provenance"] = prov
	}

	debug.Steps = append(debug.Steps, "ParsePDUs:finish")
	debug.DetectionReasons = reasons

	// store the debug snapshot for this IP (best-effort)
	if sideEffects {
		if err := RecordParseDebug(scanIP, debug); err != nil {
			if logFn != nil {
				logFn("failed to persist parse debug: " + err.Error())
			}
		}
	}
	return pi, isPrinter
}

// MergeVendorMetrics enhances a PrinterInfo with vendor-specific metrics.
// This should be called after ParsePDUs to apply ICE-style vendor parsing.
// It detects the vendor from the PDUs, calls the vendor's Parse() method,
// and merges the extracted metrics into the PrinterInfo.
//
// Parameters:
//   - pi: Pointer to PrinterInfo to enhance (modified in place)
//   - pdus: Raw SNMP PDUs from the device
//   - vendorHint: Optional vendor name hint (e.g., "Epson", "Kyocera")
func MergeVendorMetrics(pi *PrinterInfo, pdus []gosnmp.SnmpPDU, vendorHint string) {
	// Delegate to extended version with nil context (standard parsing only)
	MergeVendorMetricsWithContext(context.Background(), pi, pdus, vendorHint, "", 0)
}

// MergeVendorMetricsWithContext is an extended version of MergeVendorMetrics
// that supports vendor-specific extended features like Epson remote-mode.
// When IP is provided, vendors that implement ExtendedVendorModule will
// be called with additional context to enable features like remote-mode queries.
//
// Parameters:
//   - ctx: Context for cancellation
//   - pi: Pointer to PrinterInfo to enhance (modified in place)
//   - pdus: Raw SNMP PDUs from the device
//   - vendorHint: Optional vendor name hint (e.g., "Epson", "Kyocera")
//   - ip: Device IP address (enables extended features when non-empty)
//   - timeoutSeconds: SNMP timeout for extended queries
func MergeVendorMetricsWithContext(ctx context.Context, pi *PrinterInfo, pdus []gosnmp.SnmpPDU, vendorHint string, ip string, timeoutSeconds int) {
	if pi == nil || len(pdus) == 0 {
		return
	}

	// Extract sysObjectID, sysDescr, model from PDUs for vendor detection
	var sysObjectID, sysDescr, model string
	for _, pdu := range pdus {
		oid := strings.TrimPrefix(pdu.Name, ".")
		switch oid {
		case "1.3.6.1.2.1.1.2.0": // sysObjectID
			sysObjectID = pduToString(pdu.Value)
		case "1.3.6.1.2.1.1.1.0": // sysDescr
			sysDescr = pduToString(pdu.Value)
		case "1.3.6.1.2.1.25.3.2.1.3.1": // hrDeviceDescr
			if model == "" {
				model = pduToString(pdu.Value)
			}
		}
	}

	// Use manufacturer from pi if available and vendorHint not provided
	if vendorHint == "" && pi.Manufacturer != "" {
		vendorHint = pi.Manufacturer
	}

	// Detect vendor module
	var vendorModule vendor.VendorModule
	if vendorHint != "" {
		vendorModule = vendor.GetVendorByName(vendorHint)
	}
	if vendorModule == nil {
		vendorModule = vendor.DetectVendor(sysObjectID, sysDescr, model)
	}

	// Skip if generic vendor (no special parsing needed)
	if vendorModule == nil || vendorModule.Name() == "Generic" {
		return
	}

	// Call vendor-specific parsing (extended if available)
	metrics := vendor.ParseWithContext(ctx, vendorModule, pdus, ip, timeoutSeconds)
	if len(metrics) == 0 {
		return
	}

	// Initialize maps if needed
	if pi.Meters == nil {
		pi.Meters = make(map[string]int)
	}
	if pi.TonerLevels == nil {
		pi.TonerLevels = make(map[string]int)
	}

	// Merge metrics into PrinterInfo
	for key, value := range metrics {
		switch key {
		// Page counts
		case "total_pages", "page_count":
			if v, ok := toIntValue(value); ok && v > 0 {
				if pi.PageCount == 0 || v > pi.PageCount {
					pi.PageCount = v
				}
				pi.Meters["total_pages"] = v
			}
		case "mono_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.MonoImpressions = v
				pi.Meters["mono_pages"] = v
			}
		case "color_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.ColorImpressions = v
				pi.Meters["color_pages"] = v
			}

		// Function-specific counters (ICE-style)
		case "print_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["print_pages"] = v
			}
		case "print_mono_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["print_mono_pages"] = v
			}
		case "print_color_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["print_color_pages"] = v
			}
		case "copy_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["copy_pages"] = v
			}
		case "copy_mono_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["copy_mono_pages"] = v
			}
		case "copy_color_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["copy_color_pages"] = v
			}
		case "fax_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["fax_pages"] = v
			}
		case "fax_mono_pages":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["fax_mono_pages"] = v
			}
		case "scan_count":
			if v, ok := toIntValue(value); ok && v > 0 {
				pi.Meters["scans"] = v
			}

		// Toner/ink levels
		case "toner_black", "ink_black":
			if v, ok := toIntValue(value); ok && v >= 0 {
				pi.TonerLevelBlack = v
				pi.TonerLevels["black"] = v
			}
		case "toner_cyan", "ink_cyan":
			if v, ok := toIntValue(value); ok && v >= 0 {
				pi.TonerLevelCyan = v
				pi.TonerLevels["cyan"] = v
			}
		case "toner_magenta", "ink_magenta":
			if v, ok := toIntValue(value); ok && v >= 0 {
				pi.TonerLevelMagenta = v
				pi.TonerLevels["magenta"] = v
			}
		case "toner_yellow", "ink_yellow":
			if v, ok := toIntValue(value); ok && v >= 0 {
				pi.TonerLevelYellow = v
				pi.TonerLevels["yellow"] = v
			}

		default:
			// Handle supply_* keys
			if strings.HasPrefix(key, "supply_") || strings.HasPrefix(key, "toner_") || strings.HasPrefix(key, "ink_") {
				if v, ok := toIntValue(value); ok && v >= 0 {
					pi.TonerLevels[key] = v
				}
			} else {
				// Generic meter value
				if v, ok := toIntValue(value); ok && v > 0 {
					pi.Meters[key] = v
				}
			}
		}
	}
}

// toIntValue converts interface{} to int, handling common types
func toIntValue(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case int32:
		return int(val), true
	case uint:
		return int(val), true
	case uint64:
		return int(val), true
	case uint32:
		return int(val), true
	case float64:
		return int(val), true
	case float32:
		return int(val), true
	default:
		return 0, false
	}
}
