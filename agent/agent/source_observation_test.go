package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"printmaster/agent/scanner"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/grandcat/zeroconf"
	"golang.org/x/net/dns/dnsmessage"
)

func observationSender(ip string) *net.UDPAddr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: 1234} }

func assertRawObservations(t *testing.T, observations []scanner.Observation, source scanner.ObservationSource, targets ...string) {
	t.Helper()
	if len(observations) != len(targets) {
		t.Fatalf("observations=%+v want=%v", observations, targets)
	}
	for i, o := range observations {
		if o.IP.String() != targets[i] || o.Source != source || !o.ObservedAt.IsZero() || o.Known != (scanner.KnownDeviceHint{}) {
			t.Fatalf("not raw/expected observation: %+v", o)
		}
	}
}

func TestSourceObservationMDNSMetadata(t *testing.T) {
	t.Parallel()
	e := &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "Office Printer"}, HostName: "printer.local.", Port: 631,
		Text: []string{"ty=Laser", "note=office"}, AddrIPv4: []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2"), net.ParseIP("192.0.2.1")}, AddrIPv6: []net.IP{net.ParseIP("2001:db8::1")}}
	out := mdnsObservations("_ipp._tcp", e)
	assertRawObservations(t, out, scanner.SourceMDNS, "192.0.2.1", "192.0.2.2")
	h := out[0].Hints.MDNS
	if h.Service != "_ipp._tcp" || h.Instance != e.Instance || h.Hostname != e.HostName || h.Record.Port != 631 || !reflect.DeepEqual(h.Record.TXT, e.Text) || len(h.Record.Addresses) != 4 {
		t.Fatalf("metadata=%+v record=%+v", h, h.Record)
	}
	e.Text[0] = "changed"
	e.AddrIPv4[0][0] = 9
	out[1].Hints.MDNS.Record.TXT[1] = "changed"
	if h.Record.TXT[0] != "ty=Laser" || h.Record.TXT[1] != "note=office" || h.Record.Addresses[0].String() != "192.0.2.1" {
		t.Fatal("aliased entry or sibling observation")
	}
	if len(mdnsObservations("_ipp._tcp", nil)) != 0 {
		t.Fatal("nil entry accepted")
	}
}

func TestSourceObservationSSDP(t *testing.T) {
	t.Parallel()
	src := observationSender("192.0.2.1")
	packet := "NOTIFY * HTTP/1.1\r\nNT: urn:schemas-upnp-org:device:Printer:1\r\nST: ignored\r\nNTS: ssdp:alive\r\nUSN: uuid:printer\r\nLOCATION: http://192.0.2.2:8080/desc.xml\r\n\r\n"
	out := ssdpObservations([]byte(packet), src)
	assertRawObservations(t, out, scanner.SourceSSDP, "192.0.2.1", "192.0.2.2")
	h := out[1].Hints.SSDP
	if h.Sender.String() != "192.0.2.1" || h.SearchTarget != "ignored" || h.NotificationType != "urn:schemas-upnp-org:device:Printer:1" || h.USN != "uuid:printer" || h.NotificationSubtype != "ssdp:alive" || h.Message != "alive" || h.FilterDecision != "accepted" || h.Location != "http://192.0.2.2:8080/desc.xml" {
		t.Fatalf("hints=%+v", h)
	}
	for _, bad := range []string{
		strings.Replace(packet, "device:Printer:1", "device:InternetGatewayDevice:1", 1),
		strings.Replace(packet, "ssdp:alive", "ssdp:byebye", 1),
		strings.Replace(packet, "NOTIFY * HTTP/1.1", "UNKNOWN", 1),
		"UNKNOWN\r\nX: HTTP/1.1 200 OK\r\n", // No substring-based message admission.
		"HTTP/1.1 200 OK\r\nST: upnp:rootdevice\r\nUSN: uuid:x\r\n",
	} {
		if len(ssdpObservations([]byte(bad), src)) != 0 {
			t.Fatalf("accepted %q", bad)
		}
	}
	response := "HTTP/1.1 200 OK\r\nST: urn:printer\r\nUSN: uuid:printer\r\nLOCATION: http://192.0.2.1/path\r\n"
	assertRawObservations(t, ssdpObservations([]byte(response), src), scanner.SourceSSDP, "192.0.2.1")
	assertRawObservations(t, ssdpObservations([]byte(packet), nil), scanner.SourceSSDP, "192.0.2.2")
	for _, raw := range []string{"192.0.2.1", "ftp://192.0.2.1/", "http://printer.local/", "http://192.0.2.1@192.0.2.2/", "http://[2001:db8::1]/", "http://bad%zz/"} {
		if sourceURLIPv4(raw).IsValid() {
			t.Fatalf("invalid URI accepted: %s", raw)
		}
	}
	if sourceURLIPv4("HTTP://192.0.2.7:5357/a:b").String() != "192.0.2.7" {
		t.Fatal("URL parse did not use hostname")
	}
}

