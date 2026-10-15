package agent

import (
	"context"
	"fmt"
	"net/http"

	"printmaster/common/metadata"
)

type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("server returned status %d: %s", e.Status, e.Body)
}

func (c *ServerClient) DeviceMetadata(ctx context.Context) ([]*metadata.Record, error) {
	var records []*metadata.Record
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/agents/device-metadata", nil, &records, true)
	if err == nil && records == nil {
		return nil, fmt.Errorf("server metadata snapshot must be an array")
	}
	return records, err
}

func (c *ServerClient) EditDeviceMetadata(ctx context.Context, serial string, patch map[string]interface{}) (*metadata.Record, error) {
	body := make(map[string]interface{}, len(patch)+1)
	for field, value := range patch {
		body[field] = value
	}
	body["serial"] = serial
	var record metadata.Record
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/agents/device-metadata", body, &record, true); err != nil {
		return nil, err
	}
	if record.Serial != serial {
		return nil, fmt.Errorf("server metadata response does not match requested device")
	}
	return &record, nil
}
