package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// snmpStepper is implemented by clients that can issue individual GETNEXT /
// GETBULK requests, which lets a walk resume from the last OID it saw after a
// transient timeout instead of aborting the whole subtree.
type snmpStepper interface {
	GetNext(oids []string) (*gosnmp.SnmpPacket, error)
	GetBulk(oids []string, nonRepeaters uint8, maxRepetitions uint32) (*gosnmp.SnmpPacket, error)
	SNMPVersion() gosnmp.SnmpVersion
}

const (
	walkDefaultMaxRepetitions = 25
	walkStepAttempts          = 4
)

// resumableWalk walks root using GETBULK (or GETNEXT on v1), retrying failed
// requests from the last received OID with progressively smaller batches.
func resumableWalk(ctx context.Context, s snmpStepper, root string, fn gosnmp.WalkFunc) error {
	root = "." + strings.TrimPrefix(strings.TrimSpace(root), ".")
	prefix := root + "."
	useBulk := s.SNMPVersion() != gosnmp.Version1
	maxReps := uint32(walkDefaultMaxRepetitions)
	cur := root

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var pkt *gosnmp.SnmpPacket
		var err error
		for attempt := 0; attempt < walkStepAttempts; attempt++ {
			if useBulk {
				pkt, err = s.GetBulk([]string{cur}, 0, maxReps)
			} else {
				pkt, err = s.GetNext([]string{cur})
			}
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if maxReps > 1 {
				maxReps /= 2
			}
		}
		if err != nil {
			return fmt.Errorf("after %s: %w", cur, err)
		}
		if pkt == nil || len(pkt.Variables) == 0 {
			return nil
		}
		for _, v := range pkt.Variables {
			switch v.Type {
			case gosnmp.EndOfMibView, gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
				return nil
			}
			name := "." + strings.TrimPrefix(v.Name, ".")
			if !strings.HasPrefix(name, prefix) {
				return nil
			}
			if compareOIDs(name, cur) <= 0 {
				return fmt.Errorf("agent returned non-increasing OID %s after %s", name, cur)
			}
			if err := fn(v); err != nil {
				return err
			}
			cur = name
		}
	}
}

// compareOIDs numerically compares two dotted OIDs.
func compareOIDs(a, b string) int {
	pa := strings.Split(strings.Trim(a, "."), ".")
	pb := strings.Split(strings.Trim(b, "."), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.ParseUint(pa[i], 10, 64)
		nb, eb := strconv.ParseUint(pb[i], 10, 64)
		if ea != nil || eb != nil {
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
			continue
		}
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}
