# Epson Enterprise OID Mapping

**Source of truth**: the MIB reads and derivations performed by Epson Device Admin
(EDA), cross-checked value-by-value against EDA's device detail UI and a live
SNMP walk of an Epson ST-C8090 (enterprise `1.3.6.1.4.1.1248`).
Earlier ICE / web-UI mapping (AM-C550, WF-C17590) is consistent with this.

Implementation: `agent/scanner/vendor/epson.go`, `epson_st2_parser.go`,
constants in `common/snmp/oids/oids.go`. Fixture test:
`agent/scanner/vendor/epson_test.go` (live values).

All Epson OIDs below are relative to `1.3.6.1.4.1.1248.`.

## How values are collected

| Kind | Query | Notes |
|------|-------|-------|
| Scalars | batched GET (`MetricOIDs`, `BaseOIDs`) | |
| Enterprise tables | WALK (`MetricTableOIDs`) | function, paper-size, scanner, supplies |
| ST2 status frame | GET `1.2.2.1.1.1.4.1` (epPrtDeviceStatus) | Same `@BDC ST2` frame as remote-mode `st`; no remote-mode command needed |

## Page totals

| Metric key | OID | EDA meaning |
|------------|-----|-------------|
| `total_pages` | `1.3.6.1.2.1.43.10.2.1.4.1.1` (prtMarkerLifeCount) | Total pages |
| `mono_pages` | `1.2.2.27.1.1.3.1.1` | epPrtMarkerTotalPrinted (B&W) |
| `color_pages` | `1.2.2.27.1.1.4.1.1` | Color pages |
| `process_color_for_mono_pages` | `1.2.2.27.1.1.6.1.1` | Composite-black pages. **Not** mono |
| fallback total | `1.2.2.27.1.1.33.1.1`, `.30.1.1` | Not read by EDA; used only if prtMarkerLifeCount is missing |
| `single_pass_color_mode_pages` | `1.2.2.5.1.1.56.1.1` / `.119.1.1` | When present |

Fallback order: prtMarkerLifeCount → legacy 33/30 → ST2 0x36 → mono+color → function-table sum.

## Function counters — `1.2.2.27.6.1.<col>.1.1.<idx>` (walk)

| Col | Meaning |
|-----|---------|
| 2 | Function type: 1 = print, 2 = copy, 3 = fax, 0 = other |
| 3 | Unit; EDA accepts only 3 (pages). **Not** a B&W count |
| 4 | Total |
| 5 | Color (mono = total − color) |

Keys: `print_pages`, `print_color_pages`, `print_mono_pages`, and the same for
`copy_`, `fax_`, `other_`. Rows are matched by the type column, not by index.

## Paper sizes and duplex — `1.2.2.6.1.1.<col>.1.<row>` (walk)

| Col | Meaning |
|-----|---------|
| 2 | Paper size name (e.g. `A4/Letter`) |
| 3 | Unit; a row is valid only if non-empty and col 4 is numeric |
| 4 | Total |
| 5 | Color |
| 7 | Simplex |
| 8 | Null counter |
| 9 | Color simplex |

Per row: `paper_<size>_pages`, `_color_pages`, `_mono_pages`, `_duplex_pages`.

Simplex/duplex (EDA logic):
- If scalar `1.2.2.27.1.1.10.1.1` (simplex total) exists:
  - simplex = s, duplex = total − s
  - with `.11.1.1` (color simplex) on color devices: simplex color = sc,
    simplex mono = s − sc, duplex color = color − sc, duplex mono = duplex − duplex color
- Otherwise sum the paper-size rows the same way.

Keys: `simplex_pages`, `duplex_pages`, `simplex_mono_pages`,
`simplex_color_pages`, `duplex_mono_pages`, `duplex_color_pages`.

## Scanner — `1.4.2.6.1.1.<col>.3.<row>` (walk)

| Col | Meaning |
|-----|---------|
| 2 | Source: 3 = flatbed, 4 = ADF, 5 = sheet-feed, 6 = check scanner |
| 4 | Total |
| 5 | Two-sided (one-sided = total − two-sided) |
| 7 | Color (mono = total − color) |
| 9 | Unit: 1 = sheets, 3/empty = pages |
| 12 | ADF roller remaining (%) (13/15 = residue count / counter type) |
| 14 | ADF roller replacements |

