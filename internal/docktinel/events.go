package docktinel

import "log/slog"

type lifecycleState struct {
	phase string
	state string
}

// Called under mu. Use the same aggregation for snapshots and lifecycle events.
func (d *Daemon) lifecycleLocked() lifecycleState {
	s := lifecycleState{d.status.Phase, d.status.State}
	if s.phase == "runtime" {
		s.state = "healthy"
		for _, task := range d.states {
			if task.Failures > 0 {
				s.state = "degraded"
				break
			}
		}
	}
	return s
}

func logLifecycle(from, to lifecycleState, reason string) {
	if from == to {
		return
	}
	attrs := []any{"event", "container_state_changed", "from_phase", from.phase,
		"phase", to.phase, "from_state", from.state, "state", to.state, "reason", reason}
	if to.state == "degraded" || to.state == "unhealthy" {
		slog.Warn("container state changed", attrs...)
	} else {
		slog.Info("container state changed", attrs...)
	}
}

// Execution's transient running state must not flap health on every attempt.
func taskHealth(s TaskStatus) string {
	if s.Failures > 0 {
		return "degraded"
	}
	if s.State == "healthy" {
		return "healthy"
	}
	if s.health != "" && s.health != "pending" {
		return s.health
	}
	if s.Kind == "process" && s.Process != nil {
		return "running"
	}
	return "pending"
}

func logTaskTransition(before, after TaskStatus, phase string) {
	from, to := taskHealth(before), taskHealth(after)
	if from == to {
		return
	}
	message := "task state changed"
	if from == "degraded" && to == "healthy" {
		message = "task recovered"
	}
	attrs := []any{"event", "task_state_changed", "task", after.Name, "kind", after.Kind,
		"phase", phase, "from_state", from, "state", to,
		"failures", after.Failures, "reason", after.Reason}
	if to == "degraded" {
		slog.Warn(message, attrs...)
	} else {
		slog.Info(message, attrs...)
	}
}
