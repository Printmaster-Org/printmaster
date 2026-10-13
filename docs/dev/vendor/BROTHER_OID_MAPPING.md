# Brother Enterprise OID Mapping

**Source of truth**: the SNMP reads and derivations performed by the Brother
script in Print Audit Facilities Manager ICE (`BrotherCommon`). The binary blob
layout and record ids were cross-checked against the open-source
[`brother`](https://github.com/bieniu/brother) Python library (Apache-2.0) and
its captured DCP-L3550CDW data.

Implementation: `agent/scanner/vendor/brother.go`, constants in
`common/snmp/oids/oids.go`. Tests: `agent/scanner/vendor/brother_test.go`,
which also parses `agent/scanner/testdata/brother_snmp.json` (HL-L8260CDW).

> **Validation status:** the existing HL-L8260CDW fixture ends before the
> `...5.5` subtree, so the blob and table parsers are verified only against
> reference data. Capture a walk of `1.3.6.1.4.1.2435.2.3.9.4.2.1.5` with the
> SNMP walk capture tool to validate them on a real device.

All Brother private OIDs below are relative to `1.3.6.1.4.1.2435.`.

## Detection

| Check | Notes |
|-------|-------|
| `sysObjectID` starts with `1.3.6.1.4.1.2435.` | Brother enterprise |
| `sysDescr` or model contains `brother` | Case-insensitive; this is Print Audit's check |
| `sysObjectID` under `1.3.6.1.4.1.674` with the Brother IEEE OID present | Dell-branded Brother. The Dell service tag is at `1.3.6.1.4.1.674.10898.1.1.1`. **Not implemented.** |

## How values are collected

| Kind | Query | Notes |
|------|-------|-------|
| Identity, panel, counter and maintenance blobs | batched GET (`BaseOIDs`, `MetricOIDs`) | Missing OIDs are ignored |
| Function and scan counters | WALK (`MetricTableOIDs`) | Rows are matched by a type-code column, not by index |
| Supplies and trays | standard Printer-MIB tables | Private maintenance values are merged over them |

## Identity

| Key | OID | Notes |
|-----|-----|-------|
| `serial_number` | `2.3.9.4.2.1.5.5.1.0` | Print Audit prefers it over `prtGeneralSerialNumber` |
| `firmware_version` | `2.3.9.4.2.1.5.5.17.0` | Often contains a `YY.MM.DD` date |
| `firmware_version` (fallback) | `2.4.3.1240.6.5.0` | Older models |
| `model_name` | `2.3.9.1.1.7.0` | IEEE 1284 device ID; the `MDL:` (or `MODEL:`) field |
| `status_messages` | `2.3.9.4.2.1.5.4.5.2.0` | Front-panel text. Only used when it is valid UTF-8 |

## Info blobs

`brInfoCounter` (`2.3.9.4.2.1.5.5.10.0`) and `brInfoMaintenance`
(`2.3.9.4.2.1.5.5.8.0`) are OCTET STRINGs made of records:

```
<id:1> <flag:1> <len:1> <value:len bytes, big-endian>   ... 0xFF
```

`0xFF` ends the blob. When an id repeats, the first record wins. Modern
firmware uses `flag=01`, `len=04`.

### Counters (`5.5.10.0`)

| Id | Key | Notes |
|----|-----|-------|
| `0x00` | `page_count`, `total_pages` | Overrides `prtMarkerLifeCount` |
| `0x01` | `mono_pages` | |
| `0x02` | `color_pages` | |
| `0x06` | `duplex_pages` | Only in the `brother` library; Print Audit does not read it |

Example (DCP-L3550CDW): `00 01 04 0000064b` = 1611 total, `01 ... 000002c5` =
709 mono, `02 ... 00000386` = 902 color, `06 ... 0000021a` = 538 duplex.

### Maintenance (`5.5.8.0`)

Ids are tried in the order listed. The first valid one wins.

| Key | Percent ids | Enum id | Ratio id |
|-----|-------------|---------|----------|
| `toner_black` | `0x6F`, `0x81` | `0x31` | `0xA1` |
| `toner_cyan` | `0x70`, `0x82` | `0x32` | `0xA2` |
| `toner_magenta` | `0x71`, `0x83` | `0x33` | `0xA3` |
| `toner_yellow` | `0x72`, `0x84` | `0x34` | `0xA4` |
| `drum_life` (black drum) | `0x41`, `0x80` | `0x7F` | |
| `supply_drum_cyan` | `0x79` | `0x76` | |
| `supply_drum_magenta` | `0x7A` | `0x77` | |
| `supply_drum_yellow` | `0x7B` | `0x78` | |
| `fuser_life` | `0x6A` | | |
| `transfer_belt` | `0x69` | | |

Level encodings:

- **Percent**: the value is percent × 100, so `0x0A28` (2600) means 26%.
  Non-zero values below 100 are not in this scheme and are skipped.
  For example, the DCP-L3550CDW reports `0x81 = 30`, so black uses `0x6F`.
- **Enum**: 1 OK, 2 low, 3 replace, 4 no unit. This only fills a missing
  level; 3 and 4 become 0%.
- **Ratio** (legacy, `len=02`): `byte0 / byte1`. For example, `a1 01 02 04 14`
  means 4/20, which is 20%.

Private percentages override the public `prtMarkerSuppliesTable` level, as in
Print Audit, which notes that each source can be more complete on some models.

## Function counters (`brCounterTable`, `2.3.9.4.2.1.5.5.52.21.1`)

Column 2 is a type code and column 3 is the count in the same row.

| Code | Key |
|------|-----|
| 1 | `print_pages` |
| 2 | `fax_pages` (fallback only) |
| 3 | `copy_pages` |
| 4 | `copy_color_pages` |
| 5 | `print_color_pages` |
| 6 | `fax_color_pages` |
| 7 | `print_mono_pages` |
| 8 | `fax_mono_pages` |
| 9 | `copy_mono_pages` |

Derivations (as in Print Audit):

- `fax_pages` = fax color + fax mono. If both are zero, code 2 is used.
- On mono devices without a color split, the print and copy totals are counted
  as mono. A device counts as color when it has color pages or a cyan toner.

## Scan counters (`brScanCounterTable`, `2.3.9.4.2.1.5.5.54.2.2.1`)

Column 1 is a type code and column 3 is the count.

| Code | Key |
|------|-----|
| 1 | `scan_adf_simplex_pages` |
| 2 | `scan_flatbed_pages` |
| 3 | `scan_adf_duplex_pages` |

`scan_adf_pages` = simplex + duplex. `scan_count` = the sum of all three.

## Known gaps

- Dell-branded Brother devices are not detected.
- The per-color counters (`0x12`–`0x15`) and the image counter (`0x16`) in
  `brInfoCounter` are documented by the `brother` library but are not used.
- The agent's metric merger keeps only numeric vendor keys and
  `status_messages`. Serial and firmware still come from the per-vendor serial
  OID path.
