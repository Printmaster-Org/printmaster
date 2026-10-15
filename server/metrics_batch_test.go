package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"printmaster/common/protocol"
	"printmaster/server/storage"
)

type metricsFailureStore struct {
	storage.Store
}

func (s metricsFailureStore) SaveMetrics(ctx context.Context, metric *storage.MetricsSnapshot) error {
	if metric.Serial == "storage-failure" {
		return errors.New("private database failure details")
	}
	return s.Store.SaveMetrics(ctx, metric)
}

func TestMetricsBatchReceipts(t *testing.T) {
	f := newBoundaryFixture(t)
	serverStore = metricsFailureStore{Store: f.store}
	for _, tc := range []struct {
		name     string
		metrics  string
		received int
		stored   int
		failures []protocol.MetricsBatchFailure
	}{
		{"complete", `[{"serial":"boundary-device-a","agent_id":"untrusted","timestamp":"2026-10-15T11:59:00Z","page_count":7}]`, 1, 1, nil},
		{"empty", `[]`, 0, 0, nil},
		{"mixed", `[null,{},{"serial":"boundary-device-b"},{"serial":"missing"},{"serial":"storage-failure"},{"serial":"boundary-device-a","page_count":8}]`, 6, 1,
			[]protocol.MetricsBatchFailure{
				{Index: 0, Code: protocol.MetricsInvalidRecord},
				{Index: 1, Code: protocol.MetricsInvalidRecord},
				{Index: 2, Serial: "boundary-device-b", Code: protocol.MetricsOwnershipConflict},
				{Index: 3, Serial: "missing", Code: protocol.MetricsOwnershipConflict},
				{Index: 4, Serial: "storage-failure", Code: protocol.MetricsStorageError},
			}},
		{"all rejected", `[{"serial":"boundary-device-b"}]`, 1, 0,
			[]protocol.MetricsBatchFailure{{Index: 0, Serial: "boundary-device-b", Code: protocol.MetricsOwnershipConflict}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"agent_id":"boundary-agent-a","timestamp":"2026-10-15T12:00:00Z","metrics":` + tc.metrics + `}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/batch", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer boundary-machine-a")
			w := httptest.NewRecorder()
			requireAuth(handleMetricsBatch)(w, req)
			var result protocol.MetricsBatchResult
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if result.Received != tc.received || result.Stored != tc.stored || result.Success != (tc.stored == tc.received) ||
				!reflect.DeepEqual(result.Failures, tc.failures) {
				t.Fatalf("receipt=%+v, want received=%d stored=%d failures=%+v", result, tc.received, tc.stored, tc.failures)
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatalf("storage internals exposed: %s", w.Body.String())
			}
		})
	}
	metric, err := f.store.GetLatestMetrics(context.Background(), "boundary-device-a")
	if err != nil || metric == nil || metric.AgentID != "boundary-agent-a" || metric.PageCount != 8 {
		t.Fatalf("valid snapshot/source not persisted: %+v, %v", metric, err)
	}
	metric, err = f.store.GetLatestMetrics(context.Background(), "boundary-device-b")
	if err != nil || metric != nil {
		t.Fatalf("foreign snapshot persisted: %+v, %v", metric, err)
	}
}
