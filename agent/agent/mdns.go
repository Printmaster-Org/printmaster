package agent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"printmaster/agent/scanner"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// ObservationCallback synchronously admits raw observations. Return true only
// when accepted. Keep callbacks short (e.g. bounded enqueue); adapters do not
// spawn candidate goroutines, assert trust, or complete scanner stages.
type ObservationCallback func(scanner.Observation) bool

// SourceErrorCallback reports setup/read/send errors; nil discards them.
type SourceErrorCallback func(error)

func reportSourceError(report SourceErrorCallback, err error) {
	if report != nil && err != nil {
		report(err)
	}
}

func logSourceError(err error) {
	if err == nil {
		return
	}
	// Reported errors are local setup/socket failures; decoders never report packet contents.
	WarnCtx("Discovery source failed", "error", err.Error())
}

// Decoders leave ObservedAt zero. Only an actual receive seam supplies time,
// and only target-bound credible messages carry it through to consumers.
// This is not a stage result or authentication; the coordinator revalidates.
func sourceReceipt(o scanner.Observation, at time.Time) scanner.Observation {
	o.ObservedAt = time.Time{}
	credible := false
	switch o.Source {
	case scanner.SourceMDNS:
		h := o.Hints.MDNS
		if (h.Service == "_ipp._tcp" || h.Service == "_ipps._tcp" || h.Service == "_printer._tcp") && h.Instance != "" && h.Hostname != "" && h.Record != nil && h.Record.Port > 0 && h.Record.Port <= 65535 {
			for _, address := range h.Record.Addresses {
				credible = credible || address.Unmap() == o.IP
			}
		}
	case scanner.SourceSSDP:
		credible = ssdpReceiptCredible(o)
	case scanner.SourceWSD:
		credible = wsdReceiptCredible(o)
	case scanner.SourceTrap:
		credible = trapReceiptCredible(o)
	case scanner.SourceLLMNR:
		credible = llmnrReceiptCredible(o)
	}
	if credible && len(uniqueSourceTargets(o.IP)) != 0 {
		o.ObservedAt = at
	}
	return o
}

func sourceIPv4(ip net.IP) netip.Addr {
	if v := ip.To4(); v != nil {
		return netip.AddrFrom4([4]byte{v[0], v[1], v[2], v[3]})
	}
	return netip.Addr{}
}

func senderIPv4(src *net.UDPAddr) netip.Addr {
	if src == nil {
		return netip.Addr{}
	}
	return sourceIPv4(src.IP)
}

func waitSourceDelay(ctx context.Context, delay time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// Local throttle state never touches the legacy caller-owned seen map, which
// may be shared with unrelated workers without a common lock.
func ThrottleObservations(submit ObservationCallback, window time.Duration) ObservationCallback {
	var mu sync.Mutex
	seen := make(map[netip.Addr]time.Time)
	return func(o scanner.Observation) bool {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if at, ok := seen[o.IP]; ok && now.Sub(at) < window {
			return false
		}
		if submit == nil || !submit(o) {
			return false
		}
		seen[o.IP] = time.Now()
		for ip, at := range seen {
			if ip != o.IP && time.Since(at) >= window {
				delete(seen, ip)
			}
		}
		return true
	}
}

func mdnsObservations(service string, e *zeroconf.ServiceEntry) []scanner.Observation {
	if e == nil {
		return nil
	}
	addresses := make([]netip.Addr, 0, len(e.AddrIPv4)+len(e.AddrIPv6))
	for _, ip := range append(append([]net.IP(nil), e.AddrIPv4...), e.AddrIPv6...) {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			addresses = append(addresses, addr.Unmap())
		}
	}
	var out []scanner.Observation
	seen := make(map[netip.Addr]bool)
	for _, ip := range e.AddrIPv4 {
		addr := sourceIPv4(ip)
		if len(uniqueSourceTargets(addr)) == 0 || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, scanner.Observation{IP: addr, Source: scanner.SourceMDNS,
			Hints: scanner.ProtocolHints{MDNS: scanner.MDNSHint{Service: service, Instance: e.Instance, Hostname: e.HostName,
				Record: &scanner.MDNSRecord{Port: e.Port, TXT: append([]string(nil), e.Text...), Addresses: append([]netip.Addr(nil), addresses...)}}}})
	}
	return out
}

type mdnsBrowseFunc func(context.Context, string, chan<- *zeroconf.ServiceEntry) error

type mdnsSourceEvent struct {
	observation scanner.Observation
	err         error
}

// StartMDNSObservationBrowser drains all three service-entry streams before
// returning. All callbacks run on this owner goroutine, never concurrently.
// zeroconf closes entries before its internal socket shutdown; its API exposes
// no handle to join those internal goroutines.
func StartMDNSObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback) {
	runMDNSObservationBrowser(ctx, submit, report, func(ctx context.Context, service string, entries chan<- *zeroconf.ServiceEntry) error {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			close(entries)
			return err
		}
		return resolver.Browse(ctx, service, "local.", entries)
	})
}

// Browse implementations own closing entries, including error paths.
func runMDNSObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback, browse mdnsBrowseFunc) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	observations := make(chan mdnsSourceEvent)
	var wg sync.WaitGroup
	for _, service := range []string{"_ipp._tcp", "_ipps._tcp", "_printer._tcp"} {
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			entries := make(chan *zeroconf.ServiceEntry)
			if err := browse(ctx, service, entries); err != nil {
				select {
				case observations <- mdnsSourceEvent{err: fmt.Errorf("mDNS %s: %w", service, err)}:
				case <-ctx.Done():
				}
			}
			// Drain entries even after cancel: zeroconf sends without selecting
			// on ctx, so abandoning this stream can block its shutdown forever.
			for e := range entries {
				receivedAt := time.Now() // Local entry delivery, before decode/owner queuing.
				for _, o := range mdnsObservations(service, e) {
					select {
					case observations <- mdnsSourceEvent{observation: sourceReceipt(o, receivedAt)}:
					case <-ctx.Done():
					}
				}
			}
		}(service)
	}
	go func() { wg.Wait(); close(observations) }()
	for event := range observations {
		if event.err != nil {
			if ctx.Err() == nil {
				reportSourceError(report, event.err)
			}
			continue
		}
		if ctx.Err() == nil && submit != nil {
			submit(event.observation)
		}
	}
}