func wsdObservationXML(body string) []byte {
	return []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><s:Body>` + body + `</s:Body></s:Envelope>`)
}

func TestSourceObservationWSD(t *testing.T) {
	t.Parallel()
	src := observationSender("192.0.2.9")
	fields := `<a:EndpointReference><a:Address>urn:uuid:printer</a:Address></a:EndpointReference><d:Types>print:Printer wsdp:Device</d:Types><d:Scopes>office floor2</d:Scopes><d:XAddrs>HTTP://192.0.2.1:5357/a:b https://192.0.2.2/ http://192.0.2.1/ ftp://192.0.2.3/</d:XAddrs>`
	for _, body := range []string{`<d:Hello>` + fields + `</d:Hello>`, `<d:ProbeMatch>` + fields + `</d:ProbeMatch>`, `<d:ProbeMatches><d:ProbeMatch>` + fields + `</d:ProbeMatch><d:ProbeMatch><d:XAddrs>http://192.0.2.3/</d:XAddrs></d:ProbeMatch></d:ProbeMatches>`} {
		out := wsdObservations(wsdObservationXML(body), src)
		targets := []string{"192.0.2.1", "192.0.2.2"}
		if strings.Contains(body, "ProbeMatches") {
			targets = append(targets, "192.0.2.3")
		}
		assertRawObservations(t, out, scanner.SourceWSD, targets...)
		h := out[0].Hints.WSD
		if h.Endpoint != "urn:uuid:printer" || h.Types != "print:Printer wsdp:Device" || h.Scopes != "office floor2" || h.Sender.String() != "192.0.2.9" || !strings.Contains(h.XAddr, "a:b") {
			t.Fatalf("hints=%+v", h)
		}
	}
	for _, body := range []string{`<d:Hello><d:Types>Printer</d:Types></d:Hello>`, `<d:ProbeMatches><d:ProbeMatch><d:XAddrs>http://printer.local/</d:XAddrs></d:ProbeMatch></d:ProbeMatches>`} {
		assertRawObservations(t, wsdObservations(wsdObservationXML(body), src), scanner.SourceWSD, "192.0.2.9")
	}
	for _, body := range []string{`<d:Bye/>`, `<d:ProbeMatches/>`, `<d:Probe/>`, `<Unknown/>`, `<Unknown><d:Hello/></Unknown>`, `<Hello/>`, `<d:ProbeMatches><ProbeMatch/></d:ProbeMatches>`} {
		if out := wsdObservations(wsdObservationXML(body), src); len(out) != 0 {
			t.Fatalf("unknown/leaving body enqueued: %s %+v", body, out)
		}
	}
	if len(wsdObservations([]byte("<broken"), src)) != 0 {
		t.Fatal("malformed XML enqueued")
	}
	modern := strings.ReplaceAll(string(wsdObservationXML(`<d:Hello>`+fields+`</d:Hello>`)), "http://schemas.xmlsoap.org/ws/2005/04/discovery", "http://docs.oasis-open.org/ws-dd/ns/discovery/2009/01")
	assertRawObservations(t, wsdObservations([]byte(modern), src), scanner.SourceWSD, "192.0.2.1", "192.0.2.2")
}

func TestSourceObservationTrapEligibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, oid, enterprise, eligibility, vendor string
		admitted                                   bool
	}{
		{"printer", ".1.3.6.1.2.1.43.18.2.0.3", "", "printer-oid", "", true},
		{"vendor", "1.3.6.1.4.1.2435.1.1", "", "vendor-hint", "brother", true},
		{"v1-vendor", "", ".1.3.6.1.4.1.1602", "vendor-hint", "canon", true},
		{"unknown", "1.3.6.1.6.3.1.1.5.1", "", "unknown", "", false},
		{"prefix-collision", "1.3.6.1.2.1.430.1", "", "unknown", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bytes := []byte("raw")
			packet := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, PDUType: gosnmp.SNMPv2Trap, Enterprise: tc.enterprise, GenericTrap: 6, SpecificTrap: 5, Variables: []gosnmp.SnmpPDU{{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: tc.oid}, {Name: "1.2.3", Value: bytes}}}
			wantOID := strings.TrimPrefix(tc.oid, ".")
			if tc.name == "v1-vendor" {
				packet.Version, packet.PDUType = gosnmp.Version1, gosnmp.Trap
				wantOID = "1.3.6.1.4.1.1602.0.5"
			}
			o, ok := trapObservation(packet, observationSender("192.0.2.4"))
			if !ok {
				t.Fatal("missing raw trap")
			}
			assertRawObservations(t, []scanner.Observation{o}, scanner.SourceTrap, "192.0.2.4")
			h := o.Hints.Trap
			if h.TrapOID != wantOID || h.Eligibility != tc.eligibility || h.Vendor != tc.vendor || h.Sender != o.IP || h.Packet.GenericTrap != 6 || h.Packet.SpecificTrap != 5 {
				t.Fatalf("hints=%+v", h)
			}
			bytes[0] = 'X'
			if string(h.Packet.PDUs[1].Value.([]byte)) != "raw" {
				t.Fatal("aliased PDU bytes")
			}
			var count int
			submitTrapObservation(context.Background(), packet, observationSender("192.0.2.4"), func(scanner.Observation) bool { count++; return true })
			if (count == 1) != tc.admitted {
				t.Fatalf("admission=%d", count)
			}
		})
	}
	for _, addr := range []*net.UDPAddr{nil, observationSender("2001:db8::1"), observationSender("0.0.0.0")} {
		if _, ok := trapObservation(&gosnmp.SnmpPacket{}, addr); ok {
			t.Fatal("invalid trap sender admitted")
		}
	}
	if _, ok := trapObservation(nil, observationSender("192.0.2.1")); ok {
		t.Fatal("nil trap admitted")
	}
	if _, ok := trapObservation(&gosnmp.SnmpPacket{PDUType: gosnmp.GetRequest, Variables: []gosnmp.SnmpPDU{{Name: "1.3.6.1.2.1.43.1"}}}, observationSender("192.0.2.1")); ok {
		t.Fatal("non-notification packet admitted as trap")
	}
}

func llmnrObservationPacket(t *testing.T, response bool) []byte {
	t.Helper()
	name, err := dnsmessage.NewName("HP-Office.local.")
	if err != nil {
		t.Fatal(err)
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{Response: response}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	if response {
		m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 8}}}}
	}
	data, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSourceObservationLLMNRMetadata(t *testing.T) {
	t.Parallel()
	for _, response := range []bool{false, true} {
		data := llmnrObservationPacket(t, response)
		target, kind := "192.0.2.9", "query"
		if response {
			target, kind = "192.0.2.8", "response"
		}
		out := llmnrObservations(data, observationSender("192.0.2.9"))
		assertRawObservations(t, out, scanner.SourceLLMNR, target)
		h := out[0].Hints.LLMNR
		if h.Hostname != "HP-Office.local" || h.Message != kind || h.Sender.String() != "192.0.2.9" || h.Answer.IsValid() != response {
			t.Fatalf("metadata=%+v", h)
		}
		job := llmnrLegacyJob(out[0])
		meta, ok := job.Meta.(scanner.LLMNRHint)
		if !ok || meta != h || job.IP != target || job.Source != "llmnr" {
			t.Fatalf("legacy metadata=%+v", job)
		}
		for n := 0; n < len(data); n++ {
			if len(llmnrObservations(data[:n], observationSender("192.0.2.9"))) != 0 {
				t.Fatalf("truncated packet %d admitted", n)
			}
		}
	}
	cycle := make([]byte, 12)
	cycle[5] = 1
	cycle = append(cycle, 0xc0, 0x0c, 0, 1, 0, 1)
	if len(llmnrObservations(cycle, observationSender("192.0.2.9"))) != 0 {
		t.Fatal("compression loop admitted")
	}
}

