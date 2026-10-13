package vendor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"printmaster/agent/featureflags"
	"printmaster/agent/scanner/capabilities"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// EpsonVendor implements vendor-specific support for Epson devices.
// OIDs and derivations mirror what Epson Device Admin (EDA) reads, verified
// against a live device. Reference: docs/vendor/EPSON_OID_MAPPING.md
type EpsonVendor struct{}

func init() {
	RegisterVendor(&EpsonVendor{})
}

func (v *EpsonVendor) Name() string {
	return "Epson"
}

func (v *EpsonVendor) Detect(sysObjectID, sysDescr, model string) bool {
	// Check enterprise number (1248)
	if strings.Contains(sysObjectID, ".1.3.6.1.4.1.1248.") {
		return true
	}

	// Check sysDescr or model for "Epson"
	combined := strings.ToLower(sysDescr + " " + model)
	return strings.Contains(combined, "epson") || strings.Contains(combined, "workforce") || strings.Contains(combined, "surecolor")
}

func (v *EpsonVendor) BaseOIDs() []string {
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
		// Epson-specific identity
		oids.EpsonModelName,
		oids.EpsonSerialNumber,
		// ST2 status frame (status, inks, maintenance box, paper counts)
		oids.EpsonDeviceStatus,
	}
}

func (v *EpsonVendor) MetricOIDs(caps *capabilities.DeviceCapabilities) []string {
	return []string{
		oids.PrtMarkerLifeCount + ".1",
		oids.EpsonMarkerTotalPrinted,
		oids.EpsonMarkerColorPrinted,
		oids.EpsonMarkerProcessColorForMono,
		oids.EpsonMarkerSimplexTotal,
		oids.EpsonMarkerSimplexColor,
		oids.EpsonTotalPagesLegacy,
		oids.EpsonTotalPagesLegacy2,
		oids.EpsonSinglePassColorMode,
		oids.EpsonSinglePassColorModeAlt,
		oids.EpsonJamCount,
		oids.EpsonPowerOnCount,
		oids.EpsonMoveFromStandbyCount,
	}
}

// MetricTableOIDs returns Epson enterprise tables walked during metrics queries.
func (v *EpsonVendor) MetricTableOIDs() []string {
	return []string{
		oids.EpsonFunctionTable,
		oids.EpsonPaperSizeTable,
		oids.EpsonScanInputTable,
		oids.EpsonSuppliesTable,
		oids.EpsonInputTable,
	}
}

func (v *EpsonVendor) SupplyOIDs() []string {
	return []string{
		oids.PrtMarkerSuppliesDesc,
		oids.PrtMarkerSuppliesLevel,
		oids.PrtMarkerSuppliesMaxCap,
		oids.PrtMarkerSuppliesClass,
		oids.PrtMarkerSuppliesType,
	}
}

func (v *EpsonVendor) PaperTrayOIDs() []string {
	return []string{
		oids.PrtInputName,
		oids.PrtInputMediaName,
		oids.PrtInputCurrentLevel,
		oids.PrtInputMaxCapacity,
		oids.PrtInputStatus,
		oids.PrtInputType,
		oids.PrtInputDescription,
		oids.PrtInputDimUnit,
		oids.PrtInputMediaDimFeedDirDeclared,
		oids.PrtInputMediaDimXFeedDirDeclared,
	}
}

