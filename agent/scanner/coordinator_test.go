package scanner

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"printmaster/agent/scanner/capabilities"
	"printmaster/common/snmp/oids"
)

func coordinatorRequest(preset IntentPreset) Request {
	return Request{Observation: Observation{IP: netip.MustParseAddr("192.0.2.1"), Source: SourceRange}, Preset: preset}
}

func serialPDU(serial string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oids.PrtGeneralSerialNumber, Type: gosnmp.OctetString, Value: []byte(serial)}
}

func counterPDU(oid string, count uint64) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Counter64, Value: count}
}

func coordinatorResponse(q QueryRequest, pdus ...gosnmp.SnmpPDU) *QueryResult {
	return &QueryResult{IP: q.IP.String(), Profile: q.Profile, PDUs: pdus}
}

func coordinatorSnapshot(q QueryRequest, pdus ...gosnmp.SnmpPDU) *QueryResult {
	pdus = append(pdus,
		gosnmp.SnmpPDU{Name: oids.PrtMarkerSuppliesDesc + ".1.1", Type: gosnmp.OctetString, Value: []byte("Black toner")},
		gosnmp.SnmpPDU{Name: oids.PrtMarkerSuppliesLevel + ".1.1", Type: gosnmp.Integer, Value: 0},
		gosnmp.SnmpPDU{Name: oids.PrtMarkerSuppliesMaxCap + ".1.1", Type: gosnmp.Integer, Value: 100},
	)
	return coordinatorResponse(q, pdus...)
}

func coordinatorFixture(t *testing.T, cfg CoordinatorConfig) *Coordinator {
	t.Helper()
	if cfg.Backend.Probe == nil {
		cfg.Backend.Probe = func(context.Context, netip.Addr) ([]uint16, error) { return []uint16{9100}, nil }
	}
	if cfg.Backend.Query == nil {
		cfg.Backend.Query = func(_ context.Context, q QueryRequest) (*QueryResult, error) {
			return coordinatorResponse(q, serialPDU("S1")), nil
		}
	}
	c, err := NewCoordinator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func runCoordinator(t *testing.T, c *Coordinator, r Request) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.Do(ctx, r)
}

func TestCoordinatorProfilesAndReuse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		preset   IntentPreset
		options  WorkOptions
		profiles []QueryProfile
	}{
		{"quick", IntentQuick, WorkOptions{}, []QueryProfile{QueryMinimal}},
		{"liveness", IntentLiveness, WorkOptions{}, []QueryProfile{QueryMinimal}},
		{"manual-essential", IntentManual, WorkOptions{}, []QueryProfile{QueryEssential}},
		{"manual-full-opt-in", IntentManual, WorkOptions{FullDetail: true}, []QueryProfile{QueryEssential, QueryFull}},
		{"live-essential-enrichment", IntentLive, WorkOptions{}, []QueryProfile{QueryEssential}},
		{"live-full-detail", IntentLive, WorkOptions{FullDetail: true}, []QueryProfile{QueryEssential, QueryFull}},
		{"full", IntentFull, WorkOptions{}, []QueryProfile{QueryMinimal, QueryFull}},
		{"metrics-reuse-identity", IntentMetrics, WorkOptions{}, []QueryProfile{QueryMinimal}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls []QueryProfile
			var order []WorkStage
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
				Probe: func(context.Context, netip.Addr) ([]uint16, error) {
					order = append(order, StageReachability)
					return []uint16{9100}, nil
				},
				Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
					calls = append(calls, q.Profile)
					if len(calls) == 1 {
						order = append(order, StageIdentity)
					} else {
						order = append(order, StageDetail)
					}
					return coordinatorSnapshot(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}, counterPDU(oids.PrtMarkerLifeCount+".1", 0)), nil
				},
			}})
			r := coordinatorRequest(tc.preset)
			r.Options = tc.options
			result, err := runCoordinator(t, c, r)
			if err != nil || !reflect.DeepEqual(calls, tc.profiles) {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
			if order[0] != StageReachability || order[1] != StageIdentity || result.Serial != "S1" || len(result.Queries) != len(calls) {
				t.Fatalf("result=%+v order=%v", result, order)
			}
			if result.Fields["serial"].Field != oids.PrtGeneralSerialNumber || result.Fields["model"].IP != r.Observation.IP {
				t.Fatal("field provenance lost")
			}
			if tc.preset == IntentFull || tc.preset == IntentMetrics {
				if o := result.Outcomes[StageMetrics]; o.Reason != ReasonFreshItemMetrics {
					t.Fatalf("metrics not reused: %+v", o)
				}
			}
		})
	}
}

func TestCoordinatorIdentityRawValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		pdus    []gosnmp.SnmpPDU
		learned string
		known   string
		want    string
		reason  StageReason
	}{
		{"standard", []gosnmp.SnmpPDU{serialPDU(" \x00S1\x00 ")}, "", "", "S1", ReasonNone},
		{"learned", []gosnmp.SnmpPDU{{Name: ".1.3.6.1.4.1.11.99.0", Type: gosnmp.OctetString, Value: "S1"}}, ".1.3.6.1.4.1.11.99.0", "S1", "S1", ReasonNone},
		{"structured", []gosnmp.SnmpPDU{{Name: VendorIDTargetOIDs()[0], Type: gosnmp.OctetString, Value: "MFG:HP;MDL:LaserJet;SN:S1;"}}, "", "", "S1", ReasonNone},
		{"arbitrary-text", []gosnmp.SnmpPDU{{Name: oids.SysDescr, Type: gosnmp.OctetString, Value: "SN:S1 printer"}}, "", "", "", ReasonMissingSerial},
		{"vendor-model-only", []gosnmp.SnmpPDU{{Name: VendorIDTargetOIDs()[0], Type: gosnmp.OctetString, Value: "MFG:HP;MDL:S1;"}}, "", "", "", ReasonMissingSerial},
		{"wrong-type", []gosnmp.SnmpPDU{{Name: oids.PrtGeneralSerialNumber, Type: gosnmp.NoSuchInstance, Value: "S1"}}, "", "", "", ReasonMissingSerial},
		{"expected-conflict", []gosnmp.SnmpPDU{serialPDU("S2")}, "", "S1", "", ReasonIdentityConflict},
		{"contradictory-oids", []gosnmp.SnmpPDU{serialPDU("S1"), {Name: VendorIDTargetOIDs()[0], Type: gosnmp.OctetString, Value: "SN:S2;"}}, "", "", "", ReasonIdentityConflict},
		{"contradictory-payload", []gosnmp.SnmpPDU{{Name: VendorIDTargetOIDs()[0], Type: gosnmp.OctetString, Value: "SN:S1;SER:S2;"}}, "", "", "", ReasonIdentityConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var commits atomic.Int32
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				return coordinatorResponse(q, tc.pdus...), nil
			}}, Commit: func(context.Context, Result) error { commits.Add(1); return nil }})
			r := coordinatorRequest(IntentQuick)
			r.Options.LearnedSerialOID = tc.learned
			r.Observation.Known = KnownDeviceHint{Serial: tc.known, IP: r.Observation.IP}
			r.Observation.Hints.MDNS = MDNSHint{Service: "_ipp._tcp", Hostname: "S1"}
			result, err := runCoordinator(t, c, r)
			if result.Outcomes[StageIdentity].Reason != tc.reason || result.Serial != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if (err == nil) != (tc.reason == ReasonNone) || commits.Load() != boolCount(tc.reason == ReasonNone) {
				t.Fatalf("err=%v commits=%d", err, commits.Load())
			}
		})
	}
}

