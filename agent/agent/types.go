package agent

import (
	"time"
)

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

// ScanMeta holds optional metadata from earlier discovery steps (ARP, TCP probes, mDNS)
type ScanMeta struct {
	MAC              string
	OpenPorts        []int
	DiscoveryMethods []string
	Hostname         string
}

// PrinterInfo holds discovered printer details
type PrinterInfo struct {
	IP string `json:"ip"`
	// Manufacturer is the human-friendly vendor name (e.g. "HP", "Canon").
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Serial       string `json:"serial,omitempty"`
	// AdminContact stores the sysContact value (often administrator contact/asset info)
	AdminContact string `json:"admin_contact,omitempty"`
	// AssetID is an extracted asset number when present in admin contact or other fields
	AssetID string `json:"asset_id,omitempty"`
	// Description holds additional device description text (e.g., from prtGeneralSerialNumber or HP-specific OIDs)
	Description string `json:"description,omitempty"`
	// Location holds the physical location of the device (from sysLocation or HP-specific OIDs)
	Location  string `json:"location,omitempty"`
	PageCount int    `json:"page_count,omitempty"`
	// TotalMonoImpressions holds the prtMarkerLifeCount for marker index 1 when available.
	TotalMonoImpressions int `json:"total_mono_impressions,omitempty"`
	// Per-color impressions when available (marker counters)
	BlackImpressions   int `json:"black_impressions,omitempty"`
	CyanImpressions    int `json:"cyan_impressions,omitempty"`
	MagentaImpressions int `json:"magenta_impressions,omitempty"`
	YellowImpressions  int `json:"yellow_impressions,omitempty"`
	// Per-color toner/ink levels and descriptions (0-100 or device-reported value)
	TonerLevelBlack    int       `json:"toner_level_black,omitempty"`
	TonerLevelCyan     int       `json:"toner_level_cyan,omitempty"`
	TonerLevelMagenta  int       `json:"toner_level_magenta,omitempty"`
	TonerLevelYellow   int       `json:"toner_level_yellow,omitempty"`
	TonerDescBlack     string    `json:"toner_desc_black,omitempty"`
	TonerDescCyan      string    `json:"toner_desc_cyan,omitempty"`
	TonerDescMagenta   string    `json:"toner_desc_magenta,omitempty"`
	TonerDescYellow    string    `json:"toner_desc_yellow,omitempty"`
	MAC                string    `json:"mac_address,omitempty"`
	OpenPorts          []int     `json:"open_ports,omitempty"`
	AdvertisedServices []string  `json:"advertised_services,omitempty"`
	DiscoveryMethods   []string  `json:"discovery_methods,omitempty"`
	LastSeen           time.Time `json:"last_seen"`
	// DetectionReasons lists the heuristics/evidence used to mark this host as a printer
	DetectionReasons []string `json:"detection_reasons,omitempty"`
	// MonoImpressions is the black marker counter (marker index 1 when present)
	MonoImpressions int `json:"mono_impressions,omitempty"`
	// ColorImpressions is the combined color marker counter (marker index 2 or others when present)
	ColorImpressions int `json:"color_impressions,omitempty"`
	// TonerLevels maps a supply description to its reported level (where available)
	TonerLevels map[string]int `json:"toner_levels,omitempty"`
	// Consumables lists supply descriptions discovered on the device
	Consumables []string `json:"consumables,omitempty"`
	// StatusMessages contains textual status lines reported via SNMP (if any)
	StatusMessages []string `json:"status_messages,omitempty"`
	// Firmware reported by device (if present)
	Firmware string `json:"firmware,omitempty"`
	// UptimeSeconds (if reported via sysUpTime or similar)
	UptimeSeconds int `json:"uptime_seconds,omitempty"`
	// DuplexSupported indicates whether the device advertises duplex capability
	DuplexSupported bool `json:"duplex_supported,omitempty"`
	// PaperTrayStatus maps tray identifier to a short textual status (legacy, prefer PaperTrays)
	PaperTrayStatus map[string]string `json:"paper_tray_status,omitempty"`
	// PaperTrays contains detailed paper tray information
	PaperTrays []PaperTray `json:"paper_trays,omitempty"`
	// TonerAlerts lists any extracted alert/notification messages related to supplies
	TonerAlerts []string `json:"toner_alerts,omitempty"`
	// Network properties (best-effort extraction)
	SubnetMask string   `json:"subnet_mask,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	DNSServers []string `json:"dns_servers,omitempty"`
	DHCPServer string   `json:"dhcp_server,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`

	// Meters is a normalized map of impression/counter categories (total/mono/color/etc.)
	// Populated from marker counters, page counts, and candidate mappings when available.
	Meters map[string]int `json:"meters,omitempty"`

	// WebUIURL is the detected HTTP/HTTPS URL to the device's web interface (if available)
	WebUIURL string `json:"web_ui_url,omitempty"`

	// LearnedOIDs stores device-specific OID mappings discovered during initial walk
	// This allows metrics collection to use known-working OIDs instead of generic queries
	LearnedOIDs LearnedOIDMap `json:"learned_oids,omitempty"`

	// Capability flags (detected via SNMP evidence analysis)
	IsColor    bool   `json:"is_color,omitempty"`    // Device supports color printing
	IsMono     bool   `json:"is_mono,omitempty"`     // Device is mono-only
	IsCopier   bool   `json:"is_copier,omitempty"`   // Device has copy capability
	IsScanner  bool   `json:"is_scanner,omitempty"`  // Device has scan capability
	IsFax      bool   `json:"is_fax,omitempty"`      // Device has fax capability
	IsLaser    bool   `json:"is_laser,omitempty"`    // Laser print technology
	IsInkjet   bool   `json:"is_inkjet,omitempty"`   // Inkjet print technology
	HasDuplex  bool   `json:"has_duplex,omitempty"`  // Duplex (two-sided) printing supported
	FormFactor string `json:"form_factor,omitempty"` // Physical form: "Desktop", "Wide Format", "Label Printer", etc.
	DeviceType string `json:"device_type,omitempty"` // Classified type: "Color MFP", "Mono Printer", etc.
}

