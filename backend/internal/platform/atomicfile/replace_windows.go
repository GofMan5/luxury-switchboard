//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func Replace(path string, payload []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("state directory could not be created")
	}
	temporary, err := os.CreateTemp(directory, ".switchboard-*.tmp")
	if err != nil {
		return errors.New("temporary state file could not be created")
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return errors.New("temporary state permissions failed")
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return errors.New("temporary state write failed")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("temporary state sync failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("temporary state close failed")
	}
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return errors.New("temporary state path is invalid")
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errors.New("state path is invalid")
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return errors.New("atomic state replace failed")
	}
	committed = true
	return nil
}
