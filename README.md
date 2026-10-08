<div align="center">
  <img src="docs/images/banner.png" alt="Bridge Monitor" width="900">

[![CI](https://github.com/printmaster-org/printmaster/actions/workflows/ci.yml/badge.svg)](https://github.com/printmaster-org/printmaster/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/printmaster-org/printmaster)](https://github.com/printmaster-org/printmaster/releases)
[![Docker](https://ghcr-badge.egpl.dev/printmaster-org/printmaster-server/latest_tag?trim=major&label=latest)](https://github.com/printmaster-org/printmaster/pkgs/container/printmaster-server)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

PrintMaster automatically discovers and monitors network printers and copiers. Built for MSPs, MPS providers, copier dealers, and IT departments managing print fleets.

## Why PrintMaster?

PrintMaster was born from real-world frustration managing multi-vendor print fleets. Existing solutions meant juggling multiple tools—PrintAudit for metering, Epson Remote Services for Epson devices, Kyocera Net Manager for Kyocera, Epson Device Admin for local management—each with their own quirks, agents that randomly disconnect, and per-device licensing fees.

The goal: combine the best of fleet monitoring and vendor-specific tools into one open-source solution that actually tells you when something goes wrong.

### Transparency

This project uses AI-assisted development. Initial development relied significantly on AI tools, and this is disclosed in the spirit of transparency. As the project matures and gains users, development will slow down to focus on stability and correctness over feature velocity.

The maintainer is not a professional developer by trade, but has real-world experience in the copier/MPS industry and has contributed to other open-source projects (including OIDC SSO support for MeshCentral).

Contributions, feedback, and vendor-specific SNMP knowledge are welcome—printer MIBs are complex and vendor quirks are endless.

## Features

- **Automated Discovery** — SNMP-based network scanning finds printers automatically
- **Fleet Monitoring** — Track page counts, toner levels, and device status
- **Multi-Site Support** — Central server aggregates data from distributed agents
- **Remote Access** — WebSocket proxy to access agent UIs and printer admin pages
- **Real-Time Updates** — WebSocket heartbeat with automatic HTTP fallback
- **Cross-Platform** — Windows, Linux, macOS, Docker

## Screenshots

Captured from the current development UI on October 8, 2026, using a synthetic
eight-printer fleet. Device names, counters, supplies, and alerts are demo data.

<details>
<summary><b>Dashboard</b> — Hierarchical view of tenants, sites, agents, and devices</summary>

![Dashboard](docs/screenshots/Dashboard%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>Fleet Agents</b> — Monitor agent connectivity, versions, and status</summary>

![Agents](docs/screenshots/Agents%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>Fleet Devices</b> — Track printers, consumables, and health status</summary>

![Devices](docs/screenshots/Devices%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>Fleet Metrics</b> — Netdata-style time-series charts for throughput and usage</summary>

![Metrics](docs/screenshots/Metrics%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>System Logs</b> — Real-time log streaming with search and filtering</summary>

![Logs](docs/screenshots/Logs%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>Fleet Alerts</b> — Fleet health, alert categories, and recent events</summary>

![Fleet health and recent alerts](docs/screenshots/Alerts%20-%20PrintMaster%20Server.png)
</details>

<details>
<summary><b>Agent Devices</b> — Local printer cards with counters and consumables</summary>

![Agent device cards and consumable levels](docs/screenshots/Devices%20-%20PrintMaster%20Agent.png)
</details>

<details>
<summary><b>Device Details</b> — Identity, location, asset tags, and usage metrics</summary>

![Agent device details and metrics overview](docs/screenshots/Device%20Details%20-%20PrintMaster%20Agent.png)
</details>

## Quick Start

### Server (Docker)

```bash
docker run -d \
  --name printmaster-server \
  -p 9090:9090 \
  -v printmaster-data:/var/lib/printmaster/server \
  -e ADMIN_PASSWORD=your-password \
  ghcr.io/printmaster-org/printmaster-server:latest
```

Access at `http://localhost:9090` — Login: `admin` / your password

### Agent (Windows)

Download the MSI from [Releases](https://github.com/printmaster-org/printmaster/releases), or:

```powershell
# Install as service
.\printmaster-agent.exe --service install
.\printmaster-agent.exe --service start
```

Access at `http://localhost:8080`

### Agent (Linux)

```bash
# Debian/Ubuntu
curl -fsSL https://packages.printmaster.work/install.sh | sudo bash
```

## Architecture

```
┌─────────────┐         ┌─────────────┐         ┌─────────────┐
│   Agent     │────────▶│   Server    │◀────────│   Agent     │
│   Site A    │  API    │  (Central)  │  API    │   Site B    │
└─────────────┘         └─────────────┘         └─────────────┘
      ↓                       ↓                       ↓
  Printers              Web Dashboard            Printers
```

**Agent**: Runs at each site, discovers printers via SNMP, stores data locally, optionally reports to server. Can run standalone.

**Server**: Central hub for multi-site management. Aggregates device data, provides fleet dashboard, enables remote access via WebSocket proxy.

## Documentation

| Guide | Description |
| ------- | ------------- |
| [Installation](docs/INSTALL.md) | Complete setup instructions for all platforms |
| [Getting Started](docs/GETTING_STARTED.md) | First steps after installation |
| [Features](docs/FEATURES.md) | Detailed feature documentation |
| [Configuration](docs/CONFIGURATION.md) | All configuration options |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | Common issues and solutions |
| [FAQ](docs/FAQ.md) | Frequently asked questions |

Developer documentation is in [docs/dev/](docs/dev/).

## Configuration

**Connect agent to server** — edit `config.toml`:

```toml
[server]
enabled = true
url = "http://your-server:9090"
agent_name = "Office A"
```

**Customize SNMP settings**:

```toml
[snmp]
community = "public"
timeout_ms = 2000
retries = 1
```

See [Configuration Guide](docs/CONFIGURATION.md) for all options.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Submit a pull request

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License — see [LICENSE](LICENSE)

## Links

- [Releases](https://github.com/printmaster-org/printmaster/releases)
- [Issues](https://github.com/printmaster-org/printmaster/issues)
- [Discussions](https://github.com/printmaster-org/printmaster/discussions)
