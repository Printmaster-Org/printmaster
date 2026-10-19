package autoupdate

// Manifest signature auditing (log-only).
//
// The Server signs each release manifest with an ed25519 key and, since the
// signed-payload addition, delivers the exact signed bytes, the key ID and the
// public key alongside the manifest. The Agent:
//
//  1. verifies the signature over those bytes,
//  2. checks that the fields it actually acts on (version, platform, arch,
//     format, channel, SHA-256, size) match the signed payload, and
//  3. pins the signing public key on first successful verification
//     (trust on first use) and reports when a later manifest uses another key.
//
// This mode never blocks an update: every outcome is logged and the existing
// SHA-256 download check remains the enforced integrity control. Because the
// public key arrives over the same authenticated Server connection, this does
// not protect against a compromised Server; it detects corrupted or
// inconsistent manifests and unexpected signing-key changes so operators can
// investigate before enforcement is introduced.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"printmaster/common/updatepolicy"
)

const signingKeyPinFile = "signing_key_pin.json"

// SignatureStatus classifies the outcome of a manifest signature audit.
type SignatureStatus string

const (
	// SignatureVerified: valid signature, consistent fields, pinned key.
	SignatureVerified SignatureStatus = "verified"
	// SignatureUnverifiable: the Server did not send what is needed to verify
	// (older Server, or its signing key record is unavailable).
	SignatureUnverifiable SignatureStatus = "unverifiable"
	// SignatureInvalid: malformed key/signature/payload or a failed signature.
	SignatureInvalid SignatureStatus = "invalid"
	// SignatureMismatch: the signature is valid but the served fields differ
	// from the signed payload.
	SignatureMismatch SignatureStatus = "mismatch"
	// SignatureKeyChanged: valid signature by a key other than the pinned one.
	SignatureKeyChanged SignatureStatus = "key_changed"
)

// SignatureAudit is the result of auditing one manifest.
type SignatureAudit struct {
	Status         SignatureStatus
	KeyID          string
	Fingerprint    string   // Short SHA-256 fingerprint of the public key.
	PinnedKeyID    string   // Previously pinned key, when it differs.
	PinnedPrint    string   // Fingerprint of the previously pinned key.
	MismatchFields []string // Served fields that differ from the signed payload.
	Detail         string
}

// signedManifestFields are the signed payload fields the Agent relies on.
// Field names match the Server's releases.ManifestPayload JSON.
type signedManifestFields struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Format    string `json:"format"`
	Channel   string `json:"channel"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// verifyManifestSignature checks the signature and field consistency of a
// manifest. It does not consult or update the key pin.
func verifyManifestSignature(manifest *UpdateManifest) SignatureAudit {
	audit := SignatureAudit{KeyID: manifest.SigningKeyID}
	if manifest.Signature == "" || manifest.SignedPayload == "" || manifest.SigningPublicKey == "" {
		audit.Status = SignatureUnverifiable
		audit.Detail = "server did not provide signature, signed payload and public key"
		return audit
	}
	pub, err := base64.StdEncoding.DecodeString(manifest.SigningPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		audit.Status = SignatureInvalid
		audit.Detail = "malformed signing public key"
		return audit
	}
	audit.Fingerprint = keyFingerprint(pub)
	sig, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		audit.Status = SignatureInvalid
		audit.Detail = "malformed signature"
		return audit
	}
	payload, err := base64.StdEncoding.DecodeString(manifest.SignedPayload)
	if err != nil {
		audit.Status = SignatureInvalid
		audit.Detail = "malformed signed payload encoding"
		return audit
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		audit.Status = SignatureInvalid
		audit.Detail = "signature does not match signed payload"
		return audit
	}
	var signed signedManifestFields
	if err := json.Unmarshal(payload, &signed); err != nil {
		audit.Status = SignatureInvalid
		audit.Detail = "signed payload is not valid JSON"
		return audit
	}
	if fields := manifestFieldMismatches(manifest, signed); len(fields) > 0 {
		audit.Status = SignatureMismatch
		audit.MismatchFields = fields
		audit.Detail = "served manifest differs from signed payload"
		return audit
	}
	audit.Status = SignatureVerified
	return audit
}

// manifestFieldMismatches lists served fields that differ from the signed
// payload. Format and channel use the same defaults as the Server (binary,
// stable) so older payloads without them still compare equal.
func manifestFieldMismatches(served *UpdateManifest, signed signedManifestFields) []string {
	var fields []string
	check := func(name, got, want string) {
		if !strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
			fields = append(fields, name)
		}
	}
	check("component", served.Component, signed.Component)
	check("version", served.Version, signed.Version)
	check("platform", served.Platform, signed.Platform)
	check("arch", served.Arch, signed.Arch)
	check("format", normalizeSignedFormat(served.Format), normalizeSignedFormat(signed.Format))
	check("channel", defaultChannel(served.Channel), defaultChannel(signed.Channel))
	check("sha256", served.SHA256, signed.SHA256)
	if served.SizeBytes != signed.SizeBytes {
		fields = append(fields, "size_bytes")
	}
	return fields
}

func normalizeSignedFormat(raw string) string {
	if format, ok := updatepolicy.ParseArtifactFormat(raw); ok {
		return format.String()
	}
	return strings.TrimSpace(raw)
}

func defaultChannel(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "stable"
	}
	return raw
}

// keyFingerprint returns a short, log-friendly SHA-256 fingerprint.
func keyFingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// signingKeyPin is the persisted trust-on-first-use record.
type signingKeyPin struct {
	KeyID       string    `json:"key_id"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	PinnedAt    time.Time `json:"pinned_at"`
}

// signingKeyPinStore persists the pinned signing key in the update state
// directory. Reads and writes are serialized; a missing file means no pin.
type signingKeyPinStore struct {
	mu   sync.Mutex
	path string
}

func newSigningKeyPinStore(stateDir string) *signingKeyPinStore {
	return &signingKeyPinStore{path: filepath.Join(stateDir, signingKeyPinFile)}
}

func (s *signingKeyPinStore) load() (*signingKeyPin, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pin signingKeyPin
	if err := json.Unmarshal(data, &pin); err != nil {
		return nil, fmt.Errorf("corrupt signing key pin: %w", err)
	}
	return &pin, nil
}

func (s *signingKeyPinStore) save(pin signingKeyPin) error {
	data, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// audit verifies the manifest and applies trust-on-first-use pinning. Only a
// key that produced a valid, consistent signature is pinned. In log-only mode a
// changed key is reported once and then re-pinned so a legitimate Server key
// rotation does not warn on every check.
func (s *signingKeyPinStore) audit(manifest *UpdateManifest, now time.Time) (SignatureAudit, error) {
	audit := verifyManifestSignature(manifest)
	if audit.Status != SignatureVerified {
		return audit, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pin, err := s.load()
	if err != nil {
		// Treat an unreadable pin as absent and overwrite it below.
		audit.Detail = err.Error()
	}
	if pin != nil && pin.PublicKey == manifest.SigningPublicKey {
		return audit, nil
	}
	if pin != nil {
		audit.Status = SignatureKeyChanged
		audit.PinnedKeyID = pin.KeyID
		audit.PinnedPrint = pin.Fingerprint
	}
	next := signingKeyPin{KeyID: manifest.SigningKeyID, PublicKey: manifest.SigningPublicKey, Fingerprint: audit.Fingerprint, PinnedAt: now.UTC()}
	if err := s.save(next); err != nil {
		return audit, fmt.Errorf("persist signing key pin: %w", err)
	}
	return audit, nil
}
