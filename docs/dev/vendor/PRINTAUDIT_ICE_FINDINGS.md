# Print Audit ICE Vendor OID Findings

**Source**: the per-vendor SNMP scan scripts in Print Audit Facilities
Manager ICE 1.35.0 (ECi Software Solutions), script database version 51.
The scripts were extracted statically from the installer's administrative
image; nothing was installed. They are proprietary. This page records only
**facts**: which OIDs each vendor script reads, what they mean, and how the
values are derived. No script code is reproduced, and the scripts are not
stored in this repository.

Print Audit is a billing and metering product, so its scripts focus on page
counters. They split counts by function (print, copy, fax, scan), by
mono/color, by large/small paper, and into three-tier color billing levels.
Status, alerts, supplies and paper levels mostly come from the generic
Printer-MIB logic, which is documented in the first section.

Vendor scripts are selected by sysObjectID enterprise number. Key numbers:
HP 11, Epson 1248, Kyocera 1347, Brother 2435, Canon 1602, Konica Minolta
18334, Minolta 2590, Lexmark 641, Ricoh 367, Samsung 236, Sharp 2385,
Toshiba 1129, Xerox 253, OKI 2001, Muratec 4322, Panasonic 258, Pantum 40093,
Riso 24807, Memjet 38191, Axis 368. Default/Common/Discovery/Strict are
generic.

## Cross-reference with PrintMaster

| Vendor | Status in PrintMaster |
|--------|-----------------------|
| Kyocera | Matches the KDM-based module. `large_paper_*` (A3 + B4 + Ledger) was added from these findings. See `KYOCERA_OID_MAPPING.md`. |
| Epson | Matches the EDA-based module. `large_paper_*` (A3, A3/Legal, A3/Ledger, A3 Wide, US B) was added from these findings. See `EPSON_OID_MAPPING.md`. |
| HP | **Gap.** `agent/scanner/vendor/hp.go` uses `...4.2.1.4.4.7.0`/`.8.0` for color/mono and `...4.2.1.4.1.1/.2/.3.0` for scans/copies. Print Audit instead reads the legacy totals table `...4.2.1.4.1.2.{5,6,7}` (total/mono/color), the MFP media tables `...4.2.1.1.16.{1,3,7}.1.{1,3,8}` (print mono/color and copy; simplex/duplex/combined), the print-meter-equivalent table `...4.2.1.1.16.1.44` (float values, three-tier billing) and the scan counters `...4.2.1.2.2.1.{20,21,59,60,61,62,89,90}`. Validate against a real HP walk before rewriting. |
| Brother, Canon, Ricoh, Sharp, Toshiba, Xerox, Lexmark, Samsung, Konica Minolta, OKI and others | No vendor module yet; they use generic parsing. The sections below are the starting point for each module. Capture a walk with the SNMP walk capture tool first. |

The sections below are organized per script. "ect" names are Print Audit's
extended counter types: for example `ectPrintMono` is printer mono pages, and
`ectLargePaper` is large-paper pages.

## Generic Printer-MIB logic and enums



### Enumerations

#### `enumExtendedCountType`

| Name | Value |
|---|---:|
| `ectPrintMono` | 1 |
| `ectPrintColor` | 2 |
| `ectCopierMono` | 3 |
| `ectCopierColor` | 4 |
| `ectFaxPrint` | 5 |
| `ectLargePaper` | 6 |
| `ectStaplesUsed` | 7 |
| `ectDuplexedPages` | 8 |
| `ectPagesPrintedFromDeviceLocalStorageTotal` | 9 |
| `ectPagesPrintedFromDeviceLocalStorageColor` | 10 |
| `ectPagesPrintedFromDeviceLocalStorageMono` | 11 |
| `ectScanTotal` | 12 |
| `ectLifeTotal` | 13 |
| `ectLifeMono` | 14 |
| `ectLifeColor` | 15 |
| `ectPrintTotal` | 16 |
| `ectCopierTotal` | 17 |
| `ectScanMono` | 18 |
| `ectScanColor` | 19 |
| `ectCMYDevelopment` | 20 |
| `ectKDevelopment` | 21 |
| `ectSmallMono` | 22 |
| `ectSmallColor` | 23 |
| `ectLargeMono` | 24 |
| `ectLargeColor` | 25 |
| `ectBillingLevel1` | 26 |
| `ectBillingLevel2` | 27 |
| `ectBillingLevel3` | 28 |
| `ectTotal2` | 29 |
| `ectTotalMono2` | 30 |
| `ectTotalColor2` | 31 |
| `ectOther` | 32 |
| `ectScanToNetwork` | 33 |
| `ectScanToEmail` | 34 |
| `ectScanToFax` | 35 |
| `ectScanToDigital` | 36 |
| `ectSingleColorTotal` | 37 |
| `ectTwoColorTotal` | 38 |
| `ectFullColorTotal` | 39 |
| `ectLowMono` | 40 |
| `ectMidMono` | 41 |
| `ectHighMono` | 42 |
| `ectLowColor` | 43 |
| `ectMidColor` | 44 |
| `ectHighColor` | 45 |
| `ectToshibaErasableBlueTotal` | 46 |
| `ectToshibaErasableBluePrint` | 47 |
| `ectToshibaErasableBlueCopy` | 48 |
| `ectToshibaErasableBlueFax` | 49 |

#### Other enums

`enumConsumableLevelType`:

- `cltEnumeration = 1`
- `cltPercentage = 2`
- `cltAbsolute = 3`

`enumExtendedConsumableLevel`:

- `eclUnknown = 0`
- `eclOK = 1`
- `eclEmpty = 2`
- `eclLow = 3`
- `eclMid = 4`
- `eclHigh = 5`
- `eclFull = 6`

`enumExtendedConsumableColorType`:

- `cctNone = 0`
- `cctBlack = 1`
- `cctCyan = 2`
- `cctMagenta = 3`
- `cctYellow = 4`
- `cctOther = 5`

`enumExtendedConsumableType`:

- `xctBlackToner = 1`
- `xctCyanToner = 2`
- `xctMagentaToner = 3`
- `xctYellowToner = 4`
- `xctWasteToner = 5`
- `xctOtherToner = 6`
- `xctPaper = 7`
- `xctFuser = 8`
- `xctStaples = 9`
- `xctMaintenanceKit = 10`
- `xctPhotoConductor = 11`
- `xctTransferUnit = 12`
- `xctDeveloper = 13`
- `xctCoronaWire = 14`
- `xctFuserOil = 15`
- `xctFuserOilWick = 16`
- `xctFuserCleaningPad = 17`
- `xctFuserOiler = 18`
- `xctCleanerUnit = 19`
- `xctDrumKit = 20`
- `xctBelt = 21`
- `xctDocumentFeeder = 22`
- `xctOtherConsumable = 23`

`enumExtendedConsumableClass`:

- `xccOther = 1`
- `xccIsEmptied = 3`
- `xccIsFilled = 4`

`enumPrinterDetectedErrorState`:

- `pdesServiceRequested = 1`
- `pdesOffline = 2`
- `pdesJammed = 4`
- `pdesDoorOpen = 8`
- `pdesNoToner = 16`
- `pdesLowToner = 32`
- `pdesNoPaper = 64`
- `pdesLowPaper = 128`

`enumSubUnitStatus`:

- `susAvailableIdle = 0`
- `susUnavailableOnRequest = 1`
- `susAvailableAndStandby = 2`
- `susUnavailableBecauseBroken = 3`
- `susAvailableActive = 4`
- `susUnknown = 5`
- `susAvailableBusy = 6`
- `susNonCriticalAlerts = 8`
- `susCriticalAlerts = 16`
- `susOfflineIntended = 32`
- `susStateTransitionInProgress = 64`

`enumExtendedAlertTrainingLevel`:

- `eatlOther = 1`
- `eatlUnknown = 2`
- `eatlUntrained = 3`
- `eatlTrained = 4`
- `eatlFieldService = 5`
- `eatlManagement = 6`

`enumExtendedAlertSeverityLevel`:

- `easlOther = 1`
- `easlCritical = 3`
- `easlWarning = 4`

`enumExtendedAlertGroup`:

- `eagOther = 1`
- `eagHostResourcesMibStorageTable = 3`
- `eagHostResourcesMibDeviceTable = 4`
- `eagGeneralPrinter = 5`
- `eagCover = 6`
- `eagLocalization = 7`
- `eagInput = 8`
- `eagOutput = 9`
- `eagMarker = 10`
- `eagMarkerSupplies = 11`
- `eagMarkerColorant = 12`
- `eagMediaPath = 13`
- `eagChannel = 14`
- `eagInterpreter = 15`
- `eagConsoleDisplayBuffer = 16`
- `eagConsoleLights = 17`

`enumOptionalConfigFeatures`:

- `losfToshibaAlternateTotals = 1`
- `losfKonicaAlternateSerialNumbers = 2`

#### Alert-code enum

`enumExtendedAlertCode`:

| Code | Meaning |
|---:|---|
| 1 | Other |
| 2 | Unknown |
| 3 | Cover open |
| 4 | Cover closed |
| 5 | Interlock open |
| 6 | Interlock closed |
| 7 | Configuration change |
| 8 | Jam |
| 501 | Door open |
| 502 | Door closed |
| 503 | Power up |
| 504 | Power down |
| 801 | Input media tray missing |
| 802 | Input media size change |
| 803 | Input media weight change |
| 804 | Input media type change |
| 805 | Input media color change |
| 806 | Input media form/parts change |
| 807 | Input media supply low |
| 808 | Input media supply empty |
| 901 | Output media tray missing |
| 902 | Output media tray almost full |
| 903 | Output media tray full |
| 1001 | Marker fuser under-temperature |
| 1002 | Marker fuser over-temperature |
| 1101 | Marker toner empty |
| 1102 | Marker ink empty |
| 1103 | Marker print ribbon empty |
| 1104 | Marker toner almost empty |
| 1105 | Marker ink almost empty |
| 1106 | Marker print ribbon almost empty |
| 1107 | Waste-toner receptacle almost full |
| 1108 | Waste-ink receptacle almost full |
| 1109 | Waste-toner receptacle full |
| 1110 | Waste-ink receptacle full |
| 1111 | OPC life almost over |
| 1112 | OPC life over |
| 1113 | Developer almost empty |
| 1114 | Developer empty |
| 1301 | Media-path tray missing |
| 1302 | Media-path tray almost full |
| 1303 | Media-path tray full |
| 1501 | Interpreter memory increase |
| 1502 | Interpreter memory decrease |
| 1503 | Interpreter cartridge added |
| 1504 | Interpreter cartridge deleted |
| 1505 | Interpreter resource added |
| 1506 | Interpreter resource deleted |
| 1507 | Interpreter resource unavailable |

### OIDs read and result destinations

#### Device identity, status, and general information

From the Default script:

| OID | Meaning | Result |
|---|---|---|
| `1.3.6.1.2.1.1.1` | `sysDescr` | Header device name / fallback `DeviceName` |
| `1.3.6.1.2.1.1.5` | `sysName` | Friendly-name fallback |
| `1.3.6.1.2.1.1.6` | `sysLocation` | `Location` |
| `1.3.6.1.2.1.25.3.2.1.2` | `hrDeviceType` table | Finds printer device instance |
| `1.3.6.1.2.1.25.3.1.5` | Printer device-type value | Selects rows classified as printers |
| `1.3.6.1.2.1.25.3.2.1.3.<instance>` | `hrDeviceDescr` | Host-table device name |
| `1.3.6.1.2.1.25.3.5.1.2` | `hrPrinterDetectedErrorState` | First byte becomes `Status` |
| `1.3.6.1.2.1.43.8.2.1.14` | Output-device manufacturer | `DeviceVendor`, defaulting to `OTHER` |
| `1.3.6.1.2.1.43.8.2.1.16` | Printer friendly name | `FriendlyName` |
| `1.3.6.1.2.1.43.5.1.1.16` | Printer name | `FriendlyName` |
| `1.3.6.1.2.1.43.5.1.1.17` | Printer serial number | `SerialNumber` |
| `1.3.6.1.4.1.2699.1.2.1.2.1.1.3` | PWG IEEE printer designation | Parses manufacturer and description into vendor/name |
| `1.3.6.1.2.1.25.1.1` | `hrSystemUptime` | `UpTime` |
| `1.3.6.1.2.1.1.3` | `sysUpTime` fallback | `UpTime` |