func boolCount(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func TestCoordinatorLiveFallbackAndDetailConflict(t *testing.T) {
	t.Parallel()
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			var profiles []QueryProfile
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				profiles = append(profiles, q.Profile)
				if q.Profile == QueryEssential {
					return coordinatorResponse(q, gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}), nil
				}
				if q.TimeoutSeconds != 30 {
					t.Error("diagnostic timeout not preserved")
				}
				return coordinatorResponse(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}), nil
			}}})
			r := coordinatorRequest(IntentLive)
			r.Options.MissingSerialFallback = fallback
			result, err := runCoordinator(t, c, r)
			if (err == nil) != fallback || len(profiles) != 1+int(boolCount(fallback)) {
				t.Fatalf("profiles=%v err=%v", profiles, err)
			}
			if fallback && (!result.Queries[1].Diagnostic || result.Outcomes[StageDetail].Status != StatusSucceeded) {
				t.Fatal("diagnostic full not reused")
			}
		})
	}
	var committed atomic.Bool
	c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		serial := "S1"
		if q.Profile == QueryFull {
			serial = "S2"
		}
		return coordinatorResponse(q, serialPDU(serial)), nil
	}}, Commit: func(context.Context, Result) error { committed.Store(true); return nil }})
	r, err := runCoordinator(t, c, coordinatorRequest(IntentFull))
	if err == nil || committed.Load() || r.Outcomes[StageDetail].Reason != ReasonIdentityConflict || r.Outcomes[StageMetrics].Reason != ReasonIdentityConflict {
		t.Fatalf("conflict ignored: %+v %v", r, err)
	}
}

func TestCoordinatorMetricsRequestedCoverage(t *testing.T) {
	t.Parallel()
	const scanOID = "1.3.6.1.4.1.11.99.1"
	for _, includeScan := range []bool{false, true} {
		t.Run(fmt.Sprint(includeScan), func(t *testing.T) {
			var profiles []QueryProfile
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				profiles = append(profiles, q.Profile)
				pdus := []gosnmp.SnmpPDU{serialPDU("S1"), counterPDU(oids.PrtMarkerLifeCount+".1", 0), {Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}}
				if includeScan || q.Profile == QueryMetrics {
					pdus = append(pdus, counterPDU(scanOID+".0", 0))
				}
				return coordinatorResponse(q, pdus...), nil
			}}})
			r := coordinatorRequest(IntentFull)
			r.Metrics = []MetricField{{Name: "pages", OID: oids.PrtMarkerLifeCount}, {Name: "scans", OID: scanOID}}
			result, err := runCoordinator(t, c, r)
			if err != nil || len(profiles) != 3-int(boolCount(includeScan)) || result.Fields["scans"].Field != scanOID+".0" {
				t.Fatalf("coverage ignored: %v %+v %v", profiles, result, err)
			}
		})
	}
	r, _ := normalizeRequest(coordinatorRequest(IntentMetrics))
	r.Metrics = []MetricField{{Name: "scans", OID: scanOID}}
	for _, pdu := range []gosnmp.SnmpPDU{
		counterPDU(scanOID+"0.0", 10),
		{Name: scanOID + ".0", Type: gosnmp.NoSuchObject, Value: uint64(0)},
		{Name: scanOID + ".0", Type: gosnmp.OctetString, Value: "123"},
		{Name: scanOID + ".0", Type: gosnmp.Integer, Value: -1},
	} {
		f, _ := coordinatorMetrics(r, coordinatorResponse(QueryRequest{}, pdu), time.Now(), "S1")
		if f.HasRequestedFields {
			t.Fatalf("invalid metric accepted: %+v", pdu)
		}
	}
}

func TestCoordinatorKnownIPNeverSkipsAndRetries(t *testing.T) {
	t.Parallel()
	var probes, queries atomic.Int32
	transient := errors.New("transient")
	c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
		Probe: func(context.Context, netip.Addr) ([]uint16, error) { probes.Add(1); return []uint16{9100}, nil },
		Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
			if queries.Add(1) == 1 {
				return nil, transient
			}
			return coordinatorResponse(q, serialPDU("S1")), nil
		},
	}})
	r := coordinatorRequest(IntentLiveness)
	r.Observation.Known = KnownDeviceHint{Serial: "S1", IP: r.Observation.IP}
	r.Options.Retries = 1
	for i := 0; i < 2; i++ {
		if _, err := runCoordinator(t, c, r); err != nil {
			t.Fatal(err)
		}
	}
	if probes.Load() != 2 || queries.Load() != 3 {
		t.Fatalf("cross-item cache/retry: probes=%d queries=%d", probes.Load(), queries.Load())
	}
}

