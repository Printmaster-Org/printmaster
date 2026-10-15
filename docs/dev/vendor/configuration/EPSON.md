# Epson Configuration Investigation

Last investigated: 2026-10-15. Initial pass: local static inspection only.
No Epson executables were launched, printers contacted, settings changed, or
customer configuration/credential stores opened. An existing ILSpy tool listed
classes in one managed binary; no new decompiled source was saved.

This records factual interface observations for an independent implementation,
not copied vendor code. A subsequent authorized Web Config inspection is recorded
below. Configuration writes have not been tested.

## Artifact identity and evidence catalog

Installed Epson Device Admin (EDA) client: version 5.9.0.2100.
Configuration engine version: 5.9.0.2100. PE metadata distinguishes managed
assemblies from native executables; naming alone is not sufficient.

| Artifact, relative to the installation | Kind | SHA-256 |
|--------------------------------------|------|---------|
| `EDAClient.exe` | Managed | `8A2BD41AC1FBADAAA7BFB445A2ACC3A6493856F065F21A8D65B982A9724107D0` |
| `Config/ENConfig.exe` | Native | `D7D8783493D79D3A35BCE325336FD45436F123BA9EF44067428487B6255C37AD` |
| `NDENCMAPI.dll` | Native | `54C2DB7ED15DC83C83950AF4254C6383732774646E8A5F4FE934AC9307A1598D` |

Additional installed components: native `Config/ENPSSendRecv.exe` (5.9.0.2100),
native `Config/EDAComC.dll` (1.5.0.1100), and managed
`Config/EDAParser/EDAParser.exe` (1.0.0.1100).

The existing decompilation contains 2,787 files under the investigator's local
temporary `eda_decomp` directory. It is not checked into either repository and
may be removed by temporary-file cleanup. Retention and any redistribution need
separate review; these notes do not require publishing vendor source.

Evidence identifiers used below:

| ID | Location |
|----|----------|
| E1 | Managed client: `EDAClient.Common/NdSettingProcess.cs`, `NdSettingPerform.cs`, `NdSettingPerformTemplate.cs` |
| E2 | Managed client: `EDAClient.Common/NdCommunicationParameter.cs`, `NdSettingItem.cs` |
| E3 | Managed client: `EDAClient.ViewModel/ConfigDataAnalyzer.cs` |
| E4 | Managed client: `EDAClient.ViewModel/ConfigMainViewModel.cs`, `ConfigDataManager.cs`; `EDAClient.Common/EDAComClient.cs` |
| E5 | Managed client: `EDAClient.Common/BoronCustomClient.cs`, `NetworkSheetProvider.cs` |
| E6 | Installed `Config/ENConfig.exe`: printable strings only, not a traced call graph |
| E7 | Installed `Config/TemplateSet/Default`: printable strings and file headers only |
| E8 | `Config/EDAParser/EDAParser.exe`: managed class listing only |

## Configuration control path

**Static-confirmed (E1/E2):** the managed client starts `Config/ENConfig.exe`.
It supplies execution metadata through `Config/LinkParams.xml` and signals a
local named pipe, `\\.\pipe\Neodymium`. This is Windows inter-process
communication, not a printer protocol.

The link metadata includes an execution function, optional auto-login flag,
target identity/address information, and template mode/ID/log ID. Template modes
include create-from-base, create-new, edit, and apply. The template signal uses
two-byte start/end messages. Result metadata separates send/confirmation
failures and automatic/manual IP cases.

**Correction to the earlier summary:** `NdSettingItem` represents target devices
and results, not a collection of individual setting values. It does not establish
the format of the configuration transmitted to a printer.

**Implementation implication:** trace the native engine's actual device
interfaces. Do not reproduce this local IPC or require Epson's Windows binaries
as a PrintMaster runtime dependency.

## Read/write transport evidence

