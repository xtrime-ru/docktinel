package docktinel

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Every task owns a process group. Cancel the group, not just its shell.
func execute(ctx context.Context, task Task, onStart func(Identity)) error {
	cmd := exec.Command(task.Command[0], task.Command[1:]...)
	cmd.Env, cmd.Dir = task.Env, task.Dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if p, err := processIdentity(cmd.Process.Pid); err == nil && onStart != nil {
		onStart(p)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// A command must not leave background children behind after exiting.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return ctx.Err()
	}
}

func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
