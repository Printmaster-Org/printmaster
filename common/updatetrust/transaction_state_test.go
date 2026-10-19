package updatetrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedPublicationDetectsWorkspaceRollbackAgainstRecords(t *testing.T) {
	t.Parallel()
	repo, opts := transactionFixture(t)
	original := readPublication(t, repo.dir, "timestamp.json")
	if err := PublishTransaction(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.dir, "timestamp.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishTransaction(opts); err == nil {
		t.Fatal("workspace rollback ignored despite intact record history")
	}
	if _, err := os.Stat(filepath.Join(repo.dir, ".pending-publication")); !os.IsNotExist(err) {
		t.Fatal("rollback check should occur before preparing another version")
	}
}