| Mechanism | Evidence | Status and limit |
|-----------|----------|------------------|
| SNMP operation table | E5 reads operation rows and SETs an action value, e.g. network-sheet printing | Static-confirmed; not SMTP/NTP editing |
| ENPC packet interface | E5 imports packet construction, parameter access, send, login/logout, and SNMP packet functions from `NDENCMAPI.dll` | Static-confirmed local API boundary; wire format not established |
| Native detail operations | E6 contains `execGetDetailENPC`, `execSetDetailENPC`, and `execSetDetailForSettingTemplateENPC` names | Candidate read/write paths; strings do not prove routing for any specific setting |
| SOAP configuration | E6 contains get/set SOAP-fault diagnostics | Candidate additional transport; endpoint, authentication, and field coverage unknown |
| Web Config import/export | E6 contains import/export operation names and resource paths below | Candidate paths, not tested HTTP contracts |

Web Config resource paths present in E6:

- `/PRESENTATION/ADVANCED/CONTACTIO/EXPORT` and `/IMPORT`.
- `/PRESENTATION/ADVANCED/PRESETIO/EXPORT` and `/IMPORT`.
- `/PRESENTATION/ADVANCED/USERDEFIO/EXPORT` and `/IMPORT`.

The apparent areas are contacts, presets, and user definitions. Do not treat
these as proven full-device configuration export endpoints. Methods, tokens,
payloads, model coverage, and side effects remain unknown.

No numeric SMTP/time OIDs or usable SMTP/time HTTP/SOAP contracts were resolved
in this pass. Do not infer them from the operation table.

## SMTP and time field candidates

These are **candidate** symbols, not validated normalized values or editable
device fields. No enum values, units, or numeric OIDs are assigned yet.

| Area | Evidence | Candidate identifiers or meaning |
|------|----------|----------------------------------|
| SMTP server | E6; E7 `SettingItems_DC41.ens` | `MIB.epHrMailSmtpServerAddress`, alternate `_hr1` symbol |
| SMTP port | E6; E7 `SettingItems_DC41.ens` | `MIB.epHrMailSmtpServerPortNumber`, alternate `epHrMailSmtpServerPortNum_hr1` |
| SMTP transport security | E6; E7 DC2020/DC42 setting scripts | `MIB.epHrMailSmtpSecureConnection`; setting scripts also use `epHrMailSmtpSecConnection` |
| SMTP auth/account/secret | E7 `DUIInfo_NDST.ens` | Common mail-server controls for `AuthType`, `AuthAccount`, `AuthPassword` |
| Sender/certificate checking | E7 `DUIInfo_NDST.ens` | Common mail-server controls for `SenderAddress`, `CertValidation` |
| OAuth-related mail settings | E7 `DUIInfo_NDST.ens` | `OAuth2MailService`; native engine also has SMTP federation names |
| Time synchronization | E7 `DUIInfo_NDST.ens` | Common time controls for `TimeServerEnable`, `TimeServerAddress`, `TimeServerInterval` |
| Timezone | E7 CD1/LC45 setting scripts | `TimeZonePlus`, `TimeZoneHour`, `TimeZoneMin`; `epPrtLocalDate.GetTimeZone` appears in other families |

Do not assume that `TimeServer` proves NTP rather than SNTP or another behavior.
Likewise, "secure connection" does not establish which enum means STARTTLS,
whether it is required, or which TLS versions the SMTP client supports.

**Candidate support model (E7):** DC2020/DC42 scripts reference `SetSupported`;
LC45 references timezone support; DC41 references alternate SMTP symbols.
These support model-dependent discovery/mapping, but the resource suffixes have
not been mapped to marketed models or firmware. `SetModify`/`IsModify` names
suggest dirty-field tracking, not verified partial-write semantics.

Several other SMTP classes in the managed application concern EDA's own alert
emails. They must not be used as evidence for printer SMTP configuration.

## Installed schemas are not exported templates

**Static-confirmed (E7):** the installed default resource directory contains
`DeviceInfo`, `DataTypes`, `SettingItems`, and `DUIInfo` families across `.dat`
and `.ens` files. Samples of `.dat`/`.ens` start with `ENDU`; a sampled script
also contains `ENSCRIPT`. They are binary resources, not ordinary XML documents.

Some control/field names remain printable, but full schema and execution
semantics are unresolved. This pass did not decode protected content.