func TestCoordinatorPersistenceFailureAndWake(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var wakes atomic.Int32
			commitErr := errors.New("DB unavailable")
			c := coordinatorFixture(t, CoordinatorConfig{Commit: func(_ context.Context, result Result) error {
				if result.Serial != "S1" || len(result.Queries) != 1 || result.Committed {
					t.Error("invalid commit payload")
				}
				if fail {
					return commitErr
				}
				return nil
			}, Wake: func() { wakes.Add(1) }})
			r, err := runCoordinator(t, c, coordinatorRequest(IntentQuick))
			if errors.Is(err, commitErr) != fail || r.Committed == fail || wakes.Load() != boolCount(!fail) {
				t.Fatalf("commit=%v wake=%d err=%v", r.Committed, wakes.Load(), err)
			}
		})
	}
}

func waitCoordinator(t *testing.T, c *Coordinator, condition func(*Coordinator) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := condition(c)
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("coordinator condition timed out")
}

func TestCoordinatorCoalescingCancellationAndFollowup(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	c := coordinatorFixture(t, CoordinatorConfig{Workers: 3, Backend: Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		if probes.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []uint16{9100}, nil
	}, Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		return coordinatorSnapshot(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}, counterPDU(oids.PrtMarkerLifeCount+".1", 1)), nil
	}}})
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	first, second, upgrade := make(chan delivery, 1), make(chan delivery, 1), make(chan delivery, 1)
	r := coordinatorRequest(IntentQuick)
	go func() { result, err := c.Do(ctx1, r); first <- delivery{result, err} }()
	<-entered
	go func() { result, err := c.Do(context.Background(), r); second <- delivery{result, err} }()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.active[r.Observation.IP].subs) == 2 })
	go func() {
		result, err := c.Do(context.Background(), coordinatorRequest(IntentFull))
		upgrade <- delivery{result, err}
	}()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.pending) == 1 })
	cancel1()
	if d := <-first; !errors.Is(d.err, context.Canceled) {
		t.Fatalf("subscriber cancellation=%v", d.err)
	}
	close(release)
	if d := <-second; d.err != nil {
		t.Fatal(d.err)
	}
	if d := <-upgrade; d.err != nil || d.result.Outcomes[StageDetail].Status != StatusSucceeded {
		t.Fatalf("followup=%+v", d)
	}
	if probes.Load() != 2 {
		t.Fatalf("wanted shared quick + fresh followup, probes=%d", probes.Load())
	}
}

func TestCoordinatorPendingMergeAndIsolation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	c := coordinatorFixture(t, CoordinatorConfig{Workers: 1, Backend: Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []uint16{9100}, nil
	}, Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		return coordinatorResponse(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}, counterPDU(oids.PrtMarkerLifeCount+".1", 0)), nil
	}}})
	block := coordinatorRequest(IntentQuick)
	block.Observation.IP = netip.MustParseAddr("192.0.2.2")
	blocked := make(chan delivery, 1)
	go func() { result, err := c.Do(context.Background(), block); blocked <- delivery{result, err} }()
	<-entered
	a := coordinatorRequest(0)
	a.Intent = Intent{Identity: true}
	b := a
	b.Intent.Detail = true
	outA, outB := make(chan delivery, 1), make(chan delivery, 1)
	go func() { result, err := c.Do(context.Background(), a); outA <- delivery{result, err} }()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.pending) == 1 })
	go func() { result, err := c.Do(context.Background(), b); outB <- delivery{result, err} }()
	waitCoordinator(t, c, func(c *Coordinator) bool {
		return len(c.pending) == 1 && len(c.pending[0].subs) == 2 && c.pending[0].req.Intent.Detail
	})
	close(release)
	if d := <-blocked; d.err != nil {
		t.Fatal(d.err)
	}
	da, db := <-outA, <-outB
	if da.err != nil || db.err != nil || len(da.result.Queries) != 2 || len(db.result.Queries) != 2 {
		t.Fatalf("merge=%+v %+v", da, db)
	}
	da.result.Queries[0].Result.PDUs[0].Value.([]byte)[0] = 'X'
	da.result.Fields["serial"] = Provenance{}
	if string(db.result.Queries[0].Result.PDUs[0].Value.([]byte)) != "S1" || !db.result.Fields["serial"].IP.IsValid() {
		t.Fatal("subscriber results aliased")
	}
}

func TestCoordinatorShutdownQueueAndCancellation(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	c := coordinatorFixture(t, CoordinatorConfig{Workers: 1, Queue: 1, Backend: Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}})
	active, queued := make(chan delivery, 1), make(chan delivery, 1)
	r := coordinatorRequest(IntentQuick)
	go func() { result, err := c.Do(context.Background(), r); active <- delivery{result, err} }()
	<-entered
	q := r
	q.Observation.IP = netip.MustParseAddr("192.0.2.2")
	go func() { result, err := c.Do(context.Background(), q); queued <- delivery{result, err} }()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.pending) == 1 })
	third := r
	third.Observation.IP = netip.MustParseAddr("192.0.2.3")
	if _, err := runCoordinator(t, c, third); !errors.Is(err, ErrCoordinatorQueueFull) {
		t.Fatalf("saturation=%v", err)
	}
	c.Close()
	if d := <-active; !errors.Is(d.err, context.Canceled) {
		t.Fatalf("active cancellation=%v", d.err)
	}
	if d := <-queued; !errors.Is(d.err, ErrCoordinatorStopped) {
		t.Fatalf("queued=%v", d.err)
	}
	if _, err := runCoordinator(t, c, r); !errors.Is(err, ErrCoordinatorStopped) {
		t.Fatalf("closed=%v", err)
	}
	c.Close()
}

func TestCoordinatorCancelQueryAndTargetBinding(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "target", "profile", "nil", "backend"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			backendErr := errors.New("query failure")
			var commits atomic.Int32
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(ctx context.Context, q QueryRequest) (*QueryResult, error) {
				close(started)
				if mode == "cancel" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				result := coordinatorResponse(q, serialPDU("S1"))
				switch mode {
				case "target":
					result.IP = "192.0.2.2"
				case "profile":
					result.Profile = QueryFull
				case "nil":
					return nil, nil
				case "backend":
					return result, backendErr
				}
				return result, nil
			}}, Commit: func(context.Context, Result) error { commits.Add(1); return nil }})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan delivery, 1)
			go func() { result, err := c.Do(ctx, coordinatorRequest(IntentQuick)); out <- delivery{result, err} }()
			<-started
			if mode == "cancel" {
				cancel()
			}
			d := <-out
			if d.err == nil || commits.Load() != 0 {
				t.Fatalf("unsafe response committed: %+v", d)
			}
			if mode == "backend" && !errors.Is(d.err, backendErr) {
				t.Fatal("backend error swallowed")
			}
		})
	}
}

