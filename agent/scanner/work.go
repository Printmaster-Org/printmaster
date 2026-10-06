package scanner

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Observation is an external discovery request, never proof of a completed stage.
// ObservedAt is informational to Coordinator.Do. DoSource requires an actual
// adapter's local receipt and validates raw message shape/target/freshness before
// suppressing TCP. Neither API authenticates callers through typed metadata.
// Known and protocol hints are never identity assertions.
type Observation struct {
	IP         netip.Addr
	Source     ObservationSource
	ObservedAt time.Time
	Known      KnownDeviceHint
	Hints      ProtocolHints
}

type ObservationSource uint8

const (
	SourceUnknown ObservationSource = iota
	SourceMDNS
	SourceSSDP
	SourceWSD
	SourceTrap
	SourceLLMNR
	SourceManual
	SourceRange
)

type KnownDeviceHint struct {
	Serial string
	IP     netip.Addr // Previous address; a matching address alone proves nothing.
}

// These small value types preserve discovery metadata without a plugin registry,
// opaque payloads, exported "trusted" flags, or caller-injected stage results.
type ProtocolHints struct {
	MDNS  MDNSHint
	SSDP  SSDPHint
	WSD   WSDHint
	Trap  TrapHint
	LLMNR LLMNRHint
}

// Payload pointers retain Observation's comparability. Producers own copies;
// consumers must treat payloads as immutable and compare contents when merging.
type MDNSHint struct {
	Service, Instance, Hostname string
	Record                      *MDNSRecord
}
type MDNSRecord struct {
	Port      int
	TXT       []string
	Addresses []netip.Addr
}
type SSDPHint struct {
	USN, SearchTarget, Location                                    string
	Sender                                                         netip.Addr
	NotificationType, NotificationSubtype, Message, FilterDecision string
}
type WSDHint struct {
	Endpoint, Types, XAddr string
	Scopes, Message        string
	Sender                 netip.Addr
}
type TrapHint struct {
	EnterpriseOID, TrapOID string
	Sender                 netip.Addr
	Vendor, Eligibility    string
	Packet                 *TrapPacket
}
type TrapPacket struct {
	PDUs                      []gosnmp.SnmpPDU
	GenericTrap, SpecificTrap int
	Version                   gosnmp.SnmpVersion
	PDUType                   gosnmp.PDUType
}
type LLMNRHint struct {
	Hostname       string
	Sender, Answer netip.Addr
	Message        string
}

type IntentPreset uint8

const (
	IntentQuick IntentPreset = iota + 1
	IntentFull
	IntentLive
	IntentManual
	IntentLiveness
	IntentMetrics
)

// Intent describes requested work, not permissions or stage satisfaction.
// MetricsDue is supplied by scheduling policy; this planner owns no clock or DB.
type Intent struct {
	Reachability     bool
	Identity         bool
	Detail           bool
	MetricsRequested bool
	MetricsDue       bool
}

// PresetIntent defines initial policy explicitly. Live discovery enriches devices
// without forcing a snapshot; a scheduler may set MetricsDue. Full requests
// snapshots. Manual refresh stays essential unless full work is explicit.
// Liveness validates identity too, preventing IP-reuse attribution.
func PresetIntent(preset IntentPreset) (Intent, error) {
	switch preset {
	case IntentQuick, IntentLiveness, IntentManual:
		return Intent{Reachability: true, Identity: true}, nil
	case IntentFull:
		return Intent{Reachability: true, Identity: true, Detail: true, MetricsRequested: true}, nil
	case IntentLive:
		return Intent{Reachability: true, Identity: true, Detail: true}, nil
	case IntentMetrics:
		return Intent{Reachability: true, Identity: true, MetricsRequested: true}, nil
	default:
		return Intent{}, fmt.Errorf("unknown intent preset %d", preset)
	}
}

type WorkStage uint8

const (
	StageReachability WorkStage = iota
	StageIdentity
	StageDetail
	StageMetrics
	workStageCount
)

type StageStatus uint8

const (
	StatusNotChecked StageStatus = iota // Distinct from attempted negative/failed.
	StatusSucceeded
	StatusNegative
	StatusSkipped
	StatusFailed
)

type StageReason uint8

