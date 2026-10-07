package scanner

import (
	"context"
	"fmt"
	"strings"

	"printmaster/agent/scanner/capabilities"
	"printmaster/agent/scanner/vendor"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// QueryProfile defines what OIDs to query during SNMP operations.
// Different profiles optimize for different use cases (speed vs. completeness).
type QueryProfile int

const (
	// QueryMinimal queries only serial number OIDs.
	// Fastest option - use for quick serial lookup.
	// Replaces: QuickGetSerial function
	QueryMinimal QueryProfile = iota

	// QueryEssential queries serial + toner + pages + status.
	// Fast targeted queries for known devices.
	// Replaces: QuickRefreshDevice function
	QueryEssential

	// QueryFull performs a full diagnostic walk of all MIB trees.
	// Comprehensive - use for new device discovery and detailed diagnostics.
	// Replaces: RefreshDevice function
	QueryFull

	// QueryMetrics queries vendor-specific metrics OIDs.
	// Optimized for scheduled metrics collection (page counts, toner, scans, jams).
	// NEW - specifically for periodic metrics snapshots
	QueryMetrics
)

// String returns the string representation of QueryProfile.
func (q QueryProfile) String() string {
	switch q {
	case QueryMinimal:
		return "QueryMinimal"
	case QueryEssential:
		return "QueryEssential"
	case QueryFull:
		return "QueryFull"
	case QueryMetrics:
		return "QueryMetrics"
	default:
		return "Unknown"
	}
}

// QueryDevice performs SNMP queries based on the specified profile.
// This is the unified replacement for QuickGetSerial, QuickRefreshDevice, and RefreshDevice.
//
// Parameters:
//   - ip: Target device IP address
//   - profile: QueryProfile determining which OIDs to query
//   - vendorHint: Vendor name ("HP", "Canon", etc.) for vendor-specific OIDs. Empty string uses generic.
//   - timeout: SNMP timeout in seconds
//
// Returns:
//   - *QueryResult: Raw SNMP data (PDUs) to be parsed by caller
//   - error: Any errors encountered during query
//
// Example usage:
//
//	// Quick serial lookup
//	result, err := QueryDevice(ctx, "10.0.0.1", QueryMinimal, "", 5)
//
//	// Fast refresh with HP-specific OIDs
//	result, err := QueryDevice(ctx, "10.0.0.1", QueryEssential, "HP", 10)
//
//	// Full diagnostic walk
//	result, err := QueryDevice(ctx, "10.0.0.1", QueryFull, "HP", 15)
//
//	// Metrics collection
//	result, err := QueryDevice(ctx, "10.0.0.1", QueryMetrics, "HP", 5)
//
// QueryResult holds the raw SNMP data returned by QueryDevice.
// The caller must parse these PDUs using agent.ParsePDUs or vendor.ExtractMetrics.
type QueryResult struct {
	IP           string
	PDUs         []gosnmp.SnmpPDU
	Profile      QueryProfile
	VendorHint   string
	Capabilities *capabilities.DeviceCapabilities // Populated during QueryFull
}

func QueryDevice(ctx context.Context, ip string, profile QueryProfile, vendorHint string, timeoutSeconds int) (*QueryResult, error) {
	return QueryDeviceWithCapabilities(ctx, ip, profile, vendorHint, timeoutSeconds, nil)
}

// QueryDeviceWithCapabilities performs SNMP queries with optional capability-aware OID optimization.
// When capabilities are provided and profile is QueryMetrics, only relevant OIDs are queried.
func QueryDeviceWithCapabilities(ctx context.Context, ip string, profile QueryProfile, vendorHint string, timeoutSeconds int, caps *capabilities.DeviceCapabilities) (*QueryResult, error) {
	return queryDeviceWithCapabilitiesAndClient(ctx, ip, profile, vendorHint, timeoutSeconds, caps, NewSNMPClient)
}

// ContextQueryOptions augments the existing profiles without replacing vendor
// catalogs. RequestedOIDs includes learned scalar OIDs or requested table columns.
type ContextQueryOptions struct {
	Capabilities  *capabilities.DeviceCapabilities
	Retries       int
	RequestedOIDs []string
}

// QueryDeviceWithContext binds the transport and retries to this operation while
// retaining QueryDevice's vendor profiles, table walks and capability detection.
func QueryDeviceWithContext(ctx context.Context, ip string, profile QueryProfile, vendorHint string, timeoutSeconds int, options ContextQueryOptions) (*QueryResult, error) {
	return queryDeviceWithContextAndClient(ctx, ip, profile, vendorHint, timeoutSeconds, options, NewSNMPClientWithContext)
}

func queryDeviceWithContextAndClient(ctx context.Context, ip string, profile QueryProfile, vendorHint string, timeoutSeconds int, options ContextQueryOptions, factory func(context.Context, *SNMPConfig, string, int, int) (SNMPClient, error)) (*QueryResult, error) {
	return queryDeviceWithCapabilitiesAndClient(ctx, ip, profile, vendorHint, timeoutSeconds, options.Capabilities, func(cfg *SNMPConfig, target string, timeout int) (SNMPClient, error) {
		return factory(ctx, cfg, target, timeout, options.Retries)
	}, options.RequestedOIDs...)
}

// queryDeviceWithCapabilitiesAndClient is the internal implementation that accepts a client factory.
// This allows tests to inject mock clients without modifying global state.
func queryDeviceWithCapabilitiesAndClient(ctx context.Context, ip string, profile QueryProfile, vendorHint string, timeoutSeconds int, caps *capabilities.DeviceCapabilities, clientFactory func(*SNMPConfig, string, int) (SNMPClient, error), requestedOIDs ...string) (*QueryResult, error) {
	if ip == "" {
		return nil, fmt.Errorf("ip address required")
	}

	// Check if context is already cancelled before starting
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 1. Get SNMP config
	cfg, err := GetSNMPConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get SNMP config: %w", err)
	}

	// 2. Setup SNMP client
	client, err := clientFactory(cfg, ip, timeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("failed to create SNMP client: %w", err)
	}
	defer client.Close()

	// 3. Preliminary vendor detection (fast, minimal GET)
	var detectedVendor vendor.VendorModule
	var sysObjectID, sysDescr, model string

	{
		preOIDs := []string{
			oids.SysObjectID,
			oids.SysDescr,
			oids.HrDeviceDescr,
		}
		preRes, preErr := client.Get(preOIDs)
		if preErr == nil && preRes != nil {
			for _, pdu := range preRes.Variables {
				name := strings.TrimPrefix(pdu.Name, ".")
				if name == oids.SysObjectID {
					if b, ok := pdu.Value.([]byte); ok {
						sysObjectID = string(b)
					} else if s, ok := pdu.Value.(string); ok {
						sysObjectID = s
					}
				} else if name == oids.SysDescr {
					if b, ok := pdu.Value.([]byte); ok {
						sysDescr = string(b)
					} else if s, ok := pdu.Value.(string); ok {
						sysDescr = s
					}
				} else if name == oids.HrDeviceDescr {
					if b, ok := pdu.Value.([]byte); ok {
						model = string(b)
					} else if s, ok := pdu.Value.(string); ok {
						model = s
					}
				}
			}
		}
	}
	// If caller supplied vendorHint, prefer that; else detect
	if vendorHint != "" {
		// Attempt to match a registered module by name
		detectedVendor = vendor.DetectVendor(sysObjectID, sysDescr, model)
		if !strings.EqualFold(detectedVendor.Name(), vendorHint) {
			// Fallback: iterate modules to find explicit name match
			detectedVendor = vendor.DetectVendor("."+enterpriseFromHint(vendorHint), sysDescr, model) // crude fallback
		}
	} else {
		detectedVendor = vendor.DetectVendor(sysObjectID, sysDescr, model)
	}
	if logger.Global != nil {
		logger.Global.Debug("SNMP preliminary detection", "ip", ip, "sysObjectID", sysObjectID, "vendor_selected", detectedVendor.Name())
	}
	if detectedVendor == nil {
		// Full walk or detection failed: use generic
		detectedVendor = &vendor.GenericVendor{}
	}

	// 4. Build OID list based on profile + capabilities + vendor
	queryOIDs := buildQueryOIDsWithModule(profile, caps, detectedVendor)
	if logger.Global != nil {
		logger.Global.Debug("OID list constructed", "ip", ip, "profile", profile.String(), "oid_count", len(queryOIDs), "vendor", detectedVendor.Name())
	}

	// 5. Query SNMP (Walk for Full, Get for others)
	var pdus []gosnmp.SnmpPDU

	if profile == QueryFull {
		// Full diagnostic walk of standard MIB roots.
		// NOTE: Avoid walking the enterprise root (1.3.6.1.4.1) by default.
		// It can be extremely large and unpredictable across devices.
		roots := []string{
			"1.3.6.1.2.1",    // System MIB
			"1.3.6.1.2.1.43", // Printer-MIB
		}

		// If we know the device enterprise (from sysObjectID), walk only that vendor subtree.
		// This preserves vendor diagnostics without the unbounded cost of the full enterprise root.
		vendorEnterprise := enterprisePrefixFromSysObjectID(sysObjectID)
		if vendorEnterprise != "" {
			roots = append(roots, vendorEnterprise)
		}

		for _, root := range roots {
			// Check context before each walk
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			err := client.Walk(root, func(pdu gosnmp.SnmpPDU) error {
				// Check context during walk
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				pdus = append(pdus, pdu)
				// Limit to prevent runaway walks
				if len(pdus) >= 10000 {
					return fmt.Errorf("walk limit exceeded")
				}
				return nil
			})
			if err != nil && len(pdus) == 0 {
				return nil, fmt.Errorf("SNMP walk failed: %w", err)
			}
		}
	} else {
		// Targeted GET for specific OIDs
		if len(queryOIDs) == 0 {
			return nil, fmt.Errorf("no OIDs to query for profile %s", profile)
		}

		// For QueryMetrics and QueryEssential, separate supply table walks from scalar GETs
		// Supply OIDs are table roots (e.g., prtMarkerSuppliesLevel) that must be walked,
		// not queried with GET which would fail to return any data.
		var scalarOIDs []string
		var tableRoots []string

		if (profile == QueryMetrics || profile == QueryEssential) && detectedVendor != nil {
			supplyOIDs := detectedVendor.SupplyOIDs()
			// Also include paper tray OIDs as tables to walk
			paperTrayOIDs := detectedVendor.PaperTrayOIDs()
			tableOIDMap := make(map[string]bool)
			for _, s := range supplyOIDs {
				tableOIDMap[s] = true
			}
			for _, s := range paperTrayOIDs {
				tableOIDMap[s] = true
			}

			for _, oid := range queryOIDs {
				if tableOIDMap[oid] {
					tableRoots = append(tableRoots, oid)
				} else {
					scalarOIDs = append(scalarOIDs, oid)
				}
			}
		} else {
			scalarOIDs = queryOIDs
		}

		// GET scalar values using batched requests to avoid oversized PDUs
		if len(scalarOIDs) > 0 {
			scalarPDUs, err := batchedGet(ctx, optionalQueryClient{SNMPClient: client, ctx: ctx}, scalarOIDs, defaultOIDBatchSize)
			if err != nil {
				return nil, err
			}
			pdus = append(pdus, scalarPDUs...)
		}

		// WALK supply tables
		for _, root := range tableRoots {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			err := client.Walk(root, func(pdu gosnmp.SnmpPDU) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				pdus = append(pdus, pdu)
				if len(pdus) >= 10000 {
					return fmt.Errorf("walk limit exceeded")
				}
				return nil
			})
			// Don't fail if one table walk fails - continue with other tables
			if err != nil && logger.Global != nil {
				logger.Global.Debug("Supply table walk failed", "ip", ip, "root", root, "error", err)
			}
		}
	}

	// Optional/learned OIDs supplement every profile, including OIDs outside a
	// full walk's roots. Query separately: unsupported SNMPv1 OIDs must not poison
	// the established profile's batched GET. Missing table instances use WALK.
	for _, oid := range appendUniqueOIDs(nil, requestedOIDs...) {
		oid = normalizeOID(oid)
		if oid == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		present := false
		for _, pdu := range pdus {
			name := normalizeOID(pdu.Name)
			if (name == oid || strings.HasPrefix(name, oid+".")) && pdu.Type != gosnmp.NoSuchObject && pdu.Type != gosnmp.NoSuchInstance && pdu.Type != gosnmp.EndOfMibView && pdu.Type != gosnmp.Null {
				present = true
				break
			}
		}
		if present {
			continue
		}
		packet, getErr := client.Get([]string{oid})
		if getErr == nil && packet != nil && packet.Error == gosnmp.NoError {
			for _, pdu := range packet.Variables {
				if pdu.Type != gosnmp.NoSuchObject && pdu.Type != gosnmp.NoSuchInstance && pdu.Type != gosnmp.EndOfMibView && pdu.Type != gosnmp.Null {
					pdus = append(pdus, pdu)
					present = true
				}
			}
		}
		if !present {
			_ = client.Walk(oid, func(pdu gosnmp.SnmpPDU) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if len(pdus) >= 10000 {
					return fmt.Errorf("walk limit exceeded")
				}
				pdus = append(pdus, pdu)
				return nil
			})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(pdus) == 0 {
		return nil, fmt.Errorf("no SNMP data received from %s", ip)
	}

	result := &QueryResult{
		IP:         ip,
		PDUs:       pdus,
		Profile:    profile,
		VendorHint: vendorHint,
	}
	if logger.Global != nil {
		logger.Global.Debug("SNMP query complete", "ip", ip, "profile", profile.String(), "pdu_count", len(pdus))
	}

	// Detect capabilities if QueryFull or QueryEssential (both have enough data)
	// QueryEssential includes BaseOIDs which contain SysDescr, SysObjectID, HrDeviceDescr, etc.
	if profile == QueryFull || profile == QueryEssential {
		caps := detectCapabilities(pdus, vendorHint)
		result.Capabilities = &caps
		if logger.Global != nil {
			logger.Global.Debug("Capabilities detected", "ip", ip, "isScanner", caps.IsScanner, "isCopier", caps.IsCopier, "isFax", caps.IsFax, "hasDuplex", caps.HasDuplex)
		}
	}

	return result, nil
}

