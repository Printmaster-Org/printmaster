package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func stagePointer[T any](v T) *T { return &v }

func stageTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "stage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func stageAssertRows(t *testing.T, s *SQLiteStore, table string, want int) {
	t.Helper()
	var got int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil || got != want {
		t.Fatalf("%s rows=%d want=%d err=%v", table, got, want, err)
	}
}

func TestCommitScannerFactsPartialPreservesUserState(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newFullTestDevice("SERIAL-1", "192.0.2.1", "old maker", "user model", true, false)
	d.Firmware = "old firmware"
	d.Hostname = "keep hostname"
	d.Location, d.Description, d.AssetNumber = "user room", "user note", "asset"
	d.DeviceType, d.SourceType, d.IsUSB = "usb", "spooler", true
	d.PortName, d.DriverName, d.SpoolerStatus = "USB001", "driver", "ready"
	d.IsDefault, d.IsShared, d.UsbWebUIAvailable, d.InitialPageCount = true, true, true, 42
	d.LockedFields = []FieldLock{{Field: "model"}, {Field: "firmware"}, {Field: "consumables"}}
	d.Consumables = []string{"keep cartridge"}
	d.DNSServers = []string{"192.0.2.53"}
	d.RawData = map[string]interface{}{"learned": map[string]interface{}{"serial_oid": "1.2.3", "model_oid": "1.2.4"}, "keep": "value", "zero": 10}
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, d.Serial)
	raw := map[string]interface{}{"learned": map[string]interface{}{"model_oid": "1.2.5"}, "keep": "", "zero": 0, "false": false, "nil": nil, "empty": []string{}}
	err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{
		Manufacturer: stagePointer("new maker"), Model: stagePointer("scanner model"), Firmware: stagePointer("scanner firmware"),
		Hostname: stagePointer("  "), DNSServers: stagePointer([]string{}), Consumables: stagePointer([]string{"new cartridge"}),
		RawData: &raw,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, d.Serial)
	if err != nil {
		t.Fatal(err)
	}
	want := *before
	want.Manufacturer = "new maker"
	want.RawData = map[string]interface{}{"learned": map[string]interface{}{"serial_oid": "1.2.3", "model_oid": "1.2.5"}, "keep": "value", "zero": float64(0), "false": false}
	if !reflect.DeepEqual(after, &want) {
		t.Fatalf("unsafe partial merge:\ngot %+v\nwant %+v", after, &want)
	}
	if raw["learned"].(map[string]interface{})["serial_oid"] != nil {
		t.Fatal("caller metadata mutated")
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "metrics_raw", 0)
}

func TestCommitScannerFactsIdentityAndLiveness(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	serial, ip := "SERIAL-2", "192.0.2.2"
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{IP: &ip}, nil, nil); err == nil {
		t.Fatal("unvalidated identity created")
	}
	stageAssertRows(t, s, "devices", 0)
	at := time.Now().UTC().Add(-time.Minute)
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{ValidatedSerial: &serial, IP: &ip, LastSeen: &at}, nil, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Get(ctx, serial)
	if !d.Visible || d.IsSaved || !d.LastSeen.Equal(at) {
		t.Fatalf("creation state: %+v", d)
	}
	next := at.Add(time.Second)
	if err := s.CommitScannerFacts(ctx, serial, "192.0.2.99", DevicePatch{LastSeen: &next}, nil, nil); !errors.Is(err, ErrExpectedIPMismatch) {
		t.Fatalf("stale target accepted: %v", err)
	}
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{LastSeen: &next}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{LastSeen: &at}, nil, nil); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Get(ctx, serial)
	if !d.LastSeen.Equal(next) {
		t.Fatalf("liveness regressed: %v", d.LastSeen)
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "metrics_raw", 0)

	for _, tc := range []struct {
		name               string
		serial, expectedIP string
		patch              DevicePatch
		scan               *ScanSnapshot
		metrics            *MetricsSnapshot
	}{
		{name: "blank serial", serial: " ", expectedIP: ip},
		{name: "identity mismatch", serial: serial, expectedIP: ip, patch: DevicePatch{ValidatedSerial: stagePointer("OTHER")}},
		{name: "scan mismatch", serial: serial, expectedIP: ip, scan: &ScanSnapshot{Serial: "OTHER", CreatedAt: at, IP: ip}},
		{name: "metrics mismatch", serial: serial, expectedIP: ip, metrics: newTestMetrics("OTHER", 100)},
		{name: "liveness without IP", serial: serial, patch: DevicePatch{LastSeen: &next}},
		{name: "zero liveness", serial: serial, expectedIP: ip, patch: DevicePatch{LastSeen: stagePointer(time.Time{})}},
		{name: "scan wrong target", serial: serial, expectedIP: ip, scan: &ScanSnapshot{Serial: serial, CreatedAt: at, IP: "192.0.2.99"}},
		{name: "missing scan time", serial: serial, expectedIP: ip, scan: &ScanSnapshot{Serial: serial, IP: ip}},
		{name: "missing metric time", serial: serial, expectedIP: ip, metrics: &MetricsSnapshot{}},
		{name: "new identity no IP", serial: "NEW", patch: DevicePatch{ValidatedSerial: stagePointer("NEW")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CommitScannerFacts(ctx, tc.serial, tc.expectedIP, tc.patch, tc.scan, tc.metrics); err == nil {
				t.Fatal("invalid facts accepted")
			}
		})
	}
	stageAssertRows(t, s, "devices", 1)
}

