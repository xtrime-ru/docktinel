package docktinel

import (
	"context"
	"errors"
	"os"
	"syscall"
)

// Closing the returned file releases its advisory lock. Never unlink lock
// files: contenders must continue to lock the same inode.
func tryFileLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func waitFileLock(ctx context.Context, path string) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := tryFileLock(path)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return file, err
		}
		if !delay(ctx, retryInterval) {
			return nil, ctx.Err()
		}
	}
}