// SNMPv1 rejects an entire GET when one optional profile OID is unsupported.
// Keep established batching, retry only that rejected batch as individual GETs,
// and never treat error-status variable bindings as obtained facts.
type optionalQueryClient struct {
	SNMPClient
	ctx context.Context
}

func (c optionalQueryClient) Get(requested []string) (*gosnmp.SnmpPacket, error) {
	packet, err := c.SNMPClient.Get(requested)
	if err != nil || packet == nil || packet.Error == gosnmp.NoError {
		return packet, err
	}
	if packet.Error != gosnmp.NoSuchName {
		return nil, fmt.Errorf("SNMP GET error status: %v", packet.Error)
	}
	var pdus []gosnmp.SnmpPDU
	if len(requested) > 1 {
		for _, oid := range requested {
			if err := c.ctx.Err(); err != nil {
				return nil, err
			}
			one, err := c.SNMPClient.Get([]string{oid})
			if err != nil {
				return nil, err
			}
			if one != nil && one.Error == gosnmp.NoError {
				pdus = append(pdus, one.Variables...)
			}
		}
	}
	return &gosnmp.SnmpPacket{Variables: pdus}, nil
}

// buildQueryOIDs constructs the list of OIDs to query based on profile.
// Now vendor-aware: integrates vendor module OIDs with standard Printer-MIB.
func buildQueryOIDs(profile QueryProfile) []string {
	return buildQueryOIDsWithCapabilities(profile, nil)
}

