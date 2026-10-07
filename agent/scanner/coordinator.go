package scanner

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
	"printmaster/common/snmp/oids"
)

// Backend supplies wire operations, not identity decisions or stage outcomes.
// Functions must honor context cancellation; Query must include the target IP.
// A production adapter can call QueryDevice and its existing TCP probe. Learned
// serial OIDs must be included by the adapter even for QueryMinimal.
type Backend struct {
	Probe func(context.Context, netip.Addr) ([]uint16, error)
	// Preferred when wired: receives a private copy of requested TCP ports.
	// Legacy Probe remains supported only when no explicit ports were requested.
	ProbeWithPorts func(context.Context, netip.Addr, []uint16) ([]uint16, error)
	Query          func(context.Context, QueryRequest) (*QueryResult, error)
}

type QueryRequest struct {
	IP               netip.Addr
	Profile          QueryProfile
	VendorHint       string
	LearnedSerialOID string
	TimeoutSeconds   int
	Metrics          []MetricField
}

// CoordinatorLogger matches the existing structured logger. No PDU values,
// hints, serials, credentials or backend error strings are written to logs.
type CoordinatorLogger interface {
	Debug(string, ...interface{})
	Info(string, ...interface{})
	Warn(string, ...interface{})
	Error(string, ...interface{})
}

type CoordinatorConfig struct {
	Workers int // Default 4; persistent, shared by every request.
	Queue   int // Default 64; pending work groups, not an unbounded channel.
	Backend Backend
	Logger  CoordinatorLogger
	Commit  func(context.Context, Result) error
	Wake    func() // Called only after a successful Commit, never for read-only work.
}

// WorkOptions describes query policy, never satisfied stages. Manual and live
// default to essential identity/enrichment, quick/liveness/metrics to minimal.
// FullDetail opts live/manual into a full diagnostic walk. MissingSerialFallback
// permits one QueryFull identity diagnostic, including for live discovery.
type WorkOptions struct {
	ReadOnly              bool     // Query and validate without persistence or upload wake.
	Ports                 []uint16 // Explicit TCP ports; empty uses the backend's defaults.
	SkipTCP               bool     // Records not-requested, not reachability evidence.
	SNMPAfterTCPFailure   bool
	MissingSerialFallback bool
	FullDetail            bool
	VendorHint            string
	LearnedSerialOID      string
	TimeoutSeconds        int // Default 5.
	FullTimeoutSeconds    int // Default 30.
	Retries               int // Additional attempts for backend errors, max 3.
}

type MetricKind uint8

const (
	MetricCounter MetricKind = iota // Nonnegative integral numeric SNMP values.
	MetricGauge                     // Integral numeric values; negative sentinels rejected.
	MetricText                      // Nonempty OctetString values (e.g. status text).
)

// MetricField specifies a required scalar or table-column OID. Table instances
// match only at component boundaries. Every requested field must be present to
// reuse a same-item sample; zero counters are valid. Defaults cover page count
// and supply description/level/capacity. Runtime may supply learned/capability-
// specific fields, but page count alone must not stand in for a full snapshot.
type MetricField struct {
	Name string
	OID  string
	Kind MetricKind
}

type Request struct {
	Observation Observation
	Preset      IntentPreset // Use either Preset or Intent, not both.
	Intent      Intent
	Options     WorkOptions
	Metrics     []MetricField
}

// ObtainedQuery retains each actual response, including a missing-serial
// diagnostic. Main can convert these PDUs without another probe or query.
type ObtainedQuery struct {
	Stage      WorkStage
	Diagnostic bool
	ReceivedAt time.Time
	Result     *QueryResult
	Err        error // Failed/invalid responses are diagnostics, never attributable facts.
}

type Result struct {
	Observation  Observation
	Intent       Intent
	Serial       string
	Manufacturer string
	Model        string
	Fields       map[string]Provenance // serial/model/manufacturer and metric names.
	Outcomes     [workStageCount]StageOutcome
	Queries      []ObtainedQuery
	OpenPorts    []uint16
	Committed    bool
}

var (
	ErrCoordinatorStopped   = errors.New("scanner coordinator stopped")
	ErrCoordinatorQueueFull = errors.New("scanner coordinator queue full")
)

// OutcomeError exposes typed negative/failed outcomes without converting a TCP
// miss into an offline assertion. Backend and persistence errors remain wrapped.
type OutcomeError struct{ Outcome StageOutcome }

func (e *OutcomeError) Error() string {
	return fmt.Sprintf("scanner stage %d: status %d reason %d", e.Outcome.Stage, e.Outcome.Status, e.Outcome.Reason)
}

