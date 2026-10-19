package updatetrust

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testCustody struct {
	prepare  func(PublicationIdentity) (string, error)
	complete func(string, PublicationIdentity) error
}

func (s testCustody) Prepare(_ context.Context, p PublicationIdentity, _ string, _ bool) (string, error) {
	return s.prepare(p)
}

func (s testCustody) Complete(_ context.Context, r string, p PublicationIdentity) error {
	return s.complete(r, p)
}

func TestCustodySurroundsVerifiedDelivery(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "prepare failure", "delivery failure", "completion failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			_, opts, recovery, public := deliveryFixture(t)
			opts.Bootstrap, opts.ExpectedPreviousSHA256 = true, ""
			prepared, completed := false, false
			var identity PublicationIdentity
			stop := errors.New("injected custody failure")
			opts.Custody = testCustody{
				prepare: func(p PublicationIdentity) (string, error) {
					if len(public.writes) != 0 || len(recovery.files) == 0 {
						t.Fatal("reservation must follow retained payload and precede public writes")
					}
					if scenario == "prepare failure" {
						return "", stop
					}
					prepared, identity = true, p
					if p.TimestampSHA256 != opts.ExpectedTimestampSHA256 || !validPin(p.RecordSHA256) ||
						p.RootSHA256 != digest(opts.Root) || p.Version <= 0 {
						t.Fatal("custody not bound to captured authenticated bytes")
					}
					return "reservation", nil
				},
				complete: func(reservation string, p PublicationIdentity) error {
					if !prepared || reservation != "reservation" || p != identity ||
						digest(public.files["timestamp.json"]) != p.TimestampSHA256 {
						t.Fatal("authority completed before exact remote publication")
					}
					completed = true
					if scenario == "completion failure" {
						return stop
					}
					return nil
				},
			}
			public.beforePut = func(string, []byte) error {
				if !prepared {
					t.Fatal("public writes without reservation")
				}
				if scenario == "delivery failure" {
					return stop
				}
				return nil
			}
			err := DeliverPublication(context.Background(), opts)
			if scenario == "success" {
				if err != nil || !completed {
					t.Fatalf("custody delivery failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("failure not surfaced")
			}
			if scenario == "delivery failure" && completed {
				t.Fatal("failed delivery advanced authority")
			}
			if scenario == "completion failure" && !strings.Contains(err.Error(), "remote delivery verified") {
				t.Fatalf("ambiguous final authority not explicit: %v", err)
			}
		})
	}
}
