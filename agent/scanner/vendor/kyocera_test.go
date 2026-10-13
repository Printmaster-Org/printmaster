package vendor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// loadKyoceraFixture converts a captured SNMP walk into PDUs restricted to the
// OIDs the Kyocera module would actually request (GET scalars + walked tables).
func loadKyoceraFixture(t *testing.T, name string) []gosnmp.SnmpPDU {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Skipf("fixture %s not available: %v", name, err)
	}
	var records []struct {
		OID   string `json:"oid"`
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}

	v := &KyoceraVendor{}
	scalars := map[string]bool{}
	for _, oid := range append(v.BaseOIDs(), v.MetricOIDs(nil)...) {
		scalars[normalizeOID(oid)] = true
	}
	var roots []string
	for _, root := range append(append(v.SupplyOIDs(), v.PaperTrayOIDs()...), v.MetricTableOIDs()...) {
		roots = append(roots, normalizeOID(root)+".")
	}
	requested := func(oid string) bool {
		if scalars[oid] {
			return true
		}
		for _, r := range roots {
			if len(oid) > len(r) && oid[:len(r)] == r {
				return true
			}
		}
		return false
	}

	var pdus []gosnmp.SnmpPDU
	for _, r := range records {
		oid := normalizeOID(r.OID)
		if !requested(oid) {
			continue
		}
		pdu := gosnmp.SnmpPDU{Name: "." + oid}
		switch r.Type {
		case "Integer", "Counter32", "Gauge32", "TimeTicks":
			n, err := strconv.Atoi(r.Value)
			if err != nil {
				continue
			}
			pdu.Type, pdu.Value = gosnmp.Integer, n
		default:
			pdu.Type, pdu.Value = gosnmp.OctetString, []byte(r.Value)
		}
		pdus = append(pdus, pdu)
	}
	return pdus
}

func expectInts(t *testing.T, result map[string]interface{}, want map[string]int) {
	t.Helper()
	for key, w := range want {
		if got, ok := result[key].(int); !ok || got != w {
			t.Errorf("%s = %v, want %d", key, result[key], w)
		}
	}
}

func TestKyoceraParse_TASKalfa4053ci(t *testing.T) {
	result := (&KyoceraVendor{}).Parse(loadKyoceraFixture(t, "kyocera_cs4053_snmp.json"))

	expectInts(t, result, map[string]int{
		"page_count":         76064, // kcprtMarkerServiceCount (KDM PageCount logic 2)
		"total_pages":        76064,
		"mono_pages":         14730 + 19124 + 2493,
		"color_pages":        25870 + 13847,
		"full_color_pages":   25870 + 13847,
		"print_pages":        40600,
		"print_mono_pages":   14730,
		"print_color_pages":  25870,
		"copy_pages":         32971,
		"copy_mono_pages":    19124,
		"copy_color_pages":   13847,
		"fax_pages":          2493,
		"fax_mono_pages":     2493,
		"copy_scans":         10500,
		"fax_scans":          948,
		"other_scans":        219030,
		"scan_count":         230478, // kmscnLifeCount.3
		"paper_letter_pages": 72237,
		"paper_ledger_pages": 3498,
		"paper_legal_pages":  329,
	})
	if _, ok := result["single_color_pages"]; ok {
		t.Errorf("single_color_pages should be absent when zero, got %v", result["single_color_pages"])
	}
	if got := result["serial_number"]; got != "RH28Y00024" {
		t.Errorf("serial_number = %v", got)
	}
	if got := result["model_name"]; got != "TASKalfa 4053ci" {
		t.Errorf("model_name = %v", got)
	}
	if got := result["firmware_version"]; got != "2V8_S000.002.801" {
		t.Errorf("firmware_version = %v", got)
	}
	if got := result["status_messages"]; !reflect.DeepEqual(got, []string{"Preparing to print... (Kyocera code 10003)"}) {
		t.Errorf("status_messages = %v", got)
	}
	sum := 0
	for _, k := range []string{"paper_letter_pages", "paper_ledger_pages", "paper_legal_pages"} {
		sum += result[k].(int)
	}
	if sum != result["page_count"].(int) {
		t.Errorf("media counters sum %d != page_count %v", sum, result["page_count"])
	}
}

