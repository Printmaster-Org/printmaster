package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"

	"github.com/gosnmp/gosnmp"
)

const deviceLivenessInterval = time.Minute

type deviceLivenessStore interface {
	List(context.Context, storage.DeviceFilter) ([]*storage.Device, error)
	TouchDeviceSeen(context.Context, string, string, time.Time) error
}

// monitorDeviceLiveness deliberately does not collect metrics, walk MIBs, or
// overwrite inventory. A serial-confirmed response is evidence of this device,
// not merely an open port on an address another printer may now occupy.
func monitorDeviceLiveness(ctx context.Context, store deviceLivenessStore, probe func(context.Context, *storage.Device) (bool, error)) error {
	visible := true
	devices, err := store.List(ctx, storage.DeviceFilter{Visible: &visible})
	if err != nil {
		return err
	}
	jobs := make(chan *storage.Device)
	var workers sync.WaitGroup
	for range 5 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for device := range jobs {
				if ctx.Err() != nil {
					return
				}
				observedAt := time.Now().UTC()
				probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				alive, err := probe(probeCtx, device)
				cancel()
				if err == nil && alive && ctx.Err() == nil {
					if err := store.TouchDeviceSeen(ctx, device.Serial, device.IP, observedAt); err != nil && appLogger != nil {
						appLogger.Debug("Liveness observation no longer matches inventory", "serial", device.Serial, "error", err)
					}
				}
			}
		}()
	}
	for _, device := range devices {
		if device == nil || device.Serial == "" || device.IP == "" || device.IsUSB || device.DeviceType == "usb" || device.SourceType == "spooler" || device.DeviceType == "local" || device.DeviceType == "virtual" {
			continue
		}
		select {
		case jobs <- device:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return ctx.Err()
}

func probeKnownDeviceIdentity(ctx context.Context, device *storage.Device) (bool, error) {
	runtime, err := requireScanner()
	if err != nil {
		return false, err
	}
	observation, err := scannerObservation(device.IP, scanner.SourceManual)
	if err != nil {
		return false, err
	}
	pi := storage.DeviceToPrinterInfo(device)
	result, err := runtime.request(ctx, observation, scanner.IntentLiveness,
		scanner.WorkOptions{SNMPAfterTCPFailure: true, TimeoutSeconds: 2, LearnedSerialOID: pi.LearnedOIDs.SerialOID}, device.Serial, true, nil)
	return err == nil && result.Serial == device.Serial, err
}

func matchesKnownDeviceIdentity(device *storage.Device, pdus []gosnmp.SnmpPDU, learnedSerialOID string) bool {
	return agent.MatchesSerialIdentity(device.Serial, learnedSerialOID, pdus)
}

// startDeviceLivenessMonitor returns a stop-and-join function. Each round drains
// before another starts, keeping network work bounded even with many old devices.
func startDeviceLivenessMonitor(ctx context.Context, store deviceLivenessStore, enabled func() bool) func() {
	if mainScanner != nil {
		store = scannerLivenessStore{deviceLivenessStore: store, runtime: mainScanner}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(deviceLivenessInterval)
		defer ticker.Stop()
		for {
			if enabled() && ctx.Err() == nil {
				if err := monitorDeviceLiveness(ctx, store, probeKnownDeviceIdentity); err != nil && ctx.Err() == nil && appLogger != nil {
					appLogger.Warn("Device liveness check failed", "error", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

type scannerLivenessStore struct {
	deviceLivenessStore
	runtime *scannerRuntime
}

func (s scannerLivenessStore) TouchDeviceSeen(ctx context.Context, serial, ip string, at time.Time) error {
	store, ok := s.runtime.store.(storage.StageCommitStore)
	if !ok {
		return fmt.Errorf("staged scanner storage unavailable")
	}
	if err := store.CommitScannerFacts(ctx, serial, ip, storage.DevicePatch{LastSeen: &at}, nil, nil); err != nil {
		return err
	}
	scannerUploadWake()
	return nil
}

// Production monitors all known network devices, including hidden inventory;
// a liveness touch never changes visibility or saved state. Legacy monitor
// callers retain their existing visible-only filter.
func (s scannerLivenessStore) List(ctx context.Context, _ storage.DeviceFilter) ([]*storage.Device, error) {
	return s.deviceLivenessStore.List(ctx, storage.DeviceFilter{})
}
