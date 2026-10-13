package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Metrics holds simple counters to help diagnose scanning behavior.
type Metrics struct {
	mu                  sync.Mutex `json:"-"`
	TotalHostsScanned   int        `json:"total_hosts_scanned"`
	DetectedPrinters    int        `json:"detected_printers"`
	MissingManufacturer int        `json:"missing_manufacturer"`
	DeepWalksPerformed  int        `json:"deep_walks_performed"`
	DetectionErrors     int        `json:"detection_errors"`
	LastUpdated         string     `json:"last_updated"`
}

var metrics = &Metrics{}

// MetricsSnapshot is a copy of the metrics suitable for JSON marshalling
// and for returning outside the package without copying internal locks.
type MetricsSnapshot struct {
	TotalHostsScanned   int    `json:"total_hosts_scanned"`
	DetectedPrinters    int    `json:"detected_printers"`
	MissingManufacturer int    `json:"missing_manufacturer"`
	DeepWalksPerformed  int    `json:"deep_walks_performed"`
	DetectionErrors     int    `json:"detection_errors"`
	LastUpdated         string `json:"last_updated"`
}

func (m *Metrics) snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MetricsSnapshot{
		TotalHostsScanned:   m.TotalHostsScanned,
		DetectedPrinters:    m.DetectedPrinters,
		MissingManufacturer: m.MissingManufacturer,
		DeepWalksPerformed:  m.DeepWalksPerformed,
		DetectionErrors:     m.DetectionErrors,
		LastUpdated:         m.LastUpdated,
	}
}

func IncTotalHostsScanned() {
	metrics.mu.Lock()
	metrics.TotalHostsScanned++
	metrics.LastUpdated = time.Now().Format(time.RFC3339)
	metrics.mu.Unlock()
}

func IncDetectedPrinters() {
	metrics.mu.Lock()
	metrics.DetectedPrinters++
	metrics.LastUpdated = time.Now().Format(time.RFC3339)
	metrics.mu.Unlock()
}

func IncMissingManufacturer() {
	metrics.mu.Lock()
	metrics.MissingManufacturer++
	metrics.LastUpdated = time.Now().Format(time.RFC3339)
	metrics.mu.Unlock()
}

func IncDeepWalks() {
	metrics.mu.Lock()
	metrics.DeepWalksPerformed++
	metrics.LastUpdated = time.Now().Format(time.RFC3339)
	metrics.mu.Unlock()
}

func IncDetectionErrors() {
	metrics.mu.Lock()
	metrics.DetectionErrors++
	metrics.LastUpdated = time.Now().Format(time.RFC3339)
	metrics.mu.Unlock()
}

// PersistMetrics writes current metrics snapshot to logs/scan_metrics.json
func PersistMetrics() error {
	s := metrics.snapshot()
	logDir := ensureLogDir()
	fn := filepath.Join(logDir, "scan_metrics.json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fn, data, 0o644)
}

// GetMetricsSnapshot returns a copy of the current metrics for inspection.
func GetMetricsSnapshot() MetricsSnapshot {
	return metrics.snapshot()
}

// DeviceMetricsSnapshot represents a point-in-time snapshot of device metrics for historical tracking
type DeviceMetricsSnapshot struct {
	Observation *PrinterInfo           `json:"-"`
	Serial      string                 `json:"serial"`
	PageCount   int                    `json:"page_count"`
	ColorPages  int                    `json:"color_pages"`
	MonoPages   int                    `json:"mono_pages"`
	ScanCount   int                    `json:"scan_count"`
	TonerLevels map[string]interface{} `json:"toner_levels"`

	// Additional detailed impression counters (HP-specific and others)
	FaxPages          int `json:"fax_pages,omitempty"`
	CopyPages         int `json:"copy_pages,omitempty"`
	OtherPages        int `json:"other_pages,omitempty"` // Calculated: Total - Fax - Copy
	CopyMonoPages     int `json:"copy_mono_pages,omitempty"`
	CopyFlatbedScans  int `json:"copy_flatbed_scans,omitempty"`
	CopyADFScans      int `json:"copy_adf_scans,omitempty"`
	FaxFlatbedScans   int `json:"fax_flatbed_scans,omitempty"`
	FaxADFScans       int `json:"fax_adf_scans,omitempty"`
	ScanToHostFlatbed int `json:"scan_to_host_flatbed,omitempty"`
	ScanToHostADF     int `json:"scan_to_host_adf,omitempty"`
	DuplexSheets      int `json:"duplex_sheets,omitempty"`
	JamEvents         int `json:"jam_events,omitempty"`
	ScannerJamEvents  int `json:"scanner_jam_events,omitempty"`
}

// OLD CollectMetricsSnapshot removed (~157 lines) - replaced by CollectMetrics in scanner_api.go