#### Marker counters

`1.3.6.1.2.1.43.10.2` is walked as the marker table.

Columns used:

| OID | Meaning | Result |
|---|---|---|
| `1.3.6.1.2.1.43.10.2.1.3.<instance>` | Marker type | Selects sheet (`8`) or impression (`7`) marker |
| `1.3.6.1.2.1.43.10.2.1.4.<instance>` | `prtMarkerLifeCount` | `LifeCount` |
| `1.3.6.1.2.1.43.10.2.1.6.<instance>` | Process colorant count | Determines color capability |
| `1.3.6.1.2.1.43.10.2.1.7.<instance>` | Spot colorant count | Color fallback when process count is zero/unavailable |

The first valid sheet/impression marker is used. `LifeCountUnitType` is set to the marker type, and `LifeCountUnits` is initially taken from `prtMarkerAddressabilityUnit` (`1.3.6.1.2.1.43.10.2.1.4` is used by the helper as the life-count lookup; the script’s marker-unit lookup is tied to the marker instance).

#### Marker supplies and colorants

`1.3.6.1.2.1.43.11.1` is walked as `prtMarkerSuppliesTable`.

Columns used:

| OID | Meaning | Result |
|---|---|---|
| `1.3.6.1.2.1.43.11.1.1.3.<instance>` | Colorant index | Joins supply to colorant table |
| `1.3.6.1.2.1.43.11.1.1.4.<instance>` | Supply class | Stored as supply class |
| `1.3.6.1.2.1.43.11.1.1.5.<instance>` | Supply type | Maps to consumable type |
| `1.3.6.1.2.1.43.11.1.1.6.<instance>` | Supply description | Supply name and color fallback |
| `1.3.6.1.2.1.43.11.1.1.7.<instance>` | Supply level unit type | Detects percentage reporting |
| `1.3.6.1.2.1.43.11.1.1.8.<instance>` | Maximum supply level | Consumable max/raw max |
| `1.3.6.1.2.1.43.11.1.1.9.<instance>` | Current supply level | Consumable level/raw current |

`1.3.6.1.2.1.43.12.1` is walked as `prtMarkerColorantTable`.

Column used:

- `1.3.6.1.2.1.43.12.1.1.4.<instance>` — colorant value/name.

Colorant strings are trimmed, uppercased, and matched for `BLACK`, `CYAN`, `MAGENTA`, or `YELLOW`; other nonempty values become `cctOther`.

#### Alerts

`1.3.6.1.2.1.43.18.1.1` is walked as `prtAlertTable`.

Columns used:

| OID | Meaning | Result field |
|---|---|---|
| `1.3.6.1.2.1.43.18.1.1.2.<instance>` | Alert severity | `Alerts[n].Severity` |
| `1.3.6.1.2.1.43.18.1.1.3.<instance>` | Alert training level | `Alerts[n].TrainingLevel` |
| `1.3.6.1.2.1.43.18.1.1.4.<instance>` | Alert group | `Alerts[n].Group` |
| `1.3.6.1.2.1.43.18.1.1.7.<instance>` | Alert code | `Alerts[n].Code` |
| `1.3.6.1.2.1.43.18.1.1.8.<instance>` | Alert description | `Alerts[n].Description` |

#### Paper/input levels

`1.3.6.1.2.1.43.8.2.1` is walked as the input table.

Columns used:

| OID | Meaning | Result |
|---|---|---|
| `1.3.6.1.2.1.43.8.2.1.2.<instance>` | Input type | Selects removable/nonremovable trays (`3`, `4`) |
| `1.3.6.1.2.1.43.8.2.1.8.<instance>` | Capacity unit | Requires sheets (`8`) |
| `1.3.6.1.2.1.43.8.2.1.9.<instance>` | Maximum capacity | Paper max level |
| `1.3.6.1.2.1.43.8.2.1.10.<instance>` | Current level | Paper current level |
| `1.3.6.1.2.1.43.8.2.1.11.<instance>` | Input status | Consumable `Status` |
| `1.3.6.1.2.1.43.8.2.1.13.<instance>` | Input description | Paper consumable name |

There is a source typo in the maximum-capacity lookup: the code uses `1.3.6.1.2.43.8.2.1.9...`, omitting `.2.1` after `1.3.6.1.2`. This is distinct from the correct OID used elsewhere.

#### LCD and miscellaneous

- `1.3.6.1.2.1.43.16.5.1.2` — walked as the printer LCD/display table; used to populate LCD-related result data.
- `1.3.6.1.2.1.43.5.1.1.17` — serial number.
- `1.3.6.1.2.1.43.5.1.1.16` — friendly printer name.

### Generic counter and consumable derivation

#### Life count

`DefaultScan.GetLifeCount` walks `1.3.6.1.2.1.43.10.2` and searches `prtMarkerType`:

- Marker type `8` means sheet.
- Marker type `7` means impression.
- The first matching marker is selected.
- Only the first marker is supported because the engine is treated as unable to represent multiple marker/supply sets.
- `prtMarkerLifeCount` is read for that marker and assigned to `LifeCount`.
- Process color count is preferred; spot color count is used if process color count is zero or absent.
- More than one color marks the device as color.
- Supply processing can also mark a device as color when cyan, magenta, or yellow toner is found.

No generic derivation of `pinsResults.SubCounters[enumExtendedCountType...]` occurs in these four files for the marker life count; the generic result is `LifeCount`. Vendor scripts elsewhere populate the extended subcounter values.

#### Supplies

Supply types are mapped as follows:

- RFC types `3`, `5`, `6`, `12`, `13`, `21` → other toner.
- `4`, `8`, `14` → waste toner.
- `9` → photo conductor.
- `10` → developer.
- `11` → fuser oil.
- `15` → fuser.
- `16` → corona wire.
- `17` → fuser-oil wick.
- `18` → cleaner unit.
- `19` → fuser-cleaning pad.
- `20` → transfer unit.
- `22` → fuser oiler.
- Unmapped types → other consumable.
- Custom mappings can override the default mapping.

If `MaxLevel > 0` and `CurrentLevel >= 0`, the level is stored as a percentage. Otherwise:

- Current `-3` → enumerated OK.
- Current `0` → percentage level `0`.
- Other values → enumerated unknown.
- If maximum is missing, it becomes `-2`.
- If maximum is unknown and current is between `0` and `100`, unit type `19` (percentage) causes maximum to be treated as `100`.
- Missing supply class defaults to `xccIsEmptied`.
- A color-specific toner type is assigned only once per color; subsequent matching supplies remain other toner.

`ConvertOKTonersToLow` converts an enumerated toner level from OK to Low when raw maximum is positive. This handles devices that report a percentage until the level becomes unavailable and then report an OK-style sentinel.

#### Paper

Only removable or nonremovable trays are accepted:

- Input type `3` or `4`.
- Capacity unit `8` means sheets.
- Positive maximum and nonnegative current → percentage.
- Current `-3` → enumerated OK.
- Current `-2` or `-1` → enumerated unknown.
- Other values → absolute level.

#### Fallbacks and quirks

- Host printer description is preferred for the device name; `sysDescr` is used if the host description is absent or equals `PRINTER`.
- Vendor comes from the first output-device manufacturer; blank becomes `OTHER`.
- Friendly name falls back from the printer name to `sysName`.
- Uptime first uses `hrSystemUptime`, then `sysUpTime`.
- Status is obtained as binary data and only the first byte is retained.
- Supply and marker tables are processed by row/instance matching rather than issuing individual SNMP GETs for every derived field.
- The generic supply scan contains a typo: it tests `enumExtendedConsumableType.sctMagentaToner`, while the defined enum member is `xctMagentaToner`; therefore that particular generic magenta-color test does not match the defined enum.

### Alerts and status

#### Status

`DefaultScan.GetStatus` reads:

- `1.3.6.1.2.1.25.3.5.1.2` — `hrPrinterDetectedErrorState`.

The binary response is converted to the numeric value of its first byte and stored in `pinsScannedDevice.Status`. An empty response produces `0`.

The corresponding bit definitions in `enumPrinterDetectedErrorState` are:

| Bit value | Meaning |
|---:|---|
| 1 | Service requested |
| 2 | Offline |
| 4 | Jammed |
| 8 | Door open |
| 16 | No toner |
| 32 | Low toner |
| 64 | No paper |
| 128 | Low paper |

The four scripts do not show additional bitwise decoding into separate status fields; they retain the first-byte numeric status.

#### Alerts

`DefaultScan.GetAlerts` walks `prtAlertTable` at:

- `1.3.6.1.2.1.43.18.1.1`

It creates one alert record per returned row and independently populates severity, training level, group, code, and description by matching table instances. It does not translate the numeric alert code into prose at runtime; the meanings are represented by `enumExtendedAlertCode`.

### Discovery and vendor selection

#### Generic printer classification

`DefaultScan.GetPrinterDeviceInstance` walks:

- `1.3.6.1.2.1.25.3.2.1.2` — `hrDeviceType`.

It selects the first row whose value equals:

- `1.3.6.1.2.1.25.3.1.5` — printer device type.

The selected instance is appended to:

- `1.3.6.1.2.1.25.3.2.1.3` — `hrDeviceDescr`.

#### Vendor-specific discovery

`DiscoverDeviceScript` checks in this order and returns the associated script key on the first successful match:

1. Toshiba — GET `1.3.6.1.4.1.1129.2.3.50.1.2.3.1.2`; key `TOSHIBAESTUDIO`.
2. Dell black-and-white laser — GET `1.3.6.1.4.1.674.10898.100.2.1.2.1.2`; key `DELLBWLASER`.
3. Dell-branded Brother — only when vendor ID is `1.3.6.1.4.1.674`; exact GET `1.3.6.1.4.1.2435.2.3.9.1.1.7.0`; key `BROTHER`.
4. Xerox — GET `1.3.6.1.4.1.253.8.53`; key `XEROX`.
5. Lexmark — GET `1.3.6.1.4.1.641.1.1.1`; key `LEXMARK`.
6. Samsung — calls `SamsungCommon.DetectMIB()`; key `SAMSUNG`.
7. Riso — GET `1.3.6.1.4.1.24807.1`; key `RISO`.
8. DataMax — GET `1.3.6.1.4.1.10917.1`; key `DATAMAX`.
9. HP — exact GET `1.3.6.1.4.1.11.2.3.9.1.1.7.0`; key `HP`.

For HP, the returned IEEE identifier is parsed for `MFG:`. The manufacturer must case-insensitively equal:

- `HP`
- `HEWLETT PACKARD`
- `HEWLETT-PACKARD`

The script intentionally checks HP last because other devices may expose portions of the HP MIB.

Discovery does not directly use `sysObjectID` in the shown code. The vendor identifier passed to `DiscoverDeviceScript` is used only for the Dell-branded Brother conditional.

## HP


The script is HP Lua scanner version 2.90. HP private OIDs use enterprise root `1.3.6.1.4.1.11`. Direct private-MIB reads use an exact `.0` instance first, then fall back to GETNEXT; table reads use SNMP walks. HP private strings are generally binary/octet-prefixed and are converted by removing the two-byte prefix when detected.

### OIDs read and their destinations

#### Identity and device metadata