func (v *EpsonVendor) Parse(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	result := make(map[string]interface{})
	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Parsing Epson vendor PDUs", "pdu_count", len(pdus))
	}

	idx := newPDUIndex(pdus)
	st2 := QueryEpsonRemoteStatus(pdus)

	// === Totals (EDA: total = prtMarkerLifeCount, mono = 27.1.1.3, color = 27.1.1.4) ===
	total, hasTotal := epsonInt(idx, oids.PrtMarkerLifeCount+".1")
	if !hasTotal {
		total, hasTotal = epsonInt(idx, oids.EpsonTotalPagesLegacy)
	}
	if !hasTotal {
		total, hasTotal = epsonInt(idx, oids.EpsonTotalPagesLegacy2)
	}
	mono, hasMono := epsonInt(idx, oids.EpsonMarkerTotalPrinted)
	color, hasColor := epsonInt(idx, oids.EpsonMarkerColorPrinted)
	if st2 != nil {
		if !hasTotal && st2.TotalPages >= 0 {
			total, hasTotal = st2.TotalPages, true
		}
		if !hasMono && st2.MonoPages >= 0 {
			mono, hasMono = st2.MonoPages, true
		}
		if !hasColor && st2.ColorPages >= 0 {
			color, hasColor = st2.ColorPages, true
		}
	}
	if !hasTotal && hasMono && hasColor {
		total, hasTotal = mono+color, true
	}
	if hasTotal && hasColor && !hasMono && total >= color {
		mono, hasMono = total-color, true
	}

	// === Function counters (print/copy/fax/other) ===
	functions := parseEpsonFunctionTable(pdus)
	for k, val := range functions {
		result[k] = val
	}
	if !hasTotal {
		if sum := intVal(functions["print_pages"]) + intVal(functions["copy_pages"]) + intVal(functions["fax_pages"]) + intVal(functions["other_pages"]); sum > 0 {
			total, hasTotal = sum, true
		}
	}

	if hasTotal {
		result["total_pages"] = total
		result["page_count"] = total
	}
	if hasMono {
		result["mono_pages"] = mono
	}
	if hasColor {
		result["color_pages"] = color
	}
	if n, ok := epsonInt(idx, oids.EpsonMarkerProcessColorForMono); ok {
		result["process_color_for_mono_pages"] = n
	}
	if n, ok := epsonInt(idx, oids.EpsonSinglePassColorMode); ok {
		result["single_pass_color_mode_pages"] = n
	} else if n, ok := epsonInt(idx, oids.EpsonSinglePassColorModeAlt); ok {
		result["single_pass_color_mode_pages"] = n
	}

	// === Paper sizes and simplex/duplex ===
	paperRows := parseEpsonPaperSizeTable(pdus)
	largeTotal, largeColor, haveLarge := 0, 0, false
	for _, row := range paperRows {
		slug := epsonSlug(row.name)
		if slug == "" {
			continue
		}
		result["paper_"+slug+"_pages"] = row.total
		if row.hasColor {
			result["paper_"+slug+"_color_pages"] = row.color
			result["paper_"+slug+"_mono_pages"] = row.total - row.color
		}
		if row.hasSimplex {
			result["paper_"+slug+"_duplex_pages"] = row.total - row.simplex
		}
		if epsonLargePaperSizes[slug] {
			haveLarge = true
			largeTotal += row.total
			if row.hasColor {
				largeColor += row.color
			}
		}
	}
	if haveLarge && largeTotal > 0 {
		result["large_paper_pages"] = largeTotal
		result["large_paper_mono_pages"] = largeTotal - largeColor
		result["large_paper_color_pages"] = largeColor
	}
	for k, val := range epsonDuplexMetrics(idx, total, hasTotal, color, hasColor, paperRows) {
		result[k] = val
	}

	// === Scanner counters ===
	for k, val := range parseEpsonScanTable(pdus) {
		result[k] = val
	}
	for k, val := range parseEpsonInputRollers(pdus) {
		result[k] = val
	}

	// === Misc counters ===
	if n, ok := epsonInt(idx, oids.EpsonJamCount); ok {
		result["jams"] = n
	}
	if n, ok := epsonInt(idx, oids.EpsonPowerOnCount); ok {
		result["power_on_count"] = n
	}
	if n, ok := epsonInt(idx, oids.EpsonMoveFromStandbyCount); ok {
		result["wake_from_standby_count"] = n
	}

	// === Supplies ===
	// Standard Printer-MIB is authoritative for inks; Epson's supplies table
	// adds the maintenance box (not exposed in prtMarkerSuppliesTable).
	supplyLevels := parseSuppliesTable(pdus)
	for k, val := range supplyLevels {
		result[k] = val
	}
	maint := parseEpsonMaintenanceBoxes(pdus)
	for k, val := range maint {
		if _, exists := result[k]; !exists {
			result[k] = val
		}
	}

	// === ST2 fallbacks ===
	if st2 != nil {
		st2Metrics := st2.ToMetrics()
		for k, val := range st2Metrics {
			switch {
			case strings.HasPrefix(k, "ink_"):
				if len(supplyLevels) == 0 {
					result[k] = val
				}
			case strings.HasPrefix(k, "supply_maintenance_box"):
				if len(maint) == 0 && !hasMaintenanceSupply(result) {
					result[k] = val
				}
			case strings.HasPrefix(k, "maintenance_box_replacements"):
				if _, exists := result[k]; !exists {
					result[k] = val
				}
			case k == "total_pages", k == "mono_pages", k == "color_pages":
				// Already used as fallbacks above.
			default:
				if _, exists := result[k]; !exists {
					result[k] = val
				}
			}
		}
	}

	if logger.Global != nil {
		logger.Global.Debug("Epson parsing complete",
			"total_pages", total, "mono_pages", mono, "color_pages", color,
			"scan_count", intVal(result["scan_count"]), "st2", st2 != nil, "metrics", len(result))
	}
	return result
}

