package agent

import (
	"encoding/binary"
	"encoding/xml"
	"reflect"
	"testing"
)

// These tests exercise production parsers and the trap callback only. Browser
// entry points open sockets before processing input, so they are deliberately
// not invoked. Observation-adapter seams are covered separately in the source
// observation tests; these retain historical helper compatibility baselines.
func TestBaselineSSDPHeaders(t *testing.T) {
	t.Parallel()
	message := "HTTP/1.1 200 OK\r\nLOCATION: http://192.0.2.5:8080/desc.xml\r\n ST : urn:printer\r\nUSN: uuid:printer:1\r\nlocation: https://192.0.2.6/\r\nignored\r\n:empty-key\r\n\r\n"
	want := map[string]string{"location": "https://192.0.2.6/", "st": "urn:printer", "usn": "uuid:printer:1"}
	if got := parseSSDPHeaders(message); !reflect.DeepEqual(got, want) {
		t.Fatalf("headers = %#v, want %#v", got, want)
	}
}

func TestBaselineSSDPURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"http://192.0.2.1:8080/desc.xml", "192.0.2.1"},
		{"https://192.0.2.2/path", "192.0.2.2"},
		{"192.0.2.3", "192.0.2.3"},
		{"http://printer.example/desc.xml", ""},
		{"http://[2001:db8::1]:8080/", ""},
		{"http://999.0.2.1/", ""},
		// Limitation: this is string slicing, not URL parsing. Schemes are
		// case-sensitive and whitespace is not trimmed. Not desired policy.
		{"HTTP://192.0.2.1/", ""},
		{" http://192.0.2.1/", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			if got := extractIPFromURL(tc.input); got != tc.want {
				t.Fatalf("IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBaselineSSDPFilter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, st, usn string
		reject        bool
	}{
		{"printer", "urn:schemas-upnp-org:device:Printer:1", "uuid:printer", false},
		{"unknown", "", "uuid:unknown", false},
		{"gateway-case", "URN:InternetGatewayDevice:1", "uuid:x", true},
		{"media-usn", "", "uuid:x::MediaRenderer", true},
		{"wan-service", "urn:WANIPConnection:1", "uuid:x", true},
		// Known broad substring exclusions: rootdevice and 'dial' can reject
		// printers too. Characterize current heuristic, not an allowlist policy.
		{"root-printer", "upnp:rootdevice", "uuid:printer", true},
		{"substring", "", "uuid:dial-printer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isNonPrinterDevice(tc.st, tc.usn); got != tc.reject {
				t.Fatalf("rejected = %v, want %v", got, tc.reject)
			}
		})
	}
}

func TestBaselineWSDXAddrs(t *testing.T) {
	t.Parallel()
	input := " \thttp://192.0.2.1:5357/device\nhttps://192.0.2.2/ws http://192.0.2.1/ http://printer.example/ http://[2001:db8::1]/ invalid "
	// Current extraction preserves order and duplicates; caller owns dedup.
	want := []string{"192.0.2.1", "192.0.2.2", "192.0.2.1"}
	if got := extractIPsFromXAddrs(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("IPs = %#v, want %#v", got, want)
	}
	if got := extractIPsFromXAddrs(" \t\n"); len(got) != 0 {
		t.Fatalf("empty XAddrs = %#v", got)
	}
}

