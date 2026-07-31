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
	if temporary.Chmod(mode) != nil || writeAndSync(temporary, payload) != nil {
		_ = temporary.Close()
		return errors.New("temporary state write failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("temporary state close failed")
	}
	if err := commit(name, path); err != nil {
		return errors.New("atomic state replace failed")
	}
	committed = true
	return nil
}

func writeAndSync(file *os.File, payload []byte) error {
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}
