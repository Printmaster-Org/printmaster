package updatetrust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// AuthorizeFile authenticates both the publisher identity and the bytes of an
// already-open artifact. It does not reopen a repository-controlled pathname or
// change the file offset. The caller retains ownership of the file and must
// prevent modification until the installer has consumed that same artifact.
// Authorization alone does not make a writable package safe to install as root.
func (c *Client) AuthorizeFile(ctx context.Context, target Target, file *os.File) (Authorization, error) {
	if file == nil {
		return Authorization{}, fmt.Errorf("publisher artifact file is required")
	}
	authorization, err := c.Authorize(ctx, target)
	if err != nil {
		return Authorization{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return Authorization{}, fmt.Errorf("inspect publisher artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != target.SizeBytes {
		return Authorization{}, fmt.Errorf("publisher artifact is not a regular file of the authorized length")
	}
	hash := sha256.New()
	reader := &artifactReader{ctx: ctx, reader: io.NewSectionReader(file, 0, target.SizeBytes)}
	length, err := io.Copy(hash, reader)
	if err != nil {
		return Authorization{}, fmt.Errorf("read publisher artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Authorization{}, err
	}
	if length != target.SizeBytes || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), target.SHA256) {
		return Authorization{}, fmt.Errorf("publisher artifact length or SHA-256 differs from authenticated metadata")
	}
	after, err := file.Stat()
	if err != nil {
		return Authorization{}, fmt.Errorf("reinspect publisher artifact: %w", err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return Authorization{}, fmt.Errorf("publisher artifact changed during verification")
	}
	return authorization, nil
}

type artifactReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
