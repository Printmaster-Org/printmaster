package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	agentpkg "printmaster/agent/agent"
	"printmaster/agent/storage"
)

func wakeTestWorker(interval time.Duration) *UploadWorker {
	return NewUploadWorker(nil, nil, stubLogger{}, nil, UploadWorkerConfig{UploadInterval: interval}, "")
}

func waitUpload(t *testing.T, uploads <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-uploads:
		return at
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not run")
		return time.Time{}
	}
}

func TestUploadWorkerWakeNonblockingCoalescedNeverClosed(t *testing.T) {
	t.Parallel()
	w := wakeTestWorker(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				w.Wake()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Stop() // shutdown races producers, never closes their wake channel
	}()
	wg.Wait()
	if len(w.wakeCh) != 1 || cap(w.wakeCh) != 1 {
		t.Fatal("wake queue not bounded/coalesced")
	}
	w.Stop()
	w.Stop()
	w.Wake()
	<-w.wakeCh
	w.Wake()
	if _, ok := <-w.wakeCh; !ok {
		t.Fatal("wake channel closed")
	}
	var nilWorker *UploadWorker
	nilWorker.Wake()
}

func TestUploadWorkerWakeBatchesAndRetainsSchedule(t *testing.T) {
	t.Parallel()
	w := wakeTestWorker(1700 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploads := make(chan time.Time, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runUploadLoop(ctx, func() { uploads <- time.Now() })
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitUpload(t, uploads) // initial upload
	firstWake := time.Now()
	for i := 0; i < 1000; i++ {
		w.Wake()
	}
	select {
	case <-uploads:
		t.Fatal("wake bypassed batching debounce")
	case <-time.After(200 * time.Millisecond):
	}
	batched := waitUpload(t, uploads)
	if batched.Sub(firstWake) < 900*time.Millisecond {
		t.Fatalf("debounce too short: %v", batched.Sub(firstWake))
	}
	// Next upload must remain on the original periodic cadence, not restart
	// the schedule from the wake-triggered upload.
	scheduled := waitUpload(t, uploads)
	if scheduled.Sub(batched) > 1200*time.Millisecond {
		t.Fatalf("schedule reset by wake: %v", scheduled.Sub(batched))
	}
}

func TestUploadWorkerWakeStopAndContextCancel(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "context", true: "stop"}[stop], func(t *testing.T) {
			t.Parallel()
			w := wakeTestWorker(time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			uploads := make(chan time.Time, 10)
			w.wg.Add(1)
			done := make(chan struct{})
			go func() {
				defer w.wg.Done()
				defer close(done)
				w.runUploadLoop(ctx, func() { uploads <- time.Now() })
			}()
			waitUpload(t, uploads)
			w.Wake()
			if stop {
				w.Stop()
			} else {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("shutdown waited for debounce")
			}
			w.Wake() // producers remain safe after loop exits
			select {
			case <-uploads:
				t.Fatal("uploaded pending wake after shutdown")
			default:
			}
		})
	}
}

func TestUploadWorkerPendingWakeSatisfiedBySchedule(t *testing.T) {
	t.Parallel()
	w := wakeTestWorker(600 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploads := make(chan time.Time, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runUploadLoop(ctx, func() { uploads <- time.Now() })
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitUpload(t, uploads)
	w.Wake()
	waitUpload(t, uploads) // scheduled upload satisfies the queued wake
	select {
	case <-uploads:
		t.Fatal("redundant wake upload after scheduled upload")
	case <-time.After(450 * time.Millisecond):
	}
}

func TestUploadWorkerSustainedWakeDoesNotStarveUpload(t *testing.T) {
	t.Parallel()
	w := wakeTestWorker(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uploads := make(chan time.Time, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runUploadLoop(ctx, func() { uploads <- time.Now() })
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitUpload(t, uploads)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.Wake()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-producerDone })
	waitUpload(t, uploads) // bounded even while producer continues waking
}

type wakeDeviceStore struct{ storage.DeviceStore }

func (wakeDeviceStore) List(ctx context.Context, _ storage.DeviceFilter) ([]*storage.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &storage.Device{}
	d.Serial, d.IP, d.Visible = "WAKE-UPLOAD", "192.0.2.10", true
	return []*storage.Device{d}, nil
}

func (wakeDeviceStore) GetLatestMetrics(context.Context, string) (*storage.MetricsSnapshot, error) {
	return nil, storage.ErrNotFound
}

func TestUploadWorkerStopCancelsInFlightUpload(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "context", true: "stop"}[stop], func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				_, _ = io.Copy(io.Discard, req.Body)
				entered <- struct{}{}
				select {
				case <-req.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			w := NewUploadWorker(agentpkg.NewServerClient(server.URL, "agent", "token"), wakeDeviceStore{}, stubLogger{}, nil,
				UploadWorkerConfig{RetryAttempts: 1}, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.runCtx, w.cancel = ctx, cancel
			done := make(chan error, 1)
			w.wg.Add(1)
			go func() {
				defer w.wg.Done()
				done <- w.uploadDevices()
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("upload request not received")
			}
			if stop {
				w.Stop()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("request not canceled: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown left upload request running")
			}
		})
	}
}
