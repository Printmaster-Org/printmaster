# Scanner Module Documentation

**Location**: `agent/scanner/`

The scanner module is responsible for device detection, SNMP querying, and printer information extraction. It provides a vendor-aware, configurable scanning system.

## Serial-label fallback (reviewed behavior)

The discovery parser in `agent/agent/parse.go` can extract a fallback serial from
explicitly labeled PDU text when a direct serial is unavailable. Labels are
case-insensitive: `SN`, `S/N`, `Serial`, `SerialNumber`, or `Serial Number`.
A label must start and end at a word boundary and be separated from its value by
one or more colons, equals signs, or whitespace characters. Examples:
`SN:ABC123`, `S/N ABC123`, and `Serial Number: ABC123`.

The fallback value retains the existing 4–40 character ASCII letter/digit/hyphen
matcher and subsequent UUID/OID/supply-model rejection. `SNMPv2`, `SN123456`, and
labels embedded in words are not serial labels; a later valid label can still
match. Direct serial OIDs and structured vendor device-ID parsing are unchanged.
This heuristic does not make an IP address proof of identity and does not change
the separate known-device liveness identity validator.

Offline regression coverage is `TestSerialLabelBoundaries` in
`agent/agent/parse_test.go`; it exercises the production matcher without invoking
the parser's web-UI probes. The MIB-walk analysis tools have separate legacy
matchers; they are not changed by this runtime fix.

This section reviews only serial-label extraction. The architecture and examples
below are historical and still require reconciliation during pipeline consolidation;
no coordinator, stage-skip policy, persistence, or scheduling change is implemented
by this fix.

## Architecture Overview

### Coordinator pipeline (production)

#### Uploader wake foundation

The Agent upload worker now exposes `Wake()` for callers that have successfully
committed local facts. It is nonblocking, coalesces into a one-slot channel, and
uses a fixed one-second batching window; repeated wakes do not extend that window.
Periodic uploads retain their cadence and can satisfy a pending wake. The wake
channel is never closed, so late producers remain safe during/after shutdown.
`Stop()` cancels active uploads/retry waits and joins loops; there is no guaranteed
final flush. Use a fresh worker instance rather than restarting a stopped one.
The scanner runtime wakes the uploader after each successful commit.
Payloads, credentials, routes and protocol versions are unchanged.

`work.go` provides typed observations, intents, provenance, stage outcomes and a
pure value-based planner. `coordinator.go` owns a bounded queue and shared workers
around an injected probe/query backend. These are independently tested library
foundations; their presence does not mean production discovery has migrated.

The ordered workload is reachability → identity → detail → optional metrics.
An IP or `KnownDeviceHint` never proves identity. Every identity request validates
a fresh SNMP serial from an approved identity OID or structured vendor device ID;
a contradictory expected serial blocks attribution and commit. Detail responses
may retain that same item's validated identity, but contradictory serials cannot
be merged. No cross-item identity cache exists.

`Do` never skips TCP based on caller hints. `DoSource` is an explicit wiring
contract for a real adapter callback, not authentication of caller metadata.
Only locally received, target-bound, validated protocol responses no older than
30 seconds can skip reachability; zero/future timestamps, indirect advertised
targets and malformed messages cannot. Queued evidence is rechecked at dispatch;
once identity starts, receipt expiry cannot rewind the in-flight stage. Adapters
must preserve actual receipt time rather than stamp stale hints during enqueue.

Quick/liveness presets request identity; full requests detail and metrics; live
allows essential-response enrichment; manual stays essential unless explicitly
upgraded. Metrics run only when requested/due, and all requested fields must be
fresh in the same item to reuse a response. Zero counters are valid; missing
fields are not zeros. Negative TCP is not an offline assertion. Failed, negative,
skipped-with-reason and not-checked outcomes remain distinct.

Exact active requests coalesce; compatible pending requests union their work;
active upgrades run as fresh followups. The coordinator serializes each target.
Subscriber cancellation removes only that subscriber; the last subscriber cancels
its work. Saturation returns `ErrCoordinatorQueueFull`. The owner calls `Close`
to cancel/join workers; backend and commit callbacks must honor cancellation and
must not call `Close` themselves. No source owns or closes coordinator queues.

Commit and uploader wake are injected, not implemented by this library slice.
Wake occurs only after commit succeeds, never for read-only work. Logs cover
admission/coalescing, stage start/outcome, conflicts, persistence and shutdown;
they exclude serials, protocol payloads, credentials and backend error strings.
`work_test.go` and `coordinator_test.go` use offline fakes for ordering, evidence,
identity mismatch, metrics reuse, retries, cancellation, queue ownership and wake
ordering; coordinator race tests are mandatory.

#### Partial persistence (`storage.CommitScannerFacts`)

`SQLiteStore.CommitScannerFacts` persists only facts a work item obtained, in one
transaction: an optional device patch, optional scan snapshot, optional metrics
snapshot. Nil/blank patch fields never erase stored facts; locked fields, user
state, visibility, saved state, classification and page-count baselines are not
patchable. Raw metadata merges recursively. Identity-only work creates no scan,
metrics or `last_seen`; liveness-only work updates only `last_seen`.

