# API Reference

REST API documentation for PrintMaster Agent and Server.

---

## Agent API

**Base URL**: `http://localhost:8080` (default agent port)

### Device Management

#### List Discovered Devices
```
GET /devices/discovered
```
Returns devices found but not yet saved.

**Response**:
```json
[{
  "IP": "10.0.0.100",
  "Manufacturer": "HP",
  "Model": "LaserJet Pro M404n",
  "Serial": "JPBCD12345",
  "PageCount": 12453,
  "TonerLevels": {"Black": 45}
}]
```

#### List Saved Devices
```
GET /devices/list
```
Returns all saved devices with full metadata.

#### Get Device Profile
```
GET /api/devices/profile?serial={serial}
```
Get canonical device profile by serial number.

**Response**:
```json
{
  "device": { /* Device object */ },
  "latest_metrics": { /* MetricsSnapshot or null */ }
}
```

#### Save Device
```
POST /devices/save
Content-Type: application/json

{"serial": "JPBCD12345"}
```

#### Save All Discovered
```
POST /devices/save/all
```

#### Delete Device
```
POST /devices/delete
Content-Type: application/json

{"serial": "JPBCD12345"}
```
Permanently removes device and all history.

#### Update Device Metadata
```
POST /devices/update
Content-Type: application/json

{
  "serial": "JPBCD12345",
  "asset_number": "IT-2024-001",
  "location": "3rd Floor Copy Room",
  "description": "Main office printer"
}
```

---

### Discovery

#### Start Discovery Scan
```
POST /discover
```
Scans configured IP ranges and local subnet.

---

### Metrics

#### Collect Device Metrics
```
POST /devices/metrics/collect
Content-Type: application/json

{"serial": "JPBCD12345"}
```

#### Get Latest Metrics
```
GET /api/devices/metrics/latest?serial={serial}
```

#### Get Metrics History
```
GET /api/devices/metrics/history?serial={serial}&since={iso}&until={iso}
```
Query params use RFC3339 timestamps.

---

### Settings

#### Get Settings
```
GET /settings
```

**Response**:
```json
{
  "discovery": {
    "subnet_scan": true,
    "manual_ranges": true,
    "ranges_text": "10.0.0.1-10.0.0.254",
    "enable_snmp": true,
    "snmp_timeout_ms": 2000,
    "discover_concurrency": 20
  }
}
```

#### Update Settings
```
POST /settings
Content-Type: application/json

{
  "discovery": {
    "subnet_scan": true,
    "snmp_timeout_ms": 3000
  }
}
```
Partial updates supported.

---

### Real-Time Updates

#### Server-Sent Events
```
GET /events
```
SSE stream for real-time UI updates.

**Events**:
- `connected` - Connection established
- `discovery_update` - Discovery progress
- `device_change` - Device updated

```javascript
const eventSource = new EventSource('/events');
eventSource.addEventListener('discovery_update', (e) => {
  console.log('Progress:', JSON.parse(e.data));
});
```

---

### Logging

#### Get Recent Logs
```
GET /logs?level=INFO&tail=100
```

#### Download Log File
```
GET /logfile
```

---

## Server API

**Base URL**: `http://localhost:9090` (default server port)

### Health Check
```
GET /api/v1/health
```

### Agent Registration

#### Register Agent
```
POST /api/v1/agents/register
Content-Type: application/json

{
  "agent_id": "uuid",
  "name": "Office Agent",
  "version": "0.23.6"
}
```

#### Agent Heartbeat
```
POST /api/v1/agents/heartbeat
Content-Type: application/json

{
  "agent_id": "uuid",
  "device_count": 15
}
```

### Agent Metrics Upload Receipts

`POST /api/v1/metrics/batch` uses the Agent bearer token, not a user
session. The payload `agent_id` must match the authenticated Agent.

An authenticated, decoded batch returns HTTP `200` with `received`, `stored`,
and `success`. For metrics, `success` is true only when every snapshot was
stored. Rejected records appear in `failures` with a zero-based `index`,
an optional submitted `serial`, and a `code`: `invalid_record`,
`ownership_conflict`, or `storage_error`. Missing and foreign-owned devices
share the ownership-conflict code; database error details are not returned.
An empty batch succeeds with both counts zero.

The Agent checks persistence counts even with older Servers that return
`success: true` for partial batches. Rejections do not advance the last
successful metrics-upload time or trigger immediate whole-batch retries,
which would duplicate already accepted snapshots. Normal upload cycles
continue with the latest local snapshots; this is not a historical retry
queue or an exactly-once delivery guarantee. Transport failures retain the
existing retry policy.

Device-batch receipts and device/metrics ownership checks are unchanged.
Same-tenant shared reporting, device-level site assignment and
agent-independent device retirement are follow-up work, not enabled here.
See the canonical [machine protocol guide](https://docs.printmaster.work/api/protocol/#metrics-batch-receipts)
for an example and mixed-version behavior.

### WebSocket Connection

#### Agent WebSocket
```
WS /api/v1/agents/ws
```
Real-time communication channel for agent-server communication.

### Fleet Management

#### List Agents
```
GET /api/v1/agents
```

#### Get Agent Details
```
GET /api/v1/agents/{agent_id}
```

#### List All Devices
```
GET /api/v1/devices
```
Aggregated view across all agents.

---

## Authentication

### Agent API

The agent UI supports multiple authentication modes:

| Mode | Behavior |
|------|----------|
| `local` | No login required; admin tasks require loopback access |
| `server` | Delegates authentication to central server |
| `disabled` | No authentication (development only) |

Configure in `config.toml`:
```toml
[web.auth]
mode = "local"
allow_local_admin = true
```

### Server API

The server requires authentication for most endpoints.

#### Login
```
POST /api/v1/auth/login
Content-Type: application/json

{
  "username": "admin",
  "password": "your-password"
}
```

**Response**:
```json
{
  "token": "session-token",
  "expires_at": "2025-12-29T12:00:00Z"
}
```

Include the token in subsequent requests:
```
Authorization: Bearer {token}
```

---

## Error Handling

**HTTP Status Codes**:
- `200 OK` - Success
- `400 Bad Request` - Invalid parameters
- `401 Unauthorized` - Authentication required
- `404 Not Found` - Resource not found
- `500 Internal Server Error` - Server error

**Error Response**:
```json
{
  "error": "Device not found",
  "details": "No device with serial JPBCD12345"
}
```

---

## Examples

### JavaScript: Discover and Save

```javascript
// Start discovery
await fetch('/discover', {method: 'POST'});

// Get discovered devices
const resp = await fetch('/devices/discovered');
const devices = await resp.json();

// Save first device
await fetch('/devices/save', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({serial: devices[0].Serial})
});
```

### curl: Update Settings

```bash
curl -X POST http://localhost:8080/settings \
  -H "Content-Type: application/json" \
  -d '{
    "discovery": {
      "subnet_scan": true,
      "snmp_timeout_ms": 3000
    }
  }'
```

### PowerShell: Get Device List

```powershell
$devices = Invoke-RestMethod -Uri "http://localhost:8080/devices/list"
$devices | ForEach-Object { "$($_.Model) - $($_.Serial)" }
```

---

## See Also

- [Configuration Guide](CONFIGURATION.md)
- [Features Guide](FEATURES.md)