**False lead (E8):** the managed `EDAParser.exe` class list includes license
parsing classes. Its name does not establish a device configuration parser.
No license internals were investigated.

## EDA template data and backup container

**Static-confirmed (E3/E4), not printer-native interoperability:**

- The client analyzes XML with root `SettingTemplate` and namespace
  `http://www.epson.com/EpsonDeviceAdmin/Template/`.
- Submitted top-level items use `ID`, `stringID`, `type`, `value`, and
  `submit="true"`; tables, datatype definitions, properties, and encryption
  markers also exist. This is a partial description, not a complete schema.
- The UI masks recognized password fields and encrypted community values.
  This does not establish secret readability or safe secret round-tripping.
- The inspected retrieval path handles `DataSaveFormat` 2 through an EDA
  decryption service, and format 3 through Base64 plus inflater decompression.
  Do not bypass encryption or assume these are identical payload formats.
- Template backup is a ZIP container. It includes `zipEDAEntry` version
  metadata, `zipEntryNameN`, optional external-file name/data pairs,
  `zipEntryInformationN` format metadata, and `zipEntryN` template payloads.
- The inspected backup writes version `5.9.0`; its import path rejects newer
  backup versions. Import consumes entries sequentially, so ordering matters.
- External files are associated with template storage. Their meaning and
  requirements are not yet established.

The configuration screen's inspected CSV export produces an **application
result log** (model, serial, status), not an editable configuration CSV.
PrintMaster's configuration CSV needs its own explicit contract.

**Unknown:** backup filename extension, full format compatibility, direct
printer import format, secret portability, and a validated round trip.
Do not label EDA backup ZIPs as printer-importable backups.

## Implementation constraints supported by the findings

- Keep native field keys, normalized policy keys, and capability evidence separate.
- Add read-only vendor providers before enabling any writer.
- Represent unreadable secrets separately from absent values. Store candidate
  credentials securely and record supplied/applied/verified state independently.
- Web UI login verification concerns admin credentials, not SMTP credentials.
- Do not apply blank or masked secrets. Do not put raw templates into telemetry,
  logs, ordinary exports, or fixtures without explicit sanitization.
- Preserve selected-field intent and table structure; prove partial-update
  behavior before attempting writes.
- Require per-service TLS evidence; web HTTPS is not evidence about SMTP TLS.
- Validate untrusted imports with size/entry limits, safe archive paths, disabled
  external XML entities, version checks, and explicit unknown-field handling.

## Next investigation gates

| Priority | Bounded check | Evidence required before implementation |
|----------|---------------|-----------------------------------------|
| 1 | Identify an authorized Epson model and firmware; inspect documented SMTP/time management surfaces | Model identity and an exact read method/path or numeric OID, with authentication requirements |
| 2 | Trace one SMTP server and one time-server read | Sanitized request/response shape, types, units, and values checked against the device UI |
| 3 | Resolve secure-connection enum and capability reporting | Required vs optional STARTTLS, implicit TLS, unsupported/unknown behavior, model evidence |
| 4 | Observe EDA acquisition during an explicitly authorized read-only workflow if static evidence is insufficient | Actual transport and sanitized field mapping; no implicit template apply |
| 5 | Examine non-sensitive, intentionally generated exports | Distinguish EDA backup, printer export, and installed resource formats; record versions |
| 6 | Propose one reversible write test for separate approval | Baseline, exact field/payload, dependency/restart effects, recovery plan, and read-back check |

These gates describe the remaining work at the end of the static pass. The
subsequent inspection below establishes concrete Web Config read paths and
form-declared write targets for one device. Actual writes, SMTP TLS negotiation,
full-settings coverage, and native-format round trips remain unverified.

## Authorized Web Config inspection: WF-C878R Series

Date: 2026-10-15. Device-reported firmware: `04.27.QO23Q1`.
The user authorized inspection of one LAN printer and supplied administrator
credentials. Addresses, credentials, session cookies, setup-token values, and
configured account/server values are intentionally omitted here.

Only authentication POSTs and page/script GETs were sent. No configuration form,
time synchronization, connection test, export generation, file upload, or import
action was submitted. No raw authenticated page bodies were saved as fixtures.

