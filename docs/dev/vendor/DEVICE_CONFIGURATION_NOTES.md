# Device Configuration Notes (Cross-Vendor)

These notes record what we know about **changing** printer settings remotely.
They are reference material for a future PrintMaster feature to edit
configuration across vendors. PrintMaster does not write device settings yet.

See the [configuration research section](configuration/README.md) and
[Epson investigation](configuration/EPSON.md) for evidence levels, artifact
identities, newly traced control paths, and unresolved field contracts.

## Epson (from Epson Device Admin reverse-engineering)

EDA uses three mechanisms:

| Mechanism | Used for | Notes |
|-----------|----------|-------|
| SNMP SET | Operation and maintenance settings | Confirmed below |
| ENPC / EpsonNet (UDP 3289, `BoronCustomClient`) | Discovery, community name and network settings | Proprietary. The packet schema is not exposed in the managed code |
| Configuration templates | Bulk settings | Managed client delegates to native `ENConfig.exe`; `NdSettingItem` describes target devices/results, not individual setting values. Exact per-field transport remains under investigation |

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
- This research has not resolved numeric SNMP OIDs for changing the admin
  password, community, or IP address. ENPC and Web Config are candidate paths;
  the exact transport and contract must be established per setting.

### Other writable-looking Epson subtrees (unverified)

The following were seen in EDA's code. Treat them as read-only until a SET
has been tested on real hardware:

| Area | OID |
|------|-----|
| Status notification settings | `1.2.2.8.2.14.3` / `14.4` |
| Remote Services | `1.2.2.8.2.21.1.1.*` |
| Power management | `1.2.2.1.1.3.1.32.*` |
| EOP keys and version | `1.2.2.8.2.11.1.1.*` |

## Kyocera (from Kyocera Net Device Manager reverse-engineering)

KDM uses two mechanisms:

| Mechanism | Used for | Notes |
|-----------|----------|-------|
| SNMP SET (`TemplateOp`, `SetProperty` on operators) | Network, protocols, SNMP, SMTP, power and panel settings | Writable OIDs are listed in `def.dev.snmp-oid.xml` |
| SOAP "KM-WSDL" on TCP 9090 (HTTP) / 9091 (HTTPS), paths `/ws/km-wsdl/...` | Device settings templates, address book, counters | `DeviceSettingService` covers about 895 properties |

### SNMP SET targets (relative to `1.3.6.1.4.1.1347.`)

| Area | OID subtree |
|------|-------------|
| TCP/IP and host name | `40.10.*` |
| SNMP communities (read/write) | `40.26.2.1.3` / `40.26.2.1.4` |
| SNMP trap destinations | `40.26.5.1.*` |
| SMTP / e-mail | `40.24.1.1.*` |
| Protocol enables: LPD, RAW, IPP, WSD, Bonjour, HTTP/HTTPS | `40.20`, `40.21`, `40.22` and `40.40`, `40.36`, `40.35`, `40.41`/`40.42` |
| SNMPv3 users | `1347.1001.6.1.1.*` |
| Sleep and low-power timers | `43.5.1.1.23`, `.33`, `.40` |
| Operation panel settings | `42.5.1.1.*` |

### Security unlock

Before writing protected settings, KDM unlocks the device:

1. SET `42.24.1.6.0` to the 15-character string `FFFFFFFFFFFFFFF`. Some
   models use `42.24.1.16.0`.
2. Read `42.23.1.8.0`. A value of 4 means the device is still locked, and
   writes will be rejected.

The SETs still need a write-capable community or SNMPv3 user.

### SOAP counter mapping

KM-WSDL counter responses mirror the SNMP counters. For example, KDM's
PageCount (5036) maps to
`counter_data.accounting_counter.accounting_print_total_counter.counter`.
The SOAP path is an alternative when SNMP is disabled.

## Other vendors

Not yet researched. Record findings here using the same structure:
mechanism, confirmed OIDs or endpoints, credential requirements, and gaps.

## Safety rules for a future implementation

- Before writing, read the current value and record it for rollback.
- Only allow writes for devices and roles that have been explicitly
  enabled. Writes must be admin-only and audited.
- After each write, read the value back to verify it.
- Never send write-capable credentials to devices that don't need them.
