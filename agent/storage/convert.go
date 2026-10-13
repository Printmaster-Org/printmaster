package storage

import (
	"strings"

	"printmaster/agent/agent"
	"printmaster/agent/supplies"
)

// PrinterInfoToDevice converts agent.PrinterInfo to storage.Device
func PrinterInfoToDevice(pi agent.PrinterInfo, isSaved bool) *Device {
	device := &Device{}

	// Set common Device fields
	device.Serial = pi.Serial
	device.IP = pi.IP
	device.Manufacturer = pi.Manufacturer
	device.Model = pi.Model
	device.Hostname = pi.Hostname
	device.Firmware = pi.Firmware
	device.MACAddress = pi.MAC
	device.SubnetMask = pi.SubnetMask
	device.Gateway = pi.Gateway
	device.StatusMessages = pi.StatusMessages
	device.LastSeen = pi.LastSeen
	device.DiscoveryMethod = "snmp"
	device.AssetNumber = pi.AssetID
	device.Location = pi.Location
	device.Description = pi.Description
	device.WebUIURL = pi.WebUIURL

	// Set agent-specific fields
	device.DNSServers = pi.DNSServers
	device.DHCPServer = pi.DHCPServer
	device.IsSaved = isSaved
	device.Visible = true

	// Handle consumables
	if len(pi.Consumables) > 0 {
		device.Consumables = pi.Consumables
	}

	// Handle discovery methods
	if len(pi.DiscoveryMethods) > 0 {
		device.DiscoveryMethod = pi.DiscoveryMethods[0] // Use first method
	}

	// Store additional info in RawData
	device.RawData = map[string]interface{}{
		"admin_contact":          pi.AdminContact,
		"dns_servers":            pi.DNSServers,
		"dhcp_server":            pi.DHCPServer,
		"asset_id":               pi.AssetID,
		"total_mono_impressions": pi.TotalMonoImpressions,
		"black_impressions":      pi.BlackImpressions,
		"cyan_impressions":       pi.CyanImpressions,
		"magenta_impressions":    pi.MagentaImpressions,
		"yellow_impressions":     pi.YellowImpressions,
		"toner_level_black":      pi.TonerLevelBlack,
		"toner_level_cyan":       pi.TonerLevelCyan,
		"toner_level_magenta":    pi.TonerLevelMagenta,
		"toner_level_yellow":     pi.TonerLevelYellow,
		"toner_desc_black":       pi.TonerDescBlack,
		"toner_desc_cyan":        pi.TonerDescCyan,
		"toner_desc_magenta":     pi.TonerDescMagenta,
		"toner_desc_yellow":      pi.TonerDescYellow,
		"open_ports":             pi.OpenPorts,
		"advertised_services":    pi.AdvertisedServices,
		"detection_reasons":      pi.DetectionReasons,
		"mono_impressions":       pi.MonoImpressions,
		"color_impressions":      pi.ColorImpressions,
		"uptime_seconds":         pi.UptimeSeconds,
		"duplex_supported":       pi.DuplexSupported,
		"paper_tray_status":      pi.PaperTrayStatus,
		"toner_alerts":           pi.TonerAlerts,
		"paper_trays":            pi.PaperTrays,
		"vendor_metrics":         pi.VendorMetrics,
		"meters":                 pi.Meters,
		"learned_oids":           pi.LearnedOIDs, // Store learned OIDs for efficient metrics
		// Capabilities
		"is_color":    pi.IsColor,
		"is_mono":     pi.IsMono,
		"is_copier":   pi.IsCopier,
		"is_scanner":  pi.IsScanner,
		"is_fax":      pi.IsFax,
		"is_laser":    pi.IsLaser,
		"is_inkjet":   pi.IsInkjet,
		"has_duplex":  pi.HasDuplex,
		"form_factor": pi.FormFactor,
		"device_type": pi.DeviceType,
	}

	return device
}

