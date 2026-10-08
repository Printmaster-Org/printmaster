package main

import (
	"context"
	"errors"
	"github.com/gosnmp/gosnmp"
	"net/netip"
	"path/filepath"
	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
	"printmaster/common/snmp/oids"
	"sync/atomic"
	"testing"
	"time"
)

func runtimeTestSetup(t *testing.T, serial string) (*scannerRuntime, *storage.SQLiteStore, *atomic.Int32) {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	backend := scanner.Backend{ProbeWithPorts: func(context.Context, netip.Addr, []uint16) ([]uint16, error) { return []uint16{9100}, nil }, Query: func(ctx context.Context, r scanner.QueryRequest) (*scanner.QueryResult, error) {
		calls.Add(1)
		return &scanner.QueryResult{IP: r.IP.String(), Profile: r.Profile, VendorHint: "HP", PDUs: []gosnmp.SnmpPDU{
			{Name: oids.PrtGeneralSerialNumber, Type: gosnmp.OctetString, Value: serial},
			{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer model"},
			{Name: oids.PrtMarkerLifeCount + ".1", Type: gosnmp.Counter32, Value: uint32(0)},
			{Name: oids.PrtMarkerSuppliesDesc + ".1.1", Type: gosnmp.OctetString, Value: "Black toner"},
			{Name: oids.PrtMarkerSuppliesLevel + ".1.1", Type: gosnmp.Integer, Value: 50},
			{Name: oids.PrtMarkerSuppliesMaxCap + ".1.1", Type: gosnmp.Integer, Value: 100},
		}}, ctx.Err()
	}}
	runtime, err := newScannerRuntime(context.Background(), store, backend)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	runtime.parsePDUs = func(ip string, pdus []gosnmp.SnmpPDU, meta *agent.ScanMeta, log func(string)) (agent.PrinterInfo, bool) {
		return agent.PrinterInfo{IP: ip, Manufacturer: "HP", Model: "Printer model", PageCount: 0}, true
	}
	previous := mainScanner
	mainScanner = runtime
	t.Cleanup(func() { runtime.Close(); mainScanner = previous; store.Close() })
	return runtime, store, calls
}

func TestScannerRuntimeQuickFullEffects(t *testing.T) {
	runtime, store, calls := runtimeTestSetup(t, "SERIAL-1")
	runtime.parsePDUs = func(ip string, _ []gosnmp.SnmpPDU, _ *agent.ScanMeta, _ func(string)) (agent.PrinterInfo, bool) {
		return agent.PrinterInfo{IP: ip, Manufacturer: "HP", Model: "Printer model", PageCount: 42}, true
	}
	ctx := context.Background()
	cfg := &agent.DiscoveryConfig{TCPEnabled: true, SNMPEnabled: true}
	ranges := []string{"192.0.2.1", "192.0.2.1"}
	out, err := Discover(ctx, ranges, "quick", cfg, store, 2, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("quick=%v err=%v", out, err)
	}
	if _, err := store.Get(ctx, "SERIAL-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("quick persisted: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate range queries=%d", calls.Load())
	}
	out, err = Discover(ctx, ranges, "full", cfg, store, 2, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("full=%v err=%v", out, err)
	}
	if _, err := store.Get(ctx, "SERIAL-1"); err != nil {
		t.Fatal(err)
	}
	scans, err := store.GetScanHistory(ctx, "SERIAL-1", 10)
	if err != nil || len(scans) != 1 {
		t.Fatalf("scan rows=%d err=%v", len(scans), err)
	}
	metrics, err := store.GetLatestMetrics(ctx, "SERIAL-1")
	if err != nil || metrics.PageCount != 42 {
		t.Fatalf("metrics=%v err=%v", metrics, err)
	}
}

func TestScannerRuntimeManualFullReadOnly(t *testing.T) {
	runtime, store, calls := runtimeTestSetup(t, "SERIAL-1")
	observation, err := scannerObservation("192.0.2.1", scanner.SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.request(context.Background(), observation, scanner.IntentManual,
		scanner.WorkOptions{FullDetail: true}, "SERIAL-1", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("manual full preview expected essential and detail queries, got %d", calls.Load())
	}
	if len(result.Queries) != 2 {
		t.Fatalf("manual full result has %d queries, want 2", len(result.Queries))
	}
	if _, err := store.Get(context.Background(), "SERIAL-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("read-only preview persisted scanner data: %v", err)
	}
}

func TestScannerIPScanningAllowedFailsClosedOnSettingsErrors(t *testing.T) {
	config, err := storage.NewAgentConfigStore(filepath.Join(t.TempDir(), "scanner-config.db"))
	if err != nil {
		t.Fatal(err)
	}
	previous := agentConfigStore
	agentConfigStore = config
	t.Cleanup(func() {
		agentConfigStore = previous
		if err := config.Close(); err != nil {
			t.Errorf("close config store: %v", err)
		}
	})
	if err := scannerIPScanningAllowed(); err != nil {
		t.Fatalf("missing setting should retain the default: %v", err)
	}
	if err := config.SetConfigValue("discovery_settings", map[string]interface{}{"ip_scanning_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err := scannerIPScanningAllowed(); !errors.Is(err, errIPScanningDisabled) {
		t.Fatalf("disabled setting error = %v, want %v", err, errIPScanningDisabled)
	}
	if err := config.SetConfigValue("discovery_settings", "invalid settings shape"); err != nil {
		t.Fatal(err)
	}
	if err := scannerIPScanningAllowed(); err == nil || errors.Is(err, errIPScanningDisabled) {
		t.Fatalf("invalid settings must fail closed with a read error, got %v", err)
	}
}

func TestScannerRuntimeReusedIPRecordsReplacementWithoutTouchingOld(t *testing.T) {
	runtime, store, _ := runtimeTestSetup(t, "NEW-SERIAL")
	ctx := context.Background()
	old := &storage.Device{Serial: "OLD-SERIAL", IP: "192.0.2.1", Manufacturer: "Original", IsSaved: true, Visible: false, LastSeen: time.Now().Add(-time.Hour)}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := runtime.refreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	obs, _ := scannerObservation(old.IP, scanner.SourceMDNS)
	if _, err := runtime.request(ctx, obs, scanner.IntentLive, scanner.WorkOptions{}, "", false, nil); err != nil {
		t.Fatalf("replacement printer at reused IP rejected: %v", err)
	}
	got, err := store.Get(ctx, old.Serial)
	if err != nil || got.Manufacturer != old.Manufacturer || !got.LastSeen.Equal(old.LastSeen) || got.Visible {
		t.Fatalf("old overwritten: %+v err=%v", got, err)
	}
	if replacement, err := store.Get(ctx, "NEW-SERIAL"); err != nil || replacement.IP != old.IP {
		t.Fatalf("replacement not recorded: %+v %v", replacement, err)
	}
	// A caller's explicit expected serial must still refuse a different printer.
	if _, err := runtime.request(ctx, obs, scanner.IntentManual, scanner.WorkOptions{}, old.Serial, false, nil); err == nil {
		t.Fatal("explicit expected serial accepted a different printer")
	}
}

func TestScannerRuntimeValidatedAddressMove(t *testing.T) {
	runtime, store, _ := runtimeTestSetup(t, "SERIAL-1")
	ctx := context.Background()
	old := &storage.Device{Serial: "SERIAL-1", IP: "192.0.2.1", Location: "room", IsSaved: true, Visible: true, LastSeen: time.Now().Add(-time.Hour)}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := runtime.refreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	obs, _ := scannerObservation("192.0.2.9", scanner.SourceMDNS)
	if _, err := runtime.request(ctx, obs, scanner.IntentLive, scanner.WorkOptions{}, "", false, nil); err != nil {
		t.Fatalf("validated move rejected: %v", err)
	}
	got, err := store.Get(ctx, old.Serial)
	if err != nil || got.IP != "192.0.2.9" || got.Location != "room" || !got.IsSaved || !got.LastSeen.After(old.LastSeen) {
		t.Fatalf("move not applied safely: %+v %v", got, err)
	}
	runtime.mu.RLock()
	stale, moved := runtime.known[netip.MustParseAddr("192.0.2.1")], runtime.known[netip.MustParseAddr("192.0.2.9")]
	runtime.mu.RUnlock()
	if stale != nil || moved == nil || moved.Serial != old.Serial {
		t.Fatalf("index not refreshed after move: stale=%v moved=%v", stale, moved)
	}
}

func TestScannerRuntimeStaleLearnedOIDRetriesWithoutHints(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "hint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	const learned = "1.3.6.1.4.1.9999.1"
	old := &storage.Device{Serial: "OLD", IP: "192.0.2.1", RawData: map[string]interface{}{"learned_oids": map[string]interface{}{"serial_oid": learned}}}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	var hinted, plain atomic.Int32
	backend := scanner.Backend{ProbeWithPorts: func(context.Context, netip.Addr, []uint16) ([]uint16, error) { return []uint16{9100}, nil },
		Query: func(_ context.Context, r scanner.QueryRequest) (*scanner.QueryResult, error) {
			pdus := []gosnmp.SnmpPDU{{Name: oids.PrtGeneralSerialNumber, Type: gosnmp.OctetString, Value: "NEW"}}
			if r.LearnedSerialOID == learned {
				hinted.Add(1)
				// The new occupant answers the old printer's learned OID with other data.
				pdus = append(pdus, gosnmp.SnmpPDU{Name: learned, Type: gosnmp.OctetString, Value: "unrelated"})
			} else {
				plain.Add(1)
			}
			return &scanner.QueryResult{IP: r.IP.String(), Profile: r.Profile, PDUs: pdus}, nil
		}}
	runtime, err := newScannerRuntime(ctx, store, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	obs, _ := scannerObservation(old.IP, scanner.SourceManual)
	result, err := runtime.request(ctx, obs, scanner.IntentQuick, scanner.WorkOptions{}, "", true, nil)
	if err != nil || result.Serial != "NEW" || hinted.Load() == 0 || plain.Load() == 0 {
		t.Fatalf("stale hint not retried: serial=%q err=%v hinted=%d plain=%d", result.Serial, err, hinted.Load(), plain.Load())
	}
}

func TestScannerAgentMetricsMapping(t *testing.T) {
	t.Parallel()
	pi := agent.PrinterInfo{Serial: "S", PageCount: 10, IsMono: true, TonerLevelBlack: 40, TonerLevelCyan: 0, TonerDescCyan: "cyan",
		Meters: map[string]int{"total_pages": 120, "mono_pages": 0, "copy_pages": 7, "fax_pages": 3, "scans": 5}}
	got := scannerAgentMetrics(pi)
	if got.PageCount != 120 || got.MonoPages != 0 || got.CopyPages != 7 || got.FaxPages != 3 || got.ScanCount != 5 {
		t.Fatalf("counters: %+v", got)
	}
	if len(got.TonerLevels) != 1 || got.TonerLevels["black"] != 40 {
		t.Fatalf("mono toner: %+v", got.TonerLevels)
	}
	at := time.Now()
	s := scannerStorageMetrics(got, at)
	if s.Serial != "S" || !s.Timestamp.Equal(at) || s.PageCount != 120 || s.CopyPages != 7 || s.ScanCount != 5 {
		t.Fatalf("storage conversion: %+v", s)
	}
}

func TestScannerRuntimeEnrichmentStoresNormalizedFacts(t *testing.T) {
	runtime, store, _ := runtimeTestSetup(t, "SERIAL-1")
	ctx := context.Background()
	runtime.parsePDUs = func(ip string, _ []gosnmp.SnmpPDU, _ *agent.ScanMeta, _ func(string)) (agent.PrinterInfo, bool) {
		return agent.PrinterInfo{IP: ip, Model: "Printer model", UptimeSeconds: 99, Meters: map[string]int{"total_pages": 5}}, true
	}
	obs := scanner.Observation{IP: netip.MustParseAddr("192.0.2.1"), Source: scanner.SourceLLMNR,
		Hints: scanner.ProtocolHints{LLMNR: scanner.LLMNRHint{Hostname: "office-printer", Message: "query"}}}
	if _, err := runtime.request(ctx, obs, scanner.IntentLive, scanner.WorkOptions{}, "", false, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "SERIAL-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence, _ := got.RawData["discovery_evidence"].(map[string]interface{})
	if got.Hostname != "office-printer" || got.RawData["uptime_seconds"] != float64(99) || evidence["source"] != "llmnr" || got.RawData["scanner_pdus"] != nil {
		t.Fatalf("normalized facts not stored: hostname=%q raw=%v", got.Hostname, got.RawData)
	}
}

func TestScannerRuntimeIdentityOnlyPartialCommit(t *testing.T) {
	runtime, store, _ := runtimeTestSetup(t, "SERIAL-1")
	ctx := context.Background()
	old := &storage.Device{Serial: "SERIAL-1", IP: "192.0.2.1", Firmware: "KEEP", IsSaved: true, Visible: false, LastSeen: time.Now().Add(-time.Hour), RawData: map[string]interface{}{"custom": "keep"}}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := runtime.refreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	obs, _ := scannerObservation(old.IP, scanner.SourceManual)
	result, err := runtime.request(ctx, obs, scanner.IntentManual, scanner.WorkOptions{}, old.Serial, false, nil)
	if err != nil || !result.Committed {
		t.Fatalf("commit=%v err=%v", result.Committed, err)
	}
	got, err := store.Get(ctx, old.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firmware != "KEEP" || !got.IsSaved || got.Visible || got.RawData["custom"] != "keep" {
		t.Fatalf("partial erased user state: %+v", got)
	}
	scans, err := store.GetScanHistory(ctx, old.Serial, 10)
	if err != nil || len(scans) != 0 {
		t.Fatalf("identity synthesized scan: %v %v", scans, err)
	}
	if _, err := store.GetLatestMetrics(ctx, old.Serial); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("identity synthesized metrics: %v", err)
	}
}

func TestScannerRuntimeMetricsIdentityAndAddressGuard(t *testing.T) {
	runtime, store, _ := runtimeTestSetup(t, "SERIAL-1")
	ctx := context.Background()
	old := &storage.Device{Serial: "SERIAL-1", IP: "192.0.2.1", Visible: true, LastSeen: time.Now()}
	if err := store.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := runtime.refreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectMetrics(ctx, old.IP, "WRONG-SERIAL", "", 1); err == nil {
		t.Fatal("metrics attributed to requested wrong serial")
	}
	snapshot, err := CollectMetrics(ctx, old.IP, old.Serial, "", 1)
	if err != nil || snapshot.Serial != old.Serial {
		t.Fatalf("metrics=%v err=%v", snapshot, err)
	}
	if _, err := store.GetLatestMetrics(ctx, old.Serial); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("read-only metrics persisted: %v", err)
	}
	moved := *old
	moved.IP = "192.0.2.2"
	if err := store.Update(ctx, &moved); err != nil {
		t.Fatal(err)
	}
	err = saveScannerMetrics(ctx, old.IP, &storage.MetricsSnapshot{Serial: old.Serial, Timestamp: time.Now()})
	if !errors.Is(err, storage.ErrExpectedIPMismatch) {
		t.Fatalf("racing move accepted: %v", err)
	}
}

func TestScannerRuntimeTCPOnlyDoesNotQuery(t *testing.T) {
	_, store, calls := runtimeTestSetup(t, "SERIAL-1")
	out, err := Discover(context.Background(), []string{"192.0.2.1"}, "quick", &agent.DiscoveryConfig{TCPEnabled: true}, store, 1, 1)
	if err != nil || len(out) != 1 || out[0].Serial != "" || len(out[0].OpenPorts) != 1 || calls.Load() != 0 {
		t.Fatalf("tcp-only=%+v queries=%d err=%v", out, calls.Load(), err)
	}
}

func TestScannerRuntimeCloseCancelsSubscriber(t *testing.T) {
	runtime, _, _ := runtimeTestSetup(t, "SERIAL-1")
	started := make(chan struct{})
	runtime.coordinator.Close()
	c, err := scanner.NewCoordinator(scanner.CoordinatorConfig{Backend: scanner.Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, Query: func(context.Context, scanner.QueryRequest) (*scanner.QueryResult, error) {
		return nil, errors.New("unexpected query")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.coordinator = c
	done := make(chan error, 1)
	go func() {
		obs, _ := scannerObservation("192.0.2.1", scanner.SourceManual)
		_, err := runtime.request(context.Background(), obs, scanner.IntentQuick, scanner.WorkOptions{}, "", true, nil)
		done <- err
	}()
	<-started
	runtime.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("shutdown succeeded unexpectedly")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber survived shutdown")
	}
}
