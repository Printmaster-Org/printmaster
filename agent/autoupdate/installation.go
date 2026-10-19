package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"printmaster/common/updatetrust"
)

var errInstallationNotStarted = errors.New("installation blocked before modification")

func installationErrorCode(err error) string {
	if errors.Is(err, ErrApplicationPolicy) {
		return ErrCodeApplicationPolicy
	}
	return ErrCodeManifestError
}

// InstallationDescriptor is constructed from publisher authorization, not a
// Server-supplied sidecar. Its persisted copy is a receipt, never trust authority.
// The state directories and installation paths are protected before this receipt is used.
type InstallationDescriptor struct {
	RunID         string                    `json:"run_id"`
	FromVersion   string                    `json:"from_version"`
	Authorization updatetrust.Authorization `json:"authorization"`
	StagingPath   string                    `json:"staging_path"`
	BinaryPath    string                    `json:"binary_path"`
	BackupPath    string                    `json:"backup_path"`
	Previous      ArtifactIdentity          `json:"previous"`
}

type ArtifactIdentity struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Mode      uint32 `json:"mode,omitempty"`
}

func fileIdentity(path string) (ArtifactIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return ArtifactIdentity{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ArtifactIdentity{}, err
	}
	if !info.Mode().IsRegular() {
		return ArtifactIdentity{}, fmt.Errorf("artifact is not a regular file")
	}
	hash := sha256.New()
	length, err := io.Copy(hash, file)
	if err != nil {
		return ArtifactIdentity{}, err
	}
	return ArtifactIdentity{
		SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: length, Mode: uint32(info.Mode().Perm()),
	}, nil
}

func (expected ArtifactIdentity) verify(path string) error {
	hash, err := hex.DecodeString(expected.SHA256)
	if err != nil || len(hash) != sha256.Size || expected.SizeBytes <= 0 {
		return fmt.Errorf("invalid expected artifact identity")
	}
	actual, err := fileIdentity(path)
	if err != nil {
		return err
	}
	if actual.SizeBytes != expected.SizeBytes || !strings.EqualFold(actual.SHA256, expected.SHA256) {
		return fmt.Errorf("artifact length or SHA-256 differs from authorized identity")
	}
	return nil
}

func (m *Manager) installationDescriptor(ctx context.Context, manifest *UpdateManifest, staging, backup string) (InstallationDescriptor, error) {
	authorization, err := m.publisherAuthorization(ctx, manifest)
	if err != nil {
		return InstallationDescriptor{}, err
	}
	for _, path := range []string{m.binaryPath, staging} {
		if err := validateInstallPath(path); err != nil {
			return InstallationDescriptor{}, fmt.Errorf("unsafe installation path %s: %w", path, err)
		}
	}
	if m.isService {
		if err := validateServiceInstallPath(m.binaryPath); err != nil {
			return InstallationDescriptor{}, fmt.Errorf("unsafe service executable path: %w", err)
		}
	}
	previous, err := fileIdentity(m.binaryPath)
	if err != nil {
		return InstallationDescriptor{}, fmt.Errorf("record previous installation: %w", err)
	}
	return InstallationDescriptor{
		RunID: m.Progress().RunID, FromVersion: m.currentVersion, Authorization: authorization,
		StagingPath: staging, BinaryPath: m.binaryPath, BackupPath: backup, Previous: previous,
	}, nil
}

func (m *Manager) validateInstallation(ctx context.Context, descriptor InstallationDescriptor) error {
	target := descriptor.Authorization.Target
	if descriptor.RunID == "" || descriptor.RunID != m.Progress().RunID ||
		descriptor.FromVersion != m.currentVersion || descriptor.BinaryPath != m.binaryPath ||
		filepath.Dir(descriptor.StagingPath) != filepath.Join(m.stateDir, stagingDirName) ||
		filepath.Dir(descriptor.BackupPath) != filepath.Join(m.stateDir, backupDirName) {
		return fmt.Errorf("installation descriptor does not match this operation")
	}
	for _, path := range []string{descriptor.BinaryPath, descriptor.StagingPath, descriptor.BackupPath} {
		if err := validateInstallPath(path); err != nil {
			return fmt.Errorf("unsafe installation path %s: %w", path, err)
		}
	}
	if m.isService {
		if err := validateServiceInstallPath(m.binaryPath); err != nil {
			return fmt.Errorf("unsafe service executable path: %w", err)
		}
	}
	manifest := &UpdateManifest{Component: target.Component, Version: target.Version,
		Platform: target.Platform, Arch: target.Arch, Format: target.Format, Channel: target.Channel,
		SHA256: target.SHA256, SizeBytes: target.SizeBytes}
	authorization, err := m.publisherAuthorization(ctx, manifest)
	if err != nil {
		return err
	}
	if authorization != descriptor.Authorization {
		return fmt.Errorf("%w: publisher installation identity changed", ErrPublisherTrust)
	}
	manual, _ := ctx.Value(manualInstallKey{}).(bool)
	allowed, err := m.applicationPolicy(ctx, manifest, manual)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: candidate no longer applicable", ErrApplicationPolicy)
	}
	expected := ArtifactIdentity{SHA256: target.SHA256, SizeBytes: target.SizeBytes}
	if err := expected.verify(descriptor.StagingPath); err != nil {
		return fmt.Errorf("staged artifact verification failed: %w", err)
	}
	if err := descriptor.Previous.verify(descriptor.BinaryPath); err != nil {
		return fmt.Errorf("previous installation changed before handoff: %w", err)
	}
	if err := descriptor.Previous.verify(descriptor.BackupPath); err != nil {
		return fmt.Errorf("recovery backup verification failed: %w", err)
	}
	return nil
}

func (m *Manager) readInstallationReceipt() (InstallationDescriptor, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, "installation.json"))
	if err != nil {
		return InstallationDescriptor{}, err
	}
	var descriptor InstallationDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return descriptor, err
	}
	return descriptor, nil
}

// helperIntegrityCommand embeds expected values rather than a writable manifest path.
func helperIntegrityCommand(path string, identity ArtifactIdentity) string {
	script := fmt.Sprintf("$ErrorActionPreference='Stop'; try { $f=[IO.File]::OpenRead('%s'); $h=[Security.Cryptography.SHA256]::Create(); try { if ($f.Length -ne %d -or [BitConverter]::ToString($h.ComputeHash($f)).Replace('-','') -ne '%s') { throw 'Update artifact verification failed' } } finally { $f.Dispose(); $h.Dispose() } } catch { [Console]::Error.WriteLine($_); exit 1 }",
		strings.ReplaceAll(path, "'", "''"), identity.SizeBytes, identity.SHA256)
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, 2*len(encoded))
	for i, value := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], value)
	}
	return "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data) +
		"\r\nif %errorlevel% neq 0 exit /b 1\r\n"
}
