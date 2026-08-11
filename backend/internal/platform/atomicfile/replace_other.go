//go:build !windows

package atomicfile

import (
	"os"
	"path/filepath"
)

func commit(fromPath, toPath string) error {
	if err := os.Rename(fromPath, toPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(toPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
