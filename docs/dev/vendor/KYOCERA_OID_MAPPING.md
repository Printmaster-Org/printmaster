# Kyocera Enterprise OID Mapping

**Source of truth**: the MIB reads and derivations performed by Kyocera Net
Device Manager (KDM): `def.dev.snmp-oid.xml` (OID names) plus the decompiled
`KDM.AccessDevice.DeviceController.Operators` (how each value is computed and
which model groups use which logic). Values were checked against real SNMP
walks of a TASKalfa 4053ci (color MFP) and an ECOSYS P2040dw (mono printer).

Implementation: `agent/scanner/vendor/kyocera.go`, constants in
`common/snmp/oids/oids.go`. Fixture tests: `agent/scanner/vendor/kyocera_test.go`,
which uses `agent/scanner/testdata/kyocera_{cs4053,p2040}_snmp.json`.

All Kyocera OIDs below are relative to `1.3.6.1.4.1.1347.`.

## How values are collected

| Kind | Query | Notes |
|------|-------|-------|
| Identity and counter scalars | batched GET (`BaseOIDs`, `MetricOIDs`) | Missing OIDs are ignored |
| Index-dependent tables | WALK (`MetricTableOIDs`) | scan life count, media counters, coverage, maintenance kits, alerts, scanner error, finisher |
| Toner | standard `prtMarkerSuppliesTable` walk | KDM uses the same table on most model groups |

KDM assigns each model to a "device group" that selects a logic variant for
each operator. Among the 409 groups, the counters below follow the majority
logic. Minority variants are handled as fallbacks.

## Identity (`kcprtGeneralTable`, `43.5.1.1.<col>.1`)

| Key | OID | KDM name | Example (4053ci) |
|-----|-----|----------|------------------|
| `model_name` | `43.5.1.1.36.1` | kcprtAbsoluteModelName | `TASKalfa 4053ci` |
| (fallback) | `43.5.1.1.1.1` | kcprtGeneralModelName | `CS 4053ci` |
| `serial_number` | `43.5.1.1.28.1` | kcprtSerialNumber | `RH28Y00024` |
| `asset_number` | `43.5.1.1.29.1` | kcprtAssetNumber | |
| `firmware_version` | `43.5.4.1.5.1.1` | kcprtFirmwareVersion (system) | `2V8_S000.002.801` |
| `engine_firmware_version` | `43.5.4.1.5.1.2` | kcprtFirmwareVersion (engine) | `2V8_1000.005.001` |

The agent's serial-number fallback (`agent/agent/snmp.go`) also uses
`43.5.1.1.28.1`.

**Do not use** (these were wrong in earlier mappings):

- `40.10.1.1.5.1` is kmHostName (`KM65E2B9`), not the serial.
- `40.1.1.1.1.1` is an integer index, not the model name.
- `42.5.4.1.2.*` is kmFirmwareInfoType (1, 2, 51 ...), not counters.
- `42.2.1.1.1.5.1` is a media dimension column.

## Page totals

| Key | Source | KDM logic |
|-----|--------|-----------|
| `page_count` / `total_pages` | `43.10.1.1.12.1.1` kcprtMarkerServiceCount | PageCount (5036) logic 2, used by 349 of 409 groups |
| fallback | `1.3.6.1.2.1.43.10.2.1.4.1.1` prtMarkerLifeCount | Logic 1. Only used when `prtMarkerCounterUnit.1.1` is 7 (impressions) or 8 (sheets) |
| last resort | sum of mono, single-color and full-color | |

On the 4053ci the service count (76064) equals printer + copier + fax
output, while prtMarkerLifeCount reads 76498.

## Function and color counters

`kmMFPColorModeCounterItem` is `42.3.1.2.1.1.<function>.<mode>`.

- function: 1 = printer, 2 = copier, 3 = unused, 4 = fax
- mode: 1 = mono, 2 = single color, 3 = full color

