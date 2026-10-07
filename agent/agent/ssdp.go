package agent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"printmaster/agent/scanner"
	"strings"
	"time"
)

// SSDP/UPnP constants
const (
	ssdpMulticastAddr = "239.255.255.250:1900"
	ssdpSearchTarget  = "upnp:rootdevice" // Could also use "ssdp:all" for broader discovery
)

// StartSSDPObservationBrowser emits accepted alive/response targets with raw
// sender and headers. A Location target is not evidence its host responded.
func StartSSDPObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback) {
	browseSourceMulticast(ctx, "SSDP", ssdpMulticastAddr, submit, report, ssdpObservations,
		func() error { return sendSourceDatagram(ctx, ssdpMulticastAddr, ssdpSearchMessage()) }, 5*time.Minute)
}

func ssdpSearchMessage() string {
	return "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 3\r\nST: " + ssdpSearchTarget + "\r\n\r\n"
}

func ssdpObservations(data []byte, src *net.UDPAddr) []scanner.Observation {
	message := string(data)
	line := strings.TrimSpace(strings.SplitN(message, "\r\n", 2)[0])
	headers := parseSSDPHeaders(message)
	h := scanner.SSDPHint{Sender: senderIPv4(src), Location: headers["location"], USN: headers["usn"],
		SearchTarget: headers["st"], NotificationType: headers["nt"], NotificationSubtype: headers["nts"], FilterDecision: "accepted"}
	filterType := h.SearchTarget
	switch line {
	case "NOTIFY * HTTP/1.1":
		if h.NotificationSubtype != "ssdp:alive" {
			return nil
		}
		h.Message = "alive"
		filterType = h.NotificationType // NOTIFY carries NT, not ST.
	case "HTTP/1.1 200 OK":
		h.Message = "response"
	default:
		return nil
	}
	if isNonPrinterDevice(filterType, h.USN) {
		return nil
	}
	var out []scanner.Observation
	for _, ip := range uniqueSourceTargets(h.Sender, sourceURLIPv4(h.Location)) {
		out = append(out, scanner.Observation{IP: ip, Source: scanner.SourceSSDP, Hints: scanner.ProtocolHints{SSDP: h}})
	}
	return out
}

