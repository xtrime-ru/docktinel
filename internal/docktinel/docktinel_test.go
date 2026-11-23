package docktinel

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testTask(name, kind, command string) Task {
	return Task{Name: name, Kind: kind, Phase: "runtime", Command: []string{"sh", "-c", command}, Env: os.Environ(), Interval: 100 * time.Millisecond, Timeout: time.Second, Deadline: 500 * time.Millisecond, Failures: 3, Successes: 1, Failure: "degrade", OnExit: "restart"}
}

func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	if _, err := uptime(); err != nil {
		t.Skip("Linux integration test")
	}
	d, err := newDaemon(Config{Dir: t.TempDir(), Heartbeat: time.Second, StartupTimeout: time.Minute, StopGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	now, err := uptime()
	if err != nil {
		t.Fatal(err)
	}
	d.status.StartupDeadline = now + d.cfg.StartupTimeout.Seconds()
	t.Cleanup(func() {
		d.stop("test cleanup", false)
		select {
		case <-d.done:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return d
}

func TestTaskValidation(t *testing.T) {
	valid := testTask("dns", "check", "true")
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Task){
		"name": func(v *Task) { v.Name = "../bad" }, "kind": func(v *Task) { v.Kind = "cron" },
		"phase": func(v *Task) { v.Phase = "invalid" }, "command": func(v *Task) { v.Command = nil },
		"timeout": func(v *Task) { v.Timeout = 0 }, "interval": func(v *Task) { v.Interval = 0 },
		"failures": func(v *Task) { v.Failures = 0 }, "lock": func(v *Task) { v.Lock = "shared" },
	} {
		t.Run(name, func(t *testing.T) {
			v := valid
			edit(&v)
			if v.Validate() == nil {
				t.Fatal("invalid task accepted")
			}
		})
	}
}

func TestHealthEvaluation(t *testing.T) {
	base := Status{Version: 1, Instance: "run", Container: "container", Heartbeat: 100, Phase: "runtime", State: "healthy"}
	for _, tc := range []struct {
		name      string
		edit      func(*Status)
		fatal     bool
		unhealthy bool
	}{
		{"healthy", func(s *Status) {}, false, false},
		{"degraded", func(s *Status) { s.State = "degraded" }, false, false},
		{"invalid-phase", func(s *Status) { s.Phase = "typo"; s.StartupDeadline = 200 }, true, true},
		{"invalid-state", func(s *Status) { s.State = "typo" }, true, true},
		{"stale", func(s *Status) { s.Heartbeat = 60 }, true, true},
		{"future", func(s *Status) { s.Heartbeat = 200 }, true, true},
		{"other-container", func(s *Status) { s.Container = "old" }, true, true},
		{"starting", func(s *Status) { s.Phase = "startup"; s.StartupDeadline = 120 }, false, true},
		{"startup-timeout", func(s *Status) { s.Phase = "startup"; s.StartupDeadline = 99 }, true, true},
		{"stopping", func(s *Status) { s.Phase = "stopping"; s.StopDeadline = 120 }, false, true},
		{"stop-timeout", func(s *Status) { s.Phase = "stopping"; s.StopDeadline = 99 }, true, true},
		{"critical-overdue", func(s *Status) { s.Tasks = []TaskStatus{{Name: "dns", Critical: true, Due: 99}} }, true, true},
		{"maintenance-overdue", func(s *Status) { s.Tasks = []TaskStatus{{Name: "update", Due: 99}} }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.edit(&s)
			fatal, reason := Evaluate(s, "container", 100, 30*time.Second)
			if fatal != tc.fatal || (reason != "") != tc.unhealthy {
				t.Fatalf("fatal=%v reason=%q", fatal, reason)
			}
		})
	}
}

func TestHealthcheckReadsSnapshotBeforeUptime(t *testing.T) {
	if _, err := uptime(); err != nil {
		t.Skip("Linux integration test")
	}
	container, err := containerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	status := Status{
		Version: 1, Instance: "run", Container: container, Daemon: daemon,
		Heartbeat: 99, Phase: "runtime", State: "healthy",
	}
	if err := writeJSON(path, status); err != nil {
		t.Fatal(err)
	}

	// Publish a new heartbeat immediately after the check samples the clock.
	readUptime := func() (float64, error) {
		status.Heartbeat = 100.03
		return 100, writeJSON(path, status)
	}
	if err := healthcheck(dir, 30*time.Second, 10*time.Second, 0, true, readUptime); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheckHonorsShutdownDeadlineAfterDaemonExit(t *testing.T) {
	if _, err := uptime(); err != nil {
		t.Skip("Linux integration test")
	}
	container, err := containerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	status := Status{
		Version: 1, Instance: "run", Container: container, Heartbeat: 99,
		Phase: "stopping", Reason: "termination signal", StopDeadline: 124,
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), status); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		now  float64
		want string
	}{
		{100, "stopping: termination signal"},
		{124, "stopping: termination signal"},
		{125, "shutdown deadline exceeded"},
	} {
		readUptime := func() (float64, error) { return tc.now, nil }
		err := healthcheck(dir, time.Second, 10*time.Second, 0, true, readUptime)
		if err == nil || err.Error() != tc.want {
			t.Errorf("at %v: got %v, want %q", tc.now, err, tc.want)
		}
	}
}

func TestRegistrationAndStartupRetry(t *testing.T) {
	d := testDaemon(t)
	marker := filepath.Join(t.TempDir(), "ready")
	task := testTask("wait", "check", `if [ -e "$READY" ]; then exit 0; else touch "$READY"; exit 1; fi`)
	task.Phase = "startup"
	task.Env = append(task.Env, "READY="+marker)
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	if d.add(task) == nil {
		t.Fatal("duplicate task accepted")
	}
	if err := d.startup(); err != nil {
		t.Fatal(err)
	}
	if d.snapshot().Phase != "runtime" || d.snapshot().Tasks[0].State != "healthy" {
		t.Fatal(d.snapshot())
	}
	if d.startup() == nil {
		t.Fatal("duplicate startup accepted")
	}
}

func TestStartupDependencyCanDegrade(t *testing.T) {
	d := testDaemon(t)
	task := testTask("optional-dns", "check", "false")
	task.Phase = "startup"
	task.Deadline = 150 * time.Millisecond
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	if err := d.startup(); err != nil {
		t.Fatal(err)
	}
	if d.snapshot().Tasks[0].State != "degraded" {
		t.Fatal(d.snapshot())
	}
}

func TestCriticalCheckStopsAfterThreshold(t *testing.T) {
	d := testDaemon(t)
	task := testTask("critical", "check", "false")
	task.Failure = "restart-container"
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.launch(task)
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
		t.Fatal("critical failure did not stop daemon")
	}
	if got := d.snapshot().Tasks[0].Failures; got != 3 {
		t.Fatalf("got %d failures", got)
	}
}