// buildQueryOIDsWithCapabilities constructs the list of OIDs with optional capability-aware filtering.
// Integrates vendor-specific OIDs when vendorHint is provided.
func buildQueryOIDsWithCapabilities(profile QueryProfile, caps *capabilities.DeviceCapabilities) []string { // backward compatibility
	return buildQueryOIDsWithModule(profile, caps, &vendor.GenericVendor{})
}

// buildQueryOIDsWithModule constructs OIDs using a specific vendor module.
func buildQueryOIDsWithModule(profile QueryProfile, caps *capabilities.DeviceCapabilities, vendorModule vendor.VendorModule) []string {
	var queryOIDs []string

	switch profile {
	case QueryMinimal:
		queryOIDs = []string{oids.PrtGeneralSerialNumber}
		queryOIDs = appendUniqueOIDs(queryOIDs, VendorIDTargetOIDs()...)
	case QueryEssential:
		queryOIDs = append(queryOIDs, vendorModule.BaseOIDs()...)
		queryOIDs = append(queryOIDs, vendorModule.SupplyOIDs()...)
		queryOIDs = append(queryOIDs, vendorModule.PaperTrayOIDs()...)
		queryOIDs = append(queryOIDs, oids.PrtMarkerLifeCount)
		queryOIDs = appendUniqueOIDs(queryOIDs, VendorIDTargetOIDs()...)
	case QueryFull:
		// QueryFull intentionally returns nil because it uses SNMP walk operations
		// rather than targeted GET requests. Callers should use FullQuery() directly
		// for complete device enumeration instead of buildQueryOIDsWithModule.
		return nil
	case QueryMetrics:
		queryOIDs = append(queryOIDs, vendorModule.BaseOIDs()...)
		queryOIDs = append(queryOIDs, vendorModule.MetricOIDs(caps)...)
		queryOIDs = append(queryOIDs, vendorModule.SupplyOIDs()...)
		queryOIDs = append(queryOIDs, vendorModule.PaperTrayOIDs()...)
	}
	return queryOIDs
}