func TestCoordinatorPlannerLongWalkAndMetricFreshness(t *testing.T) {
	t.Parallel()
	p, now := workFixture(t, Intent{Detail: true, MetricsRequested: true}, workPolicy{RequireRequestedMetrics: true})
	p = workIdentified(t, p, now)
	if !p.canCommit(now.Add(2 * time.Minute)) {
		t.Fatal("same-item serial expired during walk")
	}
	p = requireWorkNext(t, p, now.Add(2*time.Minute), StageDetail, true)
	identity := workIdentity(p, now.Add(2*time.Minute))
	metrics := workMetrics(p, now)
	metrics.HasRequestedFields = true
	p, err := p.recordDetail(detailFacts{Identity: identity, Model: "Printer", Metrics: metrics}, nil, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	requireWorkNext(t, p, now.Add(2*time.Minute), StageMetrics, true)
	p, now = workFixture(t, Intent{MetricsDue: true}, workPolicy{RequireRequestedMetrics: true})
	p = workIdentified(t, p, now)
	f := workMetrics(p, now)
	f.HasPageCount = false
	f.HasRequestedFields = true
	p.metrics = f
	q := requireWorkNext(t, p, now, 0, false)
	requireWorkOutcome(t, q, StageMetrics, StatusSkipped, ReasonFreshItemMetrics)
	requireWorkNext(t, p, now.Add(31*time.Second), StageMetrics, true)
}

type coordinatorTestLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *coordinatorTestLogger) log(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg+fmt.Sprint(args...))
}
func (l *coordinatorTestLogger) Debug(msg string, args ...interface{}) { l.log(msg, args...) }
func (l *coordinatorTestLogger) Info(msg string, args ...interface{})  { l.log(msg, args...) }
func (l *coordinatorTestLogger) Warn(msg string, args ...interface{})  { l.log(msg, args...) }
func (l *coordinatorTestLogger) Error(msg string, args ...interface{}) { l.log(msg, args...) }

func TestCoordinatorLoggingAndClone(t *testing.T) {
	t.Parallel()
	log := &coordinatorTestLogger{}
	c := coordinatorFixture(t, CoordinatorConfig{Logger: log, Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		return nil, errors.New("secret-community")
	}}})
	if _, err := runCoordinator(t, c, coordinatorRequest(IntentQuick)); err == nil {
		t.Fatal("failure swallowed")
	}
	joined := strings.Join(log.lines, "\n")
	if strings.Contains(joined, "secret-community") || !strings.Contains(joined, "duration") || !strings.Contains(joined, "reason") || !strings.Contains(joined, "errortrue") {
		t.Fatalf("unsafe/incomplete logs: %s", joined)
	}
	q := &QueryResult{Capabilities: &capabilities.DeviceCapabilities{Scores: map[string]float64{"printer": 1}}}
	copy := cloneQueryResult(q)
	copy.Capabilities.Scores["printer"] = 0
	if q.Capabilities.Scores["printer"] != 1 {
		t.Fatal("capabilities aliased")
	}
}

func TestCoordinatorValidation(t *testing.T) {
	t.Parallel()
	for _, cfg := range []CoordinatorConfig{{}, {Workers: -1, Backend: Backend{Probe: func(context.Context, netip.Addr) ([]uint16, error) { return nil, nil }, Query: func(context.Context, QueryRequest) (*QueryResult, error) { return nil, nil }}}} {
		if c, err := NewCoordinator(cfg); err == nil {
			c.Close()
			t.Fatal("invalid config accepted")
		}
	}
	c := coordinatorFixture(t, CoordinatorConfig{})
	for _, edit := range []func(*Request){
		func(r *Request) { r.Observation.IP = netip.Addr{} },
		func(r *Request) { r.Preset = 255 },
		func(r *Request) { r.Intent.Identity = true },
		func(r *Request) { r.Options.Retries = -1 },
		func(r *Request) { r.Options.Retries = 4 },
		func(r *Request) { r.Options.TimeoutSeconds = -1 },
		func(r *Request) { r.Options.FullTimeoutSeconds = -1 },
		func(r *Request) { r.Options.LearnedSerialOID = "1..2" },
		func(r *Request) { r.Options.LearnedSerialOID = "1.x.2" },
		func(r *Request) { r.Metrics = []MetricField{{Name: "serial", OID: "1.2"}} },
		func(r *Request) { r.Metrics = []MetricField{{Name: "x", OID: "1.2"}, {Name: "x", OID: "1.3"}} },
		func(r *Request) { r.Metrics = []MetricField{{Name: "x", OID: "1.2", Kind: 255}} },
	} {
		r := coordinatorRequest(IntentQuick)
		edit(&r)
		if _, err := runCoordinator(t, c, r); err == nil {
			t.Fatalf("invalid request accepted: %+v", r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, coordinatorRequest(IntentQuick)); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled subscriber accepted")
	}
	a, _ := normalizeRequest(coordinatorRequest(IntentQuick))
	b := a
	b.Metrics = []MetricField{{Name: "page_count", OID: "1.2"}}
	if _, ok := mergeRequests(a, b); ok {
		t.Fatal("conflicting metric definitions merged")
	}
}

func TestCoordinatorNoMetricsNotDueAndTCPPolicy(t *testing.T) {
	t.Parallel()
	for _, proceed := range []bool{false, true} {
		var queries atomic.Int32
		c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
			Probe: func(context.Context, netip.Addr) ([]uint16, error) { return nil, nil },
			Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				queries.Add(1)
				return coordinatorResponse(q, serialPDU("S1")), nil
			},
		}})
		r := coordinatorRequest(IntentQuick)
		r.Options.SNMPAfterTCPFailure = proceed
		result, _ := runCoordinator(t, c, r)
		if queries.Load() != boolCount(proceed) || result.Outcomes[StageReachability].Reason != ReasonNoTCPResponse {
			t.Fatalf("TCP policy: %+v", result)
		}
	}
	var committed atomic.Bool
	c := coordinatorFixture(t, CoordinatorConfig{Commit: func(context.Context, Result) error { committed.Store(true); return nil }})
	result, err := runCoordinator(t, c, coordinatorRequest(IntentMetrics))
	if err == nil || result.Outcomes[StageMetrics].Reason != ReasonNoMetrics || committed.Load() {
		t.Fatal("missing requested counters treated as success")
	}
	result, err = runCoordinator(t, c, coordinatorRequest(IntentManual))
	if err != nil || result.Outcomes[StageMetrics].Reason != ReasonNotRequested || len(result.Queries) != 1 {
		t.Fatal("manual forced metrics")
	}
}