type delivery struct {
	result Result
	err    error
}
type subscription struct{ done chan delivery }
type workGroup struct {
	req      Request
	protocol *protocolFact
	ctx      context.Context
	cancel   context.CancelFunc
	subs     map[*subscription]struct{}
}

// Coordinator owns a bounded queue and serializes all work for a target IP.
// Exact active requests coalesce. Compatible pending requests union their work;
// active upgrades become fresh followups (no cross-item identity cache). A
// subscriber's cancellation affects only that subscriber unless it is the last.
// Close cancels queued/active work and joins workers; backend/Commit must honor
// their contexts. Call Close from the owner, not from a backend/Commit/Wake.
type Coordinator struct {
	cfg     CoordinatorConfig
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	ready   *sync.Cond
	pending []*workGroup
	active  map[netip.Addr]*workGroup
	stopped bool
	wg      sync.WaitGroup
}

func NewCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	if (cfg.Backend.Probe == nil && cfg.Backend.ProbeWithPorts == nil) || cfg.Backend.Query == nil {
		return nil, errors.New("probe and query backend required")
	}
	if cfg.Workers == 0 {
		cfg.Workers = 4
	}
	if cfg.Queue == 0 {
		cfg.Queue = 64
	}
	if cfg.Workers < 1 || cfg.Queue < 1 {
		return nil, errors.New("positive workers and queue required")
	}
	c := &Coordinator{cfg: cfg, active: make(map[netip.Addr]*workGroup)}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.ready = sync.NewCond(&c.mu)
	for i := 0; i < cfg.Workers; i++ {
		c.wg.Add(1)
		go c.worker()
	}
	return c, nil
}

func normalizeRequest(r Request) (Request, error) {
	r = cloneRequest(r)
	if r.Preset != 0 {
		if r.Intent != (Intent{}) {
			return r, errors.New("choose preset or explicit intent")
		}
		var err error
		r.Intent, err = PresetIntent(r.Preset)
		if err != nil {
			return r, err
		}
	}
	if r.Options.FullDetail {
		r.Intent.Detail = true
	}
	for _, port := range r.Options.Ports {
		if port == 0 {
			return r, errors.New("zero TCP port")
		}
	}
	p, err := newWorkPlanner(r.Observation, r.Intent, workPolicy{})
	if err != nil {
		return r, err
	}
	r.Observation, r.Intent = p.observation, p.intent
	if r.Options.Retries < 0 || r.Options.Retries > 3 {
		return r, errors.New("retries must be between 0 and 3")
	}
	if r.Options.TimeoutSeconds < 0 || r.Options.FullTimeoutSeconds < 0 {
		return r, errors.New("negative query timeout")
	}
	if r.Options.TimeoutSeconds == 0 {
		r.Options.TimeoutSeconds = 5
	}
	if r.Options.FullTimeoutSeconds == 0 {
		r.Options.FullTimeoutSeconds = 30
	}
	r.Options.LearnedSerialOID = normalizeOID(r.Options.LearnedSerialOID)
	if r.Options.LearnedSerialOID != "" && !validWorkOID(r.Options.LearnedSerialOID) {
		return r, errors.New("invalid learned serial OID")
	}
	r.Metrics = append([]MetricField(nil), r.Metrics...)
	if len(r.Metrics) == 0 {
		r.Metrics = []MetricField{
			{Name: "page_count", OID: oids.PrtMarkerLifeCount},
			{Name: "supply_description", OID: oids.PrtMarkerSuppliesDesc, Kind: MetricText},
			{Name: "supply_level", OID: oids.PrtMarkerSuppliesLevel, Kind: MetricGauge},
			{Name: "supply_capacity", OID: oids.PrtMarkerSuppliesMaxCap, Kind: MetricGauge},
		}
	}
	seen := make(map[string]bool)
	for i := range r.Metrics {
		f := &r.Metrics[i]
		f.OID = normalizeOID(f.OID)
		if f.Name == "" || f.Name == "serial" || f.Name == "model" || f.Name == "manufacturer" || seen[f.Name] || !validWorkOID(f.OID) || f.Kind > MetricText {
			return r, errors.New("invalid or duplicate metric field")
		}
		seen[f.Name] = true
	}
	sort.Slice(r.Metrics, func(i, j int) bool { return r.Metrics[i].Name < r.Metrics[j].Name })
	return r, nil
}