// appendUniqueOIDs appends extras to base while avoiding duplicate OIDs.
func appendUniqueOIDs(base []string, extras ...string) []string {
	if len(extras) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base))
	for _, oid := range base {
		seen[oid] = struct{}{}
	}
	for _, extra := range extras {
		if extra == "" {
			continue
		}
		if _, ok := seen[extra]; ok {
			continue
		}
		base = append(base, extra)
		seen[extra] = struct{}{}
	}
	return base
}

// enterpriseFromHint is a helper best-effort map from vendor name to a synthetic OID to reuse DetectVendor logic.
func enterpriseFromHint(name string) string {
	switch strings.ToLower(name) {
	case "hp":
		return "11"
	case "kyocera":
		return "1347"
	case "epson":
		return "1248"
	case "canon":
		return "1602"
	case "brother":
		return "2435"
	case "lexmark":
		return "641"
	case "ricoh":
		return "367"
	case "samsung":
		return "236"
	case "xerox":
		return "253"
	default:
		return ""
	}
}

// enterprisePrefixFromSysObjectID returns the vendor enterprise subtree root
// (e.g. "1.3.6.1.4.1.11") when sysObjectID appears to be an enterprise OID.
// sysObjectID values are commonly represented as dotted strings and may include
// a leading dot.
func enterprisePrefixFromSysObjectID(sysObjectID string) string {
	s := strings.TrimSpace(sysObjectID)
	s = strings.TrimPrefix(s, ".")
	const prefix = "1.3.6.1.4.1."
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(s, prefix)
	if rest == "" {
		return ""
	}
	// Keep only the first numeric component after the enterprise root.
	// Example: 1.3.6.1.4.1.11.2.3 -> 1.3.6.1.4.1.11
	i := 0
	for i < len(rest) {
		c := rest[i]
		if c < '0' || c > '9' {
			break
		}
		i++
	}
	if i == 0 {
		return ""
	}
	return prefix + rest[:i]
}