func TestCoordinatorQueuedCancelAndLastSubscriber(t *testing.T) {
	t.Parallel()
	entered, exited := make(chan struct{}), make(chan struct{})
	c := coordinatorFixture(t, CoordinatorConfig{Workers: 1, Queue: 1, Backend: Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := make(chan error, 1)
	go func() { _, err := c.Do(ctx, coordinatorRequest(IntentQuick)); active <- err }()
	<-entered
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	r := coordinatorRequest(IntentQuick)
	r.Observation.IP = netip.MustParseAddr("192.0.2.2")
	queued := make(chan error, 1)
	go func() { _, err := c.Do(queuedCtx, r); queued <- err }()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.pending) == 1 })
	cancelQueued()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.pending) == 0 })
	cancel()
	if err := <-active; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-exited
	c.Close()
}

func TestCoordinatorDifferentExpectedSerialCannotCoalesce(t *testing.T) {
	t.Parallel()
	a, _ := normalizeRequest(coordinatorRequest(IntentQuick))
	b := a
	b.Observation.Known.Serial = "OTHER"
	if _, ok := mergeRequests(a, b); ok {
		t.Fatal("expected serial weakened")
	}
	b = a
	b.Preset = IntentManual
	if _, ok := mergeRequests(a, b); ok {
		t.Fatal("manual essential merged into quick")
	}
	b = a
	b.Options.MissingSerialFallback = true
	if _, ok := mergeRequests(a, b); ok {
		t.Fatal("diagnostic policy changed")
	}
}

func TestCoordinatorDetailWithoutSerialRetainsProvenance(t *testing.T) {
	t.Parallel()
	c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
		if q.Profile == QueryMinimal {
			return coordinatorResponse(q, serialPDU("S1")), nil
		}
		return coordinatorSnapshot(q, gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}, counterPDU(oids.PrtMarkerLifeCount+".1", 0)), nil
	}}})
	r, err := runCoordinator(t, c, coordinatorRequest(IntentFull))
	if err != nil || r.Outcomes[StageIdentity].Provenance != r.Outcomes[StageDetail].Provenance || r.Fields["serial"] != r.Outcomes[StageIdentity].Provenance {
		t.Fatalf("invented serial receipt: %+v %v", r, err)
	}
}

