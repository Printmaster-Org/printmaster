package agent

import (
	"fmt"
	"testing"

	"github.com/gosnmp/gosnmp"
)

func TestParsePDUsPreservesNamedInkAndMaintenanceSupplies(t *testing.T) {
	t.Parallel()
	supplies := []struct {
		desc  string
		level int
	}{
		{"Photo Black Ink Cartridge T44H1", 28},
		{"Matte Black Ink Cartridge T44H8", 61},
		{"Cyan Ink Cartridge T44H2", 27},
		{"Light Cyan Ink Cartridge T44H5", 72},
		{"Vivid Magenta Ink Cartridge T44H3", 30},
		{"Vivid Light Magenta Ink Cartridge T44H6", 49},
		{"Maintenance Box T6997", 39},
		{"Maintenance Box2 T6997", 100},
	}
	var pdus []gosnmp.SnmpPDU
	for i, supply := range supplies {
		for column, value := range map[int]interface{}{6: supply.desc, 9: supply.level, 8: 100} {
			pdus = append(pdus, gosnmp.SnmpPDU{Name: fmt.Sprintf("1.3.6.1.2.1.43.11.1.1.%d.1.%d", column, i+1), Value: value})
		}
	}
	pi, ok := ParsePDUsWithoutNetwork("192.0.2.10", pdus, nil, nil)
	if !ok {
		t.Fatal("supply table was not detected as printer data")
	}
	for _, supply := range supplies {
		if got, ok := pi.TonerLevels[supply.desc]; !ok || got != supply.level {
			t.Fatalf("lost %s: %+v", supply.desc, pi.TonerLevels)
		}
	}
	if got := pi.TonerLevels[pi.TonerDescBlack]; got != pi.TonerLevelBlack {
		t.Fatalf("legacy black description/level disagree: %s %d != %d", pi.TonerDescBlack, got, pi.TonerLevelBlack)
	}
}

func TestSerialLabelBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "protocol", text: "SNMPv2"},
		{name: "protocol-in-description", text: "Network printer supports SNMPv2 and IPP"},
		{name: "attached-sn", text: "SN123456"},
		{name: "attached-slash-label", text: "S/NABC123"},
		{name: "attached-serial", text: "SerialABC123"},
		{name: "attached-long-label", text: "SerialNumberABC123"},
		{name: "embedded-label", text: "MSN: ABC123"},
		{name: "embedded-serial", text: "XSerial: ABC123"},
		{name: "colon", text: "SN:ABC123", want: "ABC123"},
		{name: "equals", text: "SN = ABC123", want: "ABC123"},
		{name: "whitespace", text: "SN ABC123", want: "ABC123"},
		{name: "slash-label", text: "S/N ABC123", want: "ABC123"},
		{name: "serial", text: "Serial: ABC123", want: "ABC123"},
		{name: "serial-number", text: "Serial Number: ABC123", want: "ABC123"},
		{name: "serial-number-space", text: "Serial Number ABC123", want: "ABC123"},
		{name: "serialnumber", text: "SerialNumber=ABC123", want: "ABC123"},
		{name: "case-and-hyphen", text: "s/n:\tAbC-123", want: "AbC-123"},
		{name: "structured-text", text: "MFG:ACME;SN:ABC123;", want: "ABC123"},
		{name: "real-label-after-protocol", text: "SNMPv2; SN:ABC123", want: "ABC123"},
		{name: "missing-value", text: "SN:"},
		{name: "short-value", text: "SN:ABC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Exercise the production matcher without ParsePDUs' web-UI probes.
			got := ""
			if match := serialLabelRE.FindStringSubmatch(tt.text); len(match) > 1 {
				got = match[1]
			}
			if got != tt.want {
				t.Fatalf("serial label in %q = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestParsePDUs_NonUTF8SerialAndHexCounter(t *testing.T) {
	t.Parallel()
	t.Run("non-utf8-serial-sanitized", func(t *testing.T) {
		t.Parallel()
		// serial contains a NUL byte which should be stripped by DecodeOctetString
		vars := []gosnmp.SnmpPDU{
			// Use prtGeneralSerialNumber (17.1) for serial, not 16.1 (description)
			{Name: "1.3.6.1.2.1.43.5.1.1.17.1", Type: gosnmp.OctetString, Value: []byte{'S', 'N', 0x00, 'A', 'B', 'C'}},
		}
		pi, ok := ParsePDUs("10.0.0.1", vars, nil, nil)
		if !ok {
			t.Fatalf("expected device to be detected as printer due to serial, got not-printer")
		}
		if pi.Serial != "SNABC" {
			t.Fatalf("expected sanitized serial 'SNABC', got '%s'", pi.Serial)
		}
	})

	t.Run("hex-counter-parsed", func(t *testing.T) {
		t.Parallel()
		// marker life count provided as hex string should parse to integer
		vars := []gosnmp.SnmpPDU{
			{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.OctetString, Value: []byte("0xb3e7")},
		}
		pi, ok := ParsePDUs("10.0.0.2", vars, nil, nil)
		if !ok {
			t.Fatalf("expected device to be detected as printer due to marker count, got not-printer")
		}
		if pi.PageCount != 0xb3e7 {
			t.Fatalf("expected PageCount %d got %d", 0xb3e7, pi.PageCount)
		}
		if pi.MonoImpressions != 0xb3e7 {
			t.Fatalf("expected MonoImpressions %d got %d", 0xb3e7, pi.MonoImpressions)
		}
	})
}

func TestParsePDUs_OID16_UUID_DoesNotSetSerial(t *testing.T) {
	t.Parallel()
	// Ensure UUID-like value at 1.3.6.1.2.1.43.5.1.1.16.1 does not become serial
	uuid := "5995673a-a33d-49d2-a45e-1852a977"
	vars := []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.2.1.43.5.1.1.16.1", Type: gosnmp.OctetString, Value: []byte(uuid)},
		// Provide a marker count so the device is detected as a printer
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.OctetString, Value: []byte("1234")},
	}
	pi, ok := ParsePDUs("10.0.0.3", vars, nil, nil)
	if !ok {
		t.Fatalf("expected device to be detected as printer due to marker count")
	}
	if pi.Serial != "" {
		t.Fatalf("expected serial to remain empty when only OID 16.1 UUID present; got '%s'", pi.Serial)
	}
	if pi.Description != uuid {
		t.Fatalf("expected description to capture UUID '%s', got '%s'", uuid, pi.Description)
	}
}

func TestParsePDUs_VendorDeviceIDSetsModelAndSerial(t *testing.T) {
	t.Parallel()

	vars := []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.4.1.11.2.3.9.1.1.7.0", Type: gosnmp.OctetString, Value: []byte("MFG:HP;MDL:LaserJet 400;SN:CN123456;DES:Workgroup printer;")},
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.OctetString, Value: []byte("1500")},
	}

	pi, ok := ParsePDUs("10.0.0.4", vars, nil, nil)
	if !ok {
		t.Fatalf("expected vendor device ID to still mark as printer via marker count")
	}
	if pi.Model != "LaserJet 400" {
		t.Fatalf("expected model LaserJet 400, got %q", pi.Model)
	}
	if pi.Serial != "CN123456" {
		t.Fatalf("expected serial CN123456, got %q", pi.Serial)
	}
	if pi.Description != "Workgroup printer" {
		t.Fatalf("expected description from DES field, got %q", pi.Description)
	}
}

func TestParsePDUs_RicohEnterpriseOID(t *testing.T) {
	t.Parallel()

	vars := []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.2.1.1.2.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.367.1.1"},
		{Name: "1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("RICOH IM C4510 2.07 / RICOH Network Printer C model")},
		{Name: "1.3.6.1.4.1.367.3.2.1.1.1.11.0", Type: gosnmp.OctetString, Value: []byte("MFG:RICOH;MDL:IM C4510;SN:9175R401338;DES:RICOH IM C4510;")},
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.Integer, Value: 207124},
	}

	pi, ok := ParsePDUs("192.168.100.99", vars, nil, nil)
	if !ok {
		t.Fatal("expected Ricoh device to be detected as a printer")
	}
	if pi.Manufacturer != "Ricoh" {
		t.Errorf("Manufacturer = %q, want Ricoh", pi.Manufacturer)
	}
}

