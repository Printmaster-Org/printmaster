package vendor

import (
	"context"
	"strings"

	"printmaster/agent/scanner/capabilities"
	"printmaster/common/logger"

	"github.com/gosnmp/gosnmp"
)

// VendorModule defines the interface for vendor-specific SNMP handling.
// Each vendor (HP, Canon, Brother, etc.) implements this interface to provide
// custom OID lists and parsing logic for their proprietary enterprise MIBs.
type VendorModule interface {
	// Name returns the vendor identifier (e.g., "HP", "Kyocera", "Epson").
	Name() string

	// Detect returns true if this module should be used for the given device.
	// Checks sysObjectID, sysDescr, and other evidence.
	Detect(sysObjectID, sysDescr, model string) bool

	// BaseOIDs returns standard OIDs that should always be queried (page counts, status).
	// These are included in all query profiles.
	BaseOIDs() []string

	// MetricOIDs returns vendor-specific metric OIDs (color pages, copy/fax/scan counters).
	// Only included when QueryProfile is QueryMetrics or QueryEssential.
	// Can return capability-aware OIDs if caps is provided.
	MetricOIDs(caps *capabilities.DeviceCapabilities) []string

	// SupplyOIDs returns OIDs for supply levels (toner, ink, drums).
	// Typically walks prtMarkerSuppliesTable or vendor-specific supply trees.
	SupplyOIDs() []string

	// PaperTrayOIDs returns OIDs for paper input tray status.
	// Walks prtInputTable (43.8) for tray levels, capacity, and media type.
	PaperTrayOIDs() []string

	// Parse extracts metrics from SNMP PDUs and returns a map of metric names to values.
	// Keys should match the MetricRegistry names (e.g., "color_pages", "toner_cyan").
	// Returns map[string]interface{} to support int, float64, string, etc.
	Parse(pdus []gosnmp.SnmpPDU) map[string]interface{}
}

// ExtendedVendorModule is an optional interface for vendors that support
// extended parsing with additional context (IP address, timeout, etc.).
// This enables features like Epson remote-mode that require additional
// SNMP queries beyond the standard PDU set.
type ExtendedVendorModule interface {
	VendorModule

	// ParseWithRemoteMode performs extended parsing that may issue additional
	// SNMP queries (e.g., Epson remote-mode commands for ink levels).
	// Falls back to standard Parse() if IP is empty or remote features disabled.
	ParseWithRemoteMode(ctx context.Context, pdus []gosnmp.SnmpPDU, ip string, timeoutSeconds int) map[string]interface{}
}

// MetricTableModule is an optional interface for vendors whose metrics live in
// enterprise tables (rather than scalars). Roots returned here are walked
// during QueryMetrics, alongside SupplyOIDs/PaperTrayOIDs.
type MetricTableModule interface {
	MetricTableOIDs() []string
}

// MetricTableOIDs returns the module's metric table walk roots, if any.
func MetricTableOIDs(module VendorModule) []string {
	if m, ok := module.(MetricTableModule); ok {
		return m.MetricTableOIDs()
	}
	return nil
}

// ParseWithContext calls the vendor's extended parsing if available,
// otherwise falls back to standard Parse().
// This is the preferred way to invoke vendor parsing when IP is known.
func ParseWithContext(ctx context.Context, module VendorModule, pdus []gosnmp.SnmpPDU, ip string, timeoutSeconds int) map[string]interface{} {
	// Try extended interface first
	if ext, ok := module.(ExtendedVendorModule); ok && ip != "" {
		return ext.ParseWithRemoteMode(ctx, pdus, ip, timeoutSeconds)
	}
	// Fall back to standard parse
	return module.Parse(pdus)
}

// EnterpriseOIDMap maps IANA enterprise numbers to vendor names.
// Source: https://www.iana.org/assignments/enterprise-numbers/
var EnterpriseOIDMap = map[string]string{
	"11":   "HP",      // Hewlett-Packard
	"367":  "Ricoh",   // Ricoh Company, Ltd.
	"641":  "Lexmark", // Lexmark International
	"1347": "Kyocera", // Kyocera Corporation
	"1602": "Canon",   // Canon Inc.
	"2435": "Brother", // Brother Industries, Ltd.
	"1248": "Epson",   // Seiko Epson Corporation
	"236":  "Samsung", // Samsung Electronics
	"253":  "Xerox",   // Xerox Corporation
	"9":    "Dell",    // Dell Inc.
	// Add more as needed
}

