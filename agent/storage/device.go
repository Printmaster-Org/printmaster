package storage

import (
	commonstorage "printmaster/common/storage"
)

// Re-export common types for backward compatibility
type FieldLock = commonstorage.FieldLock
type DeviceFilter = commonstorage.DeviceFilter

// Device represents a printer device with agent-specific fields
type Device struct {
	commonstorage.Device                        // Embed common fields
	ReportedMetadata     map[string]interface{} `json:"-"`

	// Agent-specific fields
	IsSaved      bool                      `json:"is_saved"`
	Visible      bool                      `json:"visible"` // false = hidden from UI (soft delete)
	WalkFilename string                    `json:"walk_filename,omitempty"`
	LastScanID   int64                     `json:"last_scan_id,omitempty"`  // FK to most recent scan_history entry
	LockedFields []commonstorage.FieldLock `json:"locked_fields,omitempty"` // Fields that should not be auto-updated
}
