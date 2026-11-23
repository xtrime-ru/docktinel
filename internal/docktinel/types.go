package docktinel

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	DefaultDir            = "/run/docktinel"
	defaultStartupTimeout = 2 * time.Minute
	apiTimeout            = 5 * time.Second
	retryInterval         = 25 * time.Millisecond
	commandStopGrace      = 2 * time.Second
	maxJSONSize           = 1 << 20
)

var taskName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// Task is a configured check, job, or process. Commands are argv.
type Task struct {
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	Phase     string        `json:"phase"`
	Command   []string      `json:"command"`
	Env       []string      `json:"-"`
	Dir       string        `json:"-"`
	Interval  time.Duration `json:"interval"`
	Timeout   time.Duration `json:"timeout"`
	Deadline  time.Duration `json:"deadline"`
	Grace     time.Duration `json:"grace"`
	Failures  int           `json:"failures"`
	Successes int           `json:"successes"`
	Failure   string        `json:"failure"`
	OnExit    string        `json:"on_exit"`
	Lock      string        `json:"lock,omitempty"`
}

func taskDefaults(kind, name string) Task {
	t := Task{Name: name, Kind: kind, Phase: "runtime", Timeout: time.Minute, Deadline: time.Minute,
		Failures: 3, Successes: 1, Failure: "degrade", OnExit: "restart"}
	switch kind {
	case "check":
		t.Interval, t.Timeout, t.Failure = 10*time.Second, 5*time.Second, "restart-container"
	case "process":
		t.Timeout = 0
	}
	return t
}

// Processes use on_exit; checks and jobs use the failure policy.
func (t Task) critical() bool {
	if t.Kind == "process" {
		return t.OnExit == "restart-container"
	}
	return t.Failure == "restart-container"
}

func (t Task) Validate() error {
	if !taskName.MatchString(t.Name) {
		return errors.New("task name must be 1–64 letters, digits, dots, underscores or hyphens")
	}
	if t.Kind != "check" && t.Kind != "job" && t.Kind != "process" {
		return fmt.Errorf("unknown task kind %q", t.Kind)
	}
	if t.Phase != "startup" && t.Phase != "after-start" && t.Phase != "runtime" && t.Phase != "shutdown" && t.Phase != "both" {
		return fmt.Errorf("unknown phase %q", t.Phase)
	}
	if len(t.Command) == 0 || t.Command[0] == "" {
		return errors.New("a command is required after --")
	}
	if t.Interval < 0 || (t.Interval > 0 && t.Interval < 100*time.Millisecond) {
		return errors.New("interval must be zero or at least 100ms")
	}
	if t.Timeout < 0 || t.Deadline <= 0 || t.Grace < 0 {
		return errors.New("timeout and grace must be nonnegative; deadline must be positive")
	}
	if t.Kind != "process" && t.Timeout == 0 {
		return errors.New("checks and jobs require a positive timeout")
	}
	if t.Kind == "check" && t.Interval == 0 {
		return errors.New("checks require a positive interval")
	}
	if t.Failures < 1 {
		return errors.New("failures must be positive")
	}
	if t.Successes < 1 || (t.Kind != "check" && t.Successes != 1) {
		return errors.New("successes must be positive; only checks support values above one")
	}
	if t.Failure != "degrade" && t.Failure != "restart-container" {
		return errors.New("failure must be degrade or restart-container")
	}
	if t.OnExit != "restart" && t.OnExit != "restart-container" {
		return errors.New("on-exit must be restart or restart-container")
	}
	if t.Kind == "process" && (t.Phase == "both" || t.Phase == "shutdown") {
		return errors.New("process phase must be startup, after-start or runtime")
	}
	if t.Kind == "check" && (t.Lock != "" || t.Phase == "shutdown") {
		return errors.New("checks cannot use locks or run at shutdown")
	}
	return nil
}

type Identity struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

type TaskStatus struct {
	health            string    // Last logical health, independent of transient execution state.
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	State             string    `json:"state"`
	Reason            string    `json:"reason,omitempty"`
	Failures          int       `json:"failures"`
	Successes         int       `json:"successes,omitzero"`
	RequiredSuccesses int       `json:"required_successes,omitzero"`
	LastStarted       float64   `json:"last_started,omitzero"`
	LastCompleted     float64   `json:"last_completed,omitzero"`
	Due               float64   `json:"due,omitzero"`
	Critical          bool      `json:"critical"`
	Process           *Identity `json:"process,omitempty"`
}

type Status struct {
	Version         int          `json:"schema_version"`
	Instance        string       `json:"instance"`
	Container       string       `json:"container"`
	Daemon          Identity     `json:"daemon"`
	Main            Identity     `json:"main"`
	Phase           string       `json:"phase"`
	State           string       `json:"state"`
	Reason          string       `json:"reason,omitempty"`
	Heartbeat       float64      `json:"heartbeat"`
	UpdatedAt       string       `json:"updated_at"`
	StartupDeadline float64      `json:"startup_deadline"`
	StopDeadline    float64      `json:"stop_deadline,omitzero"`
	Tasks           []TaskStatus `json:"tasks"`
}

type apiError struct {
	Error string `json:"error"`
}

type restartRequest struct {
	Reason string `json:"reason"`
}
