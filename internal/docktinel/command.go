package docktinel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Every task owns a process group. Cancel the group, not just its shell.
func execute(ctx context.Context, task Task, onStart func(Identity)) (time.Duration, error) {
	path, err := taskCommandPath(task)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(path, task.Command[1:]...)
	cmd.Args[0] = task.Command[0]
	cmd.Env, cmd.Dir = task.Env, task.Dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// All exit paths clean up children left in the command's process group.
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	started := time.Now()
	if p, err := processIdentity(cmd.Process.Pid); err == nil && onStart != nil {
		onStart(p)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return time.Since(started), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(commandStopGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return time.Since(started), ctx.Err()
	}
}

// Resolve PATH in the task's captured environment, never the daemon's. Absolute
// candidates also make relative PATH entries use the task's working directory.
func taskCommandPath(task Task) (string, error) {
	name := task.Command[0]
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	env := task.Env
	if env == nil {
		env = os.Environ()
	}
	path := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	cwd, err := filepath.Abs(task.Dir)
	if err != nil {
		return "", err
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		if candidate, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func delay(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
