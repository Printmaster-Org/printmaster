# PrintMaster Agent

**Cross-platform printer discovery and monitoring agent**

The PrintMaster Agent is a standalone Go application that discovers printers on local networks, collects metrics via SNMP, and provides a web UI for management. It runs on Windows, macOS, and Linux without external dependencies.

## Quick Start

### Build & Run

```powershell
# Build (development - with debug info)
.\build.ps1 agent

# Build (production - optimized & stripped)
.\build.ps1 release

# Build with verbose output
.\build.ps1 release -VerboseBuild

# Check build log
Get-Content logs\build.log -Tail 50

# Run
cd agent
.\printmaster-agent.exe -port 8080
```

**Web UI**: http://localhost:8080

**Build Targets**:
- `agent` - Development build with debug symbols (default, version: x.y.z-dev)
- `release` - Production build: optimized, stripped (~30% smaller)
- `bump` - Release build with auto-increment version (1.0.0 → 1.0.1)
- `test` - Run storage tests
- `test-all` - Run all tests
- `clean` - Remove build artifacts

**Build Flags**:
- `-VerboseBuild` - Show detailed compilation output
- `-IncrementVersion` - Auto-increment patch version (can combine with `release`)

**Version Management**:
```powershell
# Check current version
.\version.ps1

# Build with current version
.\build.ps1 release

# Build and increment version
.\build.ps1 bump
# OR
.\build.ps1 release -IncrementVersion

# Check version in running agent
curl http://localhost:8080/api/version
```

**Build Logs**: All build output is saved to `logs\build.log` with rotation

### Basic Usage

1. **Configure IP Ranges**: Settings → Network → IP Ranges (e.g., `192.168.1.0/24`)
2. **Start Discovery**: Click "Scan Network" button
3. **View Results**: Devices appear in "Discovered" tab
4. **Save Devices**: Click "Save" to move to "Saved" tab
5. **Monitor**: View metrics, page counts, supply levels

## Architecture Overview

```
agent/
├── main.go                # HTTP server, API endpoints, embedded UI
├── agent/                 # Discovery protocols & device detection
├── scanner/               # SNMP querying & vendor profiles
├── logger/                # Structured logging with SSE streaming
├── storage/               # SQLite persistence layer
├── util/                  # Shared utilities
└── tools/                 # Development tools
```

### Module Summary

#### [Agent](agent/README.md) - Discovery & Detection
- **mDNS/Bonjour**: Passive discovery of IPP/AirPrint printers
- **SSDP/UPnP**: Universal Plug and Play device discovery
- **WS-Discovery**: Windows/enterprise printer discovery
- **SNMP Traps**: Event-driven discovery (printers send notifications)
- **LLMNR**: Link-local name resolution (Windows networks)
- **ARP Table**: Extract known devices from OS cache
- **Active Scanning**: TCP port probes + ICMP ping
- **IP Enumeration**: CIDR range parsing and subnet scanning

#### [Scanner](scanner/README.md) - SNMP Queries & Parsing
- **Multi-Stage Pipeline**: Liveness → Detection → Deep Scan
- **Vendor Profiles**: HP, Canon, Brother, Epson, Kyocera, Lexmark, Ricoh, Samsung, Xerox
- **Device Detection**: Confidence scoring (is this a printer?)
- **SNMP Wrapper**: Query/Walk/BulkWalk with retries
- **Metrics Extraction**: Page counts, supply levels, status messages
- **OID Resolution**: Vendor-specific + standard Printer-MIB

#### [Logger](logger/README.md) - Structured Logging
- **Log Levels**: ERROR, WARN, INFO, DEBUG, TRACE
- **SSE Streaming**: Real-time logs to web UI (no polling)
- **Rate Limiting**: Prevent log spam from repetitive errors
- **Ring Buffer**: Last 1000 log entries in memory
- **Structured Context**: Key-value pairs for rich logging
- **Thread-Safe**: Concurrent logging from multiple goroutines

#### [Storage](storage/README.md) - SQLite Persistence
- **Device CRUD**: Create, Read, Update, Delete, Upsert
- **Scan History**: Track device changes over time
- **Metrics History**: Time-series page counts and supply levels
- **Field Locking**: Protect manually-edited fields from auto-update
- **Configuration**: Store settings (IP ranges, SNMP community, etc.)
- **Migrations**: Automatic schema upgrades

## Key Features

### Discovery Methods

| Method | Type | Platform | Best For |
|--------|------|----------|----------|
| mDNS | Passive | All | macOS, modern printers |
| SSDP | Passive | All | Consumer devices |
| WS-Discovery | Passive | All | Windows, enterprise |
| SNMP Traps | Passive | All | Event-driven monitoring |
| LLMNR | Passive | Windows | Workgroup networks |
| Active Scan | Active | All | Complete coverage |

### SNMP Capabilities

