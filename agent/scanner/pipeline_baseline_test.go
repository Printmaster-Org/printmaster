package scanner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestBaselineLivenessPool(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{0, -1, 1, 3} {
		t.Run(fmtBaselineWorkers(workers), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			probeErr := errors.New("fake probe failure")
			meta := &struct{ Hostname string }{"printer"}
			jobs := make(chan ScanJob, 3)
			for _, ip := range []string{"open", "closed", "error"} {
				jobs <- ScanJob{IP: ip, Source: "baseline", Meta: meta}
			}
			close(jobs)
			var calls atomic.Int32
			cfg := ScannerConfig{LivenessWorkers: workers, LivenessTimeout: -1, ProbeFunc: func(ip string, ports []int, timeout time.Duration) ([]int, error) {
				calls.Add(1)
				if !reflect.DeepEqual(ports, []int{80, 443, 9100}) || timeout != 500*time.Millisecond {
					t.Errorf("default probe args = %v, %v", ports, timeout)
				}
				switch ip {
				case "open":
					return []int{9100}, nil
				case "error":
					return []int{80}, probeErr
				default:
					return nil, nil
				}
			}}
			results := map[string]LivenessResult{}
			for result := range StartLivenessPool(ctx, cfg, jobs) {
				if result.Job.Source != "baseline" || result.Job.Meta != meta {
					t.Fatalf("job metadata lost: %#v", result.Job)
				}
				results[result.Job.IP] = result
			}
			if ctx.Err() != nil || calls.Load() != 3 || len(results) != 3 {
				t.Fatalf("drain = %v, calls=%d, results=%#v", ctx.Err(), calls.Load(), results)
			}
			if !results["open"].Alive || !reflect.DeepEqual(results["open"].OpenPorts, []int{9100}) || results["closed"].Alive || results["closed"].Err != nil || results["error"].Alive || results["error"].Err != probeErr || len(results["error"].OpenPorts) != 0 {
				t.Fatalf("liveness outcomes = %#v", results)
			}
		})
	}
}

func fmtBaselineWorkers(n int) string {
	if n < 0 {
		return "negative"
	}
	if n == 0 {
		return "default"
	}
	if n == 1 {
		return "single"
	}
	return "multiple"
}

func TestBaselineDetectionPool(t *testing.T) {
	t.Parallel()
	for _, useDetect := range []bool{false, true} {
		name := "nil-callback"
		if useDetect {
			name = "callback"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			upstreamErr := errors.New("liveness error")
			detectErr := errors.New("detection error")
			meta := &struct{ Hostname string }{"printer"}
			job := ScanJob{IP: "192.0.2.1", Source: "llmnr", Meta: meta}
			in := make(chan LivenessResult, 1)
			in <- LivenessResult{Job: job, Alive: false, OpenPorts: []int{631}, Err: upstreamErr}
			close(in)
			cfg := ScannerConfig{DetectionWorkers: -1}
			var calls atomic.Int32
			if useDetect {
				cfg.DetectFunc = func(_ context.Context, got ScanJob, ports []int) (interface{}, bool, error) {
					calls.Add(1)
					if !reflect.DeepEqual(got, job) || !reflect.DeepEqual(ports, []int{631}) {
						t.Errorf("callback args = %#v, %v", got, ports)
					}
					return meta, true, detectErr
				}
			}
			var results []DetectionResult
			for dr := range StartDetectionPool(ctx, cfg, in) {
				results = append(results, dr)
			}
			if ctx.Err() != nil || len(results) != 1 || !reflect.DeepEqual(results[0].Job, job) {
				t.Fatalf("results = %#v, ctx=%v", results, ctx.Err())
			}
			got := results[0]
			// Known integration gap: pool does not gate on Alive or propagate
			// liveness Err. It always delegates when callback exists; do not
			// mistake this characterization for validated reachability policy.
			if useDetect {
				if calls.Load() != 1 || !got.IsPrinter || got.Info != meta || got.Err != detectErr {
					t.Fatalf("callback result = %#v, calls=%d", got, calls.Load())
				}
			} else if got.IsPrinter || got.Info != nil || got.Err != nil {
				t.Fatalf("nil callback result = %#v", got)
			}
		})
	}
}

func TestBaselineDeepScanPool(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deepErr := errors.New("fake deep failure")
	in := make(chan DetectionResult, 4)
	for _, ip := range []string{"not-printer", "nil", "error", "ok"} {
		in <- DetectionResult{Job: ScanJob{IP: ip, Source: "baseline", Meta: "metadata"}, IsPrinter: ip != "not-printer", Info: "detection"}
	}
	close(in)
	var calls atomic.Int32
	cfg := ScannerConfig{DeepScanWorkers: 0, DeepScanFunc: func(_ context.Context, dr DetectionResult) (interface{}, error) {
		calls.Add(1)
		if dr.Job.Source != "baseline" || dr.Job.Meta != "metadata" || dr.Info != "detection" {
			t.Errorf("deep input = %#v", dr)
		}
		switch dr.Job.IP {
		case "nil":
			return nil, nil
		case "error":
			return "discarded-result", deepErr
		default:
			return "success", nil
		}
	}}
	results := map[interface{}]bool{}
	for result := range StartDeepScanPool(ctx, cfg, in) {
		results[result] = true
	}
	if ctx.Err() != nil || calls.Load() != 3 || len(results) != 2 || !results[deepErr] || !results["success"] {
		t.Fatalf("deep results = %#v, calls=%d, ctx=%v", results, calls.Load(), ctx.Err())
	}
	// Errors themselves are output items; nil successful results disappear.
	// A nil callback performs no work, even on a confirmed printer.
	nilIn := make(chan DetectionResult, 1)
	nilIn <- DetectionResult{IsPrinter: true}
	close(nilIn)
	for result := range StartDeepScanPool(ctx, ScannerConfig{}, nilIn) {
		t.Fatalf("nil deep callback emitted %#v", result)
	}
}

