package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// InventoryScope distinguishes explicit administrator access from empty scope.
// Tenant ownership comes from agents, never stale device/metric tenant hints.
type InventoryScope struct {
	Unrestricted bool
	TenantIDs    []string
}

// InventoryStore is optional; HTTP reads fail closed without it.
type InventoryStore interface {
	InventoryIndex(context.Context, InventoryScope) ([]*DeviceIndex, error)
	InventoryRows(context.Context, InventoryScope, []string, int, int) ([]*DeviceWithMetrics, error)
	InventoryCount(context.Context, InventoryScope) (int64, error)
	InventoryMetrics(context.Context, InventoryScope, []string) ([]*MetricsSnapshot, error)
}

// DeviceIndex deliberately excludes raw SNMP and supply payloads.
type DeviceIndex struct {
	Serial         string    `json:"serial"`
	AgentID        string    `json:"agent_id"`
	IP             string    `json:"ip"`
	Manufacturer   string    `json:"manufacturer"`
	Model          string    `json:"model"`
	Hostname       string    `json:"hostname"`
	Location       string    `json:"location"`
	AssetNumber    string    `json:"asset_number"`
	LastSeen       time.Time `json:"last_seen"`
	StatusMessages []string  `json:"status_messages"`
	DeviceType     string    `json:"device_type"`
	SourceType     string    `json:"source_type"`
	IsUSB          bool      `json:"is_usb"`
	SpoolerStatus  string    `json:"spooler_status"`
	PageCount      int       `json:"page_count"`
}

func inventoryWhere(scope InventoryScope, serials []string) (string, []interface{}) {
	parts := []string{"1=1"}
	args := []interface{}{}
	if !scope.Unrestricted {
		if len(scope.TenantIDs) == 0 {
			parts = append(parts, "1=0")
		} else {
			parts = append(parts, "a.tenant_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(scope.TenantIDs)), ",")+")")
			for _, id := range scope.TenantIDs {
				args = append(args, id)
			}
		}
	}
	if serials != nil {
		if len(serials) == 0 {
			parts = append(parts, "1=0")
		} else {
			parts = append(parts, "d.serial IN ("+strings.TrimSuffix(strings.Repeat("?,", len(serials)), ",")+")")
			for _, id := range serials {
				args = append(args, id)
			}
		}
	}
	return strings.Join(parts, " AND "), args
}

// Timescale can remove id. Persisted metric values deterministically break ties.
const inventoryMetricOrder = "timestamp DESC, page_count DESC, color_pages DESC, mono_pages DESC, scan_count DESC, toner_levels DESC"

func (s *BaseStore) inventoryMetricJoin() string {
	if s.dialect.Name() == "postgres" {
		return " LEFT JOIN LATERAL (SELECT serial, agent_id, timestamp, page_count, color_pages, mono_pages, scan_count, toner_levels FROM metrics_history WHERE serial=d.serial AND agent_id=d.agent_id ORDER BY " + inventoryMetricOrder + " LIMIT 1) m ON true "
	}
	return " LEFT JOIN metrics_history m ON m.rowid=(SELECT rowid FROM metrics_history WHERE serial=d.serial AND agent_id=d.agent_id ORDER BY " + inventoryMetricOrder + " LIMIT 1) "
}

const inventoryPageCount = "COALESCE((SELECT page_count FROM metrics_history WHERE serial=d.serial AND agent_id=d.agent_id ORDER BY timestamp DESC,page_count DESC LIMIT 1),0)"