func validWorkOID(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

// sourceProtocolFact is the only raw-hint admission path. It cannot prove that
// an external caller received a packet; DoSource's adapter-receipt contract is
// explicit. Never stamp time here to turn cached or zero-receipt hints into live
// evidence. Indirect URLs/DNS answers do not prove their advertised host is up.
func sourceProtocolFact(o Observation, now time.Time) *protocolFact {
	p, err := newWorkPlanner(o, Intent{Reachability: true}, workPolicy{})
	if err != nil {
		return nil
	}
	f := protocolFact{Provenance: Provenance{Source: o.Source, IP: p.observation.IP, ReceivedAt: o.ObservedAt, Field: "source_receipt"}}
	if !p.fresh(f.Provenance, now) {
		return nil
	}
	sameTarget := func(ip netip.Addr) bool { return ip.IsValid() && ip.Unmap() == p.observation.IP }
	validURL := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
	}
	switch o.Source {
	case SourceMDNS:
		h := o.Hints.MDNS
		if (h.Service != "_ipp._tcp" && h.Service != "_ipps._tcp" && h.Service != "_printer._tcp") || h.Instance == "" || h.Hostname == "" || h.Record == nil || h.Record.Port < 1 || h.Record.Port > 65535 {
			return nil
		}
		found := false
		for _, address := range h.Record.Addresses {
			found = found || sameTarget(address)
		}
		if !found {
			return nil
		}
		f.Protocol, f.Message = ProtocolMDNS, messageMDNSAnswer
	case SourceSSDP:
		h := o.Hints.SSDP
		if !sameTarget(h.Sender) || h.Message != "response" || h.FilterDecision != "accepted" || h.USN == "" || h.SearchTarget == "" || !validURL(h.Location) {
			return nil
		}
		f.Protocol, f.Message = ProtocolSSDP, messageSSDPResponse
	case SourceWSD:
		h := o.Hints.WSD
		if !sameTarget(h.Sender) || h.Message != "ProbeMatch" || h.Endpoint == "" || h.Types == "" {
			return nil
		}
		addresses := strings.Fields(h.XAddr)
		if len(addresses) == 0 {
			return nil
		}
		for _, address := range addresses {
			if !validURL(address) {
				return nil
			}
		}
		f.Protocol, f.Message = ProtocolWSD, messageWSDResponse
	case SourceTrap:
		h := o.Hints.Trap
		if !sameTarget(h.Sender) || h.Packet == nil || (h.Eligibility != "printer-oid" && h.Eligibility != "vendor-hint") {
			return nil
		}
		packet := h.Packet
		eligible := func(oid string) bool {
			oid = normalizeOID(oid)
			if !validWorkOID(oid) {
				return false
			}
			roots := []string{"1.3.6.1.2.1.43"}
			if h.Eligibility == "vendor-hint" {
				roots = []string{"1.3.6.1.4.1.11", "1.3.6.1.4.1.1602", "1.3.6.1.4.1.1248", "1.3.6.1.4.1.2435", "1.3.6.1.4.1.253", "1.3.6.1.4.1.367", "1.3.6.1.4.1.1347", "1.3.6.1.4.1.641"}
			}
			for _, root := range roots {
				if oid == root || strings.HasPrefix(oid, root+".") {
					return true
				}
			}
			return false
		}
		matched := eligible(h.TrapOID) || eligible(h.EnterpriseOID)
		for _, pdu := range packet.PDUs {
			matched = matched || eligible(pdu.Name)
		}
		if !matched {
			return nil
		}
		if packet.PDUType == gosnmp.Trap {
			if packet.Version != gosnmp.Version1 || !validWorkOID(normalizeOID(h.EnterpriseOID)) || packet.GenericTrap < 0 || packet.GenericTrap > 6 || packet.SpecificTrap < 0 {
				return nil
			}
			trapOID := fmt.Sprintf("1.3.6.1.6.3.1.1.5.%d", packet.GenericTrap+1)
			if packet.GenericTrap == 6 {
				trapOID = fmt.Sprintf("%s.0.%d", normalizeOID(h.EnterpriseOID), packet.SpecificTrap)
			}
			if normalizeOID(h.TrapOID) != trapOID {
				return nil
			}
		} else if packet.PDUType == gosnmp.SNMPv2Trap || packet.PDUType == gosnmp.InformRequest {
			if packet.Version != gosnmp.Version2c && packet.Version != gosnmp.Version3 {
				return nil
			}
			found := false
			for _, pdu := range packet.PDUs {
				if normalizeOID(pdu.Name) == "1.3.6.1.6.3.1.1.4.1.0" && pdu.Type == gosnmp.ObjectIdentifier {
					oid := normalizeOID(coordinatorString(pdu.Value))
					found = found || (validWorkOID(oid) && oid == normalizeOID(h.TrapOID))
				}
			}
			if !found {
				return nil
			}
		} else {
			return nil
		}
		f.Protocol, f.Message = ProtocolTrap, messageTrap
	case SourceLLMNR:
		h := o.Hints.LLMNR
		if h.Message != "response" || h.Hostname == "" || !sameTarget(h.Sender) || !sameTarget(h.Answer) {
			return nil
		}
		f.Protocol, f.Message = ProtocolLLMNR, messageLLMNRAnswer
	default:
		return nil
	}
	return &f
}