- **Protocols**: SNMPv1, SNMPv2c (SNMPv3 planned)
- **Community**: Configurable (default: "public")
- **Timeout**: Configurable (default: 2000ms)
- **Retries**: Configurable (default: 1)
- **Concurrency**: Configurable worker pools (default: 50)
- **Vendor Detection**: Automatic via sysObjectID enterprise OID

### Metrics Collected

**Device Info**:
- Manufacturer, model, serial number
- Hostname, IP, MAC address
- Firmware version
- Network config (subnet, gateway, DNS)

**Counters**:
- Total pages printed
- Color pages printed (vendor-specific)
- Duplex pages, fax pages, scan pages (HP)
- Drum life, maintenance counters (various vendors)

**Supplies**:
- Toner/ink levels (percentage)
- Supply names (Black Toner, Cyan Ink, etc.)
- Supply status (OK, Low, Empty)

**Status**:
- Device status (idle, printing, error)
- Status messages (paper jam, cover open, etc.)
- Error codes and warnings

## Configuration

### Config File (`config.json`)

```json
{
  "port": 8080,
  "snmp_community": "public",
  "snmp_timeout_ms": 2000,
  "snmp_retries": 1,
  "discover_concurrency": 50,
  "ip_ranges": [
    "192.168.1.0/24",
    "10.0.0.0/24"
  ],
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

### Database Settings

Settings configured via Web UI are stored in SQLite and override `config.json`.

**Location**: 
- Windows: `%APPDATA%\printmaster\devices.db`
- Linux: `~/.local/share/printmaster/devices.db`
- macOS: `~/Library/Application Support/printmaster/devices.db`

## API Endpoints

### Devices

- `GET /api/devices` - List all devices
- `GET /api/devices/{serial}` - Get single device
- `DELETE /api/devices/{serial}` - Delete device
- `POST /api/devices/{serial}/save` - Mark device as saved
- `POST /api/devices/save-all` - Save all discovered devices
- `POST /api/devices/{serial}/refresh` - Re-scan single device

### Discovery

- `POST /api/discover` - Start network scan
- `POST /api/live-discovery/start` - Enable passive discovery
- `POST /api/live-discovery/stop` - Disable passive discovery
- `GET /api/live-discovery/status` - Check discovery status

### Settings

- `GET /api/settings` - Get all settings
- `POST /api/settings` - Update settings
- `GET /api/ranges` - Get IP ranges
- `POST /api/ranges` - Update IP ranges

### Monitoring

- `GET /sse` - Server-Sent Events stream (logs, metrics)
- `GET /api/scan-history/{serial}` - Device scan history
- `GET /api/metrics-history/{serial}` - Metrics time-series

**Full API Documentation**: [docs/API_REFERENCE.md](../docs/API_REFERENCE.md)

## Development

### Prerequisites

- Go 1.27+ (uses modern Go features)
- No CGO required (pure Go SQLite driver)
- Cross-platform (Windows, macOS, Linux)

### Build

```powershell
# Standard build
go build -o printmaster-agent.exe

# Or use build script
.\build.ps1 agent

# Build for specific platform
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o printmaster-agent-linux
```

### Run Tests

```powershell
# All tests
go test ./... -v

# Specific package
go test ./agent/... -v
go test ./scanner/... -v
go test ./logger/... -v
go test ./storage/... -v

# With coverage
go test ./... -cover
```

### Project Structure

```
agent/
├── main.go                 # HTTP server, startup, and runtime settings
├── scanner_runtime.go      # Sole production coordinator owner and backend
├── scanner_api.go          # Range, manual, and metrics scanner entry points
├── discover.go             # /discover HTTP adapter to quick coordinator discovery
├── agent/                  # Protocol observations, identity extraction, and parsing
├── scanner/                # Typed planner, bounded coordinator, and SNMP queries
│   ├── work.go             # Observations, intents, provenance, and outcomes
│   ├── coordinator.go      # Bounded per-IP work execution
│   ├── query.go, snmp.go   # Shared query profiles and SNMP transport
│   ├── capabilities/      # Device capability detection
│   └── vendor/             # Vendor OID modules
├── storage/                # SQLite inventory, staged scanner commits, metrics
├── spooler/                # USB and local printer integration
├── proxy/                  # Device web UI proxy
├── autoupdate/             # Update orchestration
└── tools/                  # Development utilities
```

## Deployment

### Standalone Executable

```powershell
# Build release binary
go build -ldflags="-s -w" -o printmaster-agent.exe

# Run on server
.\printmaster-agent.exe -port 8080
```

### Windows Service

```powershell
# Install as service (requires NSSM or similar)
nssm install PrintMasterAgent "C:\path\to\printmaster-agent.exe"
nssm set PrintMasterAgent AppParameters "-port 8080"
nssm start PrintMasterAgent
```

### Linux Systemd

```bash
# Create service file: /etc/systemd/system/printmaster.service
[Unit]
Description=PrintMaster Agent
After=network.target

