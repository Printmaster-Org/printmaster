package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"printmaster/agent/scanner"
	"strings"
	"syscall"
	"time"

	"github.com/gosnmp/gosnmp"
)

// StartSNMPTrapObservationListener retains PDUs from eligible IPv4 traps.
// Printer-MIB OIDs or known vendor OIDs admit work, never establish identity.
// Listen errors are returned; the browser API reports/retries them.
func StartSNMPTrapObservationListener(ctx context.Context, submit ObservationCallback, port uint16) error {
	if ctx.Err() != nil {
		return nil
	}
	if port == 0 {
		port = 162
	}
	tl := gosnmp.NewTrapListener()
	// A fresh client avoids mutating defaults or copying GoSNMP's internal lock.
	tl.Params = &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "public", Context: ctx}
	tl.OnNewTrap = func(packet *gosnmp.SnmpPacket, addr *net.UDPAddr) {
		submitTrapObservation(ctx, packet, addr, submit)
	}
	return runTrapObservationListener(ctx, tl, fmt.Sprintf("0.0.0.0:%d", port))
}

func submitTrapObservation(ctx context.Context, packet *gosnmp.SnmpPacket, addr *net.UDPAddr, submit ObservationCallback) {
	receivedAt := time.Now() // Local GoSNMP callback receipt, before copying PDUs.
	if ctx.Err() != nil || submit == nil {
		return
	}
	if o, ok := trapObservation(packet, addr); ok && o.Hints.Trap.Eligibility != "unknown" {
		if ctx.Err() == nil {
			submit(sourceReceipt(o, receivedAt))
		}
	}
}

type sourceTrapListener interface {
	Listen(string) error
	Listening() <-chan bool
	Close()
}

func runTrapObservationListener(ctx context.Context, listener sourceTrapListener, address string) error {
	if ctx.Err() != nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- listener.Listen(address) }()
	// Close must not race socket initialization: readiness publishes the socket.
	select {
	case err := <-done:
		return err
	case <-listener.Listening():
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		listener.Close()
		<-done
		return nil
	}
}

func oidUnder(oid, root string) bool { return oid == root || strings.HasPrefix(oid, root+".") }

