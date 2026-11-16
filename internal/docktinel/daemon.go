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
	"slices"
	"sync"
	"syscall"
	"time"
	"uuid"
)

type Config struct {
	Dir                 string
	Heartbeat           time.Duration
	RegistrationTimeout time.Duration
	ConfigPath          string
	ConfigRequired      bool
	MainPID             int
	StopGrace           time.Duration
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
	stopOnce  sync.Once
	done      chan struct{}
}

func newDaemon(cfg Config) (*Daemon, error) {
	now, err := uptime()
	if err != nil {
		return nil, err
	}
	container, err := containerIdentity()
	if err != nil {
		return nil, err
	}
	self, err := processIdentity(os.Getpid())
	if err != nil {
		return nil, err
	}
	mainPID := cfg.MainPID
	if mainPID == 0 {
		mainPID = os.Getppid()
	}
	main, err := processIdentity(mainPID)
	if err != nil {
		return nil, err
	}
	if main.PID <= 1 {
		return nil, errors.New("start daemon from an entrypoint under Docker init: true / --init")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{cfg: cfg, states: map[string]TaskStatus{}, locks: map[string]chan struct{}{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), status: Status{
		Version: 1, Instance: uuid.New().String(), Container: container, Daemon: self, Main: main,
		Phase: "configuring", State: "starting", StartupDeadline: now + cfg.RegistrationTimeout.Seconds(),
	}}, nil
}

func RunDaemon(cfg Config) error {
	if cfg.Heartbeat < 100*time.Millisecond || cfg.RegistrationTimeout <= 0 || cfg.StopGrace <= 0 {
		return errors.New("heartbeat must be at least 100ms; registration-timeout and stop-grace must be positive")
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
	if cfg.ConfigPath != "" {
		tasks, err := loadConfig(cfg.ConfigPath, cfg.ConfigRequired)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if err := d.add(&task); err != nil {
				return err
			}
		}
	}
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
	if err := writeJSON(filepath.Join(cfg.Dir, "identity.json"), stopRecord{Container: d.status.Container, Target: d.status.Main}); err != nil {
		return err
	}
	slog.Info("Docktinel listening", "socket", socket)
	server := d.controlServer()
	defer server.Close()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	ticker := time.NewTicker(cfg.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-serveErrors:
			d.stop("control API stopped", true)
			<-d.done
			return fmt.Errorf("serve control API: %w", err)
		case <-signals:
			d.stop("termination signal", false)
		case <-d.done:
			return nil
		case <-ticker.C:
			s := d.snapshot()
			if s.State == "starting" && s.Heartbeat > s.StartupDeadline {
				d.stop("startup deadline exceeded", true)
			}
			if err := d.publish(); err != nil {
				slog.Error("cannot publish state", "error", err)
				d.stop("cannot publish state", true)
			}
		}
	}
}

func (d *Daemon) snapshot() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.status
	s.Heartbeat, _ = uptime()
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.Tasks = make([]TaskStatus, 0, len(d.tasks))
	degraded := false
	for _, task := range d.tasks {
		t := d.states[task.Name]
		s.Tasks = append(s.Tasks, t)
		degraded = degraded || t.Failures > 0
	}
	if s.Phase == "runtime" {
		s.State = "healthy"
		if degraded {
			s.State = "degraded"
		}
	}
	return s
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
	d.states[task.Name] = TaskStatus{Name: task.Name, Kind: task.Kind, State: "pending", Critical: task.critical()}
	if task.Lock != "" {
		if _, ok := d.locks[task.Lock]; !ok {
			d.locks[task.Lock] = make(chan struct{}, 1)
		}
	}
	return nil
}

