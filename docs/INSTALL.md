# Installation Guide

This guide covers installing PrintMaster on all supported platforms.

## Table of Contents

- [Quick Install](#quick-install)
- [Server Installation](#server-installation)
  - [Docker (Recommended)](#docker-recommended)
  - [Unraid](#unraid)
  - [Manual Installation](#manual-server-installation)
- [Agent Installation](#agent-installation)
  - [Windows](#windows)
  - [Linux (Debian/Ubuntu)](#linux-debianubuntu)
  - [Linux (Fedora/RHEL)](#linux-fedorarhel)
  - [macOS](#macos)
  - [Docker](#docker-agent)
- [First-Time Setup](#first-time-setup)

---

## Quick Install

### Server (Docker)

```bash
docker run -d \
  --name printmaster-server \
  -p 9090:9090 \
  -v printmaster-data:/var/lib/printmaster/server \
  -e ADMIN_PASSWORD=your-secure-password \
  ghcr.io/printmaster-org/printmaster-server:latest
```

Access at `http://localhost:9090` with username `admin` and your chosen password.

### Agent (Windows)

Download and run the MSI installer from [GitHub Releases](https://github.com/printmaster-org/printmaster/releases).

### Agent (Linux)

```bash
# Debian/Ubuntu
curl -fsSL https://packages.printmaster.work/install.sh | sudo bash

# Or manual apt install
echo "deb [trusted=yes] https://packages.printmaster.work stable main" | \
  sudo tee /etc/apt/sources.list.d/printmaster.list
sudo apt-get update && sudo apt-get install -y printmaster-agent
```

---

## Server Installation

The server provides centralized management for multiple agents. If you only need to monitor printers at a single site, you can run the agent standalone without a server.

### Docker (Recommended)

Docker is the recommended deployment method for the server.

#### Prerequisites
- Docker Engine 20.10 or later
- Docker Compose (optional but recommended)

#### Using Docker Run

```bash
# Basic setup
docker run -d \
  --name printmaster-server \
  -p 9090:9090 \
  -v printmaster-data:/var/lib/printmaster/server \
  -v printmaster-logs:/var/log/printmaster/server \
  -e ADMIN_PASSWORD=your-secure-password \
  ghcr.io/printmaster-org/printmaster-server:latest
```

#### Using Docker Compose

Create a `docker-compose.yml` file:

```yaml
version: '3.8'
services:
  printmaster-server:
    image: ghcr.io/printmaster-org/printmaster-server:latest
    container_name: printmaster-server
    ports:
      - "9090:9090"
    volumes:
      - printmaster-data:/var/lib/printmaster/server
      - printmaster-logs:/var/log/printmaster/server
    environment:
      - ADMIN_PASSWORD=your-secure-password
      - LOG_LEVEL=info
      - BEHIND_PROXY=false
    restart: unless-stopped

volumes:
  printmaster-data:
  printmaster-logs:
```

Start with:
```bash
docker compose up -d
```

#### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_PASSWORD` | `printmaster` | Admin password (set before first run!) |
| `LOG_LEVEL` | `info` | Logging level: debug, info, warn, error |
| `BEHIND_PROXY` | `false` | Set to `true` if behind a reverse proxy |
| `BIND_ADDRESS` | `0.0.0.0` | Address to bind to |
| `HTTP_PORT` | `9090` | HTTP port |
| `HTTPS_PORT` | `9443` | HTTPS port (when TLS enabled) |

> **Important**: Set `ADMIN_PASSWORD` before the first run. The password can only be set during initial database creation.

#### Behind a Reverse Proxy

If using Nginx Proxy Manager, Traefik, or another reverse proxy:

```yaml
environment:
  - BEHIND_PROXY=true
  - BIND_ADDRESS=0.0.0.0
```

Configure your proxy to:
- Forward to port 9090
- Enable WebSocket support (required for real-time features)
- Handle SSL termination

### Unraid

1. **Using Community Applications** (Easiest):
   - Install the Community Applications plugin
   - Search for "PrintMaster Server"
   - Click Install and configure

2. **Manual Docker Setup**:
   - Go to Docker tab → Add Container
   - Repository: `ghcr.io/printmaster-org/printmaster-server:latest`
   - Port: 9090 → 9090
   - Path: `/mnt/user/appdata/printmaster-server/data` → `/var/lib/printmaster/server`
   - Path: `/mnt/user/appdata/printmaster-server/logs` → `/var/log/printmaster/server`

See [Unraid Deployment Guide](dev/UNRAID_DEPLOYMENT.md) for detailed instructions.

### Manual Server Installation

Download the server binary from [GitHub Releases](https://github.com/printmaster-org/printmaster/releases) and run:

```bash
# Linux/macOS
./printmaster-server

# Windows
.\printmaster-server.exe
```

---

## Agent Installation

### Windows

#### MSI Installer (Recommended)

1. Download the latest MSI from [GitHub Releases](https://github.com/printmaster-org/printmaster/releases)
2. Run the installer
3. The agent will be installed as a Windows service and start automatically
4. Access the web UI at `http://localhost:8080`

#### Manual Installation

```powershell
# Download the binary
Invoke-WebRequest -Uri "https://github.com/printmaster-org/printmaster/releases/latest/download/printmaster-agent-windows-amd64.exe" -OutFile "printmaster-agent.exe"

# Install as service (requires Administrator)
.\printmaster-agent.exe --service install

# Start the service
.\printmaster-agent.exe --service start
```

#### Service Management

```powershell
# Check status
Get-Service PrintMasterAgent

# Stop service
.\printmaster-agent.exe --service stop

# Uninstall service
.\printmaster-agent.exe --service uninstall
```

### Linux (Debian/Ubuntu)

#### APT Repository (Recommended)

```bash
# Add repository
echo "deb [trusted=yes] https://packages.printmaster.work stable main" | \
  sudo tee /etc/apt/sources.list.d/printmaster.list

# Install
sudo apt-get update
sudo apt-get install -y printmaster-agent

# The service starts automatically
systemctl status printmaster-agent
```

#### With GPG Signature Verification (Recommended for Production)

```bash
# Import GPG key
curl -fsSL https://packages.printmaster.work/gpg.key | \
  sudo gpg --dearmor -o /usr/share/keyrings/printmaster.gpg

# Add repository with signature verification
echo "deb [signed-by=/usr/share/keyrings/printmaster.gpg] https://packages.printmaster.work stable main" | \
  sudo tee /etc/apt/sources.list.d/printmaster.list

# Install
sudo apt-get update
sudo apt-get install -y printmaster-agent
```

#### Manual Installation

```bash
# Download
wget https://github.com/printmaster-org/printmaster/releases/latest/download/printmaster-agent-linux-amd64

# Make executable
chmod +x printmaster-agent-linux-amd64
sudo mv printmaster-agent-linux-amd64 /usr/local/bin/printmaster-agent

# Install as service
sudo printmaster-agent --service install
sudo systemctl start PrintMasterAgent
```

### Linux (Fedora/RHEL)

#### DNF Repository (Recommended)

```bash
# Import GPG key (recommended)
sudo rpm --import https://packages.printmaster.work/gpg.key

# Add repository
sudo dnf config-manager addrepo --from-repofile=https://packages.printmaster.work/printmaster.repo

# Install
sudo dnf install -y printmaster-agent

# The service starts automatically
systemctl status printmaster-agent
```

#### Important Linux Paths

| Path | Description |
|------|-------------|
| `/usr/bin/printmaster-agent` | Agent binary |
| `/etc/printmaster/agent.toml` | Configuration file |
| `/var/lib/printmaster` | Data directory (SQLite DB) |
| `/var/log/printmaster` | Log files |

### macOS

```bash
# Download
curl -LO https://github.com/printmaster-org/printmaster/releases/latest/download/printmaster-agent-darwin-amd64

# Make executable
chmod +x printmaster-agent-darwin-amd64
sudo mv printmaster-agent-darwin-amd64 /usr/local/bin/printmaster-agent

# Install as service
sudo printmaster-agent --service install

# Start service
sudo launchctl load /Library/LaunchDaemons/com.printmaster.agent.plist
```

### Docker Agent

The agent can also run in Docker for specialized deployments:

```bash
docker run -d \
  --name printmaster-agent \
  --network host \
  -v printmaster-agent-data:/var/lib/printmaster/agent \
  ghcr.io/printmaster-org/printmaster-agent:latest
```

> **Note**: `--network host` is required for SNMP discovery to work properly.

---

## First-Time Setup

### Accessing the Web UI

| Component | Default URL | Default Port |
|-----------|-------------|--------------|
| Agent | `http://localhost:8080` | 8080 |
| Server | `http://localhost:9090` | 9090 |

### Server First Login

1. Open `http://your-server:9090`
2. Log in with:
   - Username: `admin`
   - Password: The password you set via `ADMIN_PASSWORD` (default: `printmaster`)
3. **Change the default password immediately** if you didn't set one during installation

### Connecting an Agent to the Server

1. Open the agent's web UI at `http://agent-ip:8080`
2. Go to **Settings** → **Server Connection**
3. Enter your server URL: `http://your-server:9090`
4. Click **Save**

Or edit the agent's config file:

```toml
[server]
enabled = true
url = "http://your-server:9090"
```

### Next Steps

- [Getting Started Guide](GETTING_STARTED.md) - Configure discovery and scan your first printers
- [Features Guide](FEATURES.md) - Learn about all available features
- [Configuration Guide](CONFIGURATION.md) - Fine-tune your setup

---

## Upgrading

### Docker

```bash
docker pull ghcr.io/printmaster-org/printmaster-server:latest
docker compose down
docker compose up -d
```

### Linux (APT)

```bash
sudo apt-get update
sudo apt-get upgrade printmaster-agent
```

### Linux (DNF)

```bash
sudo dnf upgrade printmaster-agent
```

### Windows

Run the new MSI installer - it will upgrade the existing installation.

### Auto-Updates

Agents support automatic updates. See [Configuration Guide](CONFIGURATION.md#auto-updates) for setup instructions.
