package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"printmaster/agent/storage"
	"printmaster/common/snmp/oids"
	commonstorage "printmaster/common/storage"

	"github.com/gosnmp/gosnmp"
)

func TestMonitorDeviceLivenessPreservesAbsentPrinters(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	for _, serial := range []string{"alive", "off", "reused-ip", "usb", "hidden"} {
		d := &storage.Device{Device: commonstorage.Device{Serial: serial, IP: "192.0.2.1", LastSeen: old, StatusMessages: []string{"paper jam"}}, Visible: serial != "hidden", IsSaved: serial == "alive"}
		d.IsUSB = serial == "usb"
		if err := store.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	var attempts atomic.Int32
	err = monitorDeviceLiveness(ctx, store, func(ctx context.Context, d *storage.Device) (bool, error) {
		attempts.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("probe has no deadline")
		}
		if d.Serial == "off" {
			return false, errors.New("timeout")
		}
		return d.Serial == "alive", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts=%d, want only visible network printers", attempts.Load())
	}
	for _, serial := range []string{"alive", "off", "reused-ip", "usb", "hidden"} {
		d, err := store.Get(ctx, serial)
		if err != nil {
			t.Fatal(err)
		}
		if serial == "alive" {
			if !d.LastSeen.After(old) {
				t.Error("confirmed printer timestamp not refreshed")
			}
		} else if !d.LastSeen.Equal(old) {
			t.Errorf("%s incorrectly refreshed", serial)
		}
		if len(d.StatusMessages) != 1 || d.StatusMessages[0] != "paper jam" {
			t.Error("cached faults overwritten")
		}
		history, err := store.GetScanHistory(ctx, serial, 10)
		if err != nil || len(history) != 0 {
			t.Fatalf("liveness added scans: %v, %v", history, err)
		}
	}
}

func TestLivenessTouchDoesNotResurrectOrOverwrite(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	d := &storage.Device{Device: commonstorage.Device{Serial: "original", IP: "192.0.2.1", LastSeen: now}, Visible: true}
	if err := store.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		serial, ip string
		at         time.Time
	}{
		{"missing", d.IP, now.Add(time.Minute)},
		{d.Serial, "192.0.2.2", now.Add(time.Minute)},
		{d.Serial, d.IP, now.Add(-time.Minute)},
	} {
		if err := store.TouchDeviceSeen(ctx, tc.serial, tc.ip, tc.at); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("unguarded touch: %v", err)
		}
	}
	if err := store.MarkSaved(ctx, d.Serial); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDiscovered(ctx, d.Serial); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAllSaved(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, d.Serial)
	if err != nil || !stored.LastSeen.Equal(now) {
		t.Fatalf("metadata operation refreshed seen: %+v, %v", stored, err)
	}
	if err := store.Delete(ctx, d.Serial); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchDeviceSeen(ctx, d.Serial, d.IP, now.Add(time.Minute)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted device resurrected: %v", err)
	}
}

func TestLivenessIdentityRequiresMatchingSerial(t *testing.T) {
	t.Parallel()
	d := &storage.Device{Device: commonstorage.Device{Serial: "MATCH", IP: "192.0.2.1"}}
	for _, tc := range []struct {
		name, oid string
		kind      gosnmp.Asn1BER
		value     interface{}
		learned   string
		want      bool
	}{
		{"standard", oids.PrtGeneralSerialNumber, gosnmp.OctetString, []byte("MATCH"), "", true},
		{"reused address", oids.PrtGeneralSerialNumber, gosnmp.OctetString, []byte("NEW-PRINTER"), "", false},
		{"no serial", oids.PrtGeneralSerialNumber, gosnmp.NoSuchInstance, nil, "", false},
		{"learned", "1.3.6.1.4.1.999.1", gosnmp.OctetString, "MATCH", "1.3.6.1.4.1.999.1", true},
		{"unrelated", oids.SysDescr, gosnmp.OctetString, "MATCH", "", false},
		{"IEEE1284", "1.3.6.1.4.1.11.2.3.9.1.1.7.0", gosnmp.OctetString, "MFG:HP;MDL:LaserJet;SN:MATCH;", "", true},
		{"IEEE1284 reused", "1.3.6.1.4.1.11.2.3.9.1.1.7.0", gosnmp.OctetString, "MFG:HP;SN:OTHER;", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesKnownDeviceIdentity(d, []gosnmp.SnmpPDU{{Name: tc.oid, Type: tc.kind, Value: tc.value}}, tc.learned); got != tc.want {
				t.Fatalf("matched=%v want %v", got, tc.want)
			}
		})
	}
}

func TestLivenessCancellationDoesNotTouch(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	d := &storage.Device{Device: commonstorage.Device{Serial: "cancel", IP: "192.0.2.1", LastSeen: time.Now().UTC().Add(-time.Hour)}, Visible: true}
	if err := store.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	err = monitorDeviceLiveness(ctx, store, func(ctx context.Context, _ *storage.Device) (bool, error) { cancel(); return true, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	stored, err := store.Get(context.Background(), d.Serial)
	if err != nil || !stored.LastSeen.Equal(d.LastSeen) {
		t.Fatalf("cancelled probe refreshed: %+v, %v", stored, err)
	}
}

func TestLivenessMonitorDisabledStopsAndJoins(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checked := make(chan struct{}, 1)
	stop := startDeviceLivenessMonitor(context.Background(), store, func() bool {
		checked <- struct{}{}
		return false
	})
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("monitor never checked runtime flags")
	}
	stop()
}
