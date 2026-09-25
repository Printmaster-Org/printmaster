// Package report provides types and utilities for device data issue reporting.
// Reports are submitted to a proxy service which creates GitHub Gists for
// attaching diagnostic data to GitHub issues.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"regexp"
	"strings"
	"time"
)

// IssueType categorizes the kind of data issue being reported.
type IssueType string

const (
	IssueWrongManufacturer IssueType = "wrong_manufacturer"
	IssueWrongModel        IssueType = "wrong_model"
	IssueMissingSerial     IssueType = "missing_serial"
	IssueWrongSerial       IssueType = "wrong_serial"
	IssueIncorrectCounters IssueType = "incorrect_counters"
	IssueMissingToner      IssueType = "missing_toner"
	IssueMissingSupplies   IssueType = "missing_supplies"
	IssueWrongHostname     IssueType = "wrong_hostname"
	IssueOther             IssueType = "other"
)

// String returns a human-readable label for the issue type.
func (t IssueType) String() string {
	switch t {
	case IssueWrongManufacturer:
		return "Wrong Manufacturer"
	case IssueWrongModel:
		return "Wrong/Missing Model"
	case IssueMissingSerial:
		return "Missing Serial Number"
	case IssueWrongSerial:
		return "Wrong Serial Number"
	case IssueIncorrectCounters:
		return "Incorrect Page Counters"
	case IssueMissingToner:
		return "Missing Toner Levels"
	case IssueMissingSupplies:
		return "Missing Supply Info"
	case IssueWrongHostname:
		return "Wrong/Missing Hostname"
	case IssueOther:
		return "Other Issue"
	default:
		return string(t)
	}
}

// DiagnosticReport contains all data needed to report a device data issue.
// This is sent to the proxy service for Gist creation.
type DiagnosticReport struct {
	// Report metadata
	ReportID  string    `json:"report_id"`
	Timestamp time.Time `json:"timestamp"`

	// Agent/environment info
	AgentVersion string `json:"agent_version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`

	// Issue classification (user-provided)
	IssueType     IssueType `json:"issue_type"`
	ExpectedValue string    `json:"expected_value,omitempty"` // What user thinks it should be
	UserMessage   string    `json:"user_message,omitempty"`   // Additional notes

	// Device identification (anonymized as needed)
	DeviceIP     string `json:"device_ip"`               // Private IPs kept, public anonymized
	DeviceSerial string `json:"device_serial,omitempty"` // Kept for identification
	DeviceModel  string `json:"device_model,omitempty"`
	DeviceMAC    string `json:"device_mac,omitempty"`

	// Current detected values (basic)
	CurrentManufacturer string `json:"current_manufacturer,omitempty"`
	CurrentModel        string `json:"current_model,omitempty"`
	CurrentSerial       string `json:"current_serial,omitempty"`
	CurrentHostname     string `json:"current_hostname,omitempty"`
	CurrentPageCount    int    `json:"current_page_count,omitempty"`

	// Extended device info (all available data)
	Firmware        string   `json:"firmware,omitempty"`
	SubnetMask      string   `json:"subnet_mask,omitempty"`
	Gateway         string   `json:"gateway,omitempty"`
	Consumables     []string `json:"consumables,omitempty"`
	StatusMessages  []string `json:"status_messages,omitempty"`
	DiscoveryMethod string   `json:"discovery_method,omitempty"`
	WebUIURL        string   `json:"web_ui_url,omitempty"`
	DeviceType      string   `json:"device_type,omitempty"` // network, usb, local, shared, virtual
	SourceType      string   `json:"source_type,omitempty"` // snmp, spooler, manual
	IsUSB           bool     `json:"is_usb,omitempty"`
	PortName        string   `json:"port_name,omitempty"` // USB001, LPT1:, etc.
	DriverName      string   `json:"driver_name,omitempty"`
	IsDefault       bool     `json:"is_default,omitempty"`
	IsShared        bool     `json:"is_shared,omitempty"`
	SpoolerStatus   string   `json:"spooler_status,omitempty"`

	// Metrics data
	ColorPages  int                    `json:"color_pages,omitempty"`
	MonoPages   int                    `json:"mono_pages,omitempty"`
	ScanCount   int                    `json:"scan_count,omitempty"`
	TonerLevels map[string]interface{} `json:"toner_levels,omitempty"`

	// Diagnostic data for developers
	DetectedVendor   string   `json:"detected_vendor,omitempty"`
	VendorConfidence float64  `json:"vendor_confidence,omitempty"`
	DetectionSteps   []string `json:"detection_steps,omitempty"`

	// Raw SNMP data (the core diagnostic info)
	SNMPResponses []SNMPResponse `json:"snmp_responses,omitempty"`

	// Recent relevant log entries
	RecentLogs []string `json:"recent_logs,omitempty"`

	// Full raw_data from device (catch-all for any extra fields)
	RawData map[string]interface{} `json:"raw_data,omitempty"`

	// === Extended Diagnostic Data (separate gist files) ===

	// DeviceRecord contains the full device database entry for debugging
	DeviceRecord map[string]interface{} `json:"device_record,omitempty"`

	// MetricsHistory contains the last N metrics snapshots for trend analysis
	MetricsHistory []map[string]interface{} `json:"metrics_history,omitempty"`

	// AgentLogs contains recent agent log entries (separate from RecentLogs for clarity)
	AgentLogs []string `json:"agent_logs,omitempty"`

	// ServerLogs contains recent server log entries (when report submitted via server)
	ServerLogs []string `json:"server_logs,omitempty"`

	// USBProxyInfo contains USB-specific device information for USB printers.
	// This helps debug IPP-USB connectivity and web scraping issues.
	USBProxyInfo *USBProxyInfo `json:"usb_proxy_info,omitempty"`
}

