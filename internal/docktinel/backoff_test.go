package docktinel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertJitter(t *testing.T, got, nominal time.Duration) {
	t.Helper()
	if got < nominal*4/5 || got > nominal*6/5 {
		t.Fatalf("delay %s outside ±20%% of %s", got, nominal)
	}
}

func TestProcessBackoffAndStableReset(t *testing.T) {
	for _, failed := range []bool{false, true} {
		b := retryBackoff{}
		task := Task{Kind: "process"}
		for _, seconds := range []int{1, 2, 4, 8, 16, 30, 30} {
			assertJitter(t, b.next(task, 30*time.Second, failed), time.Duration(seconds)*time.Second)
		}
		if got := b.next(task, 30*time.Second+time.Nanosecond, failed); got != 0 {
			t.Fatalf("stable process should restart immediately, got %s", got)
		}
		assertJitter(t, b.next(task, 0, failed), time.Second)
	}
}

func TestJobBackoffResetsAfterSuccess(t *testing.T) {
	b := retryBackoff{}
	task := Task{Kind: "job", Interval: 17 * time.Second}
	for _, seconds := range []int{5, 10, 20, 40, 60, 60} {
		assertJitter(t, b.next(task, 0, true), time.Duration(seconds)*time.Second)
	}
	if got := b.next(task, 0, false); got != task.Interval {
		t.Fatalf("successful job should resume its interval, got %s", got)
	}
	assertJitter(t, b.next(task, 0, true), 5*time.Second)
}

func TestChecksKeepFixedInterval(t *testing.T) {
	b := retryBackoff{}
	task := Task{Kind: "check", Interval: 123 * time.Millisecond}
	for range 20 {
		for _, failed := range []bool{true, false} {
			if got := b.next(task, 0, failed); got != task.Interval {
				t.Fatalf("check interval changed: %s", got)
			}
		}
	}
}

func TestDelayZeroAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	if !delay(ctx, 0) {
		t.Fatal("zero delay rejected a live context")
	}
	cancel()
	if delay(ctx, 0) || delay(ctx, time.Hour) {
		t.Fatal("cancellation did not interrupt backoff")
	}
}

func TestQuickSuccessfulProcessDoesNotSpin(t *testing.T) {
	d := testDaemon(t)
	marker := filepath.Join(t.TempDir(), "starts")
	task := testTask("quick", "process", `echo started >> "$STARTS"`)
	task.Env = append(task.Env, "STARTS="+marker)
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	d.launch(task)
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, _ := os.ReadFile(marker)
		if len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A 100ms restart loop used to run several copies in this interval.
	time.Sleep(300 * time.Millisecond)
	data, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(data), "started") != 1 {
		t.Fatalf("short successful process restarted too quickly: %q, %v", data, err)
	}
}

func TestProcessRestartContainerDoesNotWaitForBackoff(t *testing.T) {
	for _, command := range []string{"true", "false"} {
		t.Run(command, func(t *testing.T) {
			d := testDaemon(t)
			task := testTask("critical-process", "process", command)
			task.OnExit = "restart-container"
			if err := d.add(&task); err != nil {
				t.Fatal(err)
			}
			d.launch(task)
			select {
			case <-d.done:
			case <-time.After(time.Second):
				t.Fatal("container stop was delayed by process backoff")
			}
		})
	}
}

func TestExecutionFailureHasZeroLifetime(t *testing.T) {
	task := Task{Command: []string{"/this-command-does-not-exist"}}
	elapsed, err := execute(t.Context(), task, nil)
	if err == nil || elapsed != 0 {
		t.Fatalf("failed exec has elapsed=%s, error=%v", elapsed, err)
	}
}

func TestHeartbeatPublishesExpiredStartupAsStopping(t *testing.T) {
	d := testDaemon(t)
	d.status.StartupDeadline = 1
	if err := d.heartbeat(); err != nil {
		t.Fatal(err)
	}
	s, err := readStatus(d.cfg.Dir)
	if err != nil || s.Phase != "stopping" || s.State != "unhealthy" {
		t.Fatalf("expired startup published the old state: %+v, %v", s, err)
	}
}
