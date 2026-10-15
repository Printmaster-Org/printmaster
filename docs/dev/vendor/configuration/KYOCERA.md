# Kyocera Command Center RX Configuration Investigation

Investigated: 2026-10-15. Scope: authorized HTTPS page inspection and one
administrator login submission per device. No configuration changes, connection
tests, exports, uploads, or imports were attempted. Credentials, cookies,
addresses, and configured values are omitted.

## Devices and evidence

| Device-reported model | UI generation evidence | Authentication result |
|-----------------------|------------------------|-----------------------|
| CS 4053ci | Command Center RX; Knockout 3.4.2; device model data reports MFP | Login POST returned 404; authenticated settings access not established |
| ECOSYS P2040dw | Command Center RX; Knockout 2.3.0 | Login POST returned 404; authenticated settings access not established |

Model names came from the server-generated `Start_Wlm.model.htm` resource, not
from guessed IP/model associations. Firmware versions were not established.

Both devices were inspected with the same user-authorized credential pair.
The login result does not establish that those credentials are invalid: the
submission contract/session setup is still unresolved. No password guessing,
default-credential attempts, or credential retries were performed.

The inspection client bypassed untrusted HTTPS certificate validation only for
the explicitly selected research devices. This is not a production trust policy.

## Landing page and session-dependent resources

**Device-observed on both models:**

- `/` and `/wlm/index.htm` expose a frameset referencing
  `/startwlm/Start_Wlm.htm?arg11=`.
- The application loads JavaScript, server-generated model resources, and
  Knockout view-model code. A generic HTML-input scraper cannot recover all
  values because many controls use `data-bind` expressions.
- Principal resources include
  `/js/jssrc/model/startwlm/Device_Config.model.htm`,
  `/js/jssrc/model/startwlm/Start_Wlm.model.htm`, and
  `/js/jssrc/view_model/startwlm/Start_Wlm.viewmodel.js`.
- The landing page supplies a query string for the main model request; preserve
  actual query requirements rather than assuming the bare resource is equivalent.

Initial independent requests on the CS 4053ci returned HTTP 500 for frame/script
resources. A session-aware request sequence using a browser-like User-Agent and
Referer retrieved them successfully. Which of those differences is necessary
was not isolated; do not prescribe a specific header as a proven fix.

## Login contract: observed form, unresolved submission

The form declares POST `/startwlm/login.cgi`. Fields include:

- `arg01_UserName`, `arg02_Password`.
- `failhtmfile`, `okhtmfile`.
- `func`, `arg03_LoginType`, `arg04_LoginFrom`, `hidden`.
- Conditional account/domain fields such as `arg05_AccountId` and
  `arg06_DomainName`.

Several hidden values are populated by JavaScript bindings rather than HTML
`value` attributes. The inspected view model defines success/failure pages,
login/logout function selection, local/domain/account login modes, and explicit
authentication-error handling.

The exploratory login submission used `func=authLogin`, local mode `_mode_off`,
the observed field names, and the start page as success/failure destination.
Both devices returned HTTP 404. Subsequent model/settings requests did not
establish login success. These submitted defaults are **not a verified login
contract** and should not be copied into an implementation as one.

## Protected settings and false success responses

The navigation exposes candidate routes:

| Area | Route |
|------|-------|
| Date/time | `/dvcset/dateset/DvcSet_DateSet_DateSet.htm` |
| Email | `/funcset/email/FuncSet_Email_EmailSet.htm` |

These are navigation evidence, not validated configuration read contracts.
The CS 4053ci date page and both ECOSYS pages returned HTTP 200 containing a
`Logout` HTML page with JavaScript redirecting the top frame to the root.

Direct date/email model-resource checks also returned this logout HTML on both
models. No protected settings values were obtained. Do not interpret this as
empty configuration, unsupported SMTP/time settings, or successful authentication.

**Implementation requirement:** distinguish actual settings/model payloads from
logout pages, login forms, redirects, and server errors even when HTTP status is
200. Recognize explicit auth outcomes before persisting observations.

The existing PrintMaster web-login adapter accepts selected HTTP status codes as
success and posts to the selected login page. The observed device form uses a
different submission endpoint and JavaScript-derived inputs. A future adapter
needs the actual contract and stronger verification; this research did not
change or diagnose all behavior of that adapter.

## Relationship to Kyocera Net Device Manager research

[Earlier configuration notes](../DEVICE_CONFIGURATION_NOTES.md) identify
candidate SNMP/SOAP configuration surfaces from Kyocera Net Device Manager.
Those are a separate possible transport. This Web UI inspection neither
validates nor disproves their availability on these two models.

No SMTP/time numeric OIDs, authenticated HTTP setting fields, write payloads,
TLS capabilities, or native-format interoperability were established here.

## Next bounded checks

1. Inspect an ordinary successful browser login on one device, with the user's
   approval. Observe the generated fields, headers, session initialization, final
   request destination, and success indicator without recording secret values.
2. Establish why the exploratory submission returned 404 before retrying
   credentials. A failed endpoint/session flow is not a password-verification test.
3. After verified login, map server-generated date/email model values to controls
   and normalize SMTP/time semantics. Check a second model independently.
4. Compare Web UI reads with authorized SNMP/SOAP alternatives; choose verified
   contracts rather than assuming a single transport across all Kyoceras.