// Do waits for this subscriber only. Queue saturation returns an explicit error
// rather than blocking a send or spawning a waiter goroutine.
func (c *Coordinator) Do(ctx context.Context, req Request) (Result, error) {
	return c.do(ctx, req, false)
}

// DoSource admits a raw event from an actual source-adapter callback. ObservedAt
// must be that adapter's local receipt time, never a remote timestamp or a time
// manufactured during enqueue/dispatch. Zero/old/future receipts and malformed
// or indirect-target messages fall back to TCP. This is a wiring contract, NOT
// authentication of arbitrary callers: typed metadata cannot prove wire origin.
// Until adapters provide reliable local receipts, use Do (no protocol skip).
// No caller can inject stage outcomes or skip identity through either API.
func (c *Coordinator) DoSource(ctx context.Context, req Request) (Result, error) {
	return c.do(ctx, req, true)
}

func (c *Coordinator) do(ctx context.Context, req Request, sourceReceipt bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	r, err := normalizeRequest(req)
	if err != nil {
		return Result{}, err
	}
	if len(r.Options.Ports) != 0 && !r.Options.SkipTCP && c.cfg.Backend.ProbeWithPorts == nil {
		return Result{}, errors.New("explicit TCP ports require ProbeWithPorts backend")
	}
	var protocol *protocolFact
	if sourceReceipt {
		protocol = sourceProtocolFact(r.Observation, time.Now())
	}
	s := &subscription{done: make(chan delivery, 1)}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return Result{}, ErrCoordinatorStopped
	}
	var g *workGroup
	if a := c.active[r.Observation.IP]; a != nil && a.ctx.Err() == nil && reflect.DeepEqual(a.req, r) && reflect.DeepEqual(a.protocol, protocol) {
		g = a
	}
	if g == nil {
		for _, queued := range c.pending {
			if !reflect.DeepEqual(queued.protocol, protocol) {
				continue
			}
			if merged, ok := mergeRequests(queued.req, r); ok {
				queued.req, g = merged, queued
				break
			}
		}
	}
	if g == nil {
		if len(c.pending) >= c.cfg.Queue {
			c.mu.Unlock()
			return Result{}, ErrCoordinatorQueueFull
		}
		g = &workGroup{req: r, protocol: protocol, subs: make(map[*subscription]struct{})}
		g.ctx, g.cancel = context.WithCancel(c.ctx)
		c.pending = append(c.pending, g)
	}
	g.subs[s] = struct{}{}
	c.ready.Broadcast()
	c.mu.Unlock()
	select {
	case d := <-s.done:
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return d.result, d.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(g.subs, s)
		if len(g.subs) == 0 {
			g.cancel()
			for i, queued := range c.pending {
				if queued == g {
					c.pending = append(c.pending[:i], c.pending[i+1:]...)
					break
				}
			}
			c.ready.Broadcast()
		}
		c.mu.Unlock()
		return Result{}, ctx.Err()
	}
}

func mergeRequests(a, b Request) (Request, bool) {
	// Source/hints/expected serial and query policy cannot be weakened by merging.
	// Different presets retain their different identity/detail profiles as followups.
	if !reflect.DeepEqual(a.Observation, b.Observation) || a.Preset != b.Preset || !reflect.DeepEqual(a.Options, b.Options) {
		return a, false
	}
	fields := make(map[string]MetricField)
	for _, f := range a.Metrics {
		fields[f.Name] = f
	}
	for _, f := range b.Metrics {
		if old, ok := fields[f.Name]; ok && old != f {
			return a, false
		}
		fields[f.Name] = f
	}
	a.Metrics = nil
	for _, f := range fields {
		a.Metrics = append(a.Metrics, f)
	}
	sort.Slice(a.Metrics, func(i, j int) bool { return a.Metrics[i].Name < a.Metrics[j].Name })
	a.Intent = Intent{a.Intent.Reachability || b.Intent.Reachability, a.Intent.Identity || b.Intent.Identity, a.Intent.Detail || b.Intent.Detail, a.Intent.MetricsRequested || b.Intent.MetricsRequested, a.Intent.MetricsDue || b.Intent.MetricsDue}
	return a, true
}

