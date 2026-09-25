# Unraid Deployment

Deploy PrintMaster Server on Unraid using the Docker container.

---

## Installation Methods

### Method 1: Community Applications (Recommended)

1. Install **Community Applications** plugin if not already installed
2. Search for "PrintMaster Server"
3. Click **Install**
4. Configure settings (see below)
5. Click **Apply**

### Method 2: Manual Docker Setup

1. Go to **Docker** tab in Unraid
2. Click **Add Container**
3. Configure:

| Setting | Value |
|---------|-------|
| Name | `PrintMaster-Server` |
| Repository | `ghcr.io/printmaster-org/printmaster-server:latest` |
| Network Type | `Bridge` |
| Port | `9090` → `9090` (TCP) |
| Volume | `/mnt/user/appdata/printmaster-server/data` → `/var/lib/printmaster/server` |
| Volume | `/mnt/user/appdata/printmaster-server/logs` → `/var/log/printmaster/server` |

---

## Configuration

### Essential Settings

| Setting | Value | Description |
|---------|-------|-------------|
| **HTTP Port** | `9090` | Web interface and API |
| **Data Directory** | `/mnt/user/appdata/printmaster-server/data` | Database storage |
| **Logs Directory** | `/mnt/user/appdata/printmaster-server/logs` | Application logs |
| **Timezone** | Your timezone (e.g., `America/New_York`) | For correct timestamps |

### Environment Variables

| Variable | Value | Description |
|----------|-------|-------------|
| `BIND_ADDRESS` | `0.0.0.0` | Allow external access |
| `BEHIND_PROXY` | `true` or `false` | Behind Nginx Proxy Manager? |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `TZ` | `America/New_York` | Your timezone |
| `PM_DISABLE_SELFUPDATE` | `true` | Recommended for Docker |

---

## Reverse Proxy Setup

### With Nginx Proxy Manager

1. Set container variables:
   ```
   BEHIND_PROXY=true
   PROXY_USE_HTTPS=true
   ```

2. In Nginx Proxy Manager:
   - **Scheme**: `http` (not https)
   - **Forward Hostname**: `printmaster-server` or Unraid IP
   - **Forward Port**: `9090`
   - **Enable WebSockets**: ✅ Required!
   - Configure SSL certificate

### Without Reverse Proxy (Direct HTTPS)

| Variable | Value |
|----------|-------|
| `BEHIND_PROXY` | `false` |
| `TLS_MODE` | `self-signed` |
| `SERVER_HTTPS_PORT` | `9443` |

Map port `9443` in addition to `9090`.

---

## File Locations

### On Unraid Host

```
/mnt/user/appdata/printmaster-server/
├── data/
│   ├── server.db           # SQLite database
│   └── config.toml         # Configuration (optional)
└── logs/
    └── server.log          # Application logs
```

### Inside Container

```
/var/lib/printmaster/server/    # Data
/var/log/printmaster/server/    # Logs
```

---

## First-Time Setup

1. **Start the container**
   - Database and config created automatically

2. **Access the Web UI**
   - Direct: `http://YOUR-UNRAID-IP:9090`
   - Via Proxy: `https://printmaster.yourdomain.com`

3. **Login**
   - Username: `admin`
   - Password: `printmaster` (change immediately!)

4. **Connect Agents**
   - Agent config: `server_url = "http://YOUR-UNRAID-IP:9090"`

---

## Updating

### Via Unraid UI

1. Go to **Docker** tab
2. Click **Check for Updates**
3. Click **Update** if available

### Via Community Applications

Use **CA Auto Update Applications** plugin for automatic updates.

---

## Backup

### What to Backup

```
/mnt/user/appdata/printmaster-server/data/server.db
/mnt/user/appdata/printmaster-server/data/config.toml (if customized)
```

### Using Appdata Backup Plugin

- Includes `/mnt/user/appdata/printmaster-server/` automatically

### Manual Backup

```bash
# Stop container first
docker stop PrintMaster-Server

# Backup
cp /mnt/user/appdata/printmaster-server/data/server.db \
   /mnt/user/backups/printmaster-$(date +%Y%m%d).db

# Restart
docker start PrintMaster-Server
```

---

## Troubleshooting

### Container Won't Start

1. Check logs: Docker tab → Click container → **Logs**
2. Verify appdata folder permissions
3. Ensure port 9090 isn't in use

### Can't Access Web UI

```bash
# From Unraid terminal
curl http://localhost:9090/api/v1/health
```

### Permission Errors

```bash
# Fix permissions
chown -R 99:100 /mnt/user/appdata/printmaster-server/
chmod -R 755 /mnt/user/appdata/printmaster-server/
```

> Container uses UID 99, matching Unraid's `nobody` user.

### 502 Bad Gateway (Nginx Proxy Manager)

- Ensure `BEHIND_PROXY=true`
- Use `http://` in NPM (not https)
- Enable WebSockets in proxy settings

### View Logs

```bash
docker logs -f PrintMaster-Server
```

---

## Integration with Other Apps

### Uptime Kuma

Monitor PrintMaster availability:
- URL: `http://printmaster-server:9090/api/v1/health`

### Nginx Proxy Manager

- Install from Community Applications
- Create proxy host for PrintMaster
- Use Let's Encrypt for SSL

---

## Complete Example

```
Container Name: PrintMaster-Server
Repository: ghcr.io/printmaster-org/printmaster-server:latest
Network Type: bridge

Ports:
  9090/tcp → 9090

Volumes:
  /mnt/user/appdata/printmaster-server/data → /var/lib/printmaster/server
  /mnt/user/appdata/printmaster-server/logs → /var/log/printmaster/server

Environment Variables:
  BIND_ADDRESS=0.0.0.0
  BEHIND_PROXY=true
  LOG_LEVEL=info
  TZ=America/Chicago
  PM_DISABLE_SELFUPDATE=true
```

Works with Nginx Proxy Manager at `https://printmaster.mydomain.com`.

---

## See Also

- [Docker Deployment](docker.md)
- [Configuration Guide](../CONFIGURATION.md)
- [Installation Guide](../INSTALL.md)