func (d *Daemon) startup(budget time.Duration) error {
	if budget <= 0 {
		return errors.New("startup timeout must be positive")
	}

	d.mu.Lock()
	if d.status.Phase != "configuring" {
		d.mu.Unlock()
		return errors.New("startup has already been requested")
	}
	now, _ := uptime()
	if d.status.StartupDeadline > 0 && now > d.status.StartupDeadline {
		d.mu.Unlock()
		d.stop("registration deadline exceeded", true)
		return errors.New("registration deadline exceeded")
	}
	d.status.StartupDeadline = now + budget.Seconds()
	d.status.Phase = "startup"
	d.workers.Add(1)
	tasks := slices.Clone(d.tasks)
	d.mu.Unlock()
	defer d.workers.Done()
	if err := d.runPhase("startup", tasks); err != nil {
		d.stop(err.Error(), true)
		return err
	}
	d.mu.Lock()
	if d.status.Phase == "startup" {
		d.status.Phase = "awaiting-main"
	}
	d.mu.Unlock()
	return d.publish()
}

func (d *Daemon) attach(main *Identity) error {
	if main == nil || !sameProcess(*main) {
		return errors.New("main process identity is invalid; PID 1 cannot be the application")
	}
	d.mu.Lock()
	if d.status.Phase != "awaiting-main" {
		d.mu.Unlock()
		return errors.New("run startup before exec")
	}
	if *main != d.status.Main {
		d.mu.Unlock()
		return errors.New("exec must replace the entrypoint: use exec docktinel exec -- COMMAND")
	}
	now, _ := uptime()
	if now > d.status.StartupDeadline {
		d.mu.Unlock()
		d.stop("startup deadline exceeded", true)
		return errors.New("startup deadline exceeded")
	}
	d.status.Main = *main
	d.status.Phase = "after-start"
	d.workers.Add(1)
	tasks := slices.Clone(d.tasks)
	d.mu.Unlock()
	if err := d.publish(); err != nil {
		d.workers.Done()
		return err
	}
	go func() {
		defer d.workers.Done()
		if err := d.runPhase("after-start", tasks); err != nil {
			d.stop(err.Error(), true)
			return
		}
		d.mu.Lock()
		if d.status.Phase == "stopping" {
			d.mu.Unlock()
			return
		}
		d.status.Phase = "runtime"
		for _, task := range tasks {
			if task.Phase == "runtime" || task.Phase == "both" {
				d.launchLocked(task)
			}
		}
		d.mu.Unlock()
		if err := d.publish(); err != nil {
			d.stop("cannot publish runtime state", true)
		}

	}()
	return nil
}

func (d *Daemon) stop(reason string, terminateMain bool) {
	d.stopOnce.Do(func() {
		now, _ := uptime()
		d.mu.Lock()
		d.status.Phase = "stopping"
		d.status.State = "unhealthy"
		d.status.Reason = reason
		d.status.StopDeadline = now + 2*d.cfg.StopGrace.Seconds() + 5
		main := d.status.Main
		tasks := slices.Clone(d.tasks)
		d.mu.Unlock()
		slog.Warn("stopping", "reason", reason)
		d.cancel()
		if err := d.publish(); err != nil {
			slog.Error("cannot publish stopping state", "error", err)
		}
		go func() {
			// Child cancellation normally completes in two seconds. Keep the
			// daemon alive if a child is stuck; external healthcheck can escalate.
			d.workers.Wait()
			cleanup, cancel := context.WithTimeout(context.Background(), d.cfg.StopGrace)
			for _, task := range tasks {
				if task.Phase == "shutdown" {
					if err := d.attempt(cleanup, task); err != nil {
						slog.Warn("cleanup failed", "task", task.Name, "error", err)
					}
				}
			}
			cancel()
			if terminateMain && sameProcess(main) {
				_ = signalProcess(main, syscall.SIGTERM)
				deadline := time.Now().Add(d.cfg.StopGrace)
				for sameProcess(main) && time.Now().Before(deadline) {
					time.Sleep(100 * time.Millisecond)
				}
				if sameProcess(main) {
					_ = signalProcess(main, syscall.SIGKILL)
				}
			}
			close(d.done)
		}()
	})
}
