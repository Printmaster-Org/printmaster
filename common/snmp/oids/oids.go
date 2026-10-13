package oids

// This package centralizes common SNMP OIDs used throughout the agent.  The
// constants mirror the structure documented in the Host Resources, Printer, and
// Port Monitor MIBs so callers can avoid scattering raw dotted strings.
//
// OID Reference (ICE-compatible):
// - MIB-II System: 1.3.6.1.2.1.1.*
// - MIB-II Interfaces: 1.3.6.1.2.1.2.*
// - Host Resources: 1.3.6.1.2.1.25.*
// - Printer MIB: 1.3.6.1.2.1.43.*
// - PWG Port Monitor: 1.3.6.1.4.1.2699.*

const (
	// --- MIB-II System (RFC 1213) ---

	// SysDescr reports a human-readable system description string.
	SysDescr = "1.3.6.1.2.1.1.1.0"
	// SysObjectID contains the authoritative enterprise OID for the device.
	SysObjectID = "1.3.6.1.2.1.1.2.0"
	// SysUpTime is the time since the device was last re-initialized (hundredths of a second).
	SysUpTime = "1.3.6.1.2.1.1.3.0"
	// SysName provides the device hostname/location string.
	SysName = "1.3.6.1.2.1.1.5.0"
	// SysLocation is the physical location of the device.
	SysLocation = "1.3.6.1.2.1.1.6.0"
)

const (
	// --- MIB-II Interfaces (RFC 1213) ---

	// IfPhysAddress is the MAC address (ifPhysAddress.1)
	IfPhysAddress = "1.3.6.1.2.1.2.2.1.6.1"
)

const (
	// --- Host Resources MIB (RFC 2790) ---

	// HrSystemUptime is the system uptime from Host Resources MIB.
	HrSystemUptime = "1.3.6.1.2.1.25.1.1.0"
	// HrDeviceDescr points at HOST-RESOURCES-MIB::hrDeviceDescr.1
	HrDeviceDescr = "1.3.6.1.2.1.25.3.2.1.3.1"
	// HrDeviceType identifies the HOST-RESOURCES-MIB device class (Printer=3).
	HrDeviceType = "1.3.6.1.2.1.25.3.2.1.2"
	// HrDeviceStatus is the current status of the device (1=unknown, 2=running, 3=warning, 4=testing, 5=down)
	HrDeviceStatus = "1.3.6.1.2.1.25.3.2.1.5.1"
)

