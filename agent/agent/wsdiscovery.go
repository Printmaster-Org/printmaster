package agent

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/netip"
	"printmaster/agent/scanner"
	"strings"
	"time"
)

// WS-Discovery constants
const (
	wsDiscoveryMulticastAddr = "239.255.255.250:3702"
)

// WS-Discovery SOAP envelope structures
type wsDiscoveryEnvelope struct {
	XMLName xml.Name `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Header  wsDiscoveryHeader
	Body    wsDiscoveryBody
}

type wsDiscoveryHeader struct {
	Action    string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing Action"`
	MessageID string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing MessageID"`
	To        string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing To"`
}

type wsDiscoveryBody struct {
	Hello      *wsHello      `xml:"http://schemas.xmlsoap.org/ws/2005/04/discovery Hello,omitempty"`
	Bye        *wsBye        `xml:"http://schemas.xmlsoap.org/ws/2005/04/discovery Bye,omitempty"`
	ProbeMatch *wsProbeMatch `xml:"http://schemas.xmlsoap.org/ws/2005/04/discovery ProbeMatch,omitempty"`
}

type wsHello struct {
	EndpointReference wsEndpointReference
	Types             string `xml:"Types"`
	Scopes            string `xml:"Scopes,omitempty"`
	XAddrs            string `xml:"XAddrs"`
	MetadataVersion   int    `xml:"MetadataVersion"`
}

type wsBye struct {
	EndpointReference wsEndpointReference
}

type wsProbeMatch struct {
	EndpointReference wsEndpointReference
	Types             string `xml:"Types"`
	Scopes            string `xml:"Scopes,omitempty"`
	XAddrs            string `xml:"XAddrs"`
	MetadataVersion   int    `xml:"MetadataVersion"`
}

type wsEndpointReference struct {
	Address string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing Address"`
}

// StartWSDiscoveryObservationBrowser emits Hello and ProbeMatch observations,
// retaining scopes and advertised URLs without asserting printer identity.
func StartWSDiscoveryObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback) {
	browseSourceMulticast(ctx, "WS-Discovery", wsDiscoveryMulticastAddr, submit, report, wsdObservations,
		func() error { return sendSourceDatagram(ctx, wsDiscoveryMulticastAddr, wsProbeMessage()) }, 0)
}

// Separate decoder leaves the historical envelope helper's semantics intact.
// Both discovery namespaces occur in deployed printers. Recognized messages
// must be direct Body children (or matches inside a ProbeMatches child).
type wsdObservationEnvelope struct {
	XMLName xml.Name `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Body    struct {
		Messages []wsdObservationMessage `xml:",any"`
	} `xml:"http://www.w3.org/2003/05/soap-envelope Body"`
}
type wsdObservationMessage struct {
	XMLName           xml.Name
	EndpointReference struct {
		Address string `xml:"Address"`
	} `xml:"EndpointReference"`
	Types   string                  `xml:"Types"`
	Scopes  string                  `xml:"Scopes"`
	XAddrs  string                  `xml:"XAddrs"`
	Matches []wsdObservationMessage `xml:"ProbeMatch"`
}

func wsdNamespace(ns string) bool {
	return ns == "http://schemas.xmlsoap.org/ws/2005/04/discovery" || ns == "http://docs.oasis-open.org/ws-dd/ns/discovery/2009/01"
}

func wsdObservations(data []byte, src *net.UDPAddr) []scanner.Observation {
	var envelope wsdObservationEnvelope
	if err := xml.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	var out []scanner.Observation
	add := func(message wsdObservationMessage) {
		if !wsdNamespace(message.XMLName.Space) || (message.XMLName.Local != "Hello" && message.XMLName.Local != "ProbeMatch") {
			return
		}
		h := scanner.WSDHint{Endpoint: message.EndpointReference.Address, Types: message.Types, Scopes: message.Scopes,
			XAddr: message.XAddrs, Sender: senderIPv4(src), Message: message.XMLName.Local}
		var ips []netip.Addr
		for _, raw := range strings.Fields(message.XAddrs) {
			ips = append(ips, sourceURLIPv4(raw))
		}
		ips = uniqueSourceTargets(ips...)
		// Explicit fallback only for recognized discovery messages, never Bye,
		// malformed XML, unrelated SOAP bodies, or empty ProbeMatches wrappers.
		if len(ips) == 0 {
			ips = uniqueSourceTargets(h.Sender)
		}
		for _, ip := range ips {
			out = append(out, scanner.Observation{IP: ip, Source: scanner.SourceWSD, Hints: scanner.ProtocolHints{WSD: h}})
		}
	}
	for _, message := range envelope.Body.Messages {
		if message.XMLName.Local == "ProbeMatches" && wsdNamespace(message.XMLName.Space) {
			for _, match := range message.Matches {
				if match.XMLName.Space == message.XMLName.Space {
					add(match)
				}
			}
		} else {
			add(message)
		}
	}
	return out
}

func wsdReceiptCredible(o scanner.Observation) bool {
	h := o.Hints.WSD
	if h.Sender != o.IP || h.Message != "ProbeMatch" || h.Endpoint == "" || h.Types == "" {
		return false
	}
	addresses := strings.Fields(h.XAddr)
	if len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if !sourceURLValid(address) {
			return false
		}
	}
	return true
}

func wsProbeMessage() string {
	return `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:wsdp="http://schemas.xmlsoap.org/ws/2006/02/devprof">
<soap:Header><wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</wsa:Action><wsa:MessageID>urn:uuid:` + generateUUID() + `</wsa:MessageID><wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To></soap:Header>
<soap:Body><wsd:Probe><wsd:Types>wsdp:Device</wsd:Types></wsd:Probe></soap:Body></soap:Envelope>`
}

// sendProbe sends a WS-Discovery Probe message to discover existing devices
func sendProbe() {
	reportSourceError(logSourceError, sendSourceDatagram(context.Background(), wsDiscoveryMulticastAddr, wsProbeMessage()))
}

// extractIPsFromXAddrs parses XAddrs field (space-separated URLs) and extracts IPv4 addresses
func extractIPsFromXAddrs(xaddrs string) []string {
	var ips []string
	urls := strings.Fields(xaddrs)
	for _, url := range urls {
		// XAddrs typically contains URLs like http://192.168.1.100:5357/
		// Extract IP using simple parsing
		url = strings.TrimPrefix(url, "http://")
		url = strings.TrimPrefix(url, "https://")
		if idx := strings.Index(url, ":"); idx > 0 {
			url = url[:idx]
		}
		if idx := strings.Index(url, "/"); idx > 0 {
			url = url[:idx]
		}
		// Validate it's an IP
		if ip := net.ParseIP(url); ip != nil && ip.To4() != nil {
			ips = append(ips, ip.To4().String())
		}
	}
	return ips
}

// generateUUID creates a simple UUID for WS-Discovery messages
func generateUUID() string {
	// Simple UUID generation for message IDs
	return fmt.Sprintf("%d-%d-%d-%d-%d",
		time.Now().UnixNano()%100000,
		time.Now().UnixNano()%10000,
		time.Now().UnixNano()%1000,
		time.Now().UnixNano()%100,
		time.Now().UnixNano()%10)
}
