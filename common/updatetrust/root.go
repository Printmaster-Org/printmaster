package updatetrust

import (
	"crypto"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// InitializeRoot is an explicit offline provisioning operation. Routine
// publishing never calls it and never needs the root private key.
func InitializeRoot(rootKey ed25519.PrivateKey, routinePublicKeys map[string]crypto.PublicKey, expires time.Time) ([]byte, error) {
	if len(rootKey) != ed25519.PrivateKeySize || !expires.After(time.Now()) {
		return nil, fmt.Errorf("valid offline root key and future expiry required")
	}
	root := metadata.Root(expires.UTC())
	keys := map[string]crypto.PublicKey{"root": rootKey.Public()}
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		if routinePublicKeys[role] == nil {
			return nil, fmt.Errorf("public key required for %s", role)
		}
		keys[role] = routinePublicKeys[role]
	}
	seen := make(map[string]bool)
	for role, public := range keys {
		if _, ok := public.(ed25519.PublicKey); !ok {
			return nil, fmt.Errorf("publisher role %s requires an Ed25519 public key", role)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			return nil, err
		}
		id, err := key.ID()
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, fmt.Errorf("publisher roles require distinct signing keys")
		}
		seen[id] = true
		if err := root.Signed.AddKey(key, role); err != nil {
			return nil, err
		}
	}
	if err := signMetadata(root, rootKey); err != nil {
		return nil, err
	}
	return root.ToBytes(false)
}

// RotateRoot supports the initial single-maintainer, one-root-signature policy.
// Both old and replacement root authorities must authorize the next version.
// Multiple custodians require a deliberate threshold-signing ceremony instead.
func RotateRoot(previous []byte, oldKey, newKey ed25519.PrivateKey, expires time.Time) ([]byte, error) {
	if len(oldKey) != ed25519.PrivateKeySize || len(newKey) != ed25519.PrivateKeySize || !expires.After(time.Now()) {
		return nil, fmt.Errorf("valid old/new offline keys and future expiry required")
	}
	oldRoot, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return nil, err
	}
	if err := oldRoot.VerifyDelegate("root", oldRoot); err != nil {
		return nil, err
	}
	if oldRoot.Signed.Roles["root"].Threshold != 1 {
		return nil, fmt.Errorf("root threshold requires a multi-signer ceremony")
	}
	next, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return nil, err
	}
	oldIDs := append([]string(nil), next.Signed.Roles["root"].KeyIDs...)
	for _, id := range oldIDs {
		if err := next.Signed.RevokeKey(id, "root"); err != nil {
			return nil, err
		}
	}
	key, err := metadata.KeyFromPublicKey(newKey.Public())
	if err != nil {
		return nil, err
	}
	newID, err := key.ID()
	if err != nil {
		return nil, err
	}
	for role, spec := range next.Signed.Roles {
		if role != "root" {
			for _, id := range spec.KeyIDs {
				if id == newID {
					return nil, fmt.Errorf("offline root key must remain separate from routine publisher keys")
				}
			}
		}
	}
	if err := next.Signed.AddKey(key, "root"); err != nil {
		return nil, err
	}
	next.Signed.Version++
	next.Signed.Expires, next.Signatures = expires.UTC(), nil
	if err := signMetadata(next, oldKey); err != nil {
		return nil, err
	}
	if err := signMetadata(next, newKey); err != nil {
		return nil, err
	}
	if err := oldRoot.VerifyDelegate("root", next); err != nil {
		return nil, fmt.Errorf("previous root authorization: %w", err)
	}
	if err := next.VerifyDelegate("root", next); err != nil {
		return nil, fmt.Errorf("replacement root authorization: %w", err)
	}
	return next.ToBytes(false)
}
