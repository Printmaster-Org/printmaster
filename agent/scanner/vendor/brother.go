package vendor

import (
	"math"
	"strings"
	"unicode/utf8"

	"printmaster/agent/scanner/capabilities"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// BrotherVendor implements vendor-specific support for Brother devices via the
// enterprise OID tree 1.3.6.1.4.1.2435.*. OIDs and their semantics follow Print
// Audit ICE's Brother scan logic; the binary blob layout was cross-checked
// against real device captures.
// Reference: docs/dev/vendor/BROTHER_OID_MAPPING.md
type BrotherVendor struct{}

func init() {
	RegisterVendor(&BrotherVendor{})
}

func (v *BrotherVendor) Name() string {
	return "Brother"
}

func (v *BrotherVendor) Detect(sysObjectID, sysDescr, model string) bool {
	if strings.HasPrefix(normalizeOID(sysObjectID), oids.BrotherEnterprise+".") {
		return true
	}
	return strings.Contains(strings.ToLower(sysDescr+" "+model), "brother")
}

func (v *BrotherVendor) BaseOIDs() []string {
	return []string{
		oids.SysDescr,
		oids.SysObjectID,
		oids.SysUpTime,
		oids.SysName,
		oids.SysLocation,
		oids.HrDeviceDescr,
		oids.HrDeviceStatus,
		oids.PrtGeneralPrinterName,
		oids.PrtGeneralSerialNumber,
		oids.HrPrinterStatus + ".1",
		oids.HrPrinterDetectedErrorState + ".1",
		oids.BrotherIEEE1284DeviceID,
		oids.BrotherSerialNumber,
		oids.BrotherFirmwareVersion,
		oids.BrotherFirmwareLegacy,
		oids.BrotherPanelDisplay,
	}
}

func (v *BrotherVendor) MetricOIDs(caps *capabilities.DeviceCapabilities) []string {
	return []string{
		oids.PrtMarkerLifeCount + ".1",
		oids.BrotherCounterInfo,
		oids.BrotherMaintenanceInfo,
	}
}

// MetricTableOIDs returns Brother enterprise tables walked during metrics queries.
func (v *BrotherVendor) MetricTableOIDs() []string {
	return []string{
		oids.BrotherFunctionCounterTable,
		oids.BrotherScanCounterTable,
	}
}

func (v *BrotherVendor) SupplyOIDs() []string {
	return []string{
		oids.PrtMarkerSuppliesDesc,
		oids.PrtMarkerSuppliesLevel,
		oids.PrtMarkerSuppliesMaxCap,
		oids.PrtMarkerSuppliesClass,
		oids.PrtMarkerSuppliesType,
	}
}

func (v *BrotherVendor) PaperTrayOIDs() []string {
	return []string{
		oids.PrtInputName,
		oids.PrtInputMediaName,
		oids.PrtInputCurrentLevel,
		oids.PrtInputMaxCapacity,
		oids.PrtInputStatus,
		oids.PrtInputType,
		oids.PrtInputDescription,
	}
}

func (v *BrotherVendor) Parse(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	result := make(map[string]interface{})
	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Parsing Brother vendor PDUs", "pdu_count", len(pdus))
	}
	idx := newPDUIndex(pdus)

	parseBrotherIdentity(idx, result)

	// Print Audit takes the public life count first, then lets the private blob override it.
	if n, ok := epsonInt(idx, oids.PrtMarkerLifeCount+".1"); ok && n > 0 {
		result["page_count"] = n
		result["total_pages"] = n
	}
	parseBrotherLifeCounters(brotherInfoRecords(idx, oids.BrotherCounterInfo), result)

	for k, val := range parseSuppliesTable(pdus) {
		result[k] = val
	}
	parseBrotherMaintenance(brotherInfoRecords(idx, oids.BrotherMaintenanceInfo), result)

	isColor := intVal(result["color_pages"]) > 0
	if _, ok := result["toner_cyan"]; ok {
		isColor = true
	}
	parseBrotherFunctionCounters(pdus, isColor, result)
	parseBrotherScans(pdus, result)

	if logger.Global != nil {
		logger.Global.Debug("Brother parsing complete",
			"page_count", result["page_count"], "mono_pages", result["mono_pages"],
			"color_pages", result["color_pages"], "scan_count", result["scan_count"])
	}
	return result
}