func TestMergeVendorMetrics_EpsonICEOIDs(t *testing.T) {
	t.Parallel()

	// Simulate Epson enterprise OIDs as ICE would query them
	// OIDs must match the constants in common/snmp/oids/oids.go exactly
	pdus := []gosnmp.SnmpPDU{
		// sysObjectID to trigger Epson vendor detection
		{Name: "1.3.6.1.2.1.1.2.0", Type: gosnmp.OctetString, Value: []byte("1.3.6.1.4.1.1248.1.2")},
		// ICE-style Epson page counts (using exact OIDs from constants: EpsonTotalPages, etc.)
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.1.1.33.1.1", Type: gosnmp.Integer, Value: 15000}, // EpsonTotalPages
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.1.1.6.1.1", Type: gosnmp.Integer, Value: 8000},   // EpsonMonoPages
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.1.1.4.1.1", Type: gosnmp.Integer, Value: 7000},   // EpsonColorPages
		// Function counters (EpsonFunctionTotalCount + ".<function>" for print/copy/scan)
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.6.1.4.1.1.1", Type: gosnmp.Integer, Value: 10000}, // Print total
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.6.1.4.1.1.2", Type: gosnmp.Integer, Value: 3000},  // Copy total
		{Name: "1.3.6.1.4.1.1248.1.2.2.27.6.1.4.1.1.4", Type: gosnmp.Integer, Value: 500},   // Scan count
	}

	pi := PrinterInfo{
		IP:           "10.0.0.5",
		Manufacturer: "Epson",
	}

	MergeVendorMetrics(&pi, pdus, "Epson")

	// Verify page counts were merged
	if pi.PageCount != 15000 {
		t.Errorf("expected PageCount 15000, got %d", pi.PageCount)
	}
	if pi.MonoImpressions != 8000 {
		t.Errorf("expected MonoImpressions 8000, got %d", pi.MonoImpressions)
	}
	if pi.ColorImpressions != 7000 {
		t.Errorf("expected ColorImpressions 7000, got %d", pi.ColorImpressions)
	}

	// Verify Meters map was populated
	if pi.Meters == nil {
		t.Fatalf("expected Meters map to be initialized")
	}
	if pi.Meters["total_pages"] != 15000 {
		t.Errorf("expected Meters[total_pages] 15000, got %d", pi.Meters["total_pages"])
	}
	if pi.Meters["mono_pages"] != 8000 {
		t.Errorf("expected Meters[mono_pages] 8000, got %d", pi.Meters["mono_pages"])
	}
	if pi.Meters["color_pages"] != 7000 {
		t.Errorf("expected Meters[color_pages] 7000, got %d", pi.Meters["color_pages"])
	}
}

func TestMergeVendorMetrics_KyoceraICEOIDs(t *testing.T) {
	t.Parallel()

	// Simulate Kyocera enterprise OIDs
	pdus := []gosnmp.SnmpPDU{
		// sysObjectID to trigger Kyocera vendor detection
		{Name: "1.3.6.1.2.1.1.2.0", Type: gosnmp.OctetString, Value: []byte("1.3.6.1.4.1.1347")},
		// Kyocera function counters (ICE-style)
		{Name: "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.1", Type: gosnmp.Integer, Value: 5000}, // Print B&W
		{Name: "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.2", Type: gosnmp.Integer, Value: 2000}, // Print Color
		{Name: "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.3", Type: gosnmp.Integer, Value: 1500}, // Copy B&W
		{Name: "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.4", Type: gosnmp.Integer, Value: 800},  // Copy Color
	}

	pi := PrinterInfo{
		IP:           "10.0.0.6",
		Manufacturer: "Kyocera",
	}

	MergeVendorMetrics(&pi, pdus, "Kyocera")

	// Verify Meters map was populated with function counters
	if pi.Meters == nil {
		t.Fatalf("expected Meters map to be initialized")
	}

	// Check that some metrics were extracted (specific values depend on Kyocera Parse implementation)
	if len(pi.Meters) == 0 {
		t.Errorf("expected some meters to be extracted from Kyocera OIDs")
	}
}

