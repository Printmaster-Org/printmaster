package autoupdate

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedRepositoryPackageOutcomes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"awaiting-startup", "inspection-required", "rejected", "unbound rejection", "schema", "success", "wrong target", "missing target", "missing run", "bad run", "rejected run", "unknown field", "duplicate", "zero length", "oversized", "truncated", "cancelled", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			_, target := packageRequestTestFrame(t, []byte("package"))
			outcome := RepositoryPackageOutcome{Schema: 1, Status: "awaiting-startup", RunID: strings.Repeat("a", 32), Target: &target}
			switch mode {
			case "inspection-required":
				outcome.Status = mode
			case "rejected":
				outcome.Status, outcome.RunID = mode, ""
			case "unbound rejection":
				outcome.Status, outcome.RunID, outcome.Target = "rejected", "", nil
			case "schema":
				outcome.Schema++
			case "success":
				outcome.Status = mode
			case "wrong target":
				other := target
				other.Version = "2.0.0"
				outcome.Target = &other
			case "missing target":
				outcome.Target = nil
			case "missing run":
				outcome.RunID = ""
			case "bad run":
				outcome.RunID = "../arbitrary"
			case "rejected run":
				outcome.Status = "rejected"
			}
			data, err := json.Marshal(outcome)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown field" {
				data = append([]byte(`{"command":"arbitrary",`), data[1:]...)
			} else if mode == "duplicate" {
				data = append([]byte(`{"status":"rejected",`), data[1:]...)
			}
			frame := make([]byte, 4)
			binary.BigEndian.PutUint32(frame, uint32(len(data)))
			if mode == "zero length" {
				binary.BigEndian.PutUint32(frame, 0)
			} else if mode == "oversized" {
				binary.BigEndian.PutUint32(frame, 4097)
			}
			frame = append(frame, data...)
			if mode == "truncated" {
				frame = frame[:len(frame)-1]
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if mode != "blocked" {
				if _, err := writer.Write(frame); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "cancelled" {
				cancel()
			} else if mode == "blocked" {
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
			}
			got, err := ReadRepositoryPackageOutcome(ctx, input, target)
			valid := mode == "awaiting-startup" || mode == "inspection-required" || mode == "rejected" || mode == "unbound rejection"
			if (err == nil) != valid {
				t.Fatalf("outcome result: %+v %v", got, err)
			}
			if valid && !reflect.DeepEqual(got, outcome) {
				t.Fatalf("outcome changed: %+v", got)
			}
			if !valid && !reflect.DeepEqual(got, RepositoryPackageOutcome{}) {
				t.Fatalf("failed reply retained actionable data: %+v", got)
			}
			if mode == "blocked" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("read did not honor timeout: %v", err)
			}
			if _, err := input.Stat(); err == nil {
				t.Fatal("outcome reader did not close owned pipe")
			}
		})
	}
}

func TestPrivilegedRepositoryPackageResponseRejection(t *testing.T) {
	t.Parallel()
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing policy", true: "cancelled"}[cancelled], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			input, sender, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			response, output, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			done := make(chan error, 1)
			go func() {
				done <- ServePrivilegedRepositoryPackageResponse(ctx, nil, "", filepath.Join(dir, "state"),
					filepath.Join(dir, "missing-policy"), input, output)
			}()
			_, target := packageRequestTestFrame(t, []byte("package"))
			readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
			defer readCancel()
			got, err := ReadRepositoryPackageOutcome(readCtx, response, target)
			if err != nil || got.Status != "rejected" || got.Target != nil || got.RunID != "" {
				t.Fatalf("unsafe rejection reply: %+v %v", got, err)
			}
			if err := <-done; err == nil {
				t.Fatal("helper concealed configuration failure")
			}
			if _, err := input.Stat(); err == nil {
				t.Fatal("input was not closed")
			}
			if _, err := output.Stat(); err == nil {
				t.Fatal("output was not closed")
			}
		})
	}
}

func TestPrivilegedRepositoryPackageOutcomeWriter(t *testing.T) {
	t.Parallel()
	_, target := packageRequestTestFrame(t, []byte("package"))
	want := RepositoryPackageOutcome{Schema: 1, Status: "awaiting-startup", Target: &target, RunID: strings.Repeat("a", 32)}
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- writeRepositoryPackageOutcome(ctx, output, want) }()
	got, err := ReadRepositoryPackageOutcome(ctx, input, target)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("writer roundtrip: %+v %v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
