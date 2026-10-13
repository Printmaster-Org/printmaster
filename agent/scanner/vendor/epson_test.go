package vendor

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// Live epPrtDeviceStatus (1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1) response from an
// Epson ST-C8090, captured alongside Epson Device Admin's UI for verification.
const epsonSTC8090ST2Hex = "40 42 44 43 20 53 54 32 0d 0a 79 00 01 01 04 06 02 01 01 0a 03 11 00 04 0d 01 32 0f 0d 03 01 00 44 04 02 45 05 03 45 03 01 45 " +
	"10 09 01 05 4e 02 19 4e 04 19 4e 13 01 01 19 0c 00 00 00 00 00 75 6e 6b 6e 6f 77 6e 28 04 ff 01 00 00 36 20 ff ff ff ff ff ff ff ff " +
	"dc 09 00 00 a1 11 00 00 35 00 00 00 a8 02 00 00 4a 00 00 00 0d 00 00 00 37 03 02 00 00 40 0a 58 39 48 48 30 30 31 30 33 38 46 01 02 4b 01 00"

func epsonSTC8090ST2(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(epsonSTC8090ST2Hex, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 133 {
		t.Fatalf("ST2 fixture length = %d, want 133", len(b))
	}
	return b
}

func intPDU(oid string, v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
}

func strPDU(oid, v string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
}

// epsonSTC8090PDUs reproduces the relevant subset of the live SNMP walk.
func epsonSTC8090PDUs(t *testing.T) []gosnmp.SnmpPDU {
	const e = "1.3.6.1.4.1.1248.1.2.2."
	pdus := []gosnmp.SnmpPDU{
		strPDU("1.3.6.1.2.1.1.1.0", "EPSON Built-in 11b/g/n Print Server"),
		intPDU("1.3.6.1.2.1.43.10.2.1.4.1.1", 7037),
		intPDU(e+"27.1.1.3.1.1", 4513),
		intPDU(e+"27.1.1.4.1.1", 2524),
		intPDU(e+"27.1.1.6.1.1", 0),
		intPDU(e+"36.1.1.2.1", 6),
		intPDU(e+"3.1.1.17.1", 46),
		intPDU(e+"3.1.1.18.1", 2501),
		{Name: "." + e + "1.1.1.4.1", Type: gosnmp.OctetString, Value: epsonSTC8090ST2(t)},
		// Simplex scalars are absent on this model.
		{Name: "." + e + "27.1.1.10.1.1", Type: gosnmp.NoSuchObject},
	}
	// Function table (27.6.1): idx -> type, unit, total, color
	for idx, row := range map[string][4]int{"1": {1, 3, 4091, 2440}, "2": {2, 3, 2913, 54}, "3": {3, 3, 0, 0}, "4": {0, 3, 33, 30}} {
		for col, v := range map[int]int{2: row[0], 3: row[1], 4: row[2], 5: row[3]} {
			pdus = append(pdus, intPDU(e+"27.6.1."+itoa(col)+".1.1."+idx, v))
		}
	}
	// Paper-size table (6.1.1): row -> name, total, color, simplex, color simplex
	type paper struct {
		name                string
		total, color, s, cs int
	}
	for row, p := range map[string]paper{"1": {"A3/Ledger", 255, 105, 254, 105}, "2": {"A4/Letter", 6782, 2419, 6540, 2411}, "3": {"A5", 0, 0, 0, 0}} {
		pdus = append(pdus,
			strPDU(e+"6.1.1.2.1."+row, p.name),
			intPDU(e+"6.1.1.3.1."+row, 3),
			intPDU(e+"6.1.1.4.1."+row, p.total),
			intPDU(e+"6.1.1.5.1."+row, p.color),
			intPDU(e+"6.1.1.7.1."+row, p.s),
			intPDU(e+"6.1.1.8.1."+row, 0),
			intPDU(e+"6.1.1.9.1."+row, p.cs),
		)
	}
	// Scanner input table (1.4.2.6.1.1): row1 flatbed, row2 ADF
	const s = "1.3.6.1.4.1.1248.1.4.2.6.1.1."
	pdus = append(pdus,
		intPDU(s+"2.3.1", 3), intPDU(s+"4.3.1", 398), intPDU(s+"7.3.1", 92), intPDU(s+"8.3.1", 0), intPDU(s+"9.3.1", 3),
		intPDU(s+"2.3.2", 4), intPDU(s+"4.3.2", 680), intPDU(s+"7.3.2", 465), intPDU(s+"8.3.2", 680), intPDU(s+"9.3.2", 3),
	)
	// Epson supplies table (28.1.1): 4 ink bottles + maintenance box
	for i, lvl := range []int{68, 69, 69, 69} {
		n := itoa(i + 1)
		pdus = append(pdus, intPDU(e+"28.1.1.2.1."+n, lvl), intPDU(e+"28.1.1.3.1."+n, -2), intPDU(e+"28.1.1.21.1."+n, 1002))
	}
	pdus = append(pdus,
		intPDU(e+"28.1.1.2.1.5", 50), intPDU(e+"28.1.1.3.1.5", 2),
		strPDU(e+"28.1.1.5.1.5", "Maintenance Cartridge C9345"), intPDU(e+"28.1.1.21.1.5", 1003),
	)
	// Standard Printer-MIB supplies (inks only; no maintenance box)
	for i, d := range []struct {
		desc string
		lvl  int
	}{{"Black Ink Bottle", 68}, {"Cyan Ink Bottle", 69}, {"Magenta Ink Bottle", 69}, {"Yellow Ink Bottle", 69}} {
		n := itoa(i + 1)
		pdus = append(pdus,
			strPDU("1.3.6.1.2.1.43.11.1.1.6.1."+n, d.desc),
			intPDU("1.3.6.1.2.1.43.11.1.1.9.1."+n, d.lvl),
			intPDU("1.3.6.1.2.1.43.11.1.1.8.1."+n, 100),
			intPDU("1.3.6.1.2.1.43.11.1.1.4.1."+n, 3),
		)
	}
	return pdus
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Expected values match the counters shown in Epson Device Admin for the device.
func TestEpsonVendorParse_MatchesEpsonDeviceAdmin(t *testing.T) {
	t.Parallel()
	result := (&EpsonVendor{}).Parse(epsonSTC8090PDUs(t))

	want := map[string]int{
		"total_pages": 7037, "page_count": 7037, "mono_pages": 4513, "color_pages": 2524,
		"print_pages": 4091, "print_color_pages": 2440, "print_mono_pages": 1651,
		"copy_pages": 2913, "copy_color_pages": 54, "copy_mono_pages": 2859,
		"fax_pages": 0, "other_pages": 33, "other_color_pages": 30, "other_mono_pages": 3,
		"simplex_pages": 6794, "duplex_pages": 243,
		"simplex_color_pages": 2516, "simplex_mono_pages": 4278,
		"duplex_color_pages": 8, "duplex_mono_pages": 235,
		"paper_a3_ledger_pages": 255, "paper_a3_ledger_color_pages": 105, "paper_a3_ledger_mono_pages": 150, "paper_a3_ledger_duplex_pages": 1,
		"paper_a4_letter_pages": 6782, "paper_a4_letter_color_pages": 2419, "paper_a4_letter_mono_pages": 4363, "paper_a4_letter_duplex_pages": 242,
		"scan_count": 1078, "scan_color_pages": 557, "scan_mono_pages": 521,
		"scan_flatbed_pages": 398, "scan_flatbed_color_pages": 92, "scan_flatbed_mono_pages": 306,
		"scan_adf_pages": 680, "scan_adf_color_pages": 465, "scan_adf_mono_pages": 215,
		"jams": 6, "power_on_count": 46, "wake_from_standby_count": 2501,
		"maintenance_box_replacements": 2,
	}
	for k, v := range want {
		if got, ok := result[k].(int); !ok || got != v {
			t.Errorf("%s = %v, want %d", k, result[k], v)
		}
	}
	if got, ok := result["supply_maintenance_cartridge_c9345"].(float64); !ok || got != 50 {
		t.Errorf("maintenance cartridge = %v, want 50", result["supply_maintenance_cartridge_c9345"])
	}
	// The ST2 maintenance fallback must not duplicate the named cartridge.
	if _, ok := result["supply_maintenance_box"]; ok {
		t.Errorf("unexpected duplicate supply_maintenance_box: %v", result)
	}
	// Standard Printer-MIB remains the source of ink levels.
	for k := range result {
		if strings.HasPrefix(k, "ink_") {
			t.Errorf("ST2 ink key %s should not override standard supplies", k)
		}
	}
	// Composite-black counter is zero here and must not be mistaken for mono.
	if result["mono_pages"] == 0 {
		t.Error("mono_pages must come from epPrtMarkerTotalPrinted, not 27.1.1.6")
	}
}

func TestEpsonVendorParse_ST2OnlyFallback(t *testing.T) {
	t.Parallel()
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1", Type: gosnmp.OctetString, Value: epsonSTC8090ST2(t)},
	}
	result := (&EpsonVendor{}).Parse(pdus)

	wantInt := map[string]int{"mono_pages": 4513, "color_pages": 2524, "total_pages": 7037, "maintenance_box_replacements": 2}
	for k, v := range wantInt {
		if got, ok := result[k].(int); !ok || got != v {
			t.Errorf("%s = %v, want %d", k, result[k], v)
		}
	}
	wantLevel := map[string]float64{"ink_black": 68, "ink_cyan": 69, "ink_magenta": 69, "ink_yellow": 69, "supply_maintenance_box": 50}
	for k, v := range wantLevel {
		if got, ok := result[k].(float64); !ok || got != v {
			t.Errorf("%s = %v, want %v", k, result[k], v)
		}
	}
}

func TestParseST2Response_LiveSTC8090(t *testing.T) {
	t.Parallel()
	st, err := ParseST2Response(epsonSTC8090ST2(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.StatusText != "Idle" || !st.Ready {
		t.Errorf("status = %q ready=%v", st.StatusText, st.Ready)
	}
	for color, lvl := range map[string]int{"Black": 68, "Cyan": 69, "Magenta": 69, "Yellow": 69} {
		if st.InkLevels[color] != lvl {
			t.Errorf("%s = %d, want %d", color, st.InkLevels[color], lvl)
		}
	}
	if len(st.InkLevels) != 4 {
		t.Errorf("ink levels = %v", st.InkLevels)
	}
	if len(st.MaintenanceTankFree) != 1 || st.MaintenanceTankFree[0] != 50 || st.TankIncompatible {
		t.Errorf("tank free = %v incompatible=%v", st.MaintenanceTankFree, st.TankIncompatible)
	}
	if st.TotalPages != -1 || st.ColorPages != 2524 || st.MonoPages != 4513 {
		t.Errorf("paper counts = %d/%d/%d", st.TotalPages, st.ColorPages, st.MonoPages)
	}
	if st.SerialNumber != "X9HH001038" {
		t.Errorf("serial = %q", st.SerialNumber)
	}
	if len(st.MaintenanceBoxReplacements) != 1 || st.MaintenanceBoxReplacements[0] != 2 {
		t.Errorf("replacements = %v", st.MaintenanceBoxReplacements)
	}
	if !st.CISS {
		t.Error("expected CISS flag from field 0x28")
	}
	if st.MaintenanceBoxes[1] != "OK" {
		t.Errorf("maintenance box 1 = %q", st.MaintenanceBoxes[1])
	}
}

func TestST2InkNameFollowsEDARule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code, aio byte
		want      string
	}{
		{1, 0, "Black"},
		{9, 7, "Dark Yellow"},
		{2, 1, "Unknown 2"}, // codes < 64 never fall back to the all-in-one table
		{0x40, 2, "Magenta"},
		{0x41, 10, "Violet"},
	}
	for _, c := range cases {
		if got := st2InkName(c.code, c.aio); got != c.want {
			t.Errorf("st2InkName(%d,%d) = %q, want %q", c.code, c.aio, got, c.want)
		}
	}
}

func TestEpsonMetricTableOIDsAreWalkRoots(t *testing.T) {
	t.Parallel()
	roots := MetricTableOIDs(&EpsonVendor{})
	if len(roots) == 0 {
		t.Fatal("expected Epson metric table walk roots")
	}
	if MetricTableOIDs(&GenericVendor{}) != nil {
		t.Fatal("generic vendor should not declare metric tables")
	}
}

// WF-C878R epPrtInputTable rows from a live walk: rear ASF without a roller
// counter, then the standard cassette and three optional cassettes.
func TestEpsonInputRollersWFC878R(t *testing.T) {
	const tbl = "1.3.6.1.4.1.1248.1.2.2.23.1.1."
	var pdus []gosnmp.SnmpPDU
	feedTypes := []int{6, 101, 201, 202, 203}
	levels := []int{-2, 95, 95, 100, 100}
	changed := []int{-2, 0, 0, 0, 1}
	for i := range feedTypes {
		row := fmt.Sprintf(".1.%d", i+1)
		pdus = append(pdus,
			intPDU(tbl+"14"+row, feedTypes[i]),
			intPDU(tbl+"8"+row, 8),
			intPDU(tbl+"9"+row, levels[i]),
			intPDU(tbl+"5"+row, changed[i]),
		)
		if i > 0 {
			pdus = append(pdus, strPDU(tbl+"13"+row, "S210049"))
		}
	}

	got := parseEpsonInputRollers(pdus)
	want := map[string]interface{}{
		"supply_feed_roller_1":       95.0,
		"supply_feed_roller_2":       95.0,
		"supply_feed_roller_3":       100.0,
		"supply_feed_roller_4":       100.0,
		"feed_roller_1_replacements": 0,
		"feed_roller_2_replacements": 0,
		"feed_roller_3_replacements": 0,
		"feed_roller_4_replacements": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d metrics %v, want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}

	all := (&EpsonVendor{}).Parse(pdus)
	if all["supply_feed_roller_1"] != 95.0 {
		t.Errorf("Parse did not include roller levels: %v", all)
	}
}
