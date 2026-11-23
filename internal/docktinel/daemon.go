package docktinel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"uuid"
)

type Config struct {
	Dir            string
	Heartbeat      time.Duration
	StartupTimeout time.Duration
	ConfigPath     string
	Command        []string
	StopGrace      time.Duration
}

type Daemon struct {
	cfg       Config
	mu        sync.Mutex
	publishMu sync.Mutex
	status    Status
	tasks     []Task
	states    map[string]TaskStatus
	locks     map[string]chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	done      chan struct{}
	stopError error
}

func newDaemon(cfg Config) (*Daemon, error) {
	container, err := containerIdentity()
	if err != nil {
		return nil, err
	}
	self, err := processIdentity(os.Getpid())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{cfg: cfg, states: map[string]TaskStatus{}, locks: map[string]chan struct{}{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), status: Status{
		Version: 1, Instance: uuid.New().String(), Container: container, Daemon: self, Main: self,
		Phase: "configuring", State: "starting",
	}}, nil
}

func RunDaemon(cfg Config) error {
	if os.Getppid() != 1 || !hasDockerInit() {
		return errors.New("Docker init must launch Docktinel directly. Use init: true in Compose or docker run --init. In shell entrypoints, use exec docktinel")
	}
	if cfg.Heartbeat < 100*time.Millisecond || cfg.StartupTimeout <= 0 || cfg.StopGrace <= 0 {
		return errors.New("heartbeat must be at least 100ms; timeout and stop-grace must be positive")
	}
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return err
	}
	lock, err := tryFileLock(filepath.Join(cfg.Dir, "daemon.lock"))
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errors.New("another Docktinel daemon is already running in this runtime directory")
		}
		return err
	}
	defer lock.Close()

	d, err := newDaemon(cfg)
	if err != nil {
		return err
	}
	defer d.cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, watchdogStopSignal)
	defer signal.Stop(signals)
	tasks, err := loadConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	if len(cfg.Command) > 0 {
		app := taskDefaults("process", "application")
		app.Phase, app.OnExit, app.Command = "startup", "restart-container", cfg.Command
		tasks = append(tasks, app)
	}
	for _, task := range tasks {
		if err := d.add(&task); err != nil {
			return err
		}
	}
	now, err := uptime()
	if err != nil {
		return err
	}
	d.status.StartupDeadline = now + cfg.StartupTimeout.Seconds()
	socket := filepath.Join(cfg.Dir, "control.sock")
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	if err := d.publish(); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(cfg.Dir, "identity.json"), stopRecord{Container: d.status.Container, Target: d.status.Daemon}); err != nil {
		return err
	}
	slog.Info("Docktinel listening", "socket", socket)
	logLifecycle(lifecycleState{}, lifecycleState{d.status.Phase, d.status.State}, "daemon started")
	server := d.controlServer()
	defer server.Close()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	go d.startup()
	ticker := time.NewTicker(cfg.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-serveErrors:
			d.stop("control API stopped", true)
			<-d.done
			return fmt.Errorf("serve control API: %w", err)
		case sig := <-signals:
			if sig == watchdogStopSignal {
				d.stop("healthcheck requested shutdown", true)
			} else {
				d.stop("termination signal", false)
			}
		case <-d.done:
			return d.stopError
		case <-ticker.C:
			if err := d.heartbeat(); err != nil {
				slog.Error("cannot publish state", "error", err)
				d.stop("cannot publish state", true)
			}
		}
	}
}

func (d *Daemon) snapshot() Status {
	// Capture time before state; avoid filesystem reads/formatting under mu.
	now, _ := uptime()
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	d.mu.Lock()
	s := d.status
	s.Tasks = make([]TaskStatus, 0, len(d.tasks))
	for _, task := range d.tasks {
		t := d.states[task.Name]
		s.Tasks = append(s.Tasks, t)
	}
	s.State = d.lifecycleLocked().state
	d.mu.Unlock()
	s.Heartbeat, s.UpdatedAt = now, updatedAt
	return s
}