func TestKyoceraParse_ECOSYSP2040dw(t *testing.T) {
	result := (&KyoceraVendor{}).Parse(loadKyoceraFixture(t, "kyocera_p2040_snmp.json"))

	expectInts(t, result, map[string]int{
		"page_count": 7342,
	})
	if _, ok := result["scan_count"]; ok {
		t.Errorf("printer-only model should not report scan_count, got %v", result["scan_count"])
	}
	if _, ok := result["color_pages"]; ok {
		t.Errorf("mono model should not report color_pages, got %v", result["color_pages"])
	}
	if got := result["status_messages"]; !reflect.DeepEqual(got, []string{"Sleeping (Kyocera code 35078)"}) {
		t.Errorf("status_messages = %v", got)
	}
	if got := result["firmware_version"]; got != "2RX_3000.002.503" {
		t.Errorf("firmware_version = %v", got)
	}
}

func TestKyoceraParse_PrinterColorModeCounters(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.4.1.1347.42.2.2.1.1.3.1.1", Value: 900},
		{Name: ".1.3.6.1.4.1.1347.42.2.2.1.1.3.1.2", Value: 25},
		{Name: ".1.3.6.1.4.1.1347.42.2.2.1.1.3.1.3", Value: 75},
		{Name: ".1.3.6.1.2.1.43.10.2.1.4.1.1", Value: 1000},
		{Name: ".1.3.6.1.2.1.43.10.2.1.3.1.1", Value: 7},
	}
	result := (&KyoceraVendor{}).Parse(pdus)
	expectInts(t, result, map[string]int{
		"mono_pages":         900,
		"color_pages":        100,
		"full_color_pages":   75,
		"single_color_pages": 25,
		"page_count":         1000,
	})
}

func TestKyoceraParse_LifeCountIgnoredForNonImpressionUnits(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.43.10.2.1.4.1.1", Value: 123456},
		{Name: ".1.3.6.1.2.1.43.10.2.1.3.1.1", Value: 3}, // tenThousandthsOfInches
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.1.1", Value: 50},
	}
	result := (&KyoceraVendor{}).Parse(pdus)
	expectInts(t, result, map[string]int{"page_count": 50})
}

func TestKyoceraParse_AlertsMaintenanceAndCoverage(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.4.1.1347.43.18.2.1.2.1.1", Value: []byte("Call service. C6000")},
		{Name: ".1.3.6.1.4.1.1347.43.18.2.1.3.1.1", Value: 50100},
		{Name: ".1.3.6.1.4.1.1347.46.18.1.1.2.3", Value: 2},
		{Name: ".1.3.6.1.4.1.1347.43.9.1.1.8.1.1", Value: 3},
		{Name: ".1.3.6.1.4.1.1347.43.9.1.1.9.1.1", Value: -2},
		{Name: ".1.3.6.1.4.1.1347.43.24.1.1.4.1", Value: []byte("MK-1150")},
		{Name: ".1.3.6.1.4.1.1347.43.24.1.1.5.1", Value: 12000},
		{Name: ".1.3.6.1.4.1.1347.43.24.1.1.6.1", Value: 100000},
		{Name: ".1.3.6.1.4.1.1347.42.2.4.1.1.1.1.1", Value: 10},
		{Name: ".1.3.6.1.4.1.1347.42.2.4.1.1.1.1.2", Value: 5},
		{Name: ".1.3.6.1.4.1.1347.42.2.4.1.1.3.1.1", Value: 7},
	}
	result := (&KyoceraVendor{}).Parse(pdus)

	want := []string{
		"Call service. C6000 (Kyocera code 50100)",
		"Call for service",
		"Scanner cover open",
		"Add staples",
	}
	if got := result["status_messages"]; !reflect.DeepEqual(got, want) {
		t.Errorf("status_messages = %v, want %v", got, want)
	}
	expectInts(t, result, map[string]int{
		"maintenance_kit_mk_1150_count":  12000,
		"maintenance_kit_mk_1150_preset": 100000,
		"coverage_low_pages":             15,
		"coverage_high_pages":            7,
	})
}

func TestKyoceraMetricTableOIDsAreWalkRoots(t *testing.T) {
	roots := MetricTableOIDs(&KyoceraVendor{})
	if len(roots) == 0 {
		t.Fatal("expected Kyocera metric table roots")
	}
	scalars := map[string]bool{}
	for _, oid := range (&KyoceraVendor{}).MetricOIDs(nil) {
		scalars[oid] = true
	}
	for _, r := range roots {
		if scalars[r] {
			t.Errorf("%s is both a scalar GET and a walk root", r)
		}
	}
}