// ParseWithRemoteMode extends Parse with a dedicated status fetch when the
// Epson remote-mode feature flag is enabled and the ST2 frame was not already
// collected by the regular query (epPrtDeviceStatus in BaseOIDs).
func (v *EpsonVendor) ParseWithRemoteMode(ctx context.Context, pdus []gosnmp.SnmpPDU, ip string, timeoutSeconds int) map[string]interface{} {
	result := v.Parse(pdus)

	if !featureflags.EpsonRemoteModeEnabled() || ip == "" {
		if logger.Global != nil {
			logger.Global.TraceTag("epson_remote", "Epson remote mode: skipped", "enabled", featureflags.EpsonRemoteModeEnabled(), "ip_set", ip != "")
		}
		return result
	}
	if QueryEpsonRemoteStatus(pdus) != nil {
		return result
	}

	if logger.Global != nil {
		logger.Global.TraceTag("epson_remote", "Epson remote mode: fetching extended metrics", "ip", ip)
	}
	remoteMetrics := FetchEpsonRemoteMetricsWithIP(ctx, ip, timeoutSeconds)
	for k, val := range remoteMetrics {
		if _, exists := result[k]; !exists {
			result[k] = val
		}
	}
	if logger.Global != nil && remoteMetrics != nil {
		logger.Global.Debug("Epson remote mode: merged metrics", "ip", ip, "remote_metrics", len(remoteMetrics), "total_metrics", len(result))
	}
	return result
}

// --- helpers ---

// epsonInt returns an integer value only if the OID is present and numeric.
func epsonInt(idx *pduIndex, oid string) (int, bool) {
	pdu, ok := idx.byOID[normalizeOID(oid)]
	if !ok {
		return 0, false
	}
	switch pdu.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return 0, false
	}
	switch pdu.Value.(type) {
	case int, int32, int64, uint, uint32, uint64:
		n := coerceToInt(pdu.Value)
		return n, n >= 0
	}
	return 0, false
}

func pduText(value interface{}) string {
	switch v := value.(type) {
	case []byte:
		return strings.TrimRight(string(v), "\x00")
	case string:
		return strings.TrimRight(v, "\x00")
	}
	return ""
}