func sourceReceiptFixture(source ObservationSource, at time.Time) Observation {
	ip := netip.MustParseAddr("192.0.2.1")
	return Observation{IP: ip, Source: source, ObservedAt: at, Hints: ProtocolHints{
		MDNS:  MDNSHint{Service: "_ipp._tcp", Instance: "Printer", Hostname: "printer.local.", Record: &MDNSRecord{Port: 631, TXT: []string{"ty=Printer"}, Addresses: []netip.Addr{ip}}},
		SSDP:  SSDPHint{Sender: ip, Message: "response", FilterDecision: "accepted", USN: "uuid:printer", SearchTarget: "upnp:rootdevice", Location: "http://192.0.2.1/device.xml"},
		WSD:   WSDHint{Sender: ip, Message: "ProbeMatch", Endpoint: "urn:uuid:printer", Types: "PrintDevice", XAddr: "http://192.0.2.1/wsd"},
		Trap:  TrapHint{Sender: ip, Eligibility: "printer-oid", TrapOID: "1.3.6.1.2.1.43.18.2.0.1", Packet: &TrapPacket{Version: gosnmp.Version2c, PDUType: gosnmp.SNMPv2Trap, PDUs: []gosnmp.SnmpPDU{{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1.2.1.43.18.2.0.1"}}}},
		LLMNR: LLMNRHint{Sender: ip, Answer: ip, Hostname: "printer", Message: "response"},
	}}
}

func TestCoordinatorSourceReceiptValidation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, source := range []ObservationSource{SourceMDNS, SourceSSDP, SourceWSD, SourceTrap, SourceLLMNR} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name string
				at   time.Time
				want bool
			}{
				{"fresh", now, true}, {"boundary", now.Add(-30 * time.Second), true},
				{"old", now.Add(-30*time.Second - time.Nanosecond), false},
				{"future", now.Add(time.Nanosecond), false}, {"zero", time.Time{}, false},
			} {
				o := sourceReceiptFixture(source, tc.at)
				f := sourceProtocolFact(o, now)
				if (f != nil) != tc.want {
					t.Fatalf("%s: fact=%+v", tc.name, f)
				}
				if f != nil && (f.ReceivedAt != tc.at || f.IP != o.IP || f.Source != source) {
					t.Fatal("receipt/target provenance changed")
				}
			}
			o := sourceReceiptFixture(source, now)
			if sourceProtocolFact(o, time.Time{}) != nil {
				t.Fatal("zero clock accepted")
			}
			o.Hints = ProtocolHints{}
			if sourceProtocolFact(o, now) != nil {
				t.Fatal("source label without raw evidence accepted")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		source ObservationSource
		edit   func(*Observation)
	}{
		{"mdns-no-record", SourceMDNS, func(o *Observation) { o.Hints.MDNS.Record = nil }},
		{"mdns-port", SourceMDNS, func(o *Observation) { o.Hints.MDNS.Record.Port = 65536 }},
		{"mdns-target", SourceMDNS, func(o *Observation) { o.Hints.MDNS.Record.Addresses = []netip.Addr{netip.MustParseAddr("192.0.2.2")} }},
		{"mdns-service", SourceMDNS, func(o *Observation) { o.Hints.MDNS.Service = "_http._tcp" }},
		{"ssdp-alive", SourceSSDP, func(o *Observation) { o.Hints.SSDP.Message = "alive" }},
		{"ssdp-url-only", SourceSSDP, func(o *Observation) { o.Hints.SSDP.Sender = netip.MustParseAddr("192.0.2.2") }},
		{"ssdp-filter", SourceSSDP, func(o *Observation) { o.Hints.SSDP.FilterDecision = "rejected" }},
		{"ssdp-url", SourceSSDP, func(o *Observation) { o.Hints.SSDP.Location = "file:///printer" }},
		{"wsd-hello", SourceWSD, func(o *Observation) { o.Hints.WSD.Message = "Hello" }},
		{"wsd-url-only", SourceWSD, func(o *Observation) { o.Hints.WSD.Sender = netip.MustParseAddr("192.0.2.2") }},
		{"wsd-shape", SourceWSD, func(o *Observation) { o.Hints.WSD.Endpoint = "" }},
		{"wsd-url", SourceWSD, func(o *Observation) { o.Hints.WSD.XAddr = "not-a-url" }},
		{"trap-no-packet", SourceTrap, func(o *Observation) { o.Hints.Trap.Packet = nil }},
		{"trap-type", SourceTrap, func(o *Observation) { o.Hints.Trap.Packet.PDUType = gosnmp.GetResponse }},
		{"trap-version", SourceTrap, func(o *Observation) { o.Hints.Trap.Packet.Version = gosnmp.Version1 }},
		{"trap-unmatched-id", SourceTrap, func(o *Observation) { o.Hints.Trap.TrapOID = "1.3.6.1.2.1.43.99" }},
		{"trap-untyped-id", SourceTrap, func(o *Observation) { o.Hints.Trap.Packet.PDUs[0].Type = gosnmp.OctetString }},
		{"trap-unbound", SourceTrap, func(o *Observation) { o.Hints.Trap.Sender = netip.MustParseAddr("192.0.2.2") }},
		{"trap-ineligible", SourceTrap, func(o *Observation) { o.Hints.Trap.Eligibility = "unknown" }},
		{"llmnr-query", SourceLLMNR, func(o *Observation) { o.Hints.LLMNR.Message = "query" }},
		{"llmnr-indirect", SourceLLMNR, func(o *Observation) { o.Hints.LLMNR.Sender = netip.MustParseAddr("192.0.2.2") }},
		{"llmnr-answer", SourceLLMNR, func(o *Observation) { o.Hints.LLMNR.Answer = netip.MustParseAddr("192.0.2.2") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := sourceReceiptFixture(tc.source, now)
			tc.edit(&o)
			if f := sourceProtocolFact(o, now); f != nil {
				t.Fatalf("malformed/indirect evidence accepted: %+v", f)
			}
		})
	}
	for _, source := range []ObservationSource{SourceUnknown, SourceManual, SourceRange, 255} {
		if sourceProtocolFact(sourceReceiptFixture(source, now), now) != nil {
			t.Fatal("nonprotocol source accepted")
		}
	}
	// Validate actual v1 mapping, not just an adapter's eligibility label.
	o := sourceReceiptFixture(SourceTrap, now)
	o.Hints.Trap.EnterpriseOID = "1.3.6.1.2.1.43"
	o.Hints.Trap.TrapOID = "1.3.6.1.2.1.43.0.7"
	o.Hints.Trap.Packet = &TrapPacket{Version: gosnmp.Version1, PDUType: gosnmp.Trap, GenericTrap: 6, SpecificTrap: 7}
	if sourceProtocolFact(o, now) == nil {
		t.Fatal("valid v1 trap rejected")
	}
	o.Hints.Trap.Packet.SpecificTrap = 8
	if sourceProtocolFact(o, now) != nil {
		t.Fatal("contradictory v1 trap accepted")
	}
}

func TestCoordinatorExecuteRejectsMappedUnspecified(t *testing.T) {
	t.Parallel()
	c := coordinatorFixture(t, CoordinatorConfig{})
	r := coordinatorRequest(IntentQuick)
	r.Observation.IP = netip.MustParseAddr("::ffff:0.0.0.0")
	if _, err := normalizeRequest(r); err == nil {
		t.Fatal("mapped unspecified target admitted")
	}
	if _, err := c.execute(context.Background(), r, nil); err == nil {
		t.Fatal("invalid execution returned success")
	}
}

func TestCoordinatorExplicitSourceIntake(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ordinary", "receipt", "zero", "old", "future"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var probes, queries atomic.Int32
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
				Probe: func(context.Context, netip.Addr) ([]uint16, error) { probes.Add(1); return []uint16{631}, nil },
				Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
					queries.Add(1)
					return coordinatorResponse(q, serialPDU("S1")), nil
				},
			}})
			r := coordinatorRequest(IntentQuick)
			r.Observation = sourceReceiptFixture(SourceMDNS, time.Now())
			switch mode {
			case "zero":
				r.Observation.ObservedAt = time.Time{}
			case "old":
				r.Observation.ObservedAt = time.Now().Add(-31 * time.Second)
			case "future":
				r.Observation.ObservedAt = time.Now().Add(time.Hour)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var result Result
			var err error
			if mode == "ordinary" {
				result, err = c.Do(ctx, r)
			} else {
				result, err = c.DoSource(ctx, r)
			}
			if err != nil || queries.Load() != 1 || probes.Load() != boolCount(mode != "receipt") {
				t.Fatalf("probe=%d query=%d err=%v", probes.Load(), queries.Load(), err)
			}
			if mode == "receipt" && (result.Outcomes[StageReachability].Reason != ReasonFreshProtocol || len(result.OpenPorts) != 0 || result.Outcomes[StageIdentity].Status != StatusSucceeded) {
				t.Fatal("source invented probe/identity")
			}
		})
	}
	var probes atomic.Int32
	c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Probe: func(context.Context, netip.Addr) ([]uint16, error) { probes.Add(1); return []uint16{631}, nil }}})
	r := coordinatorRequest(IntentQuick)
	r.Observation = sourceReceiptFixture(SourceMDNS, time.Now().Add(-31*time.Second))
	r, _ = normalizeRequest(r)
	f := &protocolFact{Provenance: Provenance{IP: r.Observation.IP, Source: SourceMDNS, Protocol: ProtocolMDNS, ReceivedAt: r.Observation.ObservedAt}, Message: messageMDNSAnswer}
	if _, err := c.execute(context.Background(), r, f); err != nil || probes.Load() != 1 {
		t.Fatal("queued receipt did not expire before dispatch")
	}
}

