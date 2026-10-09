package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestClassifyStatusMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		messages    []string
		wantError   bool
		wantWarning bool
		wantJam     bool
	}{
		{"nil messages", nil, false, false, false},
		{"empty messages", []string{}, false, false, false},
		{"jam message", []string{"Paper Jam"}, true, false, true},
		{"jam lowercase", []string{"paper jam detected"}, true, false, true},
		{"error message", []string{"Device Error"}, true, false, false},
		{"fail message", []string{"Print Failed"}, true, false, false},
		{"offline message", []string{"Device Offline"}, true, false, false},
		{"warning message", []string{"Warning: Low toner"}, false, true, false},
		{"low message", []string{"Toner Low"}, false, true, false},
		{"warn message", []string{"Warn: Check paper"}, false, true, false},
		{"multiple with jam", []string{"Ready", "Paper Jam", "Low Toner"}, true, true, true},
		{"multiple no jam", []string{"Ready", "Error Found", "Low Toner"}, true, true, false},
		{"clean messages", []string{"Ready", "Online", "Idle"}, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasError, hasWarning, hasJam := classifyStatusMessages(tt.messages)
			if hasError != tt.wantError {
				t.Errorf("hasError = %v, want %v", hasError, tt.wantError)
			}
			if hasWarning != tt.wantWarning {
				t.Errorf("hasWarning = %v, want %v", hasWarning, tt.wantWarning)
			}
			if hasJam != tt.wantJam {
				t.Errorf("hasJam = %v, want %v", hasJam, tt.wantJam)
			}
		})
	}
}

func TestBandForPercentage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pct  float64
		want consumableBand
	}{
		{"0% is critical", 0, consumableCritical},
		{"5% is critical", 5, consumableCritical},
		{"6% is low", 6, consumableLow},
		{"15% is low", 15, consumableLow},
		{"16% is medium", 16, consumableMedium},
		{"60% is medium", 60, consumableMedium},
		{"61% is high", 61, consumableHigh},
		{"100% is high", 100, consumableHigh},
		{"negative is critical", -5, consumableCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bandForPercentage(tt.pct)
			if got != tt.want {
				t.Errorf("bandForPercentage(%v) = %v, want %v", tt.pct, got, tt.want)
			}
		})
	}
}