func (d *Daemon) heartbeat() error {
	// Keep snapshot creation and publication ordered with lifecycle updates.
	d.publishMu.Lock()
	s := d.snapshot()
	if s.State == "starting" && s.Heartbeat > s.StartupDeadline {
		d.publishMu.Unlock()
		// stop publishes the new stopping state using the same publication lock.
		d.stop("startup deadline exceeded", true)
		return nil
	}
	defer d.publishMu.Unlock()
	return writeJSON(filepath.Join(d.cfg.Dir, "status.json"), s)
}

func (d *Daemon) publish() error {
	// Serialize snapshot creation and rename together, so an older snapshot
	// cannot overwrite a newer lifecycle transition.
	d.publishMu.Lock()
	defer d.publishMu.Unlock()
	return writeJSON(filepath.Join(d.cfg.Dir, "status.json"), d.snapshot())
}

func (d *Daemon) add(task *Task) error {
	if task == nil {
		return errors.New("missing task")
	}
	if err := task.Validate(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.status.Phase != "configuring" {
		return errors.New("register all tasks before startup")
	}
	if _, exists := d.states[task.Name]; exists {
		return fmt.Errorf("task %q already exists", task.Name)
	}
	d.tasks = append(d.tasks, *task)
	state := TaskStatus{Name: task.Name, Kind: task.Kind, State: "pending", Critical: task.critical()}
	if task.Kind == "check" {
		state.RequiredSuccesses = task.Successes
	}
	d.states[task.Name] = state
	if task.Lock != "" {
		if _, ok := d.locks[task.Lock]; !ok {
			d.locks[task.Lock] = make(chan struct{}, 1)
		}
	}
	return nil
}

func (d *Daemon) startup() (err error) {
	d.mu.Lock()
	if d.status.Phase != "configuring" {
		d.mu.Unlock()
		return errors.New("startup interrupted")
	}
	d.workers.Add(1)
	tasks := d.tasks
	d.mu.Unlock()
	defer func() {
		if err != nil {
			d.stop(err.Error(), true)
		}
		d.workers.Done()
	}()

	for _, phase := range []string{"startup", "after-start", "runtime"} {
		d.mu.Lock()
		if d.status.Phase == "stopping" {
			d.mu.Unlock()
			return nil
		}
		before := d.lifecycleLocked()
		d.status.Phase = phase
		if phase == "runtime" {
			for _, task := range tasks {
				if task.Phase == "runtime" || task.Phase == "both" {
					d.launchLocked(task)
				}
			}
		}
		after := d.lifecycleLocked()
		d.mu.Unlock()
		logLifecycle(before, after, "starting "+phase)
		if err := d.publish(); err != nil {
			return err
		}
		if phase == "runtime" {
			return nil
		}
		if err := d.runPhase(phase, tasks); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) stop(reason string, restart bool) {
	now, _ := uptime()
	d.mu.Lock()
	if d.status.Phase == "stopping" {
		d.mu.Unlock()
		return
	}
	before := d.lifecycleLocked()
	d.status.Phase, d.status.State, d.status.Reason = "stopping", "unhealthy", reason
	d.status.StopDeadline = now + (d.cfg.StopGrace + 2*commandStopGrace + time.Second).Seconds()
	after := d.lifecycleLocked()
	d.mu.Unlock()

	d.cancel()
	logLifecycle(before, after, reason)
	if err := d.publish(); err != nil {
		slog.Error("cannot publish stopping state", "error", err)
	}
	go func() {
		// Allow the external healthcheck to escalate if a worker cannot exit.
		d.workers.Wait()
		cleanup, cancel := context.WithTimeout(context.Background(), d.cfg.StopGrace)
		defer close(d.done)
		defer cancel()
		for _, task := range d.tasks {
			if task.Phase == "shutdown" {
				if err := d.attempt(cleanup, task); err != nil {
					slog.Warn("cleanup failed", "task", task.Name, "error", err)
				}
			}
		}
		if restart {
			d.stopError = errors.New(reason)
		}
	}()
}