func parseBrotherIdentity(idx *pduIndex, result map[string]interface{}) {
	text := func(oid string) string {
		if pdu, ok := idx.byOID[normalizeOID(oid)]; ok {
			return strings.TrimSpace(pduText(pdu.Value))
		}
		return ""
	}
	if s := text(oids.BrotherSerialNumber); s != "" {
		result["serial_number"] = s
	}
	if s := text(oids.BrotherFirmwareVersion); s != "" {
		result["firmware_version"] = s
	} else if s := text(oids.BrotherFirmwareLegacy); s != "" {
		result["firmware_version"] = s
	}
	if model := ieee1284Field(text(oids.BrotherIEEE1284DeviceID), "MDL", "MODEL"); model != "" {
		result["model_name"] = model
	}
	// The panel text is encoded in the device's console charset; only surface it when it is clean UTF-8.
	if s := text(oids.BrotherPanelDisplay); s != "" && utf8.ValidString(s) {
		result["status_messages"] = []string{s}
	}
}

// ieee1284Field returns the first matching KEY:value; field from an IEEE 1284 device ID.
func ieee1284Field(id string, keys ...string) string {
	for _, part := range strings.Split(id, ";") {
		k, val, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		for _, key := range keys {
			if strings.EqualFold(strings.TrimSpace(k), key) {
				return strings.TrimSpace(val)
			}
		}
	}
	return ""
}

// brotherRecord is one entry of a Brother binary info blob.
type brotherRecord struct {
	raw   []byte
	value int
}

// brotherInfoRecords decodes a Brother info blob: repeated <id><flag><len><len bytes>
// records whose bytes form a big-endian integer, terminated by 0xFF. The first
// record for an id wins.
func brotherInfoRecords(idx *pduIndex, oid string) map[byte]brotherRecord {
	pdu, ok := idx.byOID[normalizeOID(oid)]
	if !ok {
		return nil
	}
	var data []byte
	switch v := pdu.Value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return nil
	}
	records := make(map[byte]brotherRecord)
	for i := 0; i+3 <= len(data) && data[i] != 0xFF; {
		id, n := data[i], int(data[i+2])
		start := i + 3
		if start+n > len(data) {
			break
		}
		raw := data[start : start+n]
		value := 0
		for _, b := range raw {
			value = value<<8 | int(b)
		}
		if _, seen := records[id]; !seen {
			records[id] = brotherRecord{raw: raw, value: value}
		}
		i = start + n
	}
	return records
}

// parseBrotherLifeCounters reads brInfoCounter: 0x00 total, 0x01 mono, 0x02 color, 0x06 duplex.
func parseBrotherLifeCounters(records map[byte]brotherRecord, result map[string]interface{}) {
	if r, ok := records[0x00]; ok && r.value > 0 {
		result["page_count"] = r.value
		result["total_pages"] = r.value
	}
	if r, ok := records[0x01]; ok && r.value > 0 {
		result["mono_pages"] = r.value
	}
	if r, ok := records[0x02]; ok && r.value > 0 {
		result["color_pages"] = r.value
	}
	if r, ok := records[0x06]; ok && r.value > 0 {
		result["duplex_pages"] = r.value
	}
}

// Brother supply level encodings from brInfoMaintenance.
const (
	brotherLevelPercent = iota // value is percent * 100 (3400 = 34%)
	brotherLevelEnum           // 1=OK, 2=low, 3=replace, 4=no unit
	brotherLevelRatio          // two bytes: current, max
)

type brotherSupplyID struct {
	id        byte
	levelType int
}

// brotherSupplies lists maintenance ids per supply in Print Audit's preference order.
var brotherSupplies = []struct {
	key string
	ids []brotherSupplyID
}{
	{"toner_black", []brotherSupplyID{{0x6F, brotherLevelPercent}, {0x81, brotherLevelPercent}, {0x31, brotherLevelEnum}, {0xA1, brotherLevelRatio}}},
	{"toner_cyan", []brotherSupplyID{{0x70, brotherLevelPercent}, {0x82, brotherLevelPercent}, {0x32, brotherLevelEnum}, {0xA2, brotherLevelRatio}}},
	{"toner_magenta", []brotherSupplyID{{0x71, brotherLevelPercent}, {0x83, brotherLevelPercent}, {0x33, brotherLevelEnum}, {0xA3, brotherLevelRatio}}},
	{"toner_yellow", []brotherSupplyID{{0x72, brotherLevelPercent}, {0x84, brotherLevelPercent}, {0x34, brotherLevelEnum}, {0xA4, brotherLevelRatio}}},
	{"drum_life", []brotherSupplyID{{0x41, brotherLevelPercent}, {0x80, brotherLevelPercent}, {0x7F, brotherLevelEnum}}},
	{"supply_drum_cyan", []brotherSupplyID{{0x79, brotherLevelPercent}, {0x76, brotherLevelEnum}}},
	{"supply_drum_magenta", []brotherSupplyID{{0x7A, brotherLevelPercent}, {0x77, brotherLevelEnum}}},
	{"supply_drum_yellow", []brotherSupplyID{{0x7B, brotherLevelPercent}, {0x78, brotherLevelEnum}}},
	{"fuser_life", []brotherSupplyID{{0x6A, brotherLevelPercent}}},
	{"transfer_belt", []brotherSupplyID{{0x69, brotherLevelPercent}}},
}