### Login and authentication evidence

**Device-observed:** the root redirects via JavaScript to
`/PRESENTATION/ADVANCED/COMMON/TOP`. Before login this page identifies the model
and has restricted navigation; after login it exposes administrator settings and
a logout control. This provides stronger evidence than HTTP 200 alone.

The actual login page is `GET /PRESENTATION/ADVANCED/PASSWORD/TOP`.
`/PRESENTATION/ADVANCED/PASSWORD` is a JavaScript redirect stub, not the form.
The form declares `POST /PRESENTATION/ADVANCED/PASSWORD/SET` with fields
`INPUTT_USERNAME`, `INPUTT_PASSWORD`, `SEL_SESSIONTYPE`, `access`,
`INPUTT_ACCSESSMETHOD` (device value `0`), and `INPUTT_DUMMY`.
The observed session uses administrator type `ADMIN` and access value `https`.
An in-memory HTTP session successfully accessed administrator navigation and the
configuration pages below. No browser proxy or Epson executable was required.

This device's HTTPS certificate was not trusted by the inspection client.
Certificate checking was bypassed only for this explicitly selected research
target; this is not a proposed fleet-wide trust policy.

### Concrete read paths and declared write targets

All paths below are relative to `/PRESENTATION/ADVANCED/`. Reads are GETs.
Write targets are resolved from observed form actions, **not tested writes**.

| Read page | Declared action | Fields observed |
|-----------|-----------------|-----------------|
| `NW_EMAILSV_BASIC/TOP` | POST `NW_EMAILSV_BASIC/SET` | SMTP/auth fields below; setup token |
| `SYS_TIME_SV/TOP` | POST `SYS_TIME_SV/POLL` | `INPUTT_USETIMESERVER`, `INPUTT_TIMESERVERADDRESS`, `INPUTT_UPDATEINTERVAL`; setup token |
| `SYS_TIME_DIF/TOP` | POST `SYS_TIME_DIF/SET` | `INPUTT_SUNMMERTIME`, `INPUTT_TIMEDIFFERENCE1`, `INPUTT_TIMEDIFFERENCE2`; setup token |
| `SYS_TIME_SET/TOP` | POST `SYS_TIME_SET/SET` | `INPUTT_YEAR`, `INPUTT_MONTH`, `INPUTT_DAY`, `INPUTT_AMPM`, `INPUTT_HOUR`, `INPUTT_MINUTE`; setup token |

Read values from HTML form controls, not just visible text. Observe selected
options, checked radios, hidden fields, and script-driven disabled controls.
Use an HTML parser in implementation, not the exploratory regex extraction.

SMTP page fields and options:

| Field | Observed meaning/options |
|-------|--------------------------|
| `INPUTD_AUTHERMET` | `OFF`, `SMTP`, `POP`, `OAUTH2` |
| `INPUTD_EMAILSERVICE` | `EXCHANGE`, `OUTLOOK`; relevant to OAuth2 |
| `INPUTT_ACCOUNT` | Authentication account |
| `INPUTT_PASS` | Password control; recoverability and unchanged-secret semantics unknown |
| `INPUTT_SENDERADDR` | Sender address |
| `INPUTT_SMTPADDR` | SMTP server |
| `INPUTT_SMTPPORT` | SMTP port |
| `INPUTD_SECURECONNECTION` | `NONE`, `SSLTLS`, `STARTTLS` |
| `INPUTR_CERTVALIDATION` | Enable/disable radio controls; submitted values not recorded |
| `INPUTT_POP3ADDR`, `INPUTT_POP3PORT` | POP-before-SMTP fields |
| `INPUTT_SETUPTOKEN` | Hidden setup token; lifetime, binding, and validation behavior not tested |

**Client-script evidence:** SMTP account validation permits 0-255 printable
ASCII characters; password validation permits 0-20. Port validation is 1-65535.
These are observed UI constraints, not verified server-side limits or limits for
every Epson model. OAuth2 selection can populate server/port/security defaults;
switching auth mode can clear or disable other fields and sign out OAuth sessions.
Do not blindly serialize every control or change authentication modes incidentally.

