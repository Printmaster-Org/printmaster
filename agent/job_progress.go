package main

import (
	"context"
	"sync"
	"time"

	"printmaster/agent/agent"
	"printmaster/agent/storage"
	wscommon "printmaster/common/ws"
)

// JobStatus represents the current status of a background job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

// JobProgress represents the progress state of a background job.
type JobProgress struct {
	JobID    string                 `json:"job_id"`
	JobType  string                 `json:"job_type"`
	Status   JobStatus              `json:"status"`
	Progress int                    `json:"progress"` // 0-100
	Message  string                 `json:"message"`
	Error    string                 `json:"error,omitempty"`
	Result   map[string]interface{} `json:"result,omitempty"`
}

// backgroundJobs tracks active jobs and their progress.
var (
	backgroundJobs   = make(map[string]*JobProgress)
	backgroundJobsMu sync.RWMutex
)

// generateJobID creates a unique job ID.
func generateJobID() string {
	return time.Now().Format("20060102150405") + "-" + randomHex(8)
}

// randomHex generates a random hex string of the specified length.
func randomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[time.Now().UnixNano()%16]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}

// registerJob registers a new background job and returns its ID.
func registerJob(jobType string) string {
	jobID := generateJobID()
	job := &JobProgress{
		JobID:    jobID,
		JobType:  jobType,
		Status:   JobStatusPending,
		Progress: 0,
		Message:  "Job registered",
	}

	backgroundJobsMu.Lock()
	backgroundJobs[jobID] = job
	backgroundJobsMu.Unlock()

	return jobID
}

// getJob retrieves a job's current progress.
func getJob(jobID string) *JobProgress {
	backgroundJobsMu.RLock()
	defer backgroundJobsMu.RUnlock()
	if job, ok := backgroundJobs[jobID]; ok {
		// Return a copy to avoid race conditions
		copy := *job
		return &copy
	}
	return nil
}

// cleanupJob removes a job from tracking after a delay.
func cleanupJob(jobID string, delay time.Duration) {
	time.AfterFunc(delay, func() {
		backgroundJobsMu.Lock()
		delete(backgroundJobs, jobID)
		backgroundJobsMu.Unlock()
	})
}

// sendJobProgress sends a job progress update via local SSE and to the server via WebSocket.
// This allows the UI to show real-time progress for long-running operations.
func sendJobProgress(jobID, jobType string, status JobStatus, progress int, message string, errMsg string, result map[string]interface{}) {
	// Update local job tracking
	backgroundJobsMu.Lock()
	if job, ok := backgroundJobs[jobID]; ok {
		job.Status = status
		job.Progress = progress
		job.Message = message
		job.Error = errMsg
		job.Result = result
	}
	backgroundJobsMu.Unlock()

	data := map[string]interface{}{
		"job_id":   jobID,
		"job_type": jobType,
		"status":   string(status),
		"progress": progress,
		"message":  message,
	}
	if errMsg != "" {
		data["error"] = errMsg
	}
	if result != nil {
		data["result"] = result
	}

	// Broadcast to local SSE clients (agent UI)
	if sseHub != nil {
		sseHub.Broadcast(SSEEvent{
			Type: "job_progress",
			Data: data,
		})
	}

	// Also send to server via WebSocket if connected
	uploadWorkerMu.RLock()
	worker := uploadWorker
	uploadWorkerMu.RUnlock()

	if worker == nil {
		return
	}

	wsClient := worker.WSClient()
	if wsClient == nil || !wsClient.IsConnected() {
		return
	}

	msg := wscommon.Message{
		Type:      wscommon.MessageTypeJobProgress,
		Data:      data,
		Timestamp: time.Now(),
	}

	if sendErr := wsClient.SendMessage(msg); sendErr != nil {
		// Log but don't fail - progress reporting is best-effort
		_ = sendErr
	}
}

