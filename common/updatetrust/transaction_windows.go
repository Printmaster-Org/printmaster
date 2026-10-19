package updatetrust

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func checkPublisherDirectory(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("publisher directory is a reparse point: %s", path)
	}
	return nil
}
