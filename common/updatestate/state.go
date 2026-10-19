// Package updatestate maintains public publisher recovery pins independently
// of the application Server and B2. GitHub and its administrators remain trusted.
package updatestate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"printmaster/common/updatetrust"
)

type Pending struct {
	Publication updatetrust.PublicationIdentity `json:"publication"`
	Previous    string                          `json:"previous_timestamp_sha256"`
}

type State struct {
	Schema     int                              `json:"schema"`
	Generation int64                            `json:"generation"`
	ParentBlob string                           `json:"parent_blob"`
	Latest     *updatetrust.PublicationIdentity `json:"latest"`
	Pending    *Pending                         `json:"pending"`
}

// Backend must update the existing state using the exact previously read blob
// revision. Missing state is always an error, never an implicit bootstrap.
type Backend interface {
	Read(context.Context) ([]byte, string, error)
	Replace(context.Context, string, []byte) error
}

type Journal struct {
	Backend Backend
}

func validHash(value string, length int) bool {
	return len(value) == length && strings.Trim(value, "0123456789abcdef") == ""
}

func validIdentity(p updatetrust.PublicationIdentity) bool {
	return validHash(p.RootSHA256, 64) && validHash(p.TimestampSHA256, 64) &&
		validHash(p.RecordSHA256, 64) && p.Version > 0
}

func (j Journal) Read(ctx context.Context) (State, string, error) {
	if j.Backend == nil {
		return State{}, "", fmt.Errorf("publication custody backend required")
	}
	data, revision, err := j.Backend.Read(ctx)
	if err != nil {
		return State{}, "", err
	}
	if len(data) == 0 || len(data) > 16<<10 || !validHash(revision, 40) {
		return State{}, "", fmt.Errorf("invalid publication state length/revision")
	}
	var state State
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, "", fmt.Errorf("decode publication state: %w", err)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return State{}, "", fmt.Errorf("trailing publication state data")
	}
	if state.Schema != 1 || state.Generation < 0 || state.Generation >= 1<<62 ||
		(state.Generation == 0 && (state.ParentBlob != "" || state.Latest != nil || state.Pending != nil)) ||
		(state.Generation > 0 && (!validHash(state.ParentBlob, 40) || (state.Latest == nil && state.Pending == nil))) ||
		(state.Latest != nil && !validIdentity(*state.Latest)) {
		return State{}, "", fmt.Errorf("invalid publication state schema/generation/identity")
	}
	if state.Pending != nil {
		p := state.Pending
		if !validIdentity(p.Publication) ||
			(state.Latest == nil && p.Previous != "") ||
			(state.Latest != nil && (p.Previous != state.Latest.TimestampSHA256 ||
				p.Publication.Version <= state.Latest.Version ||
				p.Publication.RootSHA256 != state.Latest.RootSHA256 ||
				p.Publication.TimestampSHA256 == state.Latest.TimestampSHA256)) {
			return State{}, "", fmt.Errorf("invalid pending publication ancestry")
		}
	}
	return state, revision, nil
}

func (j Journal) write(ctx context.Context, state State, revision string) (string, error) {
	state.Generation++
	state.ParentBlob = revision
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if err := j.Backend.Replace(ctx, revision, data); err != nil {
		return "", fmt.Errorf("conditional publication record write failed or ambiguous; reread and retry exact publication: %w", err)
	}
	actual, next, err := j.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("publication record read-back: %w", err)
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		return "", err
	}
	if string(data) != string(encoded) {
		return "", fmt.Errorf("publication record read-back diverged")
	}
	return next, nil
}

func (j Journal) Prepare(ctx context.Context, p updatetrust.PublicationIdentity, previous string, bootstrap bool) (string, error) {
	if !validIdentity(p) || (bootstrap && previous != "") || (!bootstrap && !validHash(previous, 64)) {
		return "", fmt.Errorf("valid publication identity and explicit bootstrap or previous pin required")
	}
	state, revision, err := j.Read(ctx)
	if err != nil {
		return "", err
	}
	if state.Pending != nil {
		if state.Pending.Publication != p || state.Pending.Previous != previous {
			return "", fmt.Errorf("another publication is pending; recover exact sealed bytes, do not sign again")
		}
		return revision, nil
	}
	if state.Latest != nil && *state.Latest == p {
		// Completion may have succeeded despite a lost HTTP response.
		return revision, nil
	}
	if (state.Latest == nil && !bootstrap) ||
		(state.Latest != nil && (bootstrap || previous != state.Latest.TimestampSHA256 ||
			p.Version <= state.Latest.Version || p.RootSHA256 != state.Latest.RootSHA256 ||
			p.TimestampSHA256 == state.Latest.TimestampSHA256)) {
		return "", fmt.Errorf("publication does not advance independent authority; root transitions require owner review")
	}
	state.Pending = &Pending{Publication: p, Previous: previous}
	return j.write(ctx, state, revision)
}

func (j Journal) Complete(ctx context.Context, reservation string, p updatetrust.PublicationIdentity) error {
	state, revision, err := j.Read(ctx)
	if err != nil {
		return err
	}
	if !validIdentity(p) || !validHash(reservation, 40) || revision != reservation {
		return fmt.Errorf("publication reservation changed; preserve pending state for owner review")
	}
	if state.Pending == nil && state.Latest != nil && *state.Latest == p {
		return nil
	}
	if state.Pending == nil || state.Pending.Publication != p {
		return fmt.Errorf("matching pending publication required")
	}
	state.Latest, state.Pending = &p, nil
	_, err = j.write(ctx, state, revision)
	return err
}

// Latest refuses ambiguous pending state even if an earlier commit is present.
// It cannot detect an administrator rolling back all GitHub state.
func (j Journal) Latest(ctx context.Context) (updatetrust.PublicationIdentity, error) {
	state, _, err := j.Read(ctx)
	if err != nil {
		return updatetrust.PublicationIdentity{}, err
	}
	if state.Pending != nil || state.Latest == nil {
		return updatetrust.PublicationIdentity{}, fmt.Errorf("no unambiguous committed publication; bootstrap/pending state needs owner recovery")
	}
	return *state.Latest, nil
}
