package agent

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/gosnmp/gosnmp"
)

type fakeStepper struct {
	oids     []string
	failAt   map[int]int // index of start OID -> remaining failures
	calls    int
	lastReps []uint32
}

func (f *fakeStepper) SNMPVersion() gosnmp.SnmpVersion { return gosnmp.Version2c }

func (f *fakeStepper) GetNext(oids []string) (*gosnmp.SnmpPacket, error) {
	return f.GetBulk(oids, 0, 1)
}

func (f *fakeStepper) GetBulk(oids []string, _ uint8, maxReps uint32) (*gosnmp.SnmpPacket, error) {
	f.calls++
	f.lastReps = append(f.lastReps, maxReps)
	start := 0
	for start < len(f.oids) && compareOIDs(f.oids[start], oids[0]) <= 0 {
		start++
	}
	if n := f.failAt[start]; n > 0 {
		f.failAt[start] = n - 1
		return nil, errors.New("request timeout")
	}
	pkt := &gosnmp.SnmpPacket{}
	for i := start; i < len(f.oids) && uint32(i-start) < maxReps; i++ {
		pkt.Variables = append(pkt.Variables, gosnmp.SnmpPDU{Name: f.oids[i], Type: gosnmp.Integer, Value: i})
	}
	if len(pkt.Variables) == 0 {
		pkt.Variables = []gosnmp.SnmpPDU{{Name: oids[0], Type: gosnmp.EndOfMibView}}
	}
	return pkt, nil
}

func TestResumableWalkRecoversFromTimeouts(t *testing.T) {
	var oids []string
	for i := 1; i <= 60; i++ {
		oids = append(oids, ".1.3.6.1.4.1.1248.1."+strconv.Itoa(i))
	}
	oids = append(oids, ".1.3.6.1.4.1.1249.1") // outside root
	f := &fakeStepper{oids: oids, failAt: map[int]int{25: 2}}

	var got []string
	err := resumableWalk(context.Background(), f, "1.3.6.1.4.1.1248", func(p gosnmp.SnmpPDU) error {
		got = append(got, p.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("got %d OIDs, want 60", len(got))
	}
	for i := 1; i < len(got); i++ {
		if compareOIDs(got[i-1], got[i]) >= 0 {
			t.Fatalf("OIDs out of order at %d: %s, %s", i, got[i-1], got[i])
		}
	}
	if f.lastReps[len(f.lastReps)-1] >= walkDefaultMaxRepetitions {
		t.Fatalf("expected batch size to shrink after timeouts, got %v", f.lastReps)
	}
}

func TestResumableWalkGivesUpAfterPersistentFailure(t *testing.T) {
	f := &fakeStepper{
		oids:   []string{".1.3.6.1.2.1.1.1.0", ".1.3.6.1.2.1.1.2.0"},
		failAt: map[int]int{2: 100},
	}
	n := 0
	err := resumableWalk(context.Background(), f, ".1.3.6.1.2.1.1", func(gosnmp.SnmpPDU) error { n++; return nil })
	if err == nil {
		t.Fatal("expected error")
	}
	if n != 2 {
		t.Fatalf("expected 2 OIDs before failure, got %d", n)
	}
}

func TestCompareOIDsNumeric(t *testing.T) {
	if compareOIDs(".1.3.6.1.2", ".1.3.6.1.10") >= 0 {
		t.Fatal("2 should sort before 10")
	}
	if compareOIDs("1.3.6", "1.3.6.1") >= 0 {
		t.Fatal("prefix should sort first")
	}
	if compareOIDs(".1.3.6", "1.3.6") != 0 {
		t.Fatal("leading dot should be ignored")
	}
}