func TestCommitScannerFactsSnapshotsOnlyWhenSupplied(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newTestDevice("SN-METRICS", "192.0.2.3", false, true)
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	metric := newTestMetrics(d.Serial, 123)
	metric.ColorPages, metric.MonoPages, metric.ScanCount = 4, 119, 6
	metricBefore := *metric
	if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{}, nil, metric); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metric, &metricBefore) {
		t.Fatal("caller snapshot mutated")
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "metrics_raw", 1)
	var pages, color, mono, scans int
	if err := s.db.QueryRow("SELECT page_count, color_pages, mono_pages, scan_count FROM metrics_raw").Scan(&pages, &color, &mono, &scans); err != nil || pages != 123 || color != 4 || mono != 119 || scans != 6 {
		t.Fatalf("snapshot counters: %d %d %d %d %v", pages, color, mono, scans, err)
	}
	scan := &ScanSnapshot{Serial: d.Serial, IP: d.IP, CreatedAt: time.Now(), Firmware: "observed"}
	if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{}, scan, nil); err != nil {
		t.Fatal(err)
	}
	stageAssertRows(t, s, "scan_history", 1)
	stageAssertRows(t, s, "metrics_raw", 1)
	after, _ := s.Get(ctx, d.Serial)
	if after.LastScanID == 0 || after.Firmware != "" || !after.LastSeen.Equal(d.LastSeen) {
		t.Fatalf("snapshot fabricated device facts: %+v", after)
	}
	tonerOnly := newTestMetrics(d.Serial, 0)
	tonerOnly.TonerLevels["black"] = 25
	if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{}, nil, tonerOnly); err != nil {
		t.Fatal(err)
	}
	// Preserve SaveMetricsSnapshot's zero-counter/reset filter, even with toner.
	stageAssertRows(t, s, "metrics_raw", 1)
}