// detectCapabilities analyzes SNMP PDUs to determine device capabilities.
// This is called during QueryFull to populate the Capabilities field.
func detectCapabilities(pdus []gosnmp.SnmpPDU, vendorHint string) capabilities.DeviceCapabilities {
	idx := make(map[string]gosnmp.SnmpPDU, len(pdus))
	for _, pdu := range pdus {
		idx[normalizeOID(pdu.Name)] = pdu
	}

	// Extract basic device info from PDUs for capability detection
	evidence := &capabilities.DetectionEvidence{
		PDUs:     pdus,
		PDUByOID: idx,
		Vendor:   vendorHint,
		SysDescr: extractOIDStringFromIndex(idx, oids.SysDescr),
		SysOID:   extractOIDStringFromIndex(idx, oids.SysObjectID),
		Model:    extractOIDStringFromIndex(idx, oids.HrDeviceDescr),          // hrDeviceDescr
		Serial:   extractOIDStringFromIndex(idx, oids.PrtGeneralSerialNumber), // prtGeneralSerialNumber
	}

	// If model is empty, try to extract from sysDescr
	if evidence.Model == "" && evidence.SysDescr != "" {
		// SysDescr often contains model info (e.g., "HP LaserJet Pro M404dn")
		evidence.Model = evidence.SysDescr
	}

	// Run capability detection
	registry := capabilities.NewCapabilityRegistry()
	return registry.DetectAll(evidence)
}

func extractOIDStringFromIndex(idx map[string]gosnmp.SnmpPDU, oid string) string {
	oid = normalizeOID(oid)
	if pdu, ok := idx[oid]; ok {
		if bytes, ok := pdu.Value.([]byte); ok {
			return string(bytes)
		}
		if str, ok := pdu.Value.(string); ok {
			return str
		}
	}
	return ""
}

// normalizeOID removes leading dot if present
func normalizeOID(oid string) string {
	if len(oid) > 0 && oid[0] == '.' {
		return oid[1:]
	}
	return oid
}
