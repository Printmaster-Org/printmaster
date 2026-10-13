# Device Configuration Notes (Cross-Vendor)

These notes record what we know about **changing** printer settings remotely.
They are reference material for a future PrintMaster feature to edit
configuration across vendors. PrintMaster does not write device settings yet.

## Epson (from Epson Device Admin reverse-engineering)

EDA uses three mechanisms:

| Mechanism | Used for | Notes |
|-----------|----------|-------|
| SNMP SET | Operation and maintenance settings | Confirmed below |
| ENPC / EpsonNet (UDP 3289, `BoronCustomClient`) | Discovery, community name and network settings | Proprietary. The packet schema is not exposed in the managed code |
| Configuration templates | Bulk settings | Applied as collections of `NdSettingItem`, then written via the channels above |

### Confirmed SNMP SET pattern: operation table

From `NetworkSheetProvider.cs`:

1. Read `1.3.6.1.4.1.1248.1.2.2.21.1.1.2.1.<i>`. This is the operation type for row `i`.
2. Read `1.3.6.1.4.1.1248.1.2.2.21.1.1.3.1.<i>`. This is the current setting.
3. Write the change with `SET 1.3.6.1.4.1.1248.1.2.2.21.1.1.3.1.<i> = 1`. EDA uses this to trigger an action, such as printing a network status sheet.

This shows that Epson exposes a writable operation table. Discover the rows
by walking `21.1.1.2` before writing anything.

### Credentials and security

- SETs need write access: a read-write community (v1/v2c) or an SNMPv3 user
  with write permissions.
- EDA supports these SNMPv3 auth algorithms: MD5 and SHA-1/224/256/384/512.
- EDA keeps device admin passwords in its own password manager, keyed by
  MAC or device ID. It does not read them from the device.
- No SNMP OIDs were found for changing the admin password, community,
  IP address or Web Config forms. EDA uses ENPC or Web Config (HTTP) for those.

### Other writable-looking Epson subtrees (unverified)

The following were seen in EDA's code. Treat them as read-only until a SET
has been tested on real hardware:

| Area | OID |
|------|-----|
| Status notification settings | `1.2.2.8.2.14.3` / `14.4` |
| Remote Services | `1.2.2.8.2.21.1.1.*` |
| Power management | `1.2.2.1.1.3.1.32.*` |
| EOP keys and version | `1.2.2.8.2.11.1.1.*` |

## Other vendors

Not yet researched. Record findings here using the same structure:
mechanism, confirmed OIDs or endpoints, credential requirements, and gaps.

## Safety rules for a future implementation

- Before writing, read the current value and record it for rollback.
- Only allow writes for devices and roles that have been explicitly
  enabled. Writes must be admin-only and audited.
- After each write, read the value back to verify it.
- Never send write-capable credentials to devices that don't need them.