func TestPercentFromString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  float64
		ok    bool
	}{
		{"simple number", "75", 75, true},
		{"with percent sign", "75%", 75, true},
		{"with percent and space", "75 %", 75, true},
		{"decimal", "75.5", 75.5, true},
		{"decimal with percent", "75.5%", 75.5, true},
		{"leading text", "Level: 75", 75, true},
		{"only text", "empty", 0, false},
		{"empty string", "", 0, false},
		{"negative extracts digits", "-10", 10, true}, // Function extracts digits only
		{"zero", "0", 0, true},
		{"complex string", "Toner at 42% remaining", 42, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := percentFromString(tt.input)
			if ok != tt.ok {
				t.Errorf("percentFromString(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("percentFromString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizePercentage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input interface{}
		want  float64
		ok    bool
	}{
		{"float64", float64(75.5), 75.5, true},
		{"float32", float32(75.5), 75.5, true},
		{"int", int(75), 75, true},
		{"int64", int64(75), 75, true},
		{"json.Number valid", json.Number("75.5"), 75.5, true},
		{"json.Number invalid", json.Number("abc"), 0, false},
		{"string with percent", "75%", 75, true},
		{"string number", "42", 42, true},
		{"nil", nil, 0, false},
		{"unsupported type", struct{}{}, 0, false},
		{"bool", true, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizePercentage(tt.input)
			if ok != tt.ok {
				t.Errorf("normalizePercentage(%v) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("normalizePercentage(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBandFromTonerLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels map[string]interface{}
		want   consumableBand
	}{
		{"nil levels", nil, consumableUnknown},
		{"empty levels", map[string]interface{}{}, consumableUnknown},
		{"all high", map[string]interface{}{"black": 90, "cyan": 85, "magenta": 95, "yellow": 88}, consumableHigh},
		{"one critical", map[string]interface{}{"black": 3, "cyan": 85, "magenta": 95, "yellow": 88}, consumableCritical},
		{"one low", map[string]interface{}{"black": 10, "cyan": 85, "magenta": 95, "yellow": 88}, consumableLow},
		{"one medium", map[string]interface{}{"black": 40, "cyan": 85, "magenta": 95, "yellow": 88}, consumableMedium},
		{"mixed with critical worst", map[string]interface{}{"black": 5, "cyan": 40, "magenta": 80}, consumableCritical},
		{"string percentages", map[string]interface{}{"black": "75%", "cyan": "10%"}, consumableLow},
		{"invalid values ignored", map[string]interface{}{"black": "invalid", "cyan": 80}, consumableHigh},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bandFromTonerLevels(tt.levels)
			if got != tt.want {
				t.Errorf("bandFromTonerLevels(%v) = %v, want %v", tt.levels, got, tt.want)
			}
		})
	}
}

func TestBandFromConsumableStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []string
		want    consumableBand
	}{
		{"nil entries", nil, consumableUnknown},
		{"empty entries", []string{}, consumableUnknown},
		{"high/full", []string{"Toner Full"}, consumableHigh},
		{"high/ok", []string{"Status OK"}, consumableHigh},
		{"high/ready", []string{"Ready"}, consumableHigh},
		{"medium", []string{"Medium Level"}, consumableMedium},
		{"half", []string{"Half Full"}, consumableMedium},
		{"low", []string{"Low Toner"}, consumableLow},
		{"very low", []string{"Very Low"}, consumableCritical},
		{"near empty", []string{"Near Empty"}, consumableCritical},
		{"empty", []string{"Toner Empty"}, consumableCritical},
		{"replace", []string{"Replace Toner"}, consumableCritical},
		{"exhausted", []string{"Exhausted"}, consumableCritical},
		{"depleted", []string{"Depleted"}, consumableCritical},
		{"percentage string", []string{"75%"}, consumableHigh},
		{"low percentage string", []string{"5%"}, consumableCritical},
		{"mixed worst wins", []string{"Full", "Low", "Replace"}, consumableCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bandFromConsumableStrings(tt.entries)
			if got != tt.want {
				t.Errorf("bandFromConsumableStrings(%v) = %v, want %v", tt.entries, got, tt.want)
			}
		})
	}
}

func TestClassifyConsumableBand(t *testing.T) {
	t.Parallel()

	// Helper to create device with consumables
	makeDevice := func(consumables []string) *Device {
		d := &Device{}
		d.Consumables = consumables
		return d
	}

	tests := []struct {
		name     string
		snapshot *MetricsSnapshot
		device   *Device
		want     consumableBand
	}{
		{"both nil", nil, nil, consumableUnknown},
		{"snapshot nil, device nil", nil, nil, consumableUnknown},
		{
			"snapshot with toner levels",
			&MetricsSnapshot{TonerLevels: map[string]interface{}{"black": 80}},
			nil,
			consumableHigh,
		},
		{
			"snapshot empty, device has consumables",
			&MetricsSnapshot{TonerLevels: map[string]interface{}{}},
			makeDevice([]string{"Toner Low"}),
			consumableLow,
		},
		{
			"snapshot takes precedence",
			&MetricsSnapshot{TonerLevels: map[string]interface{}{"black": 5}},
			makeDevice([]string{"Toner Full"}),
			consumableCritical,
		},
		{
			"device only",
			nil,
			makeDevice([]string{"Toner Full"}),
			consumableHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyConsumableBand(tt.snapshot, tt.device)
			if got != tt.want {
				t.Errorf("classifyConsumableBand() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildPlaceholderListFrom(t *testing.T) {
	t.Parallel()

	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	tests := []struct {
		name     string
		count    int
		startIdx int
		want     string
	}{
		{"zero count", 0, 1, ""},
		{"negative count", -1, 1, ""},
		{"single placeholder", 1, 1, "?"},
		{"three placeholders", 3, 1, "?,?,?"},
		{"five placeholders", 5, 1, "?,?,?,?,?"},
		{"different start index", 3, 5, "?,?,?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.buildPlaceholderListFrom(tt.count, tt.startIdx)
			if got != tt.want {
				t.Errorf("buildPlaceholderListFrom(%d, %d) = %q, want %q", tt.count, tt.startIdx, got, tt.want)
			}
		})
	}
}

func TestGetAggregatedMetrics(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// First register an agent
	agent := &Agent{
		AgentID:         "test-agent-1",
		Name:            "Test Agent",
		Hostname:        "agent-host",
		IP:              "192.168.1.100",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Status:          "active",
		Token:           "test-token",
		TenantID:        "tenant-1",
	}
	if err := s.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Create a device
	device := &Device{
		AgentID: "test-agent-1",
	}
	device.Serial = "DEV001"
	device.IP = "192.168.1.50"
	device.Manufacturer = "HP"
	device.Model = "LaserJet"
	device.StatusMessages = []string{"Ready"}

	if err := s.UpsertDevice(ctx, device); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}

	// Save some metrics
	metrics := &MetricsSnapshot{
		Serial:     "DEV001",
		AgentID:    "test-agent-1",
		Timestamp:  time.Now().UTC(),
		PageCount:  1000,
		ColorPages: 300,
		MonoPages:  700,
		ScanCount:  50,
		TonerLevels: map[string]interface{}{
			"black": 75,
			"cyan":  60,
		},
	}
	if err := s.SaveMetrics(ctx, metrics); err != nil {
		t.Fatalf("SaveMetrics: %v", err)
	}

	// Get aggregated metrics with no tenant filter
	since := time.Now().UTC().Add(-24 * time.Hour)
	agg, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics: %v", err)
	}
	if agg == nil {
		t.Fatal("expected aggregated metrics, got nil")
	}
	if agg.Fleet.Totals.Agents != 1 {
		t.Errorf("expected 1 agent, got %d", agg.Fleet.Totals.Agents)
	}
	if agg.Fleet.Totals.Devices != 1 {
		t.Errorf("expected 1 device, got %d", agg.Fleet.Totals.Devices)
	}

	// Get aggregated metrics with tenant filter
	agg2, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{TenantIDs: []string{"tenant-1"}})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics with tenant filter: %v", err)
	}
	if agg2.Fleet.Totals.Agents != 1 {
		t.Errorf("expected 1 agent with tenant filter, got %d", agg2.Fleet.Totals.Agents)
	}

	// Get aggregated metrics with non-matching tenant filter
	agg3, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{TenantIDs: []string{"other-tenant"}})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics with non-matching tenant: %v", err)
	}
	if agg3.Fleet.Totals.Agents != 0 {
		t.Errorf("expected 0 agents with non-matching tenant, got %d", agg3.Fleet.Totals.Agents)
	}
}

