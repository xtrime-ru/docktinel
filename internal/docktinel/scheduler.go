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
		ctx, cancel := context.WithTimeout(phaseCtx, task.Deadline)
		var err error
		for {
			err = d.attempt(ctx, task)
			if err == nil || ctx.Err() != nil {
				break
			}
			pause := task.Interval
			if pause == 0 {
				pause = time.Second
			}
			if !delay(ctx, pause) {
				break
			}
		}
		cancel()
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

func (d *Daemon) update(name string, fn func(*TaskStatus)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.states[name]
	fn(&s)
	d.states[name] = s
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

func (d *Daemon) attempt(parent context.Context, task Task) (err error) {
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
			if err == nil {
				s.State, s.Reason, s.Failures = "healthy", "", 0
			}
		})
	}()
	if task.Lock != "" {
		lock := d.locks[task.Lock]
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return execute(ctx, task, func(p Identity) { d.update(task.Name, func(s *TaskStatus) { s.Process = &p }) })
}

func (d *Daemon) markFailure(task Task, err error) int {
	count := 0
	d.update(task.Name, func(s *TaskStatus) {
		s.Failures++
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
	retries := 0
	for d.ctx.Err() == nil {
		err := d.attempt(d.ctx, task)
		if d.ctx.Err() != nil {
			return
		}
		failures := 0
		if err != nil {
			retries++
			failures = d.markFailure(task, err)
		} else {
			retries = 0
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
		pause := task.Interval
		switch {
		case err != nil && task.Kind != "check":
			pause = min(time.Duration(10*retries)*time.Second, time.Minute)
		case task.Kind == "process":
			pause = 100 * time.Millisecond
		}
		now, _ := uptime()
		d.update(task.Name, func(s *TaskStatus) {
			s.Due = now + pause.Seconds() + task.Timeout.Seconds() + d.cfg.Heartbeat.Seconds() + 3
		})
		if !delay(d.ctx, pause) {
			return
		}
	}
}
