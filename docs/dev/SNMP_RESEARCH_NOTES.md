# SNMP Research Notes

This file captures actionable takeaways from inspecting other open-source SNMP printer discovery/management tools so we can fold the best ideas into PrintMaster without blindly copying code.

## Sources Consulted
- `Ircama/epson_print_conf` (Python) &mdash; exhaustive remote-mode OIDs plus EEPROM decoding for dozens of Epson models ([epson_print_conf.py](https://raw.githubusercontent.com/Ircama/epson_print_conf/main/epson_print_conf.py)).
- CUPS SNMP backend (`backend/snmp.c`) &mdash; hardened discovery logic with vendor-specific fallbacks ([OpenPrinting/cups](https://raw.githubusercontent.com/OpenPrinting/cups/master/backend/snmp.c)).

## Standard OIDs We Should Poll Everywhere
These came up repeatedly in both projects and map cleanly to our existing data model:

| Purpose | OID | Notes |
| --- | --- | --- |
| Human-readable device description | `1.3.6.1.2.1.25.3.2.1.3.x` (hrDeviceDescr) | Use as early sanity check before we walk vendor trees. |
| Printer status | `1.3.6.1.2.1.25.3.5.1.1` (hrPrinterStatus) | Map to our device health enums. |
| Serial number | `1.3.6.1.2.1.43.5.1.1.17.1` (prtGeneralSerialNumber) | Works on HP/Canon/Kyocera in addition to Epson. |
| Marker supplies level | `1.3.6.1.2.1.43.11.1.1.9.X` | Already stored, but ensure we handle negative "unknown" sentinel values. |
| Marker life count | `1.3.6.1.2.1.43.11.1.1.6.X` | Lets us compute remaining capacity deltas instead of only percentages. |
| Input/output tray status | `1.3.6.1.2.1.43.8.2.1.10` / `1.3.6.1.2.1.43.9.2.1.10` | Feed jam telemetry. |
| Alert text buffer | `1.3.6.1.2.1.43.18.1.1.8` | Provide user-facing fault text without parsing vendor payloads first. |

## Epson Remote-Mode Command Surface
Epson exposes a remote-control endpoint rooted at `1.3.6.1.4.1.1248.1.2.2.44.1.1.2.1.<cmd bytes…>` where the two ASCII bytes identify the command and the next two bytes encode payload length. The Python client simply reuses this helper for several high-value calls we can mirror:

| Command | Encoded suffix example | Data Returned | Why It Matters |
| --- | --- | --- | --- |
| `di` (device identification) | `.100.105.1.0.1` | ST2/IEEE-1284 style key/value pairs (MFG, CMD, MDL, CLS, DES). | Gives us stable make/model text even when standard Printer-MIB is sparse. |
| `st` (status) | `.115.116.1.0.1` | Binary `@BDC ST2` frame containing live status, ink levels, tray state, error/warning codes, maintenance box counters, etc. | One request replaces dozens of separate OIDs for Epson devices and surfaces alerts the generic MIB often hides. |
| `ia` (ink actuator list) | `.105.97.1.0.0` | Comma-separated cartridge SKUs. | Lets us map installed cartridge types to color/capacity definitions. |
| `ii` (ink slot detail) | `.105.105.2.0.1.<slot>` | Per-slot metadata (ink color ID, production date, quantity, manufacturer code). | Enables high-fidelity consumable tracking with minimal SNMP chatter. |
| `||` (EEPROM read) | `.124.124.<len_lo>.<len_hi>.<payload…>` | Raw EEPROM bytes at addresses the config lists (serial numbers, waste counters, timers, MAC addresses). | Used extensively to expose statistics that Epson never places in public MIBs. |

Implementation tips pulled from `epson_print_conf.py`:
- Read operations use `read_key` (two-byte secret per model) plus opcodes `65/190/160` before the address pair; writes use opcode `66/189/33` followed by the Caesar-shifted `write_key`.
- The helper already batches up to three OIDs per PDU via `cluster_varbinds`; we should copy the idea by grouping EEPROM reads so we do not stall the scanner.
- Several commands (`st`, `rw`, `vi`) return framed ASCII strings containing multiple values; building a lightweight parser (similar to their `status_parser`) would let us translate Epson-specific alerts into our unified telemetry stream.

## Epson EEPROM Windows Worth Mirroring
The configuration table shows consistent address blocks we can codify in `agent/scanner/vendor/epson.go` once we add EEPROM support:

- **Waste/maintenance counters**: most EcoTank/XP devices store the first box in addresses `24/25/30`, second box in `26/27/34`, and flag thresholds at `46/47` (or `54/55` for newer models). Large-format or tri-box units add a third counter at `252/253/254` with threshold `255`.
- **Cleaning & usage stats**: sequences such as `[147,149,148]` (manual/timer/power cleaning counts) and `[171-168]` (total print passes) recur across L-series. Rear-feed totals (`[755-752]`) and scan counters (`[1843-1840]`) exist on every ET-27xx/28xx/48xx variant.
- **Serial/MAC blocks**: legacy models keep serial ASCII in `range(192,202)` while Wi-Fi MACs live at `range(130,136)` or (newer) `range(1920,1926)`. These ranges match what we already scrape via standard MIBs, so they make excellent fallbacks when the printer restricts host MIB access.
- **Reset operations**: `raw_waste_reset` dictionaries show the exact EEPROM values Epson utilities write during a maintenance reset. We should **not** expose writes in the agent, but understanding the pattern helps us detect when a third-party reset happened (sudden drop to zero combined with unchanged counters).

## Discovery and Vendor Fallbacks from CUPS
The CUPS backend reinforces a few best practices for the current scanner in `agent/scanner/query.go` and `agent/scanner/snmp_targets.go`:

- Always begin with `hrDeviceType (1.3.6.1.2.1.25.3.2.1.2)` probes to confirm the target reports itself as `Printer(3)` before issuing heavier walks.
- After the initial response, immediately parallelize GETs for description, IEEE-1284 device ID, location, and URI (`ppmPortServiceNameOrURI`). This gives enough data to decide whether to keep probing or move on.
- Maintain a table of vendor-specific device-ID OIDs for common manufacturers: e.g., `1.3.6.1.4.1.11.2.3.9.1.1.7.0` (HP), `1.3.6.1.4.1.641.2.1.2.1.3.1` (Lexmark), `1.3.6.1.4.1.367.3.2.1.1.1.11.0` (Ricoh), `1.3.6.1.4.1.128.2.1.3.1.2.0` (Xerox). CUPS hits those opportunistically whenever it sees a response from the matching enterprise OID.
- If the device never returns a URI, CUPS still attempts TCP probes on 9100 (AppSocket) and 515 (LPD) before giving up. The scanner can retain these as reachability evidence; an open port alone must not establish printer identity.

## Action Items for PrintMaster
1. **Add a vendor plug-in for Epson remote mode**: reuse the command table above behind a feature flag, deserialize ST2 payloads, and surface ink/waste metrics in the agent database.
2. **Expand vendor-specific ID OIDs**: `agent/scanner/snmp_targets.go` already maintains the target catalog. Add future candidates there with query tests rather than recreating a target list in an old pipeline.
3. **Batch EEPROM/SNMP reads**: adopt the `cluster_varbinds` idea so we cap PDUs at three OIDs but still parallelize multiple PDUs; this will keep slow printers from starving an entire worker pool.
4. **Persist learned EEPROM ranges**: cache which address blocks responded per device so future scans do not brute-force every model-specific range.
5. **Map Epson waste counters into our metrics**: once we trust the data we can store normalized percentages for `main_waste`, `borderless_waste`, and any third maintenance box inside the `metrics` table with the same downsampling policy as toner levels.