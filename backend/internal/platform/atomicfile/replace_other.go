//go:build !windows

package atomicfile

import "os"

func commit(fromPath, toPath string) error { return os.Rename(fromPath, toPath) }