[Service]
Type=simple
User=printmaster
ExecStart=/usr/local/bin/printmaster-agent -port 8080
Restart=on-failure

[Install]
WantedBy=multi-user.target

# Enable and start
sudo systemctl enable printmaster
sudo systemctl start printmaster
```

### Docker (Future)

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /build
COPY . .
RUN go build -o printmaster-agent

FROM alpine:latest
COPY --from=builder /build/printmaster-agent /usr/local/bin/
EXPOSE 8080
CMD ["printmaster-agent", "-port", "8080"]
```

## Performance

### Resource Usage

| Metric | Idle | Scanning (50 workers) |
|--------|------|----------------------|
| CPU | <1% | 5-15% |
| Memory | ~50MB | ~150MB |
| Network | Minimal | 1-5 Mbps |
| Disk I/O | Minimal | SQLite writes |

### Scan Performance

| Network Size | Time | Notes |
|--------------|------|-------|
| /24 (254 IPs) | 10-30s | With 50 workers |
| /22 (1024 IPs) | 60-120s | Adjust concurrency |
| /16 (65536 IPs) | Hours | Not recommended |

**Optimization Tips**:
- Increase concurrency for faster scans (risk: network saturation)
- Reduce SNMP timeout for faster failures (risk: miss slow devices)
- Use ARP-based discovery to pre-filter live IPs
- Enable only needed discovery methods (disable traps if not configured)

## Troubleshooting

### No Devices Found

1. **Check network connectivity**: Ensure agent can reach printers
2. **Verify SNMP enabled**: Test with `snmpwalk -v2c -c public <ip> .1.3.6`
3. **Check firewall**: Allow outbound UDP 161, TCP 9100/631
4. **Review logs**: Look for timeout or permission errors
5. **Try different discovery methods**: Some printers only respond to certain protocols

### Permission Errors

- **SNMP Traps (UDP 162)**: Requires admin/root (privileged port)
- **ICMP Ping**: Requires raw sockets (admin/root) or use system `ping`
- **Low Port Binding (<1024)**: Run as admin/root or use higher port

### Slow Scans

1. **Reduce IP range**: Scan smaller subnets
2. **Increase timeout**: Some devices respond slowly
3. **Adjust concurrency**: Balance speed vs network load
4. **Check network**: Congestion, packet loss, or slow switches

### Missing Data

1. **Incomplete SNMP support**: Not all devices implement all OIDs
2. **Vendor detection failed**: Check if vendor profile exists
3. **SNMP community mismatch**: Verify community string
4. **Field locked**: Check if field is locked from manual edit

## Security Considerations

### SNMP Community Strings

- Default "public" is world-readable (low security)
- Use unique community strings in production
- SNMPv3 recommended (planned feature) for encryption

### Network Exposure

- Agent listens on the configured HTTP/HTTPS ports (default 8080/8443)
- Web UI authentication is driven by `[web.auth]` (see `config.example.toml`). Use `mode = "server"` to force central logins or keep `mode = "local"` with `allow_local_admin = true` for loopback-only access.
- Restrict access with firewall rules when running in local mode.
- Use the built-in TLS support or terminate behind a reverse proxy (nginx, Caddy).

### Database

- SQLite file contains all device data
- No encryption at rest (use disk encryption)
- File permissions: User-only read/write

## Roadmap

### Short-Term (Planned)
- [ ] SNMPv3 support (authentication + encryption)
- [x] Authentication for web UI
- [x] TLS/HTTPS support
- [ ] Webhook notifications
- [ ] Prometheus metrics export
- [ ] CSV/JSON export

### Medium-Term
- [ ] Docker container
- [ ] Multi-agent support (central server)
- [ ] Device groups and tagging
- [ ] Alert rules (low toner, offline devices)
- [ ] Email notifications
- [ ] Mobile app (view-only)

### Long-Term
- [ ] Machine learning for anomaly detection
- [ ] Print job tracking (requires print server integration)
- [ ] Cost tracking (supply costs, page costs)
- [ ] Vendor-specific features (secure printing, pull print)

**Full Feature Tracking**: [docs/SETTINGS_TODO.md](../docs/SETTINGS_TODO.md)

## License

(Add license information here)

## Support

- **Issues**: GitHub Issues
- **Documentation**: [docs/](../docs/)
- **API Reference**: [docs/API_REFERENCE.md](../docs/API_REFERENCE.md)

## Contributing

1. Fork the repository
2. Create a feature branch
3. Write tests for new features
4. Ensure all tests pass: `go test ./...`
5. Submit pull request

**Coding Standards**:
- Follow Go conventions (`gofmt`, `golint`)
- Write tests for major features
- Document exported functions
- Update README when adding features

## Module Documentation

- **[Agent](agent/README.md)** - Discovery protocols and device detection
- **[Scanner](scanner/README.md)** - SNMP querying and vendor profiles
- **[Logger](logger/README.md)** - Structured logging with SSE
- **[Storage](storage/README.md)** - SQLite persistence layer
