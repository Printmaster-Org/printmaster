# Features Guide

Complete guide to PrintMaster's features and capabilities.

## Table of Contents

- [Device Discovery](#device-discovery)
- [Device Monitoring](#device-monitoring)
- [Multi-Site Management](#multi-site-management)
- [WebSocket Proxy](#websocket-proxy)
- [Alerts & Notifications](#alerts--notifications)
- [Scheduled Scans](#scheduled-scans)
- [Auto-Updates](#auto-updates)
- [Security Features](#security-features)
- [API Access](#api-access)

---

## Device Discovery

PrintMaster automatically discovers printers and copiers on your network using SNMP.

### How Discovery Works

1. **Port Scanning**: Quick TCP scan to find devices with printer ports open (80, 443, 9100)
2. **SNMP Detection**: Query each candidate to confirm it's a printer
3. **Deep Scan**: Collect detailed device information via SNMP

### Discovery Methods

| Method | Description | When Used |
|--------|-------------|-----------|
| **IP Range Scan** | Scan a specified range of IP addresses | Manual configuration |
| **Subnet Auto-Scan** | Automatically scan local subnets | Default behavior |
| **Single Device** | Add a specific printer by IP | Known devices |

### Supported Devices

PrintMaster supports most SNMP-enabled printers and copiers, including:

- **HP** - LaserJet, OfficeJet, PageWide, DesignJet
- **Canon** - imageRUNNER, imageCLASS
- **Epson** - WorkForce, EcoTank
- **Brother** - HL, MFC, DCP series
- **Lexmark** - All network models
- **Xerox** - VersaLink, AltaLink, WorkCentre
- **Ricoh** - IM, MP, SP series
- **Konica Minolta** - bizhub series
- **Kyocera** - ECOSYS, TASKalfa
- **Sharp** - MX series
- **Toshiba** - e-STUDIO series

### Adding Custom IP Ranges

1. Go to **Devices** → **IP Ranges**
2. Click **Add Range**
3. Enter the range in any supported format:
   - Single: `192.168.1.100`
   - Range: `192.168.1.1-254`
   - CIDR: `192.168.1.0/24`
4. Optionally set a label (e.g., "Main Office")
5. Click **Save**

### Discovery Settings

Fine-tune discovery behavior in **Settings** → **Discovery**:

| Setting | Default | Description |
|---------|---------|-------------|
| **Concurrent Scans** | 50 | Simultaneous SNMP queries |
| **SNMP Community** | public | SNMP v1/v2c community string |
| **SNMP Timeout** | 2000ms | Query timeout per device |
| **SNMP Retries** | 1 | Retry attempts for failed queries |

---

## Device Monitoring

Open **Details** on an agent device card to inspect identity, asset information,
and the metrics overview. Capabilities appear beneath the device name; editable
values have a labeled edit button beside them. Device information uses two columns
on desktop and stacks on narrow screens. All screenshots in this guide use
synthetic demo data.

![Device identity, asset tag, location, and metrics overview](screenshots/Device%20Details%20-%20PrintMaster%20Agent.png)

### Collected Data

For each discovered device, PrintMaster collects:

#### Device Identity
- Model name and manufacturer
- Serial number
- Asset tag (if configured)
- Firmware version
- MAC address

#### Page Counters
- Total page count
- Black & white pages
- Color pages (if applicable)
- Duplex pages
- Large format pages

#### Supply Levels
- Toner/ink levels (percentage)
- Drum/imaging unit life
- Fuser life
- Waste toner capacity

#### Status Information
- Online/offline status
- Current errors and alerts
- Paper tray status
- Active jobs

### Historical Data

PrintMaster stores historical metrics using a tiered retention system:

| Tier | Resolution | Retention |
|------|------------|-----------|
| Raw | As collected | 7 days |
| Hourly | 1 hour average | 30 days |
| Daily | 1 day average | 1 year |
| Monthly | 1 month average | Forever |

This allows you to track usage trends while keeping database size manageable.

The server's **Metrics** tab brings fleet throughput and consumables history
together with selectable time ranges.

![Fleet throughput and consumables history charts](screenshots/Metrics%20-%20PrintMaster%20Server.png)

### Device Groups

Organize devices into groups for easier management:

1. Go to **Devices** → **Groups**
2. Create a new group (e.g., "Finance Department")
3. Add devices to the group
4. View group-level statistics and reports

---

## Multi-Site Management

The PrintMaster Server enables centralized management of multiple agents across different locations.

### Architecture

```
                    ┌──────────────┐
         ┌────────▶│    Server    │◀────────┐
         │         │   (Central)  │         │
         │         └──────────────┘         │
         │                ▲                 │
         │                │                 │
    ┌────┴────┐     ┌────┴────┐      ┌────┴────┐
    │  Agent  │     │  Agent  │      │  Agent  │
    │ Site A  │     │ Site B  │      │ Site C  │
    └─────────┘     └─────────┘      └─────────┘
```

### Agent Features

Each agent:
- Runs independently at its site
- Maintains its own local database
- Uploads data to the server periodically
- Continues working if server connectivity is lost
- Syncs automatically when connection is restored

### Server Dashboard

The server provides:
- **Fleet Overview**: Aggregate statistics across all sites
- **Agent Status**: Real-time health monitoring of all agents
- **Combined Device List**: All devices from all sites in one view
- **Cross-Site Reports**: Compare usage across locations
- **Centralized Alerts**: Single pane for all site alerts

![Expanded tenant, site, agent, and device hierarchy](screenshots/Dashboard%20-%20PrintMaster%20Server.png)

### Agent Naming

Give each agent a meaningful name for easy identification:

```toml
[server]
agent_name = "NYC Office"
```

Or set via the web UI: **Settings** → **Server Connection** → **Agent Name**

### Upload Frequency

Control how often agents send data to the server:

| Setting | Default | Description |
|---------|---------|-------------|
| Upload Interval | 5 minutes | Full data sync frequency |
| Heartbeat Interval | 60 seconds | Status ping frequency |

---

## WebSocket Proxy

Access agent web UIs and printer admin pages remotely through the server.

### How It Works

The server creates secure WebSocket tunnels to agents, allowing you to:
- Access agent UIs without direct network access
- Open printer web interfaces from anywhere
- Manage devices behind NAT/firewalls

### Using the Proxy

1. Open the server dashboard
2. Go to **Agents**
3. Click **Open UI** on any agent card
4. The agent's web interface opens in a new tab

For device access:
1. Go to **Devices**
2. Find the device you want to access
3. Click **Open Web Interface**
4. The printer's admin page opens through the proxy

### Benefits

- **No port forwarding required**: Works through existing connections
- **Secure**: Traffic encrypted through WebSocket tunnel
- **Firewall-friendly**: Uses the same connection agent established

---

## Alerts & Notifications

The **Alerts** tab summarizes fleet health, alert categories, and recent events.

![Fleet alert summary and recent events](screenshots/Alerts%20-%20PrintMaster%20Server.png)

Get notified about important events and issues.

### Alert Types

| Alert | Description |
|-------|-------------|
| **Device Offline** | Printer not responding to SNMP |
| **Low Toner** | Toner/ink below threshold |
| **Error State** | Printer reporting an error |
| **Paper Out** | Paper tray empty |
| **Agent Disconnected** | Server lost contact with agent |

### Configuring Alerts

1. Go to **Settings** → **Alerts**
2. Enable/disable specific alert types
3. Set thresholds (e.g., low toner at 10%)
4. Configure notification methods

### Notification Methods

- **Email**: Send alerts via email
- **Webhook**: POST alerts to a URL (integrations)
- **Dashboard**: Display in web UI

### Alert Thresholds

| Supply | Default Threshold |
|--------|------------------|
| Toner/Ink | 10% |
| Drum | 5% |
| Fuser | 5% |
| Waste Toner | 95% full |

---

## Scheduled Scans

Automate device discovery and data collection.

### Creating a Schedule

1. Go to **Settings** → **Schedules**
2. Click **New Schedule**
3. Configure:
   - **Name**: Descriptive label
   - **Frequency**: Hourly, daily, weekly
   - **Time**: When to run
   - **IP Ranges**: Which ranges to scan
4. Click **Save**

### Schedule Examples

| Use Case | Configuration |
|----------|---------------|
| Continuous monitoring | Every 15 minutes, all ranges |
| Daily inventory | Daily at 6 AM, all ranges |
| Low-traffic scanning | Hourly during business hours |

### Manual Triggers

Run any schedule immediately:
1. Go to **Schedules**
2. Click **Run Now** on the desired schedule

---

## Auto-Updates

Keep agents automatically updated to the latest version.

### Update Modes

| Mode | Description |
|------|-------------|
| **Inherit** | Follow fleet policy from server (default) |
| **Local** | Use agent's local update settings |
| **Disabled** | No automatic updates |

### Fleet Policies (Server)

Administrators can set update policies for all agents:

1. Go to server **Settings** → **Update Policies**
2. Configure:
   - **Version Strategy**: Minor only, or allow major upgrades
   - **Maintenance Window**: When updates can occur
   - **Rollout Control**: Staged rollout settings

### Local Agent Override

Agents can override fleet policy if needed:

```toml
[auto_update]
mode = "local"

[auto_update.local_policy]
update_check_days = 7
version_pin_strategy = "minor"
allow_major_upgrade = false
```

### Maintenance Windows

Prevent updates during critical hours:

```toml
[auto_update.local_policy.maintenance_window]
enabled = true
timezone = "America/New_York"
start_hour = 2
end_hour = 5
days_of_week = ["Saturday", "Sunday"]
```

---

## Security Features

### Authentication

| Component | Authentication |
|-----------|---------------|
| **Server** | Username/password login, session-based |
| **Agent** | Optional, configurable auth modes |
| **API** | Token-based authentication |

### Agent Auth Modes

| Mode | Description |
|------|-------------|
| `local` | No login required; admin tasks require local access |
| `server` | Defers auth to central server |
| `disabled` | No authentication (not recommended) |

### TLS/HTTPS

Enable encrypted connections:

**Server:**
```bash
docker run -d \
  -e USE_HTTPS=true \
  -e HTTPS_PORT=9443 \
  -v /path/to/certs:/certs \
  ghcr.io/printmaster-org/printmaster-server:latest
```

**Agent:**
```toml
[web]
enable_tls = true
https_port = 8443
```

### Reverse Proxy Support

Both components work behind reverse proxies:
- Enable `BEHIND_PROXY=true` for proper header handling
- Configure WebSocket passthrough for real-time features
- Handle SSL termination at the proxy level

---

## API Access

PrintMaster provides a REST API for integrations and automation.

### Authentication

```bash
# Get auth token
curl -X POST http://server:9090/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "your-password"}'
```

### Common Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/devices` | GET | List all devices |
| `/api/v1/devices/{id}` | GET | Get device details |
| `/api/v1/agents` | GET | List connected agents |
| `/api/v1/agents/{id}/devices` | GET | Get devices for an agent |

### Webhooks

Configure webhooks to receive real-time notifications:

1. Go to **Settings** → **Integrations**
2. Add a webhook URL
3. Select events to receive
4. Test the webhook

### Integration Examples

- **Monitoring Systems**: Send alerts to PagerDuty, OpsGenie
- **Ticketing**: Create tickets in ConnectWise, Autotask
- **Reporting**: Export data to BI tools
- **Custom Dashboards**: Build your own UI

See the [API Reference](dev/API.md) for complete documentation.
