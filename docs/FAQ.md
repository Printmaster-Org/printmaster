# Frequently Asked Questions

Common questions about PrintMaster.

## General

### What is PrintMaster?

PrintMaster is a cross-platform printer/copier fleet management system. It automatically discovers network printers, collects device information (model, serial number, page counts, toner levels), and provides a web interface for monitoring your print fleet.

### Who is PrintMaster for?

- **Managed Service Providers (MSPs)** - Monitor client print infrastructure
- **Managed Print Services (MPS) providers** - Track usage and supplies
- **Copier dealers** - Manage devices across customer sites
- **IT departments** - Monitor corporate print fleets

### Is PrintMaster free?

Yes, PrintMaster is open source and free to use under the MIT license.

### What printers does PrintMaster support?

PrintMaster works with any SNMP-enabled network printer or copier. Support levels vary by manufacturer:

#### Enhanced Support (Vendor-Specific Modules)

These brands have dedicated modules with optimized OID queries and accurate metric parsing:

| Manufacturer | Page Counters | Supplies | Special Features |
|--------------|:-------------:|:--------:|------------------|
| **HP** | ✅ | ✅ | Color/mono breakdown, copy/fax/scan counters |
| **Epson** | ✅ | ✅ | Remote-mode ink levels, ST2 status parsing |
| **Kyocera** | ✅ | ✅ | Enterprise OID detection, drum counters |

#### Standard Support (Generic SNMP)

These brands work via standard Printer-MIB queries. Basic metrics are collected but some vendor-specific features may be unavailable:

| Manufacturer | Page Counters | Supplies | Notes |
|--------------|:-------------:|:--------:|-------|
| Canon | ✅ | ⚠️ | Supply levels may vary by model |
| Brother | ✅ | ⚠️ | Basic toner levels |
| Lexmark | ✅ | ⚠️ | Standard MIB support |
| Xerox | ✅ | ⚠️ | VersaLink/AltaLink tested |
| Ricoh | ✅ | ⚠️ | Basic counters only |
| Konica Minolta | ✅ | ⚠️ | bizhub series |
| Sharp | ✅ | ⚠️ | MX series |
| Toshiba | ✅ | ⚠️ | e-STUDIO series |
| Samsung | ✅ | ⚠️ | Legacy models |

**Legend:** ✅ Full support | ⚠️ Basic/partial support | ❌ Not supported

> **Want better support for your brand?** We're actively adding vendor modules. Check [GitHub Issues](https://github.com/printmaster-org/printmaster/issues) or contribute a vendor profile!

---

## Deployment

### Do I need both the agent and server?

No. You can run the agent standalone if you only need to monitor printers at a single site. The server is only needed for:
- Managing multiple sites from one dashboard
- Remote access to agents via WebSocket proxy
- Centralized reporting across all locations

### Can I run multiple agents?

Yes. Deploy one agent per site/network, and connect them all to a central server for unified management.

### What are the system requirements?

**Agent:**
- 1 CPU core, 256MB RAM minimum
- 100MB disk space + database growth
- Network access to printers (SNMP UDP 161)

**Server:**
- 1-2 CPU cores, 512MB RAM minimum
- 500MB disk space + database growth
- More resources for larger deployments

### Can I run PrintMaster in Docker?

Yes. Docker is the recommended deployment method for the server. See the [Installation Guide](INSTALL.md#docker-recommended).

---

## Features

### What data does PrintMaster collect?

For each printer:
- Model name and manufacturer
- Serial number
- IP and MAC addresses
- Page counters (total, color, B&W)
- Toner/ink levels
- Drum and fuser life
- Error status

### How often does PrintMaster scan for printers?

You control the scan frequency. Options include:
- Manual scans on-demand
- Scheduled scans (hourly, daily, weekly)
- Automatic scans after IP range changes

Device metrics (counters, toner) are typically collected every 15-60 minutes depending on your configuration.

### Can PrintMaster send alerts?

Yes. PrintMaster can alert you when:
- A device goes offline
- Toner/ink falls below a threshold
- A device reports an error
- An agent disconnects from the server

### Does PrintMaster support SNMP v3?

Currently, PrintMaster supports SNMP v1/v2c. SNMP v3 support is planned for a future release.

### Can I access printers remotely?

Yes. The WebSocket proxy feature allows you to access printer admin pages and agent UIs through the central server, even if the devices are behind NAT or firewalls.

---

## Security

### Is my data secure?

PrintMaster stores data locally on the agent and server. Data is not sent to any external services unless you configure integrations.

For secure deployments:
- Enable TLS/HTTPS for encrypted connections
- Use a reverse proxy with SSL termination
- Configure authentication for web UIs
- Restrict network access to management ports

### Can I use my own SSL certificates?

Yes. Both the agent and server support custom TLS certificates. You can also use a reverse proxy (Nginx, Traefik) to handle SSL termination.

### Is there user authentication?

Yes. The server has built-in user authentication with username/password login. The agent supports multiple authentication modes including server-delegated auth.

---

## Troubleshooting

### Why aren't my printers being discovered?

Common causes:
1. SNMP is disabled on the printer
2. Wrong SNMP community string (default is "public")
3. Firewall blocking SNMP (UDP port 161)
4. Incorrect IP range configuration

See the [Troubleshooting Guide](TROUBLESHOOTING.md#discovery-issues) for detailed solutions.

### Why can't my agent connect to the server?

Check:
1. Server URL includes protocol and port (`http://server:9090`)
2. Firewall allows the connection
3. Server is running and accessible

See [Connection Issues](TROUBLESHOOTING.md#connection-issues) for more help.

### Where are the logs?

| Platform | Location |
|----------|----------|
| Windows | Event Viewer → Application |
| Linux | `journalctl -u printmaster-agent` |
| Docker | `docker logs container-name` |

---

## Updates

### How do I update PrintMaster?

**Docker:**
```bash
docker pull ghcr.io/printmaster-org/printmaster-server:latest
docker compose down && docker compose up -d
```

**Linux (APT):**
```bash
sudo apt update && sudo apt upgrade printmaster-agent
```

**Windows:**
Run the new MSI installer.

### Does PrintMaster auto-update?

Agents can be configured to auto-update. See [Auto-Updates](FEATURES.md#auto-updates) for configuration options.

---

## Integration

### Does PrintMaster have an API?

Yes. PrintMaster provides a REST API for:
- Querying device information
- Managing agents
- Configuring settings
- Triggering scans

See the [API Reference](dev/API.md) for documentation.

### Can I integrate with my PSA/RMM tool?

Yes, via:
- REST API for custom integrations
- Webhooks for real-time event notifications
- Export data for import into other systems

### Can I export data to a spreadsheet?

The web UI supports CSV export for device lists and reports.

---

## Contributing

### How can I contribute?

We welcome contributions! See [CONTRIBUTING.md](../CONTRIBUTING.md) for guidelines on:
- Reporting bugs
- Suggesting features
- Submitting pull requests

### Where do I report bugs?

Create an issue on [GitHub Issues](https://github.com/printmaster-org/printmaster/issues) with:
- Steps to reproduce
- Expected vs actual behavior
- Version information
- Relevant logs

### Where can I get help?

- Documentation: You're reading it!
- Discussions: [GitHub Discussions](https://github.com/printmaster-org/printmaster/discussions)
- Issues: [GitHub Issues](https://github.com/printmaster-org/printmaster/issues)