// DeviceToPrinterInfo converts storage.Device to agent.PrinterInfo
func DeviceToPrinterInfo(device *Device) agent.PrinterInfo {
	pi := agent.PrinterInfo{
		Serial:       device.Serial,
		IP:           device.IP,
		Manufacturer: device.Manufacturer,
		Model:        device.Model,
		Hostname:     device.Hostname,
		Firmware:     device.Firmware,
		MAC:          device.MACAddress,
		SubnetMask:   device.SubnetMask,
		Gateway:      device.Gateway,
		DNSServers:   device.DNSServers,
		DHCPServer:   device.DHCPServer,
		// NOTE: PageCount and TonerLevels retrieved from latest MetricsSnapshot, not Device
		Consumables:    device.Consumables,
		StatusMessages: device.StatusMessages,
		LastSeen:       device.LastSeen,
		WebUIURL:       device.WebUIURL,
	}

	// Extract from RawData if present
	if device.RawData != nil {
		if value, ok := device.RawData["vendor_metrics"].(map[string]interface{}); ok {
			pi.VendorMetrics = value
		}
		if v, ok := device.RawData["admin_contact"].(string); ok {
			pi.AdminContact = v
		}
		if v, ok := device.RawData["asset_id"].(string); ok {
			pi.AssetID = v
		}
		if v, ok := device.RawData["total_mono_impressions"].(float64); ok {
			pi.TotalMonoImpressions = int(v)
		} else if v, ok := device.RawData["total_mono_impressions"].(int); ok {
			pi.TotalMonoImpressions = v
		}
		if v, ok := device.RawData["black_impressions"].(float64); ok {
			pi.BlackImpressions = int(v)
		} else if v, ok := device.RawData["black_impressions"].(int); ok {
			pi.BlackImpressions = v
		}
		if v, ok := device.RawData["uptime_seconds"].(float64); ok {
			pi.UptimeSeconds = int(v)
		} else if v, ok := device.RawData["uptime_seconds"].(int); ok {
			pi.UptimeSeconds = v
		}
		if v, ok := device.RawData["duplex_supported"].(bool); ok {
			pi.DuplexSupported = v
		}
		// Extract learned OIDs for efficient metrics collection
		if v, ok := device.RawData["learned_oids"].(map[string]interface{}); ok {
			learnedOIDs := agent.LearnedOIDMap{}
			if pageCountOID, ok := v["page_count_oid"].(string); ok {
				learnedOIDs.PageCountOID = pageCountOID
			}
			if monoPagesOID, ok := v["mono_pages_oid"].(string); ok {
				learnedOIDs.MonoPagesOID = monoPagesOID
			}
			if colorPagesOID, ok := v["color_pages_oid"].(string); ok {
				learnedOIDs.ColorPagesOID = colorPagesOID
			}
			if cyanOID, ok := v["cyan_oid"].(string); ok {
				learnedOIDs.CyanOID = cyanOID
			}
			if magentaOID, ok := v["magenta_oid"].(string); ok {
				learnedOIDs.MagentaOID = magentaOID
			}
			if yellowOID, ok := v["yellow_oid"].(string); ok {
				learnedOIDs.YellowOID = yellowOID
			}
			if tonerOIDPrefix, ok := v["toner_oid_prefix"].(string); ok {
				learnedOIDs.TonerOIDPrefix = tonerOIDPrefix
			}
			if serialOID, ok := v["serial_oid"].(string); ok {
				learnedOIDs.SerialOID = serialOID
			}
			if modelOID, ok := v["model_oid"].(string); ok {
				learnedOIDs.ModelOID = modelOID
			}
			if vendorOIDs, ok := v["vendor_specific_oids"].(map[string]interface{}); ok {
				learnedOIDs.VendorSpecificOIDs = make(map[string]string)
				for key, val := range vendorOIDs {
					if oid, ok := val.(string); ok {
						learnedOIDs.VendorSpecificOIDs[key] = oid
					}
				}
			}
			pi.LearnedOIDs = learnedOIDs
		}
		// Add more field extractions as needed

		// Populate per-color toner fields and TonerLevels map from RawData when present
		// Some devices / older runs stored color-specific toner values in raw_data
		// under keys like "toner_level_black" (number) and "toner_desc_black" (string).
		// Map those into the PrinterInfo so UI code that expects TonerLevels or
		// TonerLevel* fields will find them.
		tonerMap := map[string]int{}
		// helper to read numeric value from raw_data
		readInt := func(key string) (int, bool) {
			if v, ok := device.RawData[key].(float64); ok {
				return int(v), true
			}
			if v, ok := device.RawData[key].(int); ok {
				return v, true
			}
			return 0, false
		}

		if v, ok := readInt("toner_level_black"); ok {
			pi.TonerLevelBlack = v
			tonerMap["Black"] = v
		}
		if v, ok := readInt("toner_level_cyan"); ok {
			pi.TonerLevelCyan = v
			tonerMap["Cyan"] = v
		}
		if v, ok := readInt("toner_level_magenta"); ok {
			pi.TonerLevelMagenta = v
			tonerMap["Magenta"] = v
		}
		if v, ok := readInt("toner_level_yellow"); ok {
			pi.TonerLevelYellow = v
			tonerMap["Yellow"] = v
		}

		// copy toner descriptions if present
		if v, ok := device.RawData["toner_desc_black"].(string); ok {
			pi.TonerDescBlack = v
		}
		if v, ok := device.RawData["toner_desc_cyan"].(string); ok {
			pi.TonerDescCyan = v
		}
		if v, ok := device.RawData["toner_desc_magenta"].(string); ok {
			pi.TonerDescMagenta = v
		}
		if v, ok := device.RawData["toner_desc_yellow"].(string); ok {
			pi.TonerDescYellow = v
		}

		if len(tonerMap) > 0 {
			pi.TonerLevels = tonerMap
		}

		// Extract capability fields
		if v, ok := device.RawData["is_color"].(bool); ok {
			pi.IsColor = v
		}
		if v, ok := device.RawData["is_mono"].(bool); ok {
			pi.IsMono = v
		}
		if v, ok := device.RawData["is_copier"].(bool); ok {
			pi.IsCopier = v
		}
		if v, ok := device.RawData["is_scanner"].(bool); ok {
			pi.IsScanner = v
		}
		if v, ok := device.RawData["is_fax"].(bool); ok {
			pi.IsFax = v
		}
		if v, ok := device.RawData["is_laser"].(bool); ok {
			pi.IsLaser = v
		}
		if v, ok := device.RawData["is_inkjet"].(bool); ok {
			pi.IsInkjet = v
		}
		if v, ok := device.RawData["has_duplex"].(bool); ok {
			pi.HasDuplex = v
		}
		if v, ok := device.RawData["form_factor"].(string); ok {
			pi.FormFactor = v
		}
		if v, ok := device.RawData["device_type"].(string); ok {
			pi.DeviceType = v
		}
	}

	return pi
}