func TestCommitScannerFactsRealTransactionRollback(t *testing.T) {
	t.Parallel()
	for _, failTable := range []string{"scan_history", "metrics_raw"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", failTable, existing), func(t *testing.T) {
				t.Parallel()
				s := stageTestStore(t)
				ctx := context.Background()
				serial, ip := "ROLLBACK", "192.0.2.4"
				var before *Device
				if existing {
					d := newFullTestDevice(serial, ip, "maker", "before", true, false)
					if err := s.Create(ctx, d); err != nil {
						t.Fatal(err)
					}
					before, _ = s.Get(ctx, serial)
				}
				// This trigger proves the device write actually happened in the
				// transaction. Prevalidation failure cannot pass this assertion.
				_, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_stage BEFORE INSERT ON %s BEGIN
					SELECT CASE WHEN (SELECT model FROM devices WHERE serial = NEW.serial) = 'after'
					THEN RAISE(ABORT, 'injected_after_device_write') ELSE RAISE(ABORT, 'device_not_written') END; END`, failTable))
				if err != nil {
					t.Fatal(err)
				}
				patch := DevicePatch{ValidatedSerial: &serial, IP: &ip, Model: stagePointer("after")}
				scan := &ScanSnapshot{Serial: serial, IP: ip, CreatedAt: time.Now()}
				metrics := newTestMetrics(serial, 123)
				err = s.CommitScannerFacts(ctx, serial, ip, patch, scan, metrics)
				if err == nil || !strings.Contains(err.Error(), "injected_after_device_write") {
					t.Fatalf("not a post-write rollback: %v", err)
				}
				after, err := s.Get(ctx, serial)
				if existing {
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("device/last_scan_id not rolled back: %+v %+v %v", before, after, err)
					}
				} else if !errors.Is(err, ErrNotFound) {
					t.Fatalf("creation not rolled back: %+v %v", after, err)
				}
				stageAssertRows(t, s, "scan_history", 0)
				stageAssertRows(t, s, "metrics_raw", 0)
			})
		}
	}
}

func TestCommitScannerFactsConcurrentMerge(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newTestDevice("CONCURRENT", "192.0.2.5", true, false)
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := map[string]interface{}{"learned": map[string]interface{}{fmt.Sprintf("oid_%d", i): i}}
			if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{RawData: &raw}, nil, nil); err != nil {
				t.Errorf("concurrent commit: %v", err)
			}
		}(i)
	}
	wg.Wait()
	after, err := s.Get(ctx, d.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.RawData["learned"].(map[string]interface{})) != 12 || after.Visible || !after.IsSaved {
		t.Fatalf("lost facts/state: %+v", after)
	}
}

func TestCommitScannerFactsNoFabricatedObservation(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	serial, ip := "IDENTITY-ONLY", "192.0.2.6"
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{ValidatedSerial: &serial, IP: &ip}, nil, nil); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get(ctx, serial)
	if err != nil || !d.LastSeen.IsZero() || d.LastScanID != 0 {
		t.Fatalf("fabricated observation: %+v %v", d, err)
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "metrics_raw", 0)
}

func TestCommitScannerFactsLockedLivenessAndRaw(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newTestDevice("LOCKED", "192.0.2.7", false, false)
	d.RawData = map[string]interface{}{"keep": "old"}
	d.LockedFields = []FieldLock{{Field: "last_seen"}, {Field: "raw_data"}, {Field: "ip"}}
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, d.Serial)
	raw := map[string]interface{}{"keep": "new"}
	if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{
		ValidatedSerial: &d.Serial, IP: stagePointer("192.0.2.8"), LastSeen: stagePointer(time.Now().Add(time.Second)), RawData: &raw,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx, d.Serial)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("locked state changed: %+v %+v", before, after)
	}
}

func TestMergeScannerMetadataEmptyNestedPreservesFact(t *testing.T) {
	t.Parallel()
	dst := map[string]interface{}{"keep": "old", "nested": map[string]interface{}{"oid": "1.2.3"}}
	mergeScannerMetadata(dst, map[string]interface{}{"keep": map[string]interface{}{"empty": ""}, "nested": map[string]interface{}{"oid": ""}})
	if dst["keep"] != "old" || dst["nested"].(map[string]interface{})["oid"] != "1.2.3" {
		t.Fatalf("empty nested values erased facts: %+v", dst)
	}
}

type stageOutcomeLogger struct {
	message string
	values  []interface{}
}

func (*stageOutcomeLogger) Error(string, ...interface{})                                  {}
func (*stageOutcomeLogger) Warn(string, ...interface{})                                   {}
func (*stageOutcomeLogger) Info(string, ...interface{})                                   {}
func (*stageOutcomeLogger) WarnRateLimited(string, time.Duration, string, ...interface{}) {}
func (l *stageOutcomeLogger) Debug(message string, values ...interface{}) {
	l.message, l.values = message, append([]interface{}(nil), values...)
}

func TestCommitScannerFactsStructuredOutcomeNoSecrets(t *testing.T) {
	// Global logger tests must run sequentially, before parallel storage tests.
	s := stageTestStore(t)
	l := &stageOutcomeLogger{}
	previous := storageLogger
	SetLogger(l)
	defer SetLogger(previous)
	serial, ip := "PRIVATE-SERIAL", "192.0.2.100"
	raw := map[string]interface{}{"community": "SECRET-COMMUNITY"}
	patch := DevicePatch{ValidatedSerial: &serial, IP: &ip, Model: stagePointer("printer"), RawData: &raw}
	scan := &ScanSnapshot{Serial: serial, IP: ip, CreatedAt: time.Now()}
	metrics := newTestMetrics(serial, 10)
	if err := s.CommitScannerFacts(context.Background(), serial, ip, patch, scan, metrics); err != nil {
		t.Fatal(err)
	}
	want := []interface{}{"outcome", "committed", "fields", 3, "scans", 1, "metrics", 1, "metrics_outcome", "stored"}
	if l.message != "Scanner facts commit" || !reflect.DeepEqual(l.values, want) {
		t.Fatalf("bad structured outcome: %s %+v", l.message, l.values)
	}
	if err := s.CommitScannerFacts(context.Background(), serial, "stale", patch, scan, metrics); err == nil {
		t.Fatal("stale commit accepted")
	}
	want = []interface{}{"outcome", "rejected", "fields", 0, "scans", 0, "metrics", 0, "metrics_outcome", "rolled_back"}
	if !reflect.DeepEqual(l.values, want) {
		t.Fatalf("rejected commit reported writes: %+v", l.values)
	}
	metrics.PageCount = 0
	if err := s.CommitScannerFacts(context.Background(), serial, ip, DevicePatch{}, nil, metrics); err != nil {
		t.Fatal(err)
	}
	want = []interface{}{"outcome", "committed", "fields", 0, "scans", 0, "metrics", 0, "metrics_outcome", "zero_counters"}
	if !reflect.DeepEqual(l.values, want) {
		t.Fatalf("omitted metrics not reported: %+v", l.values)
	}
}

func TestCommitScannerFactsAddressMove(t *testing.T) {
	t.Parallel()
	for _, lockedIP := range []bool{false, true} {
		t.Run(fmt.Sprintf("locked=%v", lockedIP), func(t *testing.T) {
			t.Parallel()
			s := stageTestStore(t)
			ctx := context.Background()
			d := newFullTestDevice("MOVE", "192.0.2.10", "maker", "user model", true, false)
			d.Location, d.Description, d.AssetNumber, d.InitialPageCount = "room", "note", "asset", 40
			d.LockedFields = []FieldLock{{Field: "model"}}
			if lockedIP {
				d.LockedFields = append(d.LockedFields, FieldLock{Field: "ip"})
			}
			if err := s.Create(ctx, d); err != nil {
				t.Fatal(err)
			}
			before, err := s.Get(ctx, d.Serial)
			if err != nil {
				t.Fatal(err)
			}
			destination := "192.0.2.11"
			at := before.LastSeen.Add(time.Second)
			patch := DevicePatch{ValidatedSerial: &d.Serial, IP: &destination, LastSeen: &at, Model: stagePointer("scanner model")}
			scan := &ScanSnapshot{Serial: d.Serial, IP: destination, CreatedAt: at}
			metric := newTestMetrics(d.Serial, 123)
			// Prior address, not destination, is the CAS operand. Every rejection
			// must preserve device state AND prevent either history insertion.
			for _, guard := range []string{destination, "192.0.2.99", ""} {
				if err := s.CommitScannerFacts(ctx, d.Serial, guard, patch, scan, metric); err == nil {
					t.Fatalf("unsafe move accepted with guard %q", guard)
				}
				after, err := s.Get(ctx, d.Serial)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected move changed row: %+v %v", after, err)
				}
			}
			stageAssertRows(t, s, "scan_history", 0)
			stageAssertRows(t, s, "metrics_raw", 0)
			wrongScan := *scan
			wrongScan.IP = d.IP
			if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, patch, &wrongScan, metric); err == nil {
				t.Fatal("prior-address snapshot accepted for destination facts")
			}
			if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, patch, scan, metric); err != nil {
				t.Fatal(err)
			}
			after, err := s.Get(ctx, d.Serial)
			if err != nil {
				t.Fatal(err)
			}
			want := *before
			if !lockedIP {
				want.IP = destination
			}
			want.LastSeen, want.LastScanID = at, after.LastScanID
			if after.LastScanID == 0 || !reflect.DeepEqual(after, &want) {
				t.Fatalf("move lost user state/locks: got %+v want %+v", after, &want)
			}
			stageAssertRows(t, s, "scan_history", 1)
			stageAssertRows(t, s, "metrics_raw", 1)
			var storedIP string
			if err := s.db.QueryRow("SELECT ip FROM scan_history").Scan(&storedIP); err != nil || storedIP != destination {
				t.Fatalf("destination history = %q: %v", storedIP, err)
			}
		})
	}
}

func TestCommitScannerFactsTargetOccupancy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"create", "move", "locked_move", "liveness", "metrics", "unguarded", "ipv4_alias", "ipv6_alias"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := stageTestStore(t)
			ctx := context.Background()
			serial, prior, destination, occupantIP := "SUBJECT", "192.0.2.20", "192.0.2.21", "192.0.2.21"
			if mode == "ipv4_alias" {
				occupantIP = "::ffff:192.0.2.21"
			}
			if mode == "ipv6_alias" {
				destination, occupantIP = "2001:db8::21", "2001:0db8:0:0:0:0:0:21"
			}
			occupant := newFullTestDevice("OCCUPANT", occupantIP, "maker", "occupant", true, false)
			if err := s.Create(ctx, occupant); err != nil {
				t.Fatal(err)
			}
			occupantBefore, _ := s.Get(ctx, occupant.Serial)
			patch := DevicePatch{ValidatedSerial: &serial, IP: &destination, Model: stagePointer("changed")}
			guard := prior
			var before *Device
			if mode == "create" {
				guard = destination
			} else {
				if mode == "liveness" || mode == "metrics" || mode == "unguarded" {
					prior, guard = destination, destination
				}
				d := newFullTestDevice(serial, prior, "maker", "original", true, false)
				if mode == "locked_move" {
					d.LockedFields = []FieldLock{{Field: "ip"}}
				}
				if err := s.Create(ctx, d); err != nil {
					t.Fatal(err)
				}
				before, _ = s.Get(ctx, serial)
			}
			scan := &ScanSnapshot{Serial: serial, IP: destination, CreatedAt: time.Now()}
			metrics := newTestMetrics(serial, 100)
			if mode == "liveness" {
				patch = DevicePatch{LastSeen: stagePointer(time.Now().Add(time.Second))}
				scan, metrics = nil, nil
			}
			if mode == "metrics" {
				patch, scan = DevicePatch{}, nil
			}
			if mode == "unguarded" {
				guard, patch = "", DevicePatch{Model: stagePointer("changed")}
			}
			if err := s.CommitScannerFacts(ctx, serial, guard, patch, scan, metrics); !errors.Is(err, ErrScannerTargetOccupied) {
				t.Fatalf("ambiguous target accepted: %v", err)
			}
			after, err := s.Get(ctx, serial)
			if before == nil {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("ambiguous device created: %+v %v", after, err)
				}
			} else if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("ambiguous commit changed subject: %+v %v", after, err)
			}
			occupantAfter, err := s.Get(ctx, occupant.Serial)
			if err != nil || !reflect.DeepEqual(occupantBefore, occupantAfter) {
				t.Fatalf("occupant overwritten: %+v %v", occupantAfter, err)
			}
			stageAssertRows(t, s, "scan_history", 0)
			stageAssertRows(t, s, "metrics_raw", 0)
		})
	}
}

func TestCommitScannerFactsConcurrentTargetClaims(t *testing.T) {
	t.Parallel()
	for _, creating := range []bool{false, true} {
		t.Run(fmt.Sprintf("creating=%v", creating), func(t *testing.T) {
			t.Parallel()
			s := stageTestStore(t)
			ctx := context.Background()
			destination := "192.0.2.30"
			const claims = 8
			for i := 0; i < claims && !creating; i++ {
				if err := s.Create(ctx, newTestDevice(fmt.Sprintf("CLAIM-%d", i), fmt.Sprintf("192.0.2.%d", 40+i), false, true)); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, claims)
			for i := 0; i < claims; i++ {
				go func(i int) {
					<-start
					serial, guard := fmt.Sprintf("CLAIM-%d", i), destination
					if !creating {
						guard = fmt.Sprintf("192.0.2.%d", 40+i)
					}
					results <- s.CommitScannerFacts(ctx, serial, guard, DevicePatch{ValidatedSerial: &serial, IP: &destination}, nil, newTestMetrics(serial, 100))
				}(i)
			}
			close(start)
			committed := 0
			for i := 0; i < claims; i++ {
				if err := <-results; err == nil {
					committed++
				} else if !errors.Is(err, ErrScannerTargetOccupied) {
					t.Fatalf("unexpected claim error: %v", err)
				}
			}
			if committed != 1 {
				t.Fatalf("committed %d claims, want 1", committed)
			}
			stageAssertRows(t, s, "metrics_raw", 1)
			stageAssertRows(t, s, "scan_history", 0)
		})
	}
}

func TestCommitScannerFactsSerialKeySafety(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	ip := "192.0.2.50"
	for _, serial := range []string{"", " ", " leading", "trailing ", ".", "..", "../device", `device\child`, "nul\x00byte", "line\nbreak", "tab\tkey", "delete\x7fkey", "c1\u0085key", "invalid\xffkey"} {
		if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{ValidatedSerial: &serial, IP: &ip}, nil, nil); !errors.Is(err, ErrInvalidSerial) {
			t.Fatalf("unsafe serial %q: %v", serial, err)
		}
	}
	stageAssertRows(t, s, "devices", 0)
	// SQL-looking punctuation and Unicode are opaque keys, not SQL or paths.
	serial := "製造-ABC ' ; --"
	if err := s.CommitScannerFacts(ctx, serial, ip, DevicePatch{ValidatedSerial: &serial, IP: &ip}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d, err := s.Get(ctx, serial); err != nil || d.Serial != serial {
		t.Fatalf("key altered: %+v %v", d, err)
	}
}

func TestCommitScannerFactsMetricsPolicyParity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		previous, incoming [4]int
		wantRows           int
	}{
		{"zero", [4]int{}, [4]int{}, 0},
		{"first", [4]int{}, [4]int{1000, 400, 600, 100}, 1},
		{"increase", [4]int{1000, 400, 600, 100}, [4]int{1100, 440, 660, 110}, 2},
		{"equal", [4]int{1000, 400, 600, 100}, [4]int{1000, 400, 600, 100}, 2},
		{"page_decrease", [4]int{1000, 400, 600, 100}, [4]int{900, 360, 540, 100}, 1},
		{"color_decrease", [4]int{1000, 400, 600, 100}, [4]int{1000, 300, 700, 100}, 1},
		{"mono_decrease", [4]int{1000, 400, 600, 100}, [4]int{1000, 500, 500, 100}, 1},
		{"scan_decrease", [4]int{1000, 400, 600, 100}, [4]int{1000, 400, 600, 80}, 1},
		{"five_percent_boundary", [4]int{1000, 0, 0, 0}, [4]int{950, 0, 0, 0}, 2},
		{"five_percent_exceeded", [4]int{1000, 0, 0, 0}, [4]int{949, 0, 0, 0}, 1},
		{"minimum_ten_boundary", [4]int{100, 0, 0, 0}, [4]int{90, 0, 0, 0}, 2},
		{"minimum_ten_exceeded", [4]int{100, 0, 0, 0}, [4]int{89, 0, 0, 0}, 1},
		{"toner_reset", [4]int{1000, 400, 600, 100}, [4]int{}, 1},
		{"parts_under", [4]int{}, [4]int{10000, 3000, 3000, 0}, 0},
		{"parts_over", [4]int{}, [4]int{10000, 6000, 6000, 0}, 0},
		{"parts_tolerance", [4]int{}, [4]int{10000, 5000, 4000, 0}, 1},
		{"parts_minimum", [4]int{}, [4]int{200, 50, 50, 0}, 1},
		{"parts_minimum_exceeded", [4]int{}, [4]int{200, 49, 50, 0}, 0},
		{"mono_only", [4]int{}, [4]int{10000, 0, 10000, 0}, 1},
		{"scan_only", [4]int{}, [4]int{0, 0, 0, 100}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacy, staged := stageTestStore(t), stageTestStore(t)
			// Transactional validation must work with no spare DB connection.
			staged.db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			serial, ip := "METRIC-PARITY", "192.0.2.60"
			at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("test", 3600))
			for _, s := range []*SQLiteStore{legacy, staged} {
				if err := s.Create(ctx, newTestDevice(serial, ip, false, true)); err != nil {
					t.Fatal(err)
				}
				if tc.previous != [4]int{} {
					previous := &MetricsSnapshot{Serial: serial, Timestamp: at.Add(-time.Minute), PageCount: tc.previous[0], ColorPages: tc.previous[1], MonoPages: tc.previous[2], ScanCount: tc.previous[3]}
					if err := s.SaveMetricsSnapshot(ctx, previous); err != nil {
						t.Fatal(err)
					}
				}
			}
			metric := &MetricsSnapshot{Serial: serial, Timestamp: at, PageCount: tc.incoming[0], ColorPages: tc.incoming[1], MonoPages: tc.incoming[2], ScanCount: tc.incoming[3], TonerLevels: map[string]interface{}{"black": 25}}
			original := *metric
			legacyMetric := *metric
			if err := legacy.SaveMetricsSnapshot(ctx, &legacyMetric); err != nil {
				t.Fatal(err)
			}
			if err := staged.CommitScannerFacts(ctx, serial, ip, DevicePatch{Model: stagePointer("obtained")}, nil, metric); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(metric, &original) {
				t.Fatal("caller metric mutated")
			}
			for _, s := range []*SQLiteStore{legacy, staged} {
				stageAssertRows(t, s, "metrics_raw", tc.wantRows)
			}
			device, err := staged.Get(ctx, serial)
			if err != nil || device.Model != "obtained" {
				t.Fatalf("metrics omission lost obtained facts: %+v %v", device, err)
			}
			if tc.wantRows > 0 {
				got, err := staged.GetLatestMetrics(ctx, serial)
				want, wantErr := legacy.GetLatestMetrics(ctx, serial)
				if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("metric policy diverged: got %+v want %+v errors %v/%v", got, want, err, wantErr)
				}
			}
		})
	}
}

func TestCommitScannerFactsMetricsReadFailureRollsBack(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newTestDevice("READ-FAILURE", "192.0.2.70", true, false)
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, d.Serial)
	// A validation read error must not become permission to insert metrics or
	// partially persist facts/history. Renaming preserves the table for checks.
	if _, err := s.db.Exec("ALTER TABLE metrics_raw RENAME TO unavailable_metrics"); err != nil {
		t.Fatal(err)
	}
	scan := &ScanSnapshot{Serial: d.Serial, IP: d.IP, CreatedAt: time.Now()}
	if err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{Model: stagePointer("after")}, scan, newTestMetrics(d.Serial, 123)); err == nil {
		t.Fatal("metrics validation read failure ignored")
	}
	after, err := s.Get(ctx, d.Serial)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read failure left partial device: %+v %v", after, err)
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "unavailable_metrics", 0)
}

func TestCommitScannerFactsAddressMoveRollback(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newFullTestDevice("MOVE-ROLLBACK", "192.0.2.80", "maker", "original", true, false)
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, d.Serial)
	destination := "192.0.2.81"
	// Prove the address move happened before injecting a later history failure.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_move BEFORE INSERT ON metrics_raw BEGIN
		SELECT CASE WHEN (SELECT ip FROM devices WHERE serial = NEW.serial) = '192.0.2.81'
		THEN RAISE(ABORT, 'injected_after_move') ELSE RAISE(ABORT, 'move_not_written') END; END`); err != nil {
		t.Fatal(err)
	}
	scan := &ScanSnapshot{Serial: d.Serial, IP: destination, CreatedAt: time.Now()}
	err := s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{ValidatedSerial: &d.Serial, IP: &destination}, scan, newTestMetrics(d.Serial, 100))
	if err == nil || !strings.Contains(err.Error(), "injected_after_move") {
		t.Fatalf("not a post-move failure: %v", err)
	}
	after, err := s.Get(ctx, d.Serial)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("move/history pointer not rolled back: %+v %v", after, err)
	}
	stageAssertRows(t, s, "scan_history", 0)
	stageAssertRows(t, s, "metrics_raw", 0)
}

