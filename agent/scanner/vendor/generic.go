package vendor

import (
	"fmt"
	"strconv"
	"strings"

	"printmaster/agent/scanner/capabilities"
	"printmaster/agent/supplies"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

type pduIndex struct {
	byOID map[string]gosnmp.SnmpPDU
}

func newPDUIndex(pdus []gosnmp.SnmpPDU) *pduIndex {
	idx := &pduIndex{byOID: make(map[string]gosnmp.SnmpPDU, len(pdus))}
	for _, pdu := range pdus {
		idx.byOID[normalizeOID(pdu.Name)] = pdu
	}
	return idx
}

// GenericVendor is the fallback module for devices without vendor-specific support.
// Uses standard Printer-MIB (RFC 3805) OIDs only.
type GenericVendor struct{}

func init() {
	// Register generic module as fallback
	SetGenericModule(&GenericVendor{})
}

func (v *GenericVendor) Name() string {
	return "Generic"
}

func (v *GenericVendor) Detect(sysObjectID, sysDescr, model string) bool {
	// Generic module is always a valid fallback
	return true
}

func (v *GenericVendor) BaseOIDs() []string {
	return []string{
		oids.SysDescr,
		oids.SysObjectID,
		oids.SysUpTime, // ICE queries this
		oids.SysName,
		oids.SysLocation, // ICE queries this
		oids.HrDeviceDescr,
		oids.HrDeviceStatus, // ICE queries this
		oids.PrtGeneralPrinterName,
		oids.PrtGeneralSerialNumber,
		oids.HrPrinterStatus + ".1",
		oids.HrPrinterDetectedErrorState + ".1",
		// PWG Port Monitor IEEE 1284 Device ID (contains capability info)
		oids.PpmPrinterIEEE1284DeviceID + ".1",
	}
}

func (v *GenericVendor) MetricOIDs(caps *capabilities.DeviceCapabilities) []string {
	return []string{
		oids.PrtMarkerLifeCount + ".1", // prtMarkerLifeCount (instance .1)
	}
}

func (v *GenericVendor) SupplyOIDs() []string {
	// Return OID roots for SNMP walk of supply tables
	return []string{
		oids.PrtMarkerSuppliesDesc,
		oids.PrtMarkerSuppliesLevel,
		oids.PrtMarkerSuppliesMaxCap,
		oids.PrtMarkerSuppliesClass,
		oids.PrtMarkerSuppliesType,
	}
}

func (v *GenericVendor) PaperTrayOIDs() []string {
	// Return OID roots for SNMP walk of paper input table (prtInputTable)
	return []string{
		oids.PrtInputName,         // Tray name (e.g., "Tray 1")
		oids.PrtInputMediaName,    // Media type (e.g., "Letter", "A4")
		oids.PrtInputCurrentLevel, // Current paper level
		oids.PrtInputMaxCapacity,  // Max tray capacity
		oids.PrtInputStatus,       // Tray status
		oids.PrtInputType,         // Tray type (manual feed, cassette, etc.)
		oids.PrtInputDescription,  // Tray description
	}
}

func (v *GenericVendor) Parse(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	result := make(map[string]interface{})
	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Parsing Generic vendor PDUs", "pdu_count", len(pdus))
	}

	idx := newPDUIndex(pdus)

	// Parse basic page count
	if pageCount := getOIDIntIndexed(idx, pdus, oids.PrtMarkerLifeCount+".1"); pageCount > 0 {
		result["total_pages"] = pageCount
		result["page_count"] = pageCount
	}

	// Parse supply levels from prtMarkerSuppliesTable
	supplies := parseSuppliesTable(pdus)
	if logger.Global != nil {
		logger.Global.Debug("Generic supply table parsed", "supplies_count", len(supplies))
	}
	for k, v := range supplies {
		result[k] = v
	}

	return result
}

func getOIDIntIndexed(idx *pduIndex, pdus []gosnmp.SnmpPDU, oid string) int {
	oid = normalizeOID(oid)
	if idx != nil {
		if pdu, ok := idx.byOID[oid]; ok {
			return coerceToInt(pdu.Value)
		}
	}
	return getOIDInt(pdus, oid)
}

