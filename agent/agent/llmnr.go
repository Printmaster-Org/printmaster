package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"printmaster/agent/scanner"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// LLMNR (Link-Local Multicast Name Resolution) constants
// RFC 4795 - Windows native alternative to mDNS
const (
	llmnrMulticastAddr = "224.0.0.252:5355"
)

// StartLLMNRObservationBrowser retains hostname/answer/sender hints, not identity.
// Existing hostname heuristics and query-source fallback remain admission policy.
func StartLLMNRObservationBrowser(ctx context.Context, submit ObservationCallback, report SourceErrorCallback) {
	browseSourceMulticast(ctx, "LLMNR", llmnrMulticastAddr, submit, report, llmnrObservations, nil, 0)
}

func llmnrObservations(data []byte, src *net.UDPAddr) []scanner.Observation {
	// Use a bounded DNS decoder on untrusted traffic.
	var message dnsmessage.Message
	if err := message.Unpack(data); err != nil || message.OpCode != 0 || message.RCode != dnsmessage.RCodeSuccess || message.Truncated || len(message.Questions) != 1 {
		return nil
	}
	if message.Questions[0].Class != dnsmessage.ClassINET || (message.Questions[0].Type != dnsmessage.TypeA && message.Questions[0].Type != dnsmessage.TypeALL) {
		return nil
	}
	host := strings.TrimSuffix(message.Questions[0].Name.String(), ".")
	if !isPrinterHostname(host) {
		return nil
	}
	h := scanner.LLMNRHint{Hostname: host, Sender: senderIPv4(src), Message: "query"}
	target := h.Sender
	if message.Response {
		h.Message = "response"
		for _, answer := range message.Answers {
			if !strings.EqualFold(answer.Header.Name.String(), message.Questions[0].Name.String()) || answer.Header.Class != dnsmessage.ClassINET {
				continue
			}
			if body, ok := answer.Body.(*dnsmessage.AResource); ok {
				h.Answer = sourceIPv4(net.IP(body.A[:]))
				target = h.Answer // Preserve last matching A-answer selection.
			}
		}
	}
	if len(uniqueSourceTargets(target)) == 0 {
		return nil
	}
	return []scanner.Observation{{IP: target, Source: scanner.SourceLLMNR, Hints: scanner.ProtocolHints{LLMNR: h}}}
}

func llmnrReceiptCredible(o scanner.Observation) bool {
	h := o.Hints.LLMNR
	return h.Message == "response" && h.Hostname != "" && h.Sender == o.IP && h.Answer == o.IP
}

// parseLLMNRPacket extracts hostname and IP from LLMNR DNS packet
func parseLLMNRPacket(data []byte) (hostname string, isResponse bool, ipv4Addr string) {
	if len(data) < 12 {
		return "", false, ""
	}

	// Parse DNS header
	flags := binary.BigEndian.Uint16(data[2:4])
	isResponse = (flags & 0x8000) != 0 // QR bit
	qdCount := binary.BigEndian.Uint16(data[4:6])
	anCount := binary.BigEndian.Uint16(data[6:8])

	offset := 12

	// Parse question section (if present)
	if qdCount > 0 && offset < len(data) {
		name, newOffset := parseDNSName(data, offset)
		hostname = name
		offset = newOffset + 4 // Skip QTYPE and QCLASS
	}

	// Parse answer section for IPv4 addresses (A records)
	if isResponse && anCount > 0 {
		for i := 0; i < int(anCount) && offset < len(data); i++ {
			// Parse answer name (usually compressed pointer)
			_, newOffset := parseDNSName(data, offset)
			offset = newOffset

			if offset+10 > len(data) {
				break
			}

			rrType := binary.BigEndian.Uint16(data[offset : offset+2])
			// rrClass := binary.BigEndian.Uint16(data[offset+2 : offset+4])
			// ttl := binary.BigEndian.Uint32(data[offset+4 : offset+8])
			rdLength := binary.BigEndian.Uint16(data[offset+8 : offset+10])
			offset += 10

			// Check if it's an A record (IPv4)
			if rrType == 1 && rdLength == 4 && offset+4 <= len(data) {
				ipv4Addr = fmt.Sprintf("%d.%d.%d.%d",
					data[offset], data[offset+1], data[offset+2], data[offset+3])
			}

			offset += int(rdLength)
		}
	}

	return hostname, isResponse, ipv4Addr
}

// parseDNSName extracts a DNS name from a packet starting at offset
func parseDNSName(data []byte, offset int) (string, int) {
	var parts []string
	jumped := false
	jumpOffset := 0
	visited := make(map[int]bool)
	nameLength := 0

	for offset >= 0 && offset < len(data) {
		if visited[offset] {
			return "", len(data) // Malformed cycles must not become partial names.
		}
		visited[offset] = true
		length := int(data[offset])

		// Check for compression pointer (top 2 bits set)
		if length&0xC0 == 0xC0 {
			if offset+1 >= len(data) {
				break
			}
			// Pointer to another location in the packet
			pointer := int(binary.BigEndian.Uint16(data[offset:offset+2]) & 0x3FFF)
			if !jumped {
				jumpOffset = offset + 2
			}
			offset = pointer
			jumped = true
			continue
		}

		// End of name
		if length == 0 {
			offset++
			break
		}

		// Check bounds
		if length&0xC0 != 0 {
			return "", len(data)
		}
		if offset+1+length > len(data) {
			break
		}
		nameLength += length + 1
		if nameLength > 254 {
			return "", len(data)
		}

		// Extract label
		label := string(data[offset+1 : offset+1+length])
		parts = append(parts, label)
		offset += 1 + length
	}

	if jumped {
		offset = jumpOffset
	}

	return strings.Join(parts, "."), offset
}

// isPrinterHostname checks if a hostname likely belongs to a printer
func isPrinterHostname(hostname string) bool {
	hostname = strings.ToLower(hostname)

	// Common printer hostname patterns
	printerKeywords := []string{
		"print", "printer", "mfp", "copier", "scanner",
		"hp", "canon", "epson", "brother", "xerox", "ricoh",
		"laserjet", "deskjet", "officejet", "colorjet",
		"bizhub", "imagerunner", "workcentre",
	}

	for _, keyword := range printerKeywords {
		if strings.Contains(hostname, keyword) {
			return true
		}
	}

	// Match patterns like "PRN-", "PRINT-", "MFP-" prefixes
	if len(hostname) > 4 {
		prefix := hostname[:4]
		if prefix == "prn-" || prefix == "mfp-" {
			return true
		}
	}

	return false
}
