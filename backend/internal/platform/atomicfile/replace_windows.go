//go:build windows

package atomicfile

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// commitAttempts bounds the wait for a transient holder of the target — an
// antivirus scan, the search indexer, a concurrent reader — that briefly
// opened it without delete sharing. About 150 ms in total.
const commitAttempts = 6

func commit(fromPath, toPath string) error {
	from, err := windows.UTF16PtrFromString(fromPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(toPath)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		transient := errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
		if !transient || attempt == commitAttempts {
			return err
		}
		time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
	}
}
