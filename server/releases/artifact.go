package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"printmaster/server/storage"
)

// validateCachedArtifact checks bytes, not merely a database row or pathname.
// Availability checks must not advertise a missing or corrupted cached release.
func validateCachedArtifact(ctx context.Context, artifact *storage.ReleaseArtifact) error {
	if artifact == nil || artifact.SizeBytes <= 0 || len(artifact.SHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid cached artifact metadata")
	}
	file, err := os.Open(artifact.CachePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.SizeBytes {
		return fmt.Errorf("cached artifact size/type mismatch")
	}
	hash := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(buf)
		if n > 0 {
			_, _ = hash.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		return fmt.Errorf("cached artifact checksum mismatch")
	}
	return nil
}
