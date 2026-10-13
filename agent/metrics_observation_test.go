package main

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

func TestMetricsSaveRefreshesObservedStatusAndVendorFacts(t *testing.T) {
	_, store, _ := runtimeTestSetup(t, "SERIAL-1")
	ctx := context.Background()
	old := &storage.Device{Serial: "SERIAL-1", IP: "192.0.2.1", Manufacturer: "Epson", IsSaved: true, Visible: true,
		AssetNumber: "05046", Location: "Office", StatusMessages: []string{"Paper jam"},
		RawData: map[string]interface{}{"is_inkjet": true, "is_color": true, "learned_oids": map[string]interface{}{"serial_oid": "1.2.3"},
			"vendor_metrics": map[string]interface{}{"epson_error": "Paper jam", "epson_error_code": "4", "epson_warnings": "Ink low", "lifetime_detail": 456}, "toner_alerts": []string{"Ink low"}}}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	pi := agent.PrinterInfo{Serial: old.Serial, IP: old.IP, LastSeen: at, PageCount: 91845,
		StatusObserved: true, StatusMessages: []string{"Ready: Idle"},
		TonerLevels:   map[string]int{"Black Ink Cartridge T08M1": 33, "Photo Black Ink T44H1": 51, "supply_maintenance_box": 75, "supply_maintenance_box_2": 62},
		VendorMetrics: map[string]interface{}{"epson_status": "Idle", "epson_ready": true, "power_on_count": 12},
		Meters:        map[string]int{"copy_pages": 456}}
	snapshot := scannerStorageMetrics(scannerAgentMetrics(pi), at.Add(time.Minute))
	if !snapshot.Timestamp.Equal(at) || snapshot.TonerLevels["supply_maintenance_box"] != 75 ||
		snapshot.TonerLevels["supply_maintenance_box_2"] != 62 || snapshot.TonerLevels["Black Ink Cartridge T08M1"] != 33 ||
		snapshot.TonerLevels["Photo Black Ink T44H1"] != 51 {
		t.Fatalf("metrics lost observed sample/supplies: %+v", snapshot)
	}
	if err := saveScannerMetrics(ctx, old.IP, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, old.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.StatusMessages, pi.StatusMessages) || got.RawData["is_inkjet"] != true ||
		got.RawData["is_color"] != true || got.Location != old.Location || got.AssetNumber != old.AssetNumber {
		t.Fatalf("metrics state update damaged inventory: %+v", got)
	}
	vendor := got.RawData["vendor_metrics"].(map[string]interface{})
	if vendor["epson_error"] != nil || vendor["epson_error_code"] != nil || vendor["epson_warnings"] != nil ||
		vendor["epson_status"] != "Idle" || vendor["lifetime_detail"] != float64(456) {
		t.Fatalf("resolved error persisted: %v", vendor)
	}
	pi.LastSeen = at.Add(time.Second)
	pi.StatusMessages = []string{}
	if err := saveScannerMetrics(ctx, old.IP, scannerStorageMetrics(scannerAgentMetrics(pi), pi.LastSeen)); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(ctx, old.Serial)
	if len(got.StatusMessages) != 0 {
		t.Fatalf("observed empty status did not clear: %v", got.StatusMessages)
	}
	pi.LastSeen = at.Add(2 * time.Second)
	pi.StatusMessages, pi.StatusObserved = []string{"Paper jam"}, true
	if err := saveScannerMetrics(ctx, old.IP, scannerStorageMetrics(scannerAgentMetrics(pi), pi.LastSeen)); err != nil {
		t.Fatal(err)
	}
	pi.LastSeen = at.Add(3 * time.Second)
	pi.StatusMessages, pi.StatusObserved, pi.VendorMetrics = nil, false, nil
	if err := saveScannerMetrics(ctx, old.IP, scannerStorageMetrics(scannerAgentMetrics(pi), pi.LastSeen)); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(ctx, old.Serial)
	if !reflect.DeepEqual(got.StatusMessages, []string{"Paper jam"}) {
		t.Fatalf("missing status erased alert: %v", got.StatusMessages)
	}
	if got.RawData["vendor_metrics"].(map[string]interface{})["epson_status"] != "Idle" {
		t.Fatalf("missing status erased vendor health: %v", got.RawData)
	}
	pi.LastSeen = at.Add(4 * time.Second)
	pi.PageCount, pi.Meters = 0, nil
	pi.StatusObserved, pi.StatusMessages = true, []string{"Printer offline"}
	pi.VendorMetrics = map[string]interface{}{"epson_status": "Error", "epson_error": "Printer offline"}
	if err := saveScannerMetrics(ctx, old.IP, scannerStorageMetrics(scannerAgentMetrics(pi), pi.LastSeen)); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, old.Serial)
	if err != nil || !reflect.DeepEqual(got.StatusMessages, pi.StatusMessages) {
		t.Fatalf("dropped counter sample prevented status refresh: %+v, %v", got, err)
	}
	latest, err := store.GetLatestMetrics(ctx, old.Serial)
	if err != nil || latest.PageCount != 91845 {
		t.Fatalf("invalid counters replaced history: %+v, %v", latest, err)
	}
}