func (c *Coordinator) worker() {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		var g *workGroup
		for g == nil && !c.stopped {
			for i, queued := range c.pending {
				if c.active[queued.req.Observation.IP] == nil {
					g = queued
					c.pending = append(c.pending[:i], c.pending[i+1:]...)
					c.active[g.req.Observation.IP] = g
					break
				}
			}
			if g == nil {
				c.ready.Wait()
			}
		}
		if g == nil {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		result, err := c.execute(g.ctx, g.req, g.protocol)
		c.mu.Lock()
		delete(c.active, g.req.Observation.IP)
		g.cancel()
		for s := range g.subs {
			s.done <- delivery{cloneCoordinatorResult(result), err}
			delete(g.subs, s)
		}
		c.ready.Broadcast()
		c.mu.Unlock()
	}
}

func (c *Coordinator) Close() {
	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		c.cancel()
		for _, g := range c.pending {
			g.cancel()
			for s := range g.subs {
				s.done <- delivery{err: ErrCoordinatorStopped}
				delete(g.subs, s)
			}
		}
		c.pending = nil
		c.ready.Broadcast()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *Coordinator) execute(ctx context.Context, req Request, protocol *protocolFact) (Result, error) {
	p, err := newWorkPlanner(req.Observation, req.Intent, workPolicy{SNMPAfterTCPFailure: req.Options.SNMPAfterTCPFailure, RequireRequestedMetrics: true})
	if err != nil {
		return Result{}, err
	}
	if req.Options.SkipTCP {
		p.outcomes[StageReachability] = StageOutcome{Stage: StageReachability, Status: StatusSkipped, Reason: ReasonNotRequested}
	} else if protocol != nil {
		p = p.withProtocol(*protocol, time.Now())
	}
	result := Result{Observation: req.Observation, Intent: req.Intent, Fields: make(map[string]Provenance)}
	var collected *QueryResult
	var collectedAt time.Time
	var failures []error
	unsafe := false
	for {
		var stage WorkStage
		var ok bool
		p, stage, ok = p.dispatch(time.Now())
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		start := time.Now()
		if c.cfg.Logger != nil {
			c.cfg.Logger.Debug("Scanner stage started", "ip", req.Observation.IP.String(), "stage", stage)
		}
		var stageErr, orderErr error
		if stage == StageReachability {
			var ports []uint16
			for attempt := 0; attempt <= req.Options.Retries; attempt++ {
				if stageErr = ctx.Err(); stageErr != nil {
					break
				}
				if c.cfg.Backend.ProbeWithPorts != nil {
					ports, stageErr = c.cfg.Backend.ProbeWithPorts(ctx, req.Observation.IP, append([]uint16(nil), req.Options.Ports...))
				} else {
					ports, stageErr = c.cfg.Backend.Probe(ctx, req.Observation.IP)
				}
				if stageErr == nil && len(req.Options.Ports) != 0 {
					for _, port := range ports {
						found := false
						for _, requested := range req.Options.Ports {
							found = found || port == requested
						}
						if !found {
							stageErr = errors.New("probe returned an unrequested port")
							break
						}
					}
				}
				if stageErr == nil || ctx.Err() != nil {
					break
				}
			}
			if ctx.Err() != nil {
				stageErr = ctx.Err()
			}
			if stageErr == nil {
				result.OpenPorts = append([]uint16(nil), ports...)
			}
			var port uint16
			for _, v := range ports {
				if v != 0 {
					port = v
					break
				}
			}
			now := time.Now()
			p, orderErr = p.recordTCP(tcpFacts{Provenance: coordinatorProvenance(req, now, ProtocolTCP, "tcp"), OpenPort: port}, stageErr, now)
		} else {
			profile := QueryMinimal
			switch stage {
			case StageIdentity:
				if req.Preset == IntentLive || req.Preset == IntentManual {
					profile = QueryEssential
				}
			case StageDetail:
				profile = QueryFull
			case StageMetrics:
				profile = QueryMetrics
			}
			// Live enrichment can reuse its essential response; explicit/full
			// detail requests always use a full walk. Diagnostic Full also counts.
			reuse := stage == StageDetail && collected != nil && (collected.Profile == QueryFull || (req.Preset == IntentLive && !req.Options.FullDetail && collected.Profile == QueryEssential))
			var qr *QueryResult
			var received time.Time
			if reuse {
				qr, received = collected, collectedAt
			} else {
				qr, received, stageErr = c.query(ctx, req, stage, profile, false, &result)
			}
			facts, model, manufacturer, fields, parseErr := coordinatorIdentity(req, qr, received)
			if stageErr == nil {
				stageErr = parseErr
			}
			if stage == StageIdentity && stageErr == nil && facts.Serial == "" && req.Options.MissingSerialFallback {
				qr, received, stageErr = c.query(ctx, req, stage, QueryFull, true, &result)
				facts, model, manufacturer, fields, parseErr = coordinatorIdentity(req, qr, received)
				if stageErr == nil {
					stageErr = parseErr
				}
			}
			metrics, metricFields := coordinatorMetrics(req, qr, received, p.identity.Serial)
			if facts.Serial != "" {
				metrics.Serial = facts.Serial
			}
			if stage == StageIdentity {
				p, orderErr = p.recordIdentity(facts, stageErr, time.Now())
				if p.canCommit(time.Now()) {
					collected, collectedAt = qr, received
					metrics.Serial = p.identity.Serial
					if p.validMetrics(metrics, time.Now()) {
						p.metrics = metrics
					}
				}
			} else if stage == StageDetail {
				// A full response need not repeat serial; prior same-item identity
				// binds it. Any present contradictory serial is still rejected.
				if facts.Serial == "" && stageErr == nil {
					facts = p.identity
				}
				p, orderErr = p.recordDetail(detailFacts{Identity: facts, Model: model, Manufacturer: manufacturer, Metrics: metrics}, stageErr, time.Now())
			} else {
				p, orderErr = p.recordMetrics(metrics, stageErr, time.Now())
			}
			var identityError *OutcomeError
			if errors.As(stageErr, &identityError) && identityError.Outcome.Reason == ReasonIdentityConflict {
				p.conflict = true
				p.outcomes[stage].Reason = ReasonIdentityConflict
			}
			if stageErr == nil && p.canCommit(time.Now()) && p.outcomes[stage].Status != StatusFailed {
				for name, prov := range fields {
					result.Fields[name] = prov
				}
				for name, prov := range metricFields {
					result.Fields[name] = prov
				}
				if model != "" {
					result.Model = model
				}
				if manufacturer != "" {
					result.Manufacturer = manufacturer
				}
			}
		}
		if orderErr != nil {
			failures = append(failures, orderErr)
			break
		}
		if p.conflict {
			p.outcomes[stage].Status = StatusFailed
			p.outcomes[stage].Reason = ReasonIdentityConflict
		}
		o := p.outcomes[stage]
		if stage != StageReachability && (o.Status == StatusNegative || o.Status == StatusFailed) {
			unsafe = true
		}
		if c.cfg.Logger != nil {
			args := []interface{}{"ip", req.Observation.IP.String(), "stage", stage, "duration", time.Since(start), "status", o.Status, "reason", o.Reason, "error", stageErr != nil}
			if o.Status == StatusFailed {
				c.cfg.Logger.Error("Scanner stage failed", args...)
			} else if o.Status == StatusNegative {
				c.cfg.Logger.Warn("Scanner stage negative", args...)
			} else {
				c.cfg.Logger.Info("Scanner stage completed", args...)
			}
		}
		if stageErr != nil {
			failures = append(failures, fmt.Errorf("stage %d: %w", stage, stageErr))
		}
		if o.Status == StatusNegative || o.Status == StatusFailed {
			failures = append(failures, &OutcomeError{Outcome: o})
		}
	}
	result.Outcomes, result.Serial = p.outcomes, p.identity.Serial
	if req.Intent.Identity && result.Outcomes[StageIdentity].Status == StatusSkipped {
		failures = append(failures, &OutcomeError{Outcome: result.Outcomes[StageIdentity]})
	}
	if p.conflict {
		// Earlier facts cannot safely be attributed after contradictory identity.
		result.Serial, result.Model, result.Manufacturer = "", "", ""
		clear(result.Fields)
	}
	if c.cfg.Logger != nil {
		for _, o := range result.Outcomes {
			if o.Status == StatusSkipped {
				c.cfg.Logger.Debug("Scanner stage skipped", "ip", req.Observation.IP.String(), "stage", o.Stage, "duration", time.Duration(0), "status", o.Status, "reason", o.Reason)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	if ctx.Err() == nil && !unsafe && !req.Options.ReadOnly && p.canCommit(time.Now()) && c.cfg.Commit != nil {
		if err := c.cfg.Commit(ctx, cloneCoordinatorResult(result)); err != nil {
			failures = append(failures, fmt.Errorf("scanner commit: %w", err))
			if c.cfg.Logger != nil {
				// Commit errors are local storage errors, not remote payloads.
				c.cfg.Logger.Error("Scanner commit failed", "ip", req.Observation.IP.String(), "error", err.Error())
			}
		} else {
			result.Committed = true
			if c.cfg.Wake != nil {
				c.cfg.Wake()
			}
		}
	}
	return result, errors.Join(failures...)
}

func coordinatorProvenance(req Request, at time.Time, protocol FactProtocol, field string) Provenance {
	return Provenance{Source: req.Observation.Source, IP: req.Observation.IP, Protocol: protocol, ReceivedAt: at, Field: field}
}

func (c *Coordinator) query(ctx context.Context, req Request, stage WorkStage, profile QueryProfile, diagnostic bool, result *Result) (*QueryResult, time.Time, error) {
	timeout := req.Options.TimeoutSeconds
	if profile == QueryFull {
		timeout = req.Options.FullTimeoutSeconds
	}
	var qr *QueryResult
	var err error
	var at time.Time
	for attempt := 0; attempt <= req.Options.Retries; attempt++ {
		if err = ctx.Err(); err != nil {
			break
		}
		if c.cfg.Logger != nil {
			c.cfg.Logger.Debug("Scanner query", "ip", req.Observation.IP.String(), "stage", stage, "profile", profile.String(), "attempt", attempt, "diagnostic", diagnostic)
		}
		queryCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		qr, err = c.cfg.Backend.Query(queryCtx, QueryRequest{IP: req.Observation.IP, Profile: profile, VendorHint: req.Options.VendorHint, LearnedSerialOID: req.Options.LearnedSerialOID, TimeoutSeconds: timeout, Metrics: append([]MetricField(nil), req.Metrics...)})
		at = time.Now() // Local receipt at the actual backend callback, before parsing/cloning.
		if queryCtx.Err() != nil {
			err = queryCtx.Err()
		}
		cancel()
		qr = cloneQueryResult(qr)
		if qr == nil {
			if err == nil {
				err = errors.New("nil query response")
			}
		} else if ip, parseErr := netip.ParseAddr(qr.IP); parseErr != nil || ip.Unmap() != req.Observation.IP || qr.Profile != profile {
			err = errors.Join(err, errors.New("query response target or profile mismatch"))
		}
		if qr != nil {
			result.Queries = append(result.Queries, ObtainedQuery{Stage: stage, Diagnostic: diagnostic, ReceivedAt: at, Result: cloneQueryResult(qr), Err: err})
		}
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return qr, at, err
}

// Pure identity extraction accepts only serial-bearing OIDs and structured
// IEEE-1284 payloads. Arbitrary strings, sysDescr labels and external hints never
// establish identity. Contradictory serial fields fail closed.
func coordinatorIdentity(req Request, qr *QueryResult, at time.Time) (identityFacts, string, string, map[string]Provenance, error) {
	f := identityFacts{Provenance: coordinatorProvenance(req, at, ProtocolSNMP, ""), Class: printerConfirmed}
	fields := make(map[string]Provenance)
	var model, manufacturer string
	if qr == nil {
		return f, model, manufacturer, fields, nil
	}
	for _, pdu := range qr.PDUs {
		if pdu.Type != gosnmp.OctetString {
			continue
		}
		name, value := normalizeOID(pdu.Name), coordinatorString(pdu.Value)
		var serial string
		if _, structured := LookupVendorIDTarget(name); structured || name == oids.PpmPrinterIEEE1284DeviceID+".1" {
			for _, part := range strings.Split(value, ";") {
				i := strings.IndexAny(part, ":=")
				if i < 0 {
					continue
				}
				key, v := strings.ToLower(strings.TrimSpace(part[:i])), strings.TrimSpace(part[i+1:])
				switch key {
				case "sn", "serial", "ser":
					if serial != "" && v != "" && serial != v {
						return f, model, manufacturer, fields, &OutcomeError{Outcome: StageOutcome{Stage: StageIdentity, Status: StatusFailed, Reason: ReasonIdentityConflict}}
					}
					if v != "" {
						serial = v
					}
				case "mdl", "model":
					if v != "" {
						model = v
						fields["model"] = coordinatorProvenance(req, at, ProtocolSNMP, name+":"+key)
					}
				case "mfg", "manufacturer":
					if v != "" {
						manufacturer = v
						fields["manufacturer"] = coordinatorProvenance(req, at, ProtocolSNMP, name+":"+key)
					}
				}
			}
		} else if name == oids.PrtGeneralSerialNumber || (req.Options.LearnedSerialOID != "" && name == req.Options.LearnedSerialOID) {
			serial = value
		}
		if name == oids.HrDeviceDescr && value != "" {
			model = value
			fields["model"] = coordinatorProvenance(req, at, ProtocolSNMP, name)
		}
		if serial == "" {
			continue
		}
		if f.Serial != "" && f.Serial != serial {
			return f, model, manufacturer, fields, &OutcomeError{Outcome: StageOutcome{Stage: StageIdentity, Status: StatusFailed, Reason: ReasonIdentityConflict}}
		}
		f.Serial, f.Field = serial, name
		fields["serial"] = f.Provenance
	}
	return f, model, manufacturer, fields, nil
}

func coordinatorString(v interface{}) string {
	var s string
	switch value := v.(type) {
	case string:
		s = value
	case []byte:
		s = string(value)
	}
	return strings.Trim(strings.TrimSpace(s), "\x00 \t\r\n")
}

func coordinatorMetrics(req Request, qr *QueryResult, at time.Time, serial string) (metricsFacts, map[string]Provenance) {
	f := metricsFacts{Provenance: coordinatorProvenance(req, at, ProtocolSNMP, ""), Serial: serial}
	fields := make(map[string]Provenance)
	if qr == nil {
		return f, fields
	}
	for _, field := range req.Metrics {
		for _, pdu := range qr.PDUs {
			name := normalizeOID(pdu.Name)
			if name != field.OID && !strings.HasPrefix(name, field.OID+".") {
				continue
			}
			valid := false
			if field.Kind == MetricText {
				valid = pdu.Type == gosnmp.OctetString && coordinatorString(pdu.Value) != ""
			} else {
				switch pdu.Type {
				case gosnmp.Integer, gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Uinteger32:
					_, err := strconv.ParseUint(fmt.Sprint(pdu.Value), 10, 64)
					valid = err == nil
				}
			}
			if !valid {
				continue
			}
			prov := coordinatorProvenance(req, at, ProtocolSNMP, name)
			fields[field.Name] = prov
			if f.Field == "" {
				f.Provenance = prov
			}
			if name == oids.PrtMarkerLifeCount || strings.HasPrefix(name, oids.PrtMarkerLifeCount+".") {
				f.HasPageCount = true
				f.PageCount, _ = strconv.ParseUint(fmt.Sprint(pdu.Value), 10, 64)
			}
			break
		}
	}
	f.HasRequestedFields = len(fields) == len(req.Metrics)
	return f, fields
}

func cloneQueryResult(q *QueryResult) *QueryResult {
	if q == nil {
		return nil
	}
	copy := *q
	copy.PDUs = clonePDUs(q.PDUs)
	if q.Capabilities != nil {
		caps := *q.Capabilities
		caps.Scores = make(map[string]float64, len(q.Capabilities.Scores))
		for key, value := range q.Capabilities.Scores {
			caps.Scores[key] = value
		}
		copy.Capabilities = &caps
	}
	return &copy
}

func clonePDUs(pdus []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	if pdus == nil {
		return nil
	}
	out := make([]gosnmp.SnmpPDU, len(pdus))
	copy(out, pdus)
	for i := range out {
		switch value := out[i].Value.(type) {
		case []byte:
			out[i].Value = append([]byte(nil), value...)
		case []int:
			out[i].Value = append([]int(nil), value...)
		}
	}
	return out
}

func cloneObservation(o Observation) Observation {
	if o.Hints.MDNS.Record != nil {
		record := *o.Hints.MDNS.Record
		record.TXT = append([]string(nil), record.TXT...)
		record.Addresses = append([]netip.Addr(nil), record.Addresses...)
		o.Hints.MDNS.Record = &record
	}
	if o.Hints.Trap.Packet != nil {
		packet := *o.Hints.Trap.Packet
		packet.PDUs = clonePDUs(packet.PDUs)
		o.Hints.Trap.Packet = &packet
	}
	return o
}

func cloneRequest(r Request) Request {
	r.Observation = cloneObservation(r.Observation)
	r.Options.Ports = append([]uint16(nil), r.Options.Ports...)
	r.Metrics = append([]MetricField(nil), r.Metrics...)
	return r
}

func cloneCoordinatorResult(r Result) Result {
	r.Observation = cloneObservation(r.Observation)
	fields := make(map[string]Provenance, len(r.Fields))
	for key, value := range r.Fields {
		fields[key] = value
	}
	r.Fields = fields
	r.OpenPorts = append([]uint16(nil), r.OpenPorts...)
	r.Queries = append([]ObtainedQuery(nil), r.Queries...)
	for i := range r.Queries {
		r.Queries[i].Result = cloneQueryResult(r.Queries[i].Result)
	}
	return r
}
