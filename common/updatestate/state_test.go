package updatestate

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"printmaster/common/updatetrust"
)

type memoryBackend struct {
	data        []byte
	beforeWrite func()
	failWrite   bool
	failAfter   bool
	writes      int
}

func blob(data []byte) string {
	return fmt.Sprintf("%x", sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)))
}

func (b *memoryBackend) Read(context.Context) ([]byte, string, error) {
	if b.data == nil {
		return nil, "", errors.New("missing state")
	}
	return b.data, blob(b.data), nil
}

func (b *memoryBackend) Replace(_ context.Context, previous string, data []byte) error {
	if b.beforeWrite != nil {
		b.beforeWrite()
	}
	if b.failWrite || blob(b.data) != previous {
		return errors.New("conditional write rejected")
	}
	b.data = data
	b.writes++
	if b.failAfter {
		return errors.New("lost write response")
	}
	return nil
}

func fixture() (Journal, *memoryBackend, updatetrust.PublicationIdentity) {
	b := &memoryBackend{data: []byte(`{"schema":1,"generation":0,"parent_blob":"","latest":null,"pending":null}`)}
	p := updatetrust.PublicationIdentity{
		RootSHA256: strings.Repeat("a", 64), TimestampSHA256: strings.Repeat("b", 64),
		RecordSHA256: strings.Repeat("c", 64), Version: 1,
	}
	return Journal{Backend: b}, b, p
}

func TestJournalAdvancementAndRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	j, b, p := fixture()
	if _, err := j.Latest(ctx); err == nil {
		t.Fatal("empty authority selected")
	}
	reservation, err := j.Prepare(ctx, p, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Latest(ctx); err == nil {
		t.Fatal("pending authority selected")
	}
	retry, err := j.Prepare(ctx, p, "", true)
	if err != nil || retry != reservation || b.writes != 1 {
		t.Fatalf("pending retry: %s %v %d", retry, err, b.writes)
	}
	if err := j.Complete(ctx, reservation, p); err != nil {
		t.Fatal(err)
	}
	actual, err := j.Latest(ctx)
	if err != nil || actual != p {
		t.Fatalf("committed identity: %+v %v", actual, err)
	}
	reservation, err = j.Prepare(ctx, p, "", true)
	if err != nil || j.Complete(ctx, reservation, p) != nil || b.writes != 2 {
		t.Fatal("completed retry wrote new history")
	}
	next := p
	next.Version++
	next.TimestampSHA256 = strings.Repeat("d", 64)
	next.RecordSHA256 = strings.Repeat("e", 64)
	reservation, err = j.Prepare(ctx, next, p.TimestampSHA256, false)
	if err != nil || j.Complete(ctx, reservation, next) != nil {
		t.Fatalf("next publication failed: %v", err)
	}
	state, _, err := j.Read(ctx)
	if err != nil || state.Generation != 4 || state.Latest == nil || *state.Latest != next {
		t.Fatalf("lost monotonic state: %+v %v", state, err)
	}
}