The time-server script labels update interval in minutes and validates 1-10080.
Disabling synchronization clears/disables address and interval controls in the
UI. Exact radio values and server-side preservation semantics remain to be checked.
The `POLL` action may have a multi-step workflow or immediate synchronization
effects; do not probe it as a read endpoint.

Time difference is a signed offset, not an IANA timezone identifier:
hour choices range from `ITEM-12` to `ITEM+13`, including distinct signed zero
choices; minutes use `ITEM00`, `ITEM15`, `ITEM30`, `ITEM45`.
The summer-time field is spelled `INPUTT_SUNMMERTIME` on the device.
Do not silently translate this into regional timezone rules.

**Capability limit:** the device exposes a STARTTLS choice. That does not prove
mandatory-upgrade behavior, working SMTP authentication, or supported TLS versions.

### Printer-native import/export entry points

**Device-observed forms, not executed:**

- `GET EXPORT/TOP`: form declares POST `EXPORT/EXPORT`, with encryption password
  and confirmation fields (`INPUTT_ENCPASSWORD`, `INPUTT_CONFENCPASSWORD`), setup
  token, and selectable settings categories.
- Categories include email server (`INPUTC_NW_EMAILSV`), time
  (`INPUTC_SYS_TIME`), networking, certificates, contacts, and administrator
  password settings. Do not export all categories indiscriminately.
- `GET IMPORT/TOP`: multipart form declares POST
  `IMPORT/IMPORT_SELECTCATEGORY_WITHIMPORT`, file field `INPUTF_File`, and
  password field `INPUTT_PASSWORD`. Script evidence indicates additional
  selection/confirmation processing; do not assume uploading is harmless staging.

This establishes a printer-native workflow distinct from EDA's backup container.
Filename extension, encryption, byte format, actual category portability, and
successful round trip remain unknown; no backup was generated or downloaded.

### Implementation readiness after this check

An independent HTTP-based Epson reader is now a concrete approach for this
device family: authenticated GET, HTML control parsing, typed normalization,
and explicit secret/unknown handling. Confirm field values against the device UI
and repeat on another model/firmware before claiming general Epson coverage.

Before enabling writers, establish successful-control serialization, fresh-token
requirements, unchanged-password behavior, response/error detection, the time
server's polling workflow, and read-back verification through an explicitly
approved reversible test. Keep machine collection sessions separate from the
interactive Web UI proxy; this inspection did not diagnose its repeat-login issue.

## Second-device comparison: ST-C8090 Series

Date: 2026-10-15. Device-reported firmware: `03.97.OJ19PB`.
One authorized administrator login succeeded; only login POST and configuration
page/script GET requests were made. No write, export, upload, or connection test
was submitted. Credentials and configured values are omitted.

The ST-C8090 exposes the same observed contracts as the WF-C878R:

- Login form at `PASSWORD/TOP`, submitting to `PASSWORD/SET`.
- SMTP read page `NW_EMAILSV_BASIC/TOP`, declaring `SET`, with the same field
  names, setup token, auth modes, OAuth2 service choices, and
  `NONE`/`SSLTLS`/`STARTTLS` options.
- SMTP client validation again uses a 255-character account limit and a
  20-character printable-ASCII password limit.
- Time-server page `SYS_TIME_SV/TOP`, declaring `POLL`, with the same fields and
  1-10080 minute interval validation.
- Time-difference page `SYS_TIME_DIF/TOP`, declaring `SET`, including the
  `INPUTT_SUNMMERTIME` spelling and signed hour/minute controls.
- Category-based export declaring `EXPORT`, and multipart import declaring
  `IMPORT_SELECTCATEGORY_WITHIMPORT` with `INPUTF_File` and `INPUTT_PASSWORD`.

Export category lists differ: do not hard-code the WF-C878R list as a universal
Epson schema. Discover available categories/fields per device and firmware.

This supports a shared Web Config reader for these two observed firmware/model
combinations. It does not establish coverage of every Epson, unchanged-secret
write behavior, actual SMTP TLS negotiation, or native-file compatibility.
