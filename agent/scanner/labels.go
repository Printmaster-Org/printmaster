package scanner

import "fmt"

// Human-readable labels for scanner enums. Used in logs and error messages so
// operators see "identity" / "no TCP response" rather than raw numeric codes.

func (s ObservationSource) String() string {
	switch s {
	case SourceUnknown:
		return "unknown"
	case SourceMDNS:
		return "mDNS"
	case SourceSSDP:
		return "SSDP"
	case SourceWSD:
		return "WS-Discovery"
	case SourceTrap:
		return "SNMP trap"
	case SourceLLMNR:
		return "LLMNR"
	case SourceManual:
		return "manual"
	case SourceRange:
		return "IP range scan"
	default:
		return fmt.Sprintf("source(%d)", uint8(s))
	}
}

func (p IntentPreset) String() string {
	switch p {
	case 0:
		return "custom"
	case IntentQuick:
		return "quick"
	case IntentFull:
		return "full"
	case IntentLive:
		return "live discovery"
	case IntentManual:
		return "manual refresh"
	case IntentLiveness:
		return "liveness check"
	case IntentMetrics:
		return "metrics collection"
	default:
		return fmt.Sprintf("preset(%d)", uint8(p))
	}
}

func (s WorkStage) String() string {
	switch s {
	case StageReachability:
		return "reachability"
	case StageIdentity:
		return "identity"
	case StageDetail:
		return "detail"
	case StageMetrics:
		return "metrics"
	default:
		return fmt.Sprintf("stage(%d)", uint8(s))
	}
}

func (s StageStatus) String() string {
	switch s {
	case StatusNotChecked:
		return "not checked"
	case StatusSucceeded:
		return "succeeded"
	case StatusNegative:
		return "negative"
	case StatusSkipped:
		return "skipped"
	case StatusFailed:
		return "failed"
	default:
		return fmt.Sprintf("status(%d)", uint8(s))
	}
}

func (r StageReason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonNotRequested:
		return "not requested"
	case ReasonFreshProtocol:
		return "recent protocol response already available"
	case ReasonNoTCPResponse:
		return "no TCP response (no device at this address or ports closed)"
	case ReasonBackendFailure:
		return "probe/query error"
	case ReasonReachabilityRequired:
		return "host not reachable over TCP"
	case ReasonIdentityRequired:
		return "printer identity not confirmed"
	case ReasonNotPrinter:
		return "device is not a printer"
	case ReasonMissingSerial:
		return "device did not report a serial number"
	case ReasonIdentityConflict:
		return "serial number does not match the known device"
	case ReasonNoDetail:
		return "no detail data returned"
	case ReasonNoMetrics:
		return "no metrics data returned"
	case ReasonFreshItemMetrics:
		return "recent metrics already collected"
	case ReasonInvalidFacts:
		return "invalid or stale response"
	default:
		return fmt.Sprintf("reason(%d)", uint8(r))
	}
}

func queryProfileLabel(p QueryProfile) string {
	switch p {
	case QueryMinimal:
		return "minimal (serial only)"
	case QueryEssential:
		return "essential (serial, supplies, pages, status)"
	case QueryFull:
		return "full walk"
	case QueryMetrics:
		return "metrics"
	default:
		return p.String()
	}
}
