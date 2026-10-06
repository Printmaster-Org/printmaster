package scanner

import (
	"errors"
	"net/netip"
	"testing"
	"time"
)

// All fixtures are values with a fixed, explicit clock. No network, DB, backend,
// shared caches or mutable package globals are needed to exercise every policy.
func workFixture(t *testing.T, intent Intent, policy workPolicy) (workPlanner, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p, err := newWorkPlanner(Observation{IP: netip.MustParseAddr("192.0.2.1"), Source: SourceManual, ObservedAt: now}, intent, policy)
	if err != nil {
		t.Fatal(err)
	}
	return p, now
}

func workProvenance(p workPlanner, now time.Time, protocol FactProtocol) Provenance {
	return Provenance{IP: p.observation.IP, Source: p.observation.Source, ReceivedAt: now, Protocol: protocol, Field: "test-field"}
}

func workIdentity(p workPlanner, now time.Time) identityFacts {
	return identityFacts{Provenance: workProvenance(p, now, ProtocolSNMP), Serial: "SERIAL-1", Class: printerConfirmed}
}

func workMetrics(p workPlanner, now time.Time) metricsFacts {
	return metricsFacts{Provenance: workProvenance(p, now, ProtocolSNMP), Serial: "SERIAL-1", HasPageCount: true, PageCount: 0}
}

