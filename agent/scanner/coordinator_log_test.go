package scanner

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type levelTestLogger struct {
	mu    sync.Mutex
	lines map[string][]string
}

func (l *levelTestLogger) log(level, msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lines == nil {
		l.lines = make(map[string][]string)
	}
	l.lines[level] = append(l.lines[level], msg+" "+fmt.Sprint(args...))
}
func (l *levelTestLogger) Debug(msg string, args ...interface{}) { l.log("debug", msg, args...) }
func (l *levelTestLogger) Info(msg string, args ...interface{})  { l.log("info", msg, args...) }
func (l *levelTestLogger) Warn(msg string, args ...interface{})  { l.log("warn", msg, args...) }
func (l *levelTestLogger) Error(msg string, args ...interface{}) { l.log("error", msg, args...) }

func TestCoordinatorUnreachableHostLogsAtDebugOnly(t *testing.T) {
	t.Parallel()
	log := &levelTestLogger{}
	c := coordinatorFixture(t, CoordinatorConfig{Logger: log, Backend: Backend{
		Probe: func(context.Context, netip.Addr) ([]uint16, error) { return nil, nil },
	}})
	if _, err := runCoordinator(t, c, coordinatorRequest(IntentQuick)); err == nil {
		t.Fatal("expected negative outcome")
	}
	if len(log.lines["warn"])+len(log.lines["error"])+len(log.lines["info"]) != 0 {
		t.Fatalf("unreachable host must not log above debug: %v", log.lines)
	}
	joined := strings.Join(log.lines["debug"], "\n")
	if !strings.Contains(joined, "no TCP response") || !strings.Contains(joined, "IP range scan") {
		t.Fatalf("debug log lacks readable context: %s", joined)
	}
}

func TestCoordinatorFailureLogIsReadable(t *testing.T) {
	t.Parallel()
	log := &levelTestLogger{}
	c := coordinatorFixture(t, CoordinatorConfig{Logger: log, Backend: Backend{Query: func(context.Context, QueryRequest) (*QueryResult, error) {
		return nil, errors.New("snmp: wrong digest for user secret-community")
	}}})
	if _, err := runCoordinator(t, c, coordinatorRequest(IntentQuick)); err == nil {
		t.Fatal("failure swallowed")
	}
	if len(log.lines["error"]) != 1 {
		t.Fatalf("expected one error, got %v", log.lines)
	}
	line := log.lines["error"][0]
	for _, want := range []string{"Scanner identity check failed", "SNMP identity query error (SNMP authentication failed)", "stageidentity", "statusfailed", "minimal (serial only)", "attempts1"} {
		if !strings.Contains(line, want) {
			t.Errorf("error log missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, "secret-community") {
		t.Fatalf("backend error text leaked: %s", line)
	}
}

func TestStageLogLevels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		o       StageOutcome
		errType string
		known   bool
		level   logLevel
		msg     string
	}{
		{"no-tcp", StageOutcome{Stage: StageReachability, Status: StatusNegative, Reason: ReasonNoTCPResponse}, "", false, logDebug, "no TCP response"},
		{"not-printer", StageOutcome{Stage: StageIdentity, Status: StatusNegative, Reason: ReasonNotPrinter}, "", false, logDebug, "not a printer"},
		{"missing-serial-unknown", StageOutcome{Stage: StageIdentity, Status: StatusNegative, Reason: ReasonMissingSerial}, "", false, logDebug, "serial"},
		{"missing-serial-known", StageOutcome{Stage: StageIdentity, Status: StatusNegative, Reason: ReasonMissingSerial}, "", true, logWarn, "serial"},
		{"no-metrics", StageOutcome{Stage: StageMetrics, Status: StatusNegative, Reason: ReasonNoMetrics}, "", true, logWarn, "no metrics"},
		{"identity-timeout-unknown", StageOutcome{Stage: StageIdentity, Status: StatusFailed, Reason: ReasonBackendFailure}, "timeout", false, logDebug, "no SNMP response before timeout"},
		{"identity-timeout-known", StageOutcome{Stage: StageIdentity, Status: StatusFailed, Reason: ReasonBackendFailure}, "timeout", true, logWarn, "no SNMP response before timeout"},
		{"detail-timeout", StageOutcome{Stage: StageDetail, Status: StatusFailed, Reason: ReasonBackendFailure}, "timeout", true, logError, "Scanner detail check failed: no SNMP response before timeout"},
		{"canceled", StageOutcome{Stage: StageDetail, Status: StatusFailed, Reason: ReasonBackendFailure}, "canceled", true, logDebug, "canceled"},
		{"conflict", StageOutcome{Stage: StageIdentity, Status: StatusFailed, Reason: ReasonIdentityConflict}, ReasonIdentityConflict.String(), true, logError, "does not match the known device"},
		{"identity-ok", StageOutcome{Stage: StageIdentity, Status: StatusSucceeded}, "", false, logInfo, "identity check succeeded"},
		{"reachable", StageOutcome{Stage: StageReachability, Status: StatusSucceeded}, "", false, logDebug, "reachable"},
	} {
		level, msg := stageLogMessage(tc.o, tc.errType, tc.known)
		if level != tc.level || !strings.Contains(msg, tc.msg) {
			t.Errorf("%s: got level %d %q", tc.name, level, msg)
		}
		if strings.Count(msg, tc.msg) > 1 {
			t.Errorf("%s: repeated text in %q", tc.name, msg)
		}
	}
}

func TestClassifyStageError(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]string{
		nil:                                   "",
		context.DeadlineExceeded:              "timeout",
		fmt.Errorf("x: %w", context.Canceled): "canceled",
		errors.New("request timeout (after 1 retries)"):         "timeout",
		errors.New("dial udp: connect: connection refused"):     "connection refused",
		errors.New("query response target or profile mismatch"): "unexpected response",
		errors.New("public-community-123"):                      "other error",
	} {
		if got := classifyStageError(err); got != want {
			t.Errorf("%v: got %q want %q", err, got, want)
		}
	}
}

func TestScannerEnumLabels(t *testing.T) {
	t.Parallel()
	if StageReachability.String() != "reachability" || StatusNegative.String() != "negative" || ReasonNoTCPResponse.String() == "" {
		t.Fatal("unexpected labels")
	}
	if WorkStage(99).String() != "stage(99)" || StageStatus(99).String() != "status(99)" || StageReason(99).String() != "reason(99)" {
		t.Fatal("out-of-range values must stay identifiable")
	}
	for r := ReasonNone; r <= ReasonInvalidFacts; r++ {
		if strings.HasPrefix(r.String(), "reason(") {
			t.Errorf("reason %d has no label", r)
		}
	}
	err := (&OutcomeError{Outcome: StageOutcome{Stage: StageReachability, Status: StatusNegative, Reason: ReasonNoTCPResponse}}).Error()
	if !strings.Contains(err, "reachability") || !strings.Contains(err, "no TCP response") {
		t.Fatalf("outcome error not readable: %s", err)
	}
}