const (
	// --- Printer MIB (RFC 3805) ---

	// PrtGeneralSerialNumber (prtGeneralSerialNumber.1) is the canonical serial.
	PrtGeneralSerialNumber = "1.3.6.1.2.1.43.5.1.1.17.1"
	// PrtGeneralPrinterName (prtGeneralPrinterName.1) carries the friendly name.
	PrtGeneralPrinterName = "1.3.6.1.2.1.43.5.1.1.16.1"
	// PrtGeneralCurrentLocalization carries the current language/charset.
	PrtGeneralCurrentLocalization = "1.3.6.1.2.1.43.5.1.1.1.1"

	// --- prtInputTable (43.8) - Paper trays ---
	// PrtInputTable root for paper tray information
	PrtInputTable = "1.3.6.1.2.1.43.8.2.1"
	// PrtInputType identifies the input tray type (1=other, 2=unknown, 3=sheetFeedAutoRemovableTray, etc.)
	PrtInputType = "1.3.6.1.2.1.43.8.2.1.2"
	// PrtInputDimUnit is the unit of measure for paper dimensions (3=tenThousandthsOfInches, 4=micrometers)
	PrtInputDimUnit = "1.3.6.1.2.1.43.8.2.1.3"
	// PrtInputMediaDimFeedDirDeclared is the paper length (feed direction)
	PrtInputMediaDimFeedDirDeclared = "1.3.6.1.2.1.43.8.2.1.4"
	// PrtInputMediaDimXFeedDirDeclared is the paper width (cross-feed direction)
	PrtInputMediaDimXFeedDirDeclared = "1.3.6.1.2.1.43.8.2.1.5"
	// PrtInputCapacityUnit is the unit for capacity (3=tenThousandthsOfInches, 4=micrometers, 8=sheets, etc.)
	PrtInputCapacityUnit = "1.3.6.1.2.1.43.8.2.1.8"
	// PrtInputMaxCapacity is the maximum capacity of the input tray
	PrtInputMaxCapacity = "1.3.6.1.2.1.43.8.2.1.9"
	// PrtInputCurrentLevel is the current level in the input tray
	PrtInputCurrentLevel = "1.3.6.1.2.1.43.8.2.1.10"
	// PrtInputStatus is the current status of the input tray
	PrtInputStatus = "1.3.6.1.2.1.43.8.2.1.11"
	// PrtInputMediaName is the name of the media loaded (e.g., "Letter", "A4")
	PrtInputMediaName = "1.3.6.1.2.1.43.8.2.1.12"
	// PrtInputName is the localized name of the input tray
	PrtInputName = "1.3.6.1.2.1.43.8.2.1.13"
	// PrtInputDescription is the description of the input tray
	PrtInputDescription = "1.3.6.1.2.1.43.8.2.1.18"

	// --- prtMarkerTable (43.10) - Print engine ---
	// PrtMarkerLifeCount targets prtMarkerLifeCount.1 and is commonly treated as the page counter.
	PrtMarkerLifeCount = "1.3.6.1.2.1.43.10.2.1.4.1"
	// PrtMarkerPowerOnCount is pages printed since power on
	PrtMarkerPowerOnCount = "1.3.6.1.2.1.43.10.2.1.5"
	// PrtMarkerProcessColorants is the number of color inks/toners
	PrtMarkerProcessColorants = "1.3.6.1.2.1.43.10.2.1.6"
	// PrtMarkerCounterUnit is the counter unit (7=impressions, 8=sheets, etc.)
	PrtMarkerCounterUnit = "1.3.6.1.2.1.43.10.2.1.3"

	// --- Printer status/error indicators (25.3.5) ---
	HrPrinterStatus             = "1.3.6.1.2.1.25.3.5.1.1"
	HrPrinterDetectedErrorState = "1.3.6.1.2.1.25.3.5.1.2"

	// --- prtMarkerSuppliesTable (43.11) - Supplies/toner ---
	PrtMarkerSuppliesEntry       = "1.3.6.1.2.1.43.11.1.1"
	PrtMarkerSuppliesMarkerIndex = "1.3.6.1.2.1.43.11.1.1.2"
	PrtMarkerSuppliesColorID     = "1.3.6.1.2.1.43.11.1.1.3"
	PrtMarkerSuppliesClass       = "1.3.6.1.2.1.43.11.1.1.4"
	PrtMarkerSuppliesType        = "1.3.6.1.2.1.43.11.1.1.5"
	PrtMarkerSuppliesDesc        = "1.3.6.1.2.1.43.11.1.1.6"
	PrtMarkerSuppliesSupplyUnit  = "1.3.6.1.2.1.43.11.1.1.7"
	PrtMarkerSuppliesMaxCap      = "1.3.6.1.2.1.43.11.1.1.8"
	PrtMarkerSuppliesLevel       = "1.3.6.1.2.1.43.11.1.1.9"

	// --- prtMarkerColorantTable (43.12) - Color information ---
	PrtMarkerColorantTable       = "1.3.6.1.2.1.43.12.1.1"
	PrtMarkerColorantMarkerIndex = "1.3.6.1.2.1.43.12.1.1.2"
	PrtMarkerColorantRole        = "1.3.6.1.2.1.43.12.1.1.3"
	PrtMarkerColorantValue       = "1.3.6.1.2.1.43.12.1.1.4"
	PrtMarkerColorantTonality    = "1.3.6.1.2.1.43.12.1.1.5"

	// --- prtConsoleDisplayBuffer (43.16) - LCD display ---
	PrtConsoleDisplayBufferText = "1.3.6.1.2.1.43.16.5.1.2"

	// --- prtAlertTable (43.17) - Alerts ---
	PrtAlertSeverityLevel = "1.3.6.1.2.1.43.17.6.1.2"

	// --- prtChannelTable (43.18) - Network channels ---
	PrtChannelTable           = "1.3.6.1.2.1.43.18.1.1"
	PrtChannelType            = "1.3.6.1.2.1.43.18.1.1.2"
	PrtChannelProtocolVersion = "1.3.6.1.2.1.43.18.1.1.3"
	PrtChannelState           = "1.3.6.1.2.1.43.18.1.1.8"
)

