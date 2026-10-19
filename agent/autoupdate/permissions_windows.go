//go:build windows

package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func SecureUpdaterDirectories(dataDir string) error {
	return secureUpdaterDirectories(dataDir)
}

func secureUpdaterDirectories(dataDir string) error {
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absDataDir)
	if parent != filepath.VolumeName(absDataDir)+string(os.PathSeparator) {
		if err := secureWindowsDirectory(parent); err != nil {
			return fmt.Errorf("protect updater data parent: %w", err)
		}
	}
	if err := secureWindowsDirectory(absDataDir); err != nil {
		return fmt.Errorf("protect updater data directory: %w", err)
	}

	stateDir := filepath.Join(absDataDir, updateDirName)
	for _, dir := range []string{
		stateDir,
		filepath.Join(stateDir, stagingDirName),
		filepath.Join(stateDir, backupDirName),
		filepath.Join(stateDir, downloadDirName),
		filepath.Join(absDataDir, "update-trust"),
		filepath.Join(absDataDir, "update-trust", "metadata"),
	} {
		if err := secureWindowsDirectory(dir); err != nil {
			return fmt.Errorf("protect updater directory %s: %w", dir, err)
		}
	}
	if err := secureWindowsTree(stateDir); err != nil {
		return fmt.Errorf("protect existing updater state: %w", err)
	}
	return secureWindowsTree(filepath.Join(absDataDir, "update-trust"))
}

func secureWindowsDirectory(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkWindowsAncestors(absPath); err != nil {
		return err
	}
	if err := os.MkdirAll(absPath, 0o700); err != nil {
		return err
	}
	return setPrivateWindowsACL(absPath)
}

func secureWindowsTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("updater state contains a reparse point: %s", path)
		}
		if entry.IsDir() != (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
			return fmt.Errorf("updater state entry changed type: %s", path)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("updater state contains a non-regular entry: %s", path)
			}
		}
		return setPrivateWindowsACL(path)
	})
}

func setPrivateWindowsACL(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	owner := user.User.Sid
	if owner.Equals(systemSID) {
		owner = systemSID
	} else if isAdmin, err := token.IsMember(adminSID); err != nil {
		return err
	} else if isAdmin {
		owner = adminSID
	}

	sddl := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
}

func checkWindowsAncestors(path string) error {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return fmt.Errorf("path has no volume")
	}
	current := volume + string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimLeft(path[len(current):], `\/`), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(current))
		if err != nil {
			if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
				return nil
			}
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
			attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return fmt.Errorf("path ancestor is not an ordinary directory: %s", current)
		}
	}
	return nil
}

func validateInstallPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkWindowsAncestors(filepath.Dir(absPath)); err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(absPath))
	if err != nil {
		return err
	}
	if attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fmt.Errorf("installation target is not an ordinary file")
	}
	return validateWindowsPathPermissions(absPath)
}

func validateWindowsPathPermissions(filePath string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	trustedInstallerSID, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464")
	if err != nil {
		return err
	}

	trusted := func(sid *windows.SID) bool {
		return sid.Equals(user.User.Sid) || sid.Equals(systemSID) ||
			sid.Equals(adminSID) || sid.Equals(trustedInstallerSID)
	}
	paths := []string{filePath}
	for dir := filepath.Dir(filePath); ; dir = filepath.Dir(dir) {
		paths = append(paths, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, path := range paths {
		if err := checkWindowsObjectPermissions(
			path, trusted, path != filePath, path == filepath.Dir(filePath),
		); err != nil {
			return err
		}
	}
	return nil
}

func checkWindowsObjectPermissions(path string, trusted func(*windows.SID) bool, directory, targetParent bool) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !trusted(owner) {
		return fmt.Errorf("path has an untrusted owner: %s", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("path has a missing or permissive DACL: %s", path)
	}
	writeMask := windows.ACCESS_MASK(windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE)
	if directory {
		writeMask |= windows.ACCESS_MASK(0x0040)
		if targetParent {
			writeMask |= windows.ACCESS_MASK(0x0002)
		}
		if filepath.Dir(path) == path {
			writeMask &^= windows.DELETE
		}
	} else {
		writeMask |= windows.GENERIC_WRITE | windows.FILE_WRITE_DATA |
			windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA
	}
	const inheritOnlyACE = 0x08
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, 2, 3:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if ace.Header.AceFlags&inheritOnlyACE != 0 || ace.Mask&writeMask == 0 {
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !trusted(sid) {
				return fmt.Errorf("path grants write or replacement rights to an untrusted identity: %s", path)
			}
		default:
			if ace.Mask&writeMask != 0 {
				return fmt.Errorf("path contains an unsupported writable ACL entry: %s", path)
			}
		}
	}
	return nil
}

func validateServiceInstallPath(path string) error {
	expected := filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
	if filepath.VolumeName(expected) == "" {
		return fmt.Errorf("ProgramFiles is not configured")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(absPath), filepath.Clean(expected)) {
		return fmt.Errorf("Windows service executable must be installed at %s", expected)
	}
	return nil
}

func secureWindowsServiceExecutable(path string) error {
	if err := validateServiceInstallPath(path); err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := secureWindowsDirectory(filepath.Dir(absPath)); err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(absPath))
	if err != nil {
		return err
	}
	if attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fmt.Errorf("Windows service executable is not an ordinary file")
	}
	return setPrivateWindowsACL(absPath)
}
