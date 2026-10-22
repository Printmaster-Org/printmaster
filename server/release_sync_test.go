package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"printmaster/server/releases"
)

func preserveReleaseSync(t *testing.T) {
	t.Helper()
	worker, state, active := intakeWorker, releaseSyncState, releaseSyncInProgress
	releaseSyncState, releaseSyncInProgress = nil, false
	t.Cleanup(func() {
		intakeWorker, releaseSyncState, releaseSyncInProgress = worker, state, active
	})
}

func TestReleaseSyncStatusAuthorization(t *testing.T) {
	f := newBoundaryFixture(t)
	preserveReleaseSync(t)
	for _, tc := range []struct {
		user string
		code int
	}{
		{"", http.StatusUnauthorized}, {"viewer", http.StatusForbidden},
		{"operator", http.StatusForbidden}, {"admin", http.StatusOK},
	} {
		w := f.request(t, handleReleasesSync, tc.user, http.MethodGet, "/api/v1/releases/sync", "")
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.user, w.Code, w.Body.String())
		}
		if tc.code == http.StatusOK && w.Body.String() != "{\"in_progress\":false,\"progress\":null}\n" {
			t.Fatalf("missing history must not imply success: %s", w.Body.String())
		}
	}
}

func TestReleaseSyncReplayAndConflict(t *testing.T) {
	f := newBoundaryFixture(t)
	preserveReleaseSync(t)
	intakeWorker = &releases.IntakeWorker{}
	now := time.Now().UTC()
	releaseSyncState = &releaseSyncSnapshot{
		RunID: "one", RunStartedAt: now, UpdatedAt: now, Deadline: now.Add(time.Hour), Sequence: 1,
		SyncProgress: releases.SyncProgress{Phase: "fetching", Message: "Fetching releases"},
	}
	releaseSyncInProgress = true
	conflict := f.request(t, handleReleasesSync, "admin", http.MethodPost, "/api/v1/releases/sync", "")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("duplicate sync: %d %s", conflict.Code, conflict.Body.String())
	}
	recordReleaseSyncProgress("other", releases.SyncProgress{Phase: "complete"})
	recordReleaseSyncProgress("one", releases.SyncProgress{Phase: "downloading", PercentComplete: 45})
	recordReleaseSyncProgress("one", releases.SyncProgress{Phase: "error", Error: "metadata timeout; retry to repair cache"})
	recordReleaseSyncProgress("one", releases.SyncProgress{Phase: "complete"})
	w := f.request(t, handleReleasesSync, "admin", http.MethodGet, "/api/v1/releases/sync", "")
	var result struct {
		Progress releaseSyncSnapshot `json:"progress"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if result.Progress.RunID != "one" || result.Progress.Sequence != 3 || result.Progress.Phase != "error" ||
		result.Progress.Error != "metadata timeout; retry to repair cache" {
		t.Fatalf("terminal progress regressed: %+v", result.Progress)
	}
}

func TestManualReleaseSyncReportsActualOutcome(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newBoundaryFixture(t)
			preserveReleaseSync(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("[]"))
			}))
			defer upstream.Close()
			var err error
			intakeWorker, err = releases.NewIntakeWorker(f.store, serverLogger, releases.Options{
				CacheDir: t.TempDir(), BaseAPIURL: upstream.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			w := f.request(t, handleReleasesSync, "admin", http.MethodPost, "/api/v1/releases/sync", "")
			if w.Code != http.StatusOK {
				t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				releaseSyncMu.Lock()
				state, active := copyReleaseSyncStateLocked(), releaseSyncInProgress
				releaseSyncMu.Unlock()
				if !active {
					want := "complete"
					if status != http.StatusOK {
						want = "error"
					}
					if state == nil || state.Phase != want || state.Sequence < 2 ||
						state.Deadline.Sub(state.RunStartedAt) != time.Hour {
						t.Fatalf("actual result: %+v", state)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("manual sync did not finish")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
