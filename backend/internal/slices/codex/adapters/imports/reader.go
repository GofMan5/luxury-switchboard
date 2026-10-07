package imports

import (
	"errors"
	"os"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// errFileUnreadable is the reader's refusal for any file the OS would
// not hand over: missing, locked, or a directory. The underlying cause
// carries the full path the picker returned, so it stays here — the
// service rewords failures with base file names only.
var errFileUnreadable = errors.New("codex auth file could not be read")

// FileReader reads credential files the desktop picker returned,
// implementing the AuthFileReader port.
//
// The read is bounded by the protocol frame cap: an auth file larger
// than the frame the shell accepts is refused rather than read. A real
// auth.json is a few kilobytes, so anything past the cap is not a
// credential document, and reading it whole would hold an unbounded
// buffer for no importable content.
type FileReader struct{}

// NewFileReader returns the reader the composition root hands the
// service.
func NewFileReader() FileReader { return FileReader{} }

// ReadAuthFile implements application.AuthFileReader. It returns the
// file's bytes as text or an error that names no path and no contents.
func (FileReader) ReadAuthFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errFileUnreadable
	}
	if len(data) > platform.MaxFrameBytes {
		return "", application.ErrAuthFileTooLarge
	}
	return string(data), nil
}