func TestSourceObservationThrottleAcceptedOnly(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	submit := throttleObservations(func(scanner.Observation) bool { return attempts.Add(1) > 1 }, time.Hour)
	o := scanner.Observation{IP: netip.MustParseAddr("192.0.2.1")}
	if submit(o) || !submit(o) || submit(o) || attempts.Load() != 2 {
		t.Fatal("rejected admission poisoned throttle")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); submit(o) }()
	}
	wg.Wait()
	if attempts.Load() != 2 {
		t.Fatal("duplicate concurrent admission")
	}
}

func TestSourceObservationMDNSJoin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 3)
	release := make(chan struct{})
	finished := make(chan struct{})
	var joins atomic.Int32
	go func() {
		runMDNSObservationBrowser(ctx, nil, nil, func(ctx context.Context, service string, entries chan<- *zeroconf.ServiceEntry) error {
			go func() { started <- service; <-ctx.Done(); <-release; joins.Add(1); close(entries) }()
			return nil // Like zeroconf: return before the service browser finishes.
		})
		close(finished)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("browser failed to start")
		}
	}
	cancel()
	select {
	case <-finished:
		t.Fatal("launcher returned before browsers joined")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("launcher failed to join")
	}
	if joins.Load() != 3 {
		t.Fatal("missing service join")
	}
}

func TestSourceObservationMDNSCallbacksSerialized(t *testing.T) {
	t.Parallel()
	var observations, reports int // Race detector verifies callbacks are owner-serialized.
	runMDNSObservationBrowser(context.Background(), func(scanner.Observation) bool { observations++; return true }, func(error) { reports++ },
		func(_ context.Context, _ string, entries chan<- *zeroconf.ServiceEntry) error {
			go func() {
				entries <- &zeroconf.ServiceEntry{AddrIPv4: []net.IP{net.ParseIP("192.0.2.1")}}
				close(entries)
			}()
			return errors.New("fake browse error")
		})
	if observations != 3 || reports != 3 {
		t.Fatalf("observations=%d errors=%d", observations, reports)
	}
}

type offlineSourceConn struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
	readErr error
}

func (c *offlineSourceConn) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	close(c.started)
	if c.readErr != nil {
		return 0, nil, c.readErr
	}
	<-c.closed
	return 0, nil, net.ErrClosed
}
func (*offlineSourceConn) SetReadDeadline(time.Time) error { return nil }
func (c *offlineSourceConn) Close() error                  { c.once.Do(func() { close(c.closed) }); return nil }

func TestSourceObservationReadShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &offlineSourceConn{started: make(chan struct{}), closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		readSourceDatagrams(ctx, conn, func([]byte, *net.UDPAddr, time.Time) { t.Error("callback after cancel") }, func(error) { t.Error("shutdown logged as error") }, func() error { t.Error("probe after cancel"); return nil }, time.Hour)
		close(done)
	}()
	<-conn.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read shutdown blocked")
	}
}

func TestSourceObservationReadErrorStops(t *testing.T) {
	t.Parallel()
	want := errors.New("offline read error")
	conn := &offlineSourceConn{started: make(chan struct{}), closed: make(chan struct{}), readErr: want}
	var got error
	readSourceDatagrams(context.Background(), conn, nil, func(err error) { got = err }, nil, 0)
	if !errors.Is(got, want) {
		t.Fatalf("error=%v", got)
	}
}

type offlineTrapListener struct {
	ready   chan bool
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (l *offlineTrapListener) Listen(string) error {
	l.ready <- true
	close(l.started)
	<-l.closed
	return nil
}
func (l *offlineTrapListener) Listening() <-chan bool { return l.ready }
func (l *offlineTrapListener) Close()                 { l.once.Do(func() { close(l.closed) }) }

func TestSourceObservationTrapShutdownAndRetry(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	l := &offlineTrapListener{ready: make(chan bool, 1), started: make(chan struct{}), closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- runTrapObservationListener(ctx, l, "offline") }()
	<-l.started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("trap listener did not join")
	}
	ctx, cancel = context.WithCancel(context.Background())
	var attempts int
	runTrapObservationBrowser(ctx, nil, func(error) { cancel() }, func(context.Context, ObservationCallback, uint16) error {
		attempts++
		return errors.New("offline setup")
	})
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
	runTrapObservationBrowser(context.Background(), nil, nil, func(context.Context, ObservationCallback, uint16) error { return fmtPermissionError() })
	if waitSourceDelay(ctx, time.Hour) {
		t.Fatal("canceled retry delay completed")
	}
}

