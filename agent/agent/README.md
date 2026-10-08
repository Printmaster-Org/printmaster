# Agent Module Documentation

**Location**: `agent/agent/`

This package provides protocol observation adapters, SNMP parsing, and identity
helpers. It does not own production scan scheduling or persistence; those are
wired through `scanner_runtime.go` and the shared scanner coordinator.

> **Historical sections below:** the discovery API and flow descriptions were
> written before the coordinator migration. The old `detect.go` and IP-only
> browser wrappers have been removed. For current production wiring, see
> [`agent/scanner/README.md`](../scanner/README.md).

## Current Package Structure

```
agent/
├── mdns.go             # Typed mDNS observations
├── ssdp.go             # Typed SSDP observations
├── wsdiscovery.go      # Typed WS-Discovery observations
├── snmptraps.go        # SNMP trap observations
├── llmnr.go            # LLMNR observations
├── identity.go         # Serial identity validation
├── parse.go            # SNMP PDU parsing and OID interpretation
├── rangeparser.go      # Range syntax parsing (used by scanner API)
├── snmp_iface.go       # SNMP interfaces for diagnostics and tests
└── types.go            # Printer and observation types
```

## Historical Discovery Behavior (pre-coordinator)

### 1. Active Network Scanning (`detect.go`)

**Purpose**: Enumerate IP addresses and perform liveness checks.

**Key Functions**:
- `Discover(ctx, ranges, mode, config, store, concurrency, timeout)`: Main discovery entry point
- `GetLocalSubnets()`: Enumerate local network interfaces and subnets
- `EnumerateIPs(cidr)`: Generate all IPs in CIDR range

**Discovery Modes**:
- `"full"`: Complete scan (ARP + ICMP + TCP + SNMP)
- `"quick"`: Fast scan (TCP ports only)
- `"deep"`: Full scan + extended SNMP walks

**Flow**:
1. Parse IP ranges (CIDR notation or range syntax)
2. Enumerate all IPs in ranges
3. Read ARP table for known devices
4. Probe IPs with TCP/ICMP (parallel worker pool)
5. Query promising devices with SNMP
6. Store results in database

**Example**:
```go
config := &DiscoveryConfig{
    ARPEnabled:  true,
    ICMPEnabled: true,
    TCPEnabled:  true,
    SNMPEnabled: true,
}
results, err := Discover(ctx, []string{"192.168.1.0/24"}, "full", config, db, 50, 10)
```

### 2. mDNS/Bonjour Discovery (`mdns.go`)

**Purpose**: Passive discovery of printers advertising via mDNS/DNS-SD.

**Key Functions**:
- `StartMDNS(ctx, callback)`: Listen for mDNS advertisements
- Services monitored:
  - `_ipp._tcp`: Internet Printing Protocol
  - `_ipps._tcp`: IPP over TLS
  - `_printer._tcp`: Generic printer service

**How It Works**:
- Listens on multicast address 224.0.0.251:5353
- Receives mDNS announcements from printers
- Extracts IP, hostname, service info from TXT records
- Calls callback for SNMP enrichment

**Best For**: macOS/Linux networks, modern IPP-capable printers, zero-configuration environments

### 3. SSDP/UPnP Discovery (`ssdp.go`)

**Purpose**: Discover devices via UPnP/SSDP protocol.

**Key Functions**:
- `StartSSDP(ctx, callback)`: Listen for SSDP notifications
- `SendSSDP_MSearch()`: Active discovery broadcast

**How It Works**:
- Listens on multicast 239.255.255.250:1900
- Receives NOTIFY messages from devices
- Sends M-SEARCH queries every 5 minutes
- Filters for printer-like device types

**Device Type Filters**:
- `printer`, `scanner`, `multifunction`
- UPnP device types containing "print" keyword

**Best For**: Consumer printers, UPnP-enabled devices, mixed vendor environments

### 4. WS-Discovery (`wsdiscovery.go`)

**Purpose**: Discover printers via Web Services Discovery protocol.

**Key Functions**:
- `StartWSDiscovery(ctx, callback)`: Listen for WSD messages
- `SendWSProbe()`: Active probe for WSD devices

**How It Works**:
- Listens on multicast 239.255.255.250:3702
- SOAP-based protocol over UDP
- Receives Hello/Bye messages
- Sends Probe requests for active discovery

**Message Types**:
- **Hello**: Device announces presence
- **Bye**: Device announces departure
- **Probe**: Agent requests device responses
- **ProbeMatch**: Device responds to probe

**Best For**: Windows environments, enterprise printers (HP, Canon, Epson), corporate networks

### 5. SNMP Traps (`snmptraps.go`)

**Purpose**: Listen for SNMP trap notifications from printers.