func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	d := testDaemon(t)
	task := testTask("check", "check", "true")
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.markFailure(task, os.ErrInvalid)
	d.markFailure(task, os.ErrInvalid)
	if err := d.attempt(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if d.snapshot().Tasks[0].Failures != 0 {
		t.Fatal("failure count was not reset")
	}
}

func TestSharedLockSerializesJobs(t *testing.T) {
	d := testDaemon(t)
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		task := testTask(name, "job", `mkdir "$GUARD" || exit 1; sleep 0.05; rmdir "$GUARD"`)
		task.Interval = 0
		task.Lock = "shared"
		task.Env = append(task.Env, "GUARD="+filepath.Join(root, "guard"))
		if err := d.add(task); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range d.tasks {
		d.launch(task)
	}
	d.workers.Wait()
	for _, task := range d.snapshot().Tasks {
		if task.Failures != 0 || task.State != "healthy" {
			t.Fatal(task)
		}
	}
}

func TestTimeoutKillsCommandGroup(t *testing.T) {
	if _, err := uptime(); err != nil {
		t.Skip("Linux test")
	}
	path := filepath.Join(t.TempDir(), "child")
	task := testTask("timeout", "job", `trap '' TERM; sleep 30 & echo $! > "$CHILD"; wait`)
	task.Env = append(task.Env, "CHILD="+path)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := execute(ctx, task, nil); err == nil {
		t.Fatal("timed-out command succeeded")
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("command cancellation took too long")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// SIGKILL is asynchronous: allow the kernel to schedule the child and
	// complete its exit before inspecting /proc. Keep the assertion bounded.
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := processIdentity(pid); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background child survived timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAtomicStateConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := writeJSON(path, Status{Version: 1}); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				if _, err := readStatus(dir); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for range 100 {
		if err := writeJSON(path, Status{Version: 1, Instance: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("state is readable by other users")
	}
}

func TestCommandHelpAndErrors(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"daemon", "--help"}, {"version"}} {
		var out, err bytes.Buffer
		if code := CLI(args, &out, &err, "test"); code != 0 {
			t.Fatalf("%v: %s", args, err.String())
		}
	}
	var out, err bytes.Buffer
	if CLI([]string{"add", "check", "x", "--timeout", "0", "--", "true"}, &out, &err, "test") != 1 {
		t.Fatal("invalid task accepted")
	}
}

func TestStateDoesNotContainTaskSecrets(t *testing.T) {
	d := testDaemon(t)
	task := testTask("private", "job", "echo password")
	task.Env = append(task.Env, "TOKEN=secret")
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d.snapshot())
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "password") {
		t.Fatal("state leaked command or environment")
	}
}

func TestLockTimeoutClearsExecutionDeadline(t *testing.T) {
	d := testDaemon(t)
	task := testTask("waiting", "job", "true")
	task.Lock = "occupied"
	task.Timeout = 20 * time.Millisecond
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.locks[task.Lock] <- struct{}{}
	if err := d.attempt(context.Background(), task); err == nil {
		t.Fatal("command acquired an occupied lock")
	}
	if state := d.snapshot().Tasks[0]; state.Due != 0 || state.LastCompleted == 0 {
		t.Fatal("lock timeout left a stale execution deadline", state)
	}
}