| OID | Meaning | Destination |
|---|---|---|
| `1.3.6.1.4.1.11.2.3.9.1.1.7` | IEEE 1284 device ID; parsed for `MDL:<model>;` or `MODEL:<model>;`; also parsed for vendor | `DeviceName`, `DeviceVendor` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.2` | HP private model/device name | `DeviceName` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.3` | HP private serial number | `SerialNumber`; possibly `DeprecatedSerialNumber` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.5` | Firmware date | `FirmwareDate` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.6` | Firmware version | `Firmware` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.1` | HP model SKU | `Sku` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.10` | Friendly device name | `FriendlyName` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.3.11` | HP private location | `Location`, only if the standard location is empty |
| `1.3.6.1.2.1.1.5` | SNMP system name / hostname | `HostName` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.1.2` | Sleep-mode setting; value `2` means enabled | `SleepMode` (`1` when source is `2`, otherwise `0`) |

MAC address, uptime, LCD, status, standard alerts, standard supply levels, paper levels, and standard serial/model reads are delegated to `DefaultScan`; their OIDs are not present in this file.

#### Life, print, copy, and fax counters

| OID | Meaning | Destination/handling |
|---|---|---|
| `1.3.6.1.2.1.43.10.2.1.4.1.1` | Public printer life counter fallback | `LifeCount`, only when `DefaultScan.GetLifeCount` leaves it `-1` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.5` | Legacy total pages | `LifeCount` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.6` | Legacy monochrome total | `ectLifeMono` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.7` | Legacy color total | `ColorCount`; sets `IsColor` when greater than zero |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.3.7.1.32` | Legacy fax print count, old modem location | `ectFaxPrint`, only if not already populated |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.3.7.2.11` | Legacy fax print count, alternate location | `ectFaxPrint`, fallback |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.63` | Copies printed | `ectCopierTotal`, only if no copier total was obtained elsewhere |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.8` | MFP combined monochrome print media table | `ectPrintMono`; table values are accumulated |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.3.1.8` | MFP combined color print media table | `ectPrintColor`; table values are accumulated |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.1` | MFP monochrome simplex print table | Used to derive `ectPrintMono` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.3.1.1` | MFP color simplex print table | Used to derive `ectPrintColor` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.3` | MFP monochrome duplex print table | Used to derive `ectPrintMono` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.3.1.3` | MFP color duplex print table | Used to derive `ectPrintColor` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.4.2` | MFP duplex multiplier | Duplex counts are multiplied by this value; zero is replaced with `2` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.7.1.8` | Combined MFP copy count table | `ectCopierTotal` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.7.1.1` | MFP copy simplex table | Used to derive `ectCopierTotal` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.7.1.3` | MFP copy duplex table | Used to derive `ectCopierTotal` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.44` | Legacy duplex page count | `ectDuplexedPages` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.45` | Alternate legacy duplex page count | `ectDuplexedPages` fallback |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.2.22` | Another legacy duplex counter path | `ectDuplexedPages` fallback path |

MFP media tables are rejected when they contain only one row because the authors consider that value invalid on older HP devices.

#### Print-meter-equivalent table

| OID | Meaning | Destination |
|---|---|---|
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.44` | Base of HP print-meter-equivalent table; walked with category/source indexes | `ectTotalMono2`, `ectTotalColor2`, `ectTotal2`, `ectFaxPrint`, `ectLowColor`, `ectBillingLevel1`, `ectBillingLevel2`, `ectBillingLevel3` |

The table instance format is `.category.source.0`.

Categories documented in the script:

- `1`: monochrome
- `2`: color
- `3`: composite black
- `4`: general-office accent
- `5`: general-office color
- `6`: professional accent
- `7`: professional color
- `8`: presentation accent color
- `9`: presentation color
- `10`: maximum accent color
- `11`: maximum color
- `12`: TBD
- `13`: total

Sources:

- `1`: print
- `2`: copy
- `3`: fax

Only categories `1`, `2`, and `13` are used for primary totals. Categories `4`, `6`, `8`, and `10` feed tier one; category `5` feeds tier two; categories `7`, `9`, and `11` feed tier three.

#### Scan counters

| OID | Meaning | Destination/handling |
|---|---|---|
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.2.1.1` | MFP scan simplex table | Used for MFP scan total |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.2.1.3` | MFP scan duplex table | Added with multiplier `2` to MFP scan total |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.20` | Accessory scan total | Candidate accessory scan total |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.59` | ADF simplex scan count | Added to ADF total |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.60` | ADF duplex scan count | Added to ADF total if simplex read succeeds |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.21` | Flatbed scan count | Added to accessory scan total |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.44` | ADF scanner page count fallback | Candidate scan total when accessory total is unavailable |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.74` | Flatbed scanner page count fallback | Added to OID `.44` result |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.61` | Scan-to-copy count | Used to subtract copy scans |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.62` | Scan-to-send count | Added to `ectScanToDigital` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.89` | Scan-to-folder count | Added to `ectScanToDigital` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.2.2.1.90` | Scan-to-fax count | `ectScanToFax`; also added to `ectScanToDigital` |

Final scan selection:

- `ectScanTotal` receives the larger of the MFP scan total and accessory scan total.
- If available, accessory total minus scan-to-copy is used for `ectScanToDigital` when it exceeds the accumulated explicit digital-scan counters.

#### Large/A3 paper counters

| OID | Meaning | Destination |
|---|---|---|
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.8.5.1.27` | Overall A3/large-paper total | `ectLargePaper` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.1.27` | A3 monochrome simplex | Used for `ectLargeMono` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.1.3.27` | A3 monochrome duplex | Doubled and added for `ectLargeMono` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.3.1.1.27` | A3 color simplex | Used for `ectLargeColor` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.3.1.3.27` | A3 color duplex | Doubled and added for `ectLargeColor` |

The script comment states that A3 counters may contain a multiplier. It calculates:

- A3 mono = mono simplex + `2 ×` mono duplex
- A3 color = color simplex + `2 ×` color duplex

#### Supplies, maintenance, and consumable metadata

| OID | Meaning | Destination/handling |
|---|---|---|
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.3` | Maintenance-kit maximum capacity | Used as supply `MaxLevel` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.4` | Maintenance-kit current level | Used as supply `CurrentLevel` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.1.1` | Consumable-status table base | Walked to identify consumable rows |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.1.1.2.<row>` | Consumable manufacture date | `Consumables.ManufactureDate` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.1.1.3.<row>` | Consumable serial number | `Consumables.Serial` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.1.1.8.<row>` | Consumable installation date | `Consumables.InstallDate` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.1.1.10.<row>` through `.15.<row>` | Consumable page-count fields | Summed into `Consumables.PagesOutput` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.11` | Consumable pages-printed table | Updates `Consumables.PagesOutput` by matching row indexes |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.7` | Consumable status table | Walked, then each row is read as binary and stored as `Consumables.OemStatus` |
| `1.3.6.1.4.1.11.2.3.9.4.2.1.4.1.10.7.<row>` | Individual consumable status value | Encoded as hexadecimal `0x...` in `OemStatus` |

The script also maps HP supply type `23` to the document-feeder consumable type because some HP devices incorrectly report “Document Feeder Kit” as an RFC supply.

### Derivation and model-family logic

#### General ordering

The scan sequence is:

1. Vendor.
2. Standard/default device name.
3. HP private device name.
4. Firmware, SKU, friendly name, counters, location, serial, LCD, uptime, status, sleep mode.
5. Supplies and maintenance data.
6. Paper levels.
7. Alerts.

Private serial is preferred over the standard serial: the standard serial is temporarily cleared, the default scan is performed, and the original private serial is restored if it was found.

#### Model/vendor detection

- The IEEE 1284 device ID is searched for `MDL:` and then `MODEL:`.
- If absent, the private HP model OID is used.
- The device name is used for family-specific behavior.
- Rebranded OKI or Brother devices are detected by vendor/device identification. OKI devices use `OKICommon.GetSupplies`; Brother devices use `BrotherCommon.GetConsumables`.
- If a rebranded device name still contains `HP`, it is replaced with the default MIB device name.
- Blank device names fall back to the default name.
- The vendor name is stripped from the displayed device name unless scanning was cancelled.
- Officejet models are explicitly detected by `"Officejet"` in `DeviceName`. Their MFP print counters are ignored because the authors state that those counters can contain totals instead of monochrome counts. For Officejet devices, `ectPrintMono` is set from `ectLifeMono`, and `ectPrintColor` from `ColorCount`.

#### Counter fallback order

- Life count starts with `DefaultScan.GetLifeCount`.
- If it remains `-1`, the script reads public OID `1.3.6.1.2.1.43.10.2.1.4.1.1`.
- Legacy private totals are then available through `mblnGetLegacyTotals`.
- MFP print counters are preferred over legacy counters unless the device is an Officejet.
- Combined MFP print counters are tried first.
- If absent, separate simplex/duplex MFP counters are used.
- Duplex values are multiplied by the device-reported multiplier; zero multiplier becomes `2`.
- MFP copy counters try combined counts first, then simplex plus twice duplex.
- If a calculated print/copy total exceeds the reported `LifeCount`, it replaces `LifeCount`. The authors specifically mention a LaserJet 9065 MFP whose life count was wrong compared with summed media-size counters.
- Duplex-page counters prefer MFP-derived values and then use legacy paths if still absent.
- Scan total uses the larger MFP or accessory total.
- Large-paper counters are only fetched if the overall large-paper counter was not already obtained.
- Fax counters from print-meter-equivalent data are preferred; legacy fax paths only fill missing values.

#### Print-meter-equivalent units and rounding

Values from `1.3.6.1.4.1.11.2.3.9.4.2.1.1.16.1.44` are binary floating-point values converted with `cHexToFloat`.

- Mono, color, fax, and billing values are rounded to the nearest integer using floor(value + 0.5).
- `ectTotal2` is normally mono plus color.
- If category 13 overall total exists and is larger, it replaces the mono-plus-color total.
- Fax total uses category 13/source 3 when present; otherwise mono-fax plus color-fax.
- Tier one accent values become `ectLowColor`.
- Tier-one billing level adds monochrome total to tier-one accent.
- Tier two and tier three are stored independently.

#### Comments and quirks recorded by the authors

- HP private strings usually have two leading symbol-set bytes, commonly `"0115"` for Roman; strings must often be read as binary.
- Some HP 8000 devices at Toshiba do not support GETNEXT correctly for zero-based instances, so exact `.0` is attempted first.
- Some older HP MFP tables contain one invalid row; one-row tables are discarded.
- Officejet MFP counters can be wrong and are ignored.
- A LaserJet 9065 MFP reportedly has an incorrect life count.
- A3 counters may require duplex multiplication to match the embedded web page.
- Scanner page-count fallback was observed on the M475dw.
- HP may report a document-feeder kit as an RFC supply type incorrectly.
- Later FutureSmart devices expose consumable page counts outside the ordinary consumable-status table.

### Identity, status, alerts, supplies, and maintenance

#### Identity

Collected or attempted by this file:

- Vendor: IEEE 1284 device ID and HP default fallback.
- Model/device name: IEEE 1284 `MDL`/`MODEL`, then HP private model OID, then default scan.
- SKU: HP private SKU OID.
- Friendly name: HP private friendly-name OID.
- Serial: HP private serial, overriding the public/default serial when present.
- Firmware version and firmware date.
- Hostname: SNMP `sysName`, OID `1.3.6.1.2.1.1.5`.
- Location: standard scan first, HP private location second.
- MAC address: not directly read in this file; delegated to default scanning if supported.
- Uptime and LCD: delegated to `DefaultScan`.

#### Status and alerts

- Device status is delegated to `DefaultScan.GetStatus`.
- Alerts are delegated to `DefaultScan.GetAlerts`.
- No alert-specific HP private OID is directly read in this file.
- Sleep mode is directly read from `1.3.6.1.4.1.11.2.3.9.4.2.1.1.1.2`.

#### Supplies and maintenance

- Standard supply levels are obtained through `DefaultScan.GetSupplyLevels`.
- HP private consumable metadata includes serial, manufacture date, installation date, pages output, and OEM status.
- OEM status is preserved as a hexadecimal byte string.
- Maintenance-kit capacity and current level are represented as a synthetic `Maintenance Kit` supply with:
  - consume type `xctMaintenanceKit`
  - no RFC supply type
  - current and maximum levels from the two maintenance OIDs