const (
	ReasonNone StageReason = iota
	ReasonNotRequested
	ReasonFreshProtocol
	ReasonNoTCPResponse // Does NOT mean offline; no offline state exists here.
	ReasonBackendFailure
	ReasonReachabilityRequired
	ReasonIdentityRequired
	ReasonNotPrinter
	ReasonMissingSerial
	ReasonIdentityConflict
	ReasonNoDetail
	ReasonNoMetrics
	ReasonFreshItemMetrics
	ReasonInvalidFacts
)

type FactProtocol uint8

const (
	ProtocolUnknown FactProtocol = iota
	ProtocolTCP
	ProtocolSNMP
	ProtocolMDNS
	ProtocolSSDP
	ProtocolWSD
	ProtocolTrap
	ProtocolLLMNR
)

// Provenance records the fact's target, protocol and local receipt, not a remote
// packet timestamp. Field identifies a parsed field/OID when applicable.
type Provenance struct {
	Source     ObservationSource
	Protocol   FactProtocol
	IP         netip.Addr
	ReceivedAt time.Time
	Field      string
}

// StageOutcome is read-only output by convention; supplying one to the planner
// is deliberately impossible. A future coordinator persists only validated facts,
// never this status alone (in particular a skip is not a successful probe).
type StageOutcome struct {
	Stage      WorkStage
	Status     StageStatus
	Reason     StageReason
	Provenance Provenance
	Serial     string
	Error      string
}

type workPolicy struct {
	// Explicit opt-in: TCP filtering need not prevent a direct SNMP identity GET.
	SNMPAfterTCPFailure bool
	// Coordinator validates all requested metric fields, not merely page count.
	RequireRequestedMetrics bool
}

const workFactMaxAge = 30 * time.Second

// All trusted facts/state belong to scanner, the future coordinator's package.
// Source adapters must validate wire messages and stamp receipt locally before
// opting into DoSource; absent that wiring, observations never suppress TCP.
// The coordinator validates raw hints and concrete backend responses, NOT
// arbitrary injected stage statuses.
// Keeping ingestion unexported stops
// external Observation producers from manufacturing a "successful" stage.
type protocolFact struct {
	Provenance
	Message protocolMessage
}

type protocolMessage uint8

const (
	messageUnknown protocolMessage = iota
	messageMDNSAnswer
	messageSSDPResponse
	messageWSDResponse
	messageTrap
	messageLLMNRAnswer
)

type tcpFacts struct {
	Provenance
	OpenPort uint16 // Zero means no successful TCP connection.
}

type printerClass uint8

const (
	printerUnknown printerClass = iota
	printerConfirmed
	printerOther
)

type identityFacts struct {
	Provenance
	Serial string
	Class  printerClass
}

type detailFacts struct {
	Identity     identityFacts
	Manufacturer string
	Model        string
	Metrics      metricsFacts // Optional factual data from this same operation.
}

type metricsFacts struct {
	Provenance
	Serial             string
	PageCount          uint64
	HasPageCount       bool // Presence of a counter; zero pages is a valid sample.
	HasRequestedFields bool
}

// workPlanner is a single-item value. next/record return copies: no IO, wall clock,
// locks, shared caches or global state. The owner passes now explicitly, serializes
// one item's transitions and replaces its value. Cross-item identity reuse has no
// API; Known remains a hint and every identity request performs a fresh check.
type workPlanner struct {
	observation        Observation
	intent             Intent
	policy             workPolicy
	outcomes           [workStageCount]StageOutcome
	identity           identityFacts
	metrics            metricsFacts
	conflict           bool
	identityDispatched bool   // Receipt expiry cannot rewind an in-flight identity read.
	sourceError        string // Source failure is not a negative device probe.
}

