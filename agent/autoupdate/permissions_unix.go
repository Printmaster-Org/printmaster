//go:build !windows

package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func SecureUpdaterDirectories(dataDir string) error {
	return secureUpdaterDirectories(dataDir)
}

func secureUpdaterDirectories(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create updater data directory: %w", err)
	}
	if err := secureUnixDirectory(dataDir); err != nil {
		return fmt.Errorf("protect updater data directory: %w", err)
	}

	for _, dir := range []string{
		filepath.Join(dataDir, updateDirName),
		filepath.Join(dataDir, updateDirName, stagingDirName),
		filepath.Join(dataDir, updateDirName, backupDirName),
		filepath.Join(dataDir, updateDirName, downloadDirName),
		filepath.Join(dataDir, "update-trust"),
		filepath.Join(dataDir, "update-trust", "metadata"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create protected updater directory %s: %w", dir, err)
		}
		if err := secureUnixDirectory(dir); err != nil {
			return fmt.Errorf("protect updater directory %s: %w", dir, err)
		}
	}

	if err := secureUnixTree(filepath.Join(dataDir, updateDirName)); err != nil {
		return err
	}
	return secureUnixTree(filepath.Join(dataDir, "update-trust"))
}

func secureUnixDirectory(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkUnixAncestors(absPath); err != nil {
		return err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path is not an ordinary directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("directory is not owned by the updater process")
	}
	return os.Chmod(absPath, 0o700)
}

func checkUnixAncestors(path string) error {
	volume := filepath.VolumeName(path)
	current := volume + string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(path, current), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("path ancestor is not an ordinary directory: %s", current)
		}
		if info.Mode().Perm()&0o022 != 0 {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSticky == 0 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
				return fmt.Errorf("path ancestor is writable by an untrusted group or user: %s", current)
			}
		}
	}
	return nil
}

func secureUnixTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("updater state contains a symbolic link: %s", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("updater state entry is not owned by the updater process: %s", path)
		}
		switch {
		case entry.IsDir():
			return os.Chmod(path, 0o700)
		case info.Mode().IsRegular():
			if stat.Nlink != 1 {
				return fmt.Errorf("updater state file has multiple hard links: %s", path)
			}
			return os.Chmod(path, 0o600)
		default:
			return fmt.Errorf("updater state contains a non-regular entry: %s", path)
		}
	})
}

func validateInstallPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkUnixAncestors(filepath.Dir(absPath)); err != nil {
		return err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("installation target is not an ordinary file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("installation target has an unsafe file identity")
	}
	if stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return fmt.Errorf("installation target is not owned by a trusted identity")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("installation target is writable by an untrusted group or user")
	}
	return nil
}

func validateServiceInstallPath(string) error {
	return nil
}

func secureWindowsServiceExecutable(string) error {
	return nil
}