- Standard paper levels are delegated to `DefaultScan.GetPaperLevels`.

### Non-SNMP access

No HTTP, EWS, PJL, socket, URL, or other non-SNMP access is present in the HP script. The script only invokes the SNMP engine and shared scanning APIs.

## Canon


### Explicit OIDs

| OID | Meaning / use |
|---|---|
| `1.3.6.1.4.1.1602.1.2.1.4` | Canon private-MIB serial number → `SerialNumber`. |
| `1.3.6.1.4.1.1602.1.11.1.4` | Walk of stationary Canon counter values. Individual values are read under `.4.1.4.<counter-number>`. |
| `1.3.6.1.4.1.1602.1.11.1.4.1.4.101` | Total count; may replace `LifeCount` when greater. |
| `...102` | Canon “Total 2” → `ectTotal2`; if absent, calculated. |
| `...103` | Large-paper total → `ectLargePaper`. |
| `...108` | Mono life count → `ectLifeMono`; used with 101 to derive `ColorCount`. |
| `...110` | Large mono-color count; used only to calculate `ectTotalColor2`. |
| `...111` | Small mono-color count; used only to calculate `ectTotalColor2`. |
| `...112` | Large mono → `ectLargeMono`. |
| `...113` | Small mono → `ectSmallMono`. |
| `...122` | Large color → `ectLargeColor`. |
| `...123` | Small color → `ectSmallColor`. |
| `...201` | Copier total → `ectCopierTotal`. |
| `...232` | Copier color → `ectCopierColor`. |
| `...301` | Print total → `ectPrintTotal`. |
| `...324` | Print color → `ectPrintColor`. |
| `...501` | Scan total → `ectScanTotal`. |
| `...701` | Fax total → `ectFaxPrint`. |
| `1.3.6.1.4.1.1602.1.11.1.3.1.2` | Walk of enabled Canon counter IDs. |
| `...1.3.1.2.106` | Tests whether counter 106 is enabled. |
| `...1.3.1.2.124` | Tests whether counter 124 is enabled. |
| `1.3.6.1.4.1.1602.1.11.2.1.1` | Optional “slim” counter table. |
| `1.3.6.1.4.1.1602.1.11.2.1.1.2` | Slim-counter descriptions; matched by text. |
| `1.3.6.1.4.1.1602.1.11.2.1.1.3<instance>` | Slim-counter values. |
| `1.3.6.1.4.1.1602.1.1.1.1` | Canon device name/vendor. |
| `1.3.6.1.4.1.1602.1.3.3.1.1.2.1.1` | Canon host name. |
| `1.3.6.1.4.1.1602.1.1.3.1` | Sleep-mode state; value `"1"` maps to sleep mode `0`, anything else to `1`. |

DefaultScan calls additionally obtain standard identity, location, firmware/status, supplies, alerts, and life count, but their OIDs are defined outside this file.

### Derivation and ordering

1. Device name is read from the Canon private MIB; fallback is the default device-name/vendor logic. Vendor `"OTHER"` is changed to `"CANON"`.
2. Life count is obtained through `DefaultScan.GetLifeCount`.
3. Canon stationary counters are preferred.
4. Print mono is print total minus print color; if color is unavailable, print total is treated as mono.
5. Copier mono is copier total minus copier color; if color is unavailable, copier total is treated as mono.
6. `ColorCount` is counter 101 minus counter 108. If that result is zero and supplies did not identify a color device, it is changed to `-1`.
7. Counter 101 replaces the default life count when larger. The author explicitly cites machines where 101 was significantly higher (`ICE-279`).
8. `ectTotalMono2` is large mono counted twice plus small mono—described by the author as equivalent to Canon counter 109.
9. `ectTotalColor2` is large color counted twice plus small color. If counter 106 is detected as enabled, mono-color totals (110 counted twice plus 111) are subtracted.
10. Counter 102 is used for `ectTotal2`; absent 102, it is calculated as `ectTotalMono2 + 2×large-color + small-color`.
11. The slim table maps descriptions:
   - `Total 1`
   - `Total (Black/Large)`
   - `Total (Black/Small)`
   - `Total (Full Color + Single Color/Large)`
   - `Total (Full Color + Single Color/Small)`
   - `Copy (Total 1)`
   - `Copy (Full Color + Single Color/Large)`
   - `Copy (Full Color + Single Color/Small)`
   - `Print (Total 1)`
   - `Print (Full Color + Single Color/Large)`
   - `Print (Full Color + Single Color/Small)`
   - `Scan (Total 1)`
12. Slim counters are attempted only if the stationary counter table was unavailable. The author says this path was temporarily commented out in the source’s intended deployment because accuracy needed more validation.
13. Slim counters set print/copy/scan, large/small mono/color, and `ColorCount`; the source comments note that slim counters lack sufficient information for separate mono-color accounting. The calculations intentionally mirror the regular “2” counters, including the source’s repeated large-counter additions.

### Identity, status, supplies, and non-SNMP access

- Identity: Canon private-MIB name, host name, serial; default friendly name/location and fallback identity fields.
- Firmware, status, LCD, uptime, paper levels, supplies, and alerts come through `DefaultScan`.
- If the serial remains unavailable, the script optionally calls `Plugins.GetSerialNumber`; this is the only non-SNMP access in the file.
- Supplies are read before Canon counter processing because supply detection influences the mono/color decision.

---

## Epson


### Explicit OIDs

| OID | Meaning / use |
|---|---|
| `1.3.6.1.4.1.1248.1.2.2.1.1.1.2` | Epson device name → `DeviceName`; vendor hardcoded to Epson. |
| `1.3.6.1.4.1.1248.1.2.2.1.1.1.5` | Epson serial → `SerialNumber`. |
| `1.3.6.1.4.1.1248.1.1.3.1.1.33` | Host name → `HostName`. |
| `1.3.6.1.4.1.1248.1.2.2.27.1.1.4.1` | Color-count table walk; first row/value becomes `ColorCount`. |
| `...27.1.1.4.1.1` | Explicit first color-count entry check. |
| `1.3.6.1.4.1.1248.1.2.2.6.1.1` | Paper-size counter table walk. |
| `...6.1.1.4` | Per-size total count; summed for overall total. |
| `...6.1.1.7` | Per-size simplex count; total minus simplex → `ectDuplexedPages`. |
| `...6.1.1.15<instance>` | Per-size supported flag; only value `1` is processed. |
| `...6.1.1.2<instance>` | Per-size paper-name string. |
| `...6.1.1.4<instance>` | Per-size total. |
| `...6.1.1.5<instance>` | Per-size color count. |
| `1.3.6.1.4.1.1248.1.2.2.27.6.1` | Print/copy/fax marker table walk. |
| `...27.6.1.4.1.1.1` | Print from computer/mobile total. |
| `...27.6.1.4.1.1.4` | Print from storage-device total. |
| `...27.6.1.5.1.1.1` | Color print from computer/mobile. |
| `...27.6.1.5.1.1.4` | Color print from storage device. |
| `...27.6.1.4.1.1.2` | Total copies. |
| `...27.6.1.5.1.1.2` | Color copies. |
| `...27.6.1.4.1.1.3` | Fax total. |
| `1.3.6.1.4.1.1248.1.2.2.2.1.1.3.1` | Firmware-description table walk. |
| `...2.1.1.3.1<instance>` | Firmware descriptor strings. |
| `1.3.6.1.4.1.1248.1.2.2.2.1.1.2.1` | Firmware-value table walk. |
| `...2.1.1.2.1<instance>` | Firmware values, concatenated with periods. |

### Derivation and ordering

- Default life count is read first.
- Color count is read from the Epson color table and only stored when positive.
- Paper-size totals ignore nil and negative values; the source specifically says some Epsons report `-2`.
- Duplexed pages are total minus simplex.
- Supported paper sizes are classified:
  - **Large:** `A3`, `A3/Legal`, `A3/Ledger`, `A3 Wide`, `US B`.
  - **Small:** every other supported name.
- For large sizes, total, mono (`total − color`), and color feed `ectLargePaper`, `ectLargeMono`, and `ectLargeColor`.
- For small sizes, mono and color feed `ectSmallMono` and `ectSmallColor`; small total is not separately assigned.
- Print total is computer/mobile print plus storage-device print.
- Print color is the corresponding sum; print mono is print total minus print color.
- Copy mono is copy total minus copy color.
- Copy total/color/mono feed the copier counters.
- Fax total feeds `ectFaxPrint`.
- Firmware descriptor preference is: `Firmware`, then `FW Package`, then `Main FW`, then combined `Network FW` + `Printer FW` as `NetPrinterFW`. Values for combined firmware are joined with `"."`.
- The source comments that paper-size breakdown repeats OID reads deliberately rather than replacing the earlier method, to avoid breaking obscure duplex behavior.

### Identity, model detection, status, supplies, and non-SNMP access

- The script calls `KonicaCommon.DetectMIB`; if detected, host name and counters are delegated to KonicaCommon rather than Epson-specific logic.
- Serial falls back to `DefaultScan.GetSerialNumber` if Epson’s private OID is empty.
- Friendly name, location, LCD, uptime, status, paper levels, supplies, and alerts use `DefaultScan`.
- No non-SNMP access is present in this file.

---

## Brother

**Sources:**  
### Explicit OIDs