5. Identify firmware, capabilities, setup tokens, write dependencies, and
   unchanged-secret behavior before proposing any reversible write test.

No configuration writer or credential-verification claim is ready from this pass.

## Authenticated browser inspection: CS 4053ci

The user subsequently logged in successfully in the shared browser using the
supplied credentials. The start-page view model reported login state `4` and
`funcValue=authLogout`. Thus the earlier scripted 404 was not evidence of bad
credentials. The actual successful login POST was not captured; resolving that
contract remains open. No firmware version was established.

Only menu navigation and authenticated page/model/script GETs were performed in
this session. No save, Test, Receive, synchronize, import, or export control was
activated. Model payloads were inspected for field names, not persisted as raw
fixtures. Credentials, hidden-token values, and configured settings are omitted.

### Email reads and declared operations

The authenticated email page is
`/funcset/email/FuncSet_Email_EmailSet.htm`; its server-generated read model is
`/js/jssrc/model/funcset/email/FuncSet_Email_EmailSet.model.htm`.
Model keys include `smtpServerName`, `smtpPortNum`, `smtpServerTimeout`,
`smtpAuthProtocol`, `smtpAuthAs`, `smtpUserName`, `smtpSecurity`,
`smtpPopTimeOut`, `senderAddress`, domain restrictions, and other email defaults.

Three forms share POST `/funcset/email/set.cgi` but declare different `func`
values: `setEmailSettings`, `testSMTPConnection`, and `receivePOP3`.
Only the first is the settings-save form; neither of the others is a harmless
read. The save form declares result page `/funcset/email/FuncSet_Email_Rslt.htm`.

Observed save fields include:

| Field | Meaning |
|-------|---------|
| `arg01_SMTPSrvName`, `arg02_SMTPPortNum` | Server and port |
| `arg03_SMTPTimeout` | Server timeout; units not established |
| `arg04_SMTPAuthProtocol` | UI choices: `0` On, `1` POP before SMTP, `2` Off |
| `arg05_SMTPAuthAs` | `0`/`1`/`2` POP3 User 1/2/3, `3` Other |
| `arg06_SMTPUserName` | Explicit account |
| `arg07_SMTPLoginPwd` | Conditionally enabled hidden submitted password |
| `SMTPLoginPwd` | Visible password control; not proof of readable current secret |
| `arg08_POPSMTPTimeout` | POP-before-SMTP timeout |
| `arg13_SenderAddress` | Sender |
| `hidden`, `func`, `okhtmfile`, `failhtmfile` | Request/session and workflow metadata |

The password binding enables `arg07_SMTPLoginPwd` only if nonempty or the
`blank_Password` flag is set. View-model code tracks password edits and can set
an explicit blank-password flag. Preserve the distinction between unchanged,
replace, and clear; actual server behavior is not write-verified.

The model exposes `smtpSecurity`, but this pass did not resolve a complete SMTP
security enum or its save path. Do not infer it from the connection-test form's
`arg25_SmtpOvrSSL` field.

### Date/time reads and declared operations

The authenticated page is `/dvcset/dateset/DvcSet_DateSet_DateSet.htm`; its model
is `/js/jssrc/model/dvcset/dateset/DvcSet_DateSet_DateSet.model.htm`.
It exposes `serverAddress`, `timeZoneIndex`, `summerTimeMode`, date/time
components, and timezone index/label/DST lists.

Both forms target POST `/dvcset/dateset/set.cgi`, with distinct `func` values:
`setDateTime` and `synchronizeSNTP`. Synchronization was not requested.
Save fields include `arg01_Year`, `arg02_month`, `arg03_day`, `arg04_hour`,
`arg05_minutes`, `arg06_seconds`, `arg07_DateFormat`, `arg08_TimeZone`,
`arg09_SummerTime`, and `arg10_TimeServer`, plus workflow/hidden metadata.

The page renders 130 timezone choices. Treat numeric indexes as device-specific;
retain the associated labels/DST data rather than comparing indexes across brands
or assuming they are IANA timezone identifiers.

### Network security and TLS capabilities

Authenticated page:
`/secset/nwksec/SecSet_NwkSecSet_NwkSecSet.htm`.
Read model:
`/js/jssrc/model/secset/nwksec/SecSet_NwkSecSet_NwkSecSet.model.htm`.
The form declares POST `/secset/nwksec/set.cgi`, `func=setNwkSecSettings`.

The UI explicitly separates **Serverside Settings** and **Clientside Settings**,
each displaying SSL3.0/TLS1.0, TLS1.1, and TLS1.2 choices. Controls are
`ServerTlsVersion0/1/2` and `ClientTlsVersion0/1/2`; bound model keys include
`tlsVersion`, `ClttlsVersion`, and `SSLMode`.
Hidden save fields include `arg11_SSLtlsVersion` through `arg13_SSLtlsVersion`
and `arg14_CTLtlsVersion` through `arg16_CTLtlsVersion`.

This is device-observed configurable capability evidence, not a handshake test,
an assertion that all choices are enabled, or proof that every client service
uses the same versions. SMTP-specific STARTTLS semantics and effective TLS
negotiation still need verification.

### Next step

Capture a user-driven normal login with request path, field names, non-secret
workflow defaults, response status, and authenticated-state verification. Do not
retain passwords, cookies, or hidden-token values. Repeat authenticated reads on
the ECOSYS only after its own successful login; the copier results do not prove
that lower-end model exposes identical settings.
