package vendor

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

func brotherBlob(t *testing.T, records ...string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(records, "") + "ff")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func octets(oid string, v []byte) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: v}
}

func integer(oid string, n int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: n}
}

func expectLevels(t *testing.T, result map[string]interface{}, want map[string]float64) {
	t.Helper()
	for key, w := range want {
		if got, ok := result[key].(float64); !ok || got != w {
			t.Errorf("%s = %v, want %v", key, result[key], w)
		}
	}
}

// Blobs captured from a DCP-L3550CDW (color laser MFP).
func TestBrotherParse_InfoBlobs(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		octets(oids.BrotherCounterInfo, brotherBlob(t,
			"0001040000064b", "02010400000386", "010104000002c5",
			"0601040000021a", "080104000000ed", "0701040000012d")),
		octets(oids.BrotherMaintenanceInfo, brotherBlob(t,
			"76010400000001", "790104000023f0", "7301040000064b", "77010400000001",
			"7a0104000023f0", "7401040000064b", "78010400000001", "7b0104000023f0",
			"7501040000064b", "7f010400000001", "800104000023f0", "7e01040000064b",
			"68010400000001", "55010400000001", "32010400000001", "700104000003e8",
			"8201040000000a", "33010400000002", "71010400000320", "8301040000000a",
			"34010400000002", "720104000000c8", "8401040000000a", "31010400000001",
			"6f010400000a28", "8101040000001e", "690104000025e4", "35010400000001",
			"6d010400002648", "54010400000001", "6a0104000025e4")),
		integer(oids.PrtMarkerLifeCount+".1", 1600),
		octets(oids.BrotherSerialNumber, []byte("E78000A1B2")),
		octets(oids.BrotherFirmwareVersion, []byte("J1906051424")),
		octets(oids.BrotherIEEE1284DeviceID, []byte("MFG:Brother;CMD:PJL,HBP,URF;MDL:DCP-L3550CDW;CLS:PRINTER;")),
		octets(oids.BrotherPanelDisplay, []byte("Toner Low (Y)  ")),
	}
	result := (&BrotherVendor{}).Parse(pdus)

	expectInts(t, result, map[string]int{
		"page_count": 1611, "total_pages": 1611, "mono_pages": 709, "color_pages": 902, "duplex_pages": 538,
	})
	// 0x6F wins for black (0x81 = 30 is outside the percent*100 scheme).
	expectLevels(t, result, map[string]float64{
		"toner_black": 26, "toner_cyan": 10, "toner_magenta": 8, "toner_yellow": 2,
		"drum_life": 92, "supply_drum_cyan": 92, "supply_drum_magenta": 92, "supply_drum_yellow": 92,
		"fuser_life": 97, "transfer_belt": 97,
	})
	for key, want := range map[string]string{
		"serial_number": "E78000A1B2", "firmware_version": "J1906051424", "model_name": "DCP-L3550CDW",
	} {
		if result[key] != want {
			t.Errorf("%s = %v, want %q", key, result[key], want)
		}
	}
	if got := result["status_messages"]; !reflect.DeepEqual(got, []string{"Toner Low (Y)"}) {
		t.Errorf("status_messages = %v", got)
	}
}

func TestBrotherParse_SupplyLevelEncodings(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		// Public Printer-MIB says cyan is 40%.
		octets(oids.PrtMarkerSuppliesDesc+".1.2", []byte("Cyan Toner Cartridge")),
		integer(oids.PrtMarkerSuppliesLevel+".1.2", 40),
		integer(oids.PrtMarkerSuppliesMaxCap+".1.2", 100),
		integer(oids.PrtMarkerSuppliesClass+".1.2", 3),
		octets(oids.BrotherMaintenanceInfo, brotherBlob(t,
			"8101040000001e", // black percent below 100: rejected
			"31010400000003", // black enum: replace -> 0%
			"32010400000002", // cyan enum low: keeps the public 40%
			"a301020414",     // magenta legacy ratio 4/20 -> 20%
			"6a010400000000", // fuser exhausted: 0 is a valid percentage
		)),
	}
	result := (&BrotherVendor{}).Parse(pdus)
	expectLevels(t, result, map[string]float64{"toner_black": 0, "toner_cyan": 40, "toner_magenta": 20, "fuser_life": 0})
	if _, ok := result["toner_yellow"]; ok {
		t.Errorf("toner_yellow should be absent, got %v", result["toner_yellow"])
	}
}