func TestGetAggregatedMetrics_EmptyDatabase(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	since := time.Now().UTC().Add(-24 * time.Hour)

	agg, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics on empty db: %v", err)
	}
	if agg == nil {
		t.Fatal("expected aggregated metrics, got nil")
	}
	if agg.Fleet.Totals.Agents != 0 {
		t.Errorf("expected 0 agents, got %d", agg.Fleet.Totals.Agents)
	}
	if agg.Fleet.Totals.Devices != 0 {
		t.Errorf("expected 0 devices, got %d", agg.Fleet.Totals.Devices)
	}
}

func TestGetAggregatedMetrics_WithStatusMessages(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Register agent
	agent := &Agent{
		AgentID:         "test-agent",
		Name:            "Test",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Status:          "active",
		Token:           "token",
	}
	s.RegisterAgent(ctx, agent)

	// Create devices with various statuses
	devices := []struct {
		serial   string
		messages []string
	}{
		{"DEV001", []string{"Ready"}},
		{"DEV002", []string{"Paper Jam"}},
		{"DEV003", []string{"Error: Service Required"}},
		{"DEV004", []string{"Warning: Low Toner"}},
	}

	for _, d := range devices {
		dev := &Device{AgentID: "test-agent"}
		dev.Serial = d.serial
		dev.IP = "192.168.1.1"
		dev.StatusMessages = d.messages
		s.UpsertDevice(ctx, dev)
	}

	since := time.Now().UTC().Add(-24 * time.Hour)
	agg, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics: %v", err)
	}

	// Check status counts - jam counts as error
	// DEV002 (jam) = error+jam, DEV003 (error) = error, DEV004 (warning) = warning
	if agg.Fleet.Statuses.Error != 2 {
		t.Errorf("expected 2 error statuses (jam+error), got %d", agg.Fleet.Statuses.Error)
	}
	if agg.Fleet.Statuses.Jam != 1 {
		t.Errorf("expected 1 jam status, got %d", agg.Fleet.Statuses.Jam)
	}
	if agg.Fleet.Statuses.Warning != 1 {
		t.Errorf("expected 1 warning status, got %d", agg.Fleet.Statuses.Warning)
	}
}

