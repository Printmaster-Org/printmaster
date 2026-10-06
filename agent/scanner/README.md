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

### Consolidation foundation (not yet wired into Agent startup)

#### Uploader wake foundation

The Agent upload worker now exposes `Wake()` for callers that have successfully
committed local facts. It is nonblocking, coalesces into a one-slot channel, and
uses a fixed one-second batching window; repeated wakes do not extend that window.
Periodic uploads retain their cadence and can satisfy a pending wake. The wake
channel is never closed, so late producers remain safe during/after shutdown.
`Stop()` cancels active uploads/retry waits and joins loops; there is no guaranteed
final flush. Use a fresh worker instance rather than restarting a stopped one.
These are uploader capabilities only: scanner wiring remains a separate slice.
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
A destination held by any other serial (including hidden/saved rows and
IPv4-mapped aliases) rejects the whole transaction, so stale runtime caches
cannot authorize a conflicting move. Serials are opaque keys: path separators,
control bytes and surrounding whitespace are rejected, never normalized.

Supplied metrics use the legacy `SaveMetricsSnapshot` drop policy (all-zero,
>5%/min-10 decrease, >10%/min-100 breakdown mismatch) read inside the same
transaction; an omitted sample still commits the other facts and logs a fixed
reason. Outcome logs contain counts/reasons only. Callers may wake the uploader
only after a nil return. `stage_commit_test.go` covers trigger-proven rollback,
concurrent merges, locks, moves, occupancy and metric parity.

The legacy descriptions below remain historical until the runtime migration
slice updates callers and documentation together. USB/spooler remains a separate
physical-source adapter; it must not manufacture network reachability.

```
scanner/
├── detector.go          # Device type detection (IsPrinter? confidence scoring)
├── pipeline.go          # Multi-stage scan orchestration (liveness → detection → deep scan)
├── query.go             # SNMP query execution and vendor-specific data collection
├── snmp.go              # Low-level SNMP communication wrapper
├── enumerator.go        # IP range enumeration and subnet handling
└── vendor/              # Vendor-specific OID profiles and parsers
    ├── hp.go
    ├── canon.go
    ├── brother.go
    ├── epson.go
    ├── kyocera.go
    ├── lexmark.go
    ├── ricoh.go
    ├── samsung.go
    ├── xerox.go
    ├── generic.go       # Fallback standard Printer-MIB
    └── registry.go      # Vendor detection and module selection
```

## Core Components

### Detector (`detector.go`)

**Purpose**: Determine if a device is a printer and calculate confidence score.

**Key Functions**:
- `IsPrinterDevice(ctx, ip, timeout) (bool, float64, error)`: Fast printer detection
- Checks multiple signals:
  - Open printer ports (9100 JetDirect, 631 IPP, 515 LPD)
  - SNMP sysObjectID matches known printer enterprise IDs
  - Presence of Printer-MIB OIDs
  - HP/Canon/Brother-specific OIDs
- Returns confidence score 0.0-1.0

**Example**:
```go
isPrinter, confidence, err := IsPrinterDevice(ctx, "192.168.1.100", 5)
if isPrinter && confidence > 0.7 {
    // High confidence this is a printer
}
```

### Query System (`query.go`)

**Purpose**: Execute SNMP queries and extract structured printer information.

**Key Functions**:
- `QueryDevice(ctx, ip, community, timeout) (*PrinterInfo, error)`: Main query function
- Vendor auto-detection via sysObjectID
- Builds OID list from vendor module + generic Printer-MIB
- Executes SNMP WALK/GET operations
- Parses results using vendor-specific parsers

**Query Flow**:
1. Initial SNMP GET for sysDescr + sysObjectID
2. Detect vendor from enterprise OID (e.g., HP = 1.3.6.1.4.1.11)
3. Load vendor module (hp.go, canon.go, etc.)
4. Build combined OID list (vendor + generic)
5. Execute SNMP WALK for all OIDs
6. Parse results with vendor parser
7. Fallback to generic parser for unknown vendors

### Pipeline (`pipeline.go`)

**Purpose**: Orchestrate multi-stage scanning with worker pools.