func workReachable(t *testing.T, p workPlanner, now time.Time) workPlanner {
	t.Helper()
	q, err := p.recordTCP(tcpFacts{Provenance: workProvenance(p, now, ProtocolTCP), OpenPort: 9100}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func workIdentified(t *testing.T, p workPlanner, now time.Time) workPlanner {
	t.Helper()
	p = workReachable(t, p, now)
	q, err := p.recordIdentity(workIdentity(p, now), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func requireWorkNext(t *testing.T, p workPlanner, now time.Time, want WorkStage, active bool) workPlanner {
	t.Helper()
	q, stage, ok := p.next(now)
	if ok != active || (ok && stage != want) {
		t.Fatalf("next = (%d, %v), want (%d, %v); outcomes=%+v", stage, ok, want, active, q.outcomes)
	}
	return q
}

func requireWorkOutcome(t *testing.T, p workPlanner, stage WorkStage, status StageStatus, reason StageReason) {
	t.Helper()
	if o := p.outcomes[stage]; o.Stage != stage || o.Status != status || o.Reason != reason {
		t.Fatalf("stage %d = %+v, want status=%d reason=%d", stage, o, status, reason)
	}
}

func TestWorkPresets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		preset IntentPreset
		want   Intent
	}{
		{IntentQuick, Intent{Reachability: true, Identity: true}},
		{IntentFull, Intent{Reachability: true, Identity: true, Detail: true, MetricsRequested: true}},
		{IntentLive, Intent{Reachability: true, Identity: true, Detail: true}},
		{IntentManual, Intent{Reachability: true, Identity: true}},
		{IntentLiveness, Intent{Reachability: true, Identity: true}},
		{IntentMetrics, Intent{Reachability: true, Identity: true, MetricsRequested: true}},
	} {
		t.Run(string(rune('0'+tc.preset)), func(t *testing.T) {
			t.Parallel()
			got, err := PresetIntent(tc.preset)
			if err != nil || got != tc.want {
				t.Fatalf("preset = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
	if _, err := PresetIntent(0); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestWorkConstruction(t *testing.T) {
	t.Parallel()
	for _, target := range []netip.Addr{{}, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::"), netip.MustParseAddr("224.0.0.1"), netip.MustParseAddr("::ffff:0.0.0.0"), netip.MustParseAddr("::ffff:224.0.0.1")} {
		if _, err := newWorkPlanner(Observation{IP: target}, Intent{}, workPolicy{}); err == nil {
			t.Fatalf("invalid target %s accepted", target)
		}
	}
	if _, err := newWorkPlanner(Observation{IP: netip.MustParseAddr("192.0.2.1"), Source: 255}, Intent{}, workPolicy{}); err == nil {
		t.Fatal("invalid source accepted")
	}
	for _, intent := range []Intent{{Identity: true}, {Detail: true}, {MetricsRequested: true}, {MetricsDue: true}} {
		p, now := workFixture(t, intent, workPolicy{})
		if !p.intent.Identity || !p.intent.Reachability {
			t.Fatalf("dependencies not normalized: %+v", p.intent)
		}
		requireWorkNext(t, p, now, StageReachability, true)
	}
	p, now := workFixture(t, Intent{}, workPolicy{})
	q := requireWorkNext(t, p, now, 0, false)
	for stage := StageReachability; stage < workStageCount; stage++ {
		requireWorkOutcome(t, p, stage, StatusNotChecked, ReasonNone)
		requireWorkOutcome(t, q, stage, StatusSkipped, ReasonNotRequested)
	}
	if q.canCommit(now) {
		t.Fatal("empty intent allowed commit")
	}
	observation := p.observation
	observation.IP = netip.MustParseAddr("::ffff:192.0.2.1")
	normalized, err := newWorkPlanner(observation, Intent{}, workPolicy{})
	if err != nil || normalized.observation.IP != p.observation.IP {
		t.Fatalf("IPv4-mapped target not normalized: %+v, %v", normalized, err)
	}
}

func TestWorkProtocolEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		source   ObservationSource
		protocol FactProtocol
		message  protocolMessage
	}{
		{"mdns", SourceMDNS, ProtocolMDNS, messageMDNSAnswer},
		{"ssdp", SourceSSDP, ProtocolSSDP, messageSSDPResponse},
		{"wsd", SourceWSD, ProtocolWSD, messageWSDResponse},
		{"trap", SourceTrap, ProtocolTrap, messageTrap},
		{"llmnr", SourceLLMNR, ProtocolLLMNR, messageLLMNRAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, now := workFixture(t, Intent{Identity: true}, workPolicy{})
			p.observation.Source = tc.source
			p.observation.ObservedAt = now.Add(-24 * time.Hour) // Remote metadata does not control receipt.
			p.observation.Known = KnownDeviceHint{Serial: "SERIAL-1", IP: p.observation.IP}
			p.observation.Hints = ProtocolHints{MDNS: MDNSHint{Service: "_ipp._tcp"}, SSDP: SSDPHint{Location: "http://192.0.2.1"}, WSD: WSDHint{Types: "Printer"}, Trap: TrapHint{TrapOID: "1.2.3"}, LLMNR: LLMNRHint{Hostname: "printer"}}
			// External hints and a known IP/serial cannot skip even reachability.
			requireWorkNext(t, p, now, StageReachability, true)
			base := protocolFact{Provenance: workProvenance(p, now, tc.protocol), Message: tc.message}
			for _, variation := range []struct {
				name   string
				edit   func(*protocolFact)
				accept bool
			}{
				{"now", func(*protocolFact) {}, true},
				{"boundary", func(f *protocolFact) { f.ReceivedAt = now.Add(-30 * time.Second) }, true},
				{"old", func(f *protocolFact) { f.ReceivedAt = now.Add(-30*time.Second - time.Nanosecond) }, false},
				{"future", func(f *protocolFact) { f.ReceivedAt = now.Add(time.Nanosecond) }, false},
				{"zero", func(f *protocolFact) { f.ReceivedAt = time.Time{} }, false},
				{"other-target", func(f *protocolFact) { f.IP = netip.MustParseAddr("192.0.2.2") }, false},
				{"no-target", func(f *protocolFact) { f.IP = netip.Addr{} }, false},
				{"wrong-protocol", func(f *protocolFact) { f.Protocol = ProtocolTCP }, false},
				{"wrong-source", func(f *protocolFact) { f.Source = SourceManual }, false},
				{"unvalidated-message", func(f *protocolFact) { f.Message = messageUnknown }, false},
			} {
				t.Run(variation.name, func(t *testing.T) {
					f := base
					variation.edit(&f)
					q := p.withProtocol(f, now)
					if variation.accept {
						requireWorkOutcome(t, q, StageReachability, StatusSkipped, ReasonFreshProtocol)
						requireWorkNext(t, q, now, StageIdentity, true) // Never skip identity.
						if q.outcomes[StageReachability].Provenance != f.Provenance || q.canCommit(now) {
							t.Fatal("lost provenance or trusted identity hint")
						}
					} else {
						requireWorkNext(t, q, now, StageReachability, true)
					}
				})
			}
			q := p.withProtocol(base, now)
			requireWorkNext(t, q, now.Add(31*time.Second), StageReachability, true)
			requireWorkNext(t, p.withProtocol(base, time.Time{}), now, StageReachability, true)
		})
	}
	p, now := workFixture(t, Intent{}, workPolicy{})
	p.observation.Source = SourceMDNS
	f := protocolFact{Provenance: workProvenance(p, now, ProtocolMDNS), Message: messageMDNSAnswer}
	if p.withProtocol(f, now) != p {
		t.Fatal("evidence enabled unrequested work")
	}
}

func TestWorkTCPAndSourceFailure(t *testing.T) {
	t.Parallel()
	for _, proceed := range []bool{false, true} {
		for _, backendError := range []bool{false, true} {
			p, now := workFixture(t, Intent{Detail: true, MetricsDue: true}, workPolicy{SNMPAfterTCPFailure: proceed})
			original := p
			p = p.withSourceFailure(errors.New("source socket failed"))
			if p.sourceError != "source socket failed" || original.sourceError != "" {
				t.Fatal("source failure lost or mutated input")
			}
			requireWorkNext(t, p, now, StageReachability, true)
			var err error
			if backendError {
				err = errors.New("TCP probe failed")
			}
			q, orderErr := p.recordTCP(tcpFacts{Provenance: workProvenance(p, now, ProtocolTCP)}, err, now)
			if orderErr != nil {
				t.Fatal(orderErr)
			}
			if backendError {
				requireWorkOutcome(t, q, StageReachability, StatusFailed, ReasonBackendFailure)
				if q.outcomes[StageReachability].Error != err.Error() {
					t.Fatal("backend error lost")
				}
			} else {
				requireWorkOutcome(t, q, StageReachability, StatusNegative, ReasonNoTCPResponse)
			}
			q = requireWorkNext(t, q, now, StageIdentity, proceed)
			if !proceed {
				requireWorkOutcome(t, q, StageIdentity, StatusSkipped, ReasonReachabilityRequired)
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonIdentityRequired)
			} else {
				q, orderErr = q.recordIdentity(workIdentity(q, now), nil, now)
				if orderErr != nil || !q.canCommit(now) {
					t.Fatalf("SNMP after TCP failure rejected: %v", orderErr)
				}
			}
		}
	}
	p, now := workFixture(t, Intent{Reachability: true}, workPolicy{})
	q := workReachable(t, p, now)
	requireWorkOutcome(t, q, StageReachability, StatusSucceeded, ReasonNone)
	q = requireWorkNext(t, q, now, 0, false)
	requireWorkOutcome(t, q, StageIdentity, StatusSkipped, ReasonNotRequested)
	if q.canCommit(now) || p.withSourceFailure(nil) != p {
		t.Fatal("reachability attributed identity or nil source error mutated state")
	}
}

func TestWorkIdentityDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		edit   func(*identityFacts)
		err    error
		known  string
		status StageStatus
		reason StageReason
	}{
		{"valid", func(*identityFacts) {}, nil, "", StatusSucceeded, ReasonNone},
		{"known-match", func(f *identityFacts) { f.Serial = " SERIAL-1 " }, nil, "SERIAL-1", StatusSucceeded, ReasonNone},
		{"known-conflict", func(*identityFacts) {}, nil, "OTHER", StatusFailed, ReasonIdentityConflict},
		{"not-printer", func(f *identityFacts) { f.Class = printerOther }, nil, "", StatusNegative, ReasonNotPrinter},
		{"not-classified", func(f *identityFacts) { f.Class = printerUnknown }, nil, "", StatusNegative, ReasonNotPrinter},
		{"no-serial", func(f *identityFacts) { f.Serial = " " }, nil, "", StatusNegative, ReasonMissingSerial},
		{"wrong-target", func(f *identityFacts) { f.IP = netip.MustParseAddr("192.0.2.2") }, nil, "", StatusFailed, ReasonInvalidFacts},
		{"future", func(f *identityFacts) { f.ReceivedAt = f.ReceivedAt.Add(time.Nanosecond) }, nil, "", StatusFailed, ReasonInvalidFacts},
		{"old", func(f *identityFacts) { f.ReceivedAt = f.ReceivedAt.Add(-31 * time.Second) }, nil, "", StatusFailed, ReasonInvalidFacts},
		{"wrong-protocol", func(f *identityFacts) { f.Protocol = ProtocolMDNS }, nil, "", StatusFailed, ReasonInvalidFacts},
		{"invalid-class", func(f *identityFacts) { f.Class = 255 }, nil, "", StatusFailed, ReasonInvalidFacts},
		{"backend-error", func(*identityFacts) {}, errors.New("SNMP failed"), "", StatusFailed, ReasonBackendFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, now := workFixture(t, Intent{Detail: true, MetricsRequested: true}, workPolicy{})
			p.observation.Known = KnownDeviceHint{Serial: tc.known, IP: netip.MustParseAddr("192.0.2.9")}
			p = workReachable(t, p, now)
			f := workIdentity(p, now)
			tc.edit(&f)
			q, err := p.recordIdentity(f, tc.err, now)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkOutcome(t, q, StageIdentity, tc.status, tc.reason)
			if q.canCommit(now) != (tc.status == StatusSucceeded) || q.canCommit(now.Add(2*time.Minute)) != (tc.status == StatusSucceeded) {
				t.Fatal("unsafe commit gate")
			}
			q = requireWorkNext(t, q, now, StageDetail, tc.status == StatusSucceeded)
			if tc.reason == ReasonIdentityConflict {
				requireWorkOutcome(t, q, StageDetail, StatusSkipped, ReasonIdentityConflict)
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonIdentityConflict)
			} else if tc.status != StatusSucceeded {
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonIdentityRequired)
			}
		})
	}
}

