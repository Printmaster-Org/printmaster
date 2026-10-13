# SNMP Walk Capture (Admin Tool)

Full SNMP walks of real devices are the raw material for vendor OID mapping
(see the other files in this directory). PrintMaster includes an admin-only
tool that captures walks from remote agents and saves them as downloadable
files. You don't need direct network access to the printer.

## Using it from the server UI

1. Update the agent at the remote site to a build that includes the tool.
2. Sign in to the server as a **global admin**. The menu items are hidden for
   other roles, and the server rejects the API for them.
3. Either:
   - Right-click a device and choose **SNMP Walk (Admin)…**. The IP and
     agent are filled in, and the label defaults to the model.
   - Right-click an agent and choose **SNMP Walks (Admin)…**, then enter any
     IP reachable from that agent.
4. Adjust **Max OIDs** if needed and click **Start Walk**. Progress (OID
   count and current root) refreshes every 2 seconds.
5. When the walk finishes, click **Download** to save the
   `snmpwalk_<ip>_<label>_<timestamp>_<id>.json.gz` file. Share that file for
   analysis.

Captures stay on the agent (`<data dir>/snmpwalks/`) until you delete them.
The agent keeps the 50 most recent finished walks.

## Limits

| Setting | Value |
|---------|-------|
| Default roots | `1.3.6.1.2.1` (MIB-2), `1.3.6.1.4.1` (enterprises) |
| Default max OIDs | 100,000 |
| Maximum max OIDs | 2,000,000 |
| Concurrent walks per agent | 2 (one per IP) |

- Roots are deduplicated. Nested roots are removed, so `1.3.6.1.2.1.43` is
  dropped when `1.3.6.1.2.1` is also present.
- If the OID limit is reached, the walk is marked `truncated`.
- Walks use GETBULK on SNMPv2c/v3 and GETNEXT on v1.
- Walks resume after timeouts. Each request is retried up to 4 times, the
  GETBULK repetition count is halved on each retry (starting at 25), and the
  walk continues from the last OID received. Slow devices such as the Epson
  WF-C878R (about 68,000 OIDs in about 19 minutes) can be walked completely.
- Walks use the agent's configured SNMP credentials.
- IP scanning must be enabled on the agent.

## File format

The file is gzipped JSON, written incrementally so large walks don't need to
fit in memory:

```json
{
  "format": "printmaster-snmp-walk/v1",
  "id": "…", "ip": "10.0.0.5", "label": "WF-C17590",
  "roots": ["1.3.6.1.2.1", "1.3.6.1.4.1"], "max_oids": 100000,
  "started_at": "…", "agent_version": "…", "agent_id": "…",
  "entries": [
    {"oid": ".1.3.6.1.2.1.1.1.0", "type": "OctetString", "str_value": "EPSON …", "hex_value": "4550…"}
  ],
  "summary": {
    "status": "complete", "oid_count": 12345, "truncated": false,
    "root_counts": {"1.3.6.1.2.1": 4000, "1.3.6.1.4.1": 8345},
    "errors": [], "finished_at": "…", "duration_ms": 61234
  }
}
```

Octet strings are written with both the decoded string and the raw hex
(`hex_value`), so binary payloads such as Epson ST2 status frames survive
intact. SNMP community strings are never written to the file.

## API

These endpoints live on the agent. From the server, reach them through the
agent proxy at `/api/v1/proxy/agent/{agentID}/…`. The server allows
`api/diagnostics/*` proxy paths only for global admins. Downloads get a
180-second proxy timeout.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/diagnostics/snmp-walks` | List walks, plus `default_roots`, `default_max_oids` and `max_max_oids` |
| POST | `/api/diagnostics/snmp-walks` | Start a walk. Body: `{"ip","roots","max_oids","label"}`. Returns 202, or 400 (invalid), 403 (scanning disabled) or 409 (busy) |
| GET | `/api/diagnostics/snmp-walks/{id}` | Walk status |
| GET | `/api/diagnostics/snmp-walks/{id}/download` | Download the `.json.gz` file |
| DELETE | `/api/diagnostics/snmp-walks/{id}` | Cancel a running walk, or delete a finished one |

When called directly against an agent (not through the server), the endpoints
require an agent admin session.

## Implementation

- `agent/agent/snmp_walk_jobs.go`: job manager, streaming writer, persistence
- `agent/snmp_walk_routes.go`: HTTP endpoints and admin check
- `server/main.go` (`handleAgentProxy`, `isAdminOnlyAgentProxyPath`): admin
  gating for the proxy
- `server/web/snmp-walk.js`: UI modal; menu entries are in
  `server/web/context-menu.js`
