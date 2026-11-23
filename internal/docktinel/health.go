package docktinel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Evaluate(s Status, container string, now float64, maxAge time.Duration) (bool, string) {
	if s.Version != 1 || s.Instance == "" || s.Container != container {
		return true, "state belongs to another container or has an unsupported schema"
	}
	if s.Heartbeat <= 0 || now < s.Heartbeat || now-s.Heartbeat > maxAge.Seconds() {
		return true, "watchdog heartbeat expired or invalid"
	}
	switch s.Phase {
	case "stopping":
		if s.StopDeadline <= 0 || now > s.StopDeadline {
			return true, "shutdown deadline exceeded"
		}
		return false, "stopping: " + s.Reason
	case "configuring", "startup", "awaiting-main", "after-start":
		if s.StartupDeadline <= 0 || now > s.StartupDeadline {
			return true, "startup deadline exceeded"
		}
		return false, "starting: " + s.Phase
	case "runtime":
		// Validate runtime tasks below.
	default:
		return true, "invalid lifecycle phase: " + s.Phase
	}
	for _, task := range s.Tasks {
		if task.Critical && task.Due > 0 && now > task.Due {
			return true, "critical task overdue: " + task.Name
		}
	}
	if s.State != "healthy" && s.State != "degraded" {
		return true, "invalid runtime state: " + s.State
	}
	return false, ""
}

type stopRecord struct {
	Container string   `json:"container"`
	Target    Identity `json:"target"`
	Started   float64  `json:"started"`
}

// Escalation spans healthcheck invocations, so it never blocks Docker for the
// shutdown grace period. Never signal PID 1, another container, or a reused PID.
func emergencyStop(dir string, container string, target Identity, now float64, grace time.Duration) error {
	if !sameProcess(target) {
		return errors.New("no verified main process to stop")
	}
	lock, err := tryFileLock(filepath.Join(dir, "health-stop.lock"))
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	path := filepath.Join(dir, "health-stop.json")
	record, _ := readJSON[stopRecord](path)
	if record.Container != container || record.Target != target || record.Started <= 0 {
		record = stopRecord{Container: container, Target: target, Started: now}
		if err := writeJSON(path, record); err != nil {
			return err
		}
		return signalProcess(target, syscall.SIGTERM)
	}
	if now-record.Started >= grace.Seconds() {
		return signalProcess(target, syscall.SIGKILL)
	}
	return nil
}

func Healthcheck(dir string, maxAge, stopGrace, missingGrace time.Duration, noKill bool) error {
	return healthcheck(dir, maxAge, stopGrace, missingGrace, noKill, uptime)
}

func healthcheck(dir string, maxAge, stopGrace, missingGrace time.Duration, noKill bool, readUptime func() (float64, error)) error {
	container, err := containerIdentity()
	if err != nil {
		return err
	}
	// The daemon can publish a new heartbeat while the snapshot is being read.
	s, statusErr := readStatus(dir)
	now, err := readUptime()
	if err != nil {
		return err
	}
	if statusErr != nil {
		identity, _ := readJSON[stopRecord](filepath.Join(dir, "identity.json"))
		if identity.Container == container && sameProcess(identity.Target) {
			if !noKill {
				_ = emergencyStop(dir, container, identity.Target, now, stopGrace)
			}
			return fmt.Errorf("watchdog state is missing or invalid: %w", statusErr)
		}
		// PID 1's direct child is the configured entrypoint/application under
		// tini. A live, local identity is needed even if status.json is lost.
		if age, ageErr := containerAge(now); ageErr == nil && age < missingGrace.Seconds() {
			return errors.New("starting: state not published yet")
		}
		if !noKill {
			if target, targetErr := initChild(); targetErr == nil {
				_ = emergencyStop(dir, container, target, now, stopGrace)
			}
		}
		return fmt.Errorf("cannot read watchdog state: %w", statusErr)
	}
	if s.Container != container {
		if age, ageErr := containerAge(now); ageErr == nil && age < missingGrace.Seconds() {
			return errors.New("starting: ignoring state from another container")
		}
	}
	fatal, reason := Evaluate(s, container, now, maxAge)
	if !sameProcess(s.Daemon) {
		fatal, reason = true, "watchdog process is gone or replaced"
	}
	if !fatal && s.Phase == "runtime" {
		for _, task := range s.Tasks {
			if task.Critical && task.Kind == "process" && task.Process != nil && !sameProcess(*task.Process) {
				fatal, reason = true, "critical process disappeared: "+task.Name
			}
		}
	}
	if fatal && !noKill {
		// An old container's state is never an authority for signalling.
		target := s.Main
		if s.Container != container || !sameProcess(target) {
			target, _ = initChild()
		}
		if stopErr := emergencyStop(dir, container, target, now, stopGrace); stopErr != nil {
			return fmt.Errorf("%s (stop: %v)", reason, stopErr)
		}
	}
	if reason != "" {
		return errors.New(reason)
	}
	return nil
}

// Linux exposes process start time in USER_HZ units (100 ticks per second).
func containerAge(now float64) (float64, error) {
	p, err := processIdentity(1)
	if err != nil {
		return 0, err
	}
	ticks, err := strconv.ParseFloat(p.Start, 64)
	if err != nil {
		return 0, err
	}
	return now - ticks/100, nil
}

func initChild() (Identity, error) {
	b, err := os.ReadFile("/proc/1/task/1/children")
	if err != nil {
		return Identity{}, err
	}
	// Multiple direct children can include adopted workers. Only a single
	// child is unambiguous; otherwise require the published main identity.
	children := strings.Fields(string(b))
	if len(children) != 1 {
		return Identity{}, errors.New("cannot identify a unique init child")
	}
	pid, err := strconv.Atoi(children[0])
	if err != nil {
		return Identity{}, err
	}
	return processIdentity(pid)
}
