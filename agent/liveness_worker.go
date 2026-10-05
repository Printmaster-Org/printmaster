package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
	"printmaster/common/snmp/oids"

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
	cfg, err := scanner.GetSNMPConfig()
	if err != nil {
		return false, err
	}
	client, err := scanner.NewSNMPClientWithContext(ctx, cfg, device.IP, 2, 1)
	if err != nil {
		return false, err
	}
	defer client.Close()
	pi := storage.DeviceToPrinterInfo(device)
	identityOIDs := []string{oids.PrtGeneralSerialNumber}
	if pi.LearnedOIDs.SerialOID != "" && strings.TrimPrefix(pi.LearnedOIDs.SerialOID, ".") != oids.PrtGeneralSerialNumber {
		identityOIDs = append(identityOIDs, pi.LearnedOIDs.SerialOID)
	}
	identityOIDs = append(identityOIDs, scanner.VendorIDTargetOIDs()...)
	packet, err := client.Get(identityOIDs)
	if err != nil {
		return false, err
	}
	// SNMPv1 rejects a whole multi-GET if any optional vendor OID is absent.
	// Retry only that protocol error one OID at a time within the same deadline.
	if packet != nil && packet.Error == gosnmp.NoSuchName && cfg.Version == gosnmp.Version1 {
		for _, oid := range identityOIDs {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			response, err := client.Get([]string{oid})
			if err != nil {
				return false, err
			}
			if response != nil && response.Error == gosnmp.NoError && matchesKnownDeviceIdentity(device, response.Variables, pi.LearnedOIDs.SerialOID) {
				return true, nil
			}
		}
		return false, nil
	}
	if packet == nil || packet.Error != gosnmp.NoError {
		return false, fmt.Errorf("no successful identity response from %s", device.IP)
	}
	return matchesKnownDeviceIdentity(device, packet.Variables, pi.LearnedOIDs.SerialOID), nil
}

func matchesKnownDeviceIdentity(device *storage.Device, pdus []gosnmp.SnmpPDU, learnedSerialOID string) bool {
	return agent.MatchesSerialIdentity(device.Serial, learnedSerialOID, pdus)
}

// startDeviceLivenessMonitor returns a stop-and-join function. Each round drains
// before another starts, keeping network work bounded even with many old devices.
func startDeviceLivenessMonitor(ctx context.Context, store deviceLivenessStore, enabled func() bool) func() {
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