| OID | Meaning / use |
|---|---|
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.10.0` | Binary life-count information blob. IDs `0`, `1`, `2` mean total life, mono life, and color life. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.52.21.1.2` | Print/copy counter instance-description table. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.52.21.1.3` | Print/copy counter values. |
| `...52.21.1.2` instance `4` | Color copy. |
| Instance `9` | Mono/B&W copy. |
| Instance `3` | Total copy. |
| Instance `5` | Color print. |
| Instance `7` | Mono/B&W print. |
| Instance `1` | Total print. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.54.2.2.1.1` | Scan counter instance-description table. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.54.2.2.1.3` | Scan counter values. |
| Scan instance `1` | ADF simplex scans. |
| Scan instance `3` | ADF duplex scans. |
| Scan instance `2` | Flatbed scans. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.52.21.1.2` / `.3` | Also reused for fax instances and values. |
| Fax instance `6` | Color fax. |
| Fax instance `8` | B&W fax. |
| Fax instance `2` | Total-fax fallback if color plus B&W are unavailable. |
| `1.3.6.1.4.1.2435.2.4.3.1240.6.5` | Firmware → `Firmware`. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.1` | Brother serial → `SerialNumber`. |
| `1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.8.0` | Binary maintenance/consumable-information blob. |
| `1.3.6.1.4.1.2435.2.3.9.1.1.7.0` | IEEE identity string; parses manufacturer and model. |
| `1.3.6.1.2.1.1.1.0` | `sysDescr`; Brother detection checks for case-insensitive `"brother"`. |
| `1.3.6.1.2.1.1.5` | Standard host name → `HostName`. |
| `1.3.6.1.4.1.674.10898.1.1.1` | Dell-branded Brother service tag, retrieved as binary → `ServiceTag`. |

### Binary blob format

Brother private blobs are read with `executebinaryexact`. Each record is:

1. ID byte.
2. Flag byte.
3. Length byte.
4. That many value bytes, interpreted as a hexadecimal integer.

`0xFF` terminates the blob.

### Life and counter derivation

- `LifeCount` is first obtained through `DefaultScan.GetLifeCount`, then Brother’s blob is consulted.
- Blob IDs:
  - `0x00` total life → `LifeCount`.
  - `0x01` mono life → `ectLifeMono`.
  - `0x02` color life → `ectLifeColor`, `ColorCount`, and sets `IsColor=true`.
- Values are only assigned when positive.
- Print/copy counters are optional; missing optional tables do not fail the scan.
- On mono-only devices, total print is also assigned to `ectPrintMono` when no color-print value exists; total copy is similarly assigned to `ectCopierMono`.

### Supplies and maintenance

The private maintenance translation table tries IDs in listed order, using the first usable match:

- Black toner: `0x6F`, `0x81`, `0x31` level type 1, `0xA1` level type 2.
- Cyan toner: `0x70`, `0x82`, `0x32` type 1, `0xA2` type 2.
- Magenta toner: `0x71`, `0x83`, `0x33` type 1, `0xA3` type 2.
- Yellow toner: `0x72`, `0x84`, `0x34` type 1, `0xA4` type 2.
- Black drum: `0x41`, `0x80`, `0x7F` type 1.
- Cyan drum: `0x79`, `0x76` type 1.
- Magenta drum: `0x7A`, `0x77` type 1.
- Yellow drum: `0x7B`, `0x78` type 1.
- Fuser: `0x6A`.
- Belt: `0x69`, mapped to supply type `15`.

Consumable types are black/cyan/magenta/yellow toner, drum kits, fuser, and belt. Level enum values are:

- `1` = OK.
- `2` = Low.
- `3` = Replace.
- `4` = No unit.

For percentage records, the source expects values such as `3400` for 34%, divides by 100, and accepts only zero or values at least 100. Type-1 records map the four enum values above. Type-2 records are used as an alternate level representation.

Public `DefaultScan` supplies are read first when necessary; valid private Brother supplies are then merged over them. The author explicitly notes that Brother exposes multiple overlapping toner fields and that private/public MIBs can each be more complete.

### Identity, detection, status, and ordering

- Brother detection:
  1. Check `sysDescr` for `"brother"`.
  2. If not detected, check whether the IEEE identity OID exists.
- Device name/vendor normally come from `DefaultScan.GetDeviceName`; the common code also supports parsing `MFG:`/`MANUFACTURER:` and `MDL:`/`MODEL:` from the IEEE identity string.
- A product OID beginning with `1.3.6.1.4.1.674` is treated as a Dell-branded Brother. Vendor becomes Dell and the Dell service tag is collected.
- Serial order: Brother private serial first, then default standard serial if empty.
- Firmware is read from the Brother private firmware OID; the source comments that it commonly includes a `YY.MM.DD` firmware date but does not parse it.
- Host name uses `1.3.6.1.2.1.1.5`.
- LCD, uptime, status, friendly name, paper levels, and alerts use `DefaultScan`.
- No non-SNMP network access is present; the optional plugin path occurs only in Canon.

## Ricoh

### OIDs and result mappings

| OID | Meaning / use | Result |
|---|---|---|
| `1.3.6.1.4.1.367.3.2.1.1.1.1` | Ricoh private-MIB probe; also used by `DetectMIB` | Detects Ricoh MIB |
| `1.3.6.1.4.1.367.3.2.1.1.1.1` | Private device/vendor value | Device name/vendor fallback source |
| `1.3.6.1.4.1.367.3.2.1.1.1.7` | Private host-name value | `HostName` |
| `1.3.6.1.4.1.367.3.2.1.2.1.4` | Private serial number | `SerialNumber` |
| `1.3.6.1.4.1.367.3.2.1.2.20.1.2.1.1` | Firmware string | `Firmware` |
| `1.3.6.1.4.1.367.3.2.1.2.19` | Ricoh counter table walk | Life count and subcounters |
| `1.3.6.1.4.1.367.3.2.1.2.19.1` | Counter-table total/life value | `LifeCount` fallback |
| `1.3.6.1.4.1.367.3.2.1.2.19.2.0` | Header print total | `ectPrintMono` final fallback |
| `1.3.6.1.4.1.367.3.2.1.2.19.3` | Header fax total | Fax subcounter fallback |
| `1.3.6.1.4.1.367.3.2.1.2.19.4.0` | Header copier total | `ectCopierMono` fallback |
| `1.3.6.1.4.1.367.3.2.1.2.19.5.1.2` | Counter-type column; queried with `getinstances` | Locates counter rows by numeric ID |
| `1.3.6.1.4.1.367.3.2.1.2.19.5.1.9` + instance/row index | Counter-value column | Value for the located counter |
| `1.3.6.1.4.1.367.3.2.1.2.24` | Ricoh toner/special-consumables table | Toner and consumables |
| `1.3.6.1.4.1.367.3.2.1.2.24.1.1.3` + instance | Toner name | Supply name |
| `1.3.6.1.4.1.367.3.2.1.2.24.1.1.4` | Toner-type column | Enumerates toner rows |
| `1.3.6.1.4.1.367.3.2.1.2.24.1.1.5` + instance/row index | Toner level | Supply current level |
| `1.3.6.1.4.1.367.3.2.1.7.3.5.1.1.3` | Private model/device-name value | `DeviceName` fallback/source |

Counter IDs queried through the counter-type OID:

- **Life:** `10` (generic counter); `800` (equal-to-panel total).
- **Printer:** `821` mono ETP, `822` single-color ETP, `823` two-color ETP, `820` full-color ETP; `644` single-color UC breakdown, `643` two-color UC breakdown, `642` full-color UC breakdown; `607` mono UC, `606` color UC; `401` mono legacy, `402` single-color legacy, `403` full-color legacy.
- **Copier:** `811` mono ETP, `812` single-color ETP, `813` two-color ETP, `810` full-color ETP; `621` single-color UC breakdown, `622` two-color UC breakdown, `623` full-color UC breakdown; `605` mono UC, `604` color UC; legacy copier counters are used after these, with the mono legacy value at the corresponding legacy copier entry.
- **Fax:** `830` BW/new, `630` BW/deprecated; `831` single-color/new, `631` single-color/deprecated.
- **Development:** `603` black/K, `602` CMY.
- **Total color override:** `608`.
- **Staples:** `702`.
- **Duplex:** `841` new, `701` deprecated.
- **Local storage:** `650` mono, `651` first color, `652` second color, `653` full color.
- **Three-tier billing:** `854`, `855`, `856`.
- **Large paper:** `840` new, `700` deprecated; `672` A3/DLT BW, `671` A3/DLT color.
- **Scanning:** `873` color, `874` mono.

### Derivation and ordering

- Device name: private Ricoh value → Samsung MIB for rebadged BP320N/SP3300 devices → public/default scan.
- Vendor: private value → public/default vendor → forced to `RICOH` when blank or `RFG`.
- Serial: Ricoh private serial → Samsung serial if Samsung MIB detected → default/public serial.
- Firmware: Ricoh firmware → Samsung firmware if blank.
- Life count: counter ID `800`, then ID `10`, then public/default life count.
- Printer and copier counters prefer ETP values. ETP values are rejected if all present values are zero. The script then tries UC breakdowns, UC totals, legacy counters, and finally the header print total.
- Color printer/copy totals are sums of single-, two-, and full-color values.
- `ColorCount` is initially printer-color plus copier-color. ID `608` replaces that calculated value only when its reported value is greater.
- Fax totals sum BW and single-color fax counts, preferring new IDs and falling back to deprecated IDs.
- Development counters are page counts; the comment notes CMY development is normally approximately three times color life count because three color toners are consumed.
- Local-storage color counts from IDs `651`–`653` are summed; local-storage total is color plus mono.
- Scan total is mono plus color.
- Large-paper BW/color breakdowns are retained only if at least one is nonzero.
- Units are page/copy counts unless otherwise represented as a supply level.
- The source comments explicitly note that Ricoh counter rows are not sequential, requiring type lookup followed by value lookup.

### Identity, status, supplies, alerts

- Status, LCD, uptime, location, paper levels, friendly name, and alerts are delegated to `DefaultScan`.
- Ricoh toner levels come from the private toner table because the author says public-MIB toner values are invalid.
- Multiple toner bottles of the same color are retained; all but the last are converted to “Other Toner” so row indexes remain stable for alert triggering/clearing.
- Waste toner is obtained through `DefaultScan.GetWasteToner` after the Ricoh toner table succeeds.
- No non-SNMP access is present in these files.

---

## Sharp

### OIDs and result mappings

| OID | Meaning / use | Result |
|---|---|---|
| `1.3.6.1.4.1.2385.1.1.19.2.1.3` | Sharp counter-value table | Counter values |
| `1.3.6.1.4.1.2385.1.1.19.2.1.4` | Sharp counter-description table | Counter names/lookup |
| `1.3.6.1.4.1.2385.1.1.19.2.1.4` + matching instance | Description lookup | Selects counter |
| `1.3.6.1.4.1.2385.1.1.19.2.1.3` + matching instance | Value lookup | Selected subcounter |
| `1.3.6.1.4.1.2385.1.1.3.2.1` | Private device table | Device-table walk |
| `1.3.6.1.4.1.2385.1.1.3.2.1.1` + instance | Sharp device name | `DeviceName` |
| `1.3.6.1.4.1.2385.1.1.3.2.1.2` + instance | Sharp serial | `SerialNumber` |
| `1.3.6.1.4.1.2385.2.1.3.2.1.4.13501.1301` | Sharp host name | `HostName` |
| `1.3.6.1.4.1.2385.1.1.1.2.1.2` | Sharp firmware candidate | Not used; firmware routine is deliberately disabled |

Sharp uses descriptions rather than numeric counter IDs. The complete description table includes:

- `Prints`
- `Black & White Print Count`
- `Full Color Print Count`
- `Two Color Print Count`
- `Single Color Print Count`
- `Color Total Print Count`
- `Duplex`
- `Copies`
- `Black & White Copy Count`
- `Full Color Copy Count`
- `Two Color Copy Count`
- `Single Color Copy Count`
- `Black & White Total Count`
- `Full Color Total Count`
- `Two Color Total Count`
- `Single Color Total Count`
- `Black & White Other Count`
- `Full Color Other Count`
- `Scan to HDD`
- `Black & White Scan to HDD Count`
- `Full Color Scan to HDD Count`
- `Two Color Scan to HDD Count`
- `Color Scan to HDD Count`
- `Prints (Document Filing)`
- `Total Document Filing Print Count`
- `Black & White Document Filing Print Count`
- `Full Color Document Filing Print Count`
- `Two Color Document Filing Print Count`
- `Color Document Filing Print Count`
- `Fax Send`
- `Fax Receive`
- `Scan to E-mail/FTP`
- `Black & White Scanner Count`
- `Full Color Scanner Count`
- `I-Fax Send Count`
- `I-Fax Receive Count`
- `Low Coverage Black & White Total Count`
- `Middle Coverage Black & White Total Count`
- `High Coverage Black & White Total Count`
- `Low Coverage Full Color Total Count`
- `Middle Coverage Full Color Total Count`
- `High Coverage Full Color Total Count`

The lookup retries every description containing `Color` with `Colour` for European Sharp devices.

### Derivation

- The counter table is walked first for values and then for descriptions.
- Print totals feed `ectPrintTotal`; BW print feeds `ectPrintMono` and life-mono; color total is preferred, otherwise full-, two-, and single-color values are summed.
- Copy, scan, document-filing, fax, Internet-fax, HDD, “other,” duplex, and coverage counters are mapped by the names above.
- Overall color totals are the sum of full-, two-, and single-color total counters and replace the prior calculated color count.
- On MX201D-like devices, if life count is zero, life count is calculated from overall BW plus color totals; if still zero, print total + copier total + fax-receive count is used.
- If no overall BW total exists and the unit is noncolor with zero color count, life-mono is set equal to life count.
- Sharp’s counter-table comments state that instances encode device index, counter class, and counter type; the relevant counter class is always `4`, while device index and type vary.
- If color count remains zero on a noncolor unit, it is changed to `-1`.
- The author notes that some apparent counters are present but permanently zero and that MX201D life totals can be off because “other” may not be exposed.

### Identity, status, supplies, alerts

- Device name and serial come from the private Sharp device table. If serial is blank, the script tries RicohCommon when a Ricoh MIB is detected, then the default scan.
- Sharp firmware retrieval is commented out because the author says the firmware logic is incorrect.
- If Ricoh is detected, Ricoh firmware and hostname routines are fallback sources.
- Status, LCD, uptime, paper levels, and alerts use `DefaultScan`.
- If Ricoh is detected, Ricoh private toner handling is used. Otherwise public/default supplies are used.
- Sharp toner levels are normalized with `Utility.ConvertOKTonersToLow`.
- Sharp waste toner quirks: raw `0` means empty and becomes 100%; raw `100` means full and becomes 0%; `-3` means almost full/low; `-1` means trouble/unknown. Other values become unknown. This reverses the usual RFC interpretation.
- No non-SNMP access is present.

---

## Toshiba e-Studio

### OIDs and result mappings

#### Main total-counter tables

Base table `1.3.6.1.4.1.1129.2.3.50.1.3.21.6` is probed, then these tables are walked:

| Base OID | Meaning |
|---|---|
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.2` | Overall totals |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.3` | Printer |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.4` | Copier |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.7` | Fax |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.8` | Network scan |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.9` | Copy scan |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.10` | Fax scan |
| `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.11` | List/other |

