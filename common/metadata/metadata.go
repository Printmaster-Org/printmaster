package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"printmaster/common/storage"
)

var ErrPinned = errors.New("metadata field is controlled by the Server")
var ErrLocked = errors.New("metadata field is locked locally")

var Fields = []string{"manufacturer", "model", "hostname", "firmware", "ip", "mac_address", "subnet_mask", "gateway", "dns_servers", "dhcp_server", "asset_number", "location", "description", "web_ui_url"}

type Record struct {
	Serial    string                 `json:"serial"`
	Owner     string                 `json:"-"`
	Version   int64                  `json:"version"`
	Overrides map[string]interface{} `json:"overrides"`
	Reported  map[string]interface{} `json:"-"`
}

func Parse(data []byte) (string, map[string]interface{}, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return "", nil, err
	}
	var serial string
	if err := json.Unmarshal(body["serial"], &serial); err != nil || serial == "" {
		return "", nil, errors.New("serial required")
	}
	delete(body, "serial")
	patch := make(map[string]interface{}, len(body))
	for field, raw := range body {
		if !Known(field) {
			return "", nil, fmt.Errorf("unsupported metadata field: %s", field)
		}
		if field == "dns_servers" {
			var value []string
			if string(raw) == "null" {
				return "", nil, errors.New("dns_servers must be an array")
			}
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", nil, errors.New("dns_servers must be an array of strings")
			}
			for _, ip := range value {
				if net.ParseIP(ip) == nil {
					return "", nil, errors.New("invalid DNS address")
				}
			}
			patch[field] = value
		} else {
			var value string
			if string(raw) == "null" {
				return "", nil, fmt.Errorf("%s must be a string", field)
			}
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", nil, fmt.Errorf("%s must be a string", field)
			}
			if field == "ip" && net.ParseIP(value) == nil {
				return "", nil, errors.New("invalid IP address")
			}
			patch[field] = value
		}
	}
	if len(patch) == 0 {
		return "", nil, errors.New("no metadata fields provided")
	}
	return serial, patch, nil
}

func Known(field string) bool {
	for _, name := range Fields {
		if field == name {
			return true
		}
	}
	return false
}

func Values(d *storage.Device) map[string]interface{} {
	return map[string]interface{}{
		"manufacturer": d.Manufacturer, "model": d.Model, "hostname": d.Hostname, "firmware": d.Firmware,
		"ip": d.IP, "mac_address": d.MACAddress, "subnet_mask": d.SubnetMask, "gateway": d.Gateway,
		"dns_servers": d.DNSServers, "dhcp_server": d.DHCPServer, "asset_number": d.AssetNumber,
		"location": d.Location, "description": d.Description, "web_ui_url": d.WebUIURL,
	}
}

func NetworkFromRaw(d *storage.Device) error {
	patch := map[string]interface{}{}
	if value, ok := d.RawData["dns_servers"]; ok && d.DNSServers == nil {
		patch["dns_servers"] = value
	}
	if value, ok := d.RawData["dhcp_server"]; ok && d.DHCPServer == "" && value != nil {
		patch["dhcp_server"] = value
	}
	return Apply(d, patch)
}

func Apply(d *storage.Device, patch map[string]interface{}) error {
	for field, value := range patch {
		if !Known(field) {
			return fmt.Errorf("unsupported stored metadata field: %s", field)
		}
		if field == "dns_servers" {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &d.DNSServers); err != nil {
				return err
			}
			continue
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("invalid stored metadata type: %s", field)
		}
		switch field {
		case "manufacturer":
			d.Manufacturer = text
		case "model":
			d.Model = text
		case "hostname":
			d.Hostname = text
		case "firmware":
			d.Firmware = text
		case "ip":
			d.IP = text
		case "mac_address":
			d.MACAddress = text
		case "subnet_mask":
			d.SubnetMask = text
		case "gateway":
			d.Gateway = text
		case "dhcp_server":
			d.DHCPServer = text
		case "asset_number":
			d.AssetNumber = text
		case "location":
			d.Location = text
		case "description":
			d.Description = text
		case "web_ui_url":
			d.WebUIURL = text
		}
	}
	return nil
}

func Locked(fields []storage.FieldLock, patch map[string]interface{}) error {
	for _, lock := range fields {
		if _, ok := patch[strings.ToLower(lock.Field)]; ok {
			return fmt.Errorf("%w: %s", ErrLocked, lock.Field)
		}
	}
	return nil
}