func TestGetAggregatedMetrics_DeltaHistoryWithPreSeed(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Register agent
	agent := &Agent{
		AgentID:         "test-agent",
		Name:            "Test",
		Platform:        "linux",
		Version:         "1.0.0",
		ProtocolVersion: "1",
		Status:          "active",
		Token:           "token",
	}
	s.RegisterAgent(ctx, agent)

	// Create device
	dev := &Device{AgentID: "test-agent"}
	dev.Serial = "DEV001"
	dev.IP = "192.168.1.1"
	s.UpsertDevice(ctx, dev)

	now := time.Now().UTC()

	// Create a metric BEFORE the query window (2 hours ago)
	// This should be used as the seed value for delta calculation
	seedMetric := &MetricsSnapshot{
		Serial:     "DEV001",
		AgentID:    "test-agent",
		Timestamp:  now.Add(-2 * time.Hour),
		PageCount:  1000,
		ColorPages: 300,
		MonoPages:  700,
		ScanCount:  50,
	}
	if err := s.SaveMetrics(ctx, seedMetric); err != nil {
		t.Fatalf("SaveMetrics (seed): %v", err)
	}

	// Create a metric WITHIN the query window (30 minutes ago)
	windowMetric := &MetricsSnapshot{
		Serial:     "DEV001",
		AgentID:    "test-agent",
		Timestamp:  now.Add(-30 * time.Minute),
		PageCount:  1100, // +100 pages
		ColorPages: 350,  // +50 color
		MonoPages:  750,  // +50 mono
		ScanCount:  60,   // +10 scans
	}
	if err := s.SaveMetrics(ctx, windowMetric); err != nil {
		t.Fatalf("SaveMetrics (window): %v", err)
	}

	// Query for the last hour only - the seed metric is outside this window
	since := now.Add(-1 * time.Hour)

	agg, err := s.GetAggregatedMetrics(ctx, since, AggregatedMetricsFilter{})
	if err != nil {
		t.Fatalf("GetAggregatedMetrics: %v", err)
	}

	// The history should contain delta values calculated from the pre-seeded metric
	// Even though the seed metric is outside the query window
	if len(agg.Fleet.History.TotalImpressions) == 0 {
		t.Fatal("expected TotalImpressions history to have data, got empty")
	}

	// Check that we got the delta (100 pages) in the bucket
	totalDelta := int64(0)
	for _, pt := range agg.Fleet.History.TotalImpressions {
		totalDelta += pt.Value
	}
	if totalDelta != 100 {
		t.Errorf("expected total delta of 100 pages, got %d", totalDelta)
	}

	// Also verify color delta
	colorDelta := int64(0)
	for _, pt := range agg.Fleet.History.ColorImpressions {
		colorDelta += pt.Value
	}
	if colorDelta != 50 {
		t.Errorf("expected color delta of 50 pages, got %d", colorDelta)
	}
}