// vendorModules holds registered vendor module instances.
// Populated by init() functions in each vendor module file.
var vendorModules []VendorModule

// genericModule is the fallback when no vendor-specific module matches.
var genericModule VendorModule

// RegisterVendor adds a vendor module to the registry.
// Called by init() in each vendor module file (hp.go, canon.go, etc.).
func RegisterVendor(module VendorModule) {
	vendorModules = append(vendorModules, module)
}

// SetGenericModule sets the fallback module used when no vendor matches.
func SetGenericModule(module VendorModule) {
	genericModule = module
}

// DetectVendor identifies the appropriate vendor module for a device.
// Detection logic:
// 1. Extract enterprise number from sysObjectID (e.g., "1.3.6.1.4.1.11.2..." → "11" → HP)
// 2. Try each registered module's Detect() method
// 3. Fall back to generic module
func DetectVendor(sysObjectID, sysDescr, model string) VendorModule {
	if logger.Global != nil {
		logger.Global.Debug("Vendor detection start", "sysObjectID", sysObjectID, "sysDescr_len", len(sysDescr), "model", model)
	}
	// Try enterprise OID prefix matching first (fastest)
	if enterprise := extractEnterpriseNumber(sysObjectID); enterprise != "" {
		if vendorName, ok := EnterpriseOIDMap[enterprise]; ok {
			// Find matching vendor module
			for _, module := range vendorModules {
				if strings.EqualFold(module.Name(), vendorName) {
					if logger.Global != nil {
						logger.Global.Debug("Vendor detected via enterprise OID", "enterprise", enterprise, "vendor", module.Name())
					}
					return module
				}
			}
		}
	}

	// Try each module's Detect() method (allows heuristic matching)
	for _, module := range vendorModules {
		if module.Detect(sysObjectID, sysDescr, model) {
			if logger.Global != nil {
				logger.Global.Debug("Vendor detected via heuristic", "vendor", module.Name())
			}
			return module
		}
	}

	// Fall back to generic
	if genericModule != nil {
		if logger.Global != nil {
			logger.Global.Debug("Vendor fallback to generic module")
		}
		return genericModule
	}

	// Should never reach here, but return a safe default
	return &GenericVendor{}
}

// extractEnterpriseNumber extracts the enterprise number from a sysObjectID.
// Example: "1.3.6.1.4.1.11.2.3.9.1" → "11"
// Returns empty string if the OID doesn't match the standard enterprise pattern.
func extractEnterpriseNumber(sysObjectID string) string {
	// Remove leading dot if present
	oid := strings.TrimPrefix(sysObjectID, ".")

	// Standard enterprise OID prefix: 1.3.6.1.4.1.<enterprise_number>
	parts := strings.Split(oid, ".")
	if len(parts) >= 7 &&
		parts[0] == "1" &&
		parts[1] == "3" &&
		parts[2] == "6" &&
		parts[3] == "1" &&
		parts[4] == "4" &&
		parts[5] == "1" {
		return parts[6] // Enterprise number
	}

	return ""
}

// ManufacturerForEnterprise returns the normalized manufacturer name for an
// IANA enterprise number, or an empty string when it is not known.
func ManufacturerForEnterprise(enterprise string) string {
	return EnterpriseOIDMap[enterprise]
}

// ManufacturerForSysObjectID returns the normalized manufacturer name for a
// standard SNMP enterprise OID, or an empty string when it is not known.
func ManufacturerForSysObjectID(sysObjectID string) string {
	return ManufacturerForEnterprise(extractEnterpriseNumber(sysObjectID))
}

// GetVendorName returns the human-readable vendor name for a device.
// Used for logging and display purposes.
func GetVendorName(sysObjectID, sysDescr, model string) string {
	module := DetectVendor(sysObjectID, sysDescr, model)
	return module.Name()
}

// GetVendorByName returns the registered vendor module with the given name (case-insensitive).
// Returns nil if no matching vendor is found.
func GetVendorByName(name string) VendorModule {
	for _, module := range vendorModules {
		if strings.EqualFold(module.Name(), name) {
			return module
		}
	}
	return nil
}