func TestBaselineWSDEnvelope(t *testing.T) {
	t.Parallel()
	const prefix = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><s:Header><a:Action>action</a:Action><a:MessageID>urn:uuid:message</a:MessageID><a:To>destination</a:To></s:Header><s:Body>`
	const suffix = `</s:Body></s:Envelope>`
	const fields = `<a:EndpointReference><a:Address>urn:uuid:device</a:Address></a:EndpointReference><d:Types>wsdp:Device print:Printer</d:Types><d:Scopes>office</d:Scopes><d:XAddrs>http://192.0.2.8:5357/</d:XAddrs><d:MetadataVersion>7</d:MetadataVersion>`
	for _, kind := range []string{"Hello", "ProbeMatch", "Bye"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var got wsDiscoveryEnvelope
			if err := xml.Unmarshal([]byte(prefix+"<d:"+kind+">"+fields+"</d:"+kind+">"+suffix), &got); err != nil {
				t.Fatal(err)
			}
			if got.Header.Action != "action" || got.Header.MessageID != "urn:uuid:message" || got.Header.To != "destination" {
				t.Fatalf("header = %#v", got.Header)
			}
			wantRef := wsEndpointReference{Address: "urn:uuid:device"}
			switch kind {
			case "Hello":
				want := &wsHello{EndpointReference: wantRef, Types: "wsdp:Device print:Printer", Scopes: "office", XAddrs: "http://192.0.2.8:5357/", MetadataVersion: 7}
				if !reflect.DeepEqual(got.Body.Hello, want) {
					t.Fatalf("Hello = %#v, want %#v", got.Body.Hello, want)
				}
			case "ProbeMatch":
				want := &wsProbeMatch{EndpointReference: wantRef, Types: "wsdp:Device print:Printer", Scopes: "office", XAddrs: "http://192.0.2.8:5357/", MetadataVersion: 7}
				if !reflect.DeepEqual(got.Body.ProbeMatch, want) {
					t.Fatalf("ProbeMatch = %#v, want %#v", got.Body.ProbeMatch, want)
				}
			case "Bye":
				if !reflect.DeepEqual(got.Body.Bye, &wsBye{EndpointReference: wantRef}) {
					t.Fatalf("Bye = %#v", got.Body.Bye)
				}
			}
		})
	}
	// Known protocol gap: the struct accepts a direct ProbeMatch, but a real
	// ProbeMatches wrapper is silently ignored. This assertion documents a bug
	// for the parent slice, not desired compatibility; change it with that fix.
	var wrapped wsDiscoveryEnvelope
	if err := xml.Unmarshal([]byte(prefix+"<d:ProbeMatches><d:ProbeMatch>"+fields+"</d:ProbeMatch></d:ProbeMatches>"+suffix), &wrapped); err != nil {
		t.Fatal(err)
	}
	if wrapped.Body.ProbeMatch != nil {
		t.Fatal("baseline unexpectedly supports wrapped ProbeMatches; update characterization")
	}
	for _, input := range []string{"<broken", "<Envelope/>"} {
		var got wsDiscoveryEnvelope
		if err := xml.Unmarshal([]byte(input), &got); err == nil {
			t.Fatalf("accepted malformed/wrong-namespace envelope %q", input)
		}
	}
}

// baselineLLMNRPacket builds bytes only; it never resolves names or opens UDP.
func baselineLLMNRPacket(response bool, addresses ...[4]byte) []byte {
	packet := make([]byte, 12)
	if response {
		binary.BigEndian.PutUint16(packet[2:4], 0x8000)
	}
	binary.BigEndian.PutUint16(packet[4:6], 1)
	binary.BigEndian.PutUint16(packet[6:8], uint16(len(addresses)))
	for _, label := range []string{"HP-Office", "local"} {
		packet = append(packet, byte(len(label)))
		packet = append(packet, label...)
	}
	packet = append(packet, 0, 0, 1, 0, 1) // name end, QTYPE A, QCLASS IN
	for _, address := range addresses {
		packet = append(packet, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		packet = append(packet, address[:]...)
	}
	return packet
}

func TestBaselineLLMNRPacket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		packet   []byte
		host     string
		response bool
		ip       string
	}{
		{"short", []byte{0, 1}, "", false, ""},
		{"empty", make([]byte, 12), "", false, ""},
		{"query", baselineLLMNRPacket(false), "HP-Office.local", false, ""},
		{"answer", baselineLLMNRPacket(true, [4]byte{192, 0, 2, 10}), "HP-Office.local", true, "192.0.2.10"},
		// Current parser overwrites the A address on every answer: last A wins,
		// not multiple targets. Preserve as baseline, not future target policy.
		{"two-answers", baselineLLMNRPacket(true, [4]byte{192, 0, 2, 10}, [4]byte{192, 0, 2, 11}), "HP-Office.local", true, "192.0.2.11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, response, ip := parseLLMNRPacket(tc.packet)
			if host != tc.host || response != tc.response || ip != tc.ip {
				t.Fatalf("packet = (%q, %v, %q), want (%q, %v, %q)", host, response, ip, tc.host, tc.response, tc.ip)
			}
		})
	}
	packet := baselineLLMNRPacket(true, [4]byte{192, 0, 2, 10})
	// Every truncation must avoid extracting an incomplete address.
	for n := 0; n < len(packet); n++ {
		_, _, ip := parseLLMNRPacket(packet[:n])
		if ip != "" {
			t.Fatalf("truncation %d extracted incomplete address %q", n, ip)
		}
	}
}

