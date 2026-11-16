package docktinel

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func (d *Daemon) runPhase(phase string, tasks []Task) error {
	d.mu.Lock()
	deadline := d.status.StartupDeadline
	d.mu.Unlock()
	now, err := uptime()
	if err != nil {
		return err
	}
	phaseCtx, phaseCancel := context.WithTimeout(d.ctx, time.Duration((deadline-now)*float64(time.Second)))
	defer phaseCancel()
	for _, task := range tasks {
		if err := phaseCtx.Err(); err != nil {
			return err
		}
		if task.Phase != phase && !(phase == "startup" && task.Phase == "both") {
			continue
		}
		if task.Kind == "process" {
			d.launch(task)
			continue
		}
		err := d.runStartupTask(phaseCtx, task)
		if phaseCtx.Err() != nil {
			return phaseCtx.Err()
		}
		if err != nil {
			d.markFailure(task, err)
			if task.critical() {
				return fmt.Errorf("%s task %s failed: %w", phase, task.Name, err)
			}
		}
	}
	return phaseCtx.Err()
}

// Startup and after-start tasks share retry semantics and a per-task deadline.
func (d *Daemon) runStartupTask(parent context.Context, task Task) error {
	ctx, cancel := context.WithTimeout(parent, task.Deadline)
	defer cancel()
	pause := task.Interval
	if pause == 0 {
		pause = time.Second
	}
	for {
		err := d.attempt(ctx, task)
		if err == nil && (task.Kind != "check" || d.checkConfirmed(task)) {
			return nil
		}
		if ctx.Err() != nil || !delay(ctx, pause) {
			if err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

func (d *Daemon) update(name string, fn func(*TaskStatus)) {
	d.mu.Lock()
	beforeLife := d.lifecycleLocked()
	before := d.states[name]
	after := before
	after.health = taskHealth(before)
	fn(&after)
	after.health = taskHealth(after)
	d.states[name] = after
	afterLife := d.lifecycleLocked()
	d.mu.Unlock()
	// Emit outside the state lock; stdout backpressure must not lock the API.
	logTaskTransition(before, after, afterLife.phase)
	logLifecycle(beforeLife, afterLife, "task "+name)
}

func (d *Daemon) launch(task Task) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.launchLocked(task)
}

// Schedule and record the task atomically with lifecycle transitions, so a
// runtime snapshot cannot expose an unscheduled critical task with no deadline.
func (d *Daemon) launchLocked(task Task) {
	if d.status.Phase == "stopping" {
		return
	}
	now, _ := uptime()
	s := d.states[task.Name]
	s.Due = now + task.Grace.Seconds() + task.Timeout.Seconds() + d.cfg.Heartbeat.Seconds() + 5
	d.states[task.Name] = s
	d.workers.Go(func() { d.runTask(task) })
}

func (d *Daemon) attempt(parent context.Context, task Task) error {
	_, err := d.attemptRun(parent, task)
	return err
}

func (d *Daemon) attemptRun(parent context.Context, task Task) (elapsed time.Duration, err error) {
	ctx := parent
	cancel := func() {}
	if task.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, task.Timeout)
	}
	defer cancel()
	now, _ := uptime()
	d.update(task.Name, func(s *TaskStatus) {
		s.State = "running"
		s.LastStarted = now
		s.Process = nil
		if task.Timeout > 0 {
			s.Due = now + task.Timeout.Seconds() + d.cfg.Heartbeat.Seconds() + 3
		} else {
			s.Due = 0
		}
	})
	defer func() {
		now, _ := uptime()
		d.update(task.Name, func(s *TaskStatus) {
			s.LastCompleted, s.Process, s.Due = now, nil, 0
			if err != nil {
				s.Successes = 0
				return
			}
			// Any success breaks the failure streak, but recovery requires N
			// consecutive successes. Keep the previous reason until confirmed.
			s.Failures = 0
			if task.Kind == "check" {
				if s.Successes < task.Successes {
					s.Successes++
				}
				if s.Successes < task.Successes {
					s.State = "confirming"
					return
				}
			}
			s.State, s.Reason = "healthy", ""
		})
	}()
	if task.Lock != "" {
		lock := d.locks[task.Lock]
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return execute(ctx, task, func(p Identity) { d.update(task.Name, func(s *TaskStatus) { s.Process = &p }) })
}

func (d *Daemon) markFailure(task Task, err error) int {
	count := 0
	d.update(task.Name, func(s *TaskStatus) {
		s.Failures++
		s.Successes = 0
		count = s.Failures
		s.State = "degraded"
		s.Reason = err.Error()
		s.Process = nil
	})
	// Do not log command arguments or environment: they may contain credentials.
	slog.Warn("task failed", "task", task.Name, "failures", count, "error", err)
	return count
}

func (d *Daemon) runTask(task Task) {
	if !delay(d.ctx, task.Grace) {
		return
	}
	backoff := retryBackoff{}
	for d.ctx.Err() == nil {
		elapsed, err := d.attemptRun(d.ctx, task)
		if d.ctx.Err() != nil {
			return
		}
		failures := 0
		if err != nil {
			failures = d.markFailure(task, err)
		}
		if task.Kind == "process" && task.critical() {
			d.stop("process exited: "+task.Name, true)
			return
		}
		if err != nil && task.critical() && failures >= task.Failures {
			d.stop("task failed: "+task.Name, true)
			return
		}
		if err == nil && task.Kind == "job" && task.Interval == 0 {
			return
		}
		pause := backoff.next(task, elapsed, err != nil)
		now, _ := uptime()
		d.update(task.Name, func(s *TaskStatus) {
			s.Due = now + pause.Seconds() + task.Timeout.Seconds() + d.cfg.Heartbeat.Seconds() + 3
		})
		if !delay(d.ctx, pause) {
			return
		}
	}
}

func (d *Daemon) checkConfirmed(task Task) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.states[task.Name].Successes >= task.Successes
}
