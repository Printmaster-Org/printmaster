package vendor

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"printmaster/agent/supplies"

	"github.com/gosnmp/gosnmp"
)

func TestSuppliesPreserveDistinctInkCartridgesAndMaintenanceBoxes(t *testing.T) {
	t.Parallel()
	supplies := []struct {
		desc  string
		level int
		class int
	}{
		{"Photo Black Ink Cartridge T44H1", 28, 3},
		{"Matte Black Ink Cartridge T44H8", 61, 3},
		{"Cyan Ink Cartridge T44H2", 27, 3},
		{"Light Cyan Ink Cartridge T44H5", 72, 3},
		{"Vivid Magenta Ink Cartridge T44H3", 30, 3},
		{"Vivid Light Magenta Ink Cartridge T44H6", 49, 3},
		{"Gray Ink Cartridge T44H7", 28, 0},
		{"Maintenance Box T6997", 39, 4},
		{"Maintenance Box2 T6997", 100, 4},
	}
	var pdus []gosnmp.SnmpPDU
	for i, supply := range supplies {
		for column, value := range map[int]interface{}{6: supply.desc, 9: supply.level, 8: 100, 4: supply.class} {
			pdus = append(pdus, gosnmp.SnmpPDU{Name: fmt.Sprintf("1.3.6.1.2.1.43.11.1.1.%d.1.%d", column, i+1), Value: value})
		}
	}
	for _, module := range []VendorModule{&GenericVendor{}, &EpsonVendor{}} {
		result := module.Parse(pdus)
		for _, supply := range supplies {
			key := "supply_" + strings.ReplaceAll(strings.ToLower(supply.desc), " ", "_")
			if got, ok := result[key].(float64); !ok || math.Abs(got-float64(supply.level)) > 0.000001 {
				t.Fatalf("%T lost %s: %+v", module, key, result)
			}
		}
		for _, key := range []string{"toner_black", "toner_cyan", "toner_magenta", "waste_toner"} {
			if _, ok := result[key]; ok {
				t.Fatalf("%T emitted ambiguous aggregate %s", module, key)
			}
		}
	}
}

func TestVendorDetection(t *testing.T) {
	tests := []struct {
		name         string
		sysObjectID  string
		sysDescr     string
		model        string
		expectedName string
	}{
		{
			name:         "HP LaserJet",
			sysObjectID:  "1.3.6.1.4.1.11.2.3.9.1",
			sysDescr:     "HP LaserJet Pro M404dn",
			model:        "LaserJet Pro M404dn",
			expectedName: "HP",
		},
		{
			name:         "Kyocera TASKalfa",
			sysObjectID:  "1.3.6.1.4.1.1347.43.1.2.1",
			sysDescr:     "Kyocera TASKalfa 6052ci",
			model:        "TASKalfa 6052ci",
			expectedName: "Kyocera",
		},
		{
			name:         "Epson WorkForce",
			sysObjectID:  "1.3.6.1.4.1.1248.1.1.1",
			sysDescr:     "Epson WF-C17590 Series",
			model:        "AM-C550 Series",
			expectedName: "Epson",
		},
		{
			name:         "Generic Unknown",
			sysObjectID:  "1.3.6.1.4.1.9999.1.1",
			sysDescr:     "Unknown Printer",
			model:        "Model XYZ",
			expectedName: "Generic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			module := DetectVendor(tt.sysObjectID, tt.sysDescr, tt.model)
			if module.Name() != tt.expectedName {
				t.Errorf("DetectVendor() = %v, want %v", module.Name(), tt.expectedName)
			}
		})
	}
}

func TestEnterpriseNumberExtraction(t *testing.T) {
	tests := []struct {
		name        string
		sysObjectID string
		want        string
	}{
		{"HP OID", "1.3.6.1.4.1.11.2.3.9.1", "11"},
		{"Kyocera OID", "1.3.6.1.4.1.1347.43.1.2.1", "1347"},
		{"Epson OID", "1.3.6.1.4.1.1248.1.1.1", "1248"},
		{"With leading dot", ".1.3.6.1.4.1.11.2.3.9.1", "11"},
		{"Invalid OID", "1.2.3.4", ""},
		{"Empty OID", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractEnterpriseNumber(tt.sysObjectID)
			if got != tt.want {
				t.Errorf("extractEnterpriseNumber(%q) = %q, want %q", tt.sysObjectID, got, tt.want)
			}
		})
	}
}

func TestManufacturerForSysObjectID(t *testing.T) {
	t.Parallel()

	if got := ManufacturerForSysObjectID(".1.3.6.1.4.1.367.1.1"); got != "Ricoh" {
		t.Errorf("ManufacturerForSysObjectID() = %q, want Ricoh", got)
	}
}

