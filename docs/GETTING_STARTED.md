# Getting Started

This guide walks you through your first steps with PrintMaster after installation.

## Table of Contents

- [Overview](#overview)
- [Standalone Agent Setup](#standalone-agent-setup)
- [Server + Agent Setup](#server--agent-setup)
- [Discovering Printers](#discovering-printers)
- [Understanding the Dashboard](#understanding-the-dashboard)
- [Next Steps](#next-steps)

---

## Overview

PrintMaster can be used in two modes:

1. **Standalone Mode**: Run just the agent to monitor printers at a single site
2. **Server Mode**: Run a central server with multiple agents across sites

Choose the setup that matches your needs:

| Use Case | Recommended Setup |
|----------|-------------------|
| Single office, single network | Standalone Agent |
| Multiple sites, centralized management | Server + Agents |
| MSP managing multiple clients | Server + Agents (multi-tenant) |

---

## Standalone Agent Setup

If you're monitoring printers at a single location, the standalone agent is all you need.

### Step 1: Access the Web UI

After installation, open your browser to:
```
http://localhost:8080
```

If installed on another machine, replace `localhost` with that machine's IP address.

### Step 2: Run Your First Discovery

1. Click the **Devices** tab
2. Click **Add IP Range** to add networks to scan
3. Enter the IP range of your network (e.g., `192.168.1.1-254`)
4. Click **Save**
5. Click **Scan Now** to start discovery

### Step 3: View Discovered Devices

After the scan completes, discovered printers will appear in the Devices list showing:
- Model name
- Serial number
- IP address
- Page counts
- Toner/ink levels

The agent offers card and table views of saved devices. These screenshots use
synthetic printers rather than a live customer fleet.

![Agent device cards with consumable levels](screenshots/Devices%20-%20PrintMaster%20Agent.png)

![Agent saved devices in table view](screenshots/Device%20Table%20-%20PrintMaster%20Agent.png)

---

## Server + Agent Setup

For multi-site deployments or centralized management.

### Step 1: Set Up the Server

If you haven't already, deploy the server using Docker:

```bash
docker run -d \
  --name printmaster-server \
  -p 9090:9090 \
  -v printmaster-data:/var/lib/printmaster/server \
  -e ADMIN_PASSWORD=your-secure-password \
  ghcr.io/printmaster-org/printmaster-server:latest
```

### Step 2: Log Into the Server

1. Open `http://your-server-ip:9090`
2. Log in with username `admin` and your password

### Step 3: Connect Agents to the Server

For each agent you want to connect:

**Option A: Via Web UI**
1. Open the agent's web UI (`http://agent-ip:8080`)
2. Go to **Settings** → **Server Connection**
3. Enable server mode
4. Enter the server URL (e.g., `http://your-server-ip:9090`)
5. Click **Save**

**Option B: Via Config File**
Edit `config.toml` on the agent:

```toml
[server]
enabled = true
url = "http://your-server-ip:9090"
agent_name = "Office A"  # Friendly name for this agent
```

Restart the agent after saving.

### Step 4: Verify Connection

1. On the server dashboard, go to **Agents**
2. You should see your connected agent with a green status indicator
3. The agent will begin uploading device data automatically

![Server fleet agents with connection status and version information](screenshots/Agents%20-%20PrintMaster%20Server.png)

---

## Discovering Printers

### Automatic Discovery

PrintMaster uses SNMP (Simple Network Management Protocol) to discover and query printers. Most network printers have SNMP enabled by default.

### Adding IP Ranges

You can specify which networks to scan:

1. **Single IP**: `192.168.1.100`
2. **IP Range**: `192.168.1.1-254`
3. **CIDR Notation**: `192.168.1.0/24`
4. **Multiple Ranges**: Add multiple entries for different subnets

### Supported Formats

| Format | Example | Description |
|--------|---------|-------------|
| Single IP | `10.0.0.50` | Scan one device |
| Range | `10.0.0.1-100` | Scan IPs 1-100 |
| CIDR | `10.0.0.0/24` | Scan entire subnet |
| Wildcard | `10.0.1.*` | Scan 10.0.1.1-254 |

### Discovery Settings

Fine-tune discovery in **Settings** → **Discovery Settings**:

The current Settings page groups automatic discovery, passive discovery, and
metrics monitoring controls. The example below uses demo configuration.

If settings cannot be loaded, Apply and auto-save are paused and **Retry** is available. This
prevents saving displayed defaults or an old snapshot over the real settings.

![Agent discovery settings and metrics monitoring controls](screenshots/Settings%20-%20PrintMaster%20Agent.png)

| Setting | Description |
|---------|-------------|
| **SNMP Community** | Default: `public`. Change if your printers use a different community string |
| **Concurrent Scans** | Number of simultaneous SNMP queries (default: 50) |
| **Timeout** | How long to wait for SNMP responses (default: 2000ms) |
| **Auto-Scan** | Automatically scan local subnets |

### Manual Scan

To trigger an immediate scan:
1. Go to the **Devices** tab
2. Click **Scan Now**
3. The scan will run in the background

### Scheduled Scans

Set up automatic periodic scanning:
1. Go to **Settings** → **Schedules**
2. Create a new schedule
3. Set the frequency (hourly, daily, weekly)
4. Select which IP ranges to include

---

## Understanding the Dashboard

### Agent Dashboard

| Section | Information |
|---------|-------------|
| **Overview** | Total devices, online/offline counts, recent activity |
| **Devices** | List of all discovered printers with status |
| **Settings** | Configuration options |
| **Logs** | Recent scan and system logs |

### Server Dashboard

| Section | Information |
|---------|-------------|
| **Fleet Overview** | Aggregate stats across all sites |
| **Agents** | Connected agents with status |
| **Devices** | All devices from all agents |
| **Reports** | Usage reports and analytics |

### Device Information

For each printer, PrintMaster collects:

| Data | Description |
|------|-------------|
| **Model** | Printer/copier model name |
| **Serial Number** | Unique device identifier |
| **IP Address** | Network address |
| **MAC Address** | Hardware address |
| **Page Counts** | Total pages printed (B&W, color, etc.) |
| **Toner Levels** | Remaining toner/ink percentages |
| **Status** | Online/offline, errors |
| **Location** | If configured on the device |

Use **Details** to open a device without a mouse gesture. Dialogs support
keyboard navigation and restore focus when closed; deleting a device requires
confirmation. Missing page counters are shown as **Not collected** rather than
zero. If a refresh fails, check the visible error and use **Retry** instead of
interpreting an empty result as an empty inventory.

---

## Next Steps

Now that you have PrintMaster running:

1. **[Configure SNMP settings](CONFIGURATION.md#snmp-settings)** if your printers don't use the default community string

2. **[Set up scheduled scans](FEATURES.md#scheduled-scans)** to keep device data current

3. **[Enable alerts](FEATURES.md#alerts)** for low toner or offline devices

4. **[Explore the API](dev/API.md)** for integrations with your existing tools

5. **[Configure auto-updates](CONFIGURATION.md#auto-updates)** to keep agents current

---

## Troubleshooting First-Time Setup

### No Printers Found

1. **Check network connectivity**: Can you ping the printer IPs?
2. **Verify SNMP is enabled** on your printers
3. **Check the community string**: Some printers use a custom string
4. **Check firewall rules**: SNMP uses UDP port 161

### Agent Not Connecting to Server

1. **Verify server URL**: Include the port (e.g., `http://server:9090`)
2. **Check network path**: Can the agent reach the server?
3. **Check firewall**: Server port 9090 must be accessible
4. **Review agent logs**: Check for connection errors

### WebSocket Errors

If you see WebSocket connection errors:
1. The agent will automatically fall back to HTTP
2. Check if a proxy is blocking WebSocket connections
3. Ensure the server's WebSocket port is accessible

See the full [Troubleshooting Guide](TROUBLESHOOTING.md) for more solutions.
