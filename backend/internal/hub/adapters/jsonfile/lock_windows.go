//go:build windows

package jsonfile

import (
	"context"
	"os"
)

var processLock = make(chan struct{}, 1)

func init() { processLock <- struct{}{} }

func lockFile(ctx context.Context, _ *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-processLock:
		return nil
	}
}

func unlockFile(*os.File) error {
	processLock <- struct{}{}
	return nil
}
