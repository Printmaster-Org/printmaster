package autoupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signedTestManifest builds a manifest signed by key, mirroring the Server's
// ManifestPayload JSON.
func signedTestManifest(t *testing.T, priv ed25519.PrivateKey, keyID string) *UpdateManifest {
	t.Helper()
	m := &UpdateManifest{Component: "agent", Version: "1.2.0", Platform: "linux", Arch: "amd64", Format: "binary",
		Channel: "stable", SHA256: strings.Repeat("ab", 32), SizeBytes: 42, SigningKeyID: keyID}
	payload, err := json.Marshal(map[string]interface{}{
		"manifest_version": "1.0", "component": m.Component, "version": m.Version, "platform": m.Platform,
		"arch": m.Arch, "format": m.Format, "channel": m.Channel, "sha256": m.SHA256, "size_bytes": m.SizeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.SignedPayload = base64.StdEncoding.EncodeToString(payload)
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	m.SigningPublicKey = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	return m
}

func newTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestVerifyManifestSignatureOutcomes(t *testing.T) {
	t.Parallel()
	priv := newTestKey(t)
	cases := map[string]struct {
		mutate func(*UpdateManifest)
		want   SignatureStatus
		fields string
	}{
		"valid":            {func(*UpdateManifest) {}, SignatureVerified, ""},
		"older server":     {func(m *UpdateManifest) { m.SignedPayload, m.SigningPublicKey = "", "" }, SignatureUnverifiable, ""},
		"bad key encoding": {func(m *UpdateManifest) { m.SigningPublicKey = "!!" }, SignatureInvalid, ""},
		"bad signature":    {func(m *UpdateManifest) { m.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64)) }, SignatureInvalid, ""},
		"other key": {func(m *UpdateManifest) {
			m.SigningPublicKey = base64.StdEncoding.EncodeToString(newTestKey(t).Public().(ed25519.PublicKey))
		}, SignatureInvalid, ""},
		"tampered sha":         {func(m *UpdateManifest) { m.SHA256 = strings.Repeat("ff", 32) }, SignatureMismatch, "sha256"},
		"tampered size+ver":    {func(m *UpdateManifest) { m.SizeBytes, m.Version = 1, "9.9.9" }, SignatureMismatch, "version,size_bytes"},
		"format default equal": {func(m *UpdateManifest) { m.Format = "" }, SignatureVerified, ""},
		"sha case insensitive": {func(m *UpdateManifest) { m.SHA256 = strings.ToUpper(m.SHA256) }, SignatureVerified, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := signedTestManifest(t, priv, "k1")
			tc.mutate(m)
			audit := verifyManifestSignature(m)
			if audit.Status != tc.want || strings.Join(audit.MismatchFields, ",") != tc.fields {
				t.Fatalf("got %s %v (%s), want %s %s", audit.Status, audit.MismatchFields, audit.Detail, tc.want, tc.fields)
			}
		})
	}
}

func TestSigningKeyPinTrustOnFirstUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := newSigningKeyPinStore(dir)
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	first, second := newTestKey(t), newTestKey(t)

	if audit, err := store.audit(signedTestManifest(t, first, "k1"), now); err != nil || audit.Status != SignatureVerified {
		t.Fatalf("first use: %+v %v", audit, err)
	}
	pin, err := store.load()
	if err != nil || pin == nil || pin.KeyID != "k1" || !pin.PinnedAt.Equal(now) {
		t.Fatalf("pin not stored: %+v %v", pin, err)
	}
	if audit, _ := store.audit(signedTestManifest(t, first, "k1"), now); audit.Status != SignatureVerified {
		t.Fatalf("pinned key: %s", audit.Status)
	}

	// A tampered manifest must not replace the pin.
	tampered := signedTestManifest(t, second, "k2")
	tampered.SHA256 = strings.Repeat("00", 32)
	if audit, _ := store.audit(tampered, now); audit.Status != SignatureMismatch {
		t.Fatalf("tampered: %s", audit.Status)
	}
	if pin, _ := store.load(); pin.KeyID != "k1" {
		t.Fatalf("tampered manifest re-pinned to %s", pin.KeyID)
	}

	// A rotated key is reported once, then re-pinned.
	audit, _ := store.audit(signedTestManifest(t, second, "k2"), now)
	if audit.Status != SignatureKeyChanged || audit.PinnedKeyID != "k1" || audit.PinnedPrint == "" {
		t.Fatalf("rotation: %+v", audit)
	}
	if audit, _ := store.audit(signedTestManifest(t, second, "k2"), now); audit.Status != SignatureVerified {
		t.Fatalf("after re-pin: %s", audit.Status)
	}
}

func TestSigningKeyPinRecoversFromCorruptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, signingKeyPinFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSigningKeyPinStore(dir)
	if audit, err := store.audit(signedTestManifest(t, newTestKey(t), "k1"), time.Now()); err != nil || audit.Status != SignatureVerified {
		t.Fatalf("corrupt pin: %+v %v", audit, err)
	}
	if pin, err := store.load(); err != nil || pin.KeyID != "k1" {
		t.Fatalf("pin not rewritten: %+v %v", pin, err)
	}
}

// TestFetchManifestAuditIsLogOnly ensures an invalid signature never blocks
// the manifest from being returned.
func TestFetchManifestAuditIsLogOnly(t *testing.T) {
	t.Parallel()
	m := signedTestManifest(t, newTestKey(t), "k1")
	m.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	manager := &Manager{stateDir: t.TempDir(), platform: "linux", arch: "amd64", channel: "stable", clock: time.Now,
		client: &mockUpdateClient{manifest: m}}
	got, err := manager.fetchManifest(t.Context())
	if err != nil || got != m {
		t.Fatalf("log-only audit blocked manifest: %v %v", got, err)
	}
}
