//go:build !windows

package openssh

import (
	"io"
	"os"
)

func Guard(*os.Process) (io.Closer, error) { return nopGuard{}, nil }

type nopGuard struct{}

func (nopGuard) Close() error { return nil }