// LearnedOIDMap stores OIDs that were successfully discovered during initial device walk
// These OIDs are then used for efficient targeted metrics collection
type LearnedOIDMap struct {
	// PageCountOID is the specific OID that returned the total page count
	PageCountOID string `json:"page_count_oid,omitempty"`
	// MonoPagesOID is the OID for black/mono impressions (marker 1)
	MonoPagesOID string `json:"mono_pages_oid,omitempty"`
	// ColorPagesOID is the OID for color impressions (marker 2 or combined)
	ColorPagesOID string `json:"color_pages_oid,omitempty"`
	// CyanOID, MagentaOID, YellowOID for individual color markers
	CyanOID    string `json:"cyan_oid,omitempty"`
	MagentaOID string `json:"magenta_oid,omitempty"`
	YellowOID  string `json:"yellow_oid,omitempty"`
	// TonerOIDPrefix is the base OID for toner level queries
	TonerOIDPrefix string `json:"toner_oid_prefix,omitempty"`
	// SerialOID is the specific OID that returned the serial number
	SerialOID string `json:"serial_oid,omitempty"`
	// ModelOID is the OID that returned the model
	ModelOID string `json:"model_oid,omitempty"`
	// Additional vendor-specific OIDs can be added here
	VendorSpecificOIDs map[string]string `json:"vendor_specific_oids,omitempty"`
}

// DiscoveryConfig holds settings for which discovery methods are enabled
type DiscoveryConfig struct {
	ARPEnabled  bool
	ICMPEnabled bool // future: ICMP ping probes
	TCPEnabled  bool
	SNMPEnabled bool
	MDNSEnabled bool // future: mDNS/DNS-SD discovery
}
