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

func newDaemon(cfg Config, tasks ...Task) (*Daemon, error) {
	container, err := containerIdentity()
	if err != nil {
		return nil, err
	}
	self, err := processIdentity(os.Getpid())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{cfg: cfg, states: map[string]TaskStatus{}, locks: map[string]chan struct{}{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), status: Status{
		Version: 1, Instance: uuid.New().String(), Container: container, Daemon: self, Main: self,
		Phase: "configuring", State: "starting",
	}}
	for _, task := range tasks {
		if err := d.add(task); err != nil {
			cancel()
			return nil, err
		}
	}
	return d, nil
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
	d, err := newDaemon(cfg, tasks...)
	if err != nil {
		return err
	}
	defer d.cancel()
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

// add prepares the task registry before the daemon starts any workers.
func (d *Daemon) add(task Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	if _, exists := d.states[task.Name]; exists {
		return fmt.Errorf("task %q already exists", task.Name)
	}
	d.tasks = append(d.tasks, task)
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
	deadline := time.Now().Add(d.cfg.StopGrace)
	now, _ := uptime()
	d.mu.Lock()
	if d.status.Phase == "stopping" {
		d.mu.Unlock()
		return
	}
	before := d.lifecycleLocked()
	d.status.Phase, d.status.State, d.status.Reason = "stopping", "unhealthy", reason
	d.status.StopDeadline = now + d.cfg.StopGrace.Seconds()
	after := d.lifecycleLocked()
	d.mu.Unlock()

	d.cancel()
	logLifecycle(before, after, reason)
	if err := d.publish(); err != nil {
		slog.Error("cannot publish stopping state", "error", err)
	}
	go func() {
		defer close(d.done)
		d.shutdown(deadline)
		if restart {
			d.stopError = errors.New(reason)
		}
	}()
}

func (d *Daemon) shutdown(deadline time.Time) {
	workersDone := make(chan struct{})
	go func() {
		d.workers.Wait()
		close(workersDone)
	}()
	// Give active workers at most the first half of the shutdown budget.
	timer := time.NewTimer(time.Until(deadline.Add(-d.cfg.StopGrace / 2)))
	defer timer.Stop()
	select {
	case <-workersDone:
	case <-timer.C:
		slog.Warn("workers still running halfway through shutdown; starting cleanup")
	}

	// Cancel the last command early enough to give it time to terminate.
	commandGrace := min(commandStopGrace, time.Until(deadline)/2)
	cleanup, cancel := context.WithDeadline(context.Background(), deadline.Add(-max(0, commandGrace)))
	defer cancel()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		for _, task := range d.tasks {
			if cleanup.Err() != nil {
				return
			}
			if task.Phase == "shutdown" {
				if err := d.attempt(cleanup, task); err != nil {
					slog.Warn("cleanup failed", "task", task.Name, "error", err)
				}
			}
		}
	}()

	timer.Reset(time.Until(deadline))
	select {
	case <-cleanupDone:
	case <-timer.C:
		slog.Warn("cleanup did not finish before shutdown deadline")
	}
}
