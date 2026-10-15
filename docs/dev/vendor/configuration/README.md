# Device Configuration Research

Research reference for independent, cross-vendor printer configuration support.
This is not a list of implemented PrintMaster features. Configuration reads,
writes, policy enforcement, and native-format interoperability require their
own implementation and validation.

The canonical documentation is the [configuration research section](https://docs.printmaster.work/development/vendor/configuration/).
Keep findings here and there in sync.

## Vendor investigations

- [Epson Device Admin](EPSON.md): configuration engine, transport clues, SMTP/time
  candidates, template structure, import/export distinctions, and next checks.
- [Kyocera Command Center RX](KYOCERA.md): copier/printer comparison,
  JavaScript-bound login fields, protected-resource behavior, and unresolved authentication.
- [Existing cross-vendor notes](../DEVICE_CONFIGURATION_NOTES.md): initial Epson
  operation-table and Kyocera SNMP/SOAP findings.
- [Meter research](../README.md): counters and supplies, separate from configuration.

## Evidence rules

Use these labels for each finding:

| Label | Meaning |
|-------|---------|
| Static-confirmed | The inspected artifact establishes this specific fact; not proof of device behavior |
| Candidate | A symbol, schema, or string suggests a field or mechanism; semantics are not verified |
| Device-verified | Observed on authorized hardware; record model, firmware, transport, and date |
| Unknown | Not established; never substitute an assumed default |

For each vendor, record the tool version and artifact identity, evidence location,
read/write mechanism, authentication, supported models, native formats, validation,
and unresolved questions. Record facts and interface descriptions, not vendor
source code or proprietary resources.

For each setting, accumulate:

- Native key and optional normalized cross-vendor key.
- Value type, enum semantics, units, constraints, and dependencies.
- Read endpoint/OID, request method, response field, and authentication.
- Write endpoint/OID, payload shape, grouping, apply/restart effects, and verification.
- Per-operation support, model/firmware evidence, and collection failures.
- Sensitivity, credential purpose, and whether a secret can actually be read.

An exported template or a UI control is not evidence that a particular device
supports a field. Keep read, write, export, import, and policy support separate.

## Implementation boundaries

- Preserve native settings independently of normalized SMTP, time, DNS, and
  security fields. Do not flatten away vendor-specific behavior.
- Keep observations, desired policy, and capability evidence separate.
- Distinguish unset, unsupported, access denied, failed, stale, and unknown values.
- A stored password is a known candidate, not proof of the current device secret.
  Record supplied/applied/verified timestamps independently. Web UI authentication
  only verifies administrator credentials, not SMTP credentials.
- Never place passwords in snapshots, ordinary exports, logs, or research fixtures.
- Model TLS capabilities by service and firmware; HTTPS support is not SMTP support.
- Separate snapshot CSV, change-import CSV, EDA templates, and printer-native backups.
- No automatic writes during research. An explicitly approved hardware write test
  must identify one setting, expected side effects, baseline, and recovery plan.

## Independent implementation

Prefer published interfaces and authorized observations. Keep provenance and do
not copy vendor implementation code, assets, or binaries into PrintMaster.
Reverse-engineering and format compatibility can raise license and legal issues;
independent implementation alone is not a legal guarantee. Review applicable
terms before deeper work, and do not bypass authentication or protected formats.