func TestCoordinatorPortsAndExplicitSkip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		preset IntentPreset
		ports  []uint16
	}{
		{IntentQuick, []uint16{9100, 631}}, {IntentFull, []uint16{9100, 631, 515, 80, 443}},
	} {
		t.Run(fmt.Sprint(tc.preset), func(t *testing.T) {
			t.Parallel()
			var got []uint16
			c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
				ProbeWithPorts: func(_ context.Context, _ netip.Addr, ports []uint16) ([]uint16, error) {
					got = append([]uint16(nil), ports...)
					ports[0] = 1
					return []uint16{631}, nil
				},
				Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
					return coordinatorSnapshot(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}, counterPDU(oids.PrtMarkerLifeCount, 0)), nil
				},
			}})
			r := coordinatorRequest(tc.preset)
			r.Options.Ports = tc.ports
			result, err := runCoordinator(t, c, r)
			if err != nil || !reflect.DeepEqual(got, tc.ports) || !reflect.DeepEqual(result.OpenPorts, []uint16{631}) {
				t.Fatalf("ports=%v result=%+v err=%v", got, result, err)
			}
		})
	}
	c := coordinatorFixture(t, CoordinatorConfig{})
	r := coordinatorRequest(IntentQuick)
	r.Options.Ports = []uint16{631}
	if _, err := runCoordinator(t, c, r); err == nil {
		t.Fatal("legacy probe silently ignored ports")
	}
	for _, proceed := range []bool{false, true} {
		var queries atomic.Int32
		c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{
			Probe: func(context.Context, netip.Addr) ([]uint16, error) {
				t.Error("explicit skip probed TCP")
				return nil, nil
			},
			Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				queries.Add(1)
				return coordinatorResponse(q, serialPDU("S1")), nil
			},
		}})
		r := coordinatorRequest(IntentQuick)
		r.Options.SkipTCP, r.Options.SNMPAfterTCPFailure = true, proceed
		result, err := runCoordinator(t, c, r)
		if queries.Load() != boolCount(proceed) || (err == nil) != proceed || result.Outcomes[StageReachability].Status != StatusSkipped || result.Outcomes[StageReachability].Reason != ReasonNotRequested || len(result.OpenPorts) != 0 {
			t.Fatalf("skip policy result=%+v err=%v", result, err)
		}
		if !proceed {
			var outcome *OutcomeError
			if !errors.As(err, &outcome) || outcome.Outcome.Stage != StageIdentity {
				t.Fatal("skipped identity missing typed error")
			}
		}
	}
	for _, returned := range []uint16{0, 80} {
		c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{ProbeWithPorts: func(context.Context, netip.Addr, []uint16) ([]uint16, error) { return []uint16{returned}, nil }}})
		r := coordinatorRequest(IntentQuick)
		r.Options.Ports = []uint16{631}
		result, err := runCoordinator(t, c, r)
		if err == nil || result.Outcomes[StageReachability].Status != StatusFailed || len(result.OpenPorts) != 0 {
			t.Fatal("unrequested port accepted")
		}
	}
	if c, err := NewCoordinator(CoordinatorConfig{Backend: Backend{ProbeWithPorts: func(context.Context, netip.Addr, []uint16) ([]uint16, error) { return nil, nil }, Query: func(context.Context, QueryRequest) (*QueryResult, error) { return nil, nil }}}); err != nil {
		t.Fatal(err)
	} else {
		c.Close()
	}
}

func TestCoordinatorRequestCloneAndContentComparison(t *testing.T) {
	t.Parallel()
	a := coordinatorRequest(IntentQuick)
	a.Observation = sourceReceiptFixture(SourceMDNS, time.Now())
	a.Options.Ports = []uint16{631}
	a.Observation.Hints.Trap.Packet.PDUs = append(a.Observation.Hints.Trap.Packet.PDUs, serialPDU("S1"), gosnmp.SnmpPDU{Value: []int{1, 2}})
	a, _ = normalizeRequest(a)
	b := cloneRequest(a)
	if a.Observation.Hints.MDNS.Record == b.Observation.Hints.MDNS.Record || a.Observation.Hints.Trap.Packet == b.Observation.Hints.Trap.Packet {
		t.Fatal("payload pointers aliased")
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("clone changed content")
	}
	if _, ok := mergeRequests(a, b); !ok {
		t.Fatal("equal separately allocated payloads did not merge")
	}
	b.Observation.Hints.MDNS.Record.TXT[0] = "changed"
	b.Observation.Hints.MDNS.Record.Addresses[0] = netip.MustParseAddr("192.0.2.2")
	b.Observation.Hints.Trap.Packet.PDUs[1].Value.([]byte)[0] = 'X'
	b.Observation.Hints.Trap.Packet.PDUs[2].Value.([]int)[0] = 9
	b.Options.Ports[0] = 80
	b.Metrics[0].OID = "1.2"
	if a.Observation.Hints.MDNS.Record.TXT[0] != "ty=Printer" || a.Observation.Hints.MDNS.Record.Addresses[0] != a.Observation.IP || string(a.Observation.Hints.Trap.Packet.PDUs[1].Value.([]byte)) != "S1" || a.Observation.Hints.Trap.Packet.PDUs[2].Value.([]int)[0] != 1 || a.Options.Ports[0] != 631 {
		t.Fatal("request clone leaked mutation")
	}
	if _, ok := mergeRequests(a, b); ok {
		t.Fatal("different contents merged")
	}
	result := Result{Observation: a.Observation}
	copy := cloneCoordinatorResult(result)
	copy.Observation.Hints.MDNS.Record.TXT[0] = "changed"
	copy.Observation.Hints.Trap.Packet.PDUs[1].Value.([]byte)[0] = 'X'
	if result.Observation.Hints.MDNS.Record.TXT[0] != "ty=Printer" || string(result.Observation.Hints.Trap.Packet.PDUs[1].Value.([]byte)) != "S1" {
		t.Fatal("result hints aliased")
	}
}

