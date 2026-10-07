package scanner

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

// stageLogInfo captures what a stage actually did so logs can explain it
// without exposing PDU values, serials, credentials or backend error text.
type stageLogInfo struct {
	attempts int
	profiles []QueryProfile
	timeout  int
	reused   bool
}

type logLevel uint8

const (
	logDebug logLevel = iota
	logInfo
	logWarn
	logError
)

func (c *Coordinator) logStage(req Request, o StageOutcome, stageErr error, elapsed time.Duration, info stageLogInfo) {
	if c.cfg.Logger == nil {
		return
	}
	errType := classifyStageError(stageErr)
	known := req.Observation.Known.Serial != ""
	args := []interface{}{
		"ip", req.Observation.IP.String(),
		"stage", o.Stage.String(),
		"status", o.Status.String(),
		"reason", o.Reason.String(),
		"duration", formatLogDuration(elapsed),
		"source", req.Observation.Source.String(),
		"request", req.Preset.String(),
		"known_device", known,
	}
	if o.Stage == StageReachability {
		args = append(args, "protocol", "TCP", "ports", portsLabel(req.Options.Ports))
		if info.attempts > 0 {
			args = append(args, "attempts", info.attempts)
		}
	} else if info.reused {
		args = append(args, "protocol", "SNMP", "query", "reused previous response")
	} else if len(info.profiles) > 0 {
		labels := make([]string, len(info.profiles))
		for i, p := range info.profiles {
			labels[i] = queryProfileLabel(p)
		}
		args = append(args, "protocol", "SNMP", "query", strings.Join(labels, " then "), "attempts", info.attempts)
		if info.timeout > 0 {
			args = append(args, "timeout", (time.Duration(info.timeout) * time.Second).String())
		}
	}
	args = append(args, "error", stageErr != nil)
	if errType != "" {
		args = append(args, "error_type", errType)
	}

	level, msg := stageLogMessage(o, errType, known)
	switch level {
	case logError:
		c.cfg.Logger.Error(msg, args...)
	case logWarn:
		c.cfg.Logger.Warn(msg, args...)
	case logInfo:
		c.cfg.Logger.Info(msg, args...)
	default:
		c.cfg.Logger.Debug(msg, args...)
	}
}

// stageLogMessage keeps routine discovery misses (empty addresses, non-printers,
// non-SNMP hosts) at debug so sweeping a large range doesn't flood warnings.
// Problems with devices we already know, or unexpected errors, stay visible.
func stageLogMessage(o StageOutcome, errType string, known bool) (logLevel, string) {
	stage := o.Stage.String()
	switch o.Status {
	case StatusSucceeded:
		if o.Stage == StageReachability {
			return logDebug, "Scanner: host reachable"
		}
		return logInfo, "Scanner " + stage + " check succeeded"
	case StatusSkipped, StatusNotChecked:
		return logDebug, "Scanner " + stage + " check " + o.Status.String() + ": " + o.Reason.String()
	case StatusNegative:
		msg := "Scanner " + stage + " check: " + o.Reason.String()
		switch o.Reason {
		case ReasonNoTCPResponse, ReasonNotPrinter:
			return logDebug, msg
		case ReasonMissingSerial:
			if !known {
				return logDebug, msg
			}
		}
		return logWarn, msg
	case StatusFailed:
		reason := o.Reason.String()
		if o.Reason == ReasonBackendFailure {
			reason = failureDescription(o.Stage, errType)
		} else if errType != "" && errType != reason {
			reason += " (" + errType + ")"
		}
		msg := "Scanner " + stage + " check failed: " + reason
		if errType == "canceled" {
			return logDebug, msg
		}
		// Unknown addresses that never answer SNMP are empty IPs or non-SNMP hosts.
		if o.Stage == StageIdentity && errType == "timeout" {
			if known {
				return logWarn, msg
			}
			return logDebug, msg
		}
		return logError, msg
	}
	return logDebug, "Scanner " + stage + " check " + o.Status.String()
}

func failureDescription(stage WorkStage, errType string) string {
	var what string
	switch stage {
	case StageReachability:
		what = "TCP probe error"
	case StageIdentity:
		what = "SNMP identity query error"
	case StageDetail:
		what = "SNMP detail walk error"
	case StageMetrics:
		what = "SNMP metrics query error"
	default:
		what = "query error"
	}
	switch errType {
	case "":
		return what
	case "timeout":
		if stage == StageReachability {
			return "TCP probe timed out"
		}
		return "no SNMP response before timeout"
	default:
		return what + " (" + errType + ")"
	}
}

// classifyStageError returns a short category, never the raw error text, which
// may include community strings or device payloads.
func classifyStageError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var outcome *OutcomeError
	if errors.As(err, &outcome) {
		return outcome.Outcome.Reason.String()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout"), strings.Contains(s, "timed out"), strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "host unreachable"), strings.Contains(s, "network is unreachable"):
		return "network unreachable"
	case strings.Contains(s, "permission denied"), strings.Contains(s, "access is denied"):
		return "permission denied"
	case strings.Contains(s, "authentication"), strings.Contains(s, "unknown user"), strings.Contains(s, "wrong digest"), strings.Contains(s, "decrypt"):
		return "SNMP authentication failed"
	case strings.Contains(s, "mismatch"), strings.Contains(s, "unrequested port"):
		return "unexpected response"
	case strings.Contains(s, "nil query response"):
		return "empty response"
	default:
		return "other error"
	}
}

func formatLogDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}

func portsLabel(ports []uint16) string {
	if len(ports) == 0 {
		return "default"
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(int(p))
	}
	return strings.Join(parts, ", ")
}