For every such table, instance suffixes are:

- `.1.1` full color
- `.1.2` two-/mono-color
- `.1.3` black/BW
- `.1.4` all/total
- `.1.5` low color
- `.1.6` middle color
- `.1.7` high color
- `.1.8` erasable blue, on newer scanning mode

The script’s comment explicitly defines these instance meanings.

Large/small tables:

- `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.207` — large paper
- `1.3.6.1.4.1.1129.2.3.50.1.3.21.6.1.208` — small paper

Older counter tables:

| OID | Meaning |
|---|---|
| `1.3.6.1.4.1.1129.2.3.50.1.3.4.1.1.4.1` | Older table family |
| `...4.1.24` | Printer mono |
| `...4.1.21` | Copier mono |
| `...4.1.23` | Other |
| `...4.1.25` | Scan |
| `1.3.6.1.4.1.1129.2.3.50.1.2.1.1.1` | Another older table family |
| `...1.1.5` | Printer mono |
| `...1.1.7` | Copier mono |
| `...1.1.14` | Other |

(The abbreviated suffixes above are appended to the full base OID shown in the table immediately preceding them.)

Identity/location/supply OIDs:

- `1.3.6.1.4.1.1129.2.3.50.1.2.4.1.4.1.1` — private location, exact GET.
- `1.3.6.1.4.1.1129.2.3.50.1.2.3.1.4` — firmware.
- `1.3.6.1.4.1.1129.2.3.50.1.2.4.1.28` — serial.
- `1.3.6.1.4.1.1129.2.3.50.1.2.3.1.2` — alternate identity/device-name value; if serial remains blank, hostname is used as serial.
- `1.3.6.1.4.1.1129.2.3.50.1.3.21.6` — newer-counter-table presence probe.
- `1.3.6.1.4.1.1129.2.3.50.1.3.4.1.1.4.1` and `1.3.6.1.4.1.1129.2.3.50.1.2.1.1.1` — older counter-table walks.
- `1.3.6.1.4.1.1129.2.3.50.1.2.4.1.3` — supply-table/private supply probe.

### Derivation

- New-table selection is controlled by `GlobalOptionalConfigFeatures[enumOptionalConfigFeatures.losfToshibaAlternateTotals]`.
- New mode includes low/middle/high color and erasable-blue counts; old mode only includes low color and treats it as the billing-tier discriminator.
- Life count deliberately ignores the reported `.1.4` total. It is calculated as BW `.1.3` plus calculated color. The author comments that the reported total was intentionally ignored because totals did not reliably align.
- For old totals:
  - `LifeCount = mono + color`.
  - `ColorCount = full color + two-color + low-color`.
  - Billing level 1 = full color + two-color.
  - Billing level 2 = low color.
  - Billing level 3 = mono.
  - Low/mid/high color coverage counters are retained when available.
- Newer printer counters feed printer mono/color and erasable-blue-print.
- Copier counters feed copier total, mono, and color (`total - mono`), plus erasable-blue-copy.
- Fax total feeds `ectFaxPrint`, plus erasable-blue-fax.
- Network, fax, and copy scans are combined:
  - scan mono totals are added;
  - scan color is `total - mono`;
  - scan total is the sum of scan sources;
  - network scan feeds `ectScanToNetwork`;
  - fax scan feeds `ectScanToFax`;
  - digital scan is fax plus network.
- List total feeds `ectOther`.
- Large-paper total feeds `ectLargePaper`; large mono is the mono component; large color is `total - mono`. Small-paper values similarly feed the small-paper subcounters.
- Fallback order is newer total table → older `.4` table → older `.21` table → older `.1` table.
- Older tables initialize life at `-1`, add printer, copier, and other counts, then add one to compensate before storing life count.
- All counts are treated as page counts.

### Identity, status, supplies, alerts

- Location: private exact value → public/default location.
- Serial: private serial → alternate Toshiba identity value → hostname if still blank.
- Firmware comes from `1.3.6.1.4.1.1129.2.3.50.1.2.3.1.4`.
- Device name, status, LCD, uptime, paper levels, friendly name, and alerts are otherwise obtained through the default scan framework.
- The source comments state that newer Toshiba devices put toner in the public MIB but violate RFC 3805: current level is already a percentage and maximum is `-2`; the script compensates by treating current as the percentage rather than calculating a ratio.
- No non-SNMP access is present.

## Xerox

### SNMP OIDs read

**Identification and detection**

- `1.3.6.1.4.1.253.8.53.3.2.1.2` — Xerox model/device name → `DeviceName`.
- `1.3.6.1.4.1.128.2.1.3.1.1` — Tektronix manufacturer presence/name; used to detect Tektronix-based devices and, when present, vendor.
- `1.3.6.1.4.1.128.2.1.3.1.2` — Tektronix device name.
- `1.3.6.1.4.1.674.10898.1.1.1` — Dell service tag, binary GET → `ServiceTag`.
- `1.3.6.1.4.1.253.8.53.3.2.1.3` — Xerox serial number → `SerialNumber`.
- `1.3.6.1.4.1.253.8.64.4.2.1.7.7.22410000` — Xerox hostname → `HostName`.
- `1.3.6.1.4.1.253.8.53` — Xerox MIB detection only.

**Counter table discovery**

- `1.3.6.1.2.1.25.3.2.1.1` — Host Resources device indexes; walked to obtain indexes.
- `1.3.6.1.2.1.25.3.2.1.2` — Host Resources device types; walked to associate indexes with device classes.
- `1.3.6.1.4.1.253.8.53.13.2.1.6` — Xerox device-detail counter table; walked, then individual values are read below.

All dynamic counter OIDs have the form:

`1.3.6.1.4.1.253.8.53.13.2.1.6.<deviceIndex>.20.<counterId>`

The device index is found by matching the Host Resources type table against these type OIDs:

- `1.3.6.1.4.1.253.8.52.2.103` — copier device.
- `1.3.6.1.2.1.25.3.1.5` — printer device.
- `1.3.6.1.4.1.253.8.52.2.104` — legacy fax.
- `1.3.6.1.4.1.253.8.52.2.118` — Internet fax.
- `1.3.6.1.4.1.253.8.52.2.119` — server fax.
- `1.3.6.1.4.1.253.8.52.2.120` — embedded fax.
- `1.3.6.1.4.1.253.8.52.2.102` — scanner.

The index lookup itself reads:

`1.3.6.1.2.1.25.3.2.1.1<rowInstance>`

### Xerox counter IDs

For a printer index:

- `.20.1` — total impressions → `LifeCount`.
- `.20.7` — black/white print impressions → `ectPrintMono`.
- `.20.29` — color print impressions → `ectPrintColor`.
- `.20.9` — duplex black/white print → added to `ectDuplexedPages`.
- `.20.31` — duplex color print → added to `ectDuplexedPages`.
- `.20.10` — large black/white impressions → `ectLargeMono`; contributes to `ectLargePaper`.
- `.20.32` — large color print → `ectLargeColor`; contributes to `ectLargePaper`.

For a copier index:

- `.20.3` — black/white copier impressions → `ectCopierMono`.
- `.20.25` — color copier impressions → `ectCopierColor`.
- `.20.5` — duplex black/white copy → `ectDuplexedPages`.
- `.20.27` — duplex color copy → `ectDuplexedPages`.
- `.20.6` — large black/white copy → `ectLargeMono`; contributes to `ectLargePaper`.
- `.20.28` — large color copy → `ectLargeColor`; contributes to `ectLargePaper`.

For fax indexes:

- `.20.14` — fax printed impressions → `ectFaxPrint`.
- `.20.13` — fax sent impressions → `ectScanToFax`.

Legacy fax uses the first matching fax type. If absent, Internet, server, and embedded fax counters are accumulated.

For scanner index:

- `.20.11` — network scans → `ectScanToNetwork`.
- `.20.12` — email scans → `ectScanToEmail`.

`ectScanToDigital` and `ectScanTotal` are calculated as network + email + fax-sent.

Total-counter overrides at the printer index:

- `.20.1` — total impressions → overrides `LifeCount`.
- `.20.33` — total color impressions → overrides `ColorCount`.
- `.20.39` — total duplex sheets → overrides `ectDuplexedPages`.
- `.20.43` — total large color impressions → `ectLargeColor`.
- `.20.44` — total large black/white impressions → `ectLargeMono`.
- `.20.47` — total large impressions → `ectLargePaper`; if absent, large mono + large color is used.

### Xerox three-tier billing

The script also reads counter IDs in the same dynamic table for detailed or standard three-tier billing. The visible code identifies color-level counters including:

- `.20.80` — color level 1 impressions → `ectBillingLevel1`.
- `.20.81` — color level 2 impressions → `ectBillingLevel2`.
- `.20.82` — color level 3 impressions → `ectBillingLevel3`.

Detailed counters are preferred; standard counters are used only if detailed counters are not found. The source comments describe these as Xerox “three tier billing counters.”

### Derivation and quirks

- Xerox first discovers indexes because Xerox changes the MIB/device identifiers between models.
- Printer/copier/fax/scanner values are all lifetime impressions unless the individual Xerox MIB defines otherwise.
- Duplex counts are accumulated across printer and copier components.
- `ColorCount` is initially print color + copier color; if zero, it becomes `-1`. A later total-color counter can replace it.
- Xerox Phaser 8400 devices invoke optional `Plugins.GetXerox8400Counters`; this is non-SNMP access through a plugin.
- If Xerox counters produce no positive life count, the script falls back to `DefaultScan.GetLifeCount`.
- Tektronix devices are treated as Xerox-family devices. Dell enterprise `674` devices are relabeled Dell and receive a Dell service-tag lookup.
- Cleaner-unit supply type `18` is mapped to the maintenance-kit consumable type.
- For Tektronix color devices, a black solid-wax consumable is copied to yellow/cyan/magenta with synthetic row indexes `4000`, `4001`, and `4002`.

**Status, alerts, supplies, firmware, uptime, LCD, paper:** delegated to `DefaultScan.GetStatus`, `DefaultScan.GetAlerts`, `DefaultScan.GetSupplyLevels`, `DefaultScan.GetUpTime`, `DefaultScan.GetLCD`, and `DefaultScan.GetPaperLevels`; their OIDs are not present in the six requested files.  

---

## Lexmark

### SNMP OIDs read

Lexmark enterprise number is `641`.

**Identification**

- `1.3.6.1.4.1.641.2.1.2.1.2` — make/model string → split at the first space into vendor and `DeviceName`.
- `1.3.6.1.4.1.641.2.1.2.1.4` — firmware → `Firmware`.
- `1.3.6.1.4.1.641.2.1.2.1.6` — binary serial number → `SerialNumber`.
- `1.3.6.1.4.1.641.1.5.7.6` — hostname → `HostName`.
- `1.3.6.1.4.1.641.1.1.1` — Lexmark MIB detection.

