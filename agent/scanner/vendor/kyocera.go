package vendor

import (
	"fmt"
	"strings"

	"printmaster/agent/scanner/capabilities"
	"printmaster/common/logger"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// KyoceraVendor implements vendor-specific support for Kyocera devices via the
// enterprise OID tree 1.3.6.1.4.1.1347.*. OIDs and their semantics come from
// Kyocera Net Device Manager (def.dev.snmp-oid.xml plus the decompiled
// DeviceController operators).
// Reference: docs/dev/vendor/KYOCERA_OID_MAPPING.md
type KyoceraVendor struct{}

const (
	kyoceraFuncPrinter = 1
	kyoceraFuncCopier  = 2
	kyoceraFuncFax     = 4

	kyoceraModeMono        = 1
	kyoceraModeSingleColor = 2
	kyoceraModeFullColor   = 3
)

// KDM reports "Call for service" when kcprtAlertStateDisplayIndex holds one of these codes.
var kyoceraServiceCallCodes = map[int]bool{50100: true, 40070: true}

func init() {
	RegisterVendor(&KyoceraVendor{})
}

func (v *KyoceraVendor) Name() string {
	return "Kyocera"
}

func (v *KyoceraVendor) Detect(sysObjectID, sysDescr, model string) bool {
	if strings.Contains(sysObjectID, ".1.3.6.1.4.1.1347.") {
		return true
	}
	combined := strings.ToLower(sysDescr + " " + model)
	return strings.Contains(combined, "kyocera") || strings.Contains(combined, "taskalfa") || strings.Contains(combined, "ecosys")
}

func (v *KyoceraVendor) BaseOIDs() []string {
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
		oids.KyoceraGeneralModelName,
		oids.KyoceraAbsoluteModelName,
		oids.KyoceraSerialNumber,
		oids.KyoceraAssetNumber,
		oids.KyoceraFirmwareVersion + ".1",
		oids.KyoceraFirmwareVersion + ".2",
	}
}

func (v *KyoceraVendor) MetricOIDs(caps *capabilities.DeviceCapabilities) []string {
	list := []string{
		oids.KyoceraMarkerServiceCount,
		oids.PrtMarkerLifeCount + ".1",
		oids.PrtMarkerCounterUnit + ".1.1",
		oids.KyoceraMFPOtherScanCounter,
	}
	for n := 1; n <= 4; n++ {
		list = append(list,
			fmt.Sprintf("%s.%d", oids.KyoceraMFPCounter, n),
			fmt.Sprintf("%s.%d", oids.KyoceraMFPScanCounter, n),
		)
		for mode := kyoceraModeMono; mode <= kyoceraModeFullColor; mode++ {
			list = append(list, fmt.Sprintf("%s.%d.%d", oids.KyoceraMFPColorModeCounter, n, mode))
		}
	}
	for mode := kyoceraModeMono; mode <= kyoceraModeFullColor; mode++ {
		list = append(list, fmt.Sprintf("%s.%d", oids.KyoceraColorModeCounter, mode))
	}
	return list
}

// MetricTableOIDs returns Kyocera enterprise tables walked during metrics queries.
func (v *KyoceraVendor) MetricTableOIDs() []string {
	return []string{
		oids.KyoceraScanLifeCount,
		oids.KyoceraMediaCounterTable,
		oids.KyoceraColorCoverageTable,
		oids.KyoceraMaintenanceKitTable,
		oids.KyoceraAlertStateTable,
		oids.KyoceraScannerErrorState,
		oids.KyoceraFinisherTable,
	}
}

func (v *KyoceraVendor) SupplyOIDs() []string {
	return []string{
		oids.PrtMarkerSuppliesDesc,
		oids.PrtMarkerSuppliesLevel,
		oids.PrtMarkerSuppliesMaxCap,
		oids.PrtMarkerSuppliesClass,
		oids.PrtMarkerSuppliesType,
	}
}

