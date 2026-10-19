package updatetrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublisherMetadataReadBounds(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if _, err := readPublisherMetadata(directory); err == nil {
		t.Fatal("directory accepted as publisher metadata")
	}
	path := filepath.Join(directory, "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxMetadataBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readPublisherMetadata(path); err == nil {
		t.Fatal("oversized backup metadata accepted")
	}
}
