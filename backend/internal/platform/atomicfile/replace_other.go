//go:build !windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
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
	name := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(name)
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
	if err := os.Rename(name, path); err != nil {
		return errors.New("atomic state replace failed")
	}
	committed = true
	return nil
}
