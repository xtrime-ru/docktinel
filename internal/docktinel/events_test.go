package docktinel

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

type eventBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *eventBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func captureEvents(t *testing.T) *eventBuffer {
	t.Helper()
	b := new(eventBuffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(b, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return b
}

func (b *eventBuffer) events(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.Buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestHealthTransitionsAndRecoveryEvents(t *testing.T) {
	logs := captureEvents(t)
	d := &Daemon{status: Status{Phase: "runtime"}, states: map[string]TaskStatus{
		"dns":     {Name: "dns", Kind: "check", State: "healthy"},
		"refresh": {Name: "refresh", Kind: "job", State: "healthy"},
	}}
	// Ordinary successful checks must not produce transition noise.
	d.update("dns", func(s *TaskStatus) { s.State = "running" })
	d.update("dns", func(s *TaskStatus) { s.State = "healthy" })
	if events := logs.events(t); len(events) != 0 {
		t.Fatal("successful attempts generated events", events)
	}
	d.markFailure(Task{Name: "dns"}, os.ErrNotExist)
	d.markFailure(Task{Name: "refresh"}, os.ErrNotExist)
	d.update("dns", func(s *TaskStatus) { s.State = "running" })
	d.update("dns", func(s *TaskStatus) { s.State, s.Failures, s.Reason = "healthy", 0, "" })
	if d.snapshot().State != "degraded" {
		t.Fatal("recovering one task cleared another task's failure")
	}
	d.update("refresh", func(s *TaskStatus) { s.State, s.Failures, s.Reason = "healthy", 0, "" })
	var container, recovered int
	for _, e := range logs.events(t) {
		switch e["event"] {
		case "container_state_changed":
			container++
		case "task_state_changed":
			if e["msg"] == "task recovered" {
				recovered++
			}
		}
	}
	if container != 2 || recovered != 2 {
		t.Fatalf("container events=%d recovered tasks=%d: %+v", container, recovered, logs.events(t))
	}
}

func TestFailureCompletionDoesNotLogFalseHealth(t *testing.T) {
	logs := captureEvents(t)
	d := &Daemon{states: map[string]TaskStatus{"dns": {Name: "dns", Kind: "check", State: "pending"}}}
	d.update("dns", func(s *TaskStatus) { s.State = "running" })
	d.update("dns", func(s *TaskStatus) { s.LastCompleted = 1 })
	d.markFailure(Task{Name: "dns"}, os.ErrInvalid)
	for _, e := range logs.events(t) {
		if e["event"] == "task_state_changed" && e["state"] == "healthy" {
			t.Fatal("failed attempt logged healthy", e)
		}
	}
}

func TestLifecyclePhaseEvents(t *testing.T) {
	logs := captureEvents(t)
	d := testDaemon(t)
	if err := d.startup(); err != nil {
		t.Fatal(err)
	}
	d.stop("event test", false)
	<-d.done
	var phases []string
	for _, e := range logs.events(t) {
		if e["event"] == "container_state_changed" {
			phases = append(phases, e["phase"].(string))
		}
	}
	if got := strings.Join(phases, ","); got != "startup,after-start,runtime,stopping" {
		t.Fatal("unexpected phase events", got)
	}
}
