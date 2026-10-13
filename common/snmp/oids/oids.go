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
// Reference: ICE packet capture analysis
const (
	// KyoceraEnterprise is the Kyocera enterprise OID prefix
	KyoceraEnterprise = "1.3.6.1.4.1.1347"

	// --- Kyocera Identity (40.1) ---
	KyoceraModelName    = "1.3.6.1.4.1.1347.40.1.1.1.1.1"
	KyoceraSerialNumber = "1.3.6.1.4.1.1347.40.10.1.1.5.1"

	// --- Kyocera Extended Counter Table (42.2.1.1.1) - Detailed page counts ---
	// Table structure: .42.2.1.1.1.<column>.<printer>.<row>
	// Columns: 2-9 (8 columns), Rows: 1-14
	KyoceraCounterTable = "1.3.6.1.4.1.1347.42.2.1.1.1"

	// --- Kyocera Function Counters (42.3.1) - Print/Copy/Fax/Scan ---
	// .42.3.1.2.1.1.<function>.<color_mode>
	// Functions: 1=print, 2=copy, 3=scan, 4=fax
	// Color modes: 1=B&W, 2=single_color, 3=full_color
	KyoceraPrintBW    = "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.1"
	KyoceraPrintColor = "1.3.6.1.4.1.1347.42.3.1.2.1.1.1.3"
	KyoceraCopyBW     = "1.3.6.1.4.1.1347.42.3.1.2.1.1.2.1"
	KyoceraCopyColor  = "1.3.6.1.4.1.1347.42.3.1.2.1.1.2.3"
	KyoceraFaxBW      = "1.3.6.1.4.1.1347.42.3.1.2.1.1.4.1"

	// --- Kyocera Scan Counters (42.3.1.3 and 42.3.1.4) ---
	KyoceraCopyScans  = "1.3.6.1.4.1.1347.42.3.1.3.1.1.2"
	KyoceraFaxScans   = "1.3.6.1.4.1.1347.42.3.1.3.1.1.4"
	KyoceraOtherScans = "1.3.6.1.4.1.1347.42.3.1.4.1.1.1"

	// --- Kyocera Additional Counters (42.5.4.1) ---
	// Table: .42.5.4.1.2.<row> (rows 1-17)
	KyoceraDetailedCounters = "1.3.6.1.4.1.1347.42.5.4.1.2"

	// --- Kyocera Extended Printer MIB (43.x) ---
	KyoceraTotalPrinted = "1.3.6.1.4.1.1347.43.10.1.1.12.1.1"
	KyoceraStatusInfo   = "1.3.6.1.4.1.1347.43.5.1.1.28.1"
)