**Stages**:
1. **Liveness**: Fast TCP/ICMP checks (100-200 workers)
2. **Detection**: SNMP-based printer identification (20-50 workers)
3. **Deep Scan**: Full SNMP walks + metrics (3-10 workers)

**Key Functions**:
- `ScanRange(ctx, cidr, config) ([]PrinterInfo, error)`: Scan entire subnet
- `ScanIPs(ctx, ips, config) ([]PrinterInfo, error)`: Scan specific IPs

**Benefits**:
- Reduces wasted work (don't deep-scan non-printers)
- Configurable concurrency per stage
- Bounded resource usage
- Context-based cancellation

### Vendor System (`vendor/`)

**Purpose**: Vendor-specific OID profiles and data parsing.

**Interface** (`VendorModule`):
```go
type VendorModule interface {
    Name() string
    GetOIDs() []string
    ParseMetrics(pdus []gosnmp.SnmpPDU) map[string]interface{}
}
```

**Vendor Modules**:
- **HP** (`hp.go`): Extended metrics (job accounting, fax pages, ADF scans)
- **Canon** (`canon.go`): Canon-specific counters and status
- **Brother** (`brother.go`): Brother drum life, toner levels
- **Epson** (`epson.go`): Epson ink tank levels
- **Kyocera** (`kyocera.go`): Kyocera maintenance counters
- **Lexmark** (`lexmark.go`): Lexmark enterprise OIDs
- **Ricoh** (`ricoh.go`): Ricoh/Savin/Lanier shared OIDs
- **Samsung** (`samsung.go`): Samsung-specific metrics
- **Xerox** (`xerox.go`): Xerox FreeFlow OIDs
- **Generic** (`generic.go`): Standard Printer-MIB fallback

**Vendor Detection** (`registry.go`):
```go
func DetectVendor(sysObjectID string) VendorModule
```
Maps enterprise OIDs to vendors:
- `1.3.6.1.4.1.11.*` → HP
- `1.3.6.1.4.1.1602.*` → Canon
- `1.3.6.1.4.1.2435.*` → Brother
- etc.

### SNMP Wrapper (`snmp.go`)

**Purpose**: Low-level SNMP communication abstraction.

**Key Functions**:
- `SNMPGet(target, community, oids, timeout) ([]gosnmp.SnmpPDU, error)`
- `SNMPWalk(target, community, oid, timeout) ([]gosnmp.SnmpPDU, error)`
- `SNMPBulkWalk(target, community, oid, maxReps, timeout) ([]gosnmp.SnmpPDU, error)`

**Features**:
- Retry logic with exponential backoff
- Configurable timeouts
- Error handling and logging
- Thread-safe connection pooling

## Configuration

Scanner behavior is controlled via `ScannerConfig` struct in `main.go`:

```go
type scannerConfigStruct struct {
    SNMPTimeoutMs        int  // SNMP timeout in milliseconds (default: 2000)
    SNMPRetries          int  // SNMP retry count (default: 1)
    DiscoverConcurrency  int  // Max concurrent scan workers (default: 50)
    sync.RWMutex              // Thread-safe access
}
```

Settings are loaded from:
1. `config.json` (static config file)
2. SQLite database (via Settings UI)
3. Defaults if not specified

## Usage Examples

### Simple Single Device Query

```go
ctx := context.Background()
pi, err := scanner.QueryDevice(ctx, "192.168.1.100", "public", 5)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Found printer: %s %s (Serial: %s)\n", pi.Vendor, pi.Model, pi.Serial)
```

### Range Scan with Pipeline

```go
config := &scanner.ScanConfig{
    SNMPCommunity: "public",
    Timeout:       5,
    Concurrency:   50,
}

printers, err := scanner.ScanRange(ctx, "192.168.1.0/24", config)
for _, p := range printers {
    fmt.Printf("%s: %s %s\n", p.IP, p.Vendor, p.Model)
}
```

### Vendor-Specific Query

```go
// Auto-detects vendor from sysObjectID
pi, _ := scanner.QueryDevice(ctx, "10.0.0.50", "public", 5)

// Access vendor-specific metrics
if pi.Vendor == "HP" {
    fmt.Printf("Job Accounting Pages: %d\n", pi.ExtendedMetrics["JobAccountingPages"])
    fmt.Printf("Fax Pages: %d\n", pi.ExtendedMetrics["FaxPages"])
}
```

## Testing

**Test Files**:
- `detector_test.go`: Device detection and confidence scoring (8 tests)
- `query_test.go`: SNMP query and vendor parsing (21 tests)
- `pipeline_test.go`: Multi-stage pipeline orchestration (5 tests)

**Run Tests**:
```powershell
cd agent
go test ./scanner/... -v
```

**Test Coverage**:
```powershell
go test ./scanner/... -cover
```

## Performance Characteristics

### Timing (per device)

| Stage | Duration | Notes |
|-------|----------|-------|
| Liveness (TCP) | 100-500ms | Fast port checks |
| Detection (SNMP) | 500ms-2s | Limited OID query |
| Deep Scan (SNMP) | 2-10s | Full walk with retries |

### Concurrency Limits

| Stage | Default Workers | Tuning Notes |
|-------|----------------|--------------|
| Liveness | 100-200 | I/O bound, can be high |
| Detection | 20-50 | Network + SNMP processing |
| Deep Scan | 3-10 | Expensive, limit to prevent overwhelming devices |

### Memory Usage

- ~1KB per discovered device (in-memory PrinterInfo)
- ~5-10MB for SNMP library overhead
- Scales linearly with concurrent workers

## Error Handling

**Common Errors**:
- `context.DeadlineExceeded`: SNMP timeout
- `no such host`: Invalid IP or DNS failure
- `connection refused`: SNMP disabled on device
- `no response`: Firewall blocking UDP 161

**Retry Strategy**:
- SNMP queries: Retry once with exponential backoff
- TCP probes: No retry (fail fast)
- Pipeline stages: Continue on individual device failures

## Integration Points

### With Agent Package (`agent/agent/`)

Scanner is called by:
- `Discover()` in `detect.go`: Full network scan
- `LiveDiscoveryDetect()` in `scanner_api.go`: Single device enrichment
- Live discovery handlers (mDNS, SSDP, WS-Discovery)

### With Storage (`agent/storage/`)

Results are persisted via:
- `UpsertDevice()`: Save/update discovered printer
- `GetDevices()`: Retrieve all stored printers
- `DeleteDevice()`: Remove printer from database

### With Logger (`agent/logger/`)

Scanner logs to structured logger:
- Info: Discovery progress, device found
- Warn: SNMP failures, timeout warnings
- Error: Critical failures, configuration errors
- Debug: Detailed SNMP PDU parsing

## Future Enhancements

### Planned Features
- [ ] SNMPv3 support with authentication/encryption
- [ ] Bulk GET for improved performance
- [ ] Result caching with configurable TTL
- [ ] Dynamic OID discovery via MIB walking
- [ ] Parallel vendor module querying

### Performance Improvements
- [ ] Connection pooling for SNMP clients
- [ ] Adaptive timeout based on network latency
- [ ] Progressive OID querying (core → extended)
- [ ] Background re-scan of changed devices

## Troubleshooting

### No Devices Found
1. Check network connectivity: `ping <device_ip>`
2. Verify SNMP enabled on printer
3. Test SNMP manually: `snmpwalk -v2c -c public <device_ip> .1.3.6`
4. Check firewall allows UDP 161 outbound

### Incomplete Data
1. Increase SNMP timeout in settings
2. Check vendor module supports printer model
3. Review logs for parsing errors
4. Try generic fallback (disables vendor detection)

### Slow Scans
1. Reduce concurrency if network is saturated
2. Lower SNMP timeout for faster failures
3. Use liveness pre-filtering to skip dead IPs
4. Limit scan range to active subnets

## Related Documentation

- [Agent Module](../agent/README.md) - Discovery protocols and detection logic
- [Logger Module](../logger/README.md) - Logging system
- [Settings TODO](../../docs/SETTINGS_TODO.md) - Unimplemented scanner features
- [API Reference](../../docs/API_REFERENCE.md) - HTTP endpoints using scanner
