//go:build !windows

package main

import (
	"fmt"
	"os"
)

func makePrivateDirectory(path string) error {
	return os.Mkdir(path, 0o700)
}

func checkDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output ancestor is not an ordinary directory: %s", path)
	}
	return nil
}