func sourceURLValid(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func ssdpReceiptCredible(o scanner.Observation) bool {
	h := o.Hints.SSDP
	return h.Sender == o.IP && h.Message == "response" && h.FilterDecision == "accepted" &&
		h.USN != "" && h.SearchTarget != "" && sourceURLValid(h.Location) && !isNonPrinterDevice(h.SearchTarget, h.USN)
}

// Strict URI decoder for new adapters only; characterized legacy helpers stay.
func sourceURLIPv4(raw string) netip.Addr {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.Unmap().Is4() {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func uniqueSourceTargets(ips ...netip.Addr) []netip.Addr {
	var out []netip.Addr
	seen := make(map[netip.Addr]bool)
	for _, ip := range ips {
		if ip.IsValid() && ip.Is4() && !ip.IsUnspecified() && !ip.IsMulticast() && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}

type sourcePacketConn interface {
	ReadFromUDP([]byte) (int, *net.UDPAddr, error)
	SetReadDeadline(time.Time) error
	Close() error
}

func browseSourceMulticast(ctx context.Context, name, address string, submit ObservationCallback, report SourceErrorCallback,
	decode func([]byte, *net.UDPAddr) []scanner.Observation, probe func() error, interval time.Duration) {
	if ctx.Err() != nil {
		return
	}
	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		reportSourceError(report, fmt.Errorf("%s resolve: %w", name, err))
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		reportSourceError(report, fmt.Errorf("%s listen: %w", name, err))
		return
	}
	defer conn.Close()
	if err := conn.SetReadBuffer(65536); err != nil {
		reportSourceError(report, fmt.Errorf("%s buffer: %w", name, err))
	}
	readSourceDatagrams(ctx, conn, func(data []byte, src *net.UDPAddr, receivedAt time.Time) {
		submitSourceDatagram(ctx, data, src, receivedAt, decode, submit)
	}, func(err error) { reportSourceError(report, fmt.Errorf("%s: %w", name, err)) }, probe, interval)
}

func submitSourceDatagram(ctx context.Context, data []byte, src *net.UDPAddr, receivedAt time.Time,
	decode func([]byte, *net.UDPAddr) []scanner.Observation, submit ObservationCallback) {
	if ctx.Err() != nil || submit == nil {
		return
	}
	for _, o := range decode(data, src) {
		if ctx.Err() != nil {
			return
		}
		submit(sourceReceipt(o, receivedAt))
	}
}

func readSourceDatagrams(ctx context.Context, conn sourcePacketConn, consume func([]byte, *net.UDPAddr, time.Time), report SourceErrorCallback, probe func() error, interval time.Duration) {
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	var nextProbe time.Time
	if probe != nil {
		nextProbe = time.Now().Add(500 * time.Millisecond)
	}
	buf := make([]byte, 65536)
	for ctx.Err() == nil {
		now := time.Now()
		if !nextProbe.IsZero() && !now.Before(nextProbe) {
			reportSourceError(report, probe())
			if interval > 0 {
				nextProbe = time.Now().Add(interval)
			} else {
				nextProbe = time.Time{}
			}
		}
		deadline := time.Now().Add(time.Second)
		if !nextProbe.IsZero() && nextProbe.Before(deadline) {
			deadline = nextProbe
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			if ctx.Err() == nil {
				reportSourceError(report, err)
			}
			return
		}
		n, src, err := conn.ReadFromUDP(buf)
		receivedAt := time.Now() // Capture before decoding, callbacks, or enqueue.
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			reportSourceError(report, err)
			return // No spinning on permanently failed sockets.
		}
		if ctx.Err() == nil && consume != nil {
			consume(buf[:n], src, receivedAt)
		}
	}
}

func sendSourceDatagram(ctx context.Context, address, message string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err = conn.Write([]byte(message))
	return err
}

// sendMSearch sends an SSDP M-SEARCH message to discover existing devices
func sendMSearch() {
	reportSourceError(logSourceError, sendSourceDatagram(context.Background(), ssdpMulticastAddr, ssdpSearchMessage()))
}

// parseSSDPHeaders parses HTTP-style headers from SSDP message
func parseSSDPHeaders(message string) map[string]string {
	headers := make(map[string]string)
	lines := strings.Split(message, "\r\n")
	for _, line := range lines {
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.ToLower(strings.TrimSpace(line[:idx]))
			value := strings.TrimSpace(line[idx+1:])
			headers[key] = value
		}
	}
	return headers
}

// extractIPFromURL extracts IPv4 address from URL (e.g., http://192.168.1.100:8080/desc.xml)
func extractIPFromURL(url string) string {
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "https://")
	if idx := strings.Index(url, ":"); idx > 0 {
		url = url[:idx]
	}
	if idx := strings.Index(url, "/"); idx > 0 {
		url = url[:idx]
	}
	if ip := net.ParseIP(url); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	return ""
}

// isNonPrinterDevice checks if the device type is definitely not a printer
// Returns true for routers, gateways, media devices, etc.
func isNonPrinterDevice(st, usn string) bool {
	st = strings.ToLower(st)
	usn = strings.ToLower(usn)

	// Known non-printer device types
	nonPrinterTypes := []string{
		"internetgatewaydevice", // Routers/gateways
		"wanconnectiondevice",   // WAN devices
		"wandevice",             // WAN devices
		"mediarenderer",         // Media players (Chromecast, etc.)
		"mediaserver",           // Media servers
		"dial",                  // DIAL protocol (smart TVs)
		"upnp:rootdevice",       // Generic root device (too broad, but often routers)
	}

	// Check ST (Service Type) header
	for _, nonPrinter := range nonPrinterTypes {
		if strings.Contains(st, nonPrinter) {
			return true
		}
	}

	// Check USN (Unique Service Name)
	for _, nonPrinter := range nonPrinterTypes {
		if strings.Contains(usn, nonPrinter) {
			return true
		}
	}

	// Additional check for specific service types that are never printers
	if strings.Contains(st, "wanipconnection") ||
		strings.Contains(st, "wanpppconnection") ||
		strings.Contains(st, "layer3forwarding") ||
		strings.Contains(st, "wancommoninterface") ||
		strings.Contains(st, "wanethernetlink") {
		return true
	}

	return false
}
