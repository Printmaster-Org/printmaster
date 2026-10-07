package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// StageCommitStore is optional: existing DeviceStore implementations and mocks
// need not support staged scanner commits. Callers may notify uploaders only
// after CommitScannerFacts returns nil. Nil means the transaction committed;
// supplied metrics may be omitted by the SaveMetricsSnapshot validation policy.
type StageCommitStore interface {
	CommitScannerFacts(ctx context.Context, serial, expectedIP string, patch DevicePatch, scan *ScanSnapshot, metrics *MetricsSnapshot) error
}

// DevicePatch contains only obtained scanner facts. Nil means absent; empty
// strings/collections do not erase existing facts. ValidatedSerial must come
// from scanner identity validation, not a requested/assumed serial. It must
// equal the commit serial and is required to create a device or change its IP.
// IP is the observed destination, NOT the expected prior address. LastSeen
// without ValidatedSerial is supported for existing liveness callers: the caller
// must have validated identity at that address. A TCP response or a matching DB
// address alone cannot prove identity; storage cannot verify network evidence.
// User state, classification, spooler properties and page-count baselines are
// deliberately not patchable. RawData is recursively merged, not replaced.
type DevicePatch struct {
	ValidatedSerial                             *string
	IP, Manufacturer, Model, Hostname, Firmware *string
	MACAddress, SubnetMask, Gateway, DHCPServer *string
	DiscoveryMethod, WalkFilename, WebUIURL     *string
	DNSServers, Consumables, StatusMessages     *[]string
	RawData                                     *map[string]interface{}
	LastSeen                                    *time.Time
}

var (
	ErrExpectedIPMismatch = errors.New("device IP no longer matches scanner target")
)

var _ StageCommitStore = (*SQLiteStore)(nil)