Keys: `scan_count` (sum of rows), `scan_color_pages`, `scan_mono_pages`,
`scan_<source>_pages|sheets`, `scan_<source>_color_*`, `scan_<source>_mono_*`,
`scan_<source>_duplex_*`, `supply_adf_roller`, `scan_adf_roller_replacements`.

## Supplies

Inks come from the standard Printer-MIB (`43.11`). The maintenance box is
**only** exposed in Epson's supplies table.

`1.2.2.28.1.1.<col>.1.<idx>` (walk):

| Col | Meaning |
|-----|---------|
| 2 | Level / remaining % |
| 3 | Replacement count (−2 = n/a) |
| 4 | Used/changed flag |
| 5, 18 | Description (e.g. `Maintenance Cartridge C9345`) |
| 6 | Genuine flag |
| 9 | ID |
| 17 | First install date |
| 21 | Type: 1002 = ink, 1003 = maintenance box |

Keys: `supply_<description>` (level), `maintenance_box_replacements`.

> `1.2.2.2.1.1` is the **firmware/version table** (col 2 = value, col 3 = name:
> Main FW, Serial No, Network FW, FW Package, PS, PCL), not an ink table.

## Other scalars

| Key | OID |
|-----|-----|
| `jams` | `1.2.2.36.1.1.2.1` |
| `power_on_count` | `1.2.2.3.1.1.17.1` |
| `wake_from_standby_count` | `1.2.2.3.1.1.18.1` |
| identity | `1.2.2.1.1.1.{1..5}.1` = 1284 ID, model, series, ST2 status, serial |
| firmware | `1.1.3.1.21.1.1.2.<n>` (col 3 = name) |

## ST2 frame (epPrtDeviceStatus)

The frame has a `@BDC ST2\r\n` header, then a 16-bit little-endian length, then TLV fields:

| Field | Meaning |
|-------|---------|
| 0x01 | Status (4 = idle) |
| 0x02 | Error |
| 0x04 | Warnings |
| 0x0d | Maintenance box remaining % per box; `u` = used in an incompatible printer |
| 0x0f | Inks. See naming rule below |
| 0x28 | Printer info 2; byte 1 bit 0 = CISS |
| 0x36 | Paper counts: int32 LE; [4:8] all, [8:12] color, [12:16] mono; `FFFFFFFF` = unsupported |
| 0x37 | Maintenance box state |
| 0x40 | Serial |
| 0x46 | Maintenance box replacement count |

Field 0x0f layout: byte 0 = bytes per entry, then `[code][all-in-one][level]` per ink. A level above 100 means unknown.

EDA ink naming rule (code < 64 uses the code table only; otherwise the
all-in-one byte is used):
- Code table:
  - 1 black, 3 cyan, 4 magenta, 5 yellow
  - 6 light cyan, 7 light magenta, 8 light yellow, 9 dark yellow
  - 10 light black, 11 matte black, 12 red, 13 violet, 14 gloss optimizer, 15 light-light black
  - 16 orange, 17 green, 18 white, 20 metallic silver, 22 middle light black
  - 25 fluorescent pink, 26 fluorescent yellow
- All-in-one table:
  - 0 black, 1 cyan, 2 magenta, 3 yellow
  - 4 light cyan, 5 light magenta, 6 light yellow, 7 dark yellow
  - 8 light black, 9 red, 10 violet, 11 gloss optimizer
  - 15 white, 17 metallic silver

ST2 values are used as fallbacks:
- Inks only when the standard supplies table is empty.
- Maintenance box only when the supplies table lacks it.
- Mono/color/total only when the SNMP counters are missing.

## Verified example (ST-C8090, matches EDA UI)

| Counter | Value |
|---------|-------|
| Total / mono / color | 7037 / 4513 / 2524 |
| Print (total / color / mono) | 4091 / 2440 / 1651 |
| Copy (total / color / mono) | 2913 / 54 / 2859 |
| Other (total / color / mono) | 33 / 30 / 3 |
| Simplex / duplex | 6794 / 243 |
| Simplex color / simplex mono | 2516 / 4278 |
| Duplex color / duplex mono | 8 / 235 |
| Scans: flatbed (total / color) | 398 / 92 |
| Scans: ADF (total / color) | 680 / 465 |
| Scans: total | 1078 |
| Jams | 6 |
| Power-on | 46 |
| Maintenance box | 50 %, 2 replacements |
| Inks (K / C / M / Y) | 68 / 69 / 69 / 69 |
