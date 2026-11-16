package docktinel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// ensureDaemon serializes launchers and checks the daemon's actual lock. A
// frozen API is never a reason to start a replacement beside a live daemon.
func ensureDaemon(dir string, mainPID int) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	launcher, err := os.OpenFile(filepath.Join(dir, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer launcher.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(int(launcher.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return fmt.Errorf("wait for daemon launcher: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer syscall.Flock(int(launcher.Fd()), syscall.LOCK_UN)
	var state Status
	if callAPI(dir, "GET", "/v1/status", nil, &state, 200*time.Millisecond) == nil {
		return nil
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		lock.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if err := callAPI(dir, "GET", "/v1/status", nil, &state, 5*time.Second); err != nil {
			return fmt.Errorf("daemon is running but its API is unavailable: %w", err)
		}
		return nil
	}
	// Only the daemon holds this lock permanently. Drop the probe lock before
	// starting it; competing explicit daemons are still protected by daemon.lock.
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()
	path, err := autoStartConfig()
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(binary, "--runtime-dir", dir, "daemon", "--config", path, "--main-pid", strconv.Itoa(mainPID))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if callAPI(dir, "GET", "/v1/status", nil, &state, 100*time.Millisecond) == nil {
			return nil
		}
		select {
		case err := <-exited:
			// An explicitly launched daemon may have won the lock race.
			if callAPI(dir, "GET", "/v1/status", nil, &state, 200*time.Millisecond) == nil {
				return nil
			}
			return fmt.Errorf("daemon failed to start: %v", err)
		default:
		}
	}
	return errors.New("daemon did not become ready within 5s; inspect daemon logs")
}

// Explicit daemon mode supports CLI-only registration. Automatic startup must
// have a file, and passes it explicitly so disappearance before child loading
// cannot silently turn a configured launch into an empty daemon.
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