// USBProxyInfo contains USB device information for diagnostic purposes.
// This is populated when the device is a USB printer to help debug IPP-USB issues.
type USBProxyInfo struct {
	// DevicePath is the OS-specific USB device path (WinUSB path on Windows)
	DevicePath string `json:"device_path,omitempty"`

	// VendorID is the USB Vendor ID (e.g., 0x0482 for Kyocera)
	VendorID uint16 `json:"vendor_id,omitempty"`

	// VendorIDHex is the vendor ID as a hex string for readability
	VendorIDHex string `json:"vendor_id_hex,omitempty"`

	// ProductID is the USB Product ID
	ProductID uint16 `json:"product_id,omitempty"`

	// ProductIDHex is the product ID as a hex string for readability
	ProductIDHex string `json:"product_id_hex,omitempty"`

	// USBManufacturer is the manufacturer string from USB descriptor
	USBManufacturer string `json:"usb_manufacturer,omitempty"`

	// USBProduct is the product name from USB descriptor
	USBProduct string `json:"usb_product,omitempty"`

	// USBSerial is the serial number from USB descriptor
	USBSerial string `json:"usb_serial,omitempty"`

	// InterfaceNumber is the USB interface that supports IPP-USB
	InterfaceNumber uint8 `json:"interface_number,omitempty"`

	// SpoolerPortName is the Windows spooler port (e.g., "USB001")
	SpoolerPortName string `json:"spooler_port_name,omitempty"`

	// Status indicates the current USB connection status
	Status string `json:"status,omitempty"`

	// DetectedVendorScraper is the vendor scraper selected for web UI parsing
	DetectedVendorScraper string `json:"detected_vendor_scraper,omitempty"`

	// ProbeResults contains results from probing USB web endpoints
	ProbeResults []USBProbeResult `json:"probe_results,omitempty"`

	// ProbeError contains any error encountered during probing
	ProbeError string `json:"probe_error,omitempty"`
}

// USBProbeResult represents the result of probing a single USB web endpoint.
type USBProbeResult struct {
	// Path is the URL path that was probed (e.g., "/status.html")
	Path string `json:"path"`

	// Description explains what this endpoint is for
	Description string `json:"description,omitempty"`

	// StatusCode is the HTTP response status code (0 if connection failed)
	StatusCode int `json:"status_code"`

	// ContentType is the Content-Type header from the response
	ContentType string `json:"content_type,omitempty"`

	// Size is the response body size in bytes
	Size int `json:"size,omitempty"`

	// Error contains any error message if the probe failed
	Error string `json:"error,omitempty"`

	// Preview contains a truncated preview of the response body (first 500 bytes)
	Preview string `json:"preview,omitempty"`
}

// SNMPResponse represents a single SNMP OID/value pair from device query.
type SNMPResponse struct {
	OID      string `json:"oid"`
	Type     string `json:"type"`                // Integer, OctetString, etc.
	Value    string `json:"value"`               // String representation
	HexValue string `json:"hex_value,omitempty"` // For binary data
}

// ProxyRequest is sent to the Cloudflare Worker proxy.
type ProxyRequest struct {
	Report DiagnosticReport `json:"report"`
}

// ProxyResponse is returned by the Cloudflare Worker proxy.
type ProxyResponse struct {
	Success  bool   `json:"success"`
	GistURL  string `json:"gist_url,omitempty"`
	IssueURL string `json:"issue_url,omitempty"` // Pre-filled GitHub issue URL
	Error    string `json:"error,omitempty"`
}

// ProxyEndpoint is the URL of the diagnostic report proxy service.
const ProxyEndpoint = "https://api.printmaster.work/diagnostic"

// GitHubIssueBase is the base URL for creating new issues.
const GitHubIssueBase = "https://github.com/printmaster-org/printmaster/issues/new"

// Anonymizer provides utilities for sanitizing sensitive data in reports.
type Anonymizer struct {
	// Salt for hashing (should be consistent per installation for correlation)
	Salt string
}

