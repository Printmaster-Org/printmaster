// Package storage provides shared data structures for PrintMaster agent and server
package storage

import (
	"time"
)

// Device type constants - how a device is connected
const (
	DeviceTypeNetwork = "network" // Network printer discovered via SNMP
	DeviceTypeUSB     = "usb"     // USB-connected printer
	DeviceTypeLocal   = "local"   // Local port (LPT, COM)
	DeviceTypeShared  = "shared"  // Network share (\\server\printer)
	DeviceTypeVirtual = "virtual" // Virtual printer (PDF, XPS, OneNote)
)

// Source type constants - how the device was discovered
const (
	SourceTypeSNMP    = "snmp"    // Discovered via SNMP network scan
	SourceTypeSpooler = "spooler" // Discovered via OS print spooler
	SourceTypeManual  = "manual"  // Manually added by user
)

// FieldLock represents a locked field that should not be auto-updated from scans
type FieldLock struct {
	Field    string    `json:"field"`               // Field name (e.g., "model", "hostname", "ip")
	Reason   string    `json:"reason,omitempty"`    // Why it was locked: "manually_entered", "user_locked", "user_preference"
	LockedAt time.Time `json:"locked_at"`           // When the field was locked
	LockedBy string    `json:"locked_by,omitempty"` // User/system that locked it (for future multi-user support)
}

// PaperTray represents the status of a single paper input tray
type PaperTray struct {
	Index        int    `json:"index"`                   // Tray index (1, 2, 3...)
	Name         string `json:"name,omitempty"`          // Tray name ("Tray 1", "Manual Feed")
	MediaType    string `json:"media_type,omitempty"`    // Paper type ("Letter", "A4", "Legal")
	CurrentLevel int    `json:"current_level"`           // Current sheets (-3=someRemaining, -2=unknown, -1=unavailable, 0+=actual)
	MaxCapacity  int    `json:"max_capacity"`            // Max capacity (-2=unknown, -1=unlimited, 0+=actual)
	LevelPercent int    `json:"level_percent,omitempty"` // Calculated percentage (0-100, -1 if unknown)
	Status       string `json:"status,omitempty"`        // "ok", "low", "empty", "unknown"
}

// Device represents a printer device with all its properties (base struct shared by agent and server)
type Device struct {
	DNSServers        []string               `json:"dns_servers,omitempty"`
	DHCPServer        string                 `json:"dhcp_server,omitempty"`
	MetadataOverrides map[string]interface{} `json:"metadata_overrides,omitempty"`
	Serial            string                 `json:"serial"`
	IP                string                 `json:"ip"`
	Manufacturer      string                 `json:"manufacturer,omitempty"`
	Model             string                 `json:"model,omitempty"`
	Hostname          string                 `json:"hostname,omitempty"`
	Firmware          string                 `json:"firmware,omitempty"`
	MACAddress        string                 `json:"mac_address,omitempty"`
	SubnetMask        string                 `json:"subnet_mask,omitempty"`
	Gateway           string                 `json:"gateway,omitempty"`
	Consumables       []string               `json:"consumables,omitempty"`
	StatusMessages    []string               `json:"status_messages,omitempty"`
	LastSeen          time.Time              `json:"last_seen"`
	CreatedAt         time.Time              `json:"created_at"`
	FirstSeen         time.Time              `json:"first_seen"`
	DiscoveryMethod   string                 `json:"discovery_method,omitempty"`
	AssetNumber       string                 `json:"asset_number,omitempty"` // User-defined asset tag/number
	Location          string                 `json:"location,omitempty"`     // Physical location (room, building, etc)
	Description       string                 `json:"description,omitempty"`  // Device description/notes
	WebUIURL          string                 `json:"web_ui_url,omitempty"`   // HTTP/HTTPS URL to device web interface
	RawData           map[string]interface{} `json:"raw_data,omitempty"`     // Additional fields as JSON for extensibility

	// Device classification fields (for unified device view)
	DeviceType        string `json:"device_type,omitempty"`         // network, usb, local, shared, virtual
	SourceType        string `json:"source_type,omitempty"`         // snmp, spooler, manual
	IsUSB             bool   `json:"is_usb,omitempty"`              // Convenience flag for USB devices
	InitialPageCount  int    `json:"initial_page_count,omitempty"`  // User-set baseline for audit trail
	PortName          string `json:"port_name,omitempty"`           // Port info (USB001, LPT1:, etc.) for spooler devices
	DriverName        string `json:"driver_name,omitempty"`         // Driver name for spooler devices
	IsDefault         bool   `json:"is_default,omitempty"`          // Is this the default printer?
	IsShared          bool   `json:"is_shared,omitempty"`           // Is this printer shared on the network?
	SpoolerStatus     string `json:"spooler_status,omitempty"`      // Status from spooler (ready, offline, etc.)
	UsbWebUIAvailable bool   `json:"usb_webui_available,omitempty"` // USB printer has accessible web UI via IPP-USB
}

// MetricsSnapshot represents a point-in-time snapshot of device metrics (base struct)
type MetricsSnapshot struct {
	ID          int64                  `json:"id"`
	Serial      string                 `json:"serial"`
	Timestamp   time.Time              `json:"timestamp"`
	PageCount   int                    `json:"page_count,omitempty"`
	ColorPages  int                    `json:"color_pages,omitempty"`
	MonoPages   int                    `json:"mono_pages,omitempty"`
	ScanCount   int                    `json:"scan_count,omitempty"`
	TonerLevels map[string]interface{} `json:"toner_levels,omitempty"`
	PaperTrays  []PaperTray            `json:"paper_trays,omitempty"` // Current paper tray status
}

// DeviceFilter allows filtering devices by various criteria
type DeviceFilter struct {
	IsSaved       *bool      // nil = all, true = saved only, false = discovered only (agent-specific)
	Visible       *bool      // nil = all, true = visible only, false = hidden only (agent-specific)
	IP            string     // Filter by IP (exact match)
	Serial        string     // Filter by serial (exact match)
	Manufacturer  string     // Filter by manufacturer (case-insensitive contains)
	LastSeenAfter *time.Time // Only devices seen after this time
	Limit         int        // Max results (0 = no limit)
	DeviceType    string     // Filter by device type (network, usb, local, shared, virtual)
	SourceType    string     // Filter by source type (snmp, spooler, manual)
	IsUSB         *bool      // Filter by USB status (nil = all)
}

// PageCountAudit represents a page count change audit entry
type PageCountAudit struct {
	ID           int64     `json:"id"`
	Serial       string    `json:"serial"`        // Device serial
	OldCount     int       `json:"old_count"`     // Previous page count
	NewCount     int       `json:"new_count"`     // New page count
	ChangeType   string    `json:"change_type"`   // "manual", "polled", "initial", "adjustment"
	ChangedBy    string    `json:"changed_by"`    // User/system that made the change
	Reason       string    `json:"reason"`        // Optional reason for manual changes
	Timestamp    time.Time `json:"timestamp"`     // When the change occurred
	SourceMetric string    `json:"source_metric"` // Which metric changed (page_count, color_pages, etc.)
}
