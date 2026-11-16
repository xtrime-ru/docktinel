package docktinel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const daemonProbeTimeout = 200 * time.Millisecond

// Return the connected daemon's status so startup need not query it again.
// A frozen API is never a reason to start a second daemon beside a live one.
func ensureDaemon(dir string, mainPID int) (Status, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Status{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	launcher, err := waitFileLock(ctx, filepath.Join(dir, "launch.lock"))
	if err != nil {
		return Status{}, fmt.Errorf("wait for daemon launcher: %w", err)
	}
	defer launcher.Close()
	if state, err := daemonStatus(dir, min(daemonProbeTimeout, time.Until(deadline))); err == nil {
		return state, nil
	}
	lock, err := tryFileLock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return Status{}, err
		}
		state, err := daemonStatus(dir, time.Until(deadline))
		if err != nil {
			return Status{}, fmt.Errorf("daemon is running but its API is unavailable: %w", err)
		}
		return state, nil
	}
	// The daemon owns its lock permanently. Release the probe lock before
	// starting it; competing explicit daemons still use that same lock.
	lock.Close()
	path, err := autoStartConfig()
	if err != nil {
		return Status{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return Status{}, err
	}
	cmd := exec.Command(binary, "--runtime-dir", dir, "daemon", "--config", path, "--main-pid", strconv.Itoa(mainPID))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if err := cmd.Start(); err != nil {
		return Status{}, err
	}
	return waitDaemon(ctx, dir, cmd)
}

func waitDaemon(ctx context.Context, dir string, cmd *exec.Cmd) (Status, error) {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline, _ := ctx.Deadline()
	for ctx.Err() == nil {
		timeout := min(daemonProbeTimeout, time.Until(deadline))
		if state, err := daemonStatus(dir, timeout); err == nil {
			return state, nil
		}
		select {
		case err := <-exited:
			// An explicitly launched daemon may have won the lock race.
			if state, probeErr := daemonStatus(dir, min(daemonProbeTimeout, time.Until(deadline))); probeErr == nil {
				return state, nil
			}
			return Status{}, fmt.Errorf("daemon exited before its API became ready: %v", err)
		default:
		}
		if !delay(ctx, retryInterval) {
			break
		}
	}
	return Status{}, fmt.Errorf("daemon did not become ready: %w", ctx.Err())
}

// Explicit daemon mode supports CLI-only registration. Automatic startup must
// have a file; passing its path explicitly makes it required in the child too.
func autoStartConfig() (string, error) {
	path, _ := configPath("")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot auto-start daemon: config %s: %w; provide a config or start docktinel daemon explicitly and register tasks with add", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("cannot auto-start daemon: config %s must be a regular file", path)
	}
	return path, nil
}