func TestCoordinatorPartialStageErrorPreservesIdentity(t *testing.T) {
	t.Parallel()
	for _, failed := range []WorkStage{StageDetail, StageMetrics} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			t.Parallel()
			backendErr := errors.New("partial SNMP read")
			var commits atomic.Int32
			c := coordinatorFixture(t, CoordinatorConfig{Commit: func(context.Context, Result) error { commits.Add(1); return nil }, Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
				if q.Profile == QueryFull && failed == StageDetail || q.Profile == QueryMetrics && failed == StageMetrics {
					return coordinatorResponse(q, serialPDU("UNTRUSTED")), backendErr
				}
				return coordinatorResponse(q, serialPDU("S1"), gosnmp.SnmpPDU{Name: oids.HrDeviceDescr, Type: gosnmp.OctetString, Value: "Printer"}), nil
			}}})
			r := coordinatorRequest(IntentManual)
			if failed == StageDetail {
				r.Options.FullDetail = true
			} else {
				r = coordinatorRequest(IntentMetrics)
			}
			result, err := runCoordinator(t, c, r)
			var outcome *OutcomeError
			if !errors.Is(err, backendErr) || !errors.As(err, &outcome) || outcome.Outcome.Stage != failed || result.Serial != "S1" || result.Model != "Printer" || result.Fields["serial"] != result.Outcomes[StageIdentity].Provenance || commits.Load() != 0 {
				t.Fatalf("safe partial facts lost: %+v %v", result, err)
			}
			if len(result.Queries) != 2 || !errors.Is(result.Queries[1].Err, backendErr) {
				t.Fatal("failed raw response lacks error annotation")
			}
		})
	}
}

func TestCoordinatorDefaultMetricsRequireSupplies(t *testing.T) {
	t.Parallel()
	for _, supplies := range []bool{false, true} {
		c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Query: func(_ context.Context, q QueryRequest) (*QueryResult, error) {
			pdus := []gosnmp.SnmpPDU{serialPDU("S1"), counterPDU(oids.PrtMarkerLifeCount, 0)}
			if supplies {
				return coordinatorSnapshot(q, pdus...), nil
			}
			return coordinatorResponse(q, pdus...), nil
		}}})
		result, err := runCoordinator(t, c, coordinatorRequest(IntentMetrics))
		if (err == nil) != supplies || (result.Outcomes[StageMetrics].Reason == ReasonFreshItemMetrics) != supplies {
			t.Fatalf("page-count-only snapshot accepted: %+v %v", result, err)
		}
	}
}

func TestCoordinatorActivePayloadCoalescingAndSubscriberCleanup(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	c := coordinatorFixture(t, CoordinatorConfig{Backend: Backend{Probe: func(ctx context.Context, _ netip.Addr) ([]uint16, error) {
		if probes.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []uint16{631}, nil
	}}})
	a := coordinatorRequest(IntentQuick)
	a.Observation = sourceReceiptFixture(SourceMDNS, time.Now())
	b := cloneRequest(a) // Different pointer identities, identical contents.
	first, second := make(chan delivery, 1), make(chan delivery, 1)
	go func() { result, err := c.Do(context.Background(), a); first <- delivery{result, err} }()
	<-entered
	// Request snapshot is already owned; producer mutations must not leak.
	a.Observation.Hints.MDNS.Record.TXT[0] = "producer changed"
	a.Observation.Hints.Trap.Packet.PDUs[0].Value = "producer changed"
	go func() { result, err := c.Do(context.Background(), b); second <- delivery{result, err} }()
	waitCoordinator(t, c, func(c *Coordinator) bool { return len(c.active[b.Observation.IP].subs) == 2 })
	c.mu.Lock()
	g := c.active[b.Observation.IP]
	c.mu.Unlock()
	close(release)
	da, db := <-first, <-second
	if da.err != nil || db.err != nil || probes.Load() != 1 {
		t.Fatalf("equal contents not coalesced: %v %v probes=%d", da.err, db.err, probes.Load())
	}
	if da.result.Observation.Hints.MDNS.Record.TXT[0] != "ty=Printer" || db.result.Observation.Hints.Trap.Packet.PDUs[0].Value != "1.3.6.1.2.1.43.18.2.0.1" {
		t.Fatal("producer mutation entered work")
	}
	da.result.Observation.Hints.MDNS.Record.TXT[0] = "subscriber changed"
	if db.result.Observation.Hints.MDNS.Record.TXT[0] != "ty=Printer" {
		t.Fatal("subscriber observation payloads aliased")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(g.subs) != 0 || len(c.active) != 0 || len(c.pending) != 0 {
		t.Fatal("completed group retained subscribers/work")
	}
}

func TestCoordinatorReadOnlyAndProbeRetryIsolation(t *testing.T) {
	t.Parallel()
	var commits, wakes, probes atomic.Int32
	transient := errors.New("probe interrupted")
	c := coordinatorFixture(t, CoordinatorConfig{
		Commit: func(context.Context, Result) error { commits.Add(1); return nil },
		Wake:   func() { wakes.Add(1) },
		Backend: Backend{ProbeWithPorts: func(_ context.Context, _ netip.Addr, ports []uint16) ([]uint16, error) {
			if !reflect.DeepEqual(ports, []uint16{631}) {
				t.Error("backend mutation leaked into retry")
			}
			ports[0] = 80
			if probes.Add(1) == 1 {
				return []uint16{80}, transient
			}
			return []uint16{631}, nil
		}},
	})
	r := coordinatorRequest(IntentQuick)
	r.Options = WorkOptions{ReadOnly: true, Retries: 1, Ports: []uint16{631}}
	result, err := runCoordinator(t, c, r)
	if err != nil || probes.Load() != 2 || commits.Load() != 0 || wakes.Load() != 0 || result.Committed || result.Serial != "S1" || !reflect.DeepEqual(result.OpenPorts, []uint16{631}) || r.Options.Ports[0] != 631 {
		t.Fatalf("read-only/retry policy: %+v %v", result, err)
	}
}