func TestMetricsConversionRecognizesOnlyObtainedEpsonStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		value      interface{}
		queryError error
		want       bool
		oid        string
		pduType    gosnmp.Asn1BER
	}{
		{"ready", []byte{0, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n', 3, 0, 1, 1, 4}, nil, true, oids.EpsonDeviceStatus, gosnmp.OctetString},
		{"malformed frame", []byte("not an ST2 frame"), nil, false, oids.EpsonDeviceStatus, gosnmp.OctetString},
		{"failed query", []byte{0, '@', 'B', 'D', 'C', ' ', 'S', 'T', '2', '\r', '\n', 3, 0, 1, 1, 4}, errors.New("SNMP timeout"), false, oids.EpsonDeviceStatus, gosnmp.OctetString},
		{"zero standard errors", []byte{0}, nil, true, oids.HrPrinterDetectedErrorState + ".1", gosnmp.OctetString},
		{"untranslated standard errors", []byte{4}, nil, false, oids.HrPrinterDetectedErrorState + ".1", gosnmp.OctetString},
		{"empty standard errors", []byte{}, nil, false, oids.HrPrinterDetectedErrorState + ".1", gosnmp.OctetString},
		{"invalid standard error type", 0, nil, false, oids.HrPrinterDetectedErrorState + ".1", gosnmp.Integer},
		{"printer state alone", 3, nil, false, oids.HrPrinterStatus + ".1", gosnmp.Integer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := scanner.Result{Serial: "EPSON-1", Observation: scanner.Observation{IP: netip.MustParseAddr("192.0.2.91")},
				Intent: scanner.Intent{MetricsRequested: true}}
			result.Outcomes[scanner.StageIdentity].Status = scanner.StatusSucceeded
			result.Outcomes[scanner.StageMetrics].Status = scanner.StatusSucceeded
			result.Queries = []scanner.ObtainedQuery{
				{Stage: scanner.StageIdentity, ReceivedAt: time.Now(), Result: &scanner.QueryResult{Profile: scanner.QueryMinimal}},
				{Stage: scanner.StageMetrics, ReceivedAt: time.Now(), Err: tc.queryError, Result: &scanner.QueryResult{
					Profile: scanner.QueryMetrics, VendorHint: "Epson",
					PDUs: []gosnmp.SnmpPDU{{Name: tc.oid, Type: tc.pduType, Value: tc.value}},
				}},
			}
			pi, err := convertScannerResult(result, func(string, []gosnmp.SnmpPDU, *agent.ScanMeta, func(string)) (agent.PrinterInfo, bool) {
				return agent.PrinterInfo{}, true
			})
			if err != nil {
				t.Fatal(err)
			}
			if pi.StatusObserved != tc.want {
				t.Fatalf("observed status = %v, want %v", pi.StatusObserved, tc.want)
			}
			if tc.want && tc.oid == oids.EpsonDeviceStatus && (len(pi.StatusMessages) != 1 || pi.VendorMetrics["epson_ready"] != true) {
				t.Fatalf("decoded state lost: %+v", pi)
			}
		})
	}
}