**Key Functions**:
- `StartSNMPTraps(ctx, callback)`: Listen on UDP 162
- Processes SNMPv1 and SNMPv2c traps

**How It Works**:
- Binds to UDP port 162 (requires admin/root)
- Receives trap notifications from configured printers
- Extracts source IP from trap
- Calls callback for SNMP enrichment
- 10-minute throttle to prevent duplicate discoveries

**Common Traps**:
- Device status changes
- Supply level alerts (toner low, paper out)
- Error conditions (paper jam, cover open)
- Warmup/cooldown events

**Configuration Required**: Printers must be configured to send traps to agent's IP address

**Best For**: Enterprise environments, proactive monitoring, real-time status updates

### 6. LLMNR (`llmnr.go`)

**Purpose**: Link-Local Multicast Name Resolution for Windows networks.

**Key Functions**:
- `StartLLMNR(ctx, callback)`: Listen for LLMNR queries/responses

**How It Works**:
- Listens on multicast 224.0.0.252:5355
- Windows alternative to mDNS
- Resolves hostnames to IPs on local network
- Enriches discovered IPs with hostnames

**Best For**: Windows-only networks without DNS, workgroup environments

### 7. ARP Table Reading (`arp.go`)

**Purpose**: Extract recently-seen devices from OS ARP cache.

**Key Functions**:
- `GetARPTable()`: Read system ARP cache
- Cross-platform implementation (Windows, Linux, macOS)

**How It Works**:
- **Linux**: Parses `/proc/net/arp`
- **Windows**: Executes `arp -a` command
- **macOS**: Executes `arp -an` command
- Returns IP → MAC address mappings

**Best For**: Initial seed of known devices, offline discovery, passive monitoring

## Data Structures

### PrinterInfo (`types.go`)

Core data structure representing a discovered printer:

```go
type PrinterInfo struct {
    IP               string
    MAC              string
    Hostname         string
    Vendor           string
    Model            string
    Serial           string
    Location         string
    Contact          string
    Description      string
    PageCount        int64
    ColorPageCount   int64
    DiscoveryMethods []string
    OpenPorts        []int
    SNMPAvailable    bool
    LastSeen         time.Time
    ExtendedMetrics  map[string]interface{}
}
```

### DiscoveryConfig (`config.go`)

Controls which discovery methods are enabled:

```go
type DiscoveryConfig struct {
    ARPEnabled  bool
    ICMPEnabled bool
    TCPEnabled  bool
    SNMPEnabled bool
    MDNSEnabled bool
}
```

### ScanMeta (`types.go`)

Metadata about scan operations:

```go
type ScanMeta struct {
    StartTime     time.Time
    EndTime       time.Time
    IPsScanned    int
    DevicesFound  int
    Errors        []string
}
```

## Integration with Scanner

The agent module delegates device querying to the scanner package:

```go
// In detect.go
import "printmaster/agent/scanner"

// Enrich discovered IP with SNMP data
pi, err := scanner.QueryDevice(ctx, ip, "public", timeout)
```

**Separation of Concerns**:
- **Agent**: Network discovery, protocol handling, IP enumeration
- **Scanner**: SNMP queries, vendor detection, data parsing

## Probing and Detection (`probe.go`)

**Purpose**: Fast connectivity checks before expensive SNMP queries.

**Key Functions**:
- `ProbeTCPPorts(ip, ports, timeout) []int`: Test open TCP ports
- `PingHost(ip, timeout) bool`: ICMP echo request

**Printer Ports**:
- `9100`: HP JetDirect (raw TCP printing)
- `631`: IPP/IPPS (Internet Printing Protocol)
- `515`: LPD (Line Printer Daemon)
- `80/443`: HTTP/HTTPS (web interface)

**Probing Strategy**:
1. Try TCP connect to printer ports (fast)
2. If any printer port open → likely printer
3. Proceed to SNMP enrichment
4. If no printer ports → skip SNMP (save time)

## Parsing and Data Extraction (`parse.go`)

**Purpose**: Parse SNMP PDUs and extract printer information.

**Key Functions**:
- `ParsePrinterInfo(pdus []gosnmp.SnmpPDU) PrinterInfo`
- `ParseSupplyLevels(pdus) []SupplyInfo`
- `ParseCounters(pdus) map[string]int64`

**OID Mapping**:
- `1.3.6.1.2.1.1.5.0` → sysName (hostname)
- `1.3.6.1.2.1.25.3.2.1.3.1` → hrDeviceDescr (model)
- `1.3.6.1.2.1.43.5.1.1.17.1` → prtGeneralSerialNumber
- `1.3.6.1.2.1.43.10.2.1.4.1.1` → prtMarkerLifeCount (page count)