// NewAnonymizer creates an anonymizer with the given salt.
func NewAnonymizer(salt string) *Anonymizer {
	return &Anonymizer{Salt: salt}
}

// AnonymizeIP returns the IP unchanged if private, or hashes it if public.
func (a *Anonymizer) AnonymizeIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip // Invalid IP, return as-is
	}

	// Keep private IPs as-is
	if isPrivateIP(parsed) {
		return ip
	}

	// Hash public IPs
	return a.hash(ip)[:16] + ".anon"
}

// AnonymizeHostname removes or hashes hostnames that might identify a company.
func (a *Anonymizer) AnonymizeHostname(hostname string) string {
	if hostname == "" {
		return ""
	}

	// Keep generic printer hostnames
	lh := strings.ToLower(hostname)
	genericPrefixes := []string{"printer", "hp", "canon", "brother", "epson", "xerox", "ricoh", "kyocera", "lexmark"}
	for _, prefix := range genericPrefixes {
		if strings.HasPrefix(lh, prefix) {
			return hostname
		}
	}

	// Hash anything that looks like a company/custom name
	return a.hash(hostname)[:12] + ".hostname"
}

// hash creates a SHA256 hash of the input with salt.
func (a *Anonymizer) hash(input string) string {
	h := sha256.New()
	h.Write([]byte(a.Salt + input))
	return hex.EncodeToString(h.Sum(nil))
}

// isPrivateIP checks if an IP is in a private range.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	// Check IPv4 private ranges
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 127.0.0.0/8 (loopback)
		if ip4[0] == 127 {
			return true
		}
		// 169.254.0.0/16 (link-local)
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
	}

	// Check IPv6 private ranges
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// fc00::/7 (unique local)
	if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return true
	}

	return false
}

// SanitizeLogEntry removes potentially sensitive info from log lines.
func SanitizeLogEntry(entry string, anon *Anonymizer) string {
	// Remove email addresses
	emailRegex := regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	entry = emailRegex.ReplaceAllString(entry, "[email]")

	// Anonymize public IPs in log entries
	ipRegex := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	entry = ipRegex.ReplaceAllStringFunc(entry, func(ip string) string {
		return anon.AnonymizeIP(ip)
	})

	return entry
}

// BuildGitHubIssueURL creates a pre-filled GitHub issue URL.
func BuildGitHubIssueURL(report *DiagnosticReport, gistURL string) string {
	// Build title
	vendor := report.DetectedVendor
	if vendor == "" {
		vendor = "Unknown"
	}
	model := report.DeviceModel
	if model == "" {
		model = report.DeviceIP
	}

	title := "[Device Report] " + vendor + " - " + model

	// Build body
	var body strings.Builder
	body.WriteString("## Issue Type\n")
	body.WriteString(report.IssueType.String())
	body.WriteString("\n\n")

	if report.ExpectedValue != "" {
		body.WriteString("## Expected Value\n")
		body.WriteString(report.ExpectedValue)
		body.WriteString("\n\n")
	}

	if report.UserMessage != "" {
		body.WriteString("## Description\n")
		body.WriteString(report.UserMessage)
		body.WriteString("\n\n")
	}

	body.WriteString("## Current Values\n")
	body.WriteString("- **Manufacturer:** " + report.CurrentManufacturer + "\n")
	body.WriteString("- **Model:** " + report.CurrentModel + "\n")
	body.WriteString("- **Serial:** " + report.CurrentSerial + "\n")
	body.WriteString("\n")

	body.WriteString("## Diagnostic Data\n")
	body.WriteString("📎 [View Full Diagnostic Gist](")
	body.WriteString(gistURL)
	body.WriteString(")\n\n")

	body.WriteString("---\n")
	body.WriteString("*Report ID: " + report.ReportID + "*\n")
	body.WriteString("*Agent Version: " + report.AgentVersion + "*\n")

	// URL encode for query params
	return GitHubIssueBase +
		"?template=device-report.yml" +
		"&title=" + urlEncode(title) +
		"&gist=" + urlEncode(gistURL) +
		"&issue_type=" + urlEncode(string(report.IssueType)) +
		"&expected=" + urlEncode(report.ExpectedValue) +
		"&description=" + urlEncode(report.UserMessage)
}

// urlEncode performs basic URL encoding for query parameters.
func urlEncode(s string) string {
	// Basic replacements for URL safety
	s = strings.ReplaceAll(s, " ", "%20")
	s = strings.ReplaceAll(s, "\n", "%0A")
	s = strings.ReplaceAll(s, "#", "%23")
	s = strings.ReplaceAll(s, "&", "%26")
	s = strings.ReplaceAll(s, "=", "%3D")
	s = strings.ReplaceAll(s, "?", "%3F")
	return s
}