const (
	// --- Port Monitor (PWG 5100.6) ---

	// PpmPrinterIEEE1284DeviceID provides an IEEE-1284 string via SNMP.
	PpmPrinterIEEE1284DeviceID = "1.3.6.1.4.1.2699.1.2.1.2.1.3"
)

// Epson vendor-specific OIDs (Enterprise 1248)
// Reference: Epson Device Admin (EDA) MIB usage, verified against a live
// ST-C8090. See docs/vendor/EPSON_OID_MAPPING.md.
const (
	// EpsonEnterprise is the Epson enterprise OID prefix
	EpsonEnterprise = "1.3.6.1.4.1.1248"

	// --- Identity (epPrtIdentity 1.2.2.1.1.1.<col>.1) ---
	EpsonDeviceID1284 = "1.3.6.1.4.1.1248.1.2.2.1.1.1.1.1"
	EpsonModelName    = "1.3.6.1.4.1.1248.1.2.2.1.1.1.2.1"
	// EpsonDeviceStatus (epPrtDeviceStatus) returns a full "@BDC ST2" status
	// frame on a plain GET. This is what EDA uses for status, inks,
	// maintenance box and paper counts; no remote-mode command is needed.
	EpsonDeviceStatus = "1.3.6.1.4.1.1248.1.2.2.1.1.1.4.1"
	EpsonSerialNumber = "1.3.6.1.4.1.1248.1.2.2.1.1.1.5.1"
	// EpsonFirmwareVersion is the main firmware string.
	EpsonFirmwareVersion = "1.3.6.1.4.1.1248.1.1.3.1.21.1.1.2.5"

	// --- Version table (1.2.2.2.1.1.<col>.1.<idx>): 2=value, 3=name ---
	// e.g. Main FW / Serial No / Network FW / FW Package / PS / PCL.
	EpsonVersionTable = "1.3.6.1.4.1.1248.1.2.2.2.1.1"

	// --- Power counters (1.2.2.3.1.1) ---
	EpsonPowerOnCount         = "1.3.6.1.4.1.1248.1.2.2.3.1.1.17.1"
	EpsonMoveFromStandbyCount = "1.3.6.1.4.1.1248.1.2.2.3.1.1.18.1"

	// --- Single-pass color mode counters (1.2.2.5.1.1) ---
	EpsonSinglePassColorMode    = "1.3.6.1.4.1.1248.1.2.2.5.1.1.56.1.1"
	EpsonSinglePassColorModeAlt = "1.3.6.1.4.1.1248.1.2.2.5.1.1.119.1.1"

	// --- Paper-size table (1.2.2.6.1.1.<col>.1.<row>) ---
	// Columns: 2=size name, 3=unit (row valid only if non-empty), 4=total,
	// 5=color, 7=simplex, 8=null, 9=color simplex.
	EpsonPaperSizeTable = "1.3.6.1.4.1.1248.1.2.2.6.1.1"

	// --- Marker totals (1.2.2.27.1.1.<col>.1.1) ---
	EpsonMarkerTotalPrinted        = "1.3.6.1.4.1.1248.1.2.2.27.1.1.3.1.1" // mono (B&W) pages
	EpsonMarkerColorPrinted        = "1.3.6.1.4.1.1248.1.2.2.27.1.1.4.1.1" // color pages
	EpsonMarkerProcessColorForMono = "1.3.6.1.4.1.1248.1.2.2.27.1.1.6.1.1" // composite-black pages (NOT mono)
	EpsonMarkerSimplexTotal        = "1.3.6.1.4.1.1248.1.2.2.27.1.1.10.1.1"
	EpsonMarkerSimplexColor        = "1.3.6.1.4.1.1248.1.2.2.27.1.1.11.1.1"
	// Legacy totals not read by EDA; kept only as fallbacks when
	// prtMarkerLifeCount is missing.
	EpsonTotalPagesLegacy  = "1.3.6.1.4.1.1248.1.2.2.27.1.1.33.1.1"
	EpsonTotalPagesLegacy2 = "1.3.6.1.4.1.1248.1.2.2.27.1.1.30.1.1"

	// --- Function counters (1.2.2.27.6.1.<col>.1.1.<idx>) ---
	// Columns: 2=type (1=print, 2=copy, 3=fax, 0=other), 3=unit (3=pages),
	// 4=total, 5=color. Mono = total - color.
	EpsonFunctionTable = "1.3.6.1.4.1.1248.1.2.2.27.6.1"

	// --- Supplies table (1.2.2.28.1.1.<col>.1.<idx>) ---
	// Columns: 2=level %, 3=replacement count (-2 = n/a), 5/18=description,
	// 21=type (1002=ink, 1003=maintenance box).
	EpsonSuppliesTable = "1.3.6.1.4.1.1248.1.2.2.28.1.1"

	// --- Jam counter ---
	EpsonJamCount = "1.3.6.1.4.1.1248.1.2.2.36.1.1.2.1"

	// --- Scanner input table (1.4.2.6.1.1.<col>.3.<row>) ---
	// Columns: 2=type (3=flatbed, 4=ADF, 5=sheet-feed, 6=check), 4=total,
	// 5=two-sided, 7=color, 9=unit (1=sheets, 3/empty=pages),
	// 12=ADF roller residue, 14=ADF roller replacements.
	EpsonScanInputTable = "1.3.6.1.4.1.1248.1.4.2.6.1.1"

	// EpsonInputTable (epPrtInputTable) holds paper-feed roller life per input.
	// Columns: 5=roller replacements, 8=counter unit (8=percent),
	// 9=roller level (-2=none), 13=roller part number, 14=sheet feed type
	// (6=rear ASF; EDA maps 3/4/5 to feed rollers, newer models report 1xx/2xx).
	EpsonInputTable = "1.3.6.1.4.1.1248.1.2.2.23.1.1"
)

