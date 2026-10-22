package updatetrust

import (
	"errors"
	"fmt"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

var ErrTrustClock = errors.New("local clock is behind publisher verification history")

const TrustClockTolerance = 5 * time.Minute

func checkTrustClock(now, verifiedAt time.Time) error {
	if !verifiedAt.IsZero() && now.Before(verifiedAt.Add(-TrustClockTolerance)) {
		return fmt.Errorf("%w: local UTC %s, last successful verification %s; correct the system clock using an independent time source, preserve metadata/checkpoint and retry; do not reset trust state",
			ErrTrustClock, now.Format(time.RFC3339), verifiedAt.Format(time.RFC3339))
	}
	return nil
}

// TrustDiagnostic preserves the original TUF error for errors.Is/errors.As.
// Local time and publisher expiry cannot establish which clock is correct.
type TrustDiagnostic struct {
	Code     string    `json:"code"`
	LocalUTC time.Time `json:"local_utc"`
	Action   string    `json:"action"`
	Cause    error     `json:"-"`
}

func (e *TrustDiagnostic) Error() string {
	return fmt.Sprintf("%s: %v; local UTC %s; %s", e.Code, e.Cause, e.LocalUTC.Format(time.RFC3339), e.Action)
}

func (e *TrustDiagnostic) Unwrap() error { return e.Cause }

func diagnoseTrust(err error, now time.Time) error {
	if err == nil {
		return nil
	}
	code, action := "", ""
	switch {
	case errors.Is(err, ErrTrustState):
		code, action = "trust-state-recovery-required", "preserve metadata and its sibling checkpoint; stop verifier writers and inspect with update-client-trust; never delete state or accept Server-provided replacement trust"
	case errors.Is(err, ErrTrustClock):
		code, action = "trust-clock-rollback", "correct the local clock from an independent time source and retry without resetting trust"
	case errors.Is(err, &metadata.ErrExpiredMetadata{}):
		code, action = "trust-metadata-expired", "check the local clock independently; if correct, obtain refreshed publisher metadata or a valid signed root-rotation chain; do not disable expiry or reset state"
	case errors.Is(err, &metadata.ErrBadVersionNumber{}):
		code, action = "trust-metadata-version", "preserve local version history and investigate stale, replayed or inconsistent publisher delivery; do not reset trust"
	default:
		return err
	}
	return &TrustDiagnostic{Code: code, LocalUTC: now, Action: action, Cause: err}
}