// PrinterInfoToScanSnapshot converts agent.PrinterInfo to storage.ScanSnapshot
// Note: Only device state (IP, hostname, firmware) is stored in scan_history.
// Metrics data (page counts, toner levels) should be stored separately using SaveMetricsSnapshot.
func PrinterInfoToScanSnapshot(pi agent.PrinterInfo) *ScanSnapshot {
	snapshot := &ScanSnapshot{
		Serial:          pi.Serial,
		CreatedAt:       pi.LastSeen,
		IP:              pi.IP,
		Hostname:        pi.Hostname,
		Firmware:        pi.Firmware,
		Consumables:     pi.Consumables,
		StatusMessages:  pi.StatusMessages,
		DiscoveryMethod: "snmp",
	}

	if len(pi.DiscoveryMethods) > 0 {
		snapshot.DiscoveryMethod = pi.DiscoveryMethods[0]
	}

	return snapshot
}

// PrinterInfoToMetricsSnapshot converts agent.PrinterInfo to storage.MetricsSnapshot
// This extracts only metrics data (page counts, toner levels) for time-series storage.
func PrinterInfoToMetricsSnapshot(pi agent.PrinterInfo) *MetricsSnapshot {
	snapshot := &MetricsSnapshot{}

	// Set common MetricsSnapshot fields
	snapshot.Serial = pi.Serial
	snapshot.Timestamp = pi.LastSeen
	snapshot.PageCount = pi.PageCount
	snapshot.ColorPages = pi.ColorImpressions
	snapshot.MonoPages = pi.MonoImpressions
	snapshot.TonerLevels = normalizeTonerLevels(pi)
	snapshot.ScanCount = pi.Meters["scans"]

	return snapshot
}

func normalizeTonerLevels(pi agent.PrinterInfo) map[string]interface{} {
	tonerLevels := make(map[string]interface{})
	add := func(key string, value int) {
		if key == "" {
			return
		}
		tonerLevels[key] = value
	}

	for k, v := range pi.TonerLevels {
		key := k
		if !supplies.PreserveDescription(k) && !strings.HasPrefix(k, "ink_") && !strings.HasPrefix(k, "supply_maintenance_box") {
			if normalized := supplies.NormalizeDescription(k); normalized != "" {
				key = normalized
			}
		}
		add(key, v)
	}

	ensure := func(key string, value int, desc string) {
		if key == "" {
			return
		}
		if _, exists := tonerLevels[key]; exists {
			return
		}
		if value == 0 && desc == "" {
			return
		}
		tonerLevels[key] = value
	}

	ensure("toner_black", pi.TonerLevelBlack, pi.TonerDescBlack)
	ensure("toner_cyan", pi.TonerLevelCyan, pi.TonerDescCyan)
	ensure("toner_magenta", pi.TonerLevelMagenta, pi.TonerDescMagenta)
	ensure("toner_yellow", pi.TonerLevelYellow, pi.TonerDescYellow)

	return tonerLevels
}