**Helpers** (`helpers.go`):
- `DecodeOctetString(bytes) string`: Handle non-UTF8 SNMP strings
- `CoerceToInt(interface{}) (int64, bool)`: Parse numeric values from hex/decimal

## Merging and Deduplication (`merge.go`)

**Purpose**: Combine data from multiple discovery sources.

**Key Functions**:
- `MergeDiscoveredDevice(existing, new PrinterInfo) PrinterInfo`
- `DeduplicateBySerial(devices) []PrinterInfo`

**Merge Strategy**:
1. Prefer non-empty values (new overwrites empty)
2. Combine discovery methods array
3. Union of open ports
4. Most recent LastSeen timestamp
5. Deduplicate by serial number (handle multiple IPs for same device)

## Diagnostics and Debugging (`diagnostics.go`)

**Purpose**: Generate diagnostic files for troubleshooting.

**Key Functions**:
- `WriteDiagnostics(ip, data, filename)`: Save debug JSON
- `DumpSNMPWalk(ip, pdus)`: Log full SNMP walk

**Diagnostic Files**:
- `logs/parse_debug_<ip>.json`: SNMP parsing details
- `logs/mib_walk_<ip>.json`: Full OID walk results
- `logs/discovered_printers.json`: All discovered devices

**When Generated**:
- On parsing errors (parse_debug)
- On new device discovery (discovered_printers)
- On SNMP walk completion (mib_walk)

## Performance and Concurrency

### Worker Pools

Discovery uses bounded worker pools to control concurrency:

```go
// Liveness probes: 100-200 workers
// SNMP queries: 20-50 workers
// Deep scans: 3-10 workers
```

**Semaphore Pattern**:
```go
sem := make(chan struct{}, maxConcurrency)
for _, ip := range ips {
    sem <- struct{}{}  // Acquire
    go func(ip string) {
        defer func() { <-sem }()  // Release
        // Probe IP
    }(ip)
}
```

### Rate Limiting

**SNMP Query Throttling**:
- Configurable delay between queries (default: 0ms)
- Per-device timeout (default: 2000ms)
- Retry with exponential backoff

**Discovery Throttling**:
- 10-minute minimum between SNMP trap re-discoveries
- 5-minute interval for SSDP M-SEARCH broadcasts

## Configuration

Agent behavior is controlled via:

1. **Environment**: `config.ini` or `config.json`
2. **Database**: Settings stored in SQLite
3. **Runtime**: Passed via function parameters

**Example Configuration**:
```json
{
  "snmp_community": "public",
  "snmp_timeout_ms": 2000,
  "snmp_retries": 1,
  "discover_concurrency": 50,
  "discovery_methods": {
    "arp": true,
    "icmp": true,
    "tcp": true,
    "snmp": true,
    "mdns": true,
    "ssdp": true,
    "wsd": true,
    "traps": false
  }
}
```

## Testing

**Test Files**:
- `parse_test.go`: SNMP parsing logic (15 tests)
- `probe_test.go`: TCP/ICMP probing (8 tests)
- `rangeparser_test.go`: IP range parsing (12 tests)
- `replay_parse_test.go`: Real-world SNMP data replay (5 tests)

**Run Tests**:
```powershell
cd agent
go test ./agent/... -v
```

## Error Handling

**Common Errors**:
- `SNMP timeout`: Device offline or SNMP disabled
- `Permission denied`: Requires admin for ICMP/trap listener
- `Port in use`: Another service using UDP 162/5353
- `Invalid CIDR`: Malformed IP range syntax

**Recovery Strategies**:
- Continue on individual device failures
- Log errors but don't halt discovery
- Retry SNMP queries with backoff
- Graceful degradation (skip unavailable protocols)

## Integration Points

### With Scanner (`agent/scanner/`)
- `QueryDevice()`: SNMP enrichment
- `IsPrinterDevice()`: Confidence scoring
- Vendor detection and parsing

### With Storage (`agent/storage/`)
- `UpsertDevice()`: Save discovered printer
- `GetDevices()`: Load saved devices
- `UpdateMetrics()`: Store periodic metrics

### With Logger (`agent/logger/`)
- Structured logging with levels
- Rate-limited warnings (prevent log spam)
- SSE broadcasting to UI

## Related Documentation

- [Scanner Module](../scanner/README.md) - SNMP querying and vendor profiles
- [Logger Module](../logger/README.md) - Logging system
- [Live Discovery TODO](../../docs/LIVE_DISCOVERY_TODO.md) - Discovery method status
- [API Reference](../../docs/API_REFERENCE.md) - HTTP endpoints
- [Settings TODO](../../docs/SETTINGS_TODO.md) - Future features