func newWorkPlanner(observation Observation, intent Intent, policy workPolicy) (workPlanner, error) {
	// Validate the effective network target, not its IPv4-mapped spelling.
	observation.IP = observation.IP.Unmap()
	if !observation.IP.IsValid() || observation.IP.IsUnspecified() || observation.IP.IsMulticast() {
		return workPlanner{}, fmt.Errorf("unicast target IP required")
	}
	if observation.Source > SourceRange {
		return workPlanner{}, fmt.Errorf("unknown observation source")
	}
	observation.Known.Serial = strings.TrimSpace(observation.Known.Serial)
	if intent.Detail || intent.MetricsRequested || intent.MetricsDue {
		intent.Identity = true
	}
	if intent.Identity {
		intent.Reachability = true
	}
	p := workPlanner{observation: observation, intent: intent, policy: policy}
	for stage := StageReachability; stage < workStageCount; stage++ {
		p.outcomes[stage].Stage = stage
	}
	return p, nil
}

func (p workPlanner) fresh(prov Provenance, now time.Time) bool {
	return prov.IP.IsValid() && prov.IP.Unmap() == p.observation.IP &&
		!prov.ReceivedAt.IsZero() && !now.IsZero() && !prov.ReceivedAt.After(now) &&
		now.Sub(prov.ReceivedAt) <= workFactMaxAge
}

func (p workPlanner) withSourceFailure(err error) workPlanner {
	if err != nil {
		p.sourceError = err.Error()
	}
	return p
}

// withProtocol accepts only internally decoded, target-bound response evidence.
// Advertisements/URLs/hostnames in Observation never enter this path. Receipt
// must be local, no older than 30s; future or zero timestamps cannot skip TCP.
func (p workPlanner) withProtocol(fact protocolFact, now time.Time) workPlanner {
	if p.outcomes[StageReachability].Status != StatusNotChecked || !p.intent.Reachability || !p.fresh(fact.Provenance, now) {
		return p
	}
	credible := false
	switch fact.Message {
	case messageMDNSAnswer:
		credible = fact.Source == SourceMDNS && fact.Protocol == ProtocolMDNS
	case messageSSDPResponse:
		credible = fact.Source == SourceSSDP && fact.Protocol == ProtocolSSDP
	case messageWSDResponse:
		credible = fact.Source == SourceWSD && fact.Protocol == ProtocolWSD
	case messageTrap:
		credible = fact.Source == SourceTrap && fact.Protocol == ProtocolTrap
	case messageLLMNRAnswer:
		credible = fact.Source == SourceLLMNR && fact.Protocol == ProtocolLLMNR
	}
	if credible && fact.Source == p.observation.Source {
		p.outcomes[StageReachability] = StageOutcome{Stage: StageReachability, Status: StatusSkipped, Reason: ReasonFreshProtocol, Provenance: fact.Provenance}
	}
	return p
}

// next settles policy skips and returns exactly the earliest remaining stage.
// Calling it repeatedly on the same value/time is deterministic. No skip invents
// identity, metrics, or offline evidence. Only fresh same-item detail facts reuse
// a snapshot; persisted/cross-item samples have no ingestion path.
func (p workPlanner) next(now time.Time) (workPlanner, WorkStage, bool) {
	if o := p.outcomes[StageReachability]; o.Status == StatusSkipped && o.Reason == ReasonFreshProtocol && !p.fresh(o.Provenance, now) {
		// Evidence can expire between source receipt and dispatch. Replan TCP only
		// before identity has run; never rewind an already executed item.
		if p.outcomes[StageIdentity].Status == StatusNotChecked && !p.identityDispatched {
			p.outcomes[StageReachability] = StageOutcome{Stage: StageReachability}
		}
	}
	requested := [workStageCount]bool{p.intent.Reachability, p.intent.Identity, p.intent.Detail, p.intent.MetricsRequested || p.intent.MetricsDue}
	for stage := StageReachability; stage < workStageCount; stage++ {
		if p.outcomes[stage].Status != StatusNotChecked {
			continue
		}
		reason := ReasonNone
		switch {
		case !requested[stage]:
			reason = ReasonNotRequested
		case p.conflict:
			reason = ReasonIdentityConflict
		case stage == StageIdentity && !p.reachable() && !p.policy.SNMPAfterTCPFailure:
			reason = ReasonReachabilityRequired
		case stage > StageIdentity && !p.canCommit(now):
			reason = ReasonIdentityRequired
		case stage == StageMetrics && p.validMetrics(p.metrics, now):
			p.outcomes[stage] = StageOutcome{Stage: stage, Status: StatusSkipped, Reason: ReasonFreshItemMetrics, Provenance: p.metrics.Provenance, Serial: p.metrics.Serial}
			continue
		}
		if reason != ReasonNone {
			p.outcomes[stage] = StageOutcome{Stage: stage, Status: StatusSkipped, Reason: reason}
			continue
		}
		return p, stage, true
	}
	return p, 0, false
}