func intVal(v interface{}) int {
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

// epsonTable groups PDUs under root as column -> row-suffix -> PDU.
func epsonTable(pdus []gosnmp.SnmpPDU, root string) map[int]map[string]gosnmp.SnmpPDU {
	prefix := normalizeOID(root) + "."
	table := make(map[int]map[string]gosnmp.SnmpPDU)
	for _, pdu := range pdus {
		oid := normalizeOID(pdu.Name)
		if !strings.HasPrefix(oid, prefix) {
			continue
		}
		rest := strings.TrimPrefix(oid, prefix)
		dot := strings.IndexByte(rest, '.')
		if dot <= 0 {
			continue
		}
		var col int
		if _, err := fmt.Sscanf(rest[:dot], "%d", &col); err != nil {
			continue
		}
		if table[col] == nil {
			table[col] = make(map[string]gosnmp.SnmpPDU)
		}
		table[col][rest[dot+1:]] = pdu
	}
	return table
}

func tableInt(table map[int]map[string]gosnmp.SnmpPDU, col int, row string) (int, bool) {
	pdu, ok := table[col][row]
	if !ok {
		return 0, false
	}
	idx := &pduIndex{byOID: map[string]gosnmp.SnmpPDU{"x": pdu}}
	return epsonInt(idx, "x")
}

func tableRows(table map[int]map[string]gosnmp.SnmpPDU, col int) []string {
	rows := make([]string, 0, len(table[col]))
	for row := range table[col] {
		rows = append(rows, row)
	}
	sort.Strings(rows)
	return rows
}

// epsonLargePaperSizes are the paper-size names (as slugs) billed as "large",
// matching Print Audit's Epson large-paper classification.
var epsonLargePaperSizes = map[string]bool{"a3": true, "a3_legal": true, "a3_ledger": true, "a3_wide": true, "us_b": true}

func epsonSlug(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
		} else if !underscore && b.Len() > 0 {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// parseEpsonFunctionTable decodes 27.6.1: col2 type, col3 unit, col4 total, col5 color.
func parseEpsonFunctionTable(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	table := epsonTable(pdus, oids.EpsonFunctionTable)
	names := map[int]string{0: "other", 1: "print", 2: "copy", 3: "fax"}
	totals := map[string]int{}
	colors := map[string]int{}
	hasColor := map[string]bool{}
	for _, row := range tableRows(table, 4) {
		fnType, ok := tableInt(table, 2, row)
		if !ok {
			continue
		}
		name, ok := names[fnType]
		if !ok {
			continue
		}
		// EDA only accepts rows whose unit is 3 (pages).
		if unit, ok := tableInt(table, 3, row); ok && unit != 3 {
			continue
		}
		t, ok := tableInt(table, 4, row)
		if !ok {
			continue
		}
		totals[name] += t
		if c, ok := tableInt(table, 5, row); ok {
			colors[name] += c
			hasColor[name] = true
		}
	}
	result := make(map[string]interface{})
	for name, t := range totals {
		result[name+"_pages"] = t
		if hasColor[name] {
			result[name+"_color_pages"] = colors[name]
			result[name+"_mono_pages"] = t - colors[name]
		}
	}
	return result
}

type epsonPaperRow struct {
	name                  string
	total, color          int
	simplex, colorSimplex int
	hasColor, hasSimplex  bool
	hasColorSimplex       bool
}

// parseEpsonPaperSizeTable decodes 6.1.1 rows that EDA considers valid
// (unit column 3 non-empty and numeric total in column 4).
func parseEpsonPaperSizeTable(pdus []gosnmp.SnmpPDU) []epsonPaperRow {
	table := epsonTable(pdus, oids.EpsonPaperSizeTable)
	var rows []epsonPaperRow
	for _, row := range tableRows(table, 4) {
		unit, ok := table[3][row]
		if !ok || (pduText(unit.Value) == "" && coerceToInt(unit.Value) < 0) {
			continue
		}
		t, ok := tableInt(table, 4, row)
		if !ok {
			continue
		}
		r := epsonPaperRow{name: pduText(table[2][row].Value), total: t}
		r.color, r.hasColor = tableInt(table, 5, row)
		r.simplex, r.hasSimplex = tableInt(table, 7, row)
		r.colorSimplex, r.hasColorSimplex = tableInt(table, 9, row)
		rows = append(rows, r)
	}
	return rows
}

// epsonDuplexMetrics follows EDA: prefer scalar simplex counters with the
// marker total; otherwise sum the paper-size table.
func epsonDuplexMetrics(idx *pduIndex, total int, hasTotal bool, color int, hasColor bool, rows []epsonPaperRow) map[string]interface{} {
	result := make(map[string]interface{})
	if s, ok := epsonInt(idx, oids.EpsonMarkerSimplexTotal); ok && hasTotal && total >= s {
		duplex := total - s
		result["simplex_pages"] = s
		result["duplex_pages"] = duplex
		if sc, ok := epsonInt(idx, oids.EpsonMarkerSimplexColor); ok && hasColor {
			duplexColor := color - sc
			result["simplex_color_pages"] = sc
			result["simplex_mono_pages"] = s - sc
			result["duplex_color_pages"] = duplexColor
			result["duplex_mono_pages"] = duplex - duplexColor
		} else if !hasColor {
			result["simplex_mono_pages"] = s
			result["duplex_mono_pages"] = duplex
		}
		return result
	}

	var simplex, duplex, simplexColor, duplexColor int
	anySimplex, allColorSimplex := false, true
	for _, r := range rows {
		if !r.hasSimplex {
			continue
		}
		anySimplex = true
		simplex += r.simplex
		duplex += r.total - r.simplex
		if r.hasColor && r.hasColorSimplex {
			simplexColor += r.colorSimplex
			duplexColor += r.color - r.colorSimplex
		} else {
			allColorSimplex = false
		}
	}
	if !anySimplex {
		return result
	}
	result["simplex_pages"] = simplex
	result["duplex_pages"] = duplex
	if allColorSimplex {
		result["simplex_color_pages"] = simplexColor
		result["simplex_mono_pages"] = simplex - simplexColor
		result["duplex_color_pages"] = duplexColor
		result["duplex_mono_pages"] = duplex - duplexColor
	}
	return result
}

// parseEpsonScanTable decodes the scanner input table (1.4.2.6.1.1).
func parseEpsonScanTable(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	table := epsonTable(pdus, oids.EpsonScanInputTable)
	sources := map[int]string{3: "flatbed", 4: "adf", 5: "sheetfeed", 6: "check"}
	result := make(map[string]interface{})
	add := func(key string, n int) { result[key] = intVal(result[key]) + n }

	var total, colorTotal int
	anyRow, allColor := false, true
	for _, row := range tableRows(table, 4) {
		srcType, ok := tableInt(table, 2, row)
		if !ok {
			continue
		}
		src, ok := sources[srcType]
		if !ok {
			continue
		}
		t, ok := tableInt(table, 4, row)
		if !ok {
			continue
		}
		unit := "pages"
		if u, ok := tableInt(table, 9, row); ok && u == 1 {
			unit = "sheets"
		}
		base := "scan_" + src
		add(base+"_"+unit, t)
		anyRow = true
		total += t
		if c, ok := tableInt(table, 7, row); ok {
			add(base+"_color_"+unit, c)
			add(base+"_mono_"+unit, t-c)
			colorTotal += c
		} else {
			allColor = false
		}
		if d, ok := tableInt(table, 5, row); ok {
			add(base+"_duplex_"+unit, d)
			add(base+"_simplex_"+unit, t-d)
		}
		if src == "adf" {
			if residue, ok := tableInt(table, 12, row); ok && residue <= 100 {
				result["supply_adf_roller"] = float64(residue)
			}
			if n, ok := tableInt(table, 14, row); ok {
				add("scan_adf_roller_replacements", n)
			}
		}
	}
	if anyRow {
		result["scan_count"] = total
		if allColor {
			result["scan_color_pages"] = colorTotal
			result["scan_mono_pages"] = total - colorTotal
		}
	}
	return result
}

// parseEpsonMaintenanceBoxes reads type-1003 rows of the Epson supplies table.
func parseEpsonMaintenanceBoxes(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	table := epsonTable(pdus, oids.EpsonSuppliesTable)
	result := make(map[string]interface{})
	box := 0
	for _, row := range tableRows(table, 21) {
		if t, ok := tableInt(table, 21, row); !ok || t != 1003 {
			continue
		}
		desc := pduText(table[5][row].Value)
		if desc == "" {
			desc = pduText(table[18][row].Value)
		}
		key := st2MaintenanceKey("supply_maintenance_box", box)
		if desc != "" {
			key = "supply_" + strings.ReplaceAll(strings.ToLower(desc), " ", "_")
		}
		if level, ok := tableInt(table, 2, row); ok && level <= 100 {
			result[key] = float64(level)
		}
		if n, ok := tableInt(table, 3, row); ok {
			result[st2MaintenanceKey("maintenance_box_replacements", box)] = n
		}
		box++
	}
	return result
}

// parseEpsonInputRollers reads paper-feed roller life from epPrtInputTable,
// following EDA: feed type 6 is the rear ASF roller, other inputs are feed
// rollers numbered in table order. Levels are only reported when the counter
// unit is percent (8).
func parseEpsonInputRollers(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	table := epsonTable(pdus, oids.EpsonInputTable)
	result := make(map[string]interface{})
	rear, feed := false, 0
	for _, row := range tableRows(table, 14) {
		feedType, ok := tableInt(table, 14, row)
		if !ok {
			continue
		}
		var name string
		if feedType == 6 {
			if rear {
				continue
			}
			rear = true
			name = "rear_feed_roller"
		} else {
			feed++
			name = fmt.Sprintf("feed_roller_%d", feed)
		}
		if unit, ok := tableInt(table, 8, row); ok && unit == 8 {
			if level, ok := tableInt(table, 9, row); ok && level >= 0 && level <= 100 {
				result["supply_"+name] = float64(level)
			}
		}
		if n, ok := tableInt(table, 5, row); ok && n >= 0 {
			result[name+"_replacements"] = n
		}
	}
	return result
}

func hasMaintenanceSupply(result map[string]interface{}) bool {
	for k := range result {
		if strings.HasPrefix(k, "supply_") && strings.Contains(k, "maint") {
			return true
		}
	}
	return false
}
