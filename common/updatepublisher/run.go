// Package updatepublisher orchestrates one externally serialized publication.
package updatepublisher

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"printmaster/common/updatestate"
	"printmaster/common/updatetrust"
)

type RecoveryStore interface {
	updatetrust.PublicationStore
	updatetrust.PinnedPublicationReader
}

type Options struct {
	// Work is an existing empty protected directory on the ephemeral runner.
	Work                  string
	Root                  []byte
	Journal               updatestate.Journal
	Recovery              RecoveryStore
	Public                updatetrust.PublicationStore
	LoadKeys              func() (map[string]ed25519.PrivateKey, error)
	Targets               []updatetrust.PublishTarget
	Bootstrap             bool
	ExternalSerialization bool
}

type Result struct {
	Publication      updatetrust.PublicationIdentity `json:"publication"`
	RecoveredPending bool                            `json:"recovered_pending"`
}

// Run never signs while a pending publication exists. Retained payload is
// restored into a new runner and delivered using the original exact pins.
func Run(ctx context.Context, opts Options) (Result, error) {
	fail := func(err error) (Result, error) { return Result{}, err }
	if !opts.ExternalSerialization || opts.Recovery == nil || opts.Public == nil || opts.LoadKeys == nil {
		return fail(fmt.Errorf("serialized publisher, stores and deferred key loader required"))
	}
	if err := updatetrust.ValidateBootstrapRoot(opts.Root, time.Now()); err != nil {
		return fail(err)
	}
	state, _, err := opts.Journal.Read(ctx)
	if err != nil {
		return fail(err)
	}
	if (state.Latest != nil || state.Pending != nil) && opts.Bootstrap {
		return fail(fmt.Errorf("bootstrap is permitted only for explicit generation-zero authority"))
	}
	if state.Latest == nil && state.Pending == nil && !opts.Bootstrap {
		return fail(fmt.Errorf("unprovisioned authority requires explicit bootstrap"))
	}
	entries, err := os.ReadDir(opts.Work)
	if err != nil {
		return fail(err)
	}
	if len(entries) != 0 {
		return fail(fmt.Errorf("publisher requires an existing empty work directory"))
	}
	dirs := make(map[string]string)
	for _, name := range []string{"workspace", "records", "backup", "mirror"} {
		path := filepath.Join(opts.Work, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			return fail(err)
		}
		dirs[name] = path
	}
	previous := ""
	var identity updatetrust.PublicationIdentity
	sealed := ""
	recovered := state.Pending != nil
	if recovered && opts.Targets != nil {
		return fail(fmt.Errorf("pending recovery cannot accept a new target inventory"))
	}
	if recovered {
		identity = state.Pending.Publication
		previous = state.Pending.Previous
	} else if state.Latest != nil {
		identity = *state.Latest
		previous = identity.TimestampSHA256
	}
	if recovered || state.Latest != nil {
		if identity.RootSHA256 != fmt.Sprintf("%x", sha256.Sum256(opts.Root)) {
			return fail(fmt.Errorf("authority root differs; automatic root transition forbidden"))
		}
		if err := updatetrust.RestorePublication(ctx, updatetrust.RestoreOptions{
			Directory: dirs["workspace"], RecordDirectory: dirs["records"], Root: opts.Root,
			ExpectedTimestampSHA256: identity.TimestampSHA256, ExpectedRecordSHA256: identity.RecordSHA256,
			ExpectedTimestampVersion: identity.Version, ExternalSerialization: true, Recovery: opts.Recovery,
		}); err != nil {
			return fail(fmt.Errorf("restore retained publisher state: %w", err))
		}
		if recovered {
			sealed = dirs["workspace"]
		}
	}
	if !recovered {
		// Restore retains its seal for exact pending delivery. A new managed
		// transaction ignores record.json and writes its own sealed inventory.
		keys, err := opts.LoadKeys()
		if err != nil {
			return fail(err)
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := updatetrust.PublishTransaction(updatetrust.TransactionOptions{
			Publish: updatetrust.PublishOptions{Directory: dirs["workspace"], Root: opts.Root, Keys: keys,
				ExpectedTimestampSHA256: previous, Targets: opts.Targets},
			BackupDirectory: dirs["backup"], RecordDirectory: dirs["records"], MirrorDirectory: dirs["mirror"],
		}); err != nil {
			return fail(fmt.Errorf("sign managed publication: %w", err))
		}
		timestamp, err := os.ReadFile(filepath.Join(dirs["workspace"], "timestamp.json"))
		if err != nil {
			return fail(err)
		}
		sealed = filepath.Join(dirs["backup"], fmt.Sprintf("%x", sha256.Sum256(timestamp)))
		identity, err = updatetrust.SealedIdentity(sealed, opts.Root)
		if err != nil {
			return fail(err)
		}
	}
	if err := updatetrust.DeliverPublication(ctx, updatetrust.DeliveryOptions{
		Directory: sealed, Root: opts.Root, ExpectedTimestampSHA256: identity.TimestampSHA256,
		ExpectedRecordSHA256: identity.RecordSHA256, ExpectedPreviousSHA256: previous,
		Bootstrap: previous == "", ExternalSerialization: true, Recovery: opts.Recovery,
		Public: opts.Public, Custody: opts.Journal,
	}); err != nil {
		return fail(err)
	}
	return Result{Publication: identity, RecoveredPending: recovered}, nil
}
