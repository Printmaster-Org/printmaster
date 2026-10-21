//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func windowsServiceExecutablePath() string {
	return filepath.Join(os.Getenv("ProgramFiles"), "PrintMaster", "Agent", "printmaster-agent.exe")
}

func prepareWindowsServiceExecutable() error {
	target := windowsServiceExecutablePath()
	if filepath.VolumeName(target) == "" {
		return fmt.Errorf("ProgramFiles is not configured")
	}
	if err := protectWindowsServiceDirectory(filepath.Dir(target)); err != nil {
		return fmt.Errorf("protect service executable directory: %w", err)
	}

	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running Agent executable: %w", err)
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(source), filepath.Clean(target)) {
		return protectWindowsServiceFile(target)
	}

	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	sourceHandle, err := windows.CreateFile(
		sourceName, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return fmt.Errorf("open source Agent executable without write sharing: %w", err)
	}
	var sourceInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(sourceHandle, &sourceInfo); err != nil {
		windows.CloseHandle(sourceHandle)
		return err
	}
	if sourceInfo.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		sourceInfo.NumberOfLinks != 1 {
		windows.CloseHandle(sourceHandle)
		return fmt.Errorf("source Agent executable must be ordinary and have exactly one hard link")
	}

	sourceFile := os.NewFile(uintptr(sourceHandle), source)
	if sourceFile == nil {
		windows.CloseHandle(sourceHandle)
		return fmt.Errorf("open source Agent executable handle")
	}
	defer sourceFile.Close()
	temp, err := os.CreateTemp(filepath.Dir(target), ".printmaster-agent-*.tmp")
	if err != nil {
		return fmt.Errorf("create protected service executable staging file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, sourceFile); err != nil {
		temp.Close()
		return fmt.Errorf("copy service executable: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush service executable: %w", err)
	}
	if err := temp.Chmod(0o755); err != nil {
		temp.Close()
		return fmt.Errorf("set service executable mode: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close service executable: %w", err)
	}
	if err := windows.MoveFileEx(
		windows.StringToUTF16Ptr(tempPath),
		windows.StringToUTF16Ptr(target),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return fmt.Errorf("install protected service executable: %w", err)
	}
	return protectWindowsServiceFile(target)
}

func protectWindowsServiceDirectory(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := checkWindowsServiceAncestors(absPath); err != nil {
		return err
	}
	if err := os.MkdirAll(absPath, 0o700); err != nil {
		return err
	}
	return setPrivateWindowsSecurity(path)
}

func protectWindowsServiceFile(path string) error {
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		info.NumberOfLinks != 1 {
		return fmt.Errorf("service executable must be ordinary and have exactly one hard link")
	}
	return setPrivateWindowsSecurity(path)
}

func setPrivateWindowsSecurity(path string) error {
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
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if owner.Equals(systemSID) {
		owner = systemSID
	} else if isAdmin, err := token.IsMember(adminSID); err != nil {
		return err
	} else if isAdmin {
		owner = adminSID
	} else {
		sddl = "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	}
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

func checkWindowsServiceAncestors(path string) error {
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
			return fmt.Errorf("service path ancestor is not an ordinary directory: %s", current)
		}
	}
	return nil
}