// dispatch freezes the receipt decision at actual stage admission. A slow
// identity query must not expire its prerequisite while recording its response;
// queued items still revalidate protocol freshness before starting any IO.
func (p workPlanner) dispatch(now time.Time) (workPlanner, WorkStage, bool) {
	p, stage, ok := p.next(now)
	if ok && stage == StageIdentity {
		p.identityDispatched = true
	}
	return p, stage, ok
}

func (p workPlanner) reachable() bool {
	o := p.outcomes[StageReachability]
	return o.Status == StatusSucceeded || (o.Status == StatusSkipped && o.Reason == ReasonFreshProtocol)
}

func (p workPlanner) validIdentity(f identityFacts, now time.Time) bool {
	// Identity already validated within this item remains valid across long walks.
	// Receipt freshness is checked at ingestion, not repeatedly at attribution.
	return f.IP.IsValid() && f.IP.Unmap() == p.observation.IP &&
		!f.ReceivedAt.IsZero() && !now.IsZero() && !f.ReceivedAt.After(now) && f.Protocol == ProtocolSNMP &&
		f.Class == printerConfirmed && strings.TrimSpace(f.Serial) != ""
}

func (p workPlanner) hasMetrics(f metricsFacts) bool {
	if p.policy.RequireRequestedMetrics {
		return f.HasRequestedFields
	}
	return f.HasPageCount
}

func (p workPlanner) validMetrics(f metricsFacts, now time.Time) bool {
	return !p.conflict && p.validIdentity(p.identity, now) &&
		p.fresh(f.Provenance, now) && f.Protocol == ProtocolSNMP &&
		f.Serial == p.identity.Serial && p.hasMetrics(f)
}

// canCommit gates future persistence/attribution, not IO itself. A new address
// can be reconciled against Known.Serial, never against Known.IP alone.
func (p workPlanner) canCommit(now time.Time) bool {
	return !p.conflict && p.outcomes[StageIdentity].Status == StatusSucceeded && p.validIdentity(p.identity, now)
}

func (p workPlanner) expect(stage WorkStage, now time.Time) (workPlanner, error) {
	q, next, ok := p.next(now)
	if !ok || next != stage {
		return p, fmt.Errorf("stage %d is not next", stage)
	}
	return q, nil
}

func (p workPlanner) result(stage WorkStage, prov Provenance, serial string, status StageStatus, reason StageReason, err error) workPlanner {
	o := StageOutcome{Stage: stage, Status: status, Reason: reason, Provenance: prov, Serial: serial}
	if err != nil {
		o.Error = err.Error()
	}
	p.outcomes[stage] = o
	return p
}

func (p workPlanner) recordTCP(f tcpFacts, err error, now time.Time) (workPlanner, error) {
	p, orderErr := p.expect(StageReachability, now)
	if orderErr != nil {
		return p, orderErr
	}
	if err != nil {
		return p.result(StageReachability, f.Provenance, "", StatusFailed, ReasonBackendFailure, err), nil
	}
	if !p.fresh(f.Provenance, now) || f.Protocol != ProtocolTCP {
		return p.result(StageReachability, f.Provenance, "", StatusFailed, ReasonInvalidFacts, nil), nil
	}
	if f.OpenPort == 0 {
		return p.result(StageReachability, f.Provenance, "", StatusNegative, ReasonNoTCPResponse, nil), nil
	}
	return p.result(StageReachability, f.Provenance, "", StatusSucceeded, ReasonNone, nil), nil
}