// parseSuppliesTable walks prtMarkerSuppliesTable and extracts toner/ink levels.
// Maps supply descriptions to standardized names (toner_black, toner_cyan, etc.).
func parseSuppliesTable(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	result := make(map[string]interface{})

	// Group PDUs by instance suffix (e.g., ".1.1.6.1.1" → instance "1")
	type SupplyEntry struct {
		Description string
		Level       int
		MaxCapacity int
		Class       int
		Type        int
	}

	entries := make(map[string]*SupplyEntry)

	for _, pdu := range pdus {
		oid := normalizeOID(pdu.Name)

		// Extract instance from OID (last component after table root)
		var instance string
		var field string

		if strings.HasPrefix(oid, oids.PrtMarkerSuppliesDesc+".1.") {
			// Description
			instance = strings.TrimPrefix(oid, "1.3.6.1.2.1.43.11.1.1.6.1.")
			field = "description"
		} else if strings.HasPrefix(oid, oids.PrtMarkerSuppliesLevel+".1.") {
			// Level
			instance = strings.TrimPrefix(oid, "1.3.6.1.2.1.43.11.1.1.9.1.")
			field = "level"
		} else if strings.HasPrefix(oid, oids.PrtMarkerSuppliesMaxCap+".1.") {
			// MaxCapacity
			instance = strings.TrimPrefix(oid, "1.3.6.1.2.1.43.11.1.1.8.1.")
			field = "max_capacity"
		} else if strings.HasPrefix(oid, oids.PrtMarkerSuppliesClass+".1.") {
			// Class
			instance = strings.TrimPrefix(oid, "1.3.6.1.2.1.43.11.1.1.4.1.")
			field = "class"
		} else if strings.HasPrefix(oid, oids.PrtMarkerSuppliesType+".1.") {
			// Type
			instance = strings.TrimPrefix(oid, "1.3.6.1.2.1.43.11.1.1.5.1.")
			field = "type"
		} else {
			continue
		}

		if instance == "" {
			continue
		}

		// Initialize entry if not exists
		if entries[instance] == nil {
			entries[instance] = &SupplyEntry{
				Level:       -1,
				MaxCapacity: -1,
			}
		}
		entry := entries[instance]

		// Populate fields
		switch field {
		case "description":
			if bytes, ok := pdu.Value.([]byte); ok {
				entry.Description = string(bytes)
			} else if str, ok := pdu.Value.(string); ok {
				entry.Description = str
			}
		case "level":
			entry.Level = coerceToInt(pdu.Value)
		case "max_capacity":
			entry.MaxCapacity = coerceToInt(pdu.Value)
		case "class":
			entry.Class = coerceToInt(pdu.Value)
		case "type":
			entry.Type = coerceToInt(pdu.Value)
		}
	}

	// Map supplies to standardized names
	processed := 0
	for _, entry := range entries {
		if entry.Description == "" {
			continue
		}

		// Class 3 = supplyThatIsConsumed (toner, ink, etc.)
		// Class 4 = receptacleThatIsFilled (waste toner containers)
		// Class 0 = not reported (some devices don't report class, allow if description normalizes)
		// Skip other classes (1=other, 2=unknown)
		if entry.Class != 0 && entry.Class != 3 && entry.Class != 4 {
			continue
		}

		// If Class is 0 (not reported), only include if description normalizes to known supply type
		// This prevents random OIDs from being treated as supplies
		if entry.Class == 0 {
			normalized := supplies.NormalizeDescription(entry.Description)
			if normalized == "" && !supplies.PreserveDescription(entry.Description) {
				continue // Unknown description and no class - skip
			}
		}

		// Normalize description for matching
		// Use original description for part number matching, lowercase for word matching
		desc := entry.Description

		// Calculate percentage if we have both level and capacity
		// Per RFC 3805 (Printer-MIB):
		//   MaxCapacity = -1: other/unknown
		//   MaxCapacity = -2: unknown (manufacturer-defined)
		//   MaxCapacity = -3: someRemaining (level is not quantifiable)
		//   Level = -1: other/unknown
		//   Level = -2: unknown
		//   Level = -3: someRemaining (low but usable)
		var percentage float64
		if entry.MaxCapacity > 0 && entry.Level >= 0 {
			// Normal case: calculate percentage from level/capacity
			percentage = (float64(entry.Level) / float64(entry.MaxCapacity)) * 100.0
		} else if entry.Level >= 0 && entry.Level <= 100 && entry.MaxCapacity <= 0 {
			// MaxCapacity is unknown/special but Level is valid 0-100 range
			// Treat level as percentage directly (common for many printers)
			percentage = float64(entry.Level)
		} else if entry.Level == -3 {
			// someRemaining: report as low (10%) as a reasonable estimate
			percentage = 10.0
		} else {
			// Unknown level - cannot determine
			percentage = -1
		}

		// Match description to canonical metric key
		metricName := supplies.NormalizeDescription(desc)

		// For Class 4 (waste containers), force to waste_toner if not already mapped
		if entry.Class == 4 && metricName == "" {
			metricName = "waste_toner"
		}
		if supplies.PreserveDescription(desc) {
			metricName = ""
		}

		if metricName != "" {
			// Deduplication: if key already exists, prefer the value that's not 100%
			// (100% often indicates a "new toner" placeholder entry from some vendors)
			if existing, ok := result[metricName]; ok {
				existingVal, _ := existing.(float64)
				// Keep the existing value if:
				// - new value is exactly 100 (likely placeholder) and existing isn't
				// - new value is invalid (-1)
				if percentage == 100.0 && existingVal != 100.0 && existingVal >= 0 {
					// Skip this duplicate - keep existing more realistic value
					if logger.Global != nil {
						logger.Global.Debug("Supply dedup: keeping existing value",
							"key", metricName, "existing", existingVal, "skipped", percentage)
					}
					processed++
					continue
				}
				// If existing is 100 and new isn't, we'll overwrite (fall through)
				if existingVal == 100.0 && percentage != 100.0 && percentage >= 0 {
					if logger.Global != nil {
						logger.Global.Debug("Supply dedup: replacing placeholder",
							"key", metricName, "old", existingVal, "new", percentage)
					}
				}
			}
			result[metricName] = percentage
		}

		// Preserve named ink/maintenance supplies and unclassified descriptions.
		if metricName == "" && percentage >= 0 {
			// Store with sanitized description as key
			sanitized := strings.ToLower(entry.Description)
			sanitized = strings.ReplaceAll(sanitized, " ", "_")
			result[fmt.Sprintf("supply_%s", sanitized)] = percentage
		}
		processed++
	}
	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Supply entries processed", "count", processed)
	}

	return result
}

