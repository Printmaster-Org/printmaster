package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	pmsettings "printmaster/common/settings"
)

// ErrOwnershipConflict means a machine write does not match the existing owner.
// Callers must bind machine identifiers to authenticated credentials before writing.
var ErrOwnershipConflict = errors.New("machine ownership conflict")

// BaseStore provides shared database operations that work across SQLite and PostgreSQL.
// It embeds a *sql.DB connection and a Dialect for handling SQL syntax differences.
//
// Query placeholders are written using SQLite style (?) and converted at runtime
// when using PostgreSQL. This allows a single codebase for all database operations.
type BaseStore struct {
	db      *sql.DB
	dialect Dialect
	dbPath  string // For SQLite compatibility (stores path or DSN)
}

// NewBaseStore creates a new BaseStore with the given database connection and dialect.
func NewBaseStore(db *sql.DB, dialect Dialect, dbPath string) *BaseStore {
	return &BaseStore{
		db:      db,
		dialect: dialect,
		dbPath:  dbPath,
	}
}

// DB returns the underlying database connection.
func (s *BaseStore) DB() *sql.DB {
	return s.db
}

// Dialect returns the SQL dialect being used.
func (s *BaseStore) Dialect() Dialect {
	return s.dialect
}

// Close closes the database connection.
func (s *BaseStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Path returns the database path (SQLite) or empty string (PostgreSQL).
func (s *BaseStore) Path() string {
	return s.dbPath
}

// query converts SQLite-style ? placeholders to the dialect's format and executes.
// This allows writing queries once with ? and having them work on both SQLite and Postgres.
func (s *BaseStore) query(q string) string {
	if s.dialect.Name() == "postgres" {
		return ConvertPlaceholders(q)
	}
	return q
}

// execContext wraps ExecContext with placeholder conversion.
func (s *BaseStore) execContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.query(query), args...)
}

// queryContext wraps QueryContext with placeholder conversion.
func (s *BaseStore) queryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.query(query), args...)
}

// queryRowContext wraps QueryRowContext with placeholder conversion.
func (s *BaseStore) queryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return s.db.QueryRowContext(ctx, s.query(query), args...)
}

// insertReturningID executes an INSERT and returns the generated ID.
// For PostgreSQL, it appends RETURNING id and uses QueryRow.
// For SQLite, it uses LastInsertId from the result.
func (s *BaseStore) insertReturningID(ctx context.Context, query string, args ...interface{}) (int64, error) {
	if s.dialect.Name() == "postgres" {
		// PostgreSQL: use RETURNING id
		query = s.query(query) + " RETURNING id"
		var id int64
		err := s.db.QueryRowContext(ctx, query, args...).Scan(&id)
		if err != nil {
			return 0, err
		}
		return id, nil
	}

	// SQLite: use LastInsertId
	result, err := s.execContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ============================================================================
// Agent Management Methods
// ============================================================================

// RegisterAgent registers a new agent or updates existing.
// Note: On conflict, the 'name' field is only updated if the existing name is empty,
// to preserve user-set display names.
func (s *BaseStore) RegisterAgent(ctx context.Context, agent *Agent) error {
	if agent == nil || agent.AgentID == "" || agent.Token == "" {
		return fmt.Errorf("%w: agent ID and token are required", ErrOwnershipConflict)
	}
	query := `
		INSERT INTO agents (
			agent_id, name, hostname, ip, platform, version, protocol_version, token, tenant_id,
			registered_at, last_seen, status,
			os_version, go_version, architecture, num_cpu, total_memory_mb,
			build_type, git_commit, last_heartbeat
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			name = CASE WHEN COALESCE(agents.name, '') = '' THEN excluded.name ELSE agents.name END,
			hostname = excluded.hostname,
			ip = excluded.ip,
			platform = excluded.platform,
			version = excluded.version,
			protocol_version = excluded.protocol_version,
			last_seen = excluded.last_seen,
			status = excluded.status,
			os_version = excluded.os_version,
			go_version = excluded.go_version,
			architecture = excluded.architecture,
			num_cpu = excluded.num_cpu,
			total_memory_mb = excluded.total_memory_mb,
			build_type = excluded.build_type,
			git_commit = excluded.git_commit,
			last_heartbeat = excluded.last_heartbeat
		WHERE agents.token = excluded.token
		  AND COALESCE(agents.tenant_id, '') = COALESCE(excluded.tenant_id, '')
		RETURNING id
	`

	// RETURNING is supported by both backends and returns no row when the
	// conflict guard rejects a write. LastInsertId cannot detect that case.
	var id int64
	err := s.queryRowContext(ctx, query,
		agent.AgentID, agent.Name, agent.Hostname, agent.IP, agent.Platform,
		agent.Version, agent.ProtocolVersion, agent.Token, agent.TenantID, agent.RegisteredAt,
		agent.LastSeen, agent.Status,
		agent.OSVersion, agent.GoVersion, agent.Architecture, agent.NumCPU,
		agent.TotalMemoryMB, agent.BuildType, agent.GitCommit, agent.LastHeartbeat).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: agent %s is already registered", ErrOwnershipConflict, agent.AgentID)
	}
	if err != nil {
		return err
	}

	// Set the auto-increment ID
	if id > 0 {
		agent.ID = id
	}

	return nil
}

// GetAgent retrieves an agent by ID
func (s *BaseStore) GetAgent(ctx context.Context, agentID string) (*Agent, error) {
	query := `
		SELECT id, agent_id, name, hostname, ip, platform, version, protocol_version,
		       token, tenant_id, registered_at, last_seen, status,
		       os_version, go_version, architecture, num_cpu, total_memory_mb,
		       build_type, git_commit, last_heartbeat, device_count,
		       last_device_sync, last_metrics_sync
		FROM agents
		WHERE agent_id = ?
	`

	var agent Agent
	var name, osVersion, goVersion, architecture, buildType, gitCommit sql.NullString
	var numCPU, deviceCount sql.NullInt64
	var totalMemoryMB sql.NullInt64
	var lastHeartbeat, lastDeviceSync, lastMetricsSync sql.NullTime
	var tenantID sql.NullString

	err := s.queryRowContext(ctx, query, agentID).Scan(
		&agent.ID, &agent.AgentID, &name, &agent.Hostname, &agent.IP,
		&agent.Platform, &agent.Version, &agent.ProtocolVersion,
		&agent.Token, &tenantID, &agent.RegisteredAt, &agent.LastSeen, &agent.Status,
		&osVersion, &goVersion, &architecture, &numCPU, &totalMemoryMB,
		&buildType, &gitCommit, &lastHeartbeat, &deviceCount,
		&lastDeviceSync, &lastMetricsSync,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent not found: %w", err)
	}
	if err != nil {
		return nil, err
	}

	// Handle nullable fields
	agent.Name = name.String
	agent.OSVersion = osVersion.String
	agent.GoVersion = goVersion.String
	agent.Architecture = architecture.String
	agent.BuildType = buildType.String
	agent.GitCommit = gitCommit.String
	agent.TenantID = tenantID.String
	if numCPU.Valid {
		agent.NumCPU = int(numCPU.Int64)
	}
	if totalMemoryMB.Valid {
		agent.TotalMemoryMB = totalMemoryMB.Int64
	}
	if deviceCount.Valid {
		agent.DeviceCount = int(deviceCount.Int64)
	}
	if lastHeartbeat.Valid {
		agent.LastHeartbeat = lastHeartbeat.Time
	}
	if lastDeviceSync.Valid {
		agent.LastDeviceSync = lastDeviceSync.Time
	}
	if lastMetricsSync.Valid {
		agent.LastMetricsSync = lastMetricsSync.Time
	}

	return &agent, nil
}

// GetAgentByToken retrieves an agent by its authentication token
func (s *BaseStore) GetAgentByToken(ctx context.Context, token string) (*Agent, error) {
	query := `
		SELECT id, agent_id, name, hostname, ip, platform, version, protocol_version,
		       token, tenant_id, registered_at, last_seen, status,
		       os_version, go_version, architecture, num_cpu, total_memory_mb,
		       build_type, git_commit, last_heartbeat, device_count,
		       last_device_sync, last_metrics_sync
		FROM agents
		WHERE token = ?
	`

	var agent Agent
	var name, osVersion, goVersion, architecture, buildType, gitCommit sql.NullString
	var numCPU, deviceCount sql.NullInt64
	var totalMemoryMB sql.NullInt64
	var lastHeartbeat, lastDeviceSync, lastMetricsSync sql.NullTime
	var tenantID sql.NullString

	err := s.queryRowContext(ctx, query, token).Scan(
		&agent.ID, &agent.AgentID, &name, &agent.Hostname, &agent.IP,
		&agent.Platform, &agent.Version, &agent.ProtocolVersion,
		&agent.Token, &tenantID, &agent.RegisteredAt, &agent.LastSeen, &agent.Status,
		&osVersion, &goVersion, &architecture, &numCPU, &totalMemoryMB,
		&buildType, &gitCommit, &lastHeartbeat, &deviceCount,
		&lastDeviceSync, &lastMetricsSync,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent not found")
	}
	if err != nil {
		return nil, err
	}

	agent.Name = name.String
	agent.OSVersion = osVersion.String
	agent.GoVersion = goVersion.String
	agent.Architecture = architecture.String
	agent.BuildType = buildType.String
	agent.GitCommit = gitCommit.String
	agent.TenantID = tenantID.String
	if numCPU.Valid {
		agent.NumCPU = int(numCPU.Int64)
	}
	if totalMemoryMB.Valid {
		agent.TotalMemoryMB = totalMemoryMB.Int64
	}
	if deviceCount.Valid {
		agent.DeviceCount = int(deviceCount.Int64)
	}
	if lastHeartbeat.Valid {
		agent.LastHeartbeat = lastHeartbeat.Time
	}
	if lastDeviceSync.Valid {
		agent.LastDeviceSync = lastDeviceSync.Time
	}
	if lastMetricsSync.Valid {
		agent.LastMetricsSync = lastMetricsSync.Time
	}

	return &agent, nil
}

// ListAgents returns all registered agents
func (s *BaseStore) ListAgents(ctx context.Context) ([]*Agent, error) {
	query := `
		SELECT id, agent_id, name, hostname, ip, platform, version, protocol_version,
		       token, tenant_id, registered_at, last_seen, status,
		       os_version, go_version, architecture, num_cpu, total_memory_mb,
		       build_type, git_commit, last_heartbeat, device_count,
		       last_device_sync, last_metrics_sync
		FROM agents
		ORDER BY last_seen DESC
	`

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		agent, err := s.scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}

	return agents, rows.Err()
}

// CountAgents returns the total number of agents (optionally filtered by tenant)
func (s *BaseStore) CountAgents(ctx context.Context, tenantIDs []string) (int64, error) {
	var count int64
	if len(tenantIDs) == 0 {
		err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&count)
		return count, err
	}
	placeholders := make([]string, len(tenantIDs))
	args := make([]interface{}, len(tenantIDs))
	for i, t := range tenantIDs {
		placeholders[i] = "?"
		args[i] = t
	}
	query := fmt.Sprintf(`SELECT COUNT(*) FROM agents WHERE tenant_id IN (%s)`, strings.Join(placeholders, ","))
	err := s.queryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// ListAgentsPaginated returns agents with limit/offset pagination