func sourceOIDValid(oid string) bool {
	parts := strings.Split(strings.TrimPrefix(oid, "."), ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

func trapReceiptCredible(o scanner.Observation) bool {
	h := o.Hints.Trap
	if h.Sender != o.IP || h.Packet == nil || (h.Eligibility != "printer-oid" && h.Eligibility != "vendor-hint") {
		return false
	}
	eligible := func(oid string) bool {
		oid = strings.TrimPrefix(oid, ".")
		return sourceOIDValid(oid) && ((h.Eligibility == "printer-oid" && oidUnder(oid, "1.3.6.1.2.1.43")) || (h.Eligibility == "vendor-hint" && trapVendor(oid) != ""))
	}
	matched := eligible(h.TrapOID) || eligible(h.EnterpriseOID)
	for _, pdu := range h.Packet.PDUs {
		matched = matched || eligible(pdu.Name)
	}
	if !matched {
		return false
	}
	p := h.Packet
	if p.PDUType == gosnmp.Trap {
		if p.Version != gosnmp.Version1 || !sourceOIDValid(h.EnterpriseOID) || p.GenericTrap < 0 || p.GenericTrap > 6 || p.SpecificTrap < 0 {
			return false
		}
		oid := fmt.Sprintf("1.3.6.1.6.3.1.1.5.%d", p.GenericTrap+1)
		if p.GenericTrap == 6 {
			oid = fmt.Sprintf("%s.0.%d", h.EnterpriseOID, p.SpecificTrap)
		}
		return h.TrapOID == oid
	}
	if (p.PDUType != gosnmp.SNMPv2Trap && p.PDUType != gosnmp.InformRequest) || (p.Version != gosnmp.Version2c && p.Version != gosnmp.Version3) {
		return false
	}
	for _, pdu := range p.PDUs {
		if strings.TrimPrefix(pdu.Name, ".") == "1.3.6.1.6.3.1.1.4.1.0" && pdu.Type == gosnmp.ObjectIdentifier {
			if oid, ok := pdu.Value.(string); ok && sourceOIDValid(oid) && strings.TrimPrefix(oid, ".") == h.TrapOID {
				return true
			}
		}
	}
	return false
}

func trapVendor(oid string) string {
	for _, vendor := range []struct{ root, name string }{
		{"1.3.6.1.4.1.11", "hp"}, {"1.3.6.1.4.1.1602", "canon"}, {"1.3.6.1.4.1.1248", "epson"},
		{"1.3.6.1.4.1.2435", "brother"}, {"1.3.6.1.4.1.253", "xerox"}, {"1.3.6.1.4.1.367", "ricoh"},
		{"1.3.6.1.4.1.1347", "kyocera"}, {"1.3.6.1.4.1.641", "lexmark"},
	} {
		if oidUnder(oid, vendor.root) {
			return vendor.name
		}
	}
	return ""
}

func trapObservation(packet *gosnmp.SnmpPacket, addr *net.UDPAddr) (scanner.Observation, bool) {
	ip := senderIPv4(addr)
	if packet == nil || len(uniqueSourceTargets(ip)) == 0 {
		return scanner.Observation{}, false
	}
	if packet.PDUType != gosnmp.Trap && packet.PDUType != gosnmp.SNMPv2Trap && packet.PDUType != gosnmp.InformRequest {
		return scanner.Observation{}, false
	}
	h := scanner.TrapHint{Sender: ip, EnterpriseOID: strings.TrimPrefix(packet.Enterprise, "."), Eligibility: "unknown",
		Packet: &scanner.TrapPacket{GenericTrap: packet.GenericTrap, SpecificTrap: packet.SpecificTrap, Version: packet.Version, PDUType: packet.PDUType}}
	for _, pdu := range packet.Variables {
		copyPDU := pdu
		if bytes, ok := pdu.Value.([]byte); ok {
			copyPDU.Value = append([]byte(nil), bytes...)
		}
		if values, ok := pdu.Value.([]int); ok {
			copyPDU.Value = append([]int(nil), values...)
		}
		h.Packet.PDUs = append(h.Packet.PDUs, copyPDU)
		name := strings.TrimPrefix(pdu.Name, ".")
		if name == "1.3.6.1.6.3.1.1.4.1.0" {
			if value, ok := pdu.Value.(string); ok {
				h.TrapOID = strings.TrimPrefix(value, ".")
			}
		}
		if oidUnder(name, "1.3.6.1.2.1.43") {
			h.Eligibility = "printer-oid"
		}
		if h.Vendor == "" {
			h.Vendor = trapVendor(name)
		}
	}
	// SNMPv1 carries the notification identifier in the header rather than a
	// snmpTrapOID varbind. Map it per RFC 3584, without assuming printer status.
	if h.TrapOID == "" && packet.PDUType == gosnmp.Trap {
		if packet.GenericTrap >= 0 && packet.GenericTrap <= 5 {
			h.TrapOID = fmt.Sprintf("1.3.6.1.6.3.1.1.5.%d", packet.GenericTrap+1)
		} else if packet.GenericTrap == 6 && h.EnterpriseOID != "" {
			h.TrapOID = fmt.Sprintf("%s.0.%d", h.EnterpriseOID, packet.SpecificTrap)
		}
	}
	if oidUnder(h.TrapOID, "1.3.6.1.2.1.43") || oidUnder(h.EnterpriseOID, "1.3.6.1.2.1.43") {
		h.Eligibility = "printer-oid"
	}
	if h.Vendor == "" {
		h.Vendor = trapVendor(h.TrapOID)
	}
	if h.Vendor == "" {
		h.Vendor = trapVendor(h.EnterpriseOID)
	}
	if h.Vendor != "" && h.Eligibility == "unknown" {
		h.Eligibility = "vendor-hint"
	}
	return scanner.Observation{IP: ip, Source: scanner.SourceTrap, Hints: scanner.ProtocolHints{Trap: h}}, true
}

// StartSNMPTrapObservationBrowser retries non-permission listener errors with
// cancellable delay. Admission and deduplication belong to the callback owner.
func StartSNMPTrapObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback) {
	runTrapObservationBrowser(ctx, submit, report, StartSNMPTrapObservationListener)
}

func runTrapObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback,
	listen func(context.Context, ObservationCallback, uint16) error) {
	for ctx.Err() == nil {
		err := listen(ctx, submit, 162)
		if ctx.Err() != nil {
			return
		}
		reportSourceError(report, err)
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return
		}
		if !waitSourceDelay(ctx, 30*time.Second) {
			return
		}
	}
}