**Modern MPS counter table**

- `1.3.6.1.4.1.641.6.4.2.1.1` — general MPS counter table walk.
- `1.3.6.1.4.1.641.6.4.2.1.1.2` — counter type column; queried with `getinstances`.
- `1.3.6.1.4.1.641.6.4.2.1.1.3<instance>` — unit type.
- `1.3.6.1.4.1.641.6.4.2.1.1.4<instance>` — counter value.
- `1.3.6.1.4.1.641.6.4.2.2.1` — paper-size sides table walk.
- `1.3.6.1.4.1.641.6.4.2.2.1.2` — paper-size identifier.
- `1.3.6.1.4.1.641.6.4.2.2.1.6<instance>` — large-paper mono sides.
- `1.3.6.1.4.1.641.6.4.2.2.1.7<instance>` — large-paper color sides.

**Scan table**

- `1.3.6.1.4.1.641.6.4.3.1.1` — scan counter table walk.
- `1.3.6.1.4.1.641.6.4.3.1.1.2` — scan counter type.
- `1.3.6.1.4.1.641.6.4.3.1.1.4<instance>` — scan sides.
- `1.3.6.1.4.1.641.6.4.3.1.1.5<instance>` — scan sheets.

**Modern counter type IDs**

- `1` `totalPicked` → `ectTotal2`.
- `2` `totalSafe` → `LifeCount`.
- `3` `totalMonoSafe` → `ectLifeMono`.
- `4` `totalColorSafe` → `ColorCount`.
- `16` `printTotal` → `ectPrintTotal`.
- `17` `printMono` → `ectPrintMono`.
- `18` `printColor` → `ectPrintColor`.
- `32` `copyTotal` → `ectCopierTotal`.
- `33` `copyMono` → `ectCopierMono`.
- `34` `copyColor` → `ectCopierColor`.
- `48` `faxTotal` → `ectFaxPrint`.
- `49` `faxMono`; used to construct fax total.
- `50` `faxColor`; used to construct fax total.
- `64` `blankTotal`; conditionally added to mono.
- `65` `blankPrint`.
- `66` `blankCopy`.
- `67` `blankFax`.
- `80` `printerPageCount`.
- `81` `modularPageCount`.
- `90` `highlightColor` → `ectLowColor`.
- `91` `businessColor` → `ectMidColor`.
- `92` `graphicsColor` → `ectHighColor`.

Unit IDs: `4` = sides/impressions; `5` = sheets.

Scan type IDs:

- `1`, `257`, `769` — copy.
- `2`, `258`, `770` — fax.
- `3`, `259`, `771` — email.
- `4`, `260`, `772` — network.

**Legacy counters**

- `1.3.6.1.4.1.641.2.1.5` — legacy counter table walk.
- `.2.1.5.1` — total picked/total count → `ectTotal2`.
- `.2.1.5.2` — mono lifetime count.
- `.2.1.5.3` — color lifetime count → `ColorCount`.
- `.2.1.5.7` — mono copy → `ectCopierMono`.
- `.2.1.5.8` — color copy → `ectCopierColor`.
- `.2.1.5.9` — mono fax → `ectFaxPrint`.
- `.2.1.5.10` — color fax; added to mono fax.
- `.2.1.5.11`, `.12`, `.13` — legacy scan counters; summed → `ectScanTotal`.

**Supply metadata**

- `1.3.6.1.4.1.641.6.4.4.1.1.6` — supply serial-number table.
- `.1.1.10` — supply installation dates, binary.
- `.1.1.13` — supply unit type.
- `.1.1.17` — supply usage/page output.

### Derivation and quirks

- Modern MPS counters are preferred; legacy counters are used only if the modern table is absent or has no valid life total.
- The script chooses sides when available, because the author found that Lexmark’s nominally “sheets” life count is actually impressions and that sides provide the useful color/mono breakdown.
- If the life total has only sheets, all subsequent counters use sheets.
- If a total counter is missing but mono/color components exist, the total is their sum.
- Blank total is added to mono only when mono + color + blank equals `LifeCount`.
- Duplexed pages are calculated as total sides − total sheets.
- Large paper is only collected for sides and only for paper-size values `67` (A3) and `14` (Tabloid); large mono + large color becomes `ectLargePaper`.
- Scan total is the sum of all scan rows. Network and email are combined into `ectScanToNetwork`; `ectScanToDigital` is total scans − copy scans. The source comments say Lexmark web pages often label email traffic as network.
- Legacy mono print is calculated as mono total − mono copy − mono fax; color print is color total − color copy − color fax.
- Legacy firmware may omit mono count. If color and total exist, mono is estimated as total − color, clamped to zero.
- Lexmark supply unit type `4` causes usage/page count to be recorded.
- The main scan invokes `Utility.ConvertOKTonersToLow` because Lexmark can report toner level `-3` when it can no longer mechanically measure the remaining toner.

**Status, alerts, paper, LCD, uptime:** delegated to `DefaultScan`; no corresponding OIDs occur in these files.  

---

## Samsung

### SNMP OIDs read

Samsung enterprise number is `236`.

**Identification**

- `1.3.6.1.4.1.236.11.5.1.1.1.1` — device/model name → `DeviceName`.
- `1.3.6.1.4.1.236.11.5.1.1.1.2` — firmware → `Firmware`.
- `1.3.6.1.4.1.236.11.5.1.1.1.4` — serial number → `SerialNumber`.
- `1.3.6.1.2.1.1.5` — standard system hostname → `HostName`.
- `1.3.6.1.4.1.236.11.5.1.1.1` — Samsung MIB detection.
- Firmware fallback: `1.3.6.1.4.1.236.11.5.11.53.31.1.7`.

**Samsung summary counters**

- `1.3.6.1.4.1.236.11.5.11.53.11.1` — summary mono/color table.
- `.11.1.1` — color total.
- `.11.1.2` — mono total.

**Detailed counter table**

Base table: `1.3.6.1.4.1.236.11.5.11.53.11.2.1`

- `.2` — job type.
- `.3` — media size.
- `.4` — unit type.
- `.5` — duplex type.
- `.6` — color type.
- `.7` — count value.

The script walks each column separately and correlates rows by the `.x.y` instance suffix.

**Job type IDs**

- `1` print.
- `2` copy.
- `3` fax in.
- `4` fax out.
- `5` scan.
- `6` report/service pages.
- `11` digital send.
- `12` digital receive.
- `21` local storage.

**Media IDs**

- `-1` all.
- `1` small.
- `2` large.
- `20` A3.
- `19` ledger.
- `31` A3-over.

The source comments say A3, ledger, and A3-over can count as two clicks in machine totals. Large counters include IDs `2`, `20`, `19`, and `31`.

**Unit IDs**

- `7` impressions.
- `8` sheets.

**Duplex IDs**

- `-1` all.
- `1` simplex.
- `2` duplex.
- `3` duplex-single, apparently unused.

**Color IDs**

- `-1` all.
- `1` full color.
- `2` single color.
- `3` mono color.

**Supply metadata**

- `1.3.6.1.4.1.236.11.5.11.53.61.5.2.1.7` — supply serial numbers.
- `.1.10<instance>` — binary installation date.
- `.1.14<instance>` — pages output.

### Derivation and quirks

- The life count is first obtained through `DefaultScan.GetLifeCount`; its unit determines whether detailed Samsung counters use impressions (`7`) or sheets (`8`).
- Detailed print mono includes print and report mono, simplex and duplex.
- Detailed print color includes print and report full-color, simplex and duplex.
- Copier mono/color include simplex and duplex.
- Fax output is calculated from fax-in mono/color counters; fax-out is not incorporated.
- `ectDuplexedPages` accumulates duplex print, copier, and fax counts.
- Large mono/color counts are accumulated when the media size is large, A3, ledger, or A3-over.
- Summary totals and calculated detailed totals are compared; the larger value is retained.
- A known Samsung CLP620/3303 defect is handled: when summary mono equals `LifeCount` while color is nonzero, mono is recalculated from the detailed total or `LifeCount − ColorCount`.
- The source comments explicitly state that Samsung has no reliable total subcounters, so print/report components must be added.
- Unused detailed rows stop processing at the first `-1`; the source assumes no valid rows occur after that.
- Supply metadata is matched by finding the serial number in the public consumables table, not by relying on row order.
- Supply installation dates are converted from binary date values; page usage is recorded for matched supplies.

**Status and alerts:** `SamsungCommon.GetStatus` is used, while alerts are obtained through `DefaultScan.GetAlerts`.  
**Supplies:** normal levels come from `DefaultScan.GetSupplyLevels`, then Samsung serial/install-date/page-output metadata is added.  
**Paper, LCD, uptime:** delegated to `DefaultScan`.  

### Non-SNMP access

- Xerox: optional Phaser 8400 plugin (`Plugins.GetXerox8400Counters`) and Dell service-tag binary SNMP access.
- Lexmark and Samsung: no non-SNMP access is present in the requested scripts.
- The requested files call several `DefaultScan` functions, but those implementations are outside the six files; therefore their underlying OIDs cannot be identified from this source set.

## Konica Minolta

- **Identity/model**
  - `1.3.6.1.4.1.2699.1.2.1.2.1.1.3` walks the Printer Port Monitor table; `.1` is parsed as IEEE-1284-style `MANUFACTURER:...;MODEL:...;` text into `DeviceVendor` and `DeviceName`.
  - Fallback: `1.3.6.1.2.1.1.1` (`sysDescr`) is parsed as vendor/model.
  - Generic model fallback walks `1.3.6.1.2.1.25.3.2.1.2`, finds rows whose value is `1.3.6.1.2.1.25.3.1.5` (printer device type), then reads the matching `1.3.6.1.2.1.25.3.2.1.3.<index>` device description.
  - Firmware is read from `1.3.6.1.4.1.18334.1.1.1.5.5.1.1.3<instance>`.
- **Counters**
  - Walk `1.3.6.1.4.1.18334.1.1.1.5.7.2.1`:
    - `.2` → `ectLargePaper`
    - `.3` → `ectDuplexedPages`
    - `.5` → `ectScanTotal`
    - `.1` → `LifeCount`
  - Public life-count fallback/walk: `1.3.6.1.2.1.43.10.2.1.4`; specifically `.1.1` is read. The larger of this value and the private `.1` total becomes `LifeCount`.
  - Counter table walk: `1.3.6.1.4.1.18334.1.1.1.5.7.2.2`.
    - `.1.3<instance>` is color type: `1` mono, `2` four-color, `6` single-color, `8` two-color.
    - `.1.4<instance>` is function type: `1` copier, `2` printer.
    - `.1.5<instance>` is the counter value.
    - Four-color values → `ectFullColorTotal`; single-color → `ectSingleColorTotal`; two-color → `ectTwoColorTotal`.
    - Function/color combinations feed `ectCopierMono`, `ectCopierColor`, `ectPrintMono`, or `ectPrintColor`.
  - Scan-counter walk: `1.3.6.1.4.1.18334.1.1.1.5.7.2.3`.
    - `.1.2<instance>` identifies scan type.
    - `.1.7<instance>` scan-to-print total → mono print-related counter.
    - `.1.8<instance>` and `.1.14<instance>` are additional scan/print counters used by the common accumulation logic.
    - `.1.9<instance>` → `ectScanToFax` and initially `ectScanToDigital`.
    - `.1.15` through `.1.24<instance>` are accumulated into `ectScanToDigital`; `.21` and `.22` are also added to `ectScanToFax`.
  - Toner-coverage code is commented out and does not execute.
- **Status/alerts**
  - The scan workflow delegates status, supplies, paper levels, uptime, LCD, location, and alerts to `DefaultScan`; no vendor-specific alert OID is read in this file.

## Konica Minolta common

