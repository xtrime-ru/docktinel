package docktinel

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHelperHealthRecoversOnRestart(t *testing.T) {
	for _, code := range []string{"0", "1"} {
		t.Run(code, func(t *testing.T) {
			logs := captureEvents(t)
			d := testDaemon(t)
			marker := filepath.Join(t.TempDir(), "started")
			task := testTask("helper", "process", `if [ ! -e "$MARKER" ]; then touch "$MARKER"; exit `+code+`; fi; sleep 60`)
			task.Timeout = 0
			task.Env = append(task.Env, "MARKER="+marker)
			if err := d.add(&task); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			d.status.Phase = "runtime"
			d.mu.Unlock()
			d.launch(task)
			deadline := time.Now().Add(4 * time.Second)
			sawRestart := false
			for time.Now().Before(deadline) {
				s := d.snapshot()
				state := s.Tasks[0]
				if state.State == "restarting" {
					sawRestart = true
					if s.State != "degraded" || state.Process != nil {
						t.Fatalf("exited helper reported healthy: %+v", s)
					}
				}
				if sawRestart && state.Process != nil {
					if s.State != "healthy" || state.Failures != 0 || state.Reason != "" || state.State != "running" {
						t.Fatalf("live helper did not recover: %+v", s)
					}
					for _, e := range logs.events(t) {
						if e["msg"] == "task recovered" {
							return
						}
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("helper did not exit and restart")
		})
	}
}
