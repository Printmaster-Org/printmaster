package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
)

// Discover shares startup's coordinator. Quick read-only; full persists.
func Discover(ctx context.Context, ranges []string, mode string, cfg *agent.DiscoveryConfig, store storage.DeviceStore, concurrency, timeout int) ([]agent.PrinterInfo, error) {
	if mode != "quick" && mode != "full" {
		return nil, fmt.Errorf("invalid discovery mode: %s (must be 'quick' or 'full')", mode)
	}
	if err := scannerIPScanningAllowed(); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &agent.DiscoveryConfig{TCPEnabled: true, SNMPEnabled: true}
	}
	if !cfg.TCPEnabled && !cfg.SNMPEnabled {
		return nil, errors.New("TCP and SNMP discovery are disabled")
	}
	runtime, err := requireScanner()
	if err != nil {
		return nil, err
	}
	ips, err := discoveryIPs(ranges)
	if err != nil {
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = 50
	}
	if concurrency > 50 {
		concurrency = 50
	}
	// Preserve legacy JSON null for no candidates (not an invented empty array).
	var results []agent.PrinterInfo
	jobs := make(chan string)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ip := range jobs {
				observation, err := scannerObservation(ip, scanner.SourceRange)
				if err != nil {
					continue
				}
				preset := scanner.IntentQuick
				if mode == "full" {
					preset = scanner.IntentFull
				}
				var result scanner.Result
				ports := []uint16{9100, 80, 443}
				if mode == "full" {
					ports = append(ports, 515, 631)
				}
				if !cfg.SNMPEnabled {
					result, err = runtime.coordinator.Do(ctx, scanner.Request{Observation: observation, Intent: scanner.Intent{Reachability: true}, Options: scanner.WorkOptions{ReadOnly: true, Ports: ports}})
				} else {
					result, err = runtime.request(ctx, observation, preset, scanner.WorkOptions{TimeoutSeconds: timeout, Ports: ports, SkipTCP: !cfg.TCPEnabled, SNMPAfterTCPFailure: !cfg.TCPEnabled, MissingSerialFallback: mode == "full"}, "", mode == "quick", nil)
				}
				if err != nil {
					if appLogger != nil {
						appLogger.Debug("Range scanner candidate rejected", "ip", ip, "error", true)
					}
					continue
				}
				var pi agent.PrinterInfo
				if !cfg.SNMPEnabled {
					if result.Outcomes[scanner.StageReachability].Status != scanner.StatusSucceeded {
						continue
					}
					pi.IP = ip
					for _, port := range result.OpenPorts {
						pi.OpenPorts = append(pi.OpenPorts, int(port))
					}
				} else {
					pi, err = scannerPrinterInfo(result)
					if err != nil {
						continue
					}
				}
				if !cfg.SNMPEnabled {
					pi.DiscoveryMethods = []string{mode + "-discovery"}
				}
				mu.Lock()
				results = append(results, pi)
				mu.Unlock()
			}
		}()
	}
	for _, ip := range ips {
		select {
		case jobs <- ip:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return results, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return results, ctx.Err()
}

// First-local-subnet default retained; overlapping ranges deduplicated.
func discoveryIPs(ranges []string) ([]string, error) {
	if len(ranges) == 0 {
		subnets, err := agent.GetLocalSubnets()
		if err != nil || len(subnets) == 0 {
			return nil, errors.New("no ranges provided and could not auto-detect subnet")
		}
		ranges = []string{subnets[0].String()}
	}
	seen := make(map[string]bool)
	var ips []string
	for _, text := range ranges {
		parsed, err := agent.ParseRangeText(text, 10000)
		if err != nil {
			continue
		}
		for _, ip := range parsed.IPs {
			if !seen[ip] {
				seen[ip] = true
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no IPs to scan after parsing ranges")
	}
	return ips, nil
}

type DiscoveredDevice struct {
	IP     string `json:"ip"`
	Source string `json:"source"`
	Ports  []int  `json:"ports,omitempty"`
}

func DiscoverNow(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	// timeout remains a per-query setting, not a new whole-subnet deadline.
	timeoutSeconds := int(timeout.Seconds())
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	results, err := Discover(ctx, nil, "quick", &agent.DiscoveryConfig{TCPEnabled: true}, nil, 50, timeoutSeconds)
	if err != nil {
		return nil, err
	}
	var out []DiscoveredDevice
	for _, pi := range results {
		out = append(out, DiscoveredDevice{IP: pi.IP, Source: "tcp", Ports: pi.OpenPorts})
	}
	return out, err
}

// Compatibility wrappers query through the coordinator without persistence.
func LiveDiscoveryDetect(ctx context.Context, ip string, timeout int) (*agent.PrinterInfo, error) {
	return scannerReadPrinter(ctx, ip, timeout, false)
}
func LiveDiscoveryDeepScan(ctx context.Context, ip string, timeout int) (*agent.PrinterInfo, error) {
	return scannerReadPrinter(ctx, ip, timeout, true)
}
func scannerReadPrinter(ctx context.Context, ip string, timeout int, full bool) (*agent.PrinterInfo, error) {
	runtime, err := requireScanner()
	if err != nil {
		return nil, err
	}
	observation, err := scannerObservation(ip, scanner.SourceManual)
	if err != nil {
		return nil, err
	}
	result, err := runtime.request(ctx, observation, scanner.IntentManual, scanner.WorkOptions{TimeoutSeconds: timeout, FullTimeoutSeconds: timeout, FullDetail: full, SNMPAfterTCPFailure: true, MissingSerialFallback: true}, "", true, nil)
	if err != nil {
		return nil, err
	}
	pi, err := scannerPrinterInfo(result)
	return &pi, err
}
func CollectMetrics(ctx context.Context, ip, serial, vendor string, timeout int) (*agent.DeviceMetricsSnapshot, error) {
	return CollectMetricsWithOIDs(ctx, ip, serial, vendor, timeout, nil)
}

// Read-only metrics: callers save once, wake only after successful save.
func CollectMetricsWithOIDs(ctx context.Context, ip, serial, vendor string, timeout int, learned *agent.LearnedOIDMap) (*agent.DeviceMetricsSnapshot, error) {
	runtime, err := requireScanner()
	if err != nil {
		return nil, err
	}
	observation, err := scannerObservation(ip, scanner.SourceManual)
	if err != nil {
		return nil, err
	}
	options := scanner.WorkOptions{TimeoutSeconds: timeout, VendorHint: vendor, SNMPAfterTCPFailure: true}
	var fields []scanner.MetricField
	if learned != nil {
		options.LearnedSerialOID = learned.SerialOID
		for _, field := range []scanner.MetricField{{Name: "page_count", OID: learned.PageCountOID}, {Name: "mono_pages", OID: learned.MonoPagesOID}, {Name: "color_pages", OID: learned.ColorPagesOID},
			{Name: "toner_cyan", OID: learned.CyanOID, Kind: scanner.MetricGauge},
			{Name: "toner_magenta", OID: learned.MagentaOID, Kind: scanner.MetricGauge},
			{Name: "toner_yellow", OID: learned.YellowOID, Kind: scanner.MetricGauge}} {
			if field.OID != "" {
				fields = append(fields, field)
			}
		}
	}
	result, err := runtime.request(ctx, observation, scanner.IntentMetrics, options, serial, true, fields)
	if err != nil {
		return nil, err
	}
	pi, err := scannerPrinterInfo(result)
	if err != nil {
		return nil, err
	}
	return scannerAgentMetrics(pi), nil
}
func scannerAgentMetrics(pi agent.PrinterInfo) *agent.DeviceMetricsSnapshot {
	snapshot := &agent.DeviceMetricsSnapshot{Serial: pi.Serial, TonerLevels: make(map[string]interface{})}
	if pi.PageCount > 0 {
		snapshot.PageCount = pi.PageCount
	}
	if pi.MonoImpressions > 0 {
		snapshot.MonoPages = pi.MonoImpressions
	}
	if pi.ColorImpressions > 0 {
		snapshot.ColorPages = pi.ColorImpressions
	}
	// Vendor meters win only when positive: an absent counter is not a reset.
	meter := func(dst *int, keys ...string) {
		for _, key := range keys {
			if v := pi.Meters[key]; v > 0 {
				*dst = v
				return
			}
		}
	}
	meter(&snapshot.PageCount, "total_pages")
	meter(&snapshot.MonoPages, "mono_pages")
	meter(&snapshot.ColorPages, "color_pages")
	meter(&snapshot.ScanCount, "scans")
	meter(&snapshot.CopyPages, "copy_pages", "copies")
	meter(&snapshot.FaxPages, "fax_pages", "faxes")
	meter(&snapshot.JamEvents, "jams")
	// Mono devices often report zero-valued color supplies; record only black.
	isMono := pi.IsMono || (!pi.IsColor && pi.TonerLevelBlack > 0 &&
		pi.TonerLevelCyan == 0 && pi.TonerLevelMagenta == 0 && pi.TonerLevelYellow == 0)
	toner := func(key string, level int, desc string) {
		if level >= 0 && (desc != "" || level > 0) {
			snapshot.TonerLevels[key] = level
		}
	}
	toner("black", pi.TonerLevelBlack, pi.TonerDescBlack)
	if !isMono {
		toner("cyan", pi.TonerLevelCyan, pi.TonerDescCyan)
		toner("magenta", pi.TonerLevelMagenta, pi.TonerDescMagenta)
		toner("yellow", pi.TonerLevelYellow, pi.TonerDescYellow)
	}
	return snapshot
}

// scannerStorageMetrics is the single agent→storage metrics conversion.
func scannerStorageMetrics(a *agent.DeviceMetricsSnapshot, at time.Time) *storage.MetricsSnapshot {
	s := &storage.MetricsSnapshot{}
	s.Serial, s.Timestamp = a.Serial, at
	s.PageCount, s.ColorPages, s.MonoPages, s.ScanCount, s.TonerLevels = a.PageCount, a.ColorPages, a.MonoPages, a.ScanCount, a.TonerLevels
	s.FaxPages, s.CopyPages, s.OtherPages, s.CopyMonoPages = a.FaxPages, a.CopyPages, a.OtherPages, a.CopyMonoPages
	s.CopyFlatbedScans, s.CopyADFScans, s.FaxFlatbedScans, s.FaxADFScans = a.CopyFlatbedScans, a.CopyADFScans, a.FaxFlatbedScans, a.FaxADFScans
	s.ScanToHostFlatbed, s.ScanToHostADF, s.DuplexSheets = a.ScanToHostFlatbed, a.ScanToHostADF, a.DuplexSheets
	s.JamEvents, s.ScannerJamEvents = a.JamEvents, a.ScannerJamEvents
	return s
}
