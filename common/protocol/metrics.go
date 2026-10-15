package protocol

// MetricsBatchResult acknowledges persistence, not just receipt of an upload.
type MetricsBatchResult struct {
	Success  bool                  `json:"success"`
	Received int                   `json:"received"`
	Stored   int                   `json:"stored"`
	Failures []MetricsBatchFailure `json:"failures,omitempty"`
}

type MetricsBatchFailure struct {
	Index  int    `json:"index"`
	Serial string `json:"serial,omitempty"`
	Code   string `json:"code"`
}

const (
	MetricsInvalidRecord     = "invalid_record"
	MetricsOwnershipConflict = "ownership_conflict"
	MetricsStorageError      = "storage_error"
)
