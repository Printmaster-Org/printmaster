package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const packageHelperResultPath = "/var/lib/printmaster-update-results/current.json"

// This root-written, non-secret observation is readable by the service user.
// It never authorizes install/retry or replaces the private transaction journal.
func writeRepositoryPackageObservation(path string, transaction RepositoryPackageTransaction) (result error) {
	if os.Geteuid() != 0 {
		return fmt.Errorf("native package observations require root")
	}
	parent := filepath.Dir(path)
	if err := checkRootPackageAncestors(filepath.Dir(parent)); err != nil {
		return err
	}
	if err := os.Mkdir(parent, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := checkRootPackageAncestors(parent); err != nil {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode().Perm() != 0o755 {
		return errors.Join(fmt.Errorf("native observation directory must have mode 0755"), err)
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".observation-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeErr := file.Write(data)
	modeErr := file.Chmod(0o644)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if err := syncPackageDirectory(parent); err != nil {
		return err
	}
	return syncPackageDirectory(filepath.Dir(parent))
}

func readRepositoryPackageObservation(path string) (RepositoryPackageTransaction, error) {
	data, err := readProtectedPackageFile(path, 16*1024)
	if err != nil {
		return RepositoryPackageTransaction{}, err
	}
	return decodePackageTransaction(data, path)
}