func TestBaselineLLMNRDNSName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		data   []byte
		offset int
		want   string
		next   int
	}{
		{"labels", []byte{2, 'h', 'p', 0}, 0, "hp", 4},
		{"pointer", []byte{2, 'h', 'p', 0, 0xc0, 0}, 4, "hp", 6},
		{"pointer-with-label", []byte{2, 'h', 'p', 0, 3, 'l', 'a', 'b', 0xc0, 0}, 4, "lab.hp", 10},
		{"short-label", []byte{3, 'h'}, 0, "", 0},
		{"short-pointer", []byte{0xc0}, 0, "", 0},
		{"out-of-bounds-pointer", []byte{0xc0, 127}, 0, "", 2},
		{"negative-offset", []byte{0}, -1, "", -1},
		{"self-cycle", []byte{0xc0, 0}, 0, "", 2},
		{"two-pointer-cycle", []byte{0xc0, 2, 0xc0, 0}, 0, "", 4},
		{"label-cycle", []byte{2, 'h', 'p', 0xc0, 0}, 0, "", 5},
		{"reserved-label-tag", []byte{0x40, 0}, 0, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, next := parseDNSName(tc.data, tc.offset)
			if name != tc.want || next != tc.next {
				t.Fatalf("name = (%q, %d), want (%q, %d)", name, next, tc.want, tc.next)
			}
		})
	}
}

func TestBaselineLLMNRHostnameFilter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"HP-OFFICE", true}, {"PRN-01", true}, {"MFP-01", true},
		{"Canon.local", true}, {"copier", true}, {"desktop.local", false}, {"", false},
		// Known loose heuristic: substring 'hp' accepts unrelated names;
		// bare PRN- fails the >4 length guard. Not desired identity evidence.
		{"graphpaper", true}, {"PRN-", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			if got := isPrinterHostname(tc.host); got != tc.want {
				t.Fatalf("printer hostname = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBaselineLLMNRNonAAnswer(t *testing.T) {
	t.Parallel()
	packet := baselineLLMNRPacket(true, [4]byte{192, 0, 2, 10})
	answerOffset := len(baselineLLMNRPacket(true))
	binary.BigEndian.PutUint16(packet[answerOffset+2:answerOffset+4], 28) // AAAA, not A
	host, response, ip := parseLLMNRPacket(packet)
	if host != "HP-Office.local" || !response || ip != "" {
		t.Fatalf("non-A answer = (%q, %v, %q)", host, response, ip)
	}
	// Known gap: response hostname comes only from question, not answer name.
	// Questionless responses cannot pass browser's nonempty-hostname guard.
	questionless := append([]byte(nil), packet[:12]...)
	binary.BigEndian.PutUint16(questionless[4:6], 0)
	questionless = append(questionless, 2, 'h', 'p', 0, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 10)
	host, response, ip = parseLLMNRPacket(questionless)
	if host != "" || !response || ip != "192.0.2.10" {
		t.Fatalf("questionless = (%q, %v, %q)", host, response, ip)
	}
}
