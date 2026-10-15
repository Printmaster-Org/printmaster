# Vendor-Specific OID Documentation

This directory contains vendor-specific SNMP OID mappings and analysis for printer manufacturers.

## Device configuration research

[Configuration research](configuration/README.md) tracks read/write interfaces,
credentials, model capabilities, and import/export formats separately from meter
collection. Start with the [Epson investigation](configuration/EPSON.md).

## Files

### EPSON_OID_MAPPING.md
Epson-specific OID mappings derived from Epson Device Admin.
- Enterprise OID base: `1.3.6.1.4.1.1248.*`
- Counters, supplies, ST2 status frame decoding
- Vendor quirks and workarounds

### SNMP_WALK_CAPTURE.md
Admin-only tool for capturing full SNMP walks from remote agents via the
server UI, used to gather raw data for new vendor mappings.

### DEVICE_CONFIGURATION_NOTES.md
Known mechanisms for remotely changing device settings, such as Epson SNMP SET
operations, ENPC and templates. Reference material for future cross-vendor
configuration editing.

### KYOCERA_OID_MAPPING.md
Kyocera-specific OID mappings derived from Kyocera Net Device Manager.
- Enterprise OID base: `1.3.6.1.4.1.1347.*`
- Counters (function, color mode, media size, coverage, scans), maintenance kits
- Panel alert codes, scanner and finisher status

### BROTHER_OID_MAPPING.md
Brother-specific OID mappings derived from Print Audit ICE's Brother script.
- Enterprise OID base: `1.3.6.1.4.1.2435.*`
- `brInfoCounter`/`brInfoMaintenance` binary blob decoding, function and scan counter tables
- IEEE 1284 model, panel text

### PRINTAUDIT_ICE_FINDINGS.md
Facts about the vendor OIDs read by Print Audit Facilities Manager ICE's per-vendor scan scripts.
- HP, Canon, Epson, Brother, Ricoh, Sharp, Toshiba, Xerox, Lexmark, Samsung, Konica Minolta, OKI and smaller vendors
- Counter derivations (function, mono/color, large/small paper, three-tier billing) and generic Printer-MIB alert logic
- Gap list against PrintMaster's vendor modules (HP needs validation)

### VENDOR_METER_ANALYSIS.md
Cross-vendor meter analysis and counter normalization strategies.
- Comparison of meter implementations across vendors
- PrintAudit category mappings
- Heuristics for counter detection

## Usage

These documents serve as reference material for:
- Adding new vendor support
- Understanding vendor-specific SNMP implementations
- Debugging vendor-specific issues
- Improving parser heuristics

## Implementation

Vendor-specific code is implemented in:
- `agent/scanner/vendor/hp.go`
- `agent/scanner/vendor/canon.go`
- `agent/scanner/vendor/brother.go`
- `agent/scanner/vendor/generic.go` (fallback)
- `agent/scanner/vendor/registry.go` (vendor detection)

## Adding New Vendors

1. Identify vendor enterprise OID from `sysObjectID`
2. Document OID mappings in this directory
3. Create vendor module in `agent/scanner/vendor/`
4. Add detection logic to `registry.go`
5. Test with real hardware
6. Update main SNMP reference: `docs/SNMP_REFERENCE.md`

---

*See*: `docs/SNMP_REFERENCE.md` for general SNMP documentation