// CommitScannerFacts atomically merges a partial device patch and only the
// snapshots supplied by the caller. No snapshot or last-seen time is invented.
// For an existing device, expectedIP is a compare-and-set guard when nonempty;
// it compares the PRIOR stored IP, never the destination in patch.IP. Moving an
// existing device requires that guard and validated identity. Scan.IP must match
// the observed destination even when the device's IP field is locked. Liveness
// (LastSeen) always requires the guard. A new device requires a
// validated identity and a nonempty IP matching expectedIP when supplied.
// Other serials recorded at the destination are left untouched: the caller's
// identity was validated there, so they are stale (DHCP reuse/replacement).
// Hidden/saved devices remain hidden/saved. Locks are read inside the write tx.
func (s *SQLiteStore) CommitScannerFacts(ctx context.Context, serial, expectedIP string, patch DevicePatch, scan *ScanSnapshot, metrics *MetricsSnapshot) (err error) {
	fields, scans, metricRows := 0, 0, 0
	metricsOutcome := "absent"
	defer func() {
		if storageLogger != nil {
			outcome := "committed"
			if err != nil {
				outcome = "rejected"
				fields, scans, metricRows = 0, 0, 0
				metricsOutcome = "rolled_back"
			}
			// No identities, addresses, raw metadata or SQL errors in outcome logs.
			storageLogger.Debug("Scanner facts commit", "outcome", outcome, "fields", fields, "scans", scans, "metrics", metricRows, "metrics_outcome", metricsOutcome)
		}
	}()
	// Serial is an opaque key: never trim/sanitize into a different identity.
	// Reject path components and control bytes before they reach history/upload
	// consumers that also use serials as artifact keys.
	if !safeScannerSerial(serial) {
		return ErrInvalidSerial
	}
	if patch.ValidatedSerial != nil && *patch.ValidatedSerial != serial {
		return ErrInvalidSerial
	}
	if scan != nil && (scan.Serial != serial || scan.CreatedAt.IsZero()) {
		return fmt.Errorf("scan identity mismatch or missing timestamp")
	}
	if metrics != nil && (metrics.Serial != serial || metrics.Timestamp.IsZero()) {
		return fmt.Errorf("metrics identity mismatch or missing timestamp")
	}
	if patch.LastSeen != nil && (patch.LastSeen.IsZero() || expectedIP == "") {
		return fmt.Errorf("liveness requires timestamp and expected IP")
	}
	if patch.IP != nil && strings.TrimSpace(*patch.IP) != "" && patch.ValidatedSerial == nil {
		return fmt.Errorf("IP patch requires validated identity")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scanner commit: %w", err)
	}
	defer tx.Rollback()
	// Acquire the write reservation before reading, including for in-memory
	// stores: read/merge/write must not race a user edit or another scanner.
	if _, err = tx.ExecContext(ctx, "UPDATE devices SET serial = serial WHERE serial = ?", serial); err != nil {
		return err
	}
	var ip string
	var locksJSON, rawJSON sql.NullString
	var lastSeen time.Time
	err = tx.QueryRowContext(ctx, "SELECT ip, locked_fields, raw_data, last_seen FROM devices WHERE serial = ?", serial).Scan(&ip, &locksJSON, &rawJSON, &lastSeen)
	creating := errors.Is(err, sql.ErrNoRows)
	if creating {
		if patch.ValidatedSerial == nil {
			return ErrNotFound
		}
		if patch.IP == nil || strings.TrimSpace(*patch.IP) == "" {
			return fmt.Errorf("new identity requires IP")
		}
		ip = *patch.IP
		if expectedIP != "" && !scannerAddressesEqual(ip, expectedIP) {
			return ErrExpectedIPMismatch
		}
	} else if err != nil {
		return err
	}
	if expectedIP != "" && !scannerAddressesEqual(ip, expectedIP) {
		return ErrExpectedIPMismatch
	}
	destination := ip
	if patch.IP != nil && strings.TrimSpace(*patch.IP) != "" {
		destination = *patch.IP
	}
	if !creating && !scannerAddressesEqual(ip, destination) && expectedIP == "" {
		return fmt.Errorf("address move requires expected prior IP")
	}
	if scan != nil && !scannerAddressesEqual(scan.IP, destination) {
		return fmt.Errorf("scan IP mismatch")
	}
	// No UNIQUE(ip) constraint exists; liveness touches require a serial match,
	// so a stale row sharing this address cannot be refreshed by this commit.
	shared, err := countScannerOccupants(ctx, tx, serial, destination)
	if err != nil {
		return err
	}
	if shared > 0 && storageLogger != nil {
		storageLogger.Info("Scanner target shared with stale devices", "stale_devices", shared)
	}
	var locks []FieldLock
	if locksJSON.Valid && locksJSON.String != "" && locksJSON.String != "null" {
		if err = json.Unmarshal([]byte(locksJSON.String), &locks); err != nil {
			return fmt.Errorf("decode field locks: %w", err)
		}
	}
	locked := make(map[string]bool, len(locks))
	for _, lock := range locks {
		locked[lock.Field] = true
	}
	if creating {
		now := time.Now()
		// Identity acquisition is not a liveness observation. Persist zero unless
		// the caller supplied LastSeen; never invent scan/metrics snapshots.
		if patch.LastSeen != nil {
			lastSeen = *patch.LastSeen
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO devices (serial, ip, last_seen, created_at, first_seen, is_saved, visible, device_type, source_type) VALUES (?, ?, ?, ?, ?, 0, 1, 'network', 'snmp')`, serial, ip, lastSeen, now, now); err != nil {
			return err
		}
	}
	var sets []string
	var args []interface{}
	add := func(column string, value interface{}) {
		if !locked[column] {
			sets = append(sets, column+" = ?")
			args = append(args, value)
		}
	}
	text := func(column string, value *string) {
		if value != nil && strings.TrimSpace(*value) != "" {
			add(column, *value)
		}
	}
	text("ip", patch.IP)
	text("manufacturer", patch.Manufacturer)
	text("model", patch.Model)
	text("hostname", patch.Hostname)
	text("firmware", patch.Firmware)
	text("mac_address", patch.MACAddress)
	text("subnet_mask", patch.SubnetMask)
	text("gateway", patch.Gateway)
	text("dhcp_server", patch.DHCPServer)
	text("discovery_method", patch.DiscoveryMethod)
	text("walk_filename", patch.WalkFilename)
	text("web_ui_url", patch.WebUIURL)
	list := func(column string, value *[]string) {
		if value == nil || locked[column] {
			return
		}
		var nonempty []string
		for _, item := range *value {
			if strings.TrimSpace(item) != "" {
				nonempty = append(nonempty, item)
			}
		}
		if len(nonempty) > 0 {
			data, _ := json.Marshal(nonempty)
			add(column, string(data))
		}
	}
	list("dns_servers", patch.DNSServers)
	list("consumables", patch.Consumables)
	list("status_messages", patch.StatusMessages)
	if patch.LastSeen != nil && patch.LastSeen.After(lastSeen) {
		add("last_seen", *patch.LastSeen)
	}
	if patch.RawData != nil && len(*patch.RawData) > 0 && !locked["raw_data"] {
		previous := make(map[string]interface{})
		if rawJSON.Valid && rawJSON.String != "" && rawJSON.String != "null" {
			if err = json.Unmarshal([]byte(rawJSON.String), &previous); err != nil {
				return fmt.Errorf("decode raw metadata: %w", err)
			}
		}
		// JSON round trip clones caller data and rejects unsupported values.
		data, marshalErr := json.Marshal(*patch.RawData)
		if marshalErr != nil {
			return marshalErr
		}
		var incoming map[string]interface{}
		if err = json.Unmarshal(data, &incoming); err != nil {
			return err
		}
		mergeScannerMetadata(previous, incoming)
		data, err = json.Marshal(previous)
		if err != nil {
			return err
		}
		add("raw_data", string(data))
	}
	fields = len(sets)
	if fields > 0 {
		args = append(args, serial)
		if _, err = tx.ExecContext(ctx, "UPDATE devices SET "+strings.Join(sets, ", ")+" WHERE serial = ?", args...); err != nil {
			return err
		}
	}
	if scan != nil {
		if err = s.addScanHistoryWithExecer(ctx, tx, scan); err != nil {
			return err
		}
		scans = 1
	}
	if metrics != nil {
		// Use the legacy drop policy, but read through THIS transaction. Calling
		// SaveMetricsSnapshot here reads s.db outside the reservation (and can
		// deadlock single-connection stores). Never mutate the caller's snapshot.
		metricsOutcome, err = validateScannerMetrics(ctx, tx, metrics)
		if err != nil {
			return err
		}
		if metricsOutcome != "stored" {
			// Fixed reason only: no serial, target, counters, metadata or SQL error.
			if storageLogger != nil {
				storageLogger.Warn("Scanner metrics omitted", "reason", metricsOutcome)
			}
			return tx.Commit()
		}
		toner, marshalErr := json.Marshal(metrics.TonerLevels)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metrics_raw (serial, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			serial, metrics.Timestamp.UTC().Format(time.RFC3339Nano), metrics.PageCount, metrics.ColorPages, metrics.MonoPages, metrics.ScanCount, string(toner))
		if err != nil {
			return err
		}
		metricRows = 1
	}
	return tx.Commit()
}

func safeScannerSerial(serial string) bool {
	if serial == "" || serial != strings.TrimSpace(serial) || serial == "." || serial == ".." ||
		!utf8.ValidString(serial) || strings.ContainsAny(serial, `/\`) {
		return false
	}
	return strings.IndexFunc(serial, unicode.IsControl) < 0
}

func scannerAddressesEqual(a, b string) bool {
	aa, aerr := netip.ParseAddr(a)
	bb, berr := netip.ParseAddr(b)
	if aerr == nil && berr == nil {
		return aa.Unmap() == bb.Unmap()
	}
	// Retain exact-match behavior for legacy non-address inventory strings.
	return a == b
}

func countScannerOccupants(ctx context.Context, tx *sql.Tx, serial, destination string) (int, error) {
	if destination == "" {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT ip FROM devices WHERE serial <> ? AND ip <> ''", serial)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return 0, err
		}
		if scannerAddressesEqual(ip, destination) {
			count++
		}
	}
	return count, rows.Err()
}

// validateScannerMetrics mirrors SaveMetricsSnapshot's two drop rules and zero
// counter policy. Kept local because this slice owns only staged storage; changes
// to the legacy policy must update this implementation and the parity tests too.
// A small decrease is tolerated (5%, minimum 10); counters are not clamped or
// synthesized. SQL read failures abort instead of silently bypassing validation.
func validateScannerMetrics(ctx context.Context, tx *sql.Tx, snapshot *MetricsSnapshot) (string, error) {
	if snapshot.PageCount == 0 && snapshot.ColorPages == 0 && snapshot.MonoPages == 0 && snapshot.ScanCount == 0 {
		return "zero_counters", nil
	}
	var previous [4]int
	err := tx.QueryRowContext(ctx, `SELECT page_count, color_pages, mono_pages, scan_count
		FROM metrics_raw WHERE serial = ? ORDER BY timestamp DESC, id DESC LIMIT 1`, snapshot.Serial).
		Scan(&previous[0], &previous[1], &previous[2], &previous[3])
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	incoming := [4]int{snapshot.PageCount, snapshot.ColorPages, snapshot.MonoPages, snapshot.ScanCount}
	for i, value := range incoming {
		if previous[i] <= 0 || value >= previous[i] {
			continue
		}
		threshold := previous[i] / 20
		if threshold < 10 {
			threshold = 10
		}
		if previous[i]-value > threshold {
			return "counter_decrease", nil
		}
	}
	if snapshot.PageCount > 0 && (snapshot.ColorPages > 0 || snapshot.MonoPages > 0) {
		parts := snapshot.ColorPages + snapshot.MonoPages
		if parts > 0 {
			diff := snapshot.PageCount - parts
			if diff < 0 {
				diff = -diff
			}
			tolerance := snapshot.PageCount / 10
			if tolerance < 100 {
				tolerance = 100
			}
			if diff > tolerance {
				return "parts_mismatch", nil
			}
		}
	}
	return "stored", nil
}

func mergeScannerMetadata(dst, src map[string]interface{}) {
	for key, value := range src {
		switch v := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
		case []interface{}:
			if len(v) == 0 {
				continue
			}
		case map[string]interface{}:
			if len(v) == 0 {
				continue
			}
			old, ok := dst[key].(map[string]interface{})
			if !ok {
				old = make(map[string]interface{})
			}
			mergeScannerMetadata(old, v)
			if len(old) > 0 {
				dst[key] = old
			}
			continue
		}
		dst[key] = value
	}
}
