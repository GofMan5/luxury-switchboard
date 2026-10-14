package imports

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

func TestTheReaderReturnsAFilesTextAsItWas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("{\"tokens\":{}}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	text, err := FileReader{}.ReadAuthFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if text != "{\"tokens\":{}}" {
		t.Fatalf("text = %q, want the file's bytes untouched", text)
	}
}

func TestTheReaderRefusesAMissingFileWithoutNamingIt(t *testing.T) {
	// The picker hands back paths, but an error that echoed one would
	// carry a local layout into the UI; the refusal stays generic and
	// the service rewords it around the base name.
	path := filepath.Join(t.TempDir(), "not-there.json")

	_, err := FileReader{}.ReadAuthFile(path)
	if err == nil {
		t.Fatal("a missing file was read")
	}
	if !errors.Is(err, errFileUnreadable) {
		t.Fatalf("error = %v, want the unreadable refusal", err)
	}
	if err.Error() != "codex auth file could not be read" {
		t.Fatalf("error = %q, want a refusal that names no path", err.Error())
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("error leaks the picked path: %q", err.Error())
	}
}

func TestTheReaderRefusesADirectoryWithoutNamingIt(t *testing.T) {
	_, err := FileReader{}.ReadAuthFile(t.TempDir())
	if err == nil {
		t.Fatal("a directory was read as a file")
	}
	if !errors.Is(err, errFileUnreadable) {
		t.Fatalf("error = %v, want the unreadable refusal", err)
	}
}

func TestTheReaderBoundsAllocationBeforeReadingAnOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Small bounded fixture, not an OOM test; the old reader allocates its
	// full 4 MiB while the capped reader needs only cap+1 bytes.
	if err := file.Truncate(4 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = FileReader{}.ReadAuthFile(path)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, application.ErrAuthFileTooLarge) {
		t.Fatalf("oversized read: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("oversized read allocated %d bytes before refusal", allocated)
	}
	for _, size := range []int{platform.MaxFrameBytes - 1, platform.MaxFrameBytes, platform.MaxFrameBytes + 1} {
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		text, err := FileReader{}.ReadAuthFile(path)
		if size > platform.MaxFrameBytes {
			if !errors.Is(err, application.ErrAuthFileTooLarge) || text != "" {
				t.Fatal("overflow did not fail closed")
			}
		} else if err != nil || len(text) != size {
			t.Fatalf("size %d refused: %v", size, err)
		}
	}
}

func TestTheReaderRefusesAFilePastTheFrameCap(t *testing.T) {
	// A real auth.json is a few kilobytes; anything past the frame the
	// shell accepts is not a credential document, and reading it whole
	// would hold an unbounded buffer for no importable content.
	path := filepath.Join(t.TempDir(), "huge.json")
	if err := os.WriteFile(path, make([]byte, platform.MaxFrameBytes+1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := FileReader{}.ReadAuthFile(path)
	if !errors.Is(err, application.ErrAuthFileTooLarge) {
		t.Fatalf("error = %v, want the oversized refusal", err)
	}
}