func TestMergeVendorMetrics_GenericVendorNoMerge(t *testing.T) {
	t.Parallel()

	// Generic vendor should not add any vendor-specific metrics
	pdus := []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.2.1.1.2.0", Type: gosnmp.OctetString, Value: []byte("1.3.6.1.4.1.9999")}, // Unknown vendor
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.Integer, Value: 1000},                 // Standard marker count
	}

	pi := PrinterInfo{
		IP:        "10.0.0.7",
		PageCount: 500, // Pre-existing value from ParsePDUs
	}

	MergeVendorMetrics(&pi, pdus, "")

	// PageCount should remain unchanged (generic vendor skips merge)
	if pi.PageCount != 500 {
		t.Errorf("expected PageCount to remain 500 for generic vendor, got %d", pi.PageCount)
	}
}

func TestParsePDUs_TonerModelNotUsedAsSerial(t *testing.T) {
	t.Parallel()

	// Simulate scenario where prtGeneralSerialNumber times out/fails
	// and only supply descriptions are available. The toner model number
	// (TK-3402S) should NOT be used as a serial number guess.
	vars := []gosnmp.SnmpPDU{
		// sysDescr indicating Kyocera
		{Name: "1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("KYOCERA Document Solutions Printing System")},
		// Supply descriptions (prtMarkerSuppliesDescription)
		{Name: "1.3.6.1.2.1.43.11.1.1.6.1.1", Type: gosnmp.OctetString, Value: []byte("TK-3402S")}, // Toner model - NOT a serial!
		{Name: "1.3.6.1.2.1.43.11.1.1.6.1.2", Type: gosnmp.OctetString, Value: []byte("Waste Toner Box")},
		// Supply levels to make it look like a printer
		{Name: "1.3.6.1.2.1.43.11.1.1.9.1.1", Type: gosnmp.Integer, Value: 5940},
		{Name: "1.3.6.1.2.1.43.11.1.1.8.1.1", Type: gosnmp.Integer, Value: 6000},
		// Page count
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.Integer, Value: 8},
	}

	pi, ok := ParsePDUs("172.52.105.138", vars, nil, nil)
	if !ok {
		t.Fatalf("expected device to be detected as printer due to supply descriptions")
	}

	// Serial should be empty - TK-3402S is a toner model number, not a device serial
	if pi.Serial != "" {
		t.Fatalf("expected serial to remain empty when only toner model numbers are present; got '%s'", pi.Serial)
	}

	// Consumables should include the toner descriptions
	foundToner := false
	for _, c := range pi.Consumables {
		if c == "TK-3402S" {
			foundToner = true
			break
		}
	}
	if !foundToner {
		t.Errorf("expected TK-3402S in consumables, got %v", pi.Consumables)
	}
}

func TestParsePDUs_OIDValueNotUsedAsSerial(t *testing.T) {
	t.Parallel()

	// Simulate scenario where sysObjectID value (.1.3.6.1.4.1.1347.41) might
	// incorrectly be picked up as a serial number guess
	vars := []gosnmp.SnmpPDU{
		{Name: "1.3.6.1.2.1.1.2.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.1347.41"},
		{Name: "1.3.6.1.2.1.43.10.2.1.4.1.1", Type: gosnmp.Integer, Value: 100},
	}

	pi, ok := ParsePDUs("10.0.0.8", vars, nil, nil)
	if !ok {
		t.Fatalf("expected device to be detected as printer due to marker count")
	}

	// Serial should be empty - OID strings are not serial numbers
	if pi.Serial != "" {
		t.Fatalf("expected serial to remain empty when only OID values present; got '%s'", pi.Serial)
	}
}