func (s *BaseStore) ListAgentsPaginated(ctx context.Context, limit, offset int, tenantIDs []string) ([]*Agent, error) {
	var query string
	var args []interface{}

	if len(tenantIDs) == 0 {
		query = `
			SELECT id, agent_id, name, hostname, ip, platform, version, protocol_version,
			       token, tenant_id, registered_at, last_seen, status,
			       os_version, go_version, architecture, num_cpu, total_memory_mb,
			       build_type, git_commit, last_heartbeat, device_count,
			       last_device_sync, last_metrics_sync
			FROM agents
			ORDER BY last_seen DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	} else {
		placeholders := make([]string, len(tenantIDs))
		for i, t := range tenantIDs {
			placeholders[i] = "?"
			args = append(args, t)
		}
		query = fmt.Sprintf(`
			SELECT id, agent_id, name, hostname, ip, platform, version, protocol_version,
			       token, tenant_id, registered_at, last_seen, status,
			       os_version, go_version, architecture, num_cpu, total_memory_mb,
			       build_type, git_commit, last_heartbeat, device_count,
			       last_device_sync, last_metrics_sync
			FROM agents
			WHERE tenant_id IN (%s)
			ORDER BY last_seen DESC
			LIMIT ? OFFSET ?
		`, strings.Join(placeholders, ","))
		args = append(args, limit, offset)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		agent, err := s.scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}

	return agents, rows.Err()
}

// scanAgent scans an agent from a row scanner (works with both *sql.Row and *sql.Rows)
func (s *BaseStore) scanAgent(rows *sql.Rows) (*Agent, error) {
	var agent Agent
	var name, osVersion, goVersion, architecture, buildType, gitCommit sql.NullString
	var numCPU, deviceCount sql.NullInt64
	var totalMemoryMB sql.NullInt64
	var lastHeartbeat, lastDeviceSync, lastMetricsSync sql.NullTime
	var tenantID sql.NullString

	err := rows.Scan(
		&agent.ID, &agent.AgentID, &name, &agent.Hostname, &agent.IP,
		&agent.Platform, &agent.Version, &agent.ProtocolVersion,
		&agent.Token, &tenantID, &agent.RegisteredAt, &agent.LastSeen, &agent.Status,
		&osVersion, &goVersion, &architecture, &numCPU, &totalMemoryMB,
		&buildType, &gitCommit, &lastHeartbeat, &deviceCount,
		&lastDeviceSync, &lastMetricsSync,
	)
	if err != nil {
		return nil, err
	}

	agent.Name = name.String
	agent.OSVersion = osVersion.String
	agent.GoVersion = goVersion.String
	agent.Architecture = architecture.String
	agent.BuildType = buildType.String
	agent.GitCommit = gitCommit.String
	agent.TenantID = tenantID.String
	if numCPU.Valid {
		agent.NumCPU = int(numCPU.Int64)
	}
	if totalMemoryMB.Valid {
		agent.TotalMemoryMB = totalMemoryMB.Int64
	}
	if deviceCount.Valid {
		agent.DeviceCount = int(deviceCount.Int64)
	}
	if lastHeartbeat.Valid {
		agent.LastHeartbeat = lastHeartbeat.Time
	}
	if lastDeviceSync.Valid {
		agent.LastDeviceSync = lastDeviceSync.Time
	}
	if lastMetricsSync.Valid {
		agent.LastMetricsSync = lastMetricsSync.Time
	}

	return &agent, nil
}

// UpdateAgentHeartbeat updates the last_seen and status for an agent
func (s *BaseStore) UpdateAgentHeartbeat(ctx context.Context, agentID string, status string) error {
	now := time.Now().UTC()
	_, err := s.execContext(ctx,
		`UPDATE agents SET last_seen = ?, last_heartbeat = ?, status = ? WHERE agent_id = ?`,
		now, now, status, agentID)
	return err
}

// UpdateAgentInfo updates agent metadata (version, platform, etc.) typically on heartbeat.
// Note: This does NOT update the 'name' field - that's managed separately via UpdateAgentName
// to preserve user-set display names from being overwritten by agent heartbeats.
func (s *BaseStore) UpdateAgentInfo(ctx context.Context, agent *Agent) error {
	query := `
		UPDATE agents SET
			hostname = ?,
			ip = ?,
			platform = ?,
			version = ?,
			protocol_version = ?,
			os_version = ?,
			go_version = ?,
			architecture = ?,
			num_cpu = ?,
			total_memory_mb = ?,
			build_type = ?,
			git_commit = ?,
			device_count = ?,
			last_seen = ?,
			last_heartbeat = ?,
			status = ?
		WHERE agent_id = ?
	`

	_, err := s.execContext(ctx, query,
		agent.Hostname, agent.IP, agent.Platform,
		agent.Version, agent.ProtocolVersion,
		agent.OSVersion, agent.GoVersion, agent.Architecture,
		agent.NumCPU, agent.TotalMemoryMB, agent.BuildType, agent.GitCommit,
		agent.DeviceCount, agent.LastSeen, agent.LastHeartbeat, agent.Status,
		agent.AgentID)

	return err
}

// UpdateAgentName updates the user-friendly name for an agent
func (s *BaseStore) UpdateAgentName(ctx context.Context, agentID string, name string) error {
	_, err := s.execContext(ctx, `UPDATE agents SET name = ? WHERE agent_id = ?`, name, agentID)
	return err
}

// DeleteAgent removes an agent by its ID
func (s *BaseStore) DeleteAgent(ctx context.Context, agentID string) error {
	_, err := s.execContext(ctx, `DELETE FROM agents WHERE agent_id = ?`, agentID)
	return err
}

// ============================================================================
// Device Management Methods
// ============================================================================

// UpsertDevice creates or updates a device
func (s *BaseStore) UpsertDevice(ctx context.Context, device *Device) error {
	if device == nil || device.Serial == "" || device.AgentID == "" {
		return fmt.Errorf("%w: device serial and agent ID are required", ErrOwnershipConflict)
	}
	// Serialize JSON fields
	consumablesJSON, _ := json.Marshal(device.Consumables)
	statusJSON, _ := json.Marshal(device.StatusMessages)
	rawDataJSON, _ := json.Marshal(device.RawData)

	now := time.Now().UTC()
	if device.LastSeen.IsZero() {
		device.LastSeen = now
	}
	if device.FirstSeen.IsZero() {
		device.FirstSeen = now
	}
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}

	query := `
		INSERT INTO devices (
			serial, agent_id, ip, manufacturer, model, hostname, firmware,
			mac_address, subnet_mask, gateway, consumables, status_messages,
			last_seen, first_seen, created_at, discovery_method,
			asset_number, location, description, web_ui_url, raw_data,
			device_type, source_type, is_usb, port_name, driver_name,
			is_default, is_shared, spooler_status, usb_webui_available
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(serial) DO UPDATE SET
			ip = excluded.ip,
			manufacturer = excluded.manufacturer,
			model = excluded.model,
			hostname = excluded.hostname,
			firmware = excluded.firmware,
			mac_address = excluded.mac_address,
			subnet_mask = excluded.subnet_mask,
			gateway = excluded.gateway,
			consumables = excluded.consumables,
			status_messages = excluded.status_messages,
			last_seen = excluded.last_seen,
			discovery_method = excluded.discovery_method,
			raw_data = excluded.raw_data,
			device_type = excluded.device_type,
			source_type = excluded.source_type,
			is_usb = excluded.is_usb,
			port_name = excluded.port_name,
			driver_name = excluded.driver_name,
			is_default = excluded.is_default,
			is_shared = excluded.is_shared,
			spooler_status = excluded.spooler_status,
			usb_webui_available = excluded.usb_webui_available
		WHERE devices.agent_id = excluded.agent_id
	`

	result, err := s.execContext(ctx, query,
		device.Serial, device.AgentID, device.IP, device.Manufacturer,
		device.Model, device.Hostname, device.Firmware, device.MACAddress,
		device.SubnetMask, device.Gateway, string(consumablesJSON),
		string(statusJSON), device.LastSeen, device.FirstSeen,
		device.CreatedAt, device.DiscoveryMethod, device.AssetNumber,
		device.Location, device.Description, device.WebUIURL,
		string(rawDataJSON),
		device.DeviceType, device.SourceType, device.IsUSB, device.PortName,
		device.DriverName, device.IsDefault, device.IsShared, device.SpoolerStatus,
		device.UsbWebUIAvailable)

	if err != nil {
		return err
	}
	return requireMachineWrite(result, "device", device.Serial)
}

// requireMachineWrite distinguishes guarded no-ops from successful writes on
// both SQLite and PostgreSQL. The predicate and write execute atomically.
func requireMachineWrite(result sql.Result, kind, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: %s %s", ErrOwnershipConflict, kind, id)
	}
	return nil
}

// GetDevice retrieves a device by serial number
func (s *BaseStore) GetDevice(ctx context.Context, serial string) (*Device, error) {
	query := `
		SELECT serial, agent_id, ip, manufacturer, model, hostname, firmware,
		       mac_address, subnet_mask, gateway, consumables, status_messages,
		       last_seen, first_seen, created_at, discovery_method,
		       asset_number, location, description, web_ui_url, raw_data,
		       device_type, source_type, is_usb, port_name, driver_name,
		       is_default, is_shared, spooler_status, usb_webui_available
		FROM devices
		WHERE serial = ?
	`

	var device Device
	var consumablesJSON, statusJSON, rawDataJSON sql.NullString
	var deviceType, sourceType, portName, driverName, spoolerStatus sql.NullString
	var manufacturer, model, hostname, firmware, macAddress, subnetMask, gateway sql.NullString
	var discoveryMethod, assetNumber, location, description, webUIURL sql.NullString
	var isUSB, isDefault, isShared, usbWebUIAvailable sql.NullBool

	err := s.queryRowContext(ctx, query, serial).Scan(
		&device.Serial, &device.AgentID, &device.IP, &manufacturer,
		&model, &hostname, &firmware, &macAddress,
		&subnetMask, &gateway, &consumablesJSON, &statusJSON,
		&device.LastSeen, &device.FirstSeen, &device.CreatedAt,
		&discoveryMethod, &assetNumber, &location,
		&description, &webUIURL, &rawDataJSON,
		&deviceType, &sourceType, &isUSB, &portName, &driverName,
		&isDefault, &isShared, &spoolerStatus, &usbWebUIAvailable)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("device not found: %s: %w", serial, err)
	}
	if err != nil {
		return nil, err
	}

	// Nullable TEXT columns (may be NULL for devices that never reported these fields)
	device.Manufacturer = manufacturer.String
	device.Model = model.String
	device.Hostname = hostname.String
	device.Firmware = firmware.String
	device.MACAddress = macAddress.String
	device.SubnetMask = subnetMask.String
	device.Gateway = gateway.String
	device.DiscoveryMethod = discoveryMethod.String
	device.AssetNumber = assetNumber.String
	device.Location = location.String
	device.Description = description.String
	device.WebUIURL = webUIURL.String

	// Unmarshal JSON fields
	if consumablesJSON.Valid {
		json.Unmarshal([]byte(consumablesJSON.String), &device.Consumables)
	}
	if statusJSON.Valid {
		json.Unmarshal([]byte(statusJSON.String), &device.StatusMessages)
	}
	if rawDataJSON.Valid {
		json.Unmarshal([]byte(rawDataJSON.String), &device.RawData)
	}

	// Set USB/spooler fields
	if deviceType.Valid {
		device.DeviceType = deviceType.String
	}
	if sourceType.Valid {
		device.SourceType = sourceType.String
	}
	if isUSB.Valid {
		device.IsUSB = isUSB.Bool
	}
	if portName.Valid {
		device.PortName = portName.String
	}
	if driverName.Valid {
		device.DriverName = driverName.String
	}
	if isDefault.Valid {
		device.IsDefault = isDefault.Bool
	}
	if isShared.Valid {
		device.IsShared = isShared.Bool
	}
	if spoolerStatus.Valid {
		device.SpoolerStatus = spoolerStatus.String
	}
	if usbWebUIAvailable.Valid {
		device.UsbWebUIAvailable = usbWebUIAvailable.Bool
	}

	return &device, nil
}

// ListDevices returns devices for a specific agent
func (s *BaseStore) ListDevices(ctx context.Context, agentID string) ([]*Device, error) {
	query := `
		SELECT serial, agent_id, ip, manufacturer, model, hostname, firmware,
		       mac_address, subnet_mask, gateway, consumables, status_messages,
		       last_seen, first_seen, created_at, discovery_method,
		       asset_number, location, description, web_ui_url, raw_data,
		       device_type, source_type, is_usb, port_name, driver_name,
		       is_default, is_shared, spooler_status, usb_webui_available
		FROM devices
		WHERE agent_id = ?
		ORDER BY last_seen DESC
	`

	rows, err := s.queryContext(ctx, query, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanDevices(rows)
}

// ListAllDevices returns all devices across all agents
func (s *BaseStore) ListAllDevices(ctx context.Context) ([]*Device, error) {
	query := `
		SELECT serial, agent_id, ip, manufacturer, model, hostname, firmware,
		       mac_address, subnet_mask, gateway, consumables, status_messages,
		       last_seen, first_seen, created_at, discovery_method,
		       asset_number, location, description, web_ui_url, raw_data,
		       device_type, source_type, is_usb, port_name, driver_name,
		       is_default, is_shared, spooler_status, usb_webui_available
		FROM devices
		ORDER BY last_seen DESC
	`

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanDevices(rows)
}

// CountDevices returns the total number of devices (optionally filtered by agent IDs)
func (s *BaseStore) CountDevices(ctx context.Context, agentIDs []string) (int64, error) {
	var count int64
	if len(agentIDs) == 0 {
		err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&count)
		return count, err
	}
	placeholders := make([]string, len(agentIDs))
	args := make([]interface{}, len(agentIDs))
	for i, a := range agentIDs {
		placeholders[i] = "?"
		args[i] = a
	}
	query := fmt.Sprintf(`SELECT COUNT(*) FROM devices WHERE agent_id IN (%s)`, strings.Join(placeholders, ","))
	err := s.queryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// ListAllDevicesPaginated returns devices with limit/offset pagination
func (s *BaseStore) ListAllDevicesPaginated(ctx context.Context, limit, offset int, agentIDs []string) ([]*Device, error) {
	var query string
	var args []interface{}

	if len(agentIDs) == 0 {
		query = `
			SELECT serial, agent_id, ip, manufacturer, model, hostname, firmware,
			       mac_address, subnet_mask, gateway, consumables, status_messages,
			       last_seen, first_seen, created_at, discovery_method,
			       asset_number, location, description, web_ui_url, raw_data,
			       device_type, source_type, is_usb, port_name, driver_name,
			       is_default, is_shared, spooler_status, usb_webui_available
			FROM devices
			ORDER BY last_seen DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	} else {
		placeholders := make([]string, len(agentIDs))
		for i, a := range agentIDs {
			placeholders[i] = "?"
			args = append(args, a)
		}
		query = fmt.Sprintf(`
			SELECT serial, agent_id, ip, manufacturer, model, hostname, firmware,
			       mac_address, subnet_mask, gateway, consumables, status_messages,
			       last_seen, first_seen, created_at, discovery_method,
			       asset_number, location, description, web_ui_url, raw_data,
			       device_type, source_type, is_usb, port_name, driver_name,
			       is_default, is_shared, spooler_status, usb_webui_available
			FROM devices
			WHERE agent_id IN (%s)
			ORDER BY last_seen DESC
			LIMIT ? OFFSET ?
		`, strings.Join(placeholders, ","))
		args = append(args, limit, offset)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanDevices(rows)
}

// scanDevices scans multiple device rows
func (s *BaseStore) scanDevices(rows *sql.Rows) ([]*Device, error) {
	var devices []*Device
	for rows.Next() {
		var device Device
		var consumablesJSON, statusJSON, rawDataJSON sql.NullString
		var deviceType, sourceType, portName, driverName, spoolerStatus sql.NullString
		var manufacturer, model, hostname, firmware, macAddress, subnetMask, gateway sql.NullString
		var discoveryMethod, assetNumber, location, description, webUIURL sql.NullString
		var isUSB, isDefault, isShared, usbWebUIAvailable sql.NullBool

		err := rows.Scan(
			&device.Serial, &device.AgentID, &device.IP, &manufacturer,
			&model, &hostname, &firmware, &macAddress,
			&subnetMask, &gateway, &consumablesJSON, &statusJSON,
			&device.LastSeen, &device.FirstSeen, &device.CreatedAt,
			&discoveryMethod, &assetNumber, &location,
			&description, &webUIURL, &rawDataJSON,
			&deviceType, &sourceType, &isUSB, &portName, &driverName,
			&isDefault, &isShared, &spoolerStatus, &usbWebUIAvailable)
		if err != nil {
			return nil, err
		}

		// Nullable TEXT columns (may be NULL for devices that never reported these fields)
		device.Manufacturer = manufacturer.String
		device.Model = model.String
		device.Hostname = hostname.String
		device.Firmware = firmware.String
		device.MACAddress = macAddress.String
		device.SubnetMask = subnetMask.String
		device.Gateway = gateway.String
		device.DiscoveryMethod = discoveryMethod.String
		device.AssetNumber = assetNumber.String
		device.Location = location.String
		device.Description = description.String
		device.WebUIURL = webUIURL.String

		// Unmarshal JSON fields
		if consumablesJSON.Valid {
			json.Unmarshal([]byte(consumablesJSON.String), &device.Consumables)
		}
		if statusJSON.Valid {
			json.Unmarshal([]byte(statusJSON.String), &device.StatusMessages)
		}
		if rawDataJSON.Valid {
			json.Unmarshal([]byte(rawDataJSON.String), &device.RawData)
		}

		// Set USB/spooler fields
		if deviceType.Valid {
			device.DeviceType = deviceType.String
		}
		if sourceType.Valid {
			device.SourceType = sourceType.String
		}
		if isUSB.Valid {
			device.IsUSB = isUSB.Bool
		}
		if portName.Valid {
			device.PortName = portName.String
		}
		if driverName.Valid {
			device.DriverName = driverName.String
		}
		if isDefault.Valid {
			device.IsDefault = isDefault.Bool
		}
		if isShared.Valid {
			device.IsShared = isShared.Bool
		}
		if spoolerStatus.Valid {
			device.SpoolerStatus = spoolerStatus.String
		}
		if usbWebUIAvailable.Valid {
			device.UsbWebUIAvailable = usbWebUIAvailable.Bool
		}

		devices = append(devices, &device)
	}

	return devices, rows.Err()
}

// DeleteDevice removes a device by serial number. If deleteMetrics is true, also deletes metrics history.
// Note: Due to ON DELETE CASCADE, deleting the device will automatically delete:
// - metrics_history entries (FOREIGN KEY on serial)
// - device_credentials entries (FOREIGN KEY on serial)
func (s *BaseStore) DeleteDevice(ctx context.Context, serial string, deleteMetrics bool) error {
	if deleteMetrics {
		// Explicitly delete metrics first (even though CASCADE would do it)
		// This gives us better control and explicit logging opportunity
		if _, err := s.execContext(ctx, `DELETE FROM metrics_history WHERE serial = ?`, serial); err != nil {
			return fmt.Errorf("failed to delete metrics: %w", err)
		}
	}

	// Delete device credentials (in case they exist)
	if _, err := s.execContext(ctx, `DELETE FROM device_credentials WHERE serial = ?`, serial); err != nil {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}

	// Delete the device itself
	result, err := s.execContext(ctx, `DELETE FROM devices WHERE serial = ?`, serial)
	if err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("device not found: %s", serial)
	}

	return nil
}

// DeleteDeviceForAgent synchronizes a machine deletion without a read/delete
// ownership race. Credentials cascade with the device; metrics use the existing
// backend FK policy, just as DeleteDevice with deleteMetrics=false does.
func (s *BaseStore) DeleteDeviceForAgent(ctx context.Context, serial, agentID string) error {
	return s.DeleteDeviceForAgentWithMetrics(ctx, serial, agentID, false)
}

// DeleteDeviceForAgentWithMetrics binds UI deletion and optional history cleanup
// to the authorized owner. Delete first in the transaction: a foreign/replaced
// serial must not lose credentials or metrics before ownership is established.
func (s *BaseStore) DeleteDeviceForAgentWithMetrics(ctx context.Context, serial, agentID string, deleteMetrics bool) error {
	if serial == "" || agentID == "" {
		return fmt.Errorf("%w: device serial and agent ID are required", ErrOwnershipConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.query(`DELETE FROM devices WHERE serial = ? AND agent_id = ?`), serial, agentID)
	if err != nil {
		return err
	}
	if err := requireMachineWrite(result, "device deletion", serial); err != nil {
		return err
	}
	if deleteMetrics {
		if _, err := tx.ExecContext(ctx, s.query(`DELETE FROM metrics_history WHERE serial = ?`), serial); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ============================================================================
// Metrics Methods
// ============================================================================

// SaveMetrics saves a metrics snapshot
func (s *BaseStore) SaveMetrics(ctx context.Context, metrics *MetricsSnapshot) error {
	if metrics == nil || metrics.Serial == "" || metrics.AgentID == "" {
		return fmt.Errorf("%w: metric serial and agent ID are required", ErrOwnershipConflict)
	}
	tonerJSON, _ := json.Marshal(metrics.TonerLevels)

	query := `
		INSERT INTO metrics_history (serial, agent_id, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM devices WHERE serial = ? AND agent_id = ?)
	`

	result, err := s.execContext(ctx, query,
		metrics.Serial, metrics.AgentID, metrics.Timestamp,
		metrics.PageCount, metrics.ColorPages, metrics.MonoPages,
		metrics.ScanCount, string(tonerJSON), metrics.Serial, metrics.AgentID)

	if err != nil {
		return err
	}
	return requireMachineWrite(result, "metrics device", metrics.Serial)
}

// GetLatestMetrics retrieves the most recent metrics for a device
func (s *BaseStore) GetLatestMetrics(ctx context.Context, serial string) (*MetricsSnapshot, error) {
	// Note: id column is not included - it may not exist after TimescaleDB hypertable conversion
	query := `
		SELECT serial, agent_id, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
		FROM metrics_history
		WHERE serial = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	var m MetricsSnapshot
	var tonerJSON sql.NullString

	err := s.queryRowContext(ctx, query, serial).Scan(
		&m.Serial, &m.AgentID, &m.Timestamp,
		&m.PageCount, &m.ColorPages, &m.MonoPages, &m.ScanCount, &tonerJSON)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if tonerJSON.Valid {
		json.Unmarshal([]byte(tonerJSON.String), &m.TonerLevels)
	}

	return &m, nil
}

// GetLatestMetricsBatch retrieves the most recent metrics for multiple devices efficiently.
// Returns a map from serial to MetricsSnapshot.
func (s *BaseStore) GetLatestMetricsBatch(ctx context.Context, serials []string) (map[string]*MetricsSnapshot, error) {
	result := make(map[string]*MetricsSnapshot, len(serials))
	if len(serials) == 0 {
		return result, nil
	}

	// Use a subquery to get the latest timestamp for each serial, then join back
	// This is more efficient than N separate queries.
	placeholders := make([]string, len(serials))
	args := make([]interface{}, len(serials))
	for i, s := range serials {
		placeholders[i] = "?"
		args[i] = s
	}

	// Note: id column is not included - it may not exist after TimescaleDB hypertable conversion
	query := fmt.Sprintf(`
		SELECT m.serial, m.agent_id, m.timestamp, m.page_count, m.color_pages, m.mono_pages, m.scan_count, m.toner_levels
		FROM metrics_history m
		INNER JOIN (
			SELECT serial, MAX(timestamp) as max_ts
			FROM metrics_history
			WHERE serial IN (%s)
			GROUP BY serial
		) latest ON m.serial = latest.serial AND m.timestamp = latest.max_ts
	`, strings.Join(placeholders, ","))

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var m MetricsSnapshot
		var tonerJSON sql.NullString
		err := rows.Scan(&m.Serial, &m.AgentID, &m.Timestamp,
			&m.PageCount, &m.ColorPages, &m.MonoPages, &m.ScanCount, &tonerJSON)
		if err != nil {
			return nil, err
		}
		if tonerJSON.Valid {
			json.Unmarshal([]byte(tonerJSON.String), &m.TonerLevels)
		}
		result[m.Serial] = &m
	}

	return result, rows.Err()
}

// GetMetricsHistory retrieves metrics history for a device since a given time
func (s *BaseStore) GetMetricsHistory(ctx context.Context, serial string, since time.Time) ([]*MetricsSnapshot, error) {
	// Note: id column is not included - it may not exist after TimescaleDB hypertable conversion
	query := `
		SELECT serial, agent_id, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
		FROM metrics_history
		WHERE serial = ? AND timestamp >= ?
		ORDER BY timestamp ASC
	`

	rows, err := s.queryContext(ctx, query, serial, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*MetricsSnapshot
	for rows.Next() {
		var m MetricsSnapshot
		var tonerJSON sql.NullString

		err := rows.Scan(&m.Serial, &m.AgentID, &m.Timestamp,
			&m.PageCount, &m.ColorPages, &m.MonoPages, &m.ScanCount, &tonerJSON)
		if err != nil {
			return nil, err
		}

		if tonerJSON.Valid {
			json.Unmarshal([]byte(tonerJSON.String), &m.TonerLevels)
		}

		metrics = append(metrics, &m)
	}

	return metrics, rows.Err()
}

// GetMetricsBounds returns the min/max timestamps and total point count for a device's metrics.
func (s *BaseStore) GetMetricsBounds(ctx context.Context, serial string) (minTS, maxTS time.Time, count int64, err error) {
	query := `
		SELECT MIN(timestamp), MAX(timestamp), COUNT(*)
		FROM metrics_history
		WHERE serial = ?
	`

	var minNullable, maxNullable sql.NullTime
	var cnt int64

	err = s.queryRowContext(ctx, query, serial).Scan(&minNullable, &maxNullable, &cnt)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}

	if cnt == 0 || !minNullable.Valid || !maxNullable.Valid {
		return time.Time{}, time.Time{}, 0, sql.ErrNoRows
	}

	return minNullable.Time, maxNullable.Time, cnt, nil
}

// GetMetricsAtOrBefore retrieves the latest metrics snapshot for a device at or before a given time.
// Returns nil if no snapshot exists at or before that time.
func (s *BaseStore) GetMetricsAtOrBefore(ctx context.Context, serial string, at time.Time) (*MetricsSnapshot, error) {
	// Note: id column is not included - it may not exist after TimescaleDB hypertable conversion
	query := `
		SELECT serial, agent_id, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels
		FROM metrics_history
		WHERE serial = ? AND timestamp <= ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	var m MetricsSnapshot
	var tonerJSON sql.NullString

	err := s.queryRowContext(ctx, query, serial, at).Scan(
		&m.Serial, &m.AgentID, &m.Timestamp,
		&m.PageCount, &m.ColorPages, &m.MonoPages, &m.ScanCount, &tonerJSON)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if tonerJSON.Valid {
		json.Unmarshal([]byte(tonerJSON.String), &m.TonerLevels)
	}

	return &m, nil
}

// ============================================================================
// Tenant Management Methods
// ============================================================================

// CreateTenant creates a new tenant
func (s *BaseStore) CreateTenant(ctx context.Context, tenant *Tenant) error {
	if tenant == nil {
		return fmt.Errorf("tenant required")
	}
	if tenant.ID == "" {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		tenant.ID = hex.EncodeToString(b)
	}
	if tenant.CreatedAt.IsZero() {
		tenant.CreatedAt = time.Now().UTC()
	}

	tenant.LoginDomain = NormalizeTenantDomain(tenant.LoginDomain)

	query := `
		INSERT INTO tenants (
			id, name, description, contact_name, contact_email, contact_phone,
			business_unit, billing_code, address, login_domain, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.execContext(ctx, query,
		tenant.ID, tenant.Name, tenant.Description, tenant.ContactName, tenant.ContactEmail,
		tenant.ContactPhone, tenant.BusinessUnit, tenant.BillingCode, tenant.Address,
		nullString(tenant.LoginDomain), tenant.CreatedAt)
	return err
}

// UpdateTenant updates an existing tenant
func (s *BaseStore) UpdateTenant(ctx context.Context, tenant *Tenant) error {
	if tenant == nil {
		return fmt.Errorf("tenant required")
	}
	if tenant.ID == "" {
		return fmt.Errorf("tenant id required")
	}

	tenant.LoginDomain = NormalizeTenantDomain(tenant.LoginDomain)

	query := `
		UPDATE tenants SET
			name = ?,
			description = ?,
			contact_name = ?,
			contact_email = ?,
			contact_phone = ?,
			business_unit = ?,
			billing_code = ?,
			address = ?,
			login_domain = ?
		WHERE id = ?
	`

	res, err := s.execContext(ctx, query,
		tenant.Name, tenant.Description, tenant.ContactName, tenant.ContactEmail, tenant.ContactPhone,
		tenant.BusinessUnit, tenant.BillingCode, tenant.Address, nullString(tenant.LoginDomain), tenant.ID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetTenant retrieves a tenant by ID
func (s *BaseStore) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	query := `
		SELECT id, name, description, contact_name, contact_email, contact_phone,
		       business_unit, billing_code, address, login_domain, created_at
		FROM tenants WHERE id = ?
	`

	var t Tenant
	var loginDomain sql.NullString
	err := s.queryRowContext(ctx, query, id).Scan(
		&t.ID, &t.Name, &t.Description, &t.ContactName, &t.ContactEmail,
		&t.ContactPhone, &t.BusinessUnit, &t.BillingCode, &t.Address,
		&loginDomain, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.LoginDomain = loginDomain.String
	return &t, nil
}

// ListTenants returns all tenants
func (s *BaseStore) ListTenants(ctx context.Context) ([]*Tenant, error) {
	query := `
		SELECT id, name, description, contact_name, contact_email, contact_phone,
		       business_unit, billing_code, address, login_domain, created_at
		FROM tenants ORDER BY created_at DESC
	`

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []*Tenant
	for rows.Next() {
		var t Tenant
		var loginDomain sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.ContactName, &t.ContactEmail,
			&t.ContactPhone, &t.BusinessUnit, &t.BillingCode, &t.Address,
			&loginDomain, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.LoginDomain = loginDomain.String
		tenants = append(tenants, &t)
	}
	return tenants, nil
}

// FindTenantByDomain finds a tenant by its login domain
func (s *BaseStore) FindTenantByDomain(ctx context.Context, domain string) (*Tenant, error) {
	norm := NormalizeTenantDomain(domain)
	if norm == "" {
		return nil, nil
	}

	query := `
		SELECT id, name, description, contact_name, contact_email, contact_phone,
		       business_unit, billing_code, address, login_domain, created_at
		FROM tenants WHERE login_domain = ?
	`

	var t Tenant
	var loginDomain sql.NullString
	err := s.queryRowContext(ctx, query, norm).Scan(
		&t.ID, &t.Name, &t.Description, &t.ContactName, &t.ContactEmail,
		&t.ContactPhone, &t.BusinessUnit, &t.BillingCode, &t.Address,
		&loginDomain, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.LoginDomain = loginDomain.String
	return &t, nil
}

// DeleteTenant deletes a tenant by ID. This will cascade delete related data
// (sites, join_tokens, settings, etc.) but agents are not automatically deleted.
// Caller should check CountTenantAgents first and handle agent reassignment.
func (s *BaseStore) DeleteTenant(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("tenant id required")
	}

	// Clean up agents' tenant_id reference first (set to NULL to orphan them, preserving them)
	_, _ = s.execContext(ctx, `UPDATE agents SET tenant_id = NULL WHERE tenant_id = ?`, id)

	// Clean up user_tenants mappings
	_, _ = s.execContext(ctx, `DELETE FROM user_tenants WHERE tenant_id = ?`, id)

	// Explicitly delete sites (cascade should handle agent_sites)
	_, _ = s.execContext(ctx, `DELETE FROM sites WHERE tenant_id = ?`, id)

	// Explicitly delete join tokens
	_, _ = s.execContext(ctx, `DELETE FROM join_tokens WHERE tenant_id = ?`, id)

	// Explicitly delete tenant settings
	_, _ = s.execContext(ctx, `DELETE FROM settings_tenant WHERE tenant_id = ?`, id)

	// Explicitly delete fleet update policies
	_, _ = s.execContext(ctx, `DELETE FROM fleet_update_policies WHERE tenant_id = ?`, id)

	// Delete the tenant itself
	query := `DELETE FROM tenants WHERE id = ?`
	res, err := s.execContext(ctx, query, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// CountTenantAgents returns the number of agents assigned to a tenant.
// This is useful for safety checks before tenant deletion.
func (s *BaseStore) CountTenantAgents(ctx context.Context, tenantID string) (int64, error) {
	var count int64
	query := `SELECT COUNT(*) FROM agents WHERE tenant_id = ?`
	err := s.queryRowContext(ctx, query, tenantID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ============================================================================
// Site Management Methods
// ============================================================================

// CreateSite creates a new site within a tenant
func (s *BaseStore) CreateSite(ctx context.Context, site *Site) error {
	if site == nil {
		return fmt.Errorf("site required")
	}
	if site.TenantID == "" {
		return fmt.Errorf("tenant_id required")
	}
	if site.Name == "" {
		return fmt.Errorf("name required")
	}
	if site.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		site.ID = hex.EncodeToString(b)
	}
	if site.CreatedAt.IsZero() {
		site.CreatedAt = time.Now().UTC()
	}

	var rulesJSON []byte
	if len(site.FilterRules) > 0 {
		var err error
		rulesJSON, err = json.Marshal(site.FilterRules)
		if err != nil {
			return fmt.Errorf("failed to marshal filter rules: %w", err)
		}
	}

	query := `INSERT INTO sites (id, tenant_id, name, description, address, filter_rules, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.execContext(ctx, query, site.ID, site.TenantID, site.Name, site.Description, site.Address, nullBytes(rulesJSON), site.CreatedAt)
	return err
}

// UpdateSite updates an existing site
func (s *BaseStore) UpdateSite(ctx context.Context, site *Site) error {
	if site == nil {
		return fmt.Errorf("site required")
	}
	if site.ID == "" {
		return fmt.Errorf("site id required")
	}

	var rulesJSON []byte
	if len(site.FilterRules) > 0 {
		var err error
		rulesJSON, err = json.Marshal(site.FilterRules)
		if err != nil {
			return fmt.Errorf("failed to marshal filter rules: %w", err)
		}
	}

	res, err := s.execContext(ctx, `UPDATE sites SET name = ?, description = ?, address = ?, filter_rules = ? WHERE id = ?`,
		site.Name, site.Description, site.Address, nullBytes(rulesJSON), site.ID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetSite retrieves a site by ID
func (s *BaseStore) GetSite(ctx context.Context, id string) (*Site, error) {
	query := `SELECT id, tenant_id, name, description, address, filter_rules, created_at FROM sites WHERE id = ?`
	row := s.queryRowContext(ctx, query, id)

	var site Site
	var rulesJSON sql.NullString
	err := row.Scan(&site.ID, &site.TenantID, &site.Name, &site.Description, &site.Address, &rulesJSON, &site.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rulesJSON.Valid && rulesJSON.String != "" {
		json.Unmarshal([]byte(rulesJSON.String), &site.FilterRules)
	}
	return &site, nil
}

// ListSitesByTenant returns all sites for a tenant
func (s *BaseStore) ListSitesByTenant(ctx context.Context, tenantID string) ([]*Site, error) {
	query := `SELECT id, tenant_id, name, description, address, filter_rules, created_at FROM sites WHERE tenant_id = ? ORDER BY name`
	rows, err := s.queryContext(ctx, query, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []*Site
	for rows.Next() {
		var site Site
		var rulesJSON sql.NullString
		if err := rows.Scan(&site.ID, &site.TenantID, &site.Name, &site.Description, &site.Address, &rulesJSON, &site.CreatedAt); err != nil {
			return nil, err
		}
		if rulesJSON.Valid && rulesJSON.String != "" {
			json.Unmarshal([]byte(rulesJSON.String), &site.FilterRules)
		}
		sites = append(sites, &site)
	}
	return sites, rows.Err()
}

// DeleteSite removes a site
func (s *BaseStore) DeleteSite(ctx context.Context, id string) error {
	res, err := s.execContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AssignAgentToSite assigns an agent to a site
func (s *BaseStore) AssignAgentToSite(ctx context.Context, agentID, siteID string) error {
	query := `INSERT INTO agent_sites (agent_id, site_id, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`
	_, err := s.execContext(ctx, query, agentID, siteID, time.Now().UTC())
	return err
}

// UnassignAgentFromSite removes an agent from a site
func (s *BaseStore) UnassignAgentFromSite(ctx context.Context, agentID, siteID string) error {
	_, err := s.execContext(ctx, `DELETE FROM agent_sites WHERE agent_id = ? AND site_id = ?`, agentID, siteID)
	return err
}

// GetAgentSiteIDs returns the site IDs for an agent
func (s *BaseStore) GetAgentSiteIDs(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT site_id FROM agent_sites WHERE agent_id = ?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetSiteAgentIDs returns the agent IDs for a site
func (s *BaseStore) GetSiteAgentIDs(ctx context.Context, siteID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT agent_id FROM agent_sites WHERE site_id = ?`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetAgentSites replaces all site assignments for an agent
func (s *BaseStore) SetAgentSites(ctx context.Context, agentID string, siteIDs []string) error {
	// Delete existing assignments
	if _, err := s.execContext(ctx, `DELETE FROM agent_sites WHERE agent_id = ?`, agentID); err != nil {
		return err
	}

	// Insert new assignments
	now := time.Now().UTC()
	for _, siteID := range siteIDs {
		if _, err := s.execContext(ctx, `INSERT INTO agent_sites (agent_id, site_id, created_at) VALUES (?, ?, ?)`,
			agentID, siteID, now); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// User Management Methods
// ============================================================================

// CreateUser creates a new user with hashed password
func (s *BaseStore) CreateUser(ctx context.Context, user *User, rawPassword string) error {
	if user == nil {
		return fmt.Errorf("user required")
	}
	if user.Username == "" {
		return fmt.Errorf("username required")
	}
	if rawPassword == "" {
		return fmt.Errorf("password required")
	}

	hash, err := hashArgon(rawPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	if user.Role == "" {
		user.Role = RoleViewer
	}

	query := `INSERT INTO users (username, password_hash, role, tenant_id, email, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	id, err := s.insertReturningID(ctx, query, user.Username, hash, string(user.Role), nullString(user.TenantID), nullString(user.Email), user.CreatedAt)
	if err != nil {
		return err
	}
	user.ID = id

	// Handle multi-tenant assignment
	if len(user.TenantIDs) > 0 {
		for _, tid := range SortTenantIDs(user.TenantIDs) {
			if _, err := s.execContext(ctx, `INSERT INTO user_tenants (user_id, tenant_id) VALUES (?, ?)`, user.ID, tid); err != nil {
				logWarn("Failed to insert user_tenants row", "user_id", user.ID, "tenant_id", tid, "error", err)
			}
		}
	}

	return nil
}

// GetUserByUsername retrieves a user by username
func (s *BaseStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	query := `SELECT id, username, password_hash, role, tenant_id, email, created_at FROM users WHERE username = ?`

	var u User
	var tenantID, email sql.NullString
	err := s.queryRowContext(ctx, query, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &tenantID, &email, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.TenantID = tenantID.String
	u.Email = email.String
	u.Role = NormalizeRole(string(u.Role))

	// Load multi-tenant assignments
	u.TenantIDs, _ = s.loadUserTenantIDs(ctx, u.ID)

	return &u, nil
}

// GetUserByID retrieves a user by ID
func (s *BaseStore) GetUserByID(ctx context.Context, id int64) (*User, error) {
	query := `SELECT id, username, password_hash, role, tenant_id, email, created_at FROM users WHERE id = ?`

	var u User
	var tenantID, email sql.NullString
	err := s.queryRowContext(ctx, query, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &tenantID, &email, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.TenantID = tenantID.String
	u.Email = email.String
	u.Role = NormalizeRole(string(u.Role))
	u.TenantIDs, _ = s.loadUserTenantIDs(ctx, u.ID)

	return &u, nil
}

// GetUserByEmail retrieves a user by email
func (s *BaseStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `SELECT id, username, password_hash, role, tenant_id, email, created_at FROM users WHERE email = ?`

	var u User
	var tenantID, emailVal sql.NullString
	err := s.queryRowContext(ctx, query, email).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &tenantID, &emailVal, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.TenantID = tenantID.String
	u.Email = emailVal.String
	u.Role = NormalizeRole(string(u.Role))
	u.TenantIDs, _ = s.loadUserTenantIDs(ctx, u.ID)

	return &u, nil
}

// ListUsers returns all users
func (s *BaseStore) ListUsers(ctx context.Context) ([]*User, error) {
	query := `SELECT id, username, role, tenant_id, email, created_at FROM users ORDER BY created_at DESC`

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var u User
		var tenantID, email sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &tenantID, &email, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.TenantID = tenantID.String
		u.Email = email.String
		u.Role = NormalizeRole(string(u.Role))
		u.TenantIDs, _ = s.loadUserTenantIDs(ctx, u.ID)
		users = append(users, &u)
	}
	return users, rows.Err()
}

// UpdateUser updates a user's profile
func (s *BaseStore) UpdateUser(ctx context.Context, user *User) error {
	if user == nil {
		return fmt.Errorf("user required")
	}

	query := `UPDATE users SET username = ?, role = ?, tenant_id = ?, email = ? WHERE id = ?`
	_, err := s.execContext(ctx, query, user.Username, string(user.Role), nullString(user.TenantID), nullString(user.Email), user.ID)
	if err != nil {
		return err
	}

	// Update tenant assignments
	s.execContext(ctx, `DELETE FROM user_tenants WHERE user_id = ?`, user.ID)
	for _, tid := range SortTenantIDs(user.TenantIDs) {
		s.execContext(ctx, `INSERT INTO user_tenants (user_id, tenant_id) VALUES (?, ?)`, user.ID, tid)
	}

	return nil
}

// DeleteUser removes a user
func (s *BaseStore) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// UpdateUserPassword updates a user's password
func (s *BaseStore) UpdateUserPassword(ctx context.Context, userID int64, rawPassword string) error {
	hash, err := hashArgon(rawPassword)
	if err != nil {
		return err
	}
	_, err = s.execContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

// AuthenticateUser verifies username/password and returns the user if valid
// Returns an error if credentials are invalid (user not found or wrong password)
func (s *BaseStore) AuthenticateUser(ctx context.Context, username, rawPassword string) (*User, error) {
	u, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("invalid credentials")
	}
	ok, verr := verifyArgonHash(rawPassword, u.PasswordHash)
	if verr != nil {
		return nil, verr
	}
	if !ok {
		return nil, fmt.Errorf("invalid credentials")
	}
	u.PasswordHash = "" // Don't return hash
	return u, nil
}

// loadUserTenantIDs loads multi-tenant assignments for a user
func (s *BaseStore) loadUserTenantIDs(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT tenant_id FROM user_tenants WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return SortTenantIDs(ids), rows.Err()
}

// ============================================================================
// Session Management Methods
// ============================================================================

// CreateSession creates a new session token
func (s *BaseStore) CreateSession(ctx context.Context, userID int64, ttlMinutes int) (*Session, error) {
	rawToken, err := generateSecureToken(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate session token: %w", err)
	}
	tokenHash := hashSHA256(rawToken)

	expiresAt := time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
	createdAt := time.Now().UTC()

	query := `INSERT INTO sessions (token, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`
	_, err = s.execContext(ctx, query, tokenHash, userID, expiresAt, createdAt)
	if err != nil {
		return nil, err
	}

	return &Session{
		Token:     rawToken,
		UserID:    userID,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
	}, nil
}

// GetSessionByToken retrieves a session by raw token
func (s *BaseStore) GetSessionByToken(ctx context.Context, token string) (*Session, error) {
	tokenHash := hashSHA256(token)

	query := `
		SELECT s.token, s.user_id, s.expires_at, s.created_at, u.username
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		WHERE s.token = ? AND s.expires_at > ?
	`

	var sess Session
	var username sql.NullString
	err := s.queryRowContext(ctx, query, tokenHash, time.Now().UTC()).Scan(
		&sess.Token, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt, &username)
	if err != nil {
		return nil, err
	}
	sess.Username = username.String
	return &sess, nil
}

// DeleteSession removes a session by raw token
func (s *BaseStore) DeleteSession(ctx context.Context, token string) error {
	tokenHash := hashSHA256(token)
	_, err := s.execContext(ctx, `DELETE FROM sessions WHERE token = ?`, tokenHash)
	return err
}

// DeleteSessionByHash removes a session by its stored hash
func (s *BaseStore) DeleteSessionByHash(ctx context.Context, tokenHash string) error {
	_, err := s.execContext(ctx, `DELETE FROM sessions WHERE token = ?`, tokenHash)
	return err
}

// DeleteSessionByHashWithCount removes a session by its stored hash and returns the number of rows deleted
func (s *BaseStore) DeleteSessionByHashWithCount(ctx context.Context, tokenHash string) (int64, error) {
	result, err := s.execContext(ctx, `DELETE FROM sessions WHERE token = ?`, tokenHash)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteExpiredSessions removes all expired sessions and returns the count deleted
func (s *BaseStore) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	result, err := s.execContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListSessions returns all sessions
func (s *BaseStore) ListSessions(ctx context.Context) ([]*Session, error) {
	query := `
		SELECT s.token, s.user_id, s.expires_at, s.created_at, u.username
		FROM sessions s
		LEFT JOIN users u ON s.user_id = u.id
		ORDER BY s.created_at DESC
	`

	rows, err := s.queryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var username sql.NullString
		if err := rows.Scan(&sess.Token, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt, &username); err != nil {
			return nil, err
		}
		sess.Username = username.String
		sessions = append(sessions, &sess)
	}
	return sessions, rows.Err()
}

// ============================================================================
// Audit Log Methods
// ============================================================================

// SaveAuditEntry saves an audit log entry
func (s *BaseStore) SaveAuditEntry(ctx context.Context, entry *AuditEntry) error {
	if entry == nil {
		return fmt.Errorf("entry required")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.Severity == "" {
		entry.Severity = AuditSeverityInfo
	}

	metadataJSON, _ := json.Marshal(entry.Metadata)

	query := `
		INSERT INTO audit_log (
			timestamp, actor_type, actor_id, actor_name, action, target_type, target_id,
			tenant_id, severity, details, metadata, ip_address, user_agent, request_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	id, err := s.insertReturningID(ctx, query,
		entry.Timestamp, entry.ActorType, entry.ActorID, entry.ActorName, entry.Action,
		entry.TargetType, entry.TargetID, entry.TenantID, entry.Severity, entry.Details,
		string(metadataJSON), entry.IPAddress, entry.UserAgent, entry.RequestID)
	if err != nil {
		return err
	}

	entry.ID = id
	return nil
}

// GetAuditLog retrieves audit entries for an actor since a given time
// If actorID is empty, retrieves all entries since the given time
func (s *BaseStore) GetAuditLog(ctx context.Context, actorID string, since time.Time) ([]*AuditEntry, error) {
	var query string
	var args []interface{}

	if actorID == "" {
		query = `
			SELECT id, timestamp, actor_type, actor_id, actor_name, action, target_type, target_id,
			       tenant_id, severity, details, metadata, ip_address, user_agent, request_id
			FROM audit_log
			WHERE timestamp >= ?
			ORDER BY timestamp DESC
		`
		args = []interface{}{since}
	} else {
		query = `
			SELECT id, timestamp, actor_type, actor_id, actor_name, action, target_type, target_id,
			       tenant_id, severity, details, metadata, ip_address, user_agent, request_id
			FROM audit_log
			WHERE actor_id = ? AND timestamp >= ?
			ORDER BY timestamp DESC
		`
		args = []interface{}{actorID, since}
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		var metadataJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ActorType, &e.ActorID, &e.ActorName, &e.Action,
			&e.TargetType, &e.TargetID, &e.TenantID, &e.Severity, &e.Details, &metadataJSON,
			&e.IPAddress, &e.UserAgent, &e.RequestID); err != nil {
			return nil, err
		}
		if metadataJSON.Valid {
			json.Unmarshal([]byte(metadataJSON.String), &e.Metadata)
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// CountAuditLog returns the total number of audit entries matching the filter
func (s *BaseStore) CountAuditLog(ctx context.Context, actorID string, since time.Time) (int64, error) {
	var count int64
	var query string
	var args []interface{}

	if actorID == "" {
		query = `SELECT COUNT(*) FROM audit_log WHERE timestamp >= ?`
		args = []interface{}{since}
	} else {
		query = `SELECT COUNT(*) FROM audit_log WHERE actor_id = ? AND timestamp >= ?`
		args = []interface{}{actorID, since}
	}

	err := s.queryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// GetAuditLogPaginated retrieves paginated audit entries
func (s *BaseStore) GetAuditLogPaginated(ctx context.Context, actorID string, since time.Time, limit, offset int) ([]*AuditEntry, error) {
	var query string
	var args []interface{}

	if actorID == "" {
		query = `
			SELECT id, timestamp, actor_type, actor_id, actor_name, action, target_type, target_id,
			       tenant_id, severity, details, metadata, ip_address, user_agent, request_id
			FROM audit_log
			WHERE timestamp >= ?
			ORDER BY timestamp DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{since, limit, offset}
	} else {
		query = `
			SELECT id, timestamp, actor_type, actor_id, actor_name, action, target_type, target_id,
			       tenant_id, severity, details, metadata, ip_address, user_agent, request_id
			FROM audit_log
			WHERE actor_id = ? AND timestamp >= ?
			ORDER BY timestamp DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{actorID, since, limit, offset}
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		var e AuditEntry
		var metadataJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ActorType, &e.ActorID, &e.ActorName, &e.Action,
			&e.TargetType, &e.TargetID, &e.TenantID, &e.Severity, &e.Details, &metadataJSON,
			&e.IPAddress, &e.UserAgent, &e.RequestID); err != nil {
			return nil, err
		}
		if metadataJSON.Valid {
			json.Unmarshal([]byte(metadataJSON.String), &e.Metadata)
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// ============================================================================
// Join Token Methods
// ============================================================================

// CreateJoinToken generates a join token for tenant agent onboarding.
// Returns the JoinToken record, raw token string for sharing, and any error.
func (s *BaseStore) CreateJoinToken(ctx context.Context, tenantID string, ttlMinutes int, oneTime bool) (*JoinToken, string, error) {
	if tenantID == "" {
		return nil, "", fmt.Errorf("tenant_id required")
	}
	// Verify tenant exists
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return nil, "", fmt.Errorf("tenant not found: %s", tenantID)
	}

	// Generate random token
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	rawToken := hex.EncodeToString(b)
	logDebug("CreateJoinToken: generated token", "tenant_id", tenantID, "token_length", len(rawToken), "token_prefix", rawToken[:8], "ttl_minutes", ttlMinutes, "one_time", oneTime)

	// Hash the token using Argon2
	tokenHash, err := hashArgon(rawToken)
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttlMinutes) * time.Minute)
	tokenID, err := generateSecureToken(16)
	if err != nil {
		logWarn("CreateJoinToken: failed to generate token ID", "error", err)
		return nil, "", fmt.Errorf("failed to generate token ID: %w", err)
	}

	query := `INSERT INTO join_tokens (id, token_hash, tenant_id, expires_at, one_time, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	_, err = s.execContext(ctx, query, tokenID, tokenHash, tenantID, expiresAt, boolToInt(oneTime), now)
	if err != nil {
		logWarn("CreateJoinToken: failed to insert", "error", err, "tenant_id", tenantID)
		return nil, "", err
	}

	jt := &JoinToken{
		ID:        tokenID,
		TenantID:  tenantID,
		ExpiresAt: expiresAt,
		OneTime:   oneTime,
		CreatedAt: now,
	}

	logInfo("CreateJoinToken: token created successfully", "token_id", tokenID, "tenant_id", tenantID, "expires_at", expiresAt.Format(time.RFC3339), "one_time", oneTime)
	return jt, rawToken, nil
}

// CreateJoinTokenWithSecret creates a join token using a predefined secret instead of generating a random one.
// This is used for auto-join scenarios where agent and server share a common init secret.
func (s *BaseStore) CreateJoinTokenWithSecret(ctx context.Context, jt *JoinToken, rawSecret string) (*JoinToken, error) {
	if jt == nil || jt.TenantID == "" {
		return nil, fmt.Errorf("tenant_id required")
	}
	if rawSecret == "" {
		return nil, fmt.Errorf("secret required")
	}

	// Verify tenant exists
	if _, err := s.GetTenant(ctx, jt.TenantID); err != nil {
		return nil, fmt.Errorf("tenant not found: %s", jt.TenantID)
	}

	// Hash the secret using Argon2
	tokenHash, err := hashArgon(rawSecret)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if jt.ID == "" {
		var err error
		jt.ID, err = generateSecureToken(16)
		if err != nil {
			return nil, fmt.Errorf("failed to generate token ID: %w", err)
		}
	}
	if jt.CreatedAt.IsZero() {
		jt.CreatedAt = now
	}
	if jt.ExpiresAt.IsZero() {
		jt.ExpiresAt = now.Add(365 * 24 * time.Hour) // Default 1 year
	}

	query := `INSERT INTO join_tokens (id, token_hash, tenant_id, expires_at, one_time, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	_, err = s.execContext(ctx, query, jt.ID, tokenHash, jt.TenantID, jt.ExpiresAt, boolToInt(jt.OneTime), jt.CreatedAt)
	if err != nil {
		// Check if this is a duplicate key error (token already exists)
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "duplicate") || strings.Contains(errStr, "unique constraint") || strings.Contains(errStr, "already exists") {
			logDebug("CreateJoinTokenWithSecret: token already exists, skipping", "token_id", jt.ID, "tenant_id", jt.TenantID)
			return jt, nil // Return success - the token already exists
		}
		logWarn("CreateJoinTokenWithSecret: failed to insert", "error", err, "tenant_id", jt.TenantID)
		return nil, err
	}

	logInfo("CreateJoinTokenWithSecret: token created successfully", "token_id", jt.ID, "tenant_id", jt.TenantID, "expires_at", jt.ExpiresAt.Format(time.RFC3339), "one_time", jt.OneTime)
	return jt, nil
}

// isValidTokenFormat checks if a token has an acceptable format.
// Accepts either:
//   - Standard 48-char hex join tokens (generated by CreateJoinToken)
//   - Arbitrary strings for INIT_SECRET auto-join tokens
//
// Rejects obviously invalid tokens (empty or whitespace-only).
func isValidTokenFormat(token string) bool {
	// Reject empty or whitespace-only tokens
	if strings.TrimSpace(token) == "" {
		return false
	}
	// Accept any non-empty token - Argon2 hash comparison will validate
	return true
}

// ValidateJoinToken validates a join token and returns the JoinToken if valid.
// Returns specific errors for expired vs unknown tokens to allow different handling.
func (s *BaseStore) ValidateJoinToken(ctx context.Context, rawToken string) (*JoinToken, error) {
	logDebug("ValidateJoinToken: starting validation", "token_length", len(rawToken), "token_prefix", safePrefix(rawToken, 8))

	// First check token format - reject obviously invalid tokens early
	if !isValidTokenFormat(rawToken) {
		logWarn("ValidateJoinToken: invalid token format", "token_length", len(rawToken))
		return nil, &TokenValidationError{Err: ErrTokenInvalid}
	}

	now := time.Now().UTC()

	// First, try to find a valid (non-revoked, non-expired) token
	query := fmt.Sprintf(`
		SELECT id, token_hash, tenant_id, expires_at, one_time, created_at
		FROM join_tokens
		WHERE revoked = %s AND expires_at > ?
	`, s.dialect.BoolValue(false))
	rows, err := s.queryContext(ctx, query, now)
	if err != nil {
		logWarn("ValidateJoinToken: query failed", "error", err)
		return nil, err
	}
	defer rows.Close()

	candidateCount := 0

	for rows.Next() {
		candidateCount++
		var id, hash, tenantID string
		var expiresAt, createdAt time.Time
		var oneTimeInt interface{}
		if err := rows.Scan(&id, &hash, &tenantID, &expiresAt, &oneTimeInt, &createdAt); err != nil {
			logWarn("ValidateJoinToken: scan failed", "error", err)
			return nil, err
		}

		logDebug("ValidateJoinToken: checking candidate", "token_id", id, "tenant_id", tenantID, "expires_at", expiresAt.Format(time.RFC3339), "one_time", intToBool(oneTimeInt))

		// Verify the hash
		ok, verr := verifyArgonHash(rawToken, hash)
		if verr != nil {
			logWarn("ValidateJoinToken: hash verification error", "token_id", id, "error", verr)
			continue
		}
		if ok {
			logInfo("ValidateJoinToken: token matched", "token_id", id, "tenant_id", tenantID, "one_time", intToBool(oneTimeInt))
			// If one-time token, mark as used (revoked)
			if intToBool(oneTimeInt) {
				// Release the candidate cursor before writing: a single-connection
				// store otherwise waits on its own open rows. Consume once using
				// current DB state, not the potentially stale candidate snapshot.
				if err := rows.Close(); err != nil {
					return nil, err
				}
				markUsedQuery := fmt.Sprintf(`UPDATE join_tokens SET revoked = %s, used_at = ? WHERE id = ? AND revoked = %s AND expires_at > ?`, s.dialect.BoolValue(true), s.dialect.BoolValue(false))
				consumedAt := time.Now().UTC()
				result, err := s.execContext(ctx, markUsedQuery, consumedAt, id, consumedAt)
				if err != nil {
					logWarn("ValidateJoinToken: failed to mark token as used", "token_id", id, "error", err)
					return nil, err
				}
				n, err := result.RowsAffected()
				if err != nil {
					return nil, err
				}
				if n != 1 {
					return nil, &TokenValidationError{Err: ErrTokenRevoked, TenantID: tenantID, TokenID: id}
				}
				logInfo("ValidateJoinToken: marked one-time token as used", "token_id", id)
			}
			return &JoinToken{
				ID:        id,
				TokenHash: hash,
				TenantID:  tenantID,
				ExpiresAt: expiresAt,
				OneTime:   intToBool(oneTimeInt),
				CreatedAt: createdAt,
			}, nil
		} else {
			logDebug("ValidateJoinToken: hash mismatch for candidate", "token_id", id)
		}
	}
	rows.Close()

	// No valid token found - now check if the token matches any expired or revoked tokens
	// This allows us to distinguish between "expired but known" vs "completely unknown"
	expiredQuery := fmt.Sprintf(`
		SELECT id, token_hash, tenant_id, expires_at, revoked
		FROM join_tokens
		WHERE revoked = %s OR expires_at <= ?
	`, s.dialect.BoolValue(true))
	expiredRows, err := s.queryContext(ctx, expiredQuery, now)
	if err != nil {
		logWarn("ValidateJoinToken: expired token query failed", "error", err)
		return nil, &TokenValidationError{Err: ErrTokenInvalid}
	}
	defer expiredRows.Close()

	for expiredRows.Next() {
		var id, hash, tenantID string
		var expiresAt time.Time
		var revokedInt interface{}
		if err := expiredRows.Scan(&id, &hash, &tenantID, &expiresAt, &revokedInt); err != nil {
			continue
		}

		ok, verr := verifyArgonHash(rawToken, hash)
		if verr != nil {
			continue
		}
		if ok {
			revoked := intToBool(revokedInt)
			if revoked {
				logWarn("ValidateJoinToken: matched revoked token", "token_id", id, "tenant_id", tenantID)
				return nil, &TokenValidationError{Err: ErrTokenRevoked, TenantID: tenantID, TokenID: id}
			}
			logWarn("ValidateJoinToken: matched expired token", "token_id", id, "tenant_id", tenantID, "expired_at", expiresAt.Format(time.RFC3339))
			return nil, &TokenValidationError{Err: ErrTokenExpired, TenantID: tenantID, TokenID: id}
		}
	}

	logWarn("ValidateJoinToken: no matching token found (unknown token)", "candidates_checked", candidateCount, "token_prefix", safePrefix(rawToken, 8))
	return nil, &TokenValidationError{Err: ErrTokenInvalid}
}

// ListJoinTokens lists tokens for a tenant (admin view)
func (s *BaseStore) ListJoinTokens(ctx context.Context, tenantID string) ([]*JoinToken, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, token_hash, tenant_id, expires_at, one_time, created_at, used_at, revoked
		FROM join_tokens
		WHERE tenant_id = ?
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*JoinToken
	for rows.Next() {
		var jt JoinToken
		var oneInt, revokedInt interface{}
		var usedAt sql.NullTime
		if err := rows.Scan(&jt.ID, &jt.TokenHash, &jt.TenantID, &jt.ExpiresAt, &oneInt, &jt.CreatedAt, &usedAt, &revokedInt); err != nil {
			return nil, err
		}
		jt.OneTime = intToBool(oneInt)
		jt.Revoked = intToBool(revokedInt)
		if usedAt.Valid {
			jt.UsedAt = usedAt.Time
		}
		tokens = append(tokens, &jt)
	}
	return tokens, rows.Err()
}

// RevokeJoinToken marks a join token as revoked
func (s *BaseStore) RevokeJoinToken(ctx context.Context, id string) error {
	query := fmt.Sprintf(`UPDATE join_tokens SET revoked = %s WHERE id = ?`, s.dialect.BoolValue(true))
	_, err := s.execContext(ctx, query, id)
	return err
}

// ============================================================================
// Pending Agent Registration Methods
// ============================================================================

// CreatePendingAgentRegistration stores a pending registration from an expired token attempt.
// Only call this when ValidateJoinToken returns ErrTokenExpired.
func (s *BaseStore) CreatePendingAgentRegistration(ctx context.Context, reg *PendingAgentRegistration) (int64, error) {
	if reg.AgentID == "" || reg.ExpiredTokenID == "" || reg.ExpiredTenantID == "" {
		return 0, fmt.Errorf("agent_id, expired_token_id, and expired_tenant_id are required")
	}

	now := time.Now().UTC()
	if reg.Status == "" {
		reg.Status = PendingStatusPending
	}

	id, err := s.insertReturningID(ctx, `
		INSERT INTO pending_agent_registrations 
		(agent_id, name, hostname, ip, platform, agent_version, protocol_version, expired_token_id, expired_tenant_id, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, reg.AgentID, reg.Name, reg.Hostname, reg.IP, reg.Platform, reg.AgentVersion, reg.ProtocolVersion,
		reg.ExpiredTokenID, reg.ExpiredTenantID, reg.Status, now)
	if err != nil {
		return 0, err
	}

	reg.ID = id
	reg.CreatedAt = now

	logInfo("CreatePendingAgentRegistration: created pending registration",
		"id", id, "agent_id", reg.AgentID, "expired_tenant_id", reg.ExpiredTenantID)
	return id, nil
}

// GetPendingAgentRegistration retrieves a pending registration by ID.
func (s *BaseStore) GetPendingAgentRegistration(ctx context.Context, id int64) (*PendingAgentRegistration, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, agent_id, name, hostname, ip, platform, agent_version, protocol_version,
		       expired_token_id, expired_tenant_id, status, created_at, reviewed_at, reviewed_by, notes
		FROM pending_agent_registrations WHERE id = ?
	`, id)

	var reg PendingAgentRegistration
	var name, hostname, ip, platform, agentVersion, protocolVersion sql.NullString
	var reviewedAt sql.NullTime
	var reviewedBy, notes sql.NullString

	err := row.Scan(&reg.ID, &reg.AgentID, &name, &hostname, &ip, &platform, &agentVersion, &protocolVersion,
		&reg.ExpiredTokenID, &reg.ExpiredTenantID, &reg.Status, &reg.CreatedAt, &reviewedAt, &reviewedBy, &notes)
	if err != nil {
		return nil, err
	}

	reg.Name = name.String
	reg.Hostname = hostname.String
	reg.IP = ip.String
	reg.Platform = platform.String
	reg.AgentVersion = agentVersion.String
	reg.ProtocolVersion = protocolVersion.String
	if reviewedAt.Valid {
		reg.ReviewedAt = reviewedAt.Time
	}
	reg.ReviewedBy = reviewedBy.String
	reg.Notes = notes.String

	return &reg, nil
}

// ListPendingAgentRegistrations lists pending registrations, optionally filtered by status.
func (s *BaseStore) ListPendingAgentRegistrations(ctx context.Context, status string) ([]*PendingAgentRegistration, error) {
	var rows *sql.Rows
	var err error

	if status != "" {
		rows, err = s.queryContext(ctx, `
			SELECT id, agent_id, name, hostname, ip, platform, agent_version, protocol_version,
			       expired_token_id, expired_tenant_id, status, created_at, reviewed_at, reviewed_by, notes
			FROM pending_agent_registrations 
			WHERE status = ?
			ORDER BY created_at DESC
		`, status)
	} else {
		rows, err = s.queryContext(ctx, `
			SELECT id, agent_id, name, hostname, ip, platform, agent_version, protocol_version,
			       expired_token_id, expired_tenant_id, status, created_at, reviewed_at, reviewed_by, notes
			FROM pending_agent_registrations 
			ORDER BY created_at DESC
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var registrations []*PendingAgentRegistration
	for rows.Next() {
		var reg PendingAgentRegistration
		var name, hostname, ip, platform, agentVersion, protocolVersion sql.NullString
		var reviewedAt sql.NullTime
		var reviewedBy, notes sql.NullString

		err := rows.Scan(&reg.ID, &reg.AgentID, &name, &hostname, &ip, &platform, &agentVersion, &protocolVersion,
			&reg.ExpiredTokenID, &reg.ExpiredTenantID, &reg.Status, &reg.CreatedAt, &reviewedAt, &reviewedBy, &notes)
		if err != nil {
			return nil, err
		}

		reg.Name = name.String
		reg.Hostname = hostname.String
		reg.IP = ip.String
		reg.Platform = platform.String
		reg.AgentVersion = agentVersion.String
		reg.ProtocolVersion = protocolVersion.String
		if reviewedAt.Valid {
			reg.ReviewedAt = reviewedAt.Time
		}
		reg.ReviewedBy = reviewedBy.String
		reg.Notes = notes.String

		registrations = append(registrations, &reg)
	}

	return registrations, rows.Err()
}

// ApprovePendingRegistration transitions an existing pending registration once.
// The caller creates the join token and performs tenant assignment separately.
func (s *BaseStore) ApprovePendingRegistration(ctx context.Context, id int64, tenantID, reviewedBy string) error {
	now := time.Now().UTC()
	result, err := s.execContext(ctx, `
		UPDATE pending_agent_registrations 
		SET status = ?, reviewed_at = ?, reviewed_by = ?
		WHERE id = ? AND status = ?
	`, PendingStatusApproved, now, reviewedBy, id, PendingStatusPending)
	if err != nil {
		return err
	}
	if err := requirePendingTransition(result, id); err != nil {
		return err
	}
	logInfo("ApprovePendingRegistration: approved", "id", id, "tenant_id", tenantID, "reviewed_by", reviewedBy)
	return nil
}

// RejectPendingRegistration rejects a pending registration with optional notes.
func (s *BaseStore) RejectPendingRegistration(ctx context.Context, id int64, reviewedBy, notes string) error {
	now := time.Now().UTC()
	result, err := s.execContext(ctx, `
		UPDATE pending_agent_registrations 
		SET status = ?, reviewed_at = ?, reviewed_by = ?, notes = ?
		WHERE id = ? AND status = ?
	`, PendingStatusRejected, now, reviewedBy, notes, id, PendingStatusPending)
	if err != nil {
		return err
	}
	if err := requirePendingTransition(result, id); err != nil {
		return err
	}
	logInfo("RejectPendingRegistration: rejected", "id", id, "reviewed_by", reviewedBy)
	return nil
}

func requirePendingTransition(result sql.Result, id int64) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("registration %d does not exist or is not pending: %w", id, sql.ErrNoRows)
	}
	return nil
}

// DeletePendingAgentRegistration deletes a pending registration.
func (s *BaseStore) DeletePendingAgentRegistration(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM pending_agent_registrations WHERE id = ?`, id)
	return err
}

// ============================================================================
// Password Reset Methods
// ============================================================================

// CreatePasswordResetToken creates a reset token for a user and stores its hash
func (s *BaseStore) CreatePasswordResetToken(ctx context.Context, userID int64, ttlMinutes int) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	rawToken := hex.EncodeToString(b)

	// Hash token using Argon2
	tokenHash, err := hashArgon(rawToken)
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
	now := time.Now().UTC()

	_, err = s.execContext(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, expiresAt, now)
	if err != nil {
		return "", err
	}

	return rawToken, nil
}

// ValidatePasswordResetToken verifies the token and marks it used; returns userID
func (s *BaseStore) ValidatePasswordResetToken(ctx context.Context, token string) (int64, error) {
	query := fmt.Sprintf("SELECT id, token_hash, user_id, expires_at, used FROM password_resets WHERE used = %s AND expires_at > ?", s.dialect.BoolValue(false))
	rows, err := s.queryContext(ctx, query, time.Now().UTC())
	if err != nil {
		return 0, err
	}

	// Collect matching tokens first, then close rows before updating
	type match struct {
		id     int64
		userID int64
	}
	var foundMatch *match

	for rows.Next() {
		var id, userID int64
		var hash string
		var expiresAt time.Time
		var usedInt interface{}
		if err := rows.Scan(&id, &hash, &userID, &expiresAt, &usedInt); err != nil {
			rows.Close()
			return 0, err
		}

		ok, verr := verifyArgonHash(token, hash)
		if verr == nil && ok {
			foundMatch = &match{id: id, userID: userID}
			break // Found a match
		}
	}
	rows.Close() // Close BEFORE doing the UPDATE

	if foundMatch == nil {
		return 0, fmt.Errorf("invalid or expired token")
	}

	// Now safe to UPDATE since rows are closed
	updateQuery := fmt.Sprintf("UPDATE password_resets SET used = %s WHERE id = ?", s.dialect.BoolValue(true))
	if _, err := s.execContext(ctx, updateQuery, foundMatch.id); err != nil {
		return 0, err
	}
	return foundMatch.userID, nil
}

// DeletePasswordResetToken deletes a matching reset token (if present)
func (s *BaseStore) DeletePasswordResetToken(ctx context.Context, token string) error {
	rows, err := s.queryContext(ctx, `SELECT id, token_hash FROM password_resets`)
	if err != nil {
		return err
	}

	// Find matching token first, then close rows before deleting
	var matchID int64 = -1
	for rows.Next() {
		var id int64
		var hash string
		if err := rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return err
		}
		ok, verr := verifyArgonHash(token, hash)
		if verr == nil && ok {
			matchID = id
			break
		}
	}
	rows.Close() // Close BEFORE doing the DELETE

	if matchID >= 0 {
		_, err := s.execContext(ctx, `DELETE FROM password_resets WHERE id = ?`, matchID)
		return err
	}
	return nil
}

// ============================================================================
// User Invitation Methods
// ============================================================================

// CreateUserInvitation creates an invitation and returns the raw token
func (s *BaseStore) CreateUserInvitation(ctx context.Context, inv *UserInvitation, ttlMinutes int) (string, error) {
	if inv == nil || inv.Email == "" {
		return "", fmt.Errorf("email required")
	}

	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	rawToken := hex.EncodeToString(b)

	tokenHash, err := hashArgon(rawToken)
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
	now := time.Now().UTC()
	role := NormalizeRole(string(inv.Role))

	id, err := s.insertReturningID(ctx, `
		INSERT INTO user_invitations (email, username, role, tenant_id, token_hash, expires_at, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, inv.Email, nullString(inv.Username), string(role), nullString(inv.TenantID), tokenHash, expiresAt, now, nullString(inv.CreatedBy))
	if err != nil {
		return "", err
	}

	inv.ID = id
	inv.TokenHash = tokenHash
	inv.ExpiresAt = expiresAt
	inv.CreatedAt = now
	inv.Role = role

	return rawToken, nil
}

// GetUserInvitation validates token and returns the invitation if valid and unused
func (s *BaseStore) GetUserInvitation(ctx context.Context, token string) (*UserInvitation, error) {
	query := fmt.Sprintf(`
		SELECT id, email, username, role, tenant_id, token_hash, expires_at, used, created_at, created_by
		FROM user_invitations
		WHERE used = %s AND expires_at > ?
	`, s.dialect.BoolValue(false))
	rows, err := s.queryContext(ctx, query, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var inv UserInvitation
		var role string
		var username, tenantID, createdBy sql.NullString
		if err := rows.Scan(&inv.ID, &inv.Email, &username, &role, &tenantID, &inv.TokenHash, &inv.ExpiresAt, &inv.Used, &inv.CreatedAt, &createdBy); err != nil {
			return nil, err
		}

		ok, verr := verifyArgonHash(token, inv.TokenHash)
		if verr != nil {
			continue
		}
		if ok {
			inv.Role = Role(role)
			inv.Username = username.String
			inv.TenantID = tenantID.String
			inv.CreatedBy = createdBy.String
			return &inv, nil
		}
	}

	return nil, fmt.Errorf("invalid or expired invitation")
}

// MarkInvitationUsed marks an invitation as used
func (s *BaseStore) MarkInvitationUsed(ctx context.Context, id int64) error {
	query := fmt.Sprintf("UPDATE user_invitations SET used = %s WHERE id = ?", s.dialect.BoolValue(true))
	_, err := s.execContext(ctx, query, id)
	return err
}

// ListUserInvitations returns all invitations (for admin UI)
func (s *BaseStore) ListUserInvitations(ctx context.Context) ([]*UserInvitation, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, email, username, role, tenant_id, expires_at, used, created_at, created_by
		FROM user_invitations
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invitations []*UserInvitation
	for rows.Next() {
		var inv UserInvitation
		var role string
		var username, tenantID, createdBy sql.NullString
		if err := rows.Scan(&inv.ID, &inv.Email, &username, &role, &tenantID, &inv.ExpiresAt, &inv.Used, &inv.CreatedAt, &createdBy); err != nil {
			return nil, err
		}
		inv.Role = Role(role)
		inv.Username = username.String
		inv.TenantID = tenantID.String
		inv.CreatedBy = createdBy.String
		invitations = append(invitations, &inv)
	}
	return invitations, rows.Err()
}

// DeleteUserInvitation deletes an invitation by ID
func (s *BaseStore) DeleteUserInvitation(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM user_invitations WHERE id = ?`, id)
	return err
}

// ============================================================================
// OIDC Provider Methods
// ============================================================================

// CreateOIDCProvider creates a new OIDC provider configuration
func (s *BaseStore) CreateOIDCProvider(ctx context.Context, provider *OIDCProvider) error {
	if provider == nil {
		return fmt.Errorf("provider required")
	}
	if provider.Slug == "" || provider.Issuer == "" || provider.ClientID == "" {
		return fmt.Errorf("slug, issuer, and client_id required")
	}

	if provider.CreatedAt.IsZero() {
		provider.CreatedAt = time.Now().UTC()
	}
	provider.UpdatedAt = time.Now().UTC()
	provider.DefaultRole = NormalizeRole(string(provider.DefaultRole))

	scopes := strings.Join(provider.Scopes, " ")

	id, err := s.insertReturningID(ctx, `
		INSERT INTO oidc_providers (
			slug, display_name, issuer, client_id, client_secret, scopes, icon,
			button_text, button_style, auto_login, tenant_id, default_role, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		provider.Slug, provider.DisplayName, provider.Issuer, provider.ClientID,
		provider.ClientSecret, scopes, provider.Icon, provider.ButtonText,
		provider.ButtonStyle, boolToInt(provider.AutoLogin), nullString(provider.TenantID),
		string(provider.DefaultRole), provider.CreatedAt, provider.UpdatedAt)
	if err != nil {
		return err
	}
	provider.ID = id
	return nil
}

// UpdateOIDCProvider updates an existing OIDC provider
func (s *BaseStore) UpdateOIDCProvider(ctx context.Context, provider *OIDCProvider) error {
	if provider == nil {
		return fmt.Errorf("provider required")
	}
	if provider.Slug == "" {
		return fmt.Errorf("slug required")
	}

	provider.UpdatedAt = time.Now().UTC()
	provider.DefaultRole = NormalizeRole(string(provider.DefaultRole))
	scopes := strings.Join(provider.Scopes, " ")

	_, err := s.execContext(ctx, `
		UPDATE oidc_providers SET
			display_name = ?, issuer = ?, client_id = ?, client_secret = ?, scopes = ?,
			icon = ?, button_text = ?, button_style = ?, auto_login = ?,
			tenant_id = ?, default_role = ?, updated_at = ?
		WHERE slug = ?
	`,
		provider.DisplayName, provider.Issuer, provider.ClientID, provider.ClientSecret,
		scopes, provider.Icon, provider.ButtonText, provider.ButtonStyle,
		boolToInt(provider.AutoLogin), nullString(provider.TenantID),
		string(provider.DefaultRole), provider.UpdatedAt, provider.Slug)
	return err
}

// DeleteOIDCProvider removes an OIDC provider
func (s *BaseStore) DeleteOIDCProvider(ctx context.Context, slug string) error {
	_, err := s.execContext(ctx, `DELETE FROM oidc_providers WHERE slug = ?`, slug)
	return err
}

// GetOIDCProvider retrieves an OIDC provider by slug
func (s *BaseStore) GetOIDCProvider(ctx context.Context, slug string) (*OIDCProvider, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, slug, display_name, issuer, client_id, client_secret, scopes, icon,
		       button_text, button_style, auto_login, tenant_id, default_role, created_at, updated_at
		FROM oidc_providers WHERE slug = ?
	`, slug)

	var p OIDCProvider
	var scopes string
	var autoLogin interface{}
	var tenantID sql.NullString
	var defaultRole string

	if err := row.Scan(&p.ID, &p.Slug, &p.DisplayName, &p.Issuer, &p.ClientID,
		&p.ClientSecret, &scopes, &p.Icon, &p.ButtonText, &p.ButtonStyle,
		&autoLogin, &tenantID, &defaultRole, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	p.AutoLogin = intToBool(autoLogin)
	p.TenantID = tenantID.String
	if scopes == "" {
		scopes = "openid profile email"
	}
	p.Scopes = strings.Fields(scopes)
	p.DefaultRole = NormalizeRole(defaultRole)

	return &p, nil
}

// ListOIDCProviders returns all OIDC providers, optionally filtered by tenant
func (s *BaseStore) ListOIDCProviders(ctx context.Context, tenantID string) ([]*OIDCProvider, error) {
	query := `
		SELECT id, slug, display_name, issuer, client_id, client_secret, scopes, icon,
		       button_text, button_style, auto_login, tenant_id, default_role, created_at, updated_at
		FROM oidc_providers
	`
	args := []interface{}{}
	if tenantID != "" {
		query += " WHERE tenant_id IS NULL OR tenant_id = ?"
		args = append(args, tenantID)
	}
	query += " ORDER BY tenant_id IS NULL DESC, display_name"

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []*OIDCProvider
	for rows.Next() {
		var p OIDCProvider
		var scopes string
		var autoLogin interface{}
		var tid sql.NullString
		var defaultRole string

		if err := rows.Scan(&p.ID, &p.Slug, &p.DisplayName, &p.Issuer, &p.ClientID,
			&p.ClientSecret, &scopes, &p.Icon, &p.ButtonText, &p.ButtonStyle,
			&autoLogin, &tid, &defaultRole, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}

		p.AutoLogin = intToBool(autoLogin)
		p.TenantID = tid.String
		p.Scopes = strings.Fields(scopes)
		p.DefaultRole = NormalizeRole(defaultRole)
		providers = append(providers, &p)
	}
	return providers, rows.Err()
}

// CreateOIDCSession creates a pending OIDC login session
func (s *BaseStore) CreateOIDCSession(ctx context.Context, sess *OIDCSession) error {
	if sess == nil {
		return fmt.Errorf("session required")
	}
	if sess.ID == "" {
		return fmt.Errorf("session id required")
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}

	_, err := s.execContext(ctx, `
		INSERT INTO oidc_sessions (id, provider_slug, tenant_id, nonce, state, redirect_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, sess.ID, sess.ProviderSlug, nullString(sess.TenantID), sess.Nonce, sess.State, sess.RedirectURL, sess.CreatedAt)
	return err
}

// GetOIDCSession retrieves an OIDC session by ID
func (s *BaseStore) GetOIDCSession(ctx context.Context, id string) (*OIDCSession, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, provider_slug, tenant_id, nonce, state, redirect_url, created_at
		FROM oidc_sessions WHERE id = ?
	`, id)

	var sess OIDCSession
	var tenantID sql.NullString
	if err := row.Scan(&sess.ID, &sess.ProviderSlug, &tenantID, &sess.Nonce, &sess.State, &sess.RedirectURL, &sess.CreatedAt); err != nil {
		return nil, err
	}
	sess.TenantID = tenantID.String
	return &sess, nil
}

// DeleteOIDCSession removes an OIDC session
func (s *BaseStore) DeleteOIDCSession(ctx context.Context, id string) error {
	_, err := s.execContext(ctx, `DELETE FROM oidc_sessions WHERE id = ?`, id)
	return err
}

// CreateOIDCLink creates a link between an OIDC subject and a local user
func (s *BaseStore) CreateOIDCLink(ctx context.Context, link *OIDCLink) error {
	if link == nil {
		return fmt.Errorf("link required")
	}

	id, err := s.insertReturningID(ctx, `
		INSERT INTO oidc_links (provider_slug, subject, email, user_id, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, link.ProviderSlug, link.Subject, link.Email, link.UserID, time.Now().UTC())
	if err != nil {
		return err
	}
	link.ID = id
	return nil
}

// GetOIDCLink retrieves an OIDC link by provider and subject
func (s *BaseStore) GetOIDCLink(ctx context.Context, providerSlug, subject string) (*OIDCLink, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, provider_slug, subject, email, user_id, created_at
		FROM oidc_links WHERE provider_slug = ? AND subject = ?
	`, providerSlug, subject)

	var link OIDCLink
	if err := row.Scan(&link.ID, &link.ProviderSlug, &link.Subject, &link.Email, &link.UserID, &link.CreatedAt); err != nil {
		return nil, err
	}
	return &link, nil
}

// ListOIDCLinksForUser returns all OIDC links for a user
func (s *BaseStore) ListOIDCLinksForUser(ctx context.Context, userID int64) ([]*OIDCLink, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, provider_slug, subject, email, user_id, created_at
		FROM oidc_links WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []*OIDCLink
	for rows.Next() {
		var link OIDCLink
		if err := rows.Scan(&link.ID, &link.ProviderSlug, &link.Subject, &link.Email, &link.UserID, &link.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, &link)
	}
	return links, rows.Err()
}

// DeleteOIDCLink removes an OIDC link
func (s *BaseStore) DeleteOIDCLink(ctx context.Context, providerSlug, subject string) error {
	_, err := s.execContext(ctx, `DELETE FROM oidc_links WHERE provider_slug = ? AND subject = ?`, providerSlug, subject)
	return err
}

// ============================================================================
// Settings Methods
// ============================================================================

// GetGlobalSettings returns the persisted global settings snapshot
func (s *BaseStore) GetGlobalSettings(ctx context.Context) (*SettingsRecord, error) {
	query := `SELECT schema_version, payload, updated_at, COALESCE(updated_by, '') FROM settings_global WHERE id = 1`

	var schemaVersion string
	var payload sql.NullString
	var updatedAt sql.NullTime
	var updatedBy string

	err := s.queryRowContext(ctx, query).Scan(&schemaVersion, &payload, &updatedAt, &updatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Payload is stored as a flat object containing the Settings fields plus optional managed_sections.
	// This keeps wire format compatible with the server web UI, which sends:
	// { discovery: {...}, snmp: {...}, features: {...}, logging: {...}, web: {...}, managed_sections: [...] }
	type globalPayload struct {
		pmsettings.Settings
		ManagedSections []string `json:"managed_sections,omitempty"`
	}
	gp := globalPayload{Settings: pmsettings.DefaultSettings()}
	if payload.Valid && payload.String != "" {
		if err := json.Unmarshal([]byte(payload.String), &gp); err != nil {
			return nil, fmt.Errorf("failed to decode global settings: %w", err)
		}
	}
	pmsettings.Sanitize(&gp.Settings)

	rec := &SettingsRecord{
		SchemaVersion:   schemaVersion,
		Settings:        gp.Settings,
		ManagedSections: gp.ManagedSections,
		UpdatedBy:       updatedBy,
	}
	if updatedAt.Valid {
		rec.UpdatedAt = updatedAt.Time
	}
	return rec, nil
}

// UpsertGlobalSettings replaces the canonical global settings row
func (s *BaseStore) UpsertGlobalSettings(ctx context.Context, rec *SettingsRecord) error {
	if rec == nil {
		return fmt.Errorf("settings record required")
	}
	pmsettings.Sanitize(&rec.Settings)
	// Persist as a flat object containing Settings fields plus managed_sections.
	type globalPayload struct {
		pmsettings.Settings
		ManagedSections []string `json:"managed_sections,omitempty"`
	}
	payload, err := json.Marshal(globalPayload{Settings: rec.Settings, ManagedSections: rec.ManagedSections})
	if err != nil {
		return fmt.Errorf("failed to encode settings: %w", err)
	}
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = pmsettings.SchemaVersion
	}
	if rec.UpdatedBy == "" {
		rec.UpdatedBy = "system"
	}
	rec.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO settings_global (id, schema_version, payload, updated_at, updated_by)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			schema_version = excluded.schema_version,
			payload = excluded.payload,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by
	`
	_, err = s.execContext(ctx, query, rec.SchemaVersion, string(payload), rec.UpdatedAt, rec.UpdatedBy)
	return err
}

// GetTenantSettings returns tenant-specific overrides (if any)
func (s *BaseStore) GetTenantSettings(ctx context.Context, tenantID string) (*TenantSettingsRecord, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("tenant id required")
	}

	query := `SELECT tenant_id, schema_version, payload, updated_at, COALESCE(updated_by, '') FROM settings_tenant WHERE tenant_id = ?`

	var id, schemaVersion string
	var payload sql.NullString
	var updatedAt sql.NullTime
	var updatedBy string

	err := s.queryRowContext(ctx, query, tenantID).Scan(&id, &schemaVersion, &payload, &updatedAt, &updatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var enforcedSections []string
	overrides := map[string]interface{}{}
	if payload.Valid && payload.String != "" {
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(payload.String), &raw); err != nil {
			return nil, fmt.Errorf("failed to decode tenant settings: %w", err)
		}
		// Back-compat: older rows store overrides map directly.
		if ov, ok := raw["overrides"]; ok {
			if ovMap, ok := ov.(map[string]interface{}); ok {
				overrides = ovMap
			}
			if es, ok := raw["enforced_sections"]; ok {
				enforcedSections = parseStringSlice(es)
			}
		} else {
			overrides = raw
		}
	}

	rec := &TenantSettingsRecord{
		TenantID:         id,
		SchemaVersion:    schemaVersion,
		Overrides:        overrides,
		EnforcedSections: enforcedSections,
		UpdatedBy:        updatedBy,
	}
	if updatedAt.Valid {
		rec.UpdatedAt = updatedAt.Time
	}
	return rec, nil
}

// UpsertTenantSettings stores tenant override patches
func (s *BaseStore) UpsertTenantSettings(ctx context.Context, rec *TenantSettingsRecord) error {
	if rec == nil {
		return fmt.Errorf("tenant settings record required")
	}
	if strings.TrimSpace(rec.TenantID) == "" {
		return fmt.Errorf("tenant id required")
	}
	if rec.Overrides == nil {
		rec.Overrides = map[string]interface{}{}
	}
	// Persist tenant settings as wrapper containing overrides + enforcement metadata.
	payloadObj := map[string]interface{}{
		"overrides":         rec.Overrides,
		"enforced_sections": rec.EnforcedSections,
	}
	payload, err := json.Marshal(payloadObj)
	if err != nil {
		return fmt.Errorf("failed to encode overrides: %w", err)
	}
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = pmsettings.SchemaVersion
	}
	if rec.UpdatedBy == "" {
		rec.UpdatedBy = "system"
	}
	rec.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO settings_tenant (tenant_id, schema_version, payload, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
			schema_version = excluded.schema_version,
			payload = excluded.payload,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by
	`
	_, err = s.execContext(ctx, query, rec.TenantID, rec.SchemaVersion, string(payload), rec.UpdatedAt, rec.UpdatedBy)
	return err
}

// DeleteTenantSettings removes overrides for a tenant
func (s *BaseStore) DeleteTenantSettings(ctx context.Context, tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("tenant id required")
	}
	_, err := s.execContext(ctx, `DELETE FROM settings_tenant WHERE tenant_id = ?`, tenantID)
	return err
}

// ListTenantSettings returns all tenants with overrides
func (s *BaseStore) ListTenantSettings(ctx context.Context) ([]*TenantSettingsRecord, error) {
	rows, err := s.queryContext(ctx, `SELECT tenant_id, schema_version, payload, updated_at, COALESCE(updated_by, '') FROM settings_tenant ORDER BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*TenantSettingsRecord
	for rows.Next() {
		var tenantID, schemaVersion string
		var payload sql.NullString
		var updatedAt sql.NullTime
		var updatedBy string

		if err := rows.Scan(&tenantID, &schemaVersion, &payload, &updatedAt, &updatedBy); err != nil {
			return nil, err
		}

		var enforcedSections []string
		overrides := map[string]interface{}{}
		if payload.Valid && payload.String != "" {
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(payload.String), &raw); err != nil {
				return nil, fmt.Errorf("failed to decode tenant settings: %w", err)
			}
			if ov, ok := raw["overrides"]; ok {
				if ovMap, ok := ov.(map[string]interface{}); ok {
					overrides = ovMap
				}
				if es, ok := raw["enforced_sections"]; ok {
					enforcedSections = parseStringSlice(es)
				}
			} else {
				overrides = raw
			}
		}

		rec := &TenantSettingsRecord{
			TenantID:         tenantID,
			SchemaVersion:    schemaVersion,
			Overrides:        overrides,
			EnforcedSections: enforcedSections,
			UpdatedBy:        updatedBy,
		}
		if updatedAt.Valid {
			rec.UpdatedAt = updatedAt.Time
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func parseStringSlice(raw interface{}) []string {
	if raw == nil {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	seen := make(map[string]bool)
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// GetAgentSettings returns agent-specific overrides (if any)
func (s *BaseStore) GetAgentSettings(ctx context.Context, agentID string) (*AgentSettingsRecord, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("agent id required")
	}
	query := `SELECT agent_id, schema_version, payload, updated_at, COALESCE(updated_by, '') FROM settings_agent_override WHERE agent_id = ?`

	var id, schemaVersion string
	var payload sql.NullString
	var updatedAt sql.NullTime
	var updatedBy string

	err := s.queryRowContext(ctx, query, agentID).Scan(&id, &schemaVersion, &payload, &updatedAt, &updatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	overrides := map[string]interface{}{}
	if payload.Valid && payload.String != "" {
		if err := json.Unmarshal([]byte(payload.String), &overrides); err != nil {
			return nil, fmt.Errorf("failed to decode agent settings: %w", err)
		}
	}

	rec := &AgentSettingsRecord{
		AgentID:       id,
		SchemaVersion: schemaVersion,
		Overrides:     overrides,
		UpdatedBy:     updatedBy,
	}
	if updatedAt.Valid {
		rec.UpdatedAt = updatedAt.Time
	}
	return rec, nil
}

// UpsertAgentSettings stores agent override patches
func (s *BaseStore) UpsertAgentSettings(ctx context.Context, rec *AgentSettingsRecord) error {
	if rec == nil {
		return fmt.Errorf("agent settings record required")
	}
	if strings.TrimSpace(rec.AgentID) == "" {
		return fmt.Errorf("agent id required")
	}
	if rec.Overrides == nil {
		rec.Overrides = map[string]interface{}{}
	}

	payload, err := json.Marshal(rec.Overrides)
	if err != nil {
		return fmt.Errorf("failed to encode overrides: %w", err)
	}
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = pmsettings.SchemaVersion
	}
	if rec.UpdatedBy == "" {
		rec.UpdatedBy = "system"
	}
	rec.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO settings_agent_override (agent_id, schema_version, payload, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			schema_version = excluded.schema_version,
			payload = excluded.payload,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by
	`
	_, err = s.execContext(ctx, query, rec.AgentID, rec.SchemaVersion, string(payload), rec.UpdatedAt, rec.UpdatedBy)
	return err
}

// DeleteAgentSettings removes overrides for an agent
func (s *BaseStore) DeleteAgentSettings(ctx context.Context, agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("agent id required")
	}
	_, err := s.execContext(ctx, `DELETE FROM settings_agent_override WHERE agent_id = ?`, agentID)
	return err
}

// ============================================================================
// Fleet Update Policy Methods
// ============================================================================

// GetFleetUpdatePolicy retrieves the update policy for a tenant
func (s *BaseStore) GetFleetUpdatePolicy(ctx context.Context, tenantID string) (*FleetUpdatePolicy, error) {
	if tenantID == GlobalFleetPolicyTenantID {
		return s.getGlobalFleetUpdatePolicy(ctx)
	}

	row := s.queryRowContext(ctx, `
		SELECT tenant_id, update_check_days, version_pin_strategy, allow_major_upgrade, target_version,
		       maintenance_window_enabled, maintenance_window_start_hour, maintenance_window_start_min,
		       maintenance_window_end_hour, maintenance_window_end_min, maintenance_window_timezone,
		       maintenance_window_days, rollout_staggered, rollout_max_concurrent, rollout_batch_size,
		       rollout_delay_between_waves, rollout_jitter_seconds, rollout_emergency_abort,
		       collect_telemetry, updated_at
		FROM fleet_update_policies
		WHERE tenant_id = ?
	`, tenantID)

	return s.scanFleetUpdatePolicy(row, false)
}

func (s *BaseStore) getGlobalFleetUpdatePolicy(ctx context.Context) (*FleetUpdatePolicy, error) {
	row := s.queryRowContext(ctx, `
		SELECT update_check_days, version_pin_strategy, allow_major_upgrade, target_version,
		       maintenance_window_enabled, maintenance_window_start_hour, maintenance_window_start_min,
		       maintenance_window_end_hour, maintenance_window_end_min, maintenance_window_timezone,
		       maintenance_window_days, rollout_staggered, rollout_max_concurrent, rollout_batch_size,
		       rollout_delay_between_waves, rollout_jitter_seconds, rollout_emergency_abort,
		       collect_telemetry, updated_at
		FROM fleet_update_policy_global
		WHERE singleton = 1
	`)

	return s.scanFleetUpdatePolicy(row, true)
}

func (s *BaseStore) scanFleetUpdatePolicy(row *sql.Row, isGlobal bool) (*FleetUpdatePolicy, error) {
	var (
		tid                   string
		updateCheckDays       int
		pinStrategy           string
		allowMajor            interface{}
		targetVersion         sql.NullString
		mwEnabled             interface{}
		mwStartHour           int
		mwStartMin            int
		mwEndHour             int
		mwEndMin              int
		mwTimezone            string
		mwDaysJSON            sql.NullString
		rolloutStaggered      interface{}
		rolloutMaxConcurrent  int
		rolloutBatchSize      int
		rolloutDelayWaves     int
		rolloutJitterSec      int
		rolloutEmergencyAbort interface{}
		collectTelemetry      interface{}
		updatedAt             time.Time
	)

	var err error
	if isGlobal {
		tid = GlobalFleetPolicyTenantID
		err = row.Scan(&updateCheckDays, &pinStrategy, &allowMajor, &targetVersion,
			&mwEnabled, &mwStartHour, &mwStartMin, &mwEndHour, &mwEndMin, &mwTimezone, &mwDaysJSON,
			&rolloutStaggered, &rolloutMaxConcurrent, &rolloutBatchSize, &rolloutDelayWaves,
			&rolloutJitterSec, &rolloutEmergencyAbort, &collectTelemetry, &updatedAt)
	} else {
		err = row.Scan(&tid, &updateCheckDays, &pinStrategy, &allowMajor, &targetVersion,
			&mwEnabled, &mwStartHour, &mwStartMin, &mwEndHour, &mwEndMin, &mwTimezone, &mwDaysJSON,
			&rolloutStaggered, &rolloutMaxConcurrent, &rolloutBatchSize, &rolloutDelayWaves,
			&rolloutJitterSec, &rolloutEmergencyAbort, &collectTelemetry, &updatedAt)
	}

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var daysOfWeek []int
	if mwDaysJSON.Valid && mwDaysJSON.String != "" {
		if err := json.Unmarshal([]byte(mwDaysJSON.String), &daysOfWeek); err != nil {
			return nil, fmt.Errorf("failed to decode days_of_week: %w", err)
		}
	}

	return &FleetUpdatePolicy{
		TenantID:  tid,
		UpdatedAt: updatedAt,
		PolicySpec: PolicySpec{
			UpdateCheckDays:    updateCheckDays,
			VersionPinStrategy: VersionPinStrategy(pinStrategy),
			AllowMajorUpgrade:  intToBool(allowMajor),
			TargetVersion:      targetVersion.String,
			CollectTelemetry:   intToBool(collectTelemetry),
			MaintenanceWindow: MaintenanceWindow{
				Enabled:    intToBool(mwEnabled),
				StartHour:  mwStartHour,
				StartMin:   mwStartMin,
				EndHour:    mwEndHour,
				EndMin:     mwEndMin,
				Timezone:   mwTimezone,
				DaysOfWeek: daysOfWeek,
			},
			RolloutControl: RolloutControl{
				Staggered:         intToBool(rolloutStaggered),
				MaxConcurrent:     rolloutMaxConcurrent,
				BatchSize:         rolloutBatchSize,
				DelayBetweenWaves: rolloutDelayWaves,
				JitterSeconds:     rolloutJitterSec,
				EmergencyAbort:    intToBool(rolloutEmergencyAbort),
			},
		},
	}, nil
}

// UpsertFleetUpdatePolicy creates or updates a tenant's update policy
func (s *BaseStore) UpsertFleetUpdatePolicy(ctx context.Context, policy *FleetUpdatePolicy) error {
	if policy == nil {
		return fmt.Errorf("policy cannot be nil")
	}

	if policy.TenantID == GlobalFleetPolicyTenantID {
		return s.upsertGlobalFleetUpdatePolicy(ctx, policy)
	}

	daysJSON, err := json.Marshal(policy.MaintenanceWindow.DaysOfWeek)
	if err != nil {
		return fmt.Errorf("failed to encode days_of_week: %w", err)
	}

	policy.UpdatedAt = time.Now().UTC()

	_, err = s.execContext(ctx, `
		INSERT INTO fleet_update_policies (
			tenant_id, update_check_days, version_pin_strategy, allow_major_upgrade, target_version,
			maintenance_window_enabled, maintenance_window_start_hour, maintenance_window_start_min,
			maintenance_window_end_hour, maintenance_window_end_min, maintenance_window_timezone,
			maintenance_window_days, rollout_staggered, rollout_max_concurrent, rollout_batch_size,
			rollout_delay_between_waves, rollout_jitter_seconds, rollout_emergency_abort,
			collect_telemetry, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
			update_check_days = excluded.update_check_days,
			version_pin_strategy = excluded.version_pin_strategy,
			allow_major_upgrade = excluded.allow_major_upgrade,
			target_version = excluded.target_version,
			maintenance_window_enabled = excluded.maintenance_window_enabled,
			maintenance_window_start_hour = excluded.maintenance_window_start_hour,
			maintenance_window_start_min = excluded.maintenance_window_start_min,
			maintenance_window_end_hour = excluded.maintenance_window_end_hour,
			maintenance_window_end_min = excluded.maintenance_window_end_min,
			maintenance_window_timezone = excluded.maintenance_window_timezone,
			maintenance_window_days = excluded.maintenance_window_days,
			rollout_staggered = excluded.rollout_staggered,
			rollout_max_concurrent = excluded.rollout_max_concurrent,
			rollout_batch_size = excluded.rollout_batch_size,
			rollout_delay_between_waves = excluded.rollout_delay_between_waves,
			rollout_jitter_seconds = excluded.rollout_jitter_seconds,
			rollout_emergency_abort = excluded.rollout_emergency_abort,
			collect_telemetry = excluded.collect_telemetry,
			updated_at = excluded.updated_at
	`,
		policy.TenantID,
		policy.UpdateCheckDays,
		string(policy.VersionPinStrategy),
		boolToInt(policy.AllowMajorUpgrade),
		nullString(policy.TargetVersion),
		boolToInt(policy.MaintenanceWindow.Enabled),
		policy.MaintenanceWindow.StartHour,
		policy.MaintenanceWindow.StartMin,
		policy.MaintenanceWindow.EndHour,
		policy.MaintenanceWindow.EndMin,
		policy.MaintenanceWindow.Timezone,
		string(daysJSON),
		boolToInt(policy.RolloutControl.Staggered),
		policy.RolloutControl.MaxConcurrent,
		policy.RolloutControl.BatchSize,
		policy.RolloutControl.DelayBetweenWaves,
		policy.RolloutControl.JitterSeconds,
		boolToInt(policy.RolloutControl.EmergencyAbort),
		boolToInt(policy.CollectTelemetry),
		policy.UpdatedAt,
	)
	return err
}

func (s *BaseStore) upsertGlobalFleetUpdatePolicy(ctx context.Context, policy *FleetUpdatePolicy) error {
	if policy == nil {
		return fmt.Errorf("policy cannot be nil")
	}

	daysJSON, err := json.Marshal(policy.MaintenanceWindow.DaysOfWeek)
	if err != nil {
		return fmt.Errorf("failed to encode days_of_week: %w", err)
	}

	policy.UpdatedAt = time.Now().UTC()

	_, err = s.execContext(ctx, `
		INSERT INTO fleet_update_policy_global (
			singleton, update_check_days, version_pin_strategy, allow_major_upgrade, target_version,
			maintenance_window_enabled, maintenance_window_start_hour, maintenance_window_start_min,
			maintenance_window_end_hour, maintenance_window_end_min, maintenance_window_timezone,
			maintenance_window_days, rollout_staggered, rollout_max_concurrent, rollout_batch_size,
			rollout_delay_between_waves, rollout_jitter_seconds, rollout_emergency_abort,
			collect_telemetry, updated_at
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			update_check_days = excluded.update_check_days,
			version_pin_strategy = excluded.version_pin_strategy,
			allow_major_upgrade = excluded.allow_major_upgrade,
			target_version = excluded.target_version,
			maintenance_window_enabled = excluded.maintenance_window_enabled,
			maintenance_window_start_hour = excluded.maintenance_window_start_hour,
			maintenance_window_start_min = excluded.maintenance_window_start_min,
			maintenance_window_end_hour = excluded.maintenance_window_end_hour,
			maintenance_window_end_min = excluded.maintenance_window_end_min,
			maintenance_window_timezone = excluded.maintenance_window_timezone,
			maintenance_window_days = excluded.maintenance_window_days,
			rollout_staggered = excluded.rollout_staggered,
			rollout_max_concurrent = excluded.rollout_max_concurrent,
			rollout_batch_size = excluded.rollout_batch_size,
			rollout_delay_between_waves = excluded.rollout_delay_between_waves,
			rollout_jitter_seconds = excluded.rollout_jitter_seconds,
			rollout_emergency_abort = excluded.rollout_emergency_abort,
			collect_telemetry = excluded.collect_telemetry,
			updated_at = excluded.updated_at
	`,
		policy.UpdateCheckDays,
		string(policy.VersionPinStrategy),
		boolToInt(policy.AllowMajorUpgrade),
		nullString(policy.TargetVersion),
		boolToInt(policy.MaintenanceWindow.Enabled),
		policy.MaintenanceWindow.StartHour,
		policy.MaintenanceWindow.StartMin,
		policy.MaintenanceWindow.EndHour,
		policy.MaintenanceWindow.EndMin,
		policy.MaintenanceWindow.Timezone,
		string(daysJSON),
		boolToInt(policy.RolloutControl.Staggered),
		policy.RolloutControl.MaxConcurrent,
		policy.RolloutControl.BatchSize,
		policy.RolloutControl.DelayBetweenWaves,
		policy.RolloutControl.JitterSeconds,
		boolToInt(policy.RolloutControl.EmergencyAbort),
		boolToInt(policy.CollectTelemetry),
		policy.UpdatedAt,
	)
	return err
}

// DeleteFleetUpdatePolicy removes a tenant's update policy
func (s *BaseStore) DeleteFleetUpdatePolicy(ctx context.Context, tenantID string) error {
	if tenantID == GlobalFleetPolicyTenantID {
		_, err := s.execContext(ctx, `DELETE FROM fleet_update_policy_global WHERE singleton = 1`)
		return err
	}
	_, err := s.execContext(ctx, `DELETE FROM fleet_update_policies WHERE tenant_id = ?`, tenantID)
	return err
}

// ListFleetUpdatePolicies returns all configured update policies
func (s *BaseStore) ListFleetUpdatePolicies(ctx context.Context) ([]*FleetUpdatePolicy, error) {
	var policies []*FleetUpdatePolicy

	// Get global policy first
	if globalPolicy, err := s.getGlobalFleetUpdatePolicy(ctx); err != nil {
		return nil, err
	} else if globalPolicy != nil {
		policies = append(policies, globalPolicy)
	}

	// Get tenant policies
	rows, err := s.queryContext(ctx, `
		SELECT tenant_id, update_check_days, version_pin_strategy, allow_major_upgrade, target_version,
		       maintenance_window_enabled, maintenance_window_start_hour, maintenance_window_start_min,
		       maintenance_window_end_hour, maintenance_window_end_min, maintenance_window_timezone,
		       maintenance_window_days, rollout_staggered, rollout_max_concurrent, rollout_batch_size,
		       rollout_delay_between_waves, rollout_jitter_seconds, rollout_emergency_abort,
		       collect_telemetry, updated_at
		FROM fleet_update_policies
		ORDER BY tenant_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tid                   string
			updateCheckDays       int
			pinStrategy           string
			allowMajor            int
			targetVersion         sql.NullString
			mwEnabled             int
			mwStartHour           int
			mwStartMin            int
			mwEndHour             int
			mwEndMin              int
			mwTimezone            string
			mwDaysJSON            sql.NullString
			rolloutStaggered      int
			rolloutMaxConcurrent  int
			rolloutBatchSize      int
			rolloutDelayWaves     int
			rolloutJitterSec      int
			rolloutEmergencyAbort int
			collectTelemetry      int
			updatedAt             time.Time
		)

		if err := rows.Scan(&tid, &updateCheckDays, &pinStrategy, &allowMajor, &targetVersion,
			&mwEnabled, &mwStartHour, &mwStartMin, &mwEndHour, &mwEndMin, &mwTimezone, &mwDaysJSON,
			&rolloutStaggered, &rolloutMaxConcurrent, &rolloutBatchSize, &rolloutDelayWaves,
			&rolloutJitterSec, &rolloutEmergencyAbort, &collectTelemetry, &updatedAt); err != nil {
			return nil, err
		}

		var daysOfWeek []int
		if mwDaysJSON.Valid && mwDaysJSON.String != "" {
			if err := json.Unmarshal([]byte(mwDaysJSON.String), &daysOfWeek); err != nil {
				return nil, fmt.Errorf("failed to decode days_of_week: %w", err)
			}
		}

		policy := &FleetUpdatePolicy{
			TenantID:  tid,
			UpdatedAt: updatedAt,
			PolicySpec: PolicySpec{
				UpdateCheckDays:    updateCheckDays,
				VersionPinStrategy: VersionPinStrategy(pinStrategy),
				AllowMajorUpgrade:  allowMajor != 0,
				TargetVersion:      targetVersion.String,
				CollectTelemetry:   collectTelemetry != 0,
				MaintenanceWindow: MaintenanceWindow{
					Enabled:    mwEnabled != 0,
					StartHour:  mwStartHour,
					StartMin:   mwStartMin,
					EndHour:    mwEndHour,
					EndMin:     mwEndMin,
					Timezone:   mwTimezone,
					DaysOfWeek: daysOfWeek,
				},
				RolloutControl: RolloutControl{
					Staggered:         rolloutStaggered != 0,
					MaxConcurrent:     rolloutMaxConcurrent,
					BatchSize:         rolloutBatchSize,
					DelayBetweenWaves: rolloutDelayWaves,
					JitterSeconds:     rolloutJitterSec,
					EmergencyAbort:    rolloutEmergencyAbort != 0,
				},
			},
		}
		policies = append(policies, policy)
	}

	return policies, rows.Err()
}

// ============================================================================
// Release Artifact Methods
// ============================================================================

// UpsertReleaseArtifact stores or updates metadata for a cached release artifact
func (s *BaseStore) UpsertReleaseArtifact(ctx context.Context, artifact *ReleaseArtifact) error {
	if artifact == nil {
		return fmt.Errorf("artifact cannot be nil")
	}
	if artifact.Component == "" || artifact.Version == "" || artifact.Platform == "" || artifact.Arch == "" {
		return fmt.Errorf("artifact missing required identity fields")
	}
	if artifact.Channel == "" {
		artifact.Channel = "stable"
	}
	if artifact.SourceURL == "" {
		return fmt.Errorf("artifact source url required")
	}

	artifact.UpdatedAt = time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = artifact.UpdatedAt
	}

	_, err := s.execContext(ctx, `
		INSERT INTO release_artifacts (
			component, version, platform, arch, channel, source_url,
			cache_path, sha256, size_bytes, release_notes, published_at,
			downloaded_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(component, version, platform, arch) DO UPDATE SET
			channel = excluded.channel,
			source_url = excluded.source_url,
			cache_path = excluded.cache_path,
			sha256 = excluded.sha256,
			size_bytes = excluded.size_bytes,
			release_notes = excluded.release_notes,
			published_at = excluded.published_at,
			downloaded_at = excluded.downloaded_at,
			updated_at = excluded.updated_at
	`,
		artifact.Component, artifact.Version, artifact.Platform, artifact.Arch, artifact.Channel, artifact.SourceURL,
		nullString(artifact.CachePath), nullString(artifact.SHA256), artifact.SizeBytes, nullString(artifact.ReleaseNotes),
		nullTime(artifact.PublishedAt), nullTime(artifact.DownloadedAt), artifact.CreatedAt, artifact.UpdatedAt)
	return err
}

// GetReleaseArtifact returns the cached artifact metadata for the requested tuple
func (s *BaseStore) GetReleaseArtifact(ctx context.Context, component, version, platform, arch string) (*ReleaseArtifact, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, component, version, platform, arch, channel, source_url,
		       cache_path, sha256, size_bytes, release_notes, published_at,
		       downloaded_at, created_at, updated_at
		FROM release_artifacts
		WHERE component = ? AND version = ? AND platform = ? AND arch = ?
	`, component, version, platform, arch)
	return s.scanReleaseArtifact(row)
}

// ListReleaseArtifacts lists cached artifacts for a component ordered by publish date
func (s *BaseStore) ListReleaseArtifacts(ctx context.Context, component string, limit int) ([]*ReleaseArtifact, error) {
	query := `
		SELECT id, component, version, platform, arch, channel, source_url,
		       cache_path, sha256, size_bytes, release_notes, published_at,
		       downloaded_at, created_at, updated_at
		FROM release_artifacts
		WHERE (? = '' OR component = ?)
		ORDER BY published_at DESC, created_at DESC
	`
	args := []interface{}{component, component}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []*ReleaseArtifact
	for rows.Next() {
		artifact, serr := s.scanReleaseArtifactRow(rows)
		if serr != nil {
			return nil, serr
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (s *BaseStore) scanReleaseArtifact(row *sql.Row) (*ReleaseArtifact, error) {
	var (
		id                   int64
		component            string
		version              string
		platform             string
		arch                 string
		channel              string
		sourceURL            string
		cachePath            sql.NullString
		sha                  sql.NullString
		sizeBytes            int64
		releaseNotes         sql.NullString
		publishedAt          sql.NullTime
		downloadedAt         sql.NullTime
		createdAt, updatedAt time.Time
	)

	if err := row.Scan(&id, &component, &version, &platform, &arch, &channel, &sourceURL,
		&cachePath, &sha, &sizeBytes, &releaseNotes, &publishedAt, &downloadedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	artifact := &ReleaseArtifact{
		ID:        id,
		Component: component,
		Version:   version,
		Platform:  platform,
		Arch:      arch,
		Channel:   channel,
		SourceURL: sourceURL,
		SizeBytes: sizeBytes,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	if cachePath.Valid {
		artifact.CachePath = cachePath.String
	}
	if sha.Valid {
		artifact.SHA256 = sha.String
	}
	if releaseNotes.Valid {
		artifact.ReleaseNotes = releaseNotes.String
	}
	if publishedAt.Valid {
		artifact.PublishedAt = publishedAt.Time
	}
	if downloadedAt.Valid {
		artifact.DownloadedAt = downloadedAt.Time
	}
	return artifact, nil
}

func (s *BaseStore) scanReleaseArtifactRow(rows *sql.Rows) (*ReleaseArtifact, error) {
	var (
		id                   int64
		component            string
		version              string
		platform             string
		arch                 string
		channel              string
		sourceURL            string
		cachePath            sql.NullString
		sha                  sql.NullString
		sizeBytes            int64
		releaseNotes         sql.NullString
		publishedAt          sql.NullTime
		downloadedAt         sql.NullTime
		createdAt, updatedAt time.Time
	)

	if err := rows.Scan(&id, &component, &version, &platform, &arch, &channel, &sourceURL,
		&cachePath, &sha, &sizeBytes, &releaseNotes, &publishedAt, &downloadedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	artifact := &ReleaseArtifact{
		ID:        id,
		Component: component,
		Version:   version,
		Platform:  platform,
		Arch:      arch,
		Channel:   channel,
		SourceURL: sourceURL,
		SizeBytes: sizeBytes,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	if cachePath.Valid {
		artifact.CachePath = cachePath.String
	}
	if sha.Valid {
		artifact.SHA256 = sha.String
	}
	if releaseNotes.Valid {
		artifact.ReleaseNotes = releaseNotes.String
	}
	if publishedAt.Valid {
		artifact.PublishedAt = publishedAt.Time
	}
	if downloadedAt.Valid {
		artifact.DownloadedAt = downloadedAt.Time
	}
	return artifact, nil
}

// DeleteReleaseArtifact removes an artifact by id
func (s *BaseStore) DeleteReleaseArtifact(ctx context.Context, id int64) error {
	_, err := s.execContext(ctx, `DELETE FROM release_artifacts WHERE id = ?`, id)
	return err
}

// ListArtifactsForPruning returns artifacts for versions beyond the keepVersions threshold.
// For each component, it keeps the newest keepVersions distinct versions and returns
// all artifacts belonging to older versions (to be pruned).
func (s *BaseStore) ListArtifactsForPruning(ctx context.Context, component string, keepVersions int) ([]*ReleaseArtifact, error) {
	if keepVersions <= 0 {
		return nil, nil // No pruning when retention is disabled
	}

	// Strategy: find distinct versions per component ordered by published_at DESC,
	// skip the first keepVersions, then return all artifacts for remaining versions.
	// Using a subquery to get versions to keep, then exclude them.
	query := `
		WITH versions_to_keep AS (
			SELECT DISTINCT version
			FROM release_artifacts
			WHERE component = ?
			ORDER BY MAX(published_at) DESC, MAX(created_at) DESC
			LIMIT ?
		)
		SELECT id, component, version, platform, arch, channel, source_url,
		       cache_path, sha256, size_bytes, release_notes, published_at,
		       downloaded_at, created_at, updated_at
		FROM release_artifacts
		WHERE component = ? AND version NOT IN (SELECT version FROM versions_to_keep)
		ORDER BY published_at ASC
	`

	// The CTE with GROUP BY for proper ordering
	query = `
		WITH ranked_versions AS (
			SELECT version, MAX(published_at) as max_pub, MAX(created_at) as max_created
			FROM release_artifacts
			WHERE component = ?
			GROUP BY version
			ORDER BY max_pub DESC, max_created DESC
			LIMIT ?
		)
		SELECT id, component, version, platform, arch, channel, source_url,
		       cache_path, sha256, size_bytes, release_notes, published_at,
		       downloaded_at, created_at, updated_at
		FROM release_artifacts
		WHERE component = ? AND version NOT IN (SELECT version FROM ranked_versions)
		ORDER BY published_at ASC
	`

	rows, err := s.queryContext(ctx, query, component, keepVersions, component)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []*ReleaseArtifact
	for rows.Next() {
		artifact, serr := s.scanReleaseArtifactRow(rows)
		if serr != nil {
			return nil, serr
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// ============================================================================
// Signing Key Methods
// ============================================================================

// CreateSigningKey persists a new signing key record
func (s *BaseStore) CreateSigningKey(ctx context.Context, key *SigningKey) error {
	if key == nil {
		return fmt.Errorf("signing key cannot be nil")
	}
	if key.ID == "" || key.Algorithm == "" {
		return fmt.Errorf("signing key id and algorithm required")
	}
	if key.PublicKey == "" || key.PrivateKey == "" {
		return fmt.Errorf("signing key material incomplete")
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}

	_, err := s.execContext(ctx, `
		INSERT INTO signing_keys (id, algorithm, public_key, private_key, notes, active, created_at, rotated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, key.ID, key.Algorithm, key.PublicKey, key.PrivateKey, nullString(key.Notes), boolToInt(key.Active), key.CreatedAt, nullTime(key.RotatedAt))
	return err
}

// GetSigningKey loads key metadata (including private material) by id
func (s *BaseStore) GetSigningKey(ctx context.Context, id string) (*SigningKey, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, algorithm, public_key, private_key, notes, active, created_at, rotated_at
		FROM signing_keys WHERE id = ?
	`, id)
	return s.scanSigningKey(row)
}

// GetActiveSigningKey retrieves the currently active signing key
func (s *BaseStore) GetActiveSigningKey(ctx context.Context) (*SigningKey, error) {
	query := fmt.Sprintf(`SELECT id, algorithm, public_key, private_key, notes, active, created_at, rotated_at FROM signing_keys WHERE active = %s LIMIT 1`, s.dialect.BoolValue(true))
	row := s.queryRowContext(ctx, query)
	return s.scanSigningKey(row)
}

// ListSigningKeys returns signing key metadata ordered by creation recency
func (s *BaseStore) ListSigningKeys(ctx context.Context, limit int) ([]*SigningKey, error) {
	query := `
		SELECT id, algorithm, public_key, private_key, notes, active, created_at, rotated_at
		FROM signing_keys
		ORDER BY created_at DESC
	`
	args := []interface{}{}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*SigningKey
	for rows.Next() {
		key, serr := s.scanSigningKeyRow(rows)
		if serr != nil {
			return nil, serr
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// SetSigningKeyActive marks the provided key as active and deactivates others
func (s *BaseStore) SetSigningKeyActive(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("signing key id required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Deactivate other keys
	deactivateQuery := s.query(fmt.Sprintf(`UPDATE signing_keys SET active = %s, rotated_at = CASE WHEN rotated_at IS NULL THEN ? ELSE rotated_at END WHERE active = %s AND id != ?`, s.dialect.BoolValue(false), s.dialect.BoolValue(true)))
	if _, err = tx.ExecContext(ctx, deactivateQuery, time.Now().UTC(), id); err != nil {
		return err
	}

	// Activate target key
	activateQuery := s.query(fmt.Sprintf(`UPDATE signing_keys SET active = %s, rotated_at = NULL WHERE id = ?`, s.dialect.BoolValue(true)))
	result, err := tx.ExecContext(ctx, activateQuery, id)
	if err != nil {
		return err
	}
	affected, aerr := result.RowsAffected()
	if aerr != nil {
		return aerr
	}
	if affected == 0 {
		return fmt.Errorf("signing key %s not found", id)
	}
	return tx.Commit()
}

func (s *BaseStore) scanSigningKey(row *sql.Row) (*SigningKey, error) {
	var (
		id         string
		algorithm  string
		publicKey  string
		privateKey string
		notes      sql.NullString
		active     bool
		createdAt  time.Time
		rotatedAt  sql.NullTime
	)
	if err := row.Scan(&id, &algorithm, &publicKey, &privateKey, &notes, &active, &createdAt, &rotatedAt); err != nil {
		return nil, err
	}
	key := &SigningKey{
		ID:         id,
		Algorithm:  algorithm,
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		Active:     active,
		CreatedAt:  createdAt,
	}
	if notes.Valid {
		key.Notes = notes.String
	}
	if rotatedAt.Valid {
		key.RotatedAt = rotatedAt.Time
	}
	return key, nil
}

func (s *BaseStore) scanSigningKeyRow(rows *sql.Rows) (*SigningKey, error) {
	var (
		id         string
		algorithm  string
		publicKey  string
		privateKey string
		notes      sql.NullString
		active     bool
		createdAt  time.Time
		rotatedAt  sql.NullTime
	)
	if err := rows.Scan(&id, &algorithm, &publicKey, &privateKey, &notes, &active, &createdAt, &rotatedAt); err != nil {
		return nil, err
	}
	key := &SigningKey{
		ID:         id,
		Algorithm:  algorithm,
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		Active:     active,
		CreatedAt:  createdAt,
	}
	if notes.Valid {
		key.Notes = notes.String
	}
	if rotatedAt.Valid {
		key.RotatedAt = rotatedAt.Time
	}
	return key, nil
}

// ============================================================================
// Release Manifest Methods
// ============================================================================

// UpsertReleaseManifest stores the signed manifest for an artifact tuple
func (s *BaseStore) UpsertReleaseManifest(ctx context.Context, manifest *ReleaseManifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest cannot be nil")
	}
	if manifest.Component == "" || manifest.Version == "" || manifest.Platform == "" || manifest.Arch == "" {
		return fmt.Errorf("manifest identity incomplete")
	}
	if manifest.Channel == "" {
		manifest.Channel = "stable"
	}
	if manifest.ManifestVersion == "" || manifest.ManifestJSON == "" || manifest.Signature == "" {
		return fmt.Errorf("manifest payload incomplete")
	}
	if manifest.SigningKeyID == "" {
		return fmt.Errorf("manifest missing signing key reference")
	}

	manifest.UpdatedAt = time.Now().UTC()
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = manifest.UpdatedAt
	}
	if manifest.GeneratedAt.IsZero() {
		manifest.GeneratedAt = manifest.UpdatedAt
	}

	_, err := s.execContext(ctx, `
		INSERT INTO release_manifests (
			component, version, platform, arch, channel,
			manifest_version, manifest_json, signature, signing_key_id,
			generated_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(component, version, platform, arch) DO UPDATE SET
			channel = excluded.channel,
			manifest_version = excluded.manifest_version,
			manifest_json = excluded.manifest_json,
			signature = excluded.signature,
			signing_key_id = excluded.signing_key_id,
			generated_at = excluded.generated_at,
			updated_at = excluded.updated_at
	`,
		manifest.Component, manifest.Version, manifest.Platform, manifest.Arch, manifest.Channel,
		manifest.ManifestVersion, manifest.ManifestJSON, manifest.Signature, manifest.SigningKeyID,
		manifest.GeneratedAt, manifest.CreatedAt, manifest.UpdatedAt)
	return err
}

// GetReleaseManifest fetches the manifest envelope for the given artifact tuple
func (s *BaseStore) GetReleaseManifest(ctx context.Context, component, version, platform, arch string) (*ReleaseManifest, error) {
	row := s.queryRowContext(ctx, `
		SELECT id, component, version, platform, arch, channel,
		       manifest_version, manifest_json, signature, signing_key_id,
		       generated_at, created_at, updated_at
		FROM release_manifests
		WHERE component = ? AND version = ? AND platform = ? AND arch = ?
	`, component, version, platform, arch)
	return s.scanReleaseManifest(row)
}

// ListReleaseManifests enumerates manifests optionally filtered by component
func (s *BaseStore) ListReleaseManifests(ctx context.Context, component string, limit int) ([]*ReleaseManifest, error) {
	query := `
		SELECT id, component, version, platform, arch, channel,
		       manifest_version, manifest_json, signature, signing_key_id,
		       generated_at, created_at, updated_at
		FROM release_manifests
		WHERE (? = '' OR component = ?)
		ORDER BY generated_at DESC, version DESC
	`
	args := []interface{}{component, component}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var manifests []*ReleaseManifest
	for rows.Next() {
		manifest, serr := s.scanReleaseManifestRow(rows)
		if serr != nil {
			return nil, serr
		}
		manifests = append(manifests, manifest)
	}
	return manifests, rows.Err()
}

func (s *BaseStore) scanReleaseManifest(row *sql.Row) (*ReleaseManifest, error) {
	var (
		id              int64
		component       string
		version         string
		platform        string
		arch            string
		channel         string
		manifestVersion string
		manifestJSON    string
		signature       string
		signingKeyID    string
		generatedAt     time.Time
		createdAt       time.Time
		updatedAt       time.Time
	)
	if err := row.Scan(&id, &component, &version, &platform, &arch, &channel,
		&manifestVersion, &manifestJSON, &signature, &signingKeyID,
		&generatedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return &ReleaseManifest{
		ID:              id,
		Component:       component,
		Version:         version,
		Platform:        platform,
		Arch:            arch,
		Channel:         channel,
		ManifestVersion: manifestVersion,
		ManifestJSON:    manifestJSON,
		Signature:       signature,
		SigningKeyID:    signingKeyID,
		GeneratedAt:     generatedAt,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func (s *BaseStore) scanReleaseManifestRow(rows *sql.Rows) (*ReleaseManifest, error) {
	var (
		id              int64
		component       string
		version         string
		platform        string
		arch            string
		channel         string
		manifestVersion string
		manifestJSON    string
		signature       string
		signingKeyID    string
		generatedAt     time.Time
		createdAt       time.Time
		updatedAt       time.Time
	)
	if err := rows.Scan(&id, &component, &version, &platform, &arch, &channel,
		&manifestVersion, &manifestJSON, &signature, &signingKeyID,
		&generatedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return &ReleaseManifest{
		ID:              id,
		Component:       component,
		Version:         version,
		Platform:        platform,
		Arch:            arch,
		Channel:         channel,
		ManifestVersion: manifestVersion,
		ManifestJSON:    manifestJSON,
		Signature:       signature,
		SigningKeyID:    signingKeyID,
		GeneratedAt:     generatedAt,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

// ============================================================================
// Self-Update Run Methods
// ============================================================================
// Self-Update Run Methods
// ============================================================================

// CreateSelfUpdateRun persists a new self-update run record
func (s *BaseStore) CreateSelfUpdateRun(ctx context.Context, run *SelfUpdateRun) error {
	if run == nil {
		return fmt.Errorf("self-update run cannot be nil")
	}
	if run.Status == "" {
		run.Status = SelfUpdateStatusPending
	}
	if run.Channel == "" {
		run.Channel = "stable"
	}
	if run.RequestedAt.IsZero() {
		run.RequestedAt = time.Now().UTC()
	}

	metaJSON, err := encodeMetadata(run.Metadata)
	if err != nil {
		return err
	}

	id, err := s.insertReturningID(ctx, `
		INSERT INTO self_update_runs (
			status, requested_at, started_at, completed_at,
			current_version, target_version, channel, platform, arch,
			release_artifact_id, error_code, error_message, metadata_json, requested_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		string(run.Status), run.RequestedAt, nullTime(run.StartedAt), nullTime(run.CompletedAt),
		run.CurrentVersion, run.TargetVersion, run.Channel, run.Platform, run.Arch,
		nullInt64(run.ReleaseArtifactID), nullString(run.ErrorCode), nullString(run.ErrorMessage),
		metaJSON, nullString(run.RequestedBy))
	if err != nil {
		return err
	}
	run.ID = id
	return nil
}

// UpdateSelfUpdateRun updates an existing run record
func (s *BaseStore) UpdateSelfUpdateRun(ctx context.Context, run *SelfUpdateRun) error {
	if run == nil {
		return fmt.Errorf("self-update run cannot be nil")
	}
	if run.ID == 0 {
		return fmt.Errorf("self-update run id required")
	}
	if run.Channel == "" {
		run.Channel = "stable"
	}

	metaJSON, err := encodeMetadata(run.Metadata)
	if err != nil {
		return err
	}

	_, err = s.execContext(ctx, `
		UPDATE self_update_runs SET
			status = ?,
			started_at = ?,
			completed_at = ?,
			current_version = ?,
			target_version = ?,
			channel = ?,
			platform = ?,
			arch = ?,
			release_artifact_id = ?,
			error_code = ?,
			error_message = ?,
			metadata_json = ?,
			requested_by = ?
		WHERE id = ?
	`,
		string(run.Status), nullTime(run.StartedAt), nullTime(run.CompletedAt),
		run.CurrentVersion, run.TargetVersion, run.Channel, run.Platform, run.Arch,
		nullInt64(run.ReleaseArtifactID), nullString(run.ErrorCode), nullString(run.ErrorMessage),
		metaJSON, nullString(run.RequestedBy), run.ID)
	return err
}

// GetSelfUpdateRun returns a single self-update run by id
func (s *BaseStore) GetSelfUpdateRun(ctx context.Context, id int64) (*SelfUpdateRun, error) {
	if id == 0 {
		return nil, fmt.Errorf("self-update run id required")
	}
	row := s.queryRowContext(ctx, `
		SELECT id, status, requested_at, started_at, completed_at,
		       current_version, target_version, channel, platform, arch,
		       release_artifact_id, error_code, error_message, metadata_json, requested_by
		FROM self_update_runs
		WHERE id = ?
	`, id)
	return s.scanSelfUpdateRun(row)
}

// ListSelfUpdateRuns returns the most recent self-update runs
func (s *BaseStore) ListSelfUpdateRuns(ctx context.Context, limit int) ([]*SelfUpdateRun, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.queryContext(ctx, `
		SELECT id, status, requested_at, started_at, completed_at,
		       current_version, target_version, channel, platform, arch,
		       release_artifact_id, error_code, error_message, metadata_json, requested_by
		FROM self_update_runs
		ORDER BY requested_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*SelfUpdateRun
	for rows.Next() {
		run, serr := s.scanSelfUpdateRunRow(rows)
		if serr != nil {
			return nil, serr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *BaseStore) scanSelfUpdateRun(row *sql.Row) (*SelfUpdateRun, error) {
	var (
		id                int64
		status            string
		requestedAt       time.Time
		startedAt         sql.NullTime
		completedAt       sql.NullTime
		currentVersion    sql.NullString
		targetVersion     sql.NullString
		channel           sql.NullString
		platform          sql.NullString
		arch              sql.NullString
		releaseArtifactID sql.NullInt64
		errorCode         sql.NullString
		errorMessage      sql.NullString
		metadataJSON      sql.NullString
		requestedBy       sql.NullString
	)
	if err := row.Scan(&id, &status, &requestedAt, &startedAt, &completedAt,
		&currentVersion, &targetVersion, &channel, &platform, &arch,
		&releaseArtifactID, &errorCode, &errorMessage, &metadataJSON, &requestedBy); err != nil {
		return nil, err
	}
	run := &SelfUpdateRun{
		ID:             id,
		Status:         SelfUpdateStatus(status),
		RequestedAt:    requestedAt,
		CurrentVersion: currentVersion.String,
		TargetVersion:  targetVersion.String,
		Channel:        channel.String,
		Platform:       platform.String,
		Arch:           arch.String,
		Metadata:       decodeMetadata(metadataJSON),
		RequestedBy:    requestedBy.String,
	}
	if startedAt.Valid {
		run.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time
	}
	if releaseArtifactID.Valid {
		run.ReleaseArtifactID = releaseArtifactID.Int64
	}
	if errorCode.Valid {
		run.ErrorCode = errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = errorMessage.String
	}
	return run, nil
}

func (s *BaseStore) scanSelfUpdateRunRow(rows *sql.Rows) (*SelfUpdateRun, error) {
	var (
		id                int64
		status            string
		requestedAt       time.Time
		startedAt         sql.NullTime
		completedAt       sql.NullTime
		currentVersion    sql.NullString
		targetVersion     sql.NullString
		channel           sql.NullString
		platform          sql.NullString
		arch              sql.NullString
		releaseArtifactID sql.NullInt64
		errorCode         sql.NullString
		errorMessage      sql.NullString
		metadataJSON      sql.NullString
		requestedBy       sql.NullString
	)
	if err := rows.Scan(&id, &status, &requestedAt, &startedAt, &completedAt,
		&currentVersion, &targetVersion, &channel, &platform, &arch,
		&releaseArtifactID, &errorCode, &errorMessage, &metadataJSON, &requestedBy); err != nil {
		return nil, err
	}
	run := &SelfUpdateRun{
		ID:             id,
		Status:         SelfUpdateStatus(status),
		RequestedAt:    requestedAt,
		CurrentVersion: currentVersion.String,
		TargetVersion:  targetVersion.String,
		Channel:        channel.String,
		Platform:       platform.String,
		Arch:           arch.String,
		Metadata:       decodeMetadata(metadataJSON),
		RequestedBy:    requestedBy.String,
	}
	if startedAt.Valid {
		run.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time
	}
	if releaseArtifactID.Valid {
		run.ReleaseArtifactID = releaseArtifactID.Int64
	}
	if errorCode.Valid {
		run.ErrorCode = errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = errorMessage.String
	}
	return run, nil
}

// =============================
// Aggregated Metrics Methods
// =============================

type fleetMetricBucket struct {
	total int64
	mono  int64
	color int64
	scan  int64
}

type consumableBand int

const (
	consumableUnknown consumableBand = iota
	consumableHigh
	consumableMedium
	consumableLow
	consumableCritical
)

// GetAggregatedMetrics calculates fleet-wide aggregated metrics for dashboards
func (s *BaseStore) GetAggregatedMetrics(ctx context.Context, since time.Time, tenantIDs []string) (*AggregatedMetrics, error) {
	now := time.Now().UTC()
	agg := &AggregatedMetrics{
		GeneratedAt: now,
		RangeStart:  since.UTC(),
		RangeEnd:    now,
	}

	allowedTenants := make(map[string]struct{}, len(tenantIDs))
	for _, id := range tenantIDs {
		allowedTenants[id] = struct{}{}
	}

	agents, err := s.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	filteredAgents := make([]*Agent, 0, len(agents))
	agentTenantMap := make(map[string]string, len(agents))
	for _, a := range agents {
		if a == nil {
			continue
		}
		if len(tenantIDs) > 0 {
			if _, ok := allowedTenants[a.TenantID]; !ok {
				continue
			}
		}
		filteredAgents = append(filteredAgents, a)
		agentTenantMap[a.AgentID] = a.TenantID
	}
	agg.Fleet.Totals.Agents = len(filteredAgents)

	devices, err := s.ListAllDevices(ctx)
	if err != nil {
		return nil, err
	}

	filteredDevices := make([]*Device, 0, len(devices))
	serialMap := make(map[string]struct{}, len(devices))
	deviceBySerial := make(map[string]*Device, len(devices))
	for _, d := range devices {
		if d == nil {
			continue
		}
		if len(tenantIDs) > 0 {
			if _, ok := agentTenantMap[d.AgentID]; !ok {
				continue
			}
		}
		filteredDevices = append(filteredDevices, d)
		serialMap[d.Serial] = struct{}{}
		deviceBySerial[d.Serial] = d

		hasError, hasWarning, hasJam := classifyStatusMessages(d.StatusMessages)
		if hasJam {
			agg.Fleet.Statuses.Jam++
			agg.Fleet.Statuses.Error++
		} else if hasError {
			agg.Fleet.Statuses.Error++
		} else if hasWarning {
			agg.Fleet.Statuses.Warning++
		}
	}
	agg.Fleet.Totals.Devices = len(filteredDevices)

	if len(filteredDevices) == 0 {
		return agg, nil
	}

	buckets := make(map[time.Time]*fleetMetricBucket)
	lastValues := make(map[string]*MetricsSnapshot)

	// Pre-seed lastValues with the most recent metric BEFORE the time window for each device
	// This allows us to compute deltas for the first metric within the window
	seedQuery := `
		SELECT m.serial, m.timestamp, m.page_count, m.color_pages, m.mono_pages, m.scan_count
		FROM metrics_history m
		INNER JOIN (
			SELECT serial, MAX(timestamp) as max_ts
			FROM metrics_history
			WHERE timestamp < ?
			GROUP BY serial
		) latest ON m.serial = latest.serial AND m.timestamp = latest.max_ts
	`
	seedRows, err := s.queryContext(ctx, seedQuery, since.UTC())
	if err != nil {
		return nil, err
	}
	for seedRows.Next() {
		var (
			serial     string
			tsStr      string
			pc, cp, mp int64
			sc         int64
		)
		if err := seedRows.Scan(&serial, &tsStr, &pc, &cp, &mp, &sc); err != nil {
			seedRows.Close()
			return nil, err
		}
		if _, ok := serialMap[serial]; !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			continue
		}
		lastValues[serial] = &MetricsSnapshot{
			Serial:     serial,
			Timestamp:  ts,
			PageCount:  int(pc),
			ColorPages: int(cp),
			MonoPages:  int(mp),
			ScanCount:  int(sc),
		}
	}
	seedRows.Close()

	// Build dynamic query with placeholder conversion
	query := `
		SELECT m.timestamp, m.serial, m.agent_id, m.page_count, m.color_pages, m.mono_pages, m.scan_count, m.toner_levels
		FROM metrics_history m
		JOIN agents a ON m.agent_id = a.agent_id
		WHERE m.timestamp >= ?
	`
	args := []interface{}{since.UTC()}
	if len(tenantIDs) > 0 {
		query += ` AND a.tenant_id IN (` + s.buildPlaceholderListFrom(len(tenantIDs), 2) + `)`
		for _, id := range tenantIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY m.timestamp ASC`

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tsStr    string
			serial   string
			agentID  string
			pc, cp   int64
			mp, sc   int64
			tonerRaw sql.NullString
		)
		if err := rows.Scan(&tsStr, &serial, &agentID, &pc, &cp, &mp, &sc, &tonerRaw); err != nil {
			return nil, err
		}
		if _, ok := serialMap[serial]; !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			continue
		}
		bucketTime := ts.Truncate(time.Hour)
		if _, ok := buckets[bucketTime]; !ok {
			buckets[bucketTime] = &fleetMetricBucket{}
		}
		point := buckets[bucketTime]

		if last, ok := lastValues[serial]; ok {
			if pc >= int64(last.PageCount) {
				point.total += pc - int64(last.PageCount)
			}
			if cp >= int64(last.ColorPages) {
				point.color += cp - int64(last.ColorPages)
			}
			if mp >= int64(last.MonoPages) {
				point.mono += mp - int64(last.MonoPages)
			}
			if sc >= int64(last.ScanCount) {
				point.scan += sc - int64(last.ScanCount)
			}
		}

		var toner map[string]interface{}
		if tonerRaw.Valid && strings.TrimSpace(tonerRaw.String) != "" {
			var tmp map[string]interface{}
			if err := json.Unmarshal([]byte(tonerRaw.String), &tmp); err == nil {
				toner = tmp
			}
		}

		lastValues[serial] = &MetricsSnapshot{
			Serial:      serial,
			AgentID:     agentID,
			Timestamp:   ts,
			PageCount:   int(pc),
			ColorPages:  int(cp),
			MonoPages:   int(mp),
			ScanCount:   int(sc),
			TonerLevels: toner,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, d := range filteredDevices {
		if _, ok := lastValues[d.Serial]; ok {
			continue
		}
		snapshot, err := s.GetLatestMetrics(ctx, d.Serial)
		if err != nil || snapshot == nil {
			continue
		}
		lastValues[d.Serial] = snapshot
	}

	if len(buckets) > 0 {
		ordered := make([]time.Time, 0, len(buckets))
		for ts := range buckets {
			ordered = append(ordered, ts)
		}
		sort.Slice(ordered, func(i, j int) bool {
			return ordered[i].Before(ordered[j])
		})
		for _, ts := range ordered {
			bucket := buckets[ts]
			agg.Fleet.History.TotalImpressions = append(agg.Fleet.History.TotalImpressions, MetricSeriesPoint{Timestamp: ts, Value: bucket.total})
			agg.Fleet.History.MonoImpressions = append(agg.Fleet.History.MonoImpressions, MetricSeriesPoint{Timestamp: ts, Value: bucket.mono})
			agg.Fleet.History.ColorImpressions = append(agg.Fleet.History.ColorImpressions, MetricSeriesPoint{Timestamp: ts, Value: bucket.color})
			agg.Fleet.History.ScanVolume = append(agg.Fleet.History.ScanVolume, MetricSeriesPoint{Timestamp: ts, Value: bucket.scan})
		}
	}

	bandCounts := make(map[consumableBand]int)
	for _, d := range filteredDevices {
		snapshot := lastValues[d.Serial]
		if snapshot != nil {
			agg.Fleet.Totals.PageCount += int64(snapshot.PageCount)
			agg.Fleet.Totals.ColorPages += int64(snapshot.ColorPages)
			agg.Fleet.Totals.MonoPages += int64(snapshot.MonoPages)
			agg.Fleet.Totals.ScanCount += int64(snapshot.ScanCount)
		}
		band := classifyConsumableBand(snapshot, deviceBySerial[d.Serial])
		bandCounts[band]++
	}

	agg.Fleet.Consumables.Critical = bandCounts[consumableCritical]
	agg.Fleet.Consumables.Low = bandCounts[consumableLow]
	agg.Fleet.Consumables.Medium = bandCounts[consumableMedium]
	agg.Fleet.Consumables.High = bandCounts[consumableHigh]
	agg.Fleet.Consumables.Unknown = bandCounts[consumableUnknown]

	return agg, nil
}

// buildPlaceholderListFrom builds a comma-separated list of placeholders starting from startIdx
func (s *BaseStore) buildPlaceholderListFrom(count, startIdx int) string {
	if count <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s.dialect.Placeholder(startIdx + i))
	}
	return b.String()
}

// GetDatabaseStats returns high-level counts for observability panels
func (s *BaseStore) GetDatabaseStats(ctx context.Context) (*DatabaseStats, error) {
	query := `
		SELECT
			(SELECT COUNT(*) FROM agents),
			(SELECT COUNT(*) FROM devices),
			(SELECT COUNT(*) FROM metrics_history),
			(SELECT COUNT(*) FROM sessions),
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM audit_log),
			(SELECT COUNT(*) FROM release_artifacts),
			COALESCE((SELECT SUM(size_bytes) FROM release_artifacts), 0)
	`
	row := s.db.QueryRowContext(ctx, query)
	stats := &DatabaseStats{}
	if err := row.Scan(
		&stats.Agents,
		&stats.Devices,
		&stats.MetricsSnapshots,
		&stats.Sessions,
		&stats.Users,
		&stats.AuditEntries,
		&stats.ReleaseArtifacts,
		&stats.ReleaseBytes,
	); err != nil {
		return nil, err
	}

	// Get database size - PostgreSQL uses pg_database_size(), SQLite uses file size
	if s.dialect.Name() == "postgres" {
		var sizeBytes int64
		sizeQuery := `SELECT pg_database_size(current_database())`
		if err := s.db.QueryRowContext(ctx, sizeQuery).Scan(&sizeBytes); err == nil {
			stats.SizeBytes = sizeBytes
		}
	}
	// For SQLite, the caller (collector) handles file size via os.Stat

	return stats, nil
}

// Helper functions for aggregated metrics

func classifyStatusMessages(messages []string) (bool, bool, bool) {
	var hasError, hasWarning, hasJam bool
	for _, msg := range messages {
		lower := strings.ToLower(msg)
		switch {
		case strings.Contains(lower, "jam"):
			hasJam = true
			hasError = true
		case strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "offline"):
			hasError = true
		case strings.Contains(lower, "warn") || strings.Contains(lower, "low"):
			hasWarning = true
		}
	}
	return hasError, hasWarning, hasJam
}

func classifyConsumableBand(snapshot *MetricsSnapshot, device *Device) consumableBand {
	if snapshot != nil {
		if band := bandFromTonerLevels(snapshot.TonerLevels); band != consumableUnknown {
			return band
		}
	}
	if device != nil {
		if band := bandFromConsumableStrings(device.Consumables); band != consumableUnknown {
			return band
		}
	}
	return consumableUnknown
}

func bandFromTonerLevels(levels map[string]interface{}) consumableBand {
	if len(levels) == 0 {
		return consumableUnknown
	}
	worst := consumableUnknown
	for _, raw := range levels {
		if pct, ok := normalizePercentage(raw); ok {
			band := bandForPercentage(pct)
			if band > worst {
				worst = band
			}
		}
	}
	return worst
}

func bandFromConsumableStrings(entries []string) consumableBand {
	worst := consumableUnknown
	for _, entry := range entries {
		lower := strings.ToLower(entry)
		switch {
		case strings.Contains(lower, "empty") || strings.Contains(lower, "replace") || strings.Contains(lower, "exhausted") || strings.Contains(lower, "depleted"):
			if consumableCritical > worst {
				worst = consumableCritical
			}
		case strings.Contains(lower, "very low") || strings.Contains(lower, "near empty"):
			if consumableCritical > worst {
				worst = consumableCritical
			}
		case strings.Contains(lower, "low"):
			if consumableLow > worst {
				worst = consumableLow
			}
		case strings.Contains(lower, "medium") || strings.Contains(lower, "half"):
			if consumableMedium > worst {
				worst = consumableMedium
			}
		case strings.Contains(lower, "high") || strings.Contains(lower, "full") || strings.Contains(lower, "ok") || strings.Contains(lower, "ready"):
			if consumableHigh > worst {
				worst = consumableHigh
			}
		}
		if pct, ok := percentFromString(entry); ok {
			band := bandForPercentage(pct)
			if band > worst {
				worst = band
			}
		}
	}
	return worst
}

func normalizePercentage(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	case string:
		return percentFromString(v)
	}
	return 0, false
}

func percentFromString(s string) (float64, bool) {
	var buf strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			buf.WriteRune(r)
			continue
		}
		if buf.Len() > 0 {
			break
		}
	}
	if buf.Len() == 0 {
		return 0, false
	}
	val, err := strconv.ParseFloat(buf.String(), 64)
	if err != nil {
		return 0, false
	}
	return val, true
}

func bandForPercentage(pct float64) consumableBand {
	switch {
	case pct <= 5:
		return consumableCritical
	case pct <= 15:
		return consumableLow
	case pct <= 60:
		return consumableMedium
	default:
		return consumableHigh
	}
}