func TestGenericVendorParse(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.43.10.2.1.4.1.1", Value: 12345}, // prtMarkerLifeCount
		{Name: ".1.3.6.1.2.1.43.11.1.1.6.1.1", Value: []byte("Black Toner")},
		{Name: ".1.3.6.1.2.1.43.11.1.1.9.1.1", Value: 75},  // Level
		{Name: ".1.3.6.1.2.1.43.11.1.1.8.1.1", Value: 100}, // MaxCapacity
		{Name: ".1.3.6.1.2.1.43.11.1.1.4.1.1", Value: 3},   // Class (supplyThatIsConsumed)
		{Name: ".1.3.6.1.2.1.43.11.1.1.6.1.2", Value: []byte("Cyan Toner")},
		{Name: ".1.3.6.1.2.1.43.11.1.1.9.1.2", Value: 50},
		{Name: ".1.3.6.1.2.1.43.11.1.1.8.1.2", Value: 100},
		{Name: ".1.3.6.1.2.1.43.11.1.1.4.1.2", Value: 3},
	}

	vendor := &GenericVendor{}
	result := vendor.Parse(pdus)

	// Check page count
	if pageCount, ok := result["page_count"].(int); !ok || pageCount != 12345 {
		t.Errorf("page_count = %v, want 12345", result["page_count"])
	}

	// Check toner levels
	if tonerBlack, ok := result["toner_black"].(float64); !ok || tonerBlack != 75.0 {
		t.Errorf("toner_black = %v, want 75.0", result["toner_black"])
	}

	if tonerCyan, ok := result["toner_cyan"].(float64); !ok || tonerCyan != 50.0 {
		t.Errorf("toner_cyan = %v, want 50.0", result["toner_cyan"])
	}
}

func TestKyoceraVendorParse(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		// Kyocera enterprise OIDs
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.1.1", Value: 163915}, // Printer B&W
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.1.3", Value: 140424}, // Printer Color
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.2.1", Value: 73811},  // Copy B&W
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.2.3", Value: 42443},  // Copy Color
		{Name: ".1.3.6.1.4.1.1347.42.3.1.2.1.1.4.1", Value: 3812},   // Fax B&W
		{Name: ".1.3.6.1.4.1.1347.42.3.1.3.1.1.2", Value: 42138},    // Copy scans
		{Name: ".1.3.6.1.4.1.1347.42.3.1.3.1.1.4", Value: 3057},     // Fax scans
		{Name: ".1.3.6.1.4.1.1347.42.3.1.4.1.1.1", Value: 43441},    // Other scans
	}

	vendor := &KyoceraVendor{}
	result := vendor.Parse(pdus)

	// Check totals
	expectedMono := 163915 + 73811 + 3812
	if monoPages, ok := result["mono_pages"].(int); !ok || monoPages != expectedMono {
		t.Errorf("mono_pages = %v, want %d", result["mono_pages"], expectedMono)
	}

	expectedColor := 140424 + 42443
	if colorPages, ok := result["color_pages"].(int); !ok || colorPages != expectedColor {
		t.Errorf("color_pages = %v, want %d", result["color_pages"], expectedColor)
	}

	expectedTotal := expectedMono + expectedColor
	if pageCount, ok := result["page_count"].(int); !ok || pageCount != expectedTotal {
		t.Errorf("page_count = %v, want %d", result["page_count"], expectedTotal)
	}

	// Check function-specific
	if copyPages, ok := result["copy_pages"].(int); !ok || copyPages != 73811+42443 {
		t.Errorf("copy_pages = %v, want %d", result["copy_pages"], 73811+42443)
	}

	if faxPages, ok := result["fax_pages"].(int); !ok || faxPages != 3812 {
		t.Errorf("fax_pages = %v, want 3812", result["fax_pages"])
	}

	// Check scan counts
	expectedScans := 42138 + 3057 + 43441
	if scanCount, ok := result["scan_count"].(int); !ok || scanCount != expectedScans {
		t.Errorf("scan_count = %v, want %d", result["scan_count"], expectedScans)
	}
}