func (p workPlanner) recordIdentity(f identityFacts, err error, now time.Time) (workPlanner, error) {
	p, orderErr := p.expect(StageIdentity, now)
	if orderErr != nil {
		return p, orderErr
	}
	f.Serial = strings.TrimSpace(f.Serial)
	if err != nil {
		return p.result(StageIdentity, f.Provenance, f.Serial, StatusFailed, ReasonBackendFailure, err), nil
	}
	if !p.fresh(f.Provenance, now) || f.Protocol != ProtocolSNMP || f.Class > printerOther {
		return p.result(StageIdentity, f.Provenance, f.Serial, StatusFailed, ReasonInvalidFacts, nil), nil
	}
	if f.Serial != "" && p.observation.Known.Serial != "" && f.Serial != p.observation.Known.Serial {
		p.conflict = true
		return p.result(StageIdentity, f.Provenance, f.Serial, StatusFailed, ReasonIdentityConflict, nil), nil
	}
	if f.Class != printerConfirmed {
		return p.result(StageIdentity, f.Provenance, f.Serial, StatusNegative, ReasonNotPrinter, nil), nil
	}
	if f.Serial == "" {
		return p.result(StageIdentity, f.Provenance, "", StatusNegative, ReasonMissingSerial, nil), nil
	}
	p.identity = f
	return p.result(StageIdentity, f.Provenance, f.Serial, StatusSucceeded, ReasonNone, nil), nil
}

func (p workPlanner) recordDetail(f detailFacts, err error, now time.Time) (workPlanner, error) {
	p, orderErr := p.expect(StageDetail, now)
	if orderErr != nil {
		return p, orderErr
	}
	f.Identity.Serial = strings.TrimSpace(f.Identity.Serial)
	f.Metrics.Serial = strings.TrimSpace(f.Metrics.Serial)
	prov := f.Identity.Provenance
	if err != nil {
		return p.result(StageDetail, prov, f.Identity.Serial, StatusFailed, ReasonBackendFailure, err), nil
	}
	if (!p.fresh(prov, now) && f.Identity != p.identity) || prov.Protocol != ProtocolSNMP {
		return p.result(StageDetail, prov, f.Identity.Serial, StatusFailed, ReasonInvalidFacts, nil), nil
	}
	if (f.Identity.Serial != "" && f.Identity.Serial != p.identity.Serial) || (f.Metrics.Serial != "" && f.Metrics.Serial != p.identity.Serial) {
		p.conflict = true
		return p.result(StageDetail, prov, f.Identity.Serial, StatusFailed, ReasonIdentityConflict, nil), nil
	}
	if !p.validIdentity(f.Identity, now) {
		return p.result(StageDetail, prov, f.Identity.Serial, StatusFailed, ReasonInvalidFacts, nil), nil
	}
	// A serial-bearing detail read revalidates identity. Otherwise the owner may
	// retain this item's previously validated identity, with original provenance.
	p.identity = f.Identity
	if p.validMetrics(f.Metrics, now) {
		p.metrics = f.Metrics
	}
	if strings.TrimSpace(f.Model) == "" && strings.TrimSpace(f.Manufacturer) == "" {
		return p.result(StageDetail, prov, f.Identity.Serial, StatusNegative, ReasonNoDetail, nil), nil
	}
	return p.result(StageDetail, prov, f.Identity.Serial, StatusSucceeded, ReasonNone, nil), nil
}

func (p workPlanner) recordMetrics(f metricsFacts, err error, now time.Time) (workPlanner, error) {
	p, orderErr := p.expect(StageMetrics, now)
	if orderErr != nil {
		return p, orderErr
	}
	f.Serial = strings.TrimSpace(f.Serial)
	if err != nil {
		return p.result(StageMetrics, f.Provenance, f.Serial, StatusFailed, ReasonBackendFailure, err), nil
	}
	if !p.fresh(f.Provenance, now) || f.Protocol != ProtocolSNMP || strings.TrimSpace(f.Serial) == "" {
		return p.result(StageMetrics, f.Provenance, f.Serial, StatusFailed, ReasonInvalidFacts, nil), nil
	}
	if f.Serial != p.identity.Serial {
		p.conflict = true
		return p.result(StageMetrics, f.Provenance, f.Serial, StatusFailed, ReasonIdentityConflict, nil), nil
	}
	if !p.hasMetrics(f) {
		return p.result(StageMetrics, f.Provenance, f.Serial, StatusNegative, ReasonNoMetrics, nil), nil
	}
	p.metrics = f
	return p.result(StageMetrics, f.Provenance, f.Serial, StatusSucceeded, ReasonNone, nil), nil
}
