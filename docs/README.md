# PrintMaster Documentation

Cross-platform printer/copier fleet management for MSPs, MPS providers, and IT departments.

---

## Quick Start

| Document | Description |
|----------|-------------|
| [Installation Guide](INSTALL.md) | Install on Windows, Linux, macOS, Docker |
| [Getting Started](GETTING_STARTED.md) | First steps after installation |

---

## User Guides

| Document | Description |
|----------|-------------|
| [Features Guide](FEATURES.md) | All features explained with examples |
| [Configuration](CONFIGURATION.md) | Config files, environment variables, UI settings |
| [Troubleshooting](TROUBLESHOOTING.md) | Common issues and solutions |
| [FAQ](FAQ.md) | Frequently asked questions |

---

## Deployment

| Document | Description |
|----------|-------------|
| [Docker Deployment](deployment/docker.md) | Docker and Docker Compose setup |
| [Database Upgrade](deployment/database-upgrade.md) | PostgreSQL and TimescaleDB upgrade procedure |
| [Unraid Deployment](deployment/unraid.md) | Unraid-specific installation |

---

## API Reference

| Document | Description |
|----------|-------------|
| [API Reference](api/README.md) | REST API for agent and server |

---

## What is PrintMaster?

PrintMaster consists of two components:

### Agent
A lightweight service that runs at each site:
- Discovers printers on your network via SNMP
- Collects page counts, toner levels, device info
- Local web UI for management
- Can run standalone or report to server

### Server  
Central hub for managing multiple agents:
- Aggregates data from all agents
- Fleet view dashboard
- Remote agent access via WebSocket proxy
- Multi-tenant support

### Architecture

```
┌─────────────┐         ┌─────────────┐         ┌─────────────┐
│   Agent     │────────▶│   Server    │◀────────│   Agent     │
│   Site A    │         │  (Central)  │         │   Site B    │
└─────────────┘         └─────────────┘         └─────────────┘
      ↓                       ↓                       ↓
  Printers              Web Dashboard            Printers
```

---

## Getting Help

- [GitHub Issues](https://github.com/printmaster-org/printmaster/issues) — Bug reports
- [GitHub Discussions](https://github.com/printmaster-org/printmaster/discussions) — Questions and ideas

---

## For Developers

See [Developer Documentation](dev/README.md) for:
- Build instructions and project structure
- Internal architecture and design
- Contributing guidelines