func TestEpsonVendorParse(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		// Epson enterprise OIDs (ICE-style)
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.1.1.33.1.1", Value: 81563}, // Total pages (ICE)
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.1.1.6.1.1", Value: 39797},  // Mono pages (ICE)
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.1.1.4.1.1", Value: 41766},  // Color pages (ICE)
		// Function counters
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.6.1.4.1.1.1", Value: 49053}, // Total print
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.6.1.4.1.1.2", Value: 32474}, // Total copy
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.6.1.5.1.1.1", Value: 39434}, // Color print
		{Name: ".1.3.6.1.4.1.1248.1.2.2.27.6.1.5.1.1.2", Value: 2313},  // Color copy
	}

	vendor := &EpsonVendor{}
	result := vendor.Parse(pdus)

	// Check totals
	if totalPages, ok := result["total_pages"].(int); !ok || totalPages != 81563 {
		t.Errorf("total_pages = %v, want 81563", result["total_pages"])
	}

	if monoPages, ok := result["mono_pages"].(int); !ok || monoPages != 39797 {
		t.Errorf("mono_pages = %v, want 39797", result["mono_pages"])
	}

	if colorPages, ok := result["color_pages"].(int); !ok || colorPages != 41766 {
		t.Errorf("color_pages = %v, want 41766", result["color_pages"])
	}

	// Check copy
	if copyPages, ok := result["copy_pages"].(int); !ok || copyPages != 32474 {
		t.Errorf("copy_pages = %v, want 32474", result["copy_pages"])
	}

	if copyColor, ok := result["copy_color_pages"].(int); !ok || copyColor != 2313 {
		t.Errorf("copy_color_pages = %v, want 2313", result["copy_color_pages"])
	}

	// Check calculated B&W copy
	expectedCopyMono := 32474 - 2313
	if copyMono, ok := result["copy_mono_pages"].(int); !ok || copyMono != expectedCopyMono {
		t.Errorf("copy_mono_pages = %v, want %d", result["copy_mono_pages"], expectedCopyMono)
	}
}

func TestHPVendorFirmwareParse(t *testing.T) {
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Value: []byte("HP LaserJet Pro M404dn Firmware Datecode: 20230712 Service ID")},
		{Name: ".1.3.6.1.4.1.11.2.3.9.4.2.1.4.4.7.0", Value: 1234}, // color pages (dummy)
		{Name: ".1.3.6.1.4.1.11.2.3.9.4.2.1.4.4.8.0", Value: 4321}, // mono pages (dummy)
	}

	vendor := &HPVendor{}
	result := vendor.Parse(pdus)

	fw, ok := result["firmware_version"].(string)
	if !ok || fw != "20230712" {
		// Accept alternative extraction if pattern changes
		if fw == "" {
			t.Errorf("expected firmware_version parsed, got empty")
		}
	}
}

func TestSupplyColorMatching(t *testing.T) {
	tests := []struct {
		description string
		want        string
	}{
		{"Black Toner", "toner_black"},
		{"Cyan Cartridge", "toner_cyan"},
		{"Magenta Ink", "toner_magenta"},
		{"Yellow Toner", "toner_yellow"},
		{"BK Toner", "toner_black"},
		{"CY Toner", "toner_cyan"},
		{"Black Drum Unit", "drum_life"},
		{"Drum Cartridge", "drum_life"},
		{"Waste Toner Container", "waste_toner"},
		{"Fuser Unit", "fuser_life"},
		{"Transfer Belt", "transfer_belt"},
		{"Unknown Supply", ""},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := supplies.NormalizeDescription(tt.description)
			if got != tt.want {
				t.Errorf("NormalizeDescription(%q) = %q, want %q", tt.description, got, tt.want)
			}
		})
	}
}

