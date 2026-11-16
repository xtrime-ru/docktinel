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
	StopGrace           time.Duration
}

type Daemon struct {
	cfg      Config
	mu       sync.Mutex
	status   Status
	tasks    []Task
	states   map[string]TaskStatus
	locks    map[string]chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	stopOnce sync.Once
	done     chan struct{}
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
	main, err := processIdentity(os.Getppid())
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
	lock, err := os.OpenFile(filepath.Join(cfg.Dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another Docktinel daemon is already running in this runtime directory")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
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
		degraded = degraded || t.State == "degraded" || t.State == "failed"
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
	d.states[task.Name] = TaskStatus{Name: task.Name, Kind: task.Kind, State: "pending", Critical: task.Failure == "restart-container" || (task.Kind == "process" && task.OnExit == "restart-container")}
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
		d.mu.Unlock()
		for _, task := range tasks {
			if task.Phase == "runtime" || task.Phase == "both" {
				d.launch(task)
			}
		}
	}()
	return nil
}

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
		if d.ctx.Err() != nil {
			return d.ctx.Err()
		}
		if err != nil {
			d.markFailure(task, err)
			if task.Failure == "restart-container" {
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
	if d.status.Phase == "stopping" {
		d.mu.Unlock()
		return
	}
	now, _ := uptime()
	s := d.states[task.Name]
	s.Due = now + task.Grace.Seconds() + task.Timeout.Seconds() + d.cfg.Heartbeat.Seconds() + 5
	d.states[task.Name] = s
	d.workers.Go(func() { d.runTask(task) })
	d.mu.Unlock()
}

func (d *Daemon) attempt(parent context.Context, task Task) error {
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
	if task.Lock != "" {
		lock := d.locks[task.Lock]
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			now, _ := uptime()
			d.update(task.Name, func(s *TaskStatus) {
				s.LastCompleted = now
				s.Due = 0
			})
			return ctx.Err()
		}
	}
	err := execute(ctx, task, func(p Identity) { d.update(task.Name, func(s *TaskStatus) { s.Process = &p }) })
	now, _ = uptime()
	d.update(task.Name, func(s *TaskStatus) {
		s.LastCompleted = now
		s.Process = nil
		s.Due = 0
		if err == nil {
			s.State = "healthy"
			s.Reason = ""
			s.Failures = 0
		}
	})
	return err
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
	errorsInRow := 0
	for d.ctx.Err() == nil {
		err := d.attempt(d.ctx, task)
		if d.ctx.Err() != nil {
			return
		}
		pause := task.Interval
		if err != nil {
			errorsInRow++
			count := d.markFailure(task, err)
			if task.Kind != "process" && task.Failure == "restart-container" && count >= task.Failures {
				d.stop("task failed: "+task.Name, true)
				return
			}
			if task.Kind == "process" && task.OnExit == "restart-container" {
				d.stop("process exited: "+task.Name, true)
				return
			}
			if task.Kind != "check" {
				pause = min(time.Duration(10*errorsInRow)*time.Second, time.Minute)
			}
		} else {
			errorsInRow = 0
			if task.Kind == "process" && task.OnExit == "restart-container" {
				d.stop("process completed: "+task.Name, true)
				return
			}
			if task.Kind == "job" && task.Interval == 0 {
				return
			}
			if task.Kind == "process" {
				pause = 100 * time.Millisecond
			}
		}
		if pause <= 0 {
			pause = time.Second
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