func TestBrotherParse_FunctionAndScanTables(t *testing.T) {
	fn := oids.BrotherFunctionCounterTable
	scan := oids.BrotherScanCounterTable
	// Row indexes are arbitrary; the type-code column decides the meaning.
	pdus := []gosnmp.SnmpPDU{
		integer(fn+".2.11", brotherFnPrintTotal), integer(fn+".3.11", 1000),
		integer(fn+".2.12", brotherFnPrintColor), integer(fn+".3.12", 300),
		integer(fn+".2.13", brotherFnPrintMono), integer(fn+".3.13", 700),
		integer(fn+".2.14", brotherFnCopyTotal), integer(fn+".3.14", 50),
		integer(fn+".2.15", brotherFnCopyColor), integer(fn+".3.15", 20),
		integer(fn+".2.16", brotherFnCopyMono), integer(fn+".3.16", 30),
		integer(fn+".2.17", brotherFnFaxTotal), integer(fn+".3.17", 99),
		integer(fn+".2.18", brotherFnFaxMono), integer(fn+".3.18", 12),
		integer(scan+".1.1", 1), integer(scan+".3.1", 40),
		integer(scan+".1.2", 3), integer(scan+".3.2", 10),
		integer(scan+".1.3", 2), integer(scan+".3.3", 5),
	}
	result := (&BrotherVendor{}).Parse(pdus)
	expectInts(t, result, map[string]int{
		"print_pages": 1000, "print_color_pages": 300, "print_mono_pages": 700,
		"copy_pages": 50, "copy_color_pages": 20, "copy_mono_pages": 30,
		"fax_mono_pages": 12, "fax_pages": 12,
		"scan_adf_simplex_pages": 40, "scan_adf_duplex_pages": 10, "scan_flatbed_pages": 5,
		"scan_adf_pages": 50, "scan_count": 55,
	})
}

func TestBrotherParse_MonoTotalsAndFaxFallback(t *testing.T) {
	fn := oids.BrotherFunctionCounterTable
	pdus := []gosnmp.SnmpPDU{
		integer(fn+".2.1", brotherFnPrintTotal), integer(fn+".3.1", 400),
		integer(fn+".2.3", brotherFnCopyTotal), integer(fn+".3.3", 25),
		integer(fn+".2.2", brotherFnFaxTotal), integer(fn+".3.2", 7),
	}
	result := (&BrotherVendor{}).Parse(pdus)
	expectInts(t, result, map[string]int{
		"print_mono_pages": 400, "copy_mono_pages": 25, "fax_pages": 7,
	})
}

func TestBrotherDetectAndFixture(t *testing.T) {
	if m := DetectVendor(".1.3.6.1.4.1.2435.2.3.9.1", "", ""); m.Name() != "Brother" {
		t.Fatalf("enterprise detection = %s", m.Name())
	}
	if !(&BrotherVendor{}).Detect("", "Brother NC-8700w, Firmware Ver.Y", "") {
		t.Fatal("sysDescr detection failed")
	}

	// HL-L8260CDW walk: truncated before the brInfo subtree, so only public data parses.
	pdus := loadVendorFixture(t, "brother_snmp.json", &BrotherVendor{})
	result := (&BrotherVendor{}).Parse(pdus)
	expectInts(t, result, map[string]int{"page_count": 42976})
	if result["model_name"] != "HL-L8260CDW series" {
		t.Errorf("model_name = %v", result["model_name"])
	}
	for _, key := range []string{"toner_black", "toner_cyan", "toner_magenta", "toner_yellow"} {
		if _, ok := result[key].(float64); !ok {
			t.Errorf("%s missing from public supplies", key)
		}
	}
}
