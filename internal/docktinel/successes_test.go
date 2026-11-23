package docktinel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupAndAfterStartRequireConsecutiveSuccesses(t *testing.T) {
	for _, phase := range []string{"startup", "after-start"} {
		t.Run(phase, func(t *testing.T) {
			d := testDaemon(t)
			counter := filepath.Join(t.TempDir(), "attempts")
			task := testTask("ready", "check", `n=0; if [ -f "$COUNT" ]; then n=$(cat "$COUNT"); fi; n=$((n+1)); echo "$n" > "$COUNT"; test "$n" -ne 2`)
			task.Phase, task.Successes, task.Deadline = phase, 3, 2*time.Second
			task.Env = append(task.Env, "COUNT="+counter)
			if err := d.add(task); err != nil {
				t.Fatal(err)
			}
			if err := d.startup(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(counter)
			if err != nil || strings.TrimSpace(string(data)) != "5" {
				t.Fatalf("readiness did not require three successes after failure: %q, %v", data, err)
			}
			if state := d.snapshot().Tasks[0]; state.State != "healthy" || state.Successes != 3 {
				t.Fatal(state)
			}
		})
	}
}

func TestRecoveryRequiresSuccessThresholdAndResetsOnFailure(t *testing.T) {
	logs := captureEvents(t)
	d := testDaemon(t)
	task := testTask("dns", "check", "true")
	task.Successes = 3
	if err := d.add(task); err != nil {
		t.Fatal(err)
	}
	d.status.Phase = "runtime"
	d.markFailure(task, os.ErrInvalid)
	for range 2 {
		if err := d.attempt(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if s := d.snapshot(); s.State != "degraded" || s.Tasks[0].State != "confirming" || s.Tasks[0].Failures != 0 {
			t.Fatal("single success recovered too early or failed to reset failure streak", s)
		}
	}
	failed := task
	failed.Command = []string{"false"}
	if err := d.attempt(t.Context(), failed); err != nil {
		if failures := d.markFailure(task, err); failures != 1 {
			t.Fatal("success did not reset consecutive failure count", failures)
		}
	} else {
		t.Fatal("expected a failed check")
	}
	if got := d.snapshot().Tasks[0].Successes; got != 0 {
		t.Fatal("failure did not reset success streak", got)
	}
	for i := range 3 {
		if err := d.attempt(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if i < 2 && d.snapshot().State != "degraded" {
			t.Fatal("recovery did not wait for the full streak")
		}
	}
	state := d.snapshot()
	if state.State != "healthy" || state.Tasks[0].Successes != 3 || state.Tasks[0].Reason != "" {
		t.Fatal("recovery did not clear previous failure", state)
	}
	recovered := 0
	for _, event := range logs.events(t) {
		if event["msg"] == "task recovered" {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatal("recovery event must occur once, after confirmation", recovered)
	}
}

func TestSuccessThresholdRespectsTaskDeadline(t *testing.T) {
	for _, policy := range []string{"degrade", "restart-container"} {
		t.Run(policy, func(t *testing.T) {
			d := testDaemon(t)
			task := testTask("ready", "check", "true")
			task.Phase, task.Successes, task.Deadline, task.Failure = "startup", 3, 150*time.Millisecond, policy
			if err := d.add(task); err != nil {
				t.Fatal(err)
			}
			err := d.startup()
			if (err != nil) != (policy == "restart-container") {
				t.Fatal("deadline did not follow configured failure policy", err)
			}
			if state := d.snapshot().Tasks[0]; state.State != "degraded" || state.Successes != 0 || !strings.Contains(state.Reason, context.DeadlineExceeded.Error()) {
				t.Fatal("partial successes were treated as ready", state)
			}
		})
	}
}

func TestSuccessThresholdConfigAndValidation(t *testing.T) {
	for _, tc := range []struct {
		kind string
		n    int
		ok   bool
	}{
		{"check", 0, false}, {"check", -1, false}, {"check", 2, true},
		{"job", 2, false}, {"process", 2, false},
	} {
		task := testTask("x", tc.kind, "true")
		task.Successes = tc.n
		if (task.Validate() == nil) != tc.ok {
			t.Fatalf("kind=%s successes=%d validation mismatch", tc.kind, tc.n)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		field string
		want  int
	}{
		{"", 1}, {`,"successes":2`, 2},
	} {
		body := `{"tasks":[{"kind":"check","name":"dns","command":["true"]` + tc.field + `}]}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		tasks, err := loadConfig(path)
		if err != nil || tasks[0].Successes != tc.want {
			t.Fatalf("config successes: %+v, %v", tasks, err)
		}
	}
}
