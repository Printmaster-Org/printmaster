package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLivenessPool_ClosesWhenJobsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs := make(chan ScanJob)
	cfg := ScannerConfig{
		LivenessWorkers: 2,
		LivenessTimeout: 10 * time.Millisecond,
		LivenessPorts:   []int{1, 2},
		// Inject a fake probe to avoid network in tests
		ProbeFunc: func(ip string, ports []int, timeout time.Duration) ([]int, error) {
			return []int{}, nil
		},
	}
	out := StartLivenessPool(ctx, cfg, jobs)
	// close jobs immediately; workers should exit and out should close
	close(jobs)
	// drain out until closed
	for range out {
	}
}

func TestDetectionPool_ProcessesJobsWithDetectFunc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in := make(chan LivenessResult)
	// simple detect func that treats any job with an open port as a printer
	var detectCalled atomic.Int32
	cfg := ScannerConfig{
		DetectionWorkers: 2,
		DetectFunc: func(ctx context.Context, job ScanJob, openPorts []int) (interface{}, bool, error) {
			detectCalled.Add(1)
			if len(openPorts) > 0 {
				return map[string]interface{}{"ip": job.IP}, true, nil
			}
			return nil, false, nil
		},
	}

	out := StartDetectionPool(ctx, cfg, in)

	// enqueue two liveness results
	go func() {
		in <- LivenessResult{Job: ScanJob{IP: "10.0.0.1"}, Alive: true, OpenPorts: []int{9100}}
		in <- LivenessResult{Job: ScanJob{IP: "10.0.0.2"}, Alive: true, OpenPorts: []int{80}}
		close(in)
	}()

	count := 0
	for dr := range out {
		if dr.IsPrinter {
			if _, ok := dr.Info.(map[string]interface{}); !ok {
				t.Fatalf("expected map info for printer, got %#v", dr.Info)
			}
		}
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 results, got %d", count)
	}
	if detectCalled.Load() != 2 {
		t.Fatalf("detect func calls = %d, want 2", detectCalled.Load())
	}
}