// parseBrotherMaintenance merges private supply levels over the public Printer-MIB
// values: percentages always win, while an enumerated level only fills in a missing
// or unknown value (replace / no unit become 0%).
func parseBrotherMaintenance(records map[byte]brotherRecord, result map[string]interface{}) {
	if len(records) == 0 {
		return
	}
	for _, s := range brotherSupplies {
		for _, sid := range s.ids {
			r, ok := records[sid.id]
			if !ok {
				continue
			}
			switch sid.levelType {
			case brotherLevelPercent:
				// Non-zero values below 100 are not in the percent*100 scheme; try the next id.
				if r.value != 0 && r.value < 100 {
					continue
				}
				result[s.key] = math.Round(float64(r.value)/10) / 10
			case brotherLevelRatio:
				if len(r.raw) < 2 || r.raw[1] == 0 {
					continue
				}
				result[s.key] = math.Round(float64(r.raw[0])*1000/float64(r.raw[1])) / 10
			case brotherLevelEnum:
				if existing, ok := result[s.key].(float64); ok && existing >= 0 {
					break
				}
				if r.value == 3 || r.value == 4 {
					result[s.key] = 0.0
				}
			}
			break
		}
	}
}

// Brother function counter type codes (brCounterTable column 2).
const (
	brotherFnPrintTotal = 1
	brotherFnFaxTotal   = 2
	brotherFnCopyTotal  = 3
	brotherFnCopyColor  = 4
	brotherFnPrintColor = 5
	brotherFnFaxColor   = 6
	brotherFnPrintMono  = 7
	brotherFnFaxMono    = 8
	brotherFnCopyMono   = 9
)

// brotherTypedCounts maps a type-code column to the count column of the same row.
func brotherTypedCounts(pdus []gosnmp.SnmpPDU, root string, typeCol, countCol int) map[int]int {
	table := epsonTable(pdus, root)
	counts := make(map[int]int)
	for _, row := range tableRows(table, typeCol) {
		code, ok := tableInt(table, typeCol, row)
		if !ok {
			continue
		}
		if _, seen := counts[code]; seen {
			continue
		}
		if n, ok := tableInt(table, countCol, row); ok {
			counts[code] = n
		}
	}
	return counts
}

func parseBrotherFunctionCounters(pdus []gosnmp.SnmpPDU, isColor bool, result map[string]interface{}) {
	counts := brotherTypedCounts(pdus, oids.BrotherFunctionCounterTable, 2, 3)
	if len(counts) == 0 {
		return
	}
	positive := func(key string, n int) {
		if n > 0 {
			result[key] = n
		}
	}
	positive("print_pages", counts[brotherFnPrintTotal])
	positive("print_color_pages", counts[brotherFnPrintColor])
	positive("print_mono_pages", counts[brotherFnPrintMono])
	positive("copy_pages", counts[brotherFnCopyTotal])
	positive("copy_color_pages", counts[brotherFnCopyColor])
	positive("copy_mono_pages", counts[brotherFnCopyMono])
	// Mono devices only report totals; Print Audit books them as mono.
	if !isColor {
		if _, ok := result["print_color_pages"]; !ok {
			positive("print_mono_pages", counts[brotherFnPrintTotal])
		}
		if _, ok := result["copy_color_pages"]; !ok {
			positive("copy_mono_pages", counts[brotherFnCopyTotal])
		}
	}

	faxColor, faxMono := counts[brotherFnFaxColor], counts[brotherFnFaxMono]
	positive("fax_color_pages", faxColor)
	positive("fax_mono_pages", faxMono)
	if fax := max(faxColor, 0) + max(faxMono, 0); fax > 0 {
		result["fax_pages"] = fax
	} else {
		positive("fax_pages", counts[brotherFnFaxTotal])
	}
}

// parseBrotherScans reads brScanCounterTable: type 1 ADF simplex, 2 flatbed, 3 ADF duplex.
func parseBrotherScans(pdus []gosnmp.SnmpPDU, result map[string]interface{}) {
	counts := brotherTypedCounts(pdus, oids.BrotherScanCounterTable, 1, 3)
	simplex, flatbed, duplex := max(counts[1], 0), max(counts[2], 0), max(counts[3], 0)
	for key, n := range map[string]int{
		"scan_adf_simplex_pages": simplex,
		"scan_adf_duplex_pages":  duplex,
		"scan_flatbed_pages":     flatbed,
	} {
		if n > 0 {
			result[key] = n
		}
	}
	if simplex+duplex > 0 {
		result["scan_adf_pages"] = simplex + duplex
	}
	if total := simplex + duplex + flatbed; total > 0 {
		result["scan_count"] = total
	}
}
