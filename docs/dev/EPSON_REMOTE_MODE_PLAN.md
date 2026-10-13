# Epson Remote-Mode Integration Plan

This document captures the concrete steps required to land Epson remote-mode support in the agent while keeping the feature isolated behind a flag and reusing the new SNMP batching helper.

> **Update (EDA reverse-engineering):** Epson Device Admin does not use remote-mode commands for status.
> It reads the same `@BDC ST2` frame with a plain GET of epPrtDeviceStatus (`1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1`).
>
> That OID is now part of `EpsonVendor.BaseOIDs()`, so every normal query receives the ST2 frame and `Parse()` decodes it. No flag or IP is needed.
>
> `EpsonRemoteClient.GetStatus` also tries that GET first. It falls back to the 44.x `st` command only when the GET fails.
>
> Remote-mode remains useful only for EEPROM (`||`) waste-counter reads. See `docs/dev/vendor/EPSON_OID_MAPPING.md` for the full EDA-derived mapping.

## Goals
- Reuse the remote-mode command surface documented in `docs/SNMP_RESEARCH_NOTES.md` (commands `di`, `st`, `ia`, `ii`, `||`).
- Surface reliable ink, maintenance-box, and alert data in `PrinterInfo`/metrics without relying on sparse Printer-MIB leaves.
- Limit SNMP chatter by batching OIDs (already covered by `batchedGet`) and caching EEPROM ranges per device.

## Building Blocks
1. **Batching helper** (`agent/scanner/snmp_batch.go`): already available; use it whenever remote-mode helpers need scalar GETs.
2. **Vendor module hook** (`agent/scanner/vendor/epson.go`): entry point for calling the remote-mode helper from `MetricOIDs`/`Parse` whenever the feature flag is on.
3. **Learned OID cache** (`agent/agent/types.go::LearnedOIDMap`): add an `EpsonEEPROMWindows` slice so we remember which address ranges returned data.
4. **Feature flag plumbing**: add `epson_remote_mode_enabled` to agent config, default false, surfaced through settings UI.
5. **Storage + downsampling**: extend `DeviceMetricsSnapshot` to track `main_waste`, `borderless_waste`, `waste_box_3` so they flow through existing downsamplers.

## Implementation Steps
1. **Remote-mode transport helper**
   - New `agent/scanner/vendor/epson_remote.go` containing:
     - `type RemoteCommand string` constants for `di`, `st`, `ia`, `ii`, `rw`.
     - An `EpsonRemoteClient` struct that owns gosnmp client + batching helper, constructs OIDs like `remoteRoot + commandSuffix + length`.
     - Helpers for decoding ST2 frames and EEPROM payloads (reuse parsing notes).
   - Unit tests covering payload decode paths using canned frames from `epson_print_conf` samples.

2. **Feature flag detection**
   - Add config flag (`agent/config.go`) and CLI/env override to enable remote mode.
   - Thread flag through scanner pipeline (e.g., `scanner.DetectorConfig` or new `EpsonOptions`) so we only hit remote-mode OIDs when explicitly enabled.

3. **Vendor parse integration**
   - `EpsonVendor.Parse` should:
     - Call remote client to fetch `di` and `st` frames when enabled.
     - Map ST2 ink slots into canonical toner keys via `supplies.NormalizeDescription`.
     - Extract maintenance box percentages and write them to `result["main_waste"]`, `result["borderless_waste"]`, etc.
     - Capture remote-mode alert text into `result["status_messages"]` for UI surfacing.

4. **EEPROM window caching**
   - When `||` reads succeed, persist `[start,end]` ranges to `PrinterInfo.LearnedOIDs.VendorSpecificOIDs["epson_eeprom"]`.
   - During subsequent metrics runs, skip brute-force ranges and only query cached windows.

5. **Storage + metrics flow**
   - Update `agent/storage/convert.go` and `server/storage/types.go` to accept the new waste metrics keys.
   - Ensure downsamplers treat them like toner (raw → hourly → daily).

6. **Testing & validation**
   - Unit tests for `EpsonRemoteClient` parsing, vendor parse fallback when remote fails, and LearnedOID caching.
   - Integration test (behind build tag) that replays captured SNMP packets to verify the metrics snapshot contains toner + waste data.
   - Manual validation checklist: enable flag, run `dev/launch.ps1`, confirm logs show remote-mode batching and resulting metrics appear in UI.

## Outstanding Questions
- Verify whether `clusterVarbinds` needs vendor-specific batch size tuning (Epson payloads may require single-command PDUs).
- Decide whether remote-mode should run during detection or only deep-scan to limit traffic.
- Determine safe defaults for EEPROM reads to avoid hammering devices that reject remote-mode commands.

## Next Actions
1. Scaffold `agent/scanner/vendor/epson_remote.go` with transport + decoding logic.
2. Add feature flag plumbing + settings toggle.
3. Extend `EpsonVendor` parse to call the helper and populate normalized metrics.
4. Persist EEPROM windows + waste metrics into storage and add regression tests.