func (s *BaseStore) InventoryIndex(ctx context.Context, scope InventoryScope) ([]*DeviceIndex, error) {
	where, args := inventoryWhere(scope, nil)
	q := `SELECT d.serial,d.agent_id,d.ip,COALESCE(d.manufacturer,''),COALESCE(d.model,''),COALESCE(d.hostname,''),COALESCE(d.location,''),COALESCE(d.asset_number,''),d.last_seen,COALESCE(d.status_messages,'[]'),COALESCE(d.device_type,''),COALESCE(d.source_type,''),COALESCE(d.is_usb,false),COALESCE(d.spooler_status,''),` + inventoryPageCount + ` FROM devices d LEFT JOIN agents a ON a.agent_id=d.agent_id WHERE ` + where + ` ORDER BY d.last_seen DESC,d.serial ASC`
	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*DeviceIndex, 0)
	for rows.Next() {
		d := &DeviceIndex{StatusMessages: []string{}}
		var status string
		if err := rows.Scan(&d.Serial, &d.AgentID, &d.IP, &d.Manufacturer, &d.Model, &d.Hostname, &d.Location, &d.AssetNumber, &d.LastSeen, &status, &d.DeviceType, &d.SourceType, &d.IsUSB, &d.SpoolerStatus, &d.PageCount); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(status), &d.StatusMessages)
		if d.StatusMessages == nil {
			d.StatusMessages = []string{}
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

const inventoryDeviceColumns = `d.serial,d.agent_id,d.ip,COALESCE(d.manufacturer,''),COALESCE(d.model,''),COALESCE(d.hostname,''),COALESCE(d.firmware,''),COALESCE(d.mac_address,''),COALESCE(d.subnet_mask,''),COALESCE(d.gateway,''),COALESCE(d.consumables,'null'),COALESCE(d.status_messages,'null'),d.last_seen,d.first_seen,d.created_at,COALESCE(d.discovery_method,''),COALESCE(d.asset_number,''),COALESCE(d.location,''),COALESCE(d.description,''),COALESCE(d.web_ui_url,''),COALESCE(d.raw_data,'null'),COALESCE(d.device_type,''),COALESCE(d.source_type,''),COALESCE(d.is_usb,false),COALESCE(d.port_name,''),COALESCE(d.driver_name,''),COALESCE(d.is_default,false),COALESCE(d.is_shared,false),COALESCE(d.spooler_status,''),COALESCE(d.usb_webui_available,false)`

func (s *BaseStore) InventoryRows(ctx context.Context, scope InventoryScope, serials []string, limit, offset int) ([]*DeviceWithMetrics, error) {
	where, args := inventoryWhere(scope, serials)
	q := `SELECT ` + inventoryDeviceColumns + `,` + inventoryPageCount + ` FROM devices d LEFT JOIN agents a ON a.agent_id=d.agent_id WHERE ` + where + ` ORDER BY d.last_seen DESC,d.serial ASC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*DeviceWithMetrics, 0)
	for rows.Next() {
		d := &DeviceWithMetrics{}
		var consumables, status, raw string
		err := rows.Scan(&d.Serial, &d.AgentID, &d.IP, &d.Manufacturer, &d.Model, &d.Hostname, &d.Firmware, &d.MACAddress, &d.SubnetMask, &d.Gateway, &consumables, &status, &d.LastSeen, &d.FirstSeen, &d.CreatedAt, &d.DiscoveryMethod, &d.AssetNumber, &d.Location, &d.Description, &d.WebUIURL, &raw, &d.DeviceType, &d.SourceType, &d.IsUSB, &d.PortName, &d.DriverName, &d.IsDefault, &d.IsShared, &d.SpoolerStatus, &d.UsbWebUIAvailable, &d.PageCount)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(consumables), &d.Consumables)
		_ = json.Unmarshal([]byte(status), &d.StatusMessages)
		_ = json.Unmarshal([]byte(raw), &d.RawData)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *BaseStore) InventoryCount(ctx context.Context, scope InventoryScope) (int64, error) {
	where, args := inventoryWhere(scope, nil)
	var count int64
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM devices d LEFT JOIN agents a ON a.agent_id=d.agent_id WHERE `+where, args...).Scan(&count)
	return count, err
}

func (s *BaseStore) InventoryMetrics(ctx context.Context, scope InventoryScope, serials []string) ([]*MetricsSnapshot, error) {
	where, args := inventoryWhere(scope, serials)
	q := `SELECT m.serial,m.agent_id,m.timestamp,m.page_count,m.color_pages,m.mono_pages,m.scan_count,m.toner_levels FROM devices d LEFT JOIN agents a ON a.agent_id=d.agent_id ` + s.inventoryMetricJoin() + ` WHERE ` + where + ` AND m.serial IS NOT NULL ORDER BY d.last_seen DESC,d.serial ASC`
	rows, err := s.queryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*MetricsSnapshot, 0)
	for rows.Next() {
		m := &MetricsSnapshot{}
		var toner sql.NullString
		if err := rows.Scan(&m.Serial, &m.AgentID, &m.Timestamp, &m.PageCount, &m.ColorPages, &m.MonoPages, &m.ScanCount, &toner); err != nil {
			return nil, err
		}
		if toner.Valid {
			_ = json.Unmarshal([]byte(toner.String), &m.TonerLevels)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
