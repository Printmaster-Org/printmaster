package agent

import (
	"strings"

	"printmaster/agent/scanner"
	"printmaster/common/snmp/oids"

	"github.com/gosnmp/gosnmp"
)

// MatchesSerialIdentity is deliberately side-effect free: the full discovery
// parser also probes web UIs and writes diagnostics, unsuitable for liveness.
func MatchesSerialIdentity(serial, learnedOID string, pdus []gosnmp.SnmpPDU) bool {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return false
	}
	for _, pdu := range pdus {
		if pdu.Type != gosnmp.OctetString {
			continue
		}
		name := strings.TrimPrefix(pdu.Name, ".")
		value := strings.TrimSpace(strings.Trim(pduToString(pdu.Value), "\x00"))
		if _, ok := scanner.LookupVendorIDTarget(name); ok {
			fields := parseDeviceIDPayload(value)
			value = strings.TrimSpace(firstNonEmpty(fields["sn"], fields["serial"], fields["ser"]))
		} else if name != oids.PrtGeneralSerialNumber && (learnedOID == "" || name != strings.TrimPrefix(learnedOID, ".")) {
			continue
		}
		if value == serial {
			return true
		}
	}
	return false
}