func TestWorkDetailAndMetricsReuse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		intent   Intent
		edit     func(*detailFacts)
		err      error
		status   StageStatus
		reason   StageReason
		metrics  bool
		conflict bool
	}{
		{"reuse-requested", Intent{Detail: true, MetricsRequested: true}, func(*detailFacts) {}, nil, StatusSucceeded, ReasonNone, false, false},
		{"reuse-due", Intent{Detail: true, MetricsDue: true}, func(*detailFacts) {}, nil, StatusSucceeded, ReasonNone, false, false},
		{"not-due", Intent{Detail: true}, func(*detailFacts) {}, nil, StatusSucceeded, ReasonNone, false, false},
		{"no-metrics", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Metrics = metricsFacts{} }, nil, StatusSucceeded, ReasonNone, true, false},
		{"old-metrics", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Metrics.ReceivedAt = f.Metrics.ReceivedAt.Add(-31 * time.Second) }, nil, StatusSucceeded, ReasonNone, true, false},
		{"future-metrics", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Metrics.ReceivedAt = f.Metrics.ReceivedAt.Add(time.Nanosecond) }, nil, StatusSucceeded, ReasonNone, true, false},
		{"other-IP-metrics", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Metrics.IP = netip.MustParseAddr("192.0.2.2") }, nil, StatusSucceeded, ReasonNone, true, false},
		{"no-detail", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Model = "" }, nil, StatusNegative, ReasonNoDetail, false, false},
		{"identity-conflict", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Identity.Serial = "OTHER"; f.Identity.Class = printerOther }, nil, StatusFailed, ReasonIdentityConflict, false, true},
		{"metrics-conflict", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Metrics.Serial = "OTHER" }, nil, StatusFailed, ReasonIdentityConflict, false, true},
		{"no-identity", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Identity.Serial = "" }, nil, StatusFailed, ReasonInvalidFacts, true, false},
		{"other-target", Intent{Detail: true, MetricsDue: true}, func(f *detailFacts) { f.Identity.IP = netip.MustParseAddr("192.0.2.2") }, nil, StatusFailed, ReasonInvalidFacts, true, false},
		{"backend-error", Intent{Detail: true, MetricsDue: true}, func(*detailFacts) {}, errors.New("walk failed"), StatusFailed, ReasonBackendFailure, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, now := workFixture(t, tc.intent, workPolicy{})
			p = workIdentified(t, p, now)
			f := detailFacts{Identity: workIdentity(p, now), Model: "Printer", Metrics: workMetrics(p, now)}
			tc.edit(&f)
			q, err := p.recordDetail(f, tc.err, now)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkOutcome(t, q, StageDetail, tc.status, tc.reason)
			if q.conflict != tc.conflict || q.canCommit(now) == tc.conflict {
				t.Fatal("detail conflict did not control commit")
			}
			q = requireWorkNext(t, q, now, StageMetrics, tc.metrics)
			if tc.conflict {
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonIdentityConflict)
			} else if !tc.intent.MetricsRequested && !tc.intent.MetricsDue {
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonNotRequested)
			} else if !tc.metrics {
				requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonFreshItemMetrics)
				if q.outcomes[StageMetrics].Provenance != f.Metrics.Provenance {
					t.Fatal("reuse provenance lost")
				}
			}
		})
	}
}

func TestWorkMetricsDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		edit   func(*metricsFacts)
		err    error
		status StageStatus
		reason StageReason
	}{
		{"zero-counter-valid", func(*metricsFacts) {}, nil, StatusSucceeded, ReasonNone},
		{"no-data", func(f *metricsFacts) { f.HasPageCount = false }, nil, StatusNegative, ReasonNoMetrics},
		{"no-serial", func(f *metricsFacts) { f.Serial = "" }, nil, StatusFailed, ReasonInvalidFacts},
		{"conflict", func(f *metricsFacts) { f.Serial = "OTHER" }, nil, StatusFailed, ReasonIdentityConflict},
		{"wrong-protocol", func(f *metricsFacts) { f.Protocol = ProtocolTCP }, nil, StatusFailed, ReasonInvalidFacts},
		{"wrong-IP", func(f *metricsFacts) { f.IP = netip.MustParseAddr("192.0.2.2") }, nil, StatusFailed, ReasonInvalidFacts},
		{"old", func(f *metricsFacts) { f.ReceivedAt = f.ReceivedAt.Add(-31 * time.Second) }, nil, StatusFailed, ReasonInvalidFacts},
		{"future", func(f *metricsFacts) { f.ReceivedAt = f.ReceivedAt.Add(time.Nanosecond) }, nil, StatusFailed, ReasonInvalidFacts},
		{"backend-error", func(*metricsFacts) {}, errors.New("metrics GET failed"), StatusFailed, ReasonBackendFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, now := workFixture(t, Intent{MetricsRequested: true}, workPolicy{})
			p = workIdentified(t, p, now)
			p = requireWorkNext(t, p, now, StageMetrics, true)
			requireWorkOutcome(t, p, StageDetail, StatusSkipped, ReasonNotRequested)
			f := workMetrics(p, now)
			tc.edit(&f)
			q, err := p.recordMetrics(f, tc.err, now)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkOutcome(t, q, StageMetrics, tc.status, tc.reason)
			requireWorkNext(t, q, now, 0, false)
			if q.canCommit(now) == (tc.reason == ReasonIdentityConflict) {
				t.Fatal("metrics conflict did not halt commit")
			}
		})
	}
}