- Uses the same private MIB family, but rooted at `1.3.6.1.4.1.18334.1.1.1.5.7.2`.
- The detailed total, print/copy, scan, identity, and firmware behavior is the common implementation described above.
- Negative private counter values (`-1`) are treated as invalid; valid values are accumulated with `Utility.SafeAdd`.
- Scan types are documented in comments as combined scanner/fax (`1`), scanner (`4`), and fax (`5`).
- Identity additionally reads:
  - `1.3.6.1.4.1.18334.1.1.1.5.5.1.1.3<instance>` → firmware.
  - `1.3.6.1.4.1.18334.1.1.1.5.7.2.3.1.1` → scan-counter table instances.
- Host-name and other standard identity/status operations are delegated to the surrounding/default scan implementation.

## Minolta

- **Identity/model**
  - Walks `1.3.6.1.2.1.25.3.2.1.2`, finds printer-device rows matching `1.3.6.1.2.1.25.3.1.5`, and reads model text from `1.3.6.1.2.1.25.3.2.1.3<instance>`.
  - Vendor/model text is split at spaces; exact model lookup is used to avoid GET-NEXT behavior.
- **Counters**
  - Walk `1.3.6.1.4.1.2590.1.1.1.5.7.2.1.1`:
    - `.0` or `.1` → `LifeCount`; `.0` is preferred, `.1` is fallback.
  - Walk/read `1.3.6.1.4.1.2590.1.1.1.5.7.2.1.5`; `.0` → `ectScanTotal`.
  - Counter table:
    - `1.3.6.1.4.1.2590.1.1.1.5.7.2.2.1.3<instance>` → color type.
    - `.1.4<instance>` → function type (`1` copier, `2` printer).
    - `.1.6<instance>` → duplex count.
  - Function and color instances are discovered by walking `.1.4` and `.1.3`, then matched by instance. Duplex values are summed into printer/copier duplex totals and ultimately `ectDuplexedPages`.
  - Color classes include mono (`1`), four-color (`2`), single-color (`6`), and the script’s two-color matching value.
- **Status/alerts**
  - Standard status, alerts, supplies, paper, LCD, location, uptime, serial, and firmware operations use the common/default scan path; no dedicated Minolta alert OID is shown in the inspected counter code.

## OKI

- **Counters**
  - Copier counters:
    - `1.3.6.1.4.1.2001.1.1.1.1.11.1.10.170.1.14.1`
    - `.15.1`
    - `.16.1`
    - `.17.1`
    - First two are color, last two mono; the “large” entries are doubled.
    - Results → `ectCopierColor`, `ectCopierMono`.
  - Printer counters:
    - `.18.1`, `.19.1`, `.20.1`, `.21.1` under the same base.
    - First two color, last two mono; large entries doubled.
    - Results → `ectPrintColor`, `ectPrintMono`.
  - Fax:
    - `.22.1` and `.23.1` → `ectFaxPrint`; first value doubled.
  - Paper-size/color totals:
    - `.2.1` → `ectLargeColor`
    - `.3.1` → `ectSmallColor`
    - `.4.1` → `ectLargeMono`
    - `.5.1` → `ectSmallMono`
    - `ectLargePaper` = large mono + large color.
  - Scan total:
    - `.24.1` → `ectScanTotal`.
    - Fallback: `1.3.6.1.4.1.2001.1.1.4.2.1.3.10`.
  - Total mono:
    - Walk `1.3.6.1.4.1.2001.1.1.1.1.11.1.10.130`; `.0` → `ectLifeMono`.
  - Total color:
    - Walk `1.3.6.1.4.1.2001.1.1.1.1.11.1.10.140`; `.0` → `ColorCount`.
  - `-1` means unsupported/absent. Large-counter entries are multiplied by two.
- **Identity**
  - The common module reads OKI-specific vendor/model, serial, firmware, host, and counter data; exact identity OIDs are in the common identity routines.
  - the Oki script falls back to `DefaultScan.GetSerialNumber` if the OKI serial routine returns an empty value.
- **Status/alerts**
  - the Oki script delegates status and alerts to `DefaultScan.GetStatus` and `DefaultScan.GetAlerts`; supplies and paper levels are also delegated.

## Panasonic

- **Identity**
  - `1.3.6.1.4.1.258.405.1.1.1.3` → model; vendor is hard-coded to `Panasonic`.
  - `.4` → firmware.
  - `.5` → serial number.
- **Counter tables**
  - Type table base: `1.3.6.1.4.1.258.402.1.4.2.1`.
  - Value table uses the corresponding base/value OID under the same Panasonic counter table; instances are found by matching type values.
  - Life-count type IDs:
    - `1000` mono
    - `1001` four-color
    - `1005` two-color
    - `1004` single-color
    - `26` duplex
    - Mono → `ectLifeMono`; all color classes are summed into `ColorCount` and `ectLifeColor`; `26` → `ectDuplexedPages`.
  - Copy IDs:
    - `74` mono, `77` four-color, `76` two-color, `75` single-color.
    - Results → `ectCopierMono` and `ectCopierColor`.
  - Print IDs:
    - `70` mono; the remainder of the print/copy routine handles color print IDs and writes `ectPrintMono`/`ectPrintColor`.
  - The script matches type-table instances with `getinstances`, then reads the corresponding value-table instance. Counts are page counts; color classes are summed.
- **Status/alerts**
  - Status, alerts, supplies, paper, LCD, location, and uptime use default scan routines.

## Muratec

- **Counters**
  - `1.3.6.1.2.1.43.10.2.1.4.1` → `LifeCount`.
  - `1.3.6.1.4.1.4322.4.1` → `ectScanTotal`.
  - `1.3.6.1.4.1.4322.2.3.6.14` → `ectOther` (list count).
  - `1.3.6.1.4.1.4322.101.1.1.5.7.2.2.1.5.1.2` → total print count; list count is subtracted when total print exceeds list count; result → `ectPrintTotal`.
  - Fallback print counter: `1.3.6.1.4.1.4322.2.3.5.14` → PC-print count.
  - `1.3.6.1.4.1.4322.2.3.3.14` → `ectCopierTotal`.
  - `1.3.6.1.4.1.4322.2.3.4.14` → `ectFaxPrint`.
- **Identity**
  - `1.3.6.1.4.1.4322.5` → serial; failure is non-fatal because many devices omit it.
  - `1.3.6.1.4.1.4322.101.1.1.5.5.1.1.2.1` → firmware version name.
  - `1.3.6.1.4.1.4322.101.1.1.5.5.1.1.3.1` → firmware version code; name and code are concatenated.
  - Model/vendor handling uses the default scan path.
- **Status/alerts**
  - Toner is always reported as unknown because Muratec does not support toner levels.
  - Standard status/alerts are delegated to default routines.

## Riso

- **Counters**
  - `1.3.6.1.4.1.24807.1.2.2.3.1.0` → private total; replaces the default `LifeCount` only if larger.
  - Total-counter table: `1.3.6.1.4.1.24807.1.2.2.3.4.1`
    - `.2.1` → `ectSmallColor`
    - `.2.2` → `ectSmallMono`
    - `.3.1` → `ectLargeColor`
    - `.3.2` → `ectLargeMono`
    - `.4.1` → `ColorCount`, sets `IsColor`
    - `.4.2` → `ectLifeMono`
  - Breakdown table: `1.3.6.1.4.1.24807.1.2.2.3.2.1.2`.
    - Instance number is the counter type.
    - Color-large IDs: `1,3,6,15,17,20`.
    - Mono-large IDs: `8,10,13`.
    - Color-small IDs: `2,4,7,16,18,21`.
    - All other IDs are treated as mono-small.
    - Large color/mono and small color/mono totals are accumulated.
    - `ColorCount = largeColor*2 + smallColor`.
    - `ectLifeMono = largeMono*2 + smallMono`.
  - If calculated color + mono exceeds `LifeCount`, the calculated total replaces `LifeCount`.
- **Identity/status**
  - Serial, model, firmware, status, and alerts are handled by the remaining Riso/default scan routines; no vendor-specific alert logic appears in the counter section.

## Memjet

- **Identity**
  - `1.3.6.1.4.1.38191.1.1.2` → model; falls back to `DefaultScan.GetDeviceName`.
  - `.1.1.5` → serial; falls back to default serial.
  - `.1.1.4` → firmware.
- **Counters**
  - Counter table walk: `1.3.6.1.4.1.38191.2.1.1.2`.
  - Print counters:
    - `.3` + `.4` → `ectPrintMono`.
    - `.5` + `.6` → `ectPrintColor`.
  - Copy table:
    - Type table `...2.12.1.4`; value base `...2.12.1.2`.
    - Rows whose type is `"mono"` sum to `ectCopierMono`; all other types sum to `ectCopierColor`.
  - Scan table:
    - Type table `...2.11.1.4`; value base `...2.11.1.2`.
    - `"mono"` rows → `ectScanMono`; other rows → `ectScanColor`; sum → `ectScanTotal`.
  - `...2.10` → `ectDuplexedPages`.
  - Default life count is retained unless print mono + print color + copier mono + copier color is larger; the larger calculated total becomes `LifeCount`.
  - `ColorCount` = print color + copier color; `IsColor` is set when positive.
- **Status/alerts**
  - Standard status, alerts, supplies, paper, LCD, location, and uptime are delegated.

## Pantum

- **Identity**
  - `1.3.6.1.4.1.40093.1.1.1.10` → model; default model fallback.
  - `.6` → vendor; default vendor fallback; final fallback is `Pantum`.
  - `.5` → serial; fallback `.6.1.2`.
  - `1.3.6.1.4.1.40093.1.1.1.1` → firmware, trimmed.
- **Counters**
  - `1.3.6.1.4.1.40093.1.1.3.12` → `LifeCount`.
  - `.1.1.3.4` → `ectLargePaper` (Legal).
  - Documented but not read into results: `.1.1.3.3` A4, `.5` B5, `.6` B6, `.17` A5, `.7` other.
  - No small-paper counter is produced; the script notes that small paper must be inferred by subtracting large paper from life count.
- **Status/alerts**
  - Status, alerts, supplies, paper, LCD, location, and uptime use default routines.

## Axis

- **Discovery/identity**
  - `1.3.6.1.4.1.368.2.1.1.1` → number of supported ports.
  - Walk `1.3.6.1.4.1.368.2.1.1.2`.
  - `...2.1.1.<row>` → port-table row/index.
  - `...2.1.3.<row>` → online flag; only value `1` is scanned.
  - `...2.1.2.<row>` → port name.
  - `1.3.6.1.2.1.25.3.2.1.3.<row>` → connected printer model.
  - Vendor is hard-coded to `Axis`; missing model becomes `Unknown Device`.
- **Counters**
  - `1.3.6.1.2.1.43.10.2.1.4.<row>.1` → connected printer `LifeCount`.
- **Status/alerts**
  - Axis itself supplies no alert or firmware OID in this script; connected-device status/identity beyond the model and life count is inherited from the copied scan result/default workflow.

## DataMax

- `1.3.6.1.4.1.10917.1.5.1.1.17.1` → model; default model fallback.
- `.17.2` → serial; default serial fallback.
- `.17.5` → firmware.
- `1.3.6.1.4.1.10917.1.1.6.7` → total printed inches; stored in `ectTotal2`.
- `LifeCount` = `floor(total inches / 11)`, treating 11 inches as one standard-letter impression.
- Vendor is hard-coded to `DataMax`.
- Status, alerts, supplies, paper, LCD, location, and uptime use default routines.

## Dell BW

- Sets Lexmark enterprise prefix to `674.10898.100`, so all delegated Lexmark-common OIDs are the Lexmark OIDs with that enterprise prefix.
- `1.3.6.1.4.1.674.10898.1.1.1` → binary service tag; if empty, `ServiceTag = SerialNumber`.
- Identity, firmware, counters, status, and alerts are otherwise obtained through `LexmarkCommon` and `DefaultScan`.
- `IsColor` is forced true when `ColorCount > 0` or the inherited color flag is already true.
- Supplies reporting converts Lexmark’s `-3` low-toner indication from “OK” to “Low.”
