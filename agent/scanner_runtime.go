package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"printmaster/agent/agent"
	"printmaster/agent/scanner"
	"printmaster/agent/storage"
)

// scannerRuntime owns the sole production coordinator.
type scannerRuntime struct {
	coordinator *scanner.Coordinator
	store       storage.DeviceStore
	mu          sync.RWMutex
	known       map[netip.Addr]*storage.Device
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	parsePDUs   func(string, []gosnmp.SnmpPDU, *agent.ScanMeta, func(string)) (agent.PrinterInfo, bool)
	closing     bool
	OnCommitted func(agent.PrinterInfo, bool)
}

var mainScanner *scannerRuntime

func newScannerRuntime(ctx context.Context, store storage.DeviceStore, backend scanner.Backend) (*scannerRuntime, error) {
	r := &scannerRuntime{store: store, known: make(map[netip.Addr]*storage.Device), parsePDUs: agent.ParsePDUsWithoutNetwork}
	r.ctx, r.cancel = context.WithCancel(ctx)
	if store != nil {
		if err := r.refreshIndex(ctx); err != nil {
			r.cancel()
			return nil, err
		}
	}
	config := scanner.CoordinatorConfig{Backend: backend, Commit: r.commit, Wake: scannerUploadWake}
	if appLogger != nil {
		config.Logger = appLogger
	}
	c, err := scanner.NewCoordinator(config)
	if err != nil {
		r.cancel()
		return nil, err
	}
	r.coordinator = c
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				if err := r.refreshIndex(r.ctx); err != nil && r.ctx.Err() == nil && appLogger != nil {
					appLogger.Warn("Scanner inventory refresh failed", "error", err.Error())
				}
			}
		}
	}()
	return r, nil
}

func (r *scannerRuntime) Close() {
	r.mu.Lock()
	r.closing = true
	r.cancel()
	r.mu.Unlock()
	r.coordinator.Close()
	r.wg.Wait()
}