// Kyocera vendor-specific OIDs (Enterprise 1347)
// Reference: Kyocera Net Device Manager (KDM) def.dev.snmp-oid.xml and the
// decompiled KDM.AccessDevice.DeviceController operators. Validated against real
// walks of a TASKalfa 4053ci and an ECOSYS P2040dw.
const (
	// KyoceraEnterprise is the Kyocera enterprise OID prefix
	KyoceraEnterprise = "1.3.6.1.4.1.1347"

	// --- Identity (kcprtGeneralTable 43.5.1.1, index .1) ---
	KyoceraGeneralModelName  = "1.3.6.1.4.1.1347.43.5.1.1.1.1"  // kcprtGeneralModelName
	KyoceraMemoryTotal       = "1.3.6.1.4.1.1347.43.5.1.1.11.1" // kcprtMemoryDeviceTotalSize
	KyoceraSerialNumber      = "1.3.6.1.4.1.1347.43.5.1.1.28.1" // kcprtSerialNumber
	KyoceraAssetNumber       = "1.3.6.1.4.1.1347.43.5.1.1.29.1" // kcprtAssetNumber
	KyoceraAbsoluteModelName = "1.3.6.1.4.1.1347.43.5.1.1.36.1" // kcprtAbsoluteModelName
	KyoceraEquipmentID       = "1.3.6.1.4.1.1347.43.5.1.1.37.1" // kcprtEquipmentID
	// kcprtFirmwareVersion table: .43.5.4.1.5.1.<n> (1=system/main, 2=engine)
	KyoceraFirmwareVersion = "1.3.6.1.4.1.1347.43.5.4.1.5.1"
	KyoceraHostName        = "1.3.6.1.4.1.1347.40.10.1.1.5.1" // kmHostName

	// --- Page counters ---
	// kcprtMarkerServiceCount: KDM PageCount (5036) logic 2, used by most models.
	KyoceraMarkerServiceCount = "1.3.6.1.4.1.1347.43.10.1.1.12.1.1"

	// kmMFPCounterItem .42.3.1.1.1.1.<n>: 1=printer, 2=copier, 3/4=fax (model dependent)
	KyoceraMFPCounter = "1.3.6.1.4.1.1347.42.3.1.1.1.1"
	// kmMFPColorModeCounterItem .42.3.1.2.1.1.<function>.<mode>
	// function: 1=printer, 2=copier, 3=(unused), 4=fax; mode: 1=mono, 2=single color, 3=full color
	KyoceraMFPColorModeCounter = "1.3.6.1.4.1.1347.42.3.1.2.1.1"
	// kmMFPScanCounterItem .42.3.1.3.1.1.<n>: 2=copy scans, 3/4=fax scans
	KyoceraMFPScanCounter = "1.3.6.1.4.1.1347.42.3.1.3.1.1"
	// kmMFPOtherScanCounterItem .42.3.1.4.1.1.1
	KyoceraMFPOtherScanCounter = "1.3.6.1.4.1.1347.42.3.1.4.1.1.1"
	// kmColorModeCounterItem .42.2.2.1.1.3.1.<mode> (1=mono, 2=mono color, 3=full color)
	// used by printer-only models (KDM logic 2) instead of the MFP table.
	KyoceraColorModeCounter = "1.3.6.1.4.1.1347.42.2.2.1.1.3.1"
	// kmscnLifeCount .46.10.1.1.5.<hrDeviceIndex> (total scans; walked)
	KyoceraScanLifeCount = "1.3.6.1.4.1.1347.46.10.1.1.5"

	// kmMediaCounterTable .42.2.1.1.1.<col>.1.<row>
	// cols: 2=media name, 6=total, 7=mono, 8=full color, 9=mono color
	KyoceraMediaCounterTable = "1.3.6.1.4.1.1347.42.2.1.1.1"
	// kmColorCoverage{Low,Medium,High}CoverageCounter .42.2.4.1.1.<1|2|3>.1.<hrDeviceIndex>
	KyoceraColorCoverageTable = "1.3.6.1.4.1.1347.42.2.4.1.1"

	// --- Maintenance kits (kcprtMaintenanceKitTable 43.24.1.1) ---
	// cols: 2=utilization, 3=type, 4=name, 5=current counter, 6=preset counter
	KyoceraMaintenanceKitTable = "1.3.6.1.4.1.1347.43.24.1.1"

	// --- Status ---
	// kcprtAlertStateTable .43.18.2.1.<col>.1.<n>: 2=panel display text, 3=display index (code)
	KyoceraAlertStateTable = "1.3.6.1.4.1.1347.43.18.2.1"
	// kmscnDeviceErrorState .46.18.1.1.2.<idx>: 2 or bit 0x08 = scanner cover open
	KyoceraScannerErrorState = "1.3.6.1.4.1.1347.46.18.1.1.2"

	// --- Finisher (kcprtFinisherTable 43.9.1.1, index .1.1) ---
	// col 8 kcprtStapler: 3=installed/on, 4=off, 5=not present
	// col 9 kcprtStaplerConsumableState: -3=has staples, -2=add staples
	// col 11 kcprtPunchStatus: 1=installed
	KyoceraFinisherTable = "1.3.6.1.4.1.1347.43.9.1.1"
)