func TestWorkPureOrderingAndIsolation(t *testing.T) {
	t.Parallel()
	p, now := workFixture(t, Intent{Detail: true, MetricsRequested: true}, workPolicy{})
	if q, err := p.recordIdentity(workIdentity(p, now), nil, now); err == nil || q != p {
		t.Fatal("out-of-order identity accepted or mutated input")
	}
	if q, err := p.recordDetail(detailFacts{}, nil, now); err == nil || q != p {
		t.Fatal("out-of-order detail accepted or mutated input")
	}
	if q, err := p.recordMetrics(workMetrics(p, now), nil, now); err == nil || q != p {
		t.Fatal("out-of-order metrics accepted or mutated input")
	}
	q := workIdentified(t, p, now)
	if p.outcomes[StageIdentity].Status != StatusNotChecked {
		t.Fatal("record mutated original value")
	}
	if repeated, err := q.recordTCP(tcpFacts{}, nil, now); err == nil || repeated != q {
		t.Fatal("duplicate TCP result accepted")
	}
	a, stageA, okA := q.next(now)
	b, stageB, okB := q.next(now)
	if a != b || stageA != stageB || okA != okB {
		t.Fatal("planning not deterministic")
	}
	// A second observation of the same IP and known serial still performs identity.
	other, err := newWorkPlanner(Observation{IP: p.observation.IP, Source: SourceRange, Known: KnownDeviceHint{Serial: q.identity.Serial, IP: p.observation.IP}}, p.intent, p.policy)
	if err != nil {
		t.Fatal(err)
	}
	other = workReachable(t, other, now)
	requireWorkNext(t, other, now, StageIdentity, true)
	if other.canCommit(now) {
		t.Fatal("cross-item identity reused")
	}
	// Ingested same-item identity survives a long walk; other items still recheck.
	longWalk := requireWorkNext(t, q, now.Add(2*time.Minute), StageDetail, true)
	if !longWalk.canCommit(now.Add(2 * time.Minute)) {
		t.Fatal("same-item identity expired")
	}
	longWalk, err = longWalk.recordDetail(detailFacts{Identity: longWalk.identity, Model: "Printer"}, nil, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireWorkNext(t, longWalk, now.Add(2*time.Minute), StageMetrics, true)
	// A cached same-item sample can age out while its later identity stays fresh.
	f := detailFacts{Identity: workIdentity(q, now.Add(20*time.Second)), Model: "Printer", Metrics: workMetrics(q, now)}
	q, err = q.recordDetail(f, nil, now.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	requireWorkNext(t, q, now.Add(31*time.Second), StageMetrics, true)
}

func TestWorkReceiptExpiresDuringIdentityQuery(t *testing.T) {
	t.Parallel()
	p, now := workFixture(t, Intent{Identity: true}, workPolicy{})
	p.observation.Source = SourceMDNS
	p = p.withProtocol(protocolFact{Provenance: workProvenance(p, now.Add(-29*time.Second), ProtocolMDNS), Message: messageMDNSAnswer}, now)
	dispatched, stage, ok := p.dispatch(now)
	if !ok || stage != StageIdentity {
		t.Fatal("fresh receipt did not admit identity")
	}
	finished := now.Add(2 * time.Second)
	identified, err := dispatched.recordIdentity(workIdentity(dispatched, finished), nil, finished)
	if err != nil || !identified.canCommit(finished) {
		t.Fatalf("in-flight identity was rewound: %+v, %v", identified, err)
	}
	requireWorkOutcome(t, identified, StageReachability, StatusSkipped, ReasonFreshProtocol)
	// The same receipt must expire if its item has not yet been dispatched.
	requireWorkNext(t, p, finished, StageReachability, true)
}

func TestWorkInvalidTCPFacts(t *testing.T) {
	t.Parallel()
	for _, edit := range []func(*tcpFacts){
		func(f *tcpFacts) { f.Protocol = ProtocolSNMP },
		func(f *tcpFacts) { f.IP = netip.MustParseAddr("192.0.2.2") },
		func(f *tcpFacts) { f.ReceivedAt = time.Time{} },
		func(f *tcpFacts) { f.ReceivedAt = f.ReceivedAt.Add(time.Nanosecond) },
		func(f *tcpFacts) { f.ReceivedAt = f.ReceivedAt.Add(-31 * time.Second) },
	} {
		p, now := workFixture(t, Intent{Reachability: true}, workPolicy{})
		f := tcpFacts{Provenance: workProvenance(p, now, ProtocolTCP), OpenPort: 9100}
		edit(&f)
		q, err := p.recordTCP(f, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		requireWorkOutcome(t, q, StageReachability, StatusFailed, ReasonInvalidFacts)
	}
}