func (r *scannerRuntime) launch(work func()) bool {
	r.mu.Lock()
	if r.closing || r.ctx.Err() != nil {
		r.mu.Unlock()
		return false
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() { defer r.wg.Done(); work() }()
	return true
}

func (r *scannerRuntime) refreshIndex(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	devices, err := r.store.List(ctx, storage.DeviceFilter{})
	if err != nil {
		return err
	}
	index := make(map[netip.Addr]*storage.Device)
	ambiguous := make(map[netip.Addr]bool)
	for _, d := range devices {
		if d == nil || d.IsUSB || d.Serial == "" {
			continue
		}
		ip, err := netip.ParseAddr(d.IP)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if ambiguous[ip] {
			continue
		}
		if previous := index[ip]; previous != nil && previous.Serial != d.Serial {
			// Several rows share this address: no single row's hints apply.
			ambiguous[ip] = true
			delete(index, ip)
		} else {
			index[ip] = d
		}
	}
	r.known = index
	return nil
}

func (r *scannerRuntime) request(ctx context.Context, observation scanner.Observation, preset scanner.IntentPreset, options scanner.WorkOptions, serial string, readOnly bool, metrics []scanner.MetricField) (scanner.Result, error) {
	return r.requestWithSource(ctx, observation, preset, options, serial, readOnly, metrics, false)
}

// RequestSource is reserved for actual adapter callbacks. The callback records
// local receipt before lookup/enqueue; runtime never fabricates that evidence.
func (r *scannerRuntime) RequestSource(ctx context.Context, observation scanner.Observation, preset scanner.IntentPreset, options scanner.WorkOptions) (scanner.Result, error) {
	return r.requestWithSource(ctx, observation, preset, options, "", false, nil, true)
}

func (r *scannerRuntime) requestWithSource(ctx context.Context, observation scanner.Observation, preset scanner.IntentPreset, options scanner.WorkOptions, serial string, readOnly bool, metrics []scanner.MetricField, sourceReceipt bool) (scanner.Result, error) {
	if err := r.ctx.Err(); err != nil {
		return scanner.Result{}, err
	}
	if err := scannerIPScanningAllowed(); err != nil {
		return scanner.Result{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer func() { stop(); cancel() }()
	observation.IP = observation.IP.Unmap()
	// The index only supplies query hints (learned serial OID, vendor). It is never
	// an expected identity: DHCP reuse would otherwise block the new printer.
	r.mu.RLock()
	known := r.known[observation.IP]
	r.mu.RUnlock()
	if serial != "" {
		observation.Known = scanner.KnownDeviceHint{Serial: serial, IP: observation.IP}
	}
	base := options
	base.ReadOnly = readOnly
	hinted := base
	if known != nil {
		pi := storage.DeviceToPrinterInfo(known)
		if hinted.LearnedSerialOID == "" {
			hinted.LearnedSerialOID = pi.LearnedOIDs.SerialOID
		}
		if hinted.VendorHint == "" {
			hinted.VendorHint = known.Manufacturer
		}
	}
	if appLogger != nil {
		appLogger.Debug("Scanner intake", "ip", observation.IP.String(), "source", observation.Source, "preset", preset, "inventory_hint", known != nil, "expected_serial", serial != "")
	}
	do := func(o scanner.WorkOptions) (scanner.Result, error) {
		req := scanner.Request{Observation: observation, Preset: preset, Options: o, Metrics: metrics}
		if sourceReceipt {
			return r.coordinator.DoSource(ctx, req)
		}
		return r.coordinator.Do(ctx, req)
	}
	result, err := do(hinted)
	var outcome *scanner.OutcomeError
	if serial == "" && (hinted.LearnedSerialOID != base.LearnedSerialOID || hinted.VendorHint != base.VendorHint) &&
		errors.As(err, &outcome) && outcome.Outcome.Reason == scanner.ReasonIdentityConflict && ctx.Err() == nil {
		// A learned OID from a previous occupant of this address can contradict the
		// standard serial. Retry once with standard identity OIDs only.
		if appLogger != nil {
			appLogger.Info("Scanner retrying without stale inventory hints", "ip", observation.IP.String())
		}
		return do(base)
	}
	return result, err
}

var errIPScanningDisabled = errors.New("ip scanning is disabled in agent settings")

// scannerIPScanningAllowed applies the master network-scanning toggle to every network request.
func scannerIPScanningAllowed() error {
	if agentConfigStore == nil {
		return nil
	}
	var settings map[string]interface{}
	if agentConfigStore.GetConfigValue("discovery_settings", &settings) == nil && settings["ip_scanning_enabled"] == false {
		return errIPScanningDisabled
	}
	return nil
}

func scannerObservation(ip string, source scanner.ObservationSource) (scanner.Observation, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return scanner.Observation{}, err
	}
	return scanner.Observation{IP: addr.Unmap(), Source: source}, nil
}

func requireScanner() (*scannerRuntime, error) {
	if mainScanner == nil {
		return nil, errors.New("scanner runtime unavailable")
	}
	return mainScanner, nil
}

func scannerUploadWake() {
	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()
	if worker != nil {
		worker.Wake()
	}
}

// Pure request-local conversion: no HTTP/file side effects, cache, or JSON
// coercion. Identity/liveness skip enrichment parsing altogether.
func scannerPrinterInfo(result scanner.Result) (agent.PrinterInfo, error) {
	parse := agent.ParsePDUsWithoutNetwork
	if mainScanner != nil && mainScanner.parsePDUs != nil {
		parse = mainScanner.parsePDUs
	}
	return convertScannerResult(result, parse)
}

func scannerMetricsObtained(result scanner.Result) bool {
	o := result.Outcomes[scanner.StageMetrics]
	return o.Status == scanner.StatusSucceeded || o.Status == scanner.StatusSkipped && o.Reason == scanner.ReasonFreshItemMetrics
}

func scannerEnrichmentPDUs(result scanner.Result) ([]gosnmp.SnmpPDU, string) {
	var pdus []gosnmp.SnmpPDU
	var vendorHint string
	for _, q := range result.Queries {
		if q.Err != nil || q.Result == nil {
			continue
		}
		if q.Result.Profile == scanner.QueryMinimal && !(scannerMetricsObtained(result) && (result.Intent.MetricsRequested || result.Intent.MetricsDue)) {
			continue
		}
		if q.Stage == scanner.StageDetail && result.Outcomes[scanner.StageDetail].Status != scanner.StatusSucceeded {
			continue
		}
		if q.Stage == scanner.StageMetrics && !scannerMetricsObtained(result) {
			continue
		}
		pdus = append(pdus, q.Result.PDUs...)
		if q.Result.VendorHint != "" {
			vendorHint = q.Result.VendorHint
		}
	}
	return pdus, vendorHint
}

func scannerDiscoveryMethod(result scanner.Result) string {
	switch result.Observation.Source {
	case scanner.SourceRange:
		if result.Intent.Detail {
			return "full-discovery"
		}
		return "quick-discovery"
	case scanner.SourceMDNS:
		return "mdns"
	case scanner.SourceSSDP:
		return "ssdp"
	case scanner.SourceWSD:
		return "ws-discovery"
	case scanner.SourceTrap:
		return "snmp-trap"
	case scanner.SourceLLMNR:
		return "llmnr"
	case scanner.SourceManual:
		return "manual"
	default:
		return ""
	}
}

func convertScannerResult(result scanner.Result, parse func(string, []gosnmp.SnmpPDU, *agent.ScanMeta, func(string)) (agent.PrinterInfo, bool)) (agent.PrinterInfo, error) {
	if result.Serial == "" || result.Outcomes[scanner.StageIdentity].Status != scanner.StatusSucceeded {
		return agent.PrinterInfo{}, errors.New("validated scanner identity required")
	}
	var at time.Time
	for _, q := range result.Queries {
		if q.Err != nil || q.Result == nil {
			continue
		}
		if q.ReceivedAt.After(at) {
			at = q.ReceivedAt
		}
	}
	if at.IsZero() {
		return agent.PrinterInfo{}, errors.New("scanner receipt required")
	}
	pdus, vendorHint := scannerEnrichmentPDUs(result)
	pi := agent.PrinterInfo{Manufacturer: result.Manufacturer, Model: result.Model}
	if len(pdus) > 0 {
		pi, _ = parse(result.Observation.IP.String(), pdus, &agent.ScanMeta{}, nil)
		agent.MergeVendorMetrics(&pi, pdus, vendorHint)
	}
	pi.IP, pi.Serial, pi.LastSeen = result.Observation.IP.String(), result.Serial, at
	if method := scannerDiscoveryMethod(result); method != "" {
		pi.DiscoveryMethods = []string{method}
	}
	for _, port := range result.OpenPorts {
		pi.OpenPorts = append(pi.OpenPorts, int(port))
	}
	if pi.Model == "" {
		pi.Model = result.Model
	}
	if pi.Manufacturer == "" {
		pi.Manufacturer = result.Manufacturer
	}
	switch h := result.Observation.Hints; result.Observation.Source {
	case scanner.SourceMDNS:
		if h.MDNS.Service != "" {
			pi.AdvertisedServices = []string{h.MDNS.Service}
		}
		if pi.Hostname == "" {
			pi.Hostname = strings.TrimSuffix(h.MDNS.Hostname, ".")
		}
	case scanner.SourceLLMNR:
		if pi.Hostname == "" {
			pi.Hostname = h.LLMNR.Hostname
		}
	}
	// Requested/learned fields may live outside the parser's OID tables; they only
	// fill gaps so parser/vendor totals (e.g. multi-marker sums) are never replaced.
	fill := func(dst *int, value int) {
		if *dst == 0 {
			*dst = value
		}
	}
	for name, prov := range result.Fields {
		for _, pdu := range pdus {
			if strings.TrimPrefix(pdu.Name, ".") != strings.TrimPrefix(prov.Field, ".") {
				continue
			}
			value, err := strconv.Atoi(fmt.Sprint(pdu.Value))
			if err != nil || value < 0 {
				continue
			}
			switch name {
			case "page_count":
				fill(&pi.PageCount, value)
			case "mono_pages":
				fill(&pi.MonoImpressions, value)
			case "color_pages":
				fill(&pi.ColorImpressions, value)
			case "toner_cyan", "toner_magenta", "toner_yellow":
				if pi.TonerLevels == nil {
					pi.TonerLevels = make(map[string]int)
				}
				if _, ok := pi.TonerLevels[strings.TrimPrefix(name, "toner_")]; !ok {
					pi.TonerLevels[strings.TrimPrefix(name, "toner_")] = value
				}
			}
		}
	}
	for _, q := range result.Queries {
		if q.Err != nil || q.Result == nil || q.Result.Capabilities == nil {
			continue
		}
		caps := q.Result.Capabilities
		pi.IsColor, pi.IsMono, pi.IsCopier, pi.IsScanner, pi.IsFax = caps.IsColor, caps.IsMono, caps.IsCopier, caps.IsScanner, caps.IsFax
		pi.IsLaser, pi.IsInkjet, pi.HasDuplex, pi.FormFactor, pi.DeviceType = caps.IsLaser, caps.IsInkjet, caps.HasDuplex, caps.FormFactor, caps.DeviceType
	}
	return pi, nil
}

func (r *scannerRuntime) commit(ctx context.Context, result scanner.Result) error {
	store, ok := r.store.(storage.StageCommitStore)
	if !ok {
		return errors.New("staged scanner storage unavailable")
	}
	pi, err := convertScannerResult(result, r.parsePDUs)
	if err != nil {
		return err
	}
	patch := storage.DevicePatch{ValidatedSerial: &pi.Serial, IP: &pi.IP, LastSeen: &pi.LastSeen}
	if pi.Manufacturer != "" {
		patch.Manufacturer = &pi.Manufacturer
	}
	if pi.Model != "" {
		patch.Model = &pi.Model
	}
	obtained, _ := scannerEnrichmentPDUs(result)
	// Minimal identity/liveness does not synthesize scan metadata or snapshots.
	var scan *storage.ScanSnapshot
	if len(obtained) > 0 {
		// Same normalized keys the legacy upsert wrote, so DeviceToPrinterInfo readers
		// keep working; merge (not replace) preserves user-learned OIDs.
		raw := storage.PrinterInfoToDevice(pi, false).RawData
		if evidence := scannerSourceEvidence(result.Observation); evidence != nil {
			raw["discovery_evidence"] = evidence
		}
		patch.RawData = &raw
		method := scannerDiscoveryMethod(result)
		patch.DiscoveryMethod = &method
		patch.Manufacturer, patch.Model, patch.Hostname, patch.Firmware = &pi.Manufacturer, &pi.Model, &pi.Hostname, &pi.Firmware
		patch.MACAddress, patch.SubnetMask, patch.Gateway, patch.DHCPServer = &pi.MAC, &pi.SubnetMask, &pi.Gateway, &pi.DHCPServer
		patch.DNSServers, patch.Consumables, patch.StatusMessages = &pi.DNSServers, &pi.Consumables, &pi.StatusMessages
		if result.Intent.Detail && result.Outcomes[scanner.StageDetail].Status == scanner.StatusSucceeded {
			scan = storage.PrinterInfoToScanSnapshot(pi)
		}
	} else if pi.Hostname != "" {
		patch.Hostname = &pi.Hostname
	}
	var metrics *storage.MetricsSnapshot
	if (result.Intent.MetricsRequested || result.Intent.MetricsDue) && scannerMetricsObtained(result) {
		metrics = storage.PrinterInfoToMetricsSnapshot(pi)
		metrics.Timestamp = pi.LastSeen
	}
	existing, getErr := r.store.Get(ctx, pi.Serial)
	if getErr != nil && !errors.Is(getErr, storage.ErrNotFound) {
		return getErr
	}
	// Compare-and-set on the stored address: a validated move commits atomically,
	// a concurrent inventory edit makes this commit fail rather than overwrite it.
	guard := pi.IP
	if existing != nil {
		guard = existing.IP
	}
	if err := store.CommitScannerFacts(ctx, pi.Serial, guard, patch, scan, metrics); err != nil {
		return err
	}
	if err := r.refreshIndex(ctx); err != nil && appLogger != nil {
		appLogger.Warn("Scanner inventory refresh after commit failed", "error", err.Error())
	}
	if r.OnCommitted != nil {
		r.OnCommitted(pi, existing == nil)
	}
	if appLogger != nil {
		appLogger.Info("Scanner facts persisted", "ip", pi.IP, "new_device", existing == nil, "moved", existing != nil && existing.IP != pi.IP,
			"enriched", len(obtained) > 0, "scan", scan != nil, "metrics", metrics != nil)
	}
	return nil
}

// scannerSourceEvidence keeps the source's descriptive hint, not raw packets.
func scannerSourceEvidence(o scanner.Observation) map[string]interface{} {
	h := o.Hints
	switch o.Source {
	case scanner.SourceMDNS:
		if h.MDNS.Service == "" {
			return nil
		}
		evidence := map[string]interface{}{"source": "mdns", "service": h.MDNS.Service, "instance": h.MDNS.Instance, "hostname": h.MDNS.Hostname}
		if h.MDNS.Record != nil {
			evidence["port"], evidence["txt"] = h.MDNS.Record.Port, h.MDNS.Record.TXT
		}
		return evidence
	case scanner.SourceSSDP:
		return map[string]interface{}{"source": "ssdp", "usn": h.SSDP.USN, "search_target": h.SSDP.SearchTarget, "notification_type": h.SSDP.NotificationType, "location": h.SSDP.Location}
	case scanner.SourceWSD:
		return map[string]interface{}{"source": "ws-discovery", "endpoint": h.WSD.Endpoint, "types": h.WSD.Types, "scopes": h.WSD.Scopes, "xaddrs": h.WSD.XAddr}
	case scanner.SourceTrap:
		return map[string]interface{}{"source": "snmp-trap", "trap_oid": h.Trap.TrapOID, "enterprise_oid": h.Trap.EnterpriseOID, "vendor": h.Trap.Vendor, "eligibility": h.Trap.Eligibility}
	case scanner.SourceLLMNR:
		return map[string]interface{}{"source": "llmnr", "hostname": h.LLMNR.Hostname}
	}
	return nil
}

func saveScannerMetrics(ctx context.Context, ip string, snapshot *storage.MetricsSnapshot) error {
	if snapshot == nil {
		return errors.New("metrics snapshot required")
	}
	// Runtime-free tests/legacy embedders retain their original storage path;
	// production startup always installs the guarded runtime.
	if mainScanner == nil {
		if deviceStore == nil {
			return errors.New("device store unavailable")
		}
		return deviceStore.SaveMetricsSnapshot(ctx, snapshot)
	}
	runtime, err := requireScanner()
	if err != nil {
		return err
	}
	if err := runtime.ctx.Err(); err != nil {
		return err
	}
	store, ok := runtime.store.(storage.StageCommitStore)
	if !ok {
		return errors.New("staged scanner storage unavailable")
	}
	if err := store.CommitScannerFacts(ctx, snapshot.Serial, ip, storage.DevicePatch{}, nil, snapshot); err != nil {
		return err
	}
	scannerUploadWake()
	return nil
}

func productionScannerBackend() scanner.Backend {
	return scanner.Backend{ProbeWithPorts: func(ctx context.Context, ip netip.Addr, requested []uint16) ([]uint16, error) {
		if len(requested) == 0 {
			requested = []uint16{9100, 80, 443, 515, 631}
		}
		var ports []uint16
		for _, port := range requested {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			dialer := net.Dialer{Timeout: 500 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
			if err == nil {
				conn.Close()
				ports = append(ports, port)
			}
		}
		return ports, ctx.Err()
	}, Query: productionScannerQuery}
}

// Wire adapter only. Profiles, walks and capabilities stay in the shared query.
func productionScannerQuery(ctx context.Context, req scanner.QueryRequest) (*scanner.QueryResult, error) {
	scannerConfig.RLock()
	retries := scannerConfig.SNMPRetries
	scannerConfig.RUnlock()
	if retries < 0 {
		retries = 0
	}
	requested := []string{req.LearnedSerialOID}
	if req.Profile != scanner.QueryMinimal {
		for _, field := range req.Metrics {
			requested = append(requested, field.OID)
		}
	}
	return scanner.QueryDeviceWithContext(ctx, req.IP.String(), req.Profile, req.VendorHint, req.TimeoutSeconds, scanner.ContextQueryOptions{Retries: retries, RequestedOIDs: requested})
}