// TestGetAggregatedMetrics_AgentAndDeviceFilters verifies that agent and device
// filters scope totals and history to the selected subset instead of returning
// the whole fleet, and that filters combine with AND semantics.
func TestGetAggregatedMetrics_AgentAndDeviceFilters(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	fixtures := []struct {
		agentID, tenantID, serial string
		pages                     int
	}{
		{"agent-a", "tenant-1", "SER-A1", 100},
		{"agent-a", "tenant-1", "SER-A2", 200},
		{"agent-b", "tenant-2", "SER-B1", 400},
	}
	since := time.Now().UTC().Add(-2 * time.Hour)
	for _, f := range fixtures {
		agent := &Agent{AgentID: f.agentID, Name: f.agentID, Hostname: f.agentID, Platform: "linux", Version: "1.0.0", ProtocolVersion: "1", Status: "active", Token: "tok-" + f.agentID, TenantID: f.tenantID}
		if err := s.RegisterAgent(ctx, agent); err != nil {
			t.Fatalf("RegisterAgent %s: %v", f.agentID, err)
		}
		device := &Device{AgentID: f.agentID}
		device.Serial = f.serial
		if err := s.UpsertDevice(ctx, device); err != nil {
			t.Fatalf("UpsertDevice %s: %v", f.serial, err)
		}
		// Two samples inside the window produce a history delta equal to pages.
		for i, count := range []int{1000, 1000 + f.pages} {
			snap := &MetricsSnapshot{Serial: f.serial, AgentID: f.agentID, Timestamp: since.Add(time.Duration(i+1) * time.Minute), PageCount: count}
			if err := s.SaveMetrics(ctx, snap); err != nil {
				t.Fatalf("SaveMetrics %s: %v", f.serial, err)
			}
		}
	}

	sumHistory := func(agg *AggregatedMetrics) int64 {
		var total int64
		for _, p := range agg.Fleet.History.TotalImpressions {
			total += p.Value
		}
		return total
	}

	cases := []struct {
		name                  string
		filter                AggregatedMetricsFilter
		agents, devices       int
		pageTotal, historySum int64
	}{
		{"unfiltered", AggregatedMetricsFilter{}, 2, 3, 3700, 700},
		{"agent", AggregatedMetricsFilter{AgentID: "agent-a"}, 1, 2, 2300, 300},
		{"device", AggregatedMetricsFilter{DeviceSerial: "SER-B1"}, 1, 1, 1400, 400},
		{"agent and own device", AggregatedMetricsFilter{AgentID: "agent-a", DeviceSerial: "SER-A2"}, 1, 1, 1200, 200},
		{"agent and foreign device", AggregatedMetricsFilter{AgentID: "agent-a", DeviceSerial: "SER-B1"}, 0, 0, 0, 0},
		{"tenant excludes agent", AggregatedMetricsFilter{TenantIDs: []string{"tenant-2"}, AgentID: "agent-a"}, 0, 0, 0, 0},
		{"unknown device", AggregatedMetricsFilter{DeviceSerial: "missing"}, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		agg, err := s.GetAggregatedMetrics(ctx, since, tc.filter)
		if err != nil {
			t.Fatalf("%s: GetAggregatedMetrics: %v", tc.name, err)
		}
		if agg.Fleet.Totals.Agents != tc.agents || agg.Fleet.Totals.Devices != tc.devices {
			t.Errorf("%s: agents/devices = %d/%d, want %d/%d", tc.name, agg.Fleet.Totals.Agents, agg.Fleet.Totals.Devices, tc.agents, tc.devices)
		}
		if agg.Fleet.Totals.PageCount != tc.pageTotal {
			t.Errorf("%s: page total = %d, want %d", tc.name, agg.Fleet.Totals.PageCount, tc.pageTotal)
		}
		if got := sumHistory(agg); got != tc.historySum {
			t.Errorf("%s: history sum = %d, want %d", tc.name, got, tc.historySum)
		}
	}
}