func (v *KyoceraVendor) PaperTrayOIDs() []string {
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

func (v *KyoceraVendor) Parse(pdus []gosnmp.SnmpPDU) map[string]interface{} {
	result := make(map[string]interface{})
	if logger.Global != nil {
		logger.Global.TraceTag("vendor_parse", "Parsing Kyocera vendor PDUs", "pdu_count", len(pdus))
	}
	idx := newPDUIndex(pdus)

	parseKyoceraIdentity(idx, result)
	parseKyoceraCounters(idx, result)
	parseKyoceraScans(idx, pdus, result)
	parseKyoceraMediaCounters(pdus, result)
	parseKyoceraCoverage(pdus, result)
	parseKyoceraMaintenanceKits(pdus, result)

	if msgs := kyoceraStatusMessages(pdus); len(msgs) > 0 {
		result["status_messages"] = msgs
	}

	for k, val := range parseSuppliesTable(pdus) {
		result[k] = val
	}

	if logger.Global != nil {
		logger.Global.Debug("Kyocera parsing complete",
			"page_count", result["page_count"], "mono_pages", result["mono_pages"],
			"color_pages", result["color_pages"], "scan_count", result["scan_count"])
	}
	return result
}

func parseKyoceraIdentity(idx *pduIndex, result map[string]interface{}) {
	text := func(oid string) string {
		if pdu, ok := idx.byOID[normalizeOID(oid)]; ok {
			return strings.TrimSpace(pduText(pdu.Value))
		}
		return ""
	}
	for key, oid := range map[string]string{
		"model_name":              oids.KyoceraAbsoluteModelName,
		"serial_number":           oids.KyoceraSerialNumber,
		"asset_number":            oids.KyoceraAssetNumber,
		"firmware_version":        oids.KyoceraFirmwareVersion + ".1",
		"engine_firmware_version": oids.KyoceraFirmwareVersion + ".2",
	} {
		if s := text(oid); s != "" {
			result[key] = s
		}
	}
	if _, ok := result["model_name"]; !ok {
		if s := text(oids.KyoceraGeneralModelName); s != "" {
			result["model_name"] = s
		}
	}
}

// parseKyoceraCounters mirrors KDM's PageCount/MonoCount/FullColorCount operators.
func parseKyoceraCounters(idx *pduIndex, result map[string]interface{}) {
	colorMode := func(fn, mode int) (int, bool) {
		return epsonInt(idx, fmt.Sprintf("%s.%d.%d", oids.KyoceraMFPColorModeCounter, fn, mode))
	}
	funcCounter := func(n int) (int, bool) {
		return epsonInt(idx, fmt.Sprintf("%s.%d", oids.KyoceraMFPCounter, n))
	}

	var mono, single, full int
	haveMFP := false
	for fn := 1; fn <= 4; fn++ {
		if n, ok := colorMode(fn, kyoceraModeMono); ok && n > 0 {
			mono += n
			haveMFP = true
		}
		if n, ok := colorMode(fn, kyoceraModeSingleColor); ok && n > 0 {
			single += n
			haveMFP = true
		}
		if n, ok := colorMode(fn, kyoceraModeFullColor); ok && n > 0 {
			full += n
			haveMFP = true
		}
	}
	if !haveMFP {
		// Printer-only models (KDM logic 2) expose kmColorModeCounterItem instead.
		mono, _ = epsonInt(idx, oids.KyoceraColorModeCounter+".1")
		single, _ = epsonInt(idx, oids.KyoceraColorModeCounter+".2")
		full, _ = epsonInt(idx, oids.KyoceraColorModeCounter+".3")
	}

	positive := func(key string, n int) {
		if n > 0 {
			result[key] = n
		}
	}
	positive("mono_pages", mono)
	positive("color_pages", single+full)
	positive("full_color_pages", full)
	positive("single_color_pages", single)

	for _, f := range []struct {
		fn     int
		prefix string
	}{{kyoceraFuncPrinter, "print"}, {kyoceraFuncCopier, "copy"}, {kyoceraFuncFax, "fax"}} {
		m, _ := colorMode(f.fn, kyoceraModeMono)
		s, _ := colorMode(f.fn, kyoceraModeSingleColor)
		c, _ := colorMode(f.fn, kyoceraModeFullColor)
		positive(f.prefix+"_mono_pages", m)
		positive(f.prefix+"_color_pages", s+c)
		positive(f.prefix+"_full_color_pages", c)
		positive(f.prefix+"_single_color_pages", s)
		if m+s+c > 0 {
			result[f.prefix+"_pages"] = m + s + c
		}
	}
	if n, ok := funcCounter(1); ok && n > 0 {
		result["print_pages"] = n
	}
	if n, ok := funcCounter(2); ok && n > 0 {
		result["copy_pages"] = n
	}
	// Fax output is position 4 on MFPs and position 3 on older layouts.
	if n, ok := funcCounter(4); ok && n > 0 {
		result["fax_pages"] = n
	} else if n, ok := funcCounter(3); ok && n > 0 {
		result["fax_pages"] = n
	}

	total := 0
	if n, ok := epsonInt(idx, oids.KyoceraMarkerServiceCount); ok && n > 0 {
		total = n
	} else if n, ok := epsonInt(idx, oids.PrtMarkerLifeCount+".1"); ok && n > 0 {
		// KDM logic 1 only trusts prtMarkerLifeCount in impressions (7) or sheets (8).
		if unit, ok := epsonInt(idx, oids.PrtMarkerCounterUnit+".1.1"); !ok || unit == 7 || unit == 8 {
			total = n
		}
	}
	if total == 0 {
		total = mono + single + full
	}
	if total > 0 {
		result["page_count"] = total
		result["total_pages"] = total
	}
}

func parseKyoceraScans(idx *pduIndex, pdus []gosnmp.SnmpPDU, result map[string]interface{}) {
	scan := func(n int) int {
		v, _ := epsonInt(idx, fmt.Sprintf("%s.%d", oids.KyoceraMFPScanCounter, n))
		return v
	}
	copyScans := scan(2)
	faxScans := scan(4)
	if faxScans <= 0 {
		faxScans = scan(3)
	}
	otherScans, _ := epsonInt(idx, oids.KyoceraMFPOtherScanCounter)

	for key, n := range map[string]int{"copy_scans": copyScans, "fax_scans": faxScans, "other_scans": otherScans} {
		if n > 0 {
			result[key] = n
		}
	}

	// kmscnLifeCount is indexed by the scanner's hrDeviceIndex, so it is walked.
	life := 0
	prefix := normalizeOID(oids.KyoceraScanLifeCount) + "."
	for _, pdu := range pdus {
		if strings.HasPrefix(normalizeOID(pdu.Name), prefix) {
			if n := coerceToInt(pdu.Value); n > 0 {
				life += n
			}
		}
	}
	if life == 0 {
		life = max(copyScans, 0) + max(faxScans, 0) + max(otherScans, 0)
	}
	if life > 0 {
		result["scan_count"] = life
	}
}

// parseKyoceraMediaCounters decodes kmMediaCounterTable into paper_<size>_* meters.
func parseKyoceraMediaCounters(pdus []gosnmp.SnmpPDU, result map[string]interface{}) {
	table := epsonTable(pdus, oids.KyoceraMediaCounterTable)
	for _, row := range tableRows(table, 2) {
		total, ok := tableInt(table, 6, row)
		if !ok || total <= 0 {
			continue
		}
		slug := epsonSlug(pduText(table[2][row].Value))
		if slug == "" {
			continue
		}
		result["paper_"+slug+"_pages"] = total
		if n, ok := tableInt(table, 7, row); ok && n > 0 {
			result["paper_"+slug+"_mono_pages"] = n
		}
		full, _ := tableInt(table, 8, row)
		monoColor, _ := tableInt(table, 9, row)
		if full+monoColor > 0 {
			result["paper_"+slug+"_color_pages"] = full + monoColor
		}
	}
}

// parseKyoceraCoverage sums the low/medium/high coverage counters across device indexes.
func parseKyoceraCoverage(pdus []gosnmp.SnmpPDU, result map[string]interface{}) {
	table := epsonTable(pdus, oids.KyoceraColorCoverageTable)
	for col, key := range map[int]string{1: "coverage_low_pages", 2: "coverage_medium_pages", 3: "coverage_high_pages"} {
		sum := 0
		for _, row := range tableRows(table, col) {
			if n, ok := tableInt(table, col, row); ok && n > 0 {
				sum += n
			}
		}
		if sum > 0 {
			result[key] = sum
		}
	}
}

// parseKyoceraMaintenanceKits reports kcprtMaintenanceKitTable counters as raw meters;
// KDM passes them through unchanged so no remaining-life percentage is derived.
func parseKyoceraMaintenanceKits(pdus []gosnmp.SnmpPDU, result map[string]interface{}) {
	table := epsonTable(pdus, oids.KyoceraMaintenanceKitTable)
	for _, row := range tableRows(table, 4) {
		slug := epsonSlug(pduText(table[4][row].Value))
		if slug == "" {
			slug = "kit_" + epsonSlug(row)
		}
		base := "maintenance_kit_" + strings.TrimPrefix(slug, "maintenance_kit_")
		if n, ok := tableInt(table, 5, row); ok && n > 0 {
			result[base+"_count"] = n
		}
		if n, ok := tableInt(table, 6, row); ok && n > 0 {
			result[base+"_preset"] = n
		}
		if n, ok := tableInt(table, 2, row); ok && n > 0 {
			result[base+"_utilization"] = n
		}
	}
}

// kyoceraStatusMessages surfaces operator-panel text, KDM's "Call for service"
// alert codes, scanner cover state, and staple supply state.
func kyoceraStatusMessages(pdus []gosnmp.SnmpPDU) []string {
	var msgs []string
	seen := map[string]bool{}
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			msgs = append(msgs, m)
		}
	}

	alerts := epsonTable(pdus, oids.KyoceraAlertStateTable)
	rows := tableRows(alerts, 2)
	for _, row := range tableRows(alerts, 3) {
		if _, ok := alerts[2][row]; !ok {
			rows = append(rows, row)
		}
	}
	for _, row := range rows {
		text := ""
		if pdu, ok := alerts[2][row]; ok {
			text = strings.TrimSpace(pduText(pdu.Value))
		}
		code, hasCode := tableInt(alerts, 3, row)
		switch {
		case text != "" && hasCode && code > 0:
			add(fmt.Sprintf("%s (Kyocera code %d)", text, code))
		case text != "":
			add(text)
		case hasCode && code > 0:
			add(fmt.Sprintf("Kyocera code %d", code))
		}
		if hasCode && kyoceraServiceCallCodes[code] {
			add("Call for service")
		}
	}

	prefix := normalizeOID(oids.KyoceraScannerErrorState) + "."
	for _, pdu := range pdus {
		if strings.HasPrefix(normalizeOID(pdu.Name), prefix) {
			if n := coerceToInt(pdu.Value); n == 2 || (n > 0 && n&0x08 != 0) {
				add("Scanner cover open")
			}
		}
	}

	finisher := epsonTable(pdus, oids.KyoceraFinisherTable)
	if stapler, ok := tableInt(finisher, 8, "1.1"); ok && stapler == 3 {
		// kcprtStaplerConsumableState is signed (-2 = add staples), which tableInt rejects.
		if pdu, ok := finisher[9]["1.1"]; ok && coerceToInt(pdu.Value) == -2 {
			add("Add staples")
		}
	}
	return msgs
}