// PaperTray represents a single paper input tray from prtInputTable
type PaperTray struct {
	Index        int    `json:"index"`
	Name         string `json:"name,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	CurrentLevel int    `json:"current_level"`
	MaxCapacity  int    `json:"max_capacity"`
	LevelPercent int    `json:"level_percent,omitempty"`
	Status       string `json:"status,omitempty"`
	TrayType     int    `json:"tray_type,omitempty"`
	Description  string `json:"description,omitempty"`
}

// ParsePaperTrays extracts paper tray information from prtInputTable PDUs.
// Returns a slice of PaperTray structs with level, capacity, and status.
func ParsePaperTrays(pdus []gosnmp.SnmpPDU) []PaperTray {
	// prtInputTable OID structure: 1.3.6.1.2.1.43.8.2.1.<column>.<hrDeviceIndex>.<prtInputIndex>
	// Column definitions:
	//   2  = prtInputType
	//   9  = prtInputMaxCapacity
	//   10 = prtInputCurrentLevel
	//   11 = prtInputStatus
	//   12 = prtInputMediaName
	//   13 = prtInputName
	//   18 = prtInputDescription

	type TrayEntry struct {
		Name         string
		MediaType    string
		CurrentLevel int
		MaxCapacity  int
		Status       int
		TrayType     int
		Description  string
	}

	// Map of tray index -> entry
	entries := make(map[int]*TrayEntry)

	for _, pdu := range pdus {
		oid := normalizeOID(pdu.Name)

		// Parse prtInputTable entries
		// Format: 1.3.6.1.2.1.43.8.2.1.<column>.<hrIdx>.<inputIdx>
		if !strings.HasPrefix(oid, "1.3.6.1.2.1.43.8.2.1.") {
			continue
		}

		suffix := strings.TrimPrefix(oid, "1.3.6.1.2.1.43.8.2.1.")
		parts := strings.Split(suffix, ".")
		if len(parts) < 2 {
			continue
		}

		column, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		// Get tray index (last part or second-to-last if there's hrDeviceIndex)
		trayIdx := 0
		if len(parts) >= 3 {
			// Format: <column>.<hrDeviceIndex>.<prtInputIndex>
			trayIdx, _ = strconv.Atoi(parts[2])
		} else if len(parts) == 2 {
			// Format: <column>.<prtInputIndex>
			trayIdx, _ = strconv.Atoi(parts[1])
		}

		if trayIdx <= 0 {
			continue
		}

		// Initialize entry if not exists
		if entries[trayIdx] == nil {
			entries[trayIdx] = &TrayEntry{
				CurrentLevel: -1, // Unknown
				MaxCapacity:  -1, // Unknown
			}
		}
		entry := entries[trayIdx]

		// Populate fields based on column
		switch column {
		case 2: // prtInputType
			entry.TrayType = coerceToInt(pdu.Value)
		case 9: // prtInputMaxCapacity
			entry.MaxCapacity = coerceToInt(pdu.Value)
		case 10: // prtInputCurrentLevel
			entry.CurrentLevel = coerceToInt(pdu.Value)
		case 11: // prtInputStatus
			entry.Status = coerceToInt(pdu.Value)
		case 12: // prtInputMediaName
			if bytes, ok := pdu.Value.([]byte); ok {
				entry.MediaType = strings.TrimSpace(string(bytes))
			} else if str, ok := pdu.Value.(string); ok {
				entry.MediaType = strings.TrimSpace(str)
			}
		case 13: // prtInputName
			if bytes, ok := pdu.Value.([]byte); ok {
				entry.Name = strings.TrimSpace(string(bytes))
			} else if str, ok := pdu.Value.(string); ok {
				entry.Name = strings.TrimSpace(str)
			}
		case 18: // prtInputDescription
			if bytes, ok := pdu.Value.([]byte); ok {
				entry.Description = strings.TrimSpace(string(bytes))
			} else if str, ok := pdu.Value.(string); ok {
				entry.Description = strings.TrimSpace(str)
			}
		}
	}

	// Convert to slice and calculate percentages
	var trays []PaperTray
	for idx, entry := range entries {
		tray := PaperTray{
			Index:        idx,
			Name:         entry.Name,
			MediaType:    entry.MediaType,
			CurrentLevel: entry.CurrentLevel,
			MaxCapacity:  entry.MaxCapacity,
			TrayType:     entry.TrayType,
			Description:  entry.Description,
		}

		// Generate name if not provided
		if tray.Name == "" {
			tray.Name = fmt.Sprintf("Tray %d", idx)
		}

		// Calculate level percentage
		// Per RFC 3805 (Printer-MIB):
		//   Level/Capacity = -1: other/unknown
		//   Level/Capacity = -2: unknown
		//   Level = -3: someRemaining (low but usable)
		//   Capacity = -3: unlimited
		if entry.MaxCapacity > 0 && entry.CurrentLevel >= 0 {
			tray.LevelPercent = int((float64(entry.CurrentLevel) / float64(entry.MaxCapacity)) * 100)
		} else if entry.CurrentLevel == -3 {
			// someRemaining - estimate 10%
			tray.LevelPercent = 10
		} else {
			tray.LevelPercent = -1 // Unknown
		}

		// Determine status string
		tray.Status = determineTrayStatus(entry.CurrentLevel, entry.MaxCapacity, tray.LevelPercent)

		trays = append(trays, tray)
	}

	// Sort by index
	for i := 0; i < len(trays); i++ {
		for j := i + 1; j < len(trays); j++ {
			if trays[i].Index > trays[j].Index {
				trays[i], trays[j] = trays[j], trays[i]
			}
		}
	}

	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Paper trays parsed", "count", len(trays))
	}

	return trays
}

// determineTrayStatus returns a human-readable status based on level values
func determineTrayStatus(currentLevel, _ /* maxCapacity */, levelPercent int) string {
	// Check for special SNMP values
	if currentLevel == -1 || currentLevel == -2 {
		return "unknown"
	}
	if currentLevel == -3 {
		return "low" // someRemaining
	}
	if currentLevel == 0 {
		return "empty"
	}

	// Use percentage if available
	if levelPercent >= 0 {
		if levelPercent == 0 {
			return "empty"
		} else if levelPercent <= 10 {
			return "low"
		} else if levelPercent <= 25 {
			return "medium"
		}
		return "ok"
	}

	// If we have a positive level but couldn't calculate percentage, assume ok
	if currentLevel > 0 {
		return "ok"
	}

	return "unknown"
}

func getOIDInt(pdus []gosnmp.SnmpPDU, oid string) int {
	oid = normalizeOID(oid)
	for _, pdu := range pdus {
		if normalizeOID(pdu.Name) == oid || strings.HasPrefix(normalizeOID(pdu.Name), oid+".") {
			return coerceToInt(pdu.Value)
		}
	}
	return 0
}

func normalizeOID(oid string) string {
	return strings.TrimPrefix(oid, ".")
}

func coerceToInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return -1
}