Creating a device or changing its IP requires `ValidatedSerial` equal to the
commit serial. `expectedIP` is a compare-and-set guard on the **prior** stored
address, not the observed destination; liveness and address moves require it.
Other serials still recorded at the destination (DHCP reuse, replaced printers,
including hidden/saved rows and IPv4-mapped aliases) are left untouched and only
counted in an info log: the committing identity was validated at that address,
and liveness touches require a serial match, so stale rows cannot be refreshed.
Serials are opaque keys: path separators,
control bytes and surrounding whitespace are rejected, never normalized.

Supplied metrics use the legacy `SaveMetricsSnapshot` drop policy (all-zero,
>5%/min-10 decrease, >10%/min-100 breakdown mismatch) read inside the same
transaction; an omitted sample still commits the other facts and logs a fixed
reason. Outcome logs contain counts/reasons only. Callers may wake the uploader
only after a nil return. `stage_commit_test.go` covers trigger-proven rollback,
concurrent merges, locks, moves, occupancy and metric parity.

#### Network source adapters (`agent/agent`)

`Start{MDNS,SSDP,WSDiscovery,SNMPTrap,LLMNR}ObservationBrowser` decode packets into
`scanner.Observation` values that keep protocol evidence: mDNS service/instance/
host/port/TXT/addresses; SSDP USN/ST/NT/NTS/LOCATION/sender/filter decision;
WS-Discovery endpoint/types/scopes/XAddrs/sender (IPv4 XAddrs only; no sender
fallback); traps' version/PDU type/trap and enterprise OIDs/varbinds plus a
printer-MIB or vendor eligibility label (other traps are dropped); LLMNR hostname,
sender and A answer. Typed hints replace the old `ScanMeta`/map mismatch.

Adapters stamp `ObservedAt` with local receipt time only for target-bound,
structurally credible responses; announcements, queries and URL-only targets
keep a zero time and therefore never skip TCP. The coordinator revalidates
anyway. Callbacks run synchronously on one owner goroutine per browser; adapters
do not spawn per-candidate work, assert identity, or mark stages complete.
Per-IP throttling (10 minutes) counts only accepted submissions and uses private
state. Cancellation stops reads, drains zeroconf streams, and joins trap
listeners; socket/setup errors reach `SourceErrorCallback`.

#### Production wiring (`agent/scanner_runtime.go`)

One `scannerRuntime` owns the coordinator for the Agent process. Every network
entry point submits through it: range discovery (`Discover`/`DiscoverNow`, quick
and full presets), live sources (`RequestSource`, live preset), manual refresh,
post-update identity refresh, known-device liveness and scheduled/manual metrics.
USB/spooler printers stay on their own adapter and never fake reachability.

- The `ip_scanning_enabled` setting gates every network request.
- An in-memory IP index (refreshed every minute and after commits) supplies only
  query hints (learned serial OID, vendor). It never acts as identity: a stale
  learned OID that contradicts the device triggers one retry without hints, and a
  replaced printer on a reused address is recorded as its own serial.
- Explicit expected serials (manual refresh, metrics) must match the device.
- Commits use `CommitScannerFacts` with the stored prior IP as compare-and-set
  guard, then refresh the index, broadcast SSE and wake the uploader.
- Live listeners throttle each IP for 10 minutes, keep adapter receipt time, and
  run scans off the listener goroutine; stop/start never clobbers a new listener.
- Enrichment stores normalized facts (capabilities, meters, learned OIDs,
  uptime, trays) plus compact `discovery_evidence`, never raw PDU dumps.
- Metrics snapshots use positive-only vendor-meter overrides (`total_pages`,
  `copy_pages`, `fax_pages`, `scans`, …) and mono-aware toner levels; requested or
  learned fields only fill counters the parser did not obtain.

```
scanner/
├── work.go            # Observations, intents, stage outcomes, pure planner
├── coordinator.go     # Bounded queue, per-IP serialization, stage execution
├── query.go           # SNMP profiles (minimal/essential/full/metrics)
├── snmp.go            # SNMP client configuration
├── capabilities/      # Capability detection
└── vendor/            # Vendor OID modules and registry
```

Run `go test ./scanner/... ./storage ./agent .` from `agent/`; coordinator and
storage tests must also pass with `-race`.

## Troubleshooting

- **No devices found:** confirm IP scanning is enabled, TCP 9100/80/443 (plus
  515/631 for full scans) are reachable, and SNMP (UDP 161) answers with the
  configured credentials.
- **Device not updated:** check logs for `Scanner identity check failed`,
  `Scanner detail check failed` (each entry names the stage, reason, query and
  error type) or `Scanner commit failed`; identity conflicts never overwrite
  another serial. Addresses with no device, non-printers and non-SNMP hosts are
  logged at debug level only.
- **Live source silent:** check `Live discovery source failed`; trap listening on
  UDP 162 needs elevated privileges.