// collectMetricsAsync runs metrics collection in the background with progress updates.
// This is called when the /devices/metrics/collect endpoint receives async=true.
func collectMetricsAsync(jobID, serial, ip string, device *storage.Device) {
	jobType := "metrics_collect"

	// Clean up job after 5 minutes
	defer cleanupJob(jobID, 5*time.Minute)

	// Send initial running status
	sendJobProgress(jobID, jobType, JobStatusRunning, 5, "Starting metrics collection...", "", nil)

	// Check for USB device type
	if device != nil && (device.DeviceType == "usb" || device.IsUSB) && usbProxySupported() {
		sendJobProgress(jobID, jobType, JobStatusRunning, 10, "Detected USB device, collecting metrics...", "", nil)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		storageSnapshot, ok := usbProxyMetricsSnapshot(ctx, serial)
		if !ok {
			appLogger.Warn("USB metrics collection failed", "serial", serial)
			sendJobProgress(jobID, jobType, JobStatusFailed, 0, "USB metrics collection failed", "USB metrics unavailable", nil)
			return
		}

		sendJobProgress(jobID, jobType, JobStatusRunning, 80, "Saving metrics to database...", "", nil)

		// Save to database
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer saveCancel()
		if err := deviceStore.SaveMetricsSnapshot(saveCtx, storageSnapshot); err != nil {
			appLogger.Warn("Failed to save USB metrics", "serial", serial, "error", err.Error())
			sendJobProgress(jobID, jobType, JobStatusFailed, 0, "Failed to save metrics", err.Error(), nil)
			return
		}

		appLogger.Info("USB metrics collected and saved", "serial", serial, "total_pages", storageSnapshot.PageCount)
		sendJobProgress(jobID, jobType, JobStatusCompleted, 100, "USB metrics collected successfully", "", map[string]interface{}{
			"serial":      serial,
			"total_pages": storageSnapshot.PageCount,
			"source":      "usb",
		})
		return
	}

	// Network device - use SNMP metrics collection
	if ip == "" {
		sendJobProgress(jobID, jobType, JobStatusFailed, 0, "IP address required for network device", "ip required", nil)
		return
	}

	sendJobProgress(jobID, jobType, JobStatusRunning, 10, "Looking up vendor information...", "", nil)

	// Get vendor hint from database
	vendorHint := ""
	if deviceStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		d, getErr := deviceStore.Get(ctx, serial)
		cancel()
		if getErr == nil && d != nil {
			vendorHint = d.Manufacturer
		}
	}

	sendJobProgress(jobID, jobType, JobStatusRunning, 20, "Connecting to device via SNMP...", "", nil)

	// Use new scanner for metrics collection with extended context
	jobCtx := context.Background()
	if mainScanner != nil {
		jobCtx = mainScanner.ctx
	}
	metricsCtx, cancelMetrics := context.WithTimeout(jobCtx, 60*time.Second)
	defer cancelMetrics()

	appLogger.Info("Collecting metrics (async)", "serial", serial, "ip", ip, "vendor_hint", vendorHint)

	sendJobProgress(jobID, jobType, JobStatusRunning, 30, "Querying device metrics...", "", nil)

	agentSnapshot, err := CollectMetrics(metricsCtx, ip, serial, vendorHint, 10)
	if err != nil {
		appLogger.Warn("Metrics collection failed", "serial", serial, "ip", ip, "error", err.Error())
		if agent.DebugEnabled {
			agent.Debug("POST /devices/metrics/collect (async) - FAILED for " + serial + " (" + ip + "): " + err.Error())
		}
		sendJobProgress(jobID, jobType, JobStatusFailed, 0, "Metrics collection failed", err.Error(), nil)
		return
	}

	sendJobProgress(jobID, jobType, JobStatusRunning, 80, "Processing metrics data...", "", nil)

	storageSnapshot := scannerStorageMetrics(agentSnapshot, time.Now().UTC())

	sendJobProgress(jobID, jobType, JobStatusRunning, 90, "Saving metrics to database...", "", nil)

	// Save to database
	saveCtx, cancelSave := context.WithTimeout(metricsCtx, 10*time.Second)
	defer cancelSave()
	if err := saveScannerMetrics(saveCtx, ip, storageSnapshot); err != nil {
		sendJobProgress(jobID, jobType, JobStatusFailed, 0, "Failed to save metrics", err.Error(), nil)
		return
	}

	appLogger.Info("Metrics collected successfully (async)",
		"serial", serial,
		"ip", ip,
		"page_count", agentSnapshot.PageCount,
		"color_pages", agentSnapshot.ColorPages,
		"mono_pages", agentSnapshot.MonoPages,
		"scan_count", agentSnapshot.ScanCount)

	if agent.DebugEnabled {
		agent.Debug("POST /devices/metrics/collect (async) - SUCCESS for " + serial + " (" + ip + ")")
	}

	sendJobProgress(jobID, jobType, JobStatusCompleted, 100, "Metrics collected successfully", "", map[string]interface{}{
		"serial":      serial,
		"page_count":  agentSnapshot.PageCount,
		"color_pages": agentSnapshot.ColorPages,
		"mono_pages":  agentSnapshot.MonoPages,
		"scan_count":  agentSnapshot.ScanCount,
	})
}