// Brother vendor-specific OIDs (Enterprise 2435)
// Reference: Print Audit ICE BrotherCommon scan logic (docs/dev/vendor/PRINTAUDIT_ICE_FINDINGS.md),
// cross-checked against captured blobs from the open-source bieniu/brother library.
const (
	// BrotherEnterprise is the Brother enterprise OID prefix
	BrotherEnterprise = "1.3.6.1.4.1.2435"

	// --- Identity ---
	BrotherIEEE1284DeviceID = "1.3.6.1.4.1.2435.2.3.9.1.1.7.0"         // MFG:/MDL: device ID
	BrotherSerialNumber     = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.1.0"   // brInfoSerialNumber
	BrotherFirmwareVersion  = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.17.0"  // main firmware (newer models)
	BrotherFirmwareLegacy   = "1.3.6.1.4.1.2435.2.4.3.1240.6.5.0"      // firmware (Print Audit)
	BrotherPanelDisplay     = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.4.5.2.0" // front-panel status text

	// --- Binary info blobs: records of <id><flag><len><len bytes big-endian>, 0xFF terminated ---
	// brInfoCounter ids: 0x00=total, 0x01=mono, 0x02=color, 0x06=duplex
	BrotherCounterInfo = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.10.0"
	// brInfoMaintenance ids: toner/drum/fuser/belt remaining life (see brother.go)
	BrotherMaintenanceInfo = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.8.0"

	// --- Function counter table .52.21.1.<col>.<idx>: col 2=type code, col 3=count ---
	// type codes: 1=print total, 2=fax total, 3=copy total, 4=copy color, 5=print color,
	// 6=fax color, 7=print mono, 8=fax mono, 9=copy mono
	BrotherFunctionCounterTable = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.52.21.1"
	// --- Scan counter table .54.2.2.1.<col>.<idx>: col 1=type code, col 3=count ---
	// type codes: 1=ADF simplex, 2=flatbed, 3=ADF duplex
	BrotherScanCounterTable = "1.3.6.1.4.1.2435.2.3.9.4.2.1.5.5.54.2.2.1"
)
