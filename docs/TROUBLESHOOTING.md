# Troubleshooting Guide

Solutions for common PrintMaster issues.

## Table of Contents

- [Discovery Issues](#discovery-issues)
- [Connection Issues](#connection-issues)
- [Web UI Issues](#web-ui-issues)
- [Service Issues](#service-issues)
- [Database Issues](#database-issues)
- [Performance Issues](#performance-issues)
- [Logs & Diagnostics](#logs--diagnostics)

---

## Discovery Issues

### No Printers Found

**Symptoms**: Scan completes but no devices appear.

**Solutions**:

1. **Verify network connectivity**
   ```bash
   # Can you reach the printer?
   ping 192.168.1.100
   ```

2. **Check SNMP is enabled on the printer**
   - Access the printer's web interface
   - Look for SNMP settings in Network or Security
   - Ensure SNMP v1/v2c is enabled

3. **Verify the SNMP community string**
   ```bash
   # Test with snmpwalk (if available)
   snmpwalk -v2c -c public 192.168.1.100 sysDescr
   ```
   - Default is `public`, but some printers use `private` or a custom string
   - Update in **Settings** → **SNMP Community**

4. **Check firewall rules**
   - SNMP uses UDP port 161
   - Ensure outbound UDP 161 is allowed from the agent

5. **Check the IP range configuration**
   - Verify the correct subnet is configured
   - Try scanning a single known-good IP first

### Some Printers Missing

**Symptoms**: Some printers found, others not.

**Solutions**:

1. **Different SNMP community strings**
   - Some printers may use a different community string
   - Try scanning those IPs individually with the correct string

2. **SNMP timeout too short**
   - Increase timeout: **Settings** → **SNMP Timeout** → 3000ms or higher

3. **Printer SNMP disabled or restricted**
   - Check the printer's SNMP access list
   - Some printers only respond to specific IP addresses

4. **Network segmentation**
   - Verify the agent can reach all subnets
   - May need agents in multiple VLANs

### Incomplete Device Information

**Symptoms**: Devices found but missing model, serial, or counters.

**Solutions**:

1. **Increase SNMP timeout and retries**
   ```toml
   [snmp]
   timeout_ms = 3000
   retries = 2
   ```

2. **Check vendor support**
   - Some older or budget printers have limited SNMP
   - Check the logs for specific OID errors

3. **Run a manual deep scan**
   - Go to **Devices** → select device → **Rescan**

---

## Connection Issues

### Agent Not Connecting to Server

**Symptoms**: Agent shows "Disconnected" in server dashboard.

**Solutions**:

1. **Verify server URL format**
   ```toml
   [server]
   url = "http://server-ip:9090"  # Include protocol and port!
   ```

2. **Test network connectivity**
   ```bash
   # From agent machine
   curl http://server-ip:9090/api/v1/health
   ```

3. **Check firewall**
   - Server port (default 9090) must be accessible
   - Both TCP HTTP and WebSocket connections needed

4. **Verify server is running**
   ```bash
   docker ps | grep printmaster
   # or
   systemctl status printmaster-server
   ```

5. **Check agent logs**
   - Look for connection errors
   - See [Logs & Diagnostics](#logs--diagnostics)

### WebSocket Connection Failing

**Symptoms**: "WebSocket error" messages, real-time updates not working.

**Solutions**:

1. **Agent will auto-fallback to HTTP**
   - This is normal behavior, not an error
   - Real-time updates will be slightly delayed

2. **Check proxy configuration**
   - Reverse proxies must support WebSocket passthrough
   - For Nginx:
     ```nginx
     location / {
         proxy_pass http://printmaster:9090;
         proxy_http_version 1.1;
         proxy_set_header Upgrade $http_upgrade;
         proxy_set_header Connection "upgrade";
     }
     ```

3. **Check for WebSocket blocking**
   - Some corporate firewalls block WebSocket
   - Test with direct connection (bypassing proxy)

### Agent Keeps Reconnecting

**Symptoms**: Agent status flapping between connected/disconnected.

**Solutions**:

1. **Network instability**
   - Check network path between agent and server
   - Monitor for packet loss or high latency

2. **Server resource issues**
   - Check server CPU/memory usage
   - May need to scale up server resources

3. **Increase heartbeat interval**
   ```toml
   [server]
   heartbeat_interval_seconds = 120
   ```

---

## Web UI Issues

### Cannot Access Web UI

**Symptoms**: Browser shows connection refused or timeout.

**Solutions**:

1. **Verify service is running**
   ```powershell
   # Windows
   Get-Service PrintMasterAgent
   
   # Linux
   systemctl status printmaster-agent
   ```

2. **Check the correct port**
   - Default: Agent = 8080, Server = 9090
   - May be configured differently

3. **Check bind address**
   - Default binds to all interfaces
   - May be restricted to localhost only

4. **Check firewall**
   - Ensure the port is open

5. **Try localhost**
   - Access from the local machine first
   - `http://localhost:8080`

### UI Loading Slowly

**Solutions**:

1. **Check network latency**
   - High latency = slow UI

2. **Check device count**
   - Large device lists may load slowly
   - Use pagination or filters

3. **Clear browser cache**
   - Old cached assets may cause issues

### Login Issues

**Symptoms**: Cannot log in, or session keeps expiring.

**Solutions**:

1. **Verify credentials**
   - Default server: `admin` / `printmaster` (or your set password)

2. **Check for cookie issues**
   - Clear browser cookies
   - Ensure cookies are enabled

3. **Reset password** (server)
   - Stop the server
   - Delete the database (⚠️ loses all data)
   - Restart with new `ADMIN_PASSWORD`

---

## Service Issues

### Service Won't Start (Windows)

**Solutions**:

1. **Check Event Viewer**
   - Look in Application log for PrintMaster errors

2. **Run interactively to see errors**
   ```powershell
   .\printmaster-agent.exe
   ```

3. **Check port conflicts**
   ```powershell
   netstat -ano | findstr :8080
   ```

4. **Reinstall service**
   ```powershell
   .\printmaster-agent.exe --service uninstall
   .\printmaster-agent.exe --service install
   .\printmaster-agent.exe --service start
   ```

### Service Won't Start (Linux)

**Solutions**:

1. **Check systemd logs**
   ```bash
   journalctl -u printmaster-agent -f
   ```

2. **Check permissions**
   - Service needs read/write to data directory
   - Check file ownership

3. **Check SELinux/AppArmor**
   - May be blocking network or file access
   ```bash
   ausearch -m avc -ts recent
   ```

### Service Stops Unexpectedly

**Solutions**:

1. **Check logs for crash information**

2. **Check resource usage**
   - Out of memory may cause crashes
   - Check disk space for database

3. **Update to latest version**
   - May be a known bug that's been fixed

---

## Database Issues

### Database Locked Errors

**Symptoms**: "database is locked" errors in logs.

**Solutions**:

1. **Check for multiple instances**
   - Only one process should access the database
   - Kill duplicate processes

2. **Check disk space**
   ```bash
   df -h /var/lib/printmaster
   ```

3. **Check disk I/O**
   - High I/O latency can cause locking issues

### Database Corruption

**Symptoms**: Startup errors mentioning database, or missing data.

**Solutions**:

1. **Stop the service**

2. **Create a backup of the database**
   ```bash
   cp printmaster.db printmaster.db.backup
   ```

3. **Try recovery**
   ```bash
   sqlite3 printmaster.db "PRAGMA integrity_check;"
   ```

4. **If corrupt, restore from backup or recreate**
   - Delete the database file
   - Restart the service (creates new database)
   - Rediscover devices

---

## Performance Issues

### Slow Discovery Scans

**Solutions**:

1. **Reduce concurrent scans** (if network constrained)
   ```toml
   discovery_concurrency = 25
   ```

2. **Increase concurrent scans** (if CPU constrained)
   ```toml
   discovery_concurrency = 100
   ```

3. **Reduce IP range scope**
   - Scan only subnets with printers
   - Avoid scanning entire /16 networks

4. **Increase timeout if many devices offline**
   - Scanning dead IPs wastes time waiting for timeout

### High Memory Usage

**Solutions**:

1. **Check device count**
   - Normal: ~1MB per 100 devices

2. **Restart service** to clear memory

3. **Check for memory leaks**
   - Report persistent memory growth as a bug

### High CPU Usage

**Solutions**:

1. **During scans is normal**
   - CPU usage spikes during discovery

2. **Check scan frequency**
   - Too frequent = constant high CPU
   - Hourly scans usually sufficient

3. **Check log level**
   - Debug logging increases CPU usage
   - Set to `info` for production

---

## Logs & Diagnostics

### Log Locations

| Platform | Agent Logs | Server Logs |
|----------|------------|-------------|
| Windows | Event Viewer → Application | Event Viewer → Application |
| Linux | `journalctl -u printmaster-agent` | `journalctl -u printmaster-server` |
| Docker | `docker logs printmaster-server` | `docker logs printmaster-agent` |

### Enabling Debug Logging

**Via config file:**
```toml
[logging]
level = "debug"
```

**Via environment variable:**
```bash
export AGENT_LOG_LEVEL=debug
```

**Via command line:**
```bash
./printmaster-agent -log-level debug
```

### Collecting Diagnostics

When reporting an issue, include:

1. **Version information**
   ```bash
   ./printmaster-agent -version
   ```

2. **Configuration** (redact sensitive values)

3. **Relevant log entries**

4. **Steps to reproduce**

5. **Expected vs actual behavior**

### Health Check Endpoints

**Agent:**
```bash
curl http://localhost:8080/api/v1/health
```

**Server:**
```bash
curl http://localhost:9090/api/v1/health
```

These return JSON with status information useful for diagnostics.

---

## Getting Help

If you can't resolve an issue:

1. **Search existing issues**: [GitHub Issues](https://github.com/printmaster-org/printmaster/issues)

2. **Ask the community**: [GitHub Discussions](https://github.com/printmaster-org/printmaster/discussions)

3. **Report a bug**: Create a new GitHub issue with diagnostics