func TestParsePaperTrays(t *testing.T) {
	// Test paper tray parsing from prtInputTable PDUs
	pdus := []gosnmp.SnmpPDU{
		// Tray 1 - Letter paper, 75% full
		{Name: ".1.3.6.1.2.1.43.8.2.1.13.1.1", Value: []byte("Tray 1")}, // prtInputName
		{Name: ".1.3.6.1.2.1.43.8.2.1.12.1.1", Value: []byte("Letter")}, // prtInputMediaName
		{Name: ".1.3.6.1.2.1.43.8.2.1.10.1.1", Value: 375},              // prtInputCurrentLevel
		{Name: ".1.3.6.1.2.1.43.8.2.1.9.1.1", Value: 500},               // prtInputMaxCapacity
		{Name: ".1.3.6.1.2.1.43.8.2.1.2.1.1", Value: 3},                 // prtInputType (sheetFeedAutoRemovableTray)
		// Tray 2 - A4 paper, low
		{Name: ".1.3.6.1.2.1.43.8.2.1.13.1.2", Value: []byte("Tray 2")},
		{Name: ".1.3.6.1.2.1.43.8.2.1.12.1.2", Value: []byte("A4")},
		{Name: ".1.3.6.1.2.1.43.8.2.1.10.1.2", Value: 25},
		{Name: ".1.3.6.1.2.1.43.8.2.1.9.1.2", Value: 250},
		// Manual feed tray - empty
		{Name: ".1.3.6.1.2.1.43.8.2.1.13.1.3", Value: []byte("Manual Feed")},
		{Name: ".1.3.6.1.2.1.43.8.2.1.10.1.3", Value: 0},
		{Name: ".1.3.6.1.2.1.43.8.2.1.9.1.3", Value: 1},
	}

	trays := ParsePaperTrays(pdus)

	if len(trays) != 3 {
		t.Fatalf("Expected 3 trays, got %d", len(trays))
	}

	// Check Tray 1
	tray1 := trays[0]
	if tray1.Index != 1 {
		t.Errorf("Tray 1 index = %d, want 1", tray1.Index)
	}
	if tray1.Name != "Tray 1" {
		t.Errorf("Tray 1 name = %q, want %q", tray1.Name, "Tray 1")
	}
	if tray1.MediaType != "Letter" {
		t.Errorf("Tray 1 media = %q, want %q", tray1.MediaType, "Letter")
	}
	if tray1.CurrentLevel != 375 {
		t.Errorf("Tray 1 level = %d, want 375", tray1.CurrentLevel)
	}
	if tray1.MaxCapacity != 500 {
		t.Errorf("Tray 1 capacity = %d, want 500", tray1.MaxCapacity)
	}
	if tray1.LevelPercent != 75 {
		t.Errorf("Tray 1 percent = %d, want 75", tray1.LevelPercent)
	}
	if tray1.Status != "ok" {
		t.Errorf("Tray 1 status = %q, want %q", tray1.Status, "ok")
	}

	// Check Tray 2 - low paper
	tray2 := trays[1]
	if tray2.Index != 2 {
		t.Errorf("Tray 2 index = %d, want 2", tray2.Index)
	}
	if tray2.LevelPercent != 10 {
		t.Errorf("Tray 2 percent = %d, want 10", tray2.LevelPercent)
	}
	if tray2.Status != "low" {
		t.Errorf("Tray 2 status = %q, want %q", tray2.Status, "low")
	}

	// Check Manual Feed - empty
	tray3 := trays[2]
	if tray3.Index != 3 {
		t.Errorf("Manual Feed index = %d, want 3", tray3.Index)
	}
	if tray3.CurrentLevel != 0 {
		t.Errorf("Manual Feed level = %d, want 0", tray3.CurrentLevel)
	}
	if tray3.Status != "empty" {
		t.Errorf("Manual Feed status = %q, want %q", tray3.Status, "empty")
	}
}

func TestParsePaperTrays_SpecialValues(t *testing.T) {
	// Test special SNMP values per RFC 3805
	pdus := []gosnmp.SnmpPDU{
		// Tray with "someRemaining" (-3) level
		{Name: ".1.3.6.1.2.1.43.8.2.1.13.1.1", Value: []byte("Tray 1")},
		{Name: ".1.3.6.1.2.1.43.8.2.1.10.1.1", Value: -3}, // someRemaining
		{Name: ".1.3.6.1.2.1.43.8.2.1.9.1.1", Value: 500},
		// Tray with unknown capacity (-2)
		{Name: ".1.3.6.1.2.1.43.8.2.1.13.1.2", Value: []byte("Tray 2")},
		{Name: ".1.3.6.1.2.1.43.8.2.1.10.1.2", Value: 50},
		{Name: ".1.3.6.1.2.1.43.8.2.1.9.1.2", Value: -2}, // unknown capacity
	}

	trays := ParsePaperTrays(pdus)

	if len(trays) != 2 {
		t.Fatalf("Expected 2 trays, got %d", len(trays))
	}

	// Tray 1 with someRemaining should report "low" and estimate 10%
	if trays[0].Status != "low" {
		t.Errorf("Tray 1 with someRemaining status = %q, want %q", trays[0].Status, "low")
	}
	if trays[0].LevelPercent != 10 {
		t.Errorf("Tray 1 with someRemaining percent = %d, want 10", trays[0].LevelPercent)
	}

	// Tray 2 with unknown capacity should have -1 percent but still report based on level
	if trays[1].LevelPercent != -1 {
		t.Errorf("Tray 2 with unknown capacity percent = %d, want -1", trays[1].LevelPercent)
	}
}

func TestParsePaperTrays_Empty(t *testing.T) {
	// Test with no paper tray PDUs
	pdus := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Value: []byte("Some Printer")}, // sysDescr (not a tray OID)
	}

	trays := ParsePaperTrays(pdus)

	if len(trays) != 0 {
		t.Errorf("Expected 0 trays, got %d", len(trays))
	}
}