func TestCommitScannerFactsConcurrentMetricsValidation(t *testing.T) {
	t.Parallel()
	s := stageTestStore(t)
	ctx := context.Background()
	d := newTestDevice("METRIC-RACE", "192.0.2.90", false, true)
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	at := time.Now().UTC()
	for i := 0; i < cap(results); i++ {
		go func(i int) {
			<-start
			pages := 100
			if i%2 == 0 {
				pages = 1000
			}
			metric := &MetricsSnapshot{Serial: d.Serial, Timestamp: at.Add(time.Duration(pages) * time.Second), PageCount: pages}
			results <- s.CommitScannerFacts(ctx, d.Serial, d.IP, DevicePatch{}, nil, metric)
		}(i)
	}
	close(start)
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	// Low observations may precede the first high one, never follow it in
	// insertion order. Validation and insertion must share the write reservation.
	var regressions int
	if err := s.db.QueryRow(`SELECT count(*) FROM metrics_raw WHERE page_count = 100
		AND id > (SELECT min(id) FROM metrics_raw WHERE page_count = 1000)`).Scan(&regressions); err != nil || regressions != 0 {
		t.Fatalf("concurrent metrics validation bypassed: regressions=%d err=%v", regressions, err)
	}
	latest, err := s.GetLatestMetrics(ctx, d.Serial)
	if err != nil || latest.PageCount != 1000 {
		t.Fatalf("latest counter regressed: %+v %v", latest, err)
	}
}