func TestTaskDeadlineAllowsNextPublication(t *testing.T) {
	d := testDaemon(t)
	d.cfg.Heartbeat = 10 * time.Second
	task := testTask("fast", "check", "sleep 0.1")
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.launch(task)
	// The initial deadline must not expire before the next state publication.
	state := d.snapshot()
	if state.Tasks[0].Due < state.Heartbeat+10 {
		t.Fatal("deadline ignores publication interval", state)
	}
}

func TestProcessCriticalityUsesOnExitOnly(t *testing.T) {
	for _, tc := range []struct {
		failure, onExit string
		critical        bool
	}{
		{"degrade", "restart", false},
		{"restart-container", "restart", false},
		{"degrade", "restart-container", true},
		{"restart-container", "restart-container", true},
	} {
		d := testDaemon(t)
		task := testTask("process", "process", "true")
		task.Failure, task.OnExit = tc.failure, tc.onExit
		if err := d.add(task); err != nil {
			t.Fatal(err)
		}
		if got := d.snapshot().Tasks[0].Critical; got != tc.critical {
			t.Fatalf("failure=%s on_exit=%s: critical=%v", tc.failure, tc.onExit, got)
		}
	}
}

func TestDegradedPersistsDuringRetryUntilSuccess(t *testing.T) {
	d := testDaemon(t)
	task := testTask("retry", "job", "sleep 0.15")
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.status.Phase = "runtime"
	d.markFailure(task, os.ErrInvalid)
	done := make(chan error, 1)
	go func() { done <- d.attempt(context.Background(), task) }()
	deadline := time.Now().Add(time.Second)
	for d.snapshot().Tasks[0].State != "running" {
		if time.Now().After(deadline) {
			t.Fatal("retry did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if state := d.snapshot().State; state != "degraded" {
		t.Fatalf("retry hid previous failure: %s", state)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := d.snapshot().State; state != "healthy" {
		t.Fatalf("success did not recover: %s", state)
	}
}

func TestRuntimeTransitionPublishesWithoutHeartbeat(t *testing.T) {
	d := testDaemon(t)
	d.cfg.Heartbeat = time.Hour
	task := testTask("delayed", "check", "true")
	task.Grace = time.Minute
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	if err := d.startup(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, err := readStatus(d.cfg.Dir)
		if err == nil && state.Phase == "runtime" {
			if len(state.Tasks) != 1 || state.Tasks[0].Due <= state.Heartbeat {
				t.Fatal("runtime published before scheduling tasks", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime transition not published: %+v, %v", state, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShutdownRunsCleanupWithStuckWorker(t *testing.T) {
	d := testDaemon(t)
	d.cfg.StopGrace = 8 * time.Second
	cleanup := testTask("cleanup", "job", "touch cleaned")
	cleanup.Phase = "shutdown"
	cleanup.Dir = d.cfg.Dir
	if err := d.add(cleanup); err != nil {
		t.Fatal(err)
	}
	d.workers.Add(1)
	defer d.workers.Done()

	started := time.Now()
	d.stop("test shutdown", false)
	status := d.snapshot()
	if remaining := status.StopDeadline - status.Heartbeat; remaining <= 0 || remaining > d.cfg.StopGrace.Seconds() {
		t.Fatalf("unexpected shutdown budget: %.3fs remaining", remaining)
	}
	select {
	case <-d.done:
	case <-time.After(d.cfg.StopGrace + time.Second):
		t.Fatal("stuck worker prevented shutdown")
	}
	if elapsed := time.Since(started); elapsed < d.cfg.StopGrace/2 {
		t.Fatalf("finished before half the shutdown budget elapsed: %v", elapsed)
	}
	if _, err := os.Stat(filepath.Join(d.cfg.Dir, "cleaned")); err != nil {
		t.Fatalf("shutdown job did not run: %v", err)
	}
}

func TestConcurrentPublicationKeepsStoppingState(t *testing.T) {
	d := testDaemon(t)
	if err := d.publish(); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			if err := d.publish(); err != nil {
				t.Error(err)
			}
		})
	}
	d.stop("review test", false)
	writers.Wait()
	state, err := readStatus(d.cfg.Dir)
	if err != nil || state.Phase != "stopping" || state.Reason != "review test" {
		t.Fatalf("stale transition: %+v, %v", state, err)
	}
}

func TestVersionValidatesArguments(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := CLI([]string{"version", "unexpected"}, &out, &stderr, "test"); code == 0 {
		t.Fatal("version ignored an unexpected argument")
	}
}

func TestExpiredPhaseDoesNotLaunchProcess(t *testing.T) {
	d := testDaemon(t)
	task := testTask("late", "process", "true")
	task.Phase = "startup"
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	now, _ := uptime()
	d.status.StartupDeadline = now - 1
	if err := d.runPhase("startup", d.tasks); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
	if state := d.snapshot().Tasks[0]; state.State != "pending" || state.Due != 0 {
		t.Fatalf("expired phase scheduled a process: %+v", state)
	}
}
