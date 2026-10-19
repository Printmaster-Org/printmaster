package updatetrust

import (
	"crypto"
	"crypto/ed25519"
	"fmt"
	"math"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// ValidateBootstrapRoot enforces the initial single-maintainer release policy.
// This checks a root's structure and self-signature, not its provenance: the
// owner must independently confirm the public fingerprint before embedding it.
func ValidateBootstrapRoot(data []byte, now time.Time) error {
	root, err := validateRootPolicy(data)
	if err != nil {
		return err
	}
	if !root.Signed.Expires.After(now) {
		return fmt.Errorf("publisher root has expired; offline root maintenance required")
	}
	return nil
}

// VerifySigningKeys checks restored keys, including after root expiry. It does
// not authorize releases or bypass clients' final metadata expiry checks.
func VerifySigningKeys(data []byte, keys map[string]ed25519.PrivateKey) error {
	root, err := validateRootPolicy(data)
	if err != nil {
		return err
	}
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		key := keys[role]
		if len(key) != ed25519.PrivateKeySize {
			return fmt.Errorf("restored %s key is missing or invalid", role)
		}
		public, err := metadata.KeyFromPublicKey(key.Public())
		if err != nil {
			return err
		}
		id, err := public.ID()
		if err != nil {
			return err
		}
		if id != root.Signed.Roles[role].KeyIDs[0] {
			return fmt.Errorf("restored %s key does not match the trusted public root", role)
		}
	}
	return nil
}

func validateRootPolicy(data []byte) (*metadata.Metadata[metadata.RootType], error) {
	if len(data) == 0 || int64(len(data)) > MaxMetadataBytes {
		return nil, fmt.Errorf("publisher root is missing or oversized")
	}
	root, err := metadata.Root().FromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse publisher root: %w", err)
	}
	if root.Signed.Version <= 0 || !root.Signed.ConsistentSnapshot {
		return nil, fmt.Errorf("publisher root requires a positive version and consistent snapshots")
	}
	if len(root.Signed.Roles) != 4 || len(root.Signed.Keys) != 4 {
		return nil, fmt.Errorf("publisher root requires exactly four distinct role keys")
	}
	seen := make(map[string]bool)
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		spec := root.Signed.Roles[role]
		if spec == nil || spec.Threshold != 1 || len(spec.KeyIDs) != 1 || seen[spec.KeyIDs[0]] {
			return nil, fmt.Errorf("publisher role %s requires one distinct key and threshold one", role)
		}
		id := spec.KeyIDs[0]
		key := root.Signed.Keys[id]
		if key == nil {
			return nil, fmt.Errorf("publisher role %s key is missing", role)
		}
		public, err := key.ToPublicKey()
		if err != nil {
			return nil, fmt.Errorf("publisher role %s public key: %w", role, err)
		}
		if _, ok := public.(ed25519.PublicKey); !ok {
			return nil, fmt.Errorf("publisher role %s requires Ed25519", role)
		}
		actualID, err := key.ID()
		if err != nil || actualID != id {
			return nil, fmt.Errorf("publisher role %s key ID mismatch", role)
		}
		seen[id] = true
	}
	if err := root.VerifyDelegate("root", root); err != nil {
		return nil, fmt.Errorf("publisher root self-authorization: %w", err)
	}
	return root, nil
}

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
	return rotateRoleKeys(previous, oldKey, newKey, map[string]crypto.PublicKey{"root": newKey.Public()}, expires)
}

// RotateRoutineKeys replaces all three online roles in one offline ceremony.
// No previous routine private key is needed, including after loss or compromise.
func RotateRoutineKeys(previous []byte, rootKey ed25519.PrivateKey, public map[string]crypto.PublicKey, expires time.Time) ([]byte, error) {
	if len(rootKey) != ed25519.PrivateKeySize || len(public) != 3 || !expires.After(time.Now()) {
		return nil, fmt.Errorf("offline root key, three replacement routine keys and future expiry required")
	}
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		if public[role] == nil {
			return nil, fmt.Errorf("replacement public key required for %s", role)
		}
	}
	return rotateRoleKeys(previous, rootKey, rootKey, public, expires)
}

func rotateRoleKeys(previous []byte, oldKey, newKey ed25519.PrivateKey, replacements map[string]crypto.PublicKey, expires time.Time) ([]byte, error) {
	// TUF permits an expired previous root to authorize its successor.
	// The release/bootstrap check still requires an unexpired final root.
	oldRoot, err := validateRootPolicy(previous)
	if err != nil {
		return nil, err
	}
	if oldRoot.Signed.Version <= 0 || oldRoot.Signed.Version == math.MaxInt64 {
		return nil, fmt.Errorf("root version cannot be incremented")
	}
	next, err := metadata.Root().FromBytes(previous)
	if err != nil {
		return nil, err
	}
	for role, public := range replacements {
		if _, ok := public.(ed25519.PublicKey); !ok {
			return nil, fmt.Errorf("replacement %s key must be Ed25519", role)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			return nil, err
		}
		id, err := key.ID()
		if err != nil {
			return nil, err
		}
		for _, previousKey := range oldRoot.Signed.Keys {
			previousID, err := previousKey.ID()
			if err != nil {
				return nil, err
			}
			if id == previousID {
				return nil, fmt.Errorf("replacement %s key must be new and distinct from previous role keys", role)
			}
		}
		for _, oldID := range append([]string(nil), next.Signed.Roles[role].KeyIDs...) {
			if err := next.Signed.RevokeKey(oldID, role); err != nil {
				return nil, err
			}
		}
		if err := next.Signed.AddKey(key, role); err != nil {
			return nil, err
		}
	}
	next.Signed.Version++
	next.Signed.Expires, next.Signatures = expires.UTC(), nil
	if err := signMetadata(next, oldKey); err != nil {
		return nil, err
	}
	if !oldKey.Equal(newKey) {
		if err := signMetadata(next, newKey); err != nil {
			return nil, err
		}
	}
	if err := verifyRootSuccessor(oldRoot, next); err != nil {
		return nil, err
	}
	data, err := next.ToBytes(false)
	if err != nil {
		return nil, err
	}
	if err := ValidateBootstrapRoot(data, time.Now()); err != nil {
		return nil, err
	}
	return data, nil
}