func fmtPermissionError() error { return &net.OpError{Op: "listen", Err: syscall.EACCES} }

func TestSourceObservationReceiptAdmission(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ip := netip.MustParseAddr("192.0.2.1")
	mdns := mdnsObservations("_ipp._tcp", &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "Printer"}, HostName: "printer.local.", Port: 631, AddrIPv4: []net.IP{net.ParseIP(ip.String())}})[0]
	ssdp := ssdpObservations([]byte("HTTP/1.1 200 OK\r\nST: urn:printer\r\nUSN: uuid:printer\r\nLOCATION: http://192.0.2.2/\r\n"), observationSender(ip.String()))
	wsd := wsdObservations(wsdObservationXML(`<d:ProbeMatches><d:ProbeMatch><a:EndpointReference><a:Address>urn:uuid:printer</a:Address></a:EndpointReference><d:Types>Printer</d:Types><d:XAddrs>http://192.0.2.1/ http://192.0.2.2/</d:XAddrs></d:ProbeMatch></d:ProbeMatches>`), observationSender(ip.String()))
	trap, _ := trapObservation(&gosnmp.SnmpPacket{Version: gosnmp.Version2c, PDUType: gosnmp.SNMPv2Trap, Variables: []gosnmp.SnmpPDU{{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1.2.1.43.18.2.0.1"}}}, observationSender(ip.String()))
	llmnr := llmnrObservations(llmnrObservationPacket(t, true), observationSender("192.0.2.8"))[0]
	for _, tc := range []struct {
		name string
		o    scanner.Observation
		want bool
	}{
		{"mdns", mdns, true}, {"ssdp-sender", ssdp[0], true}, {"ssdp-location", ssdp[1], false},
		{"wsd-sender", wsd[0], true}, {"wsd-xaddr", wsd[1], false}, {"trap", trap, true}, {"llmnr-answer-sender", llmnr, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sourceReceipt(tc.o, at)
			if got.ObservedAt.IsZero() == tc.want || tc.want && got.ObservedAt != at {
				t.Fatalf("receipt=%v want evidence=%v", got.ObservedAt, tc.want)
			}
			got.ObservedAt = time.Time{}
			if !reflect.DeepEqual(got, tc.o) {
				t.Fatal("receipt admission changed raw metadata")
			}
		})
	}
	for _, tc := range []struct {
		name string
		o    scanner.Observation
		edit func(*scanner.Observation)
	}{
		{"mdns-service", mdns, func(o *scanner.Observation) { o.Hints.MDNS.Service = "_http._tcp" }},
		{"mdns-instance", mdns, func(o *scanner.Observation) { o.Hints.MDNS.Instance = "" }},
		{"mdns-host", mdns, func(o *scanner.Observation) { o.Hints.MDNS.Hostname = "" }},
		{"mdns-port", mdns, func(o *scanner.Observation) { r := *o.Hints.MDNS.Record; r.Port = 0; o.Hints.MDNS.Record = &r }},
		{"mdns-indirect", mdns, func(o *scanner.Observation) { o.IP = netip.MustParseAddr("192.0.2.99") }},
		{"ssdp-alive", ssdp[0], func(o *scanner.Observation) { o.Hints.SSDP.Message = "alive" }},
		{"ssdp-usn", ssdp[0], func(o *scanner.Observation) { o.Hints.SSDP.USN = "" }},
		{"ssdp-credentials", ssdp[0], func(o *scanner.Observation) { o.Hints.SSDP.Location = "http://user:secret@192.0.2.1/" }},
		{"ssdp-filter", ssdp[0], func(o *scanner.Observation) { o.Hints.SSDP.SearchTarget = "upnp:rootdevice" }},
		{"wsd-hello", wsd[0], func(o *scanner.Observation) { o.Hints.WSD.Message = "Hello" }},
		{"wsd-endpoint", wsd[0], func(o *scanner.Observation) { o.Hints.WSD.Endpoint = "" }},
		{"wsd-types", wsd[0], func(o *scanner.Observation) { o.Hints.WSD.Types = "" }},
		{"wsd-url", wsd[0], func(o *scanner.Observation) { o.Hints.WSD.XAddr += " ftp://192.0.2.1/" }},
		{"trap-version", trap, func(o *scanner.Observation) {
			p := *o.Hints.Trap.Packet
			p.Version = gosnmp.Version1
			o.Hints.Trap.Packet = &p
		}},
		{"trap-type", trap, func(o *scanner.Observation) {
			p := *o.Hints.Trap.Packet
			p.PDUType = gosnmp.GetResponse
			o.Hints.Trap.Packet = &p
		}},
		{"trap-oid", trap, func(o *scanner.Observation) { o.Hints.Trap.TrapOID += ".bad" }},
		{"trap-pdus", trap, func(o *scanner.Observation) { p := *o.Hints.Trap.Packet; p.PDUs = nil; o.Hints.Trap.Packet = &p }},
		{"trap-eligibility", trap, func(o *scanner.Observation) { o.Hints.Trap.Eligibility = "unknown" }},
		{"llmnr-query", llmnr, func(o *scanner.Observation) { o.Hints.LLMNR.Message = "query" }},
		{"llmnr-indirect", llmnr, func(o *scanner.Observation) { o.Hints.LLMNR.Sender = ip }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := tc.o
			tc.edit(&o)
			o.ObservedAt = at // Never preserve caller-supplied receipts for bad evidence.
			if !sourceReceipt(o, at).ObservedAt.IsZero() {
				t.Fatal("noncredible observation acquired receipt")
			}
		})
	}
}

func TestSourceObservationDatagramReceiptAndCancel(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(-time.Second)
	packet := []byte("HTTP/1.1 200 OK\r\nST: urn:printer\r\nUSN: uuid:printer\r\nLOCATION: http://192.0.2.2/\r\n")
	var got []scanner.Observation
	submitSourceDatagram(context.Background(), packet, observationSender("192.0.2.1"), at, ssdpObservations, func(o scanner.Observation) bool { got = append(got, o); return true })
	if len(got) != 2 || got[0].ObservedAt != at || !got[1].ObservedAt.IsZero() {
		t.Fatalf("local receive time/indirect target: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	submitSourceDatagram(ctx, packet, observationSender("192.0.2.1"), at, ssdpObservations, func(scanner.Observation) bool { calls++; cancel(); return true })
	if calls != 1 {
		t.Fatal("delivery continued after callback canceled source")
	}
	submitSourceDatagram(ctx, packet, nil, at, func([]byte, *net.UDPAddr) []scanner.Observation { t.Error("decode after cancel"); return nil }, func(scanner.Observation) bool { t.Error("submit after cancel"); return false })
}

func TestSourceObservationMDNSReceiptBeforeOwnerQueue(t *testing.T) {
	t.Parallel()
	var receipts []time.Time
	runMDNSObservationBrowser(context.Background(), func(o scanner.Observation) bool {
		if o.ObservedAt.IsZero() || o.ObservedAt.After(time.Now()) {
			t.Error("missing/late local entry receipt")
		}
		receipts = append(receipts, o.ObservedAt)
		return true
	}, nil, func(_ context.Context, _ string, entries chan<- *zeroconf.ServiceEntry) error {
		go func() {
			defer close(entries)
			entries <- &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "Printer"}, HostName: "printer.local.", Port: 631, AddrIPv4: []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")}}
		}()
		return nil
	})
	if len(receipts) != 6 {
		t.Fatalf("receipts=%d", len(receipts))
	}
}

func TestSourceObservationTrapCallbackReceipt(t *testing.T) {
	t.Parallel()
	packet := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, PDUType: gosnmp.SNMPv2Trap, Variables: []gosnmp.SnmpPDU{{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1.2.1.43.18.2.0.1"}}}
	before := time.Now()
	var got scanner.Observation
	submitTrapObservation(context.Background(), packet, observationSender("192.0.2.1"), func(o scanner.Observation) bool { got = o; return true })
	if got.ObservedAt.Before(before) || got.ObservedAt.After(time.Now()) {
		t.Fatalf("callback receipt=%v", got.ObservedAt)
	}
	packet.Variables[0].Type = gosnmp.OctetString
	submitTrapObservation(context.Background(), packet, observationSender("192.0.2.1"), func(o scanner.Observation) bool {
		if !o.ObservedAt.IsZero() {
			t.Error("wrong trap varbind type got receipt")
		}
		return true
	})
}

func TestSourceObservationLLMNRMalformedShape(t *testing.T) {
	t.Parallel()
	for _, edit := range []func(*dnsmessage.Message){
		func(m *dnsmessage.Message) { m.Truncated = true },
		func(m *dnsmessage.Message) { m.Questions = append(m.Questions, m.Questions[0]) },
		func(m *dnsmessage.Message) { m.Questions[0].Class = dnsmessage.ClassCHAOS },
		func(m *dnsmessage.Message) { m.Questions[0].Type = dnsmessage.TypeAAAA },
		func(m *dnsmessage.Message) { m.OpCode = 1 },
		func(m *dnsmessage.Message) { m.RCode = dnsmessage.RCodeNameError },
	} {
		var m dnsmessage.Message
		if err := m.Unpack(llmnrObservationPacket(t, true)); err != nil {
			t.Fatal(err)
		}
		edit(&m)
		data, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		if len(llmnrObservations(data, observationSender("192.0.2.8"))) != 0 {
			t.Fatal("malformed LLMNR shape admitted")
		}
	}
}

func TestSourceObservationCoordinatorIndependentlyValidates(t *testing.T) {
	t.Parallel()
	ip := "192.0.2.1"
	packet := []byte("HTTP/1.1 200 OK\r\nST: urn:printer\r\nUSN: uuid:printer\r\nLOCATION: http://192.0.2.2/\r\n")
	var observations []scanner.Observation
	submitSourceDatagram(context.Background(), packet, observationSender(ip), time.Now(), ssdpObservations, func(o scanner.Observation) bool {
		observations = append(observations, o)
		return true
	})
	for _, tc := range []struct {
		name  string
		o     scanner.Observation
		edit  func(*scanner.Observation)
		probe bool
	}{
		{"sender", observations[0], func(*scanner.Observation) {}, false},
		{"indirect", observations[1], func(*scanner.Observation) {}, true},
		{"zero", observations[0], func(o *scanner.Observation) { o.ObservedAt = time.Time{} }, true},
		{"stale", observations[0], func(o *scanner.Observation) { o.ObservedAt = time.Now().Add(-time.Minute) }, true},
		{"future", observations[0], func(o *scanner.Observation) { o.ObservedAt = time.Now().Add(time.Minute) }, true},
		{"mutated-after-admission", observations[0], func(o *scanner.Observation) { o.Hints.SSDP.USN = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var probes atomic.Int32
			c, err := scanner.NewCoordinator(scanner.CoordinatorConfig{Backend: scanner.Backend{
				Probe: func(context.Context, netip.Addr) ([]uint16, error) { probes.Add(1); return []uint16{9100}, nil },
				Query: func(context.Context, scanner.QueryRequest) (*scanner.QueryResult, error) {
					return nil, errors.New("offline identity backend")
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			o := tc.o
			tc.edit(&o)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = c.DoSource(ctx, scanner.Request{Observation: o, Preset: scanner.IntentQuick})
			if (probes.Load() == 1) != tc.probe {
				t.Fatalf("TCP probes=%d want probe=%v", probes.Load(), tc.probe)
			}
		})
	}
}

type offlineStartingTrapListener struct {
	offlineTrapListener
	release chan struct{}
}

func (l *offlineStartingTrapListener) Listen(address string) error {
	close(l.started)
	<-l.release
	l.ready <- true
	<-l.closed
	return nil
}

func TestSourceObservationTrapCancelDuringStartup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &offlineStartingTrapListener{offlineTrapListener: offlineTrapListener{ready: make(chan bool, 1), started: make(chan struct{}), closed: make(chan struct{})}, release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- runTrapObservationListener(ctx, l, "offline") }()
	<-l.started
	cancel()
	select {
	case <-l.closed:
		t.Fatal("socket closed before readiness published")
	default:
	}
	close(l.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup cancellation failed to join listener")
	}
}

func TestSourceObservationCanceledBrowsersOffline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callback := func(scanner.Observation) bool { t.Error("canceled browser callback"); return false }
	report := func(error) { t.Error("canceled browser opened network") }
	StartMDNSObservationBrowser(ctx, callback, report)
	StartSSDPObservationBrowser(ctx, callback, report)
	StartWSDiscoveryObservationBrowser(ctx, callback, report)
	StartLLMNRObservationBrowser(ctx, callback, report)
	StartSNMPTrapObservationBrowser(ctx, callback, report)
	if err := StartSNMPTrapObservationListener(ctx, callback, 162); err != nil {
		t.Fatal(err)
	}
}