| Key | Derivation |
|-----|------------|
| `mono_pages` | sum of mode 1 over all functions (KDM MonoCount) |
| `full_color_pages` | sum of mode 3 (KDM FullColorCount) |
| `single_color_pages` | sum of mode 2 (KDM SingleColorCount 5038) |
| `color_pages` | full color + single color |
| `print_*`, `copy_*`, `fax_*` with `_mono_pages`, `_color_pages`, `_full_color_pages`, `_single_color_pages` | per function |

Printer-only models (KDM logic 2) expose `kmColorModeCounterItem` at
`42.2.2.1.1.3.1.<mode>` (1 = mono, 2 = mono color, 3 = full color) instead.
That table is used when the MFP table is absent.

Function totals come from `kmMFPCounterItem` `42.3.1.1.1.1.<n>`, which
override the color-mode sums:

| Key | n | Example (4053ci) |
|-----|---|------------------|
| `print_pages` | 1 | 40600 |
| `copy_pages` | 2 | 32971 |
| `fax_pages` | 4 (MFP layout), else 3 | 2493 |

## Scans

| Key | OID |
|-----|-----|
| `copy_scans` | `42.3.1.3.1.1.2` kmMFPScanCounterItem |
| `fax_scans` | `42.3.1.3.1.1.4`, else `.3` |
| `other_scans` | `42.3.1.4.1.1.1` kmMFPOtherScanCounterItem |
| `scan_count` | `46.10.1.1.5.<hrDeviceIndex>` kmscnLifeCount (walked; index 3 on the 4053ci). Falls back to the sum of the three counters above |

## Media-size counters (`kmMediaCounterTable`, `42.2.1.1.1.<col>.1.<row>`)

Columns: 2 = media name, 6 = total, 7 = mono, 8 = full color,
9 = mono color, 10–12 = coverage low/medium/high. Column 3 is the unit and
columns 4–5 are dimensions.

Rows with a non-zero total produce `paper_<size>_pages`,
`paper_<size>_mono_pages` and `paper_<size>_color_pages` (full + mono
color), for example `paper_letter_pages`. On the 4053ci, Letter + Ledger +
Legal equals the service count.

## Coverage (`42.2.4.1.1.<1|2|3>.1.<hrDeviceIndex>`)

The kmColorCoverage{Low,Medium,High}CoverageCounter values are summed across
device indexes, as KDM does for copier + printer. They produce
`coverage_low_pages`, `coverage_medium_pages` and `coverage_high_pages`.
Neither fixture device exposes them.

## Maintenance kits (`kcprtMaintenanceKitTable`, `43.24.1.1.<col>.<idx>`)

Columns: 2 = utilization, 3 = type, 4 = name, 5 = current counter,
6 = preset counter. KDM passes these through without interpreting them, so
PrintMaster reports them as raw meters:
`maintenance_kit_<name>_count`, `_preset` and `_utilization`. No
remaining-life percentage is derived until the semantics are confirmed on
hardware. Neither fixture device exposes the table.

## Status

| Source | Result |
|--------|--------|
| `43.18.2.1.2.1.<n>` kcprtAlertStateDisplay (panel text) + `.3.1.<n>` kcprtAlertStateDisplayIndex (code) | `status_messages` entry such as `Sleeping (Kyocera code 35078)` |
| Display code 50100 or 40070 | adds `Call for service` (KDM StatusOp) |
| `46.18.1.1.2.<idx>` kmscnDeviceErrorState = 2 or bit 0x08 | `Scanner cover open` |
| `43.9.1.1.8.1.1` kcprtStapler = 3 (installed) and `.9.1.1` kcprtStaplerConsumableState = -2 | `Add staples` |

`hrPrinterDetectedErrorState` (jam, door, toner, paper, service requested)
is already decoded by the generic agent logic. KDM evaluates it with the
same RFC 3805 bit order, so no Kyocera-specific handling is needed.

Other finisher values: kcprtStapler 4 = off and 5 = not present.
kcprtStaplerConsumableState -3 = staples present. `43.9.1.1.11.1.1`
kcprtPunchStatus 1 = punch installed.

## Configuration (write) mechanisms

See `DEVICE_CONFIGURATION_NOTES.md` for KDM's SNMP SET targets, the security
unlock sequence and the KM-WSDL SOAP services.