func TestJournalRejectsConflictsAndAmbiguousSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, scenario := range []string{"missing", "malformed", "oversized", "unknown", "trailing", "schema",
		"invalid pin", "implicit bootstrap", "pending conflict", "record conflict", "stale reservation",
		"write rejected", "CAS race", "readback divergence", "rollback", "wrong previous", "root transition"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			j, b, p := fixture()
			previous, bootstrap := "", true
			switch scenario {
			case "missing":
				b.data = nil
			case "malformed":
				b.data = []byte("{")
			case "oversized":
				b.data = []byte(strings.Repeat(" ", (16<<10)+1))
			case "unknown":
				b.data = []byte(`{"schema":1,"unexpected":true}`)
			case "trailing":
				b.data = append(b.data, []byte(` {}`)...)
			case "schema":
				b.data = []byte(`{"schema":2}`)
			case "invalid pin":
				p.RecordSHA256 = strings.Repeat("C", 64)
			case "implicit bootstrap":
				bootstrap, previous = false, strings.Repeat("f", 64)
			case "pending conflict", "record conflict", "stale reservation":
				reservation, err := j.Prepare(ctx, p, "", true)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "stale reservation" {
					if err := j.Complete(ctx, strings.Repeat("0", 40), p); err == nil {
						t.Fatal("stale reservation accepted")
					}
					if err := j.Complete(ctx, reservation, p); err != nil {
						t.Fatal(err)
					}
					return
				}
				if scenario == "record conflict" {
					p.RecordSHA256 = strings.Repeat("f", 64)
				} else {
					p.TimestampSHA256 = strings.Repeat("f", 64)
				}
			case "write rejected":
				b.failWrite = true
			case "CAS race":
				b.beforeWrite = func() { b.data = append(b.data, '\n') }
			case "readback divergence":
				j.Backend = divergentBackend{b}
			case "rollback", "wrong previous", "root transition":
				reservation, err := j.Prepare(ctx, p, "", true)
				if err != nil || j.Complete(ctx, reservation, p) != nil {
					t.Fatal("setup commit failed")
				}
				previous, bootstrap = p.TimestampSHA256, false
				p.TimestampSHA256 = strings.Repeat("d", 64)
				p.RecordSHA256 = strings.Repeat("e", 64)
				if scenario != "rollback" {
					p.Version++
				}
				if scenario == "wrong previous" {
					previous = strings.Repeat("f", 64)
				}
				if scenario == "root transition" {
					p.RootSHA256 = strings.Repeat("f", 64)
				}
			}
			if _, err := j.Prepare(ctx, p, previous, bootstrap); err == nil {
				t.Fatal("invalid advancement accepted")
			}
		})
	}
}

type divergentBackend struct{ *memoryBackend }

func (b divergentBackend) Replace(ctx context.Context, revision string, data []byte) error {
	if err := b.memoryBackend.Replace(ctx, revision, data); err != nil {
		return err
	}
	var state State
	if err := json.Unmarshal(b.data, &state); err != nil {
		return err
	}
	state.Generation++
	var err error
	b.data, err = json.Marshal(state)
	return err
}

func TestLostWriteResponsesRequireExactRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	j, b, p := fixture()
	b.failAfter = true
	if _, err := j.Prepare(ctx, p, "", true); err == nil {
		t.Fatal("lost response hidden")
	}
	b.failAfter = false
	reservation, err := j.Prepare(ctx, p, "", true)
	if err != nil || b.writes != 1 {
		t.Fatal("prepare retry rewrote history")
	}
	b.failAfter = true
	if err := j.Complete(ctx, reservation, p); err == nil {
		t.Fatal("lost completion response hidden")
	}
	b.failAfter = false
	reservation, err = j.Prepare(ctx, p, "", true)
	if err != nil || j.Complete(ctx, reservation, p) != nil || b.writes != 2 {
		t.Fatal("completion retry rewrote history")
	}
}

func TestStateLimitAndMalformedAncestry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	j, b, _ := fixture()
	b.data = append(b.data, []byte(strings.Repeat(" ", (16<<10)-len(b.data)))...)
	if _, _, err := j.Read(ctx); err != nil {
		t.Fatalf("exact 16 KiB limit rejected: %v", err)
	}
	b.data = append(b.data, ' ')
	if _, _, err := j.Read(ctx); err == nil {
		t.Fatal("16 KiB plus one accepted")
	}
	for _, change := range []func(*State){
		func(s *State) { s.Generation = -1 },
		func(s *State) { s.Generation = 1 << 62 },
		func(s *State) { s.Generation = 0 },
		func(s *State) { s.ParentBlob = "" },
		func(s *State) { s.Pending.Previous = strings.Repeat("f", 64) },
		func(s *State) { s.Latest = &s.Pending.Publication },
		func(s *State) { s.Pending.Publication.Version = 0 },
	} {
		j, b, p := fixture()
		if _, err := j.Prepare(ctx, p, "", true); err != nil {
			t.Fatal(err)
		}
		var state State
		if err := json.Unmarshal(b.data, &state); err != nil {
			t.Fatal(err)
		}
		change(&state)
		var err error
		b.data, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := j.Read(ctx); err == nil {
			t.Fatal("malformed authority ancestry accepted")
		}
	}
}