func TestBaselinePoolsCanceledBeforeStartup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := make(chan ScanJob)
	live := make(chan LivenessResult)
	detected := make(chan DetectionResult)
	cfg := ScannerConfig{
		ProbeFunc: func(string, []int, time.Duration) ([]int, error) {
			t.Error("probe called after cancel")
			return nil, nil
		},
		DetectFunc: func(context.Context, ScanJob, []int) (interface{}, bool, error) {
			t.Error("detect called after cancel")
			return nil, false, nil
		},
		DeepScanFunc: func(context.Context, DetectionResult) (interface{}, error) {
			t.Error("deep called after cancel")
			return nil, nil
		},
	}
	deadline := time.After(3 * time.Second)
	select {
	case _, ok := <-StartLivenessPool(ctx, cfg, jobs):
		if ok {
			t.Fatal("liveness output after cancel")
		}
	case <-deadline:
		t.Fatal("liveness did not close")
	}
	select {
	case _, ok := <-StartDetectionPool(ctx, cfg, live):
		if ok {
			t.Fatal("detection output after cancel")
		}
	case <-deadline:
		t.Fatal("detection did not close")
	}
	select {
	case _, ok := <-StartDeepScanPool(ctx, cfg, detected):
		if ok {
			t.Fatal("deep output after cancel")
		}
	case <-deadline:
		t.Fatal("deep did not close")
	}
}

func TestBaselinePoolWorkerCounts(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"liveness", "detection", "deep"} {
		for _, configured := range []int{-1, 0, 2} {
			t.Run(fmt.Sprintf("%s/%d", stage, configured), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				want := configured
				if want <= 0 {
					want = 5
					if stage == "liveness" {
						want = 10
					}
				}
				// Hold callbacks until all expected workers have entered. This
				// proves defaults/overrides without relying on startup jitter,
				// callback timing, or real probes. Context releases failed tests.
				entered := make(chan struct{}, 2*want)
				release := make(chan struct{})
				var active, peak, calls atomic.Int32
				callback := func() {
					calls.Add(1)
					n := active.Add(1)
					defer active.Add(-1)
					for p := peak.Load(); n > p; p = peak.Load() {
						if peak.CompareAndSwap(p, n) {
							break
						}
					}
					entered <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				cfg := ScannerConfig{
					LivenessWorkers: configured, DetectionWorkers: configured, DeepScanWorkers: configured,
					ProbeFunc: func(string, []int, time.Duration) ([]int, error) { callback(); return []int{9100}, nil },
					DetectFunc: func(context.Context, ScanJob, []int) (interface{}, bool, error) {
						callback()
						return "detected", true, nil
					},
					DeepScanFunc: func(context.Context, DetectionResult) (interface{}, error) { callback(); return "scanned", nil },
				}
				jobs := make(chan ScanJob, 2*want)
				live := make(chan LivenessResult, 2*want)
				detected := make(chan DetectionResult, 2*want)
				for i := 0; i < 2*want; i++ {
					jobs <- ScanJob{IP: "fake"}
					live <- LivenessResult{Job: ScanJob{IP: "fake"}, Alive: true, OpenPorts: []int{9100}}
					detected <- DetectionResult{Job: ScanJob{IP: "fake"}, IsPrinter: true}
				}
				close(jobs)
				close(live)
				close(detected)
				var drain func() int
				switch stage {
				case "liveness":
					out := StartLivenessPool(ctx, cfg, jobs)
					drain = func() int {
						count := 0
						for range out {
							count++
						}
						return count
					}
				case "detection":
					out := StartDetectionPool(ctx, cfg, live)
					drain = func() int {
						count := 0
						for range out {
							count++
						}
						return count
					}
				case "deep":
					out := StartDeepScanPool(ctx, cfg, detected)
					drain = func() int {
						count := 0
						for range out {
							count++
						}
						return count
					}
				}
				for i := 0; i < want; i++ {
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatalf("only %d/%d workers entered: %v", i, want, ctx.Err())
					}
				}
				close(release)
				count := drain()
				if ctx.Err() != nil || count != 2*want || calls.Load() != int32(2*want) || peak.Load() != int32(want) {
					t.Fatalf("count=%d, calls=%d, peak=%d, want workers=%d, ctx=%v", count, calls.Load(), peak.Load(), want, ctx.Err())
				}
			})
		}
	}
}
