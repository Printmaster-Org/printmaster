package settings

// Scope defines where a setting is enforced.
type Scope string

const (
	// ScopeGlobal indicates the setting is controlled at the server level for all tenants/agents.
	ScopeGlobal Scope = "global"
	// ScopeTenant indicates the setting can be overridden per-tenant but inherits from the global default.
	ScopeTenant Scope = "tenant"
	// ScopeAgentLocal indicates the setting only applies to the local agent instance.
	ScopeAgentLocal Scope = "agent"
)

// EditableRole constrains which roles may edit a field when rendered in a UI or API.
type EditableRole string

const (
	RoleServerAdmin    EditableRole = "server-admin"
	RoleTenantAdmin    EditableRole = "tenant-admin"
	RoleTenantOperator EditableRole = "tenant-operator"
	RoleAgentLocal     EditableRole = "agent-local"
)

// Settings captures the canonical configuration surface for PrintMaster agents/tenants.
// Fleet-managed sections: Discovery, SNMP, Features, Spooler
// Agent-local sections: Logging, Web
type Settings struct {
	Discovery DiscoverySettings `json:"discovery" toml:"discovery"`
	SNMP      SNMPSettings      `json:"snmp" toml:"snmp"`
	Features  FeaturesSettings  `json:"features" toml:"features"`
	Spooler   SpoolerSettings   `json:"spooler" toml:"spooler"`
	Logging   LoggingSettings   `json:"logging" toml:"logging"`
	Web       WebSettings       `json:"web" toml:"web"`
}

// DiscoverySettings control how printers are discovered and rescanned (fleet-managed).
type DiscoverySettings struct {
	// IP Scanning
	SubnetScan        bool   `json:"subnet_scan"`
	ManualRanges      bool   `json:"manual_ranges"`
	RangesText        string `json:"ranges_text"`
	DetectedSubnet    string `json:"detected_subnet"`
	IPScanningEnabled bool   `json:"ip_scanning_enabled"`
	Concurrency       int    `json:"concurrency"`

	// Probe Methods
	ARPEnabled  bool `json:"arp_enabled"`
	ICMPEnabled bool `json:"icmp_enabled"`
	TCPEnabled  bool `json:"tcp_enabled"`
	SNMPEnabled bool `json:"snmp_enabled"`
	MDNSEnabled bool `json:"mdns_enabled"`

	// Automatic Discovery
	AutoDiscoverEnabled         bool `json:"auto_discover_enabled"`
	AutosaveDiscoveredDevices   bool `json:"autosave_discovered_devices"`
	ShowDiscoverButtonAnyway    bool `json:"show_discover_button_anyway"`
	ShowDiscoveredDevicesAnyway bool `json:"show_discovered_devices_anyway"`

	// Passive Listeners
	PassiveDiscoveryEnabled  bool `json:"passive_discovery_enabled"`
	AutoDiscoverLiveMDNS     bool `json:"auto_discover_live_mdns"`
	AutoDiscoverLiveWSD      bool `json:"auto_discover_live_wsd"`
	AutoDiscoverLiveSSDP     bool `json:"auto_discover_live_ssdp"`
	AutoDiscoverLiveSNMPTrap bool `json:"auto_discover_live_snmptrap"`
	AutoDiscoverLiveLLMNR    bool `json:"auto_discover_live_llmnr"`

	// Metrics Collection
	MetricsRescanEnabled         bool `json:"metrics_rescan_enabled"`
	MetricsRescanIntervalMinutes int  `json:"metrics_rescan_interval_minutes"`
	MetricsRescanIntervalSeconds int  `json:"metrics_rescan_interval_seconds"` // For sub-minute intervals (takes precedence if set)
}

// SNMPSettings configure SNMP queries (fleet-managed).
type SNMPSettings struct {
	// Version specifies the SNMP protocol version: "1", "2c", or "3"
	Version string `json:"version"`
	// Community is the community string for SNMPv1/v2c
	Community string `json:"community"`
	// TimeoutMS is the SNMP timeout in milliseconds
	TimeoutMS int `json:"timeout_ms"`
	// Retries is the number of retry attempts for failed queries
	Retries int `json:"retries"`

	// SNMPv3 security parameters
	// SecurityLevel: "noAuthNoPriv", "authNoPriv", or "authPriv"
	SecurityLevel string `json:"security_level,omitempty"`
	// Username is the SNMPv3 security name (USM user)
	Username string `json:"username,omitempty"`
	// AuthProtocol: "MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512" (empty = no auth)
	AuthProtocol string `json:"auth_protocol,omitempty"`
	// AuthPassword is the authentication passphrase
	AuthPassword string `json:"auth_password,omitempty"`
	// PrivProtocol: "DES", "AES", "AES192", "AES256" (empty = no privacy)
	PrivProtocol string `json:"priv_protocol,omitempty"`
	// PrivPassword is the privacy passphrase
	PrivPassword string `json:"priv_password,omitempty"`
	// ContextName is the SNMPv3 context name (optional)
	ContextName string `json:"context_name,omitempty"`
}

// FeaturesSettings toggle optional features (fleet-managed).
type FeaturesSettings struct {
	AgentUpdateChannel     string `json:"agent_update_channel"`
	EpsonRemoteModeEnabled bool   `json:"epson_remote_mode_enabled"`
	CredentialsEnabled     bool   `json:"credentials_enabled"`
	AssetIDRegex           string `json:"asset_id_regex"`
}

// LoggingSettings configure agent logging (agent-local).
type LoggingSettings struct {
	Level          string `json:"level"`
	DumpParseDebug bool   `json:"dump_parse_debug"`
}

// SpoolerSettings configure local printer tracking via OS print spooler (fleet-managed).
type SpoolerSettings struct {
	Enabled                bool `json:"enabled"`
	PollIntervalSeconds    int  `json:"poll_interval_seconds"`
	IncludeNetworkPrinters bool `json:"include_network_printers"`
	IncludeVirtualPrinters bool `json:"include_virtual_printers"`
}

// WebSettings configure the agent's embedded web server (agent-local).
type WebSettings struct {
	EnableHTTP          bool   `json:"enable_http"`
	EnableHTTPS         bool   `json:"enable_https"`
	HTTPPort            string `json:"http_port"`
	HTTPSPort           string `json:"https_port"`
	RedirectHTTPToHTTPS bool   `json:"redirect_http_to_https"`
	CustomCertPath      string `json:"custom_cert_path"`
	CustomKeyPath       string `json:"custom_key_path"`
}
