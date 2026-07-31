//go:build windows

package jsonfile

import (
	"os"
	"sync"
)

var processLock sync.Mutex

func lockFile(*os.File) error   { processLock.Lock(); return nil }
func unlockFile(*os.File) error { processLock.Unlock(); return nil }
