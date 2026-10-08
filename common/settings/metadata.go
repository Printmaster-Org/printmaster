package settings

// FieldType describes the UI/control type for a setting.
type FieldType string

const (
	FieldTypeBool     FieldType = "bool"
	FieldTypeText     FieldType = "text"
	FieldTypeTextarea FieldType = "textarea"
	FieldTypeNumber   FieldType = "number"
	FieldTypeSelect   FieldType = "select"
	FieldTypePassword FieldType = "password"
)

// FieldMeta captures descriptive information about a configuration field for UIs and RBAC.
type FieldMeta struct {
	Path        string         `json:"path"`
	Type        FieldType      `json:"type"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Scope       Scope          `json:"scope"`
	EditableBy  []EditableRole `json:"editable_by"`
	Default     interface{}    `json:"default"`
	Min         *float64       `json:"min,omitempty"`
	Max         *float64       `json:"max,omitempty"`
	Enum        []string       `json:"enum,omitempty"`
}

// Schema describes all available fields and their metadata.
type Schema struct {
	Version string      `json:"version"`
	Fields  []FieldMeta `json:"fields"`
}

// DefaultSchema returns metadata describing every supported field.
func DefaultSchema() Schema {
	defaults := DefaultSettings()
	fields := []FieldMeta{
		// ========== Discovery: IP Scanning ==========
		{
			Path:        "discovery.subnet_scan",
			Type:        FieldTypeBool,
			Title:       "Scan Local Subnet",
			Description: "When enabled, automatically include the agent's directly-connected subnet in periodic discovery runs.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.SubnetScan,
		},
		{
			Path:        "discovery.manual_ranges",
			Type:        FieldTypeBool,
			Title:       "Manual Ranges Enabled",
			Description: "Allow administrators to define explicit IP/CIDR ranges to scan instead of only using autodetected subnets.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.ManualRanges,
		},
		{
			Path:        "discovery.ranges_text",
			Type:        FieldTypeTextarea,
			Title:       "IP Ranges",
			Description: "IP ranges to scan. One per line. Formats: 192.168.1.50, 192.168.1.0/24, 192.168.1.1-50, 192.168.1.x",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.RangesText,
		},
		{
			Path:        "discovery.ip_scanning_enabled",
			Type:        FieldTypeBool,
			Title:       "IP Scanning",
			Description: "Master switch that disables both manual and automatic IP scanning when turned off.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.IPScanningEnabled,
		},
		{
			Path:        "discovery.concurrency",
			Type:        FieldTypeNumber,
			Title:       "Discovery Concurrency",
			Description: "Maximum number of concurrent discovery workers per agent.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.Concurrency,
		},
		// ========== Discovery: Probe Methods ==========
		{
			Path:        "discovery.arp_enabled",
			Type:        FieldTypeBool,
			Title:       "ARP",
			Description: "Enable ARP probes for fast device liveness checks.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.ARPEnabled,
		},
		{
			Path:        "discovery.icmp_enabled",
			Type:        FieldTypeBool,
			Title:       "ICMP",
			Description: "Enable ICMP ping probes when scanning.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.ICMPEnabled,
		},
		{
			Path:        "discovery.tcp_enabled",
			Type:        FieldTypeBool,
			Title:       "TCP",
			Description: "Probe printer ports (80/443/9100) to confirm liveness even when ICMP is blocked.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.TCPEnabled,
		},
		{
			Path:        "discovery.snmp_enabled",
			Type:        FieldTypeBool,
			Title:       "SNMP",
			Description: "Collect printer metadata via SNMP queries during discovery.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.SNMPEnabled,
		},
		{
			Path:        "discovery.mdns_enabled",
			Type:        FieldTypeBool,
			Title:       "mDNS HTTP Probing",
			Description: "Attempt HTTP-based discovery using Bonjour/mDNS advertisements.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.MDNSEnabled,
		},
		// ========== Discovery: Automatic Discovery ==========
		{
			Path:        "discovery.auto_discover_enabled",
			Type:        FieldTypeBool,
			Title:       "Auto Discover",
			Description: "Run periodic discovery scans and allow passive listeners to execute.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverEnabled,
		},
		{
			Path:        "discovery.autosave_discovered_devices",
			Type:        FieldTypeBool,
			Title:       "Autosave Discovered Devices",
			Description: "Automatically promote newly discovered printers into the saved device list.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutosaveDiscoveredDevices,
		},
		{
			Path:        "discovery.show_discover_button_anyway",
			Type:        FieldTypeBool,
			Title:       "Show Manual Discover Button",
			Description: "Expose the manual \"Discover Now\" button even when auto discovery is enabled.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Discovery.ShowDiscoverButtonAnyway,
		},
		{
			Path:        "discovery.show_discovered_devices_anyway",
			Type:        FieldTypeBool,
			Title:       "Show Discovered Devices",
			Description: "Keep the Discovered Devices panel visible even when autosave is enabled.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Discovery.ShowDiscoveredDevicesAnyway,
		},
		// ========== Discovery: Passive Listeners ==========
		{
			Path:        "discovery.passive_discovery_enabled",
			Type:        FieldTypeBool,
			Title:       "Passive Discovery",
			Description: "Master toggle controlling whether passive listeners (mDNS/WSD/SSDP/etc.) are allowed to run.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.PassiveDiscoveryEnabled,
		},
		{
			Path:        "discovery.auto_discover_live_mdns",
			Type:        FieldTypeBool,
			Title:       "Live mDNS",
			Description: "Enable the mDNS/Bonjour listener when Auto Discover is enabled.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverLiveMDNS,
		},
		{
			Path:        "discovery.auto_discover_live_wsd",
			Type:        FieldTypeBool,
			Title:       "Live WS-Discovery",
			Description: "Enable the Windows WS-Discovery listener when Auto Discover is enabled.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverLiveWSD,
		},
		{
			Path:        "discovery.auto_discover_live_ssdp",
			Type:        FieldTypeBool,
			Title:       "Live SSDP",
			Description: "Enable the SSDP/UPnP listener when Auto Discover is enabled.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverLiveSSDP,
		},
		{
			Path:        "discovery.auto_discover_live_snmptrap",
			Type:        FieldTypeBool,
			Title:       "SNMP Trap Listener",
			Description: "Enable SNMP trap ingestion for event-driven discovery.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverLiveSNMPTrap,
		},
		{
			Path:        "discovery.auto_discover_live_llmnr",
			Type:        FieldTypeBool,
			Title:       "LLMNR Listener",
			Description: "Enable the LLMNR responder for limited passive discovery.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.AutoDiscoverLiveLLMNR,
		},
		// ========== Discovery: Metrics Collection ==========
		{
			Path:        "discovery.metrics_rescan_enabled",
			Type:        FieldTypeBool,
			Title:       "Metrics Rescan",
			Description: "Enable periodic SNMP metrics collection for saved printers.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.MetricsRescanEnabled,
		},
		{
			Path:        "discovery.metrics_rescan_interval_minutes",
			Type:        FieldTypeNumber,
			Title:       "Metrics Interval (minutes)",
			Description: "How often to refresh printer counters when metrics rescan is enabled. Minimum 1 minute.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.MetricsRescanIntervalMinutes,
		},
		{
			Path:        "discovery.metrics_rescan_interval_seconds",
			Type:        FieldTypeNumber,
			Title:       "Metrics Interval (seconds)",
			Description: "For sub-minute intervals. If set (15-300 seconds), overrides minutes setting. Use with caution - aggressive polling may impact older devices. 0 = use minutes interval.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Discovery.MetricsRescanIntervalSeconds,
		},
		// ========== SNMP (fleet-managed) ==========
		{
			Path:        "snmp.community",
			Type:        FieldTypeText,
			Title:       "SNMP Community",
			Description: "Community string used for SNMP v1/v2c queries.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.SNMP.Community,
		},
		{
			Path:        "snmp.timeout_ms",
			Type:        FieldTypeNumber,
			Title:       "SNMP Timeout (ms)",
			Description: "Timeout per SNMP request in milliseconds.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.SNMP.TimeoutMS,
		},
		{
			Path:        "snmp.retries",
			Type:        FieldTypeNumber,
			Title:       "SNMP Retries",
			Description: "Number of retries before marking an SNMP request as failed.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.SNMP.Retries,
		},
		// ========== Features (fleet-managed) ==========
		{
			Path:        "features.agent_update_channel",
			Type:        FieldTypeSelect,
			Title:       "Agent Update Channel",
			Description: "Persistent release channel: global default, customer override, then Agent override unless customer-enforced. Empty uses Agent configuration; Inherit removes an override. Enable prerelease intake and sync before choosing Beta/Dev.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     "",
			Enum:        []string{"", "stable", "beta", "dev"},
		},
		{
			Path:        "features.epson_remote_mode_enabled",
			Type:        FieldTypeBool,
			Title:       "Enable Epson Remote Mode",
			Description: "Allow the agent to issue Epson remote-mode commands for richer metrics (experimental).",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Features.EpsonRemoteModeEnabled,
		},
		{
			Path:        "features.credentials_enabled",
			Type:        FieldTypeBool,
			Title:       "Saved Credentials",
			Description: "Allow the agent to store per-device HTTP credentials for UI proxying.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin},
			Default:     defaults.Features.CredentialsEnabled,
		},
		{
			Path:        "features.asset_id_regex",
			Type:        FieldTypeText,
			Title:       "Asset ID Regex",
			Description: "Optional regex to extract asset identifiers from device hostnames or descriptions.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Features.AssetIDRegex,
		},
		// ========== Spooler / Local Printer Tracking (fleet-managed) ==========
		{
			Path:        "spooler.enabled",
			Type:        FieldTypeBool,
			Title:       "Enable Local Printer Tracking",
			Description: "Monitor locally-attached printers (USB, shared) via the OS print spooler.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Spooler.Enabled,
		},
		{
			Path:        "spooler.poll_interval_seconds",
			Type:        FieldTypeNumber,
			Title:       "Poll Interval (seconds)",
			Description: "How often to check for new print jobs from the spooler.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Spooler.PollIntervalSeconds,
		},
		{
			Path:        "spooler.include_network_printers",
			Type:        FieldTypeBool,
			Title:       "Include Network Printers",
			Description: "Track printers with network ports (typically already tracked via SNMP).",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Spooler.IncludeNetworkPrinters,
		},
		{
			Path:        "spooler.include_virtual_printers",
			Type:        FieldTypeBool,
			Title:       "Include Virtual Printers",
			Description: "Track PDF writers and other virtual print drivers.",
			Scope:       ScopeTenant,
			EditableBy:  []EditableRole{RoleServerAdmin, RoleTenantAdmin},
			Default:     defaults.Spooler.IncludeVirtualPrinters,
		},
		// ========== Logging (agent-local) ==========
		{
			Path:        "logging.level",
			Type:        FieldTypeSelect,
			Title:       "Log Level",
			Description: "Runtime log verbosity for the agent.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Logging.Level,
			Enum:        []string{"debug", "info", "warn", "error"},
		},
		{
			Path:        "logging.dump_parse_debug",
			Type:        FieldTypeBool,
			Title:       "Dump Parse Debug",
			Description: "Emit verbose SNMP parse information for troubleshooting.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Logging.DumpParseDebug,
		},
		// ========== Web (agent-local) ==========
		{
			Path:        "web.enable_http",
			Type:        FieldTypeBool,
			Title:       "Enable HTTP",
			Description: "Expose the agent UI/API over HTTP (non-TLS).",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.EnableHTTP,
		},
		{
			Path:        "web.enable_https",
			Type:        FieldTypeBool,
			Title:       "Enable HTTPS",
			Description: "Expose the agent UI/API over HTTPS using the configured certificate.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.EnableHTTPS,
		},
		{
			Path:        "web.http_port",
			Type:        FieldTypeText,
			Title:       "HTTP Port",
			Description: "Port used for the HTTP listener.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.HTTPPort,
		},
		{
			Path:        "web.https_port",
			Type:        FieldTypeText,
			Title:       "HTTPS Port",
			Description: "Port used for the HTTPS listener.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.HTTPSPort,
		},
		{
			Path:        "web.redirect_http_to_https",
			Type:        FieldTypeBool,
			Title:       "Redirect HTTP to HTTPS",
			Description: "When enabled, all HTTP traffic is redirected to HTTPS.",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.RedirectHTTPToHTTPS,
		},
		{
			Path:        "web.custom_cert_path",
			Type:        FieldTypeText,
			Title:       "Custom Cert Path",
			Description: "Filesystem path to a user-provided TLS certificate (PEM).",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.CustomCertPath,
		},
		{
			Path:        "web.custom_key_path",
			Type:        FieldTypeText,
			Title:       "Custom Key Path",
			Description: "Filesystem path to a user-provided TLS private key (PEM).",
			Scope:       ScopeAgentLocal,
			EditableBy:  []EditableRole{RoleAgentLocal},
			Default:     defaults.Web.CustomKeyPath,
		},
	}

	fields = append(fields,
		FieldMeta{Path: "snmp.version", Type: FieldTypeSelect, Title: "SNMP Version", Description: "Protocol used for printer queries.", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.Version, Enum: []string{"1", "2c", "3"}},
		FieldMeta{Path: "snmp.security_level", Type: FieldTypeSelect, Title: "SNMPv3 Security Level", Description: "Authentication and privacy required for SNMPv3.", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.SecurityLevel, Enum: []string{"", "noAuthNoPriv", "authNoPriv", "authPriv"}},
		FieldMeta{Path: "snmp.username", Type: FieldTypeText, Title: "SNMPv3 Username", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.Username},
		FieldMeta{Path: "snmp.auth_protocol", Type: FieldTypeSelect, Title: "SNMPv3 Auth Protocol", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.AuthProtocol, Enum: []string{"", "MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512"}},
		FieldMeta{Path: "snmp.auth_password", Type: FieldTypePassword, Title: "SNMPv3 Auth Password", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.AuthPassword},
		FieldMeta{Path: "snmp.priv_protocol", Type: FieldTypeSelect, Title: "SNMPv3 Privacy Protocol", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.PrivProtocol, Enum: []string{"", "DES", "AES", "AES192", "AES256", "AES192C", "AES256C"}},
		FieldMeta{Path: "snmp.priv_password", Type: FieldTypePassword, Title: "SNMPv3 Privacy Password", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.PrivPassword},
		FieldMeta{Path: "snmp.context_name", Type: FieldTypeText, Title: "SNMPv3 Context Name", Scope: ScopeTenant, EditableBy: []EditableRole{RoleServerAdmin, RoleTenantAdmin}, Default: defaults.SNMP.ContextName},
	)
	bounds := map[string][2]float64{
		"discovery.concurrency":                     {1, 200},
		"discovery.metrics_rescan_interval_minutes": {1, 1440},
		"discovery.metrics_rescan_interval_seconds": {0, 300},
		"snmp.timeout_ms":                           {500, 60000},
		"snmp.retries":                              {0, 5},
		"spooler.poll_interval_seconds":             {5, 300},
	}
	for i := range fields {
		if limits, ok := bounds[fields[i].Path]; ok {
			fields[i].Min = &limits[0]
			fields[i].Max = &limits[1]
		}
	}
	return Schema{Version: SchemaVersion, Fields: fields}
}
