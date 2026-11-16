package docktinel

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serveTestAPI(t *testing.T, d *Daemon) string {
	t.Helper()
	// Keep Unix socket paths below the platform's length limit.
	dir, err := os.MkdirTemp("", "dt-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := d.controlServer()
	if server.WriteTimeout != 0 {
		t.Fatal("a global write timeout would interrupt long startup requests")
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return dir
}

func TestUnixHTTPAPI(t *testing.T) {
	d := &Daemon{status: Status{Version: 1, Phase: "configuring", State: "starting"}, states: map[string]TaskStatus{}, locks: map[string]chan struct{}{}}
	dir := serveTestAPI(t, d)
	task := testTask("dns", "check", "true")
	if err := callAPI(dir, "POST", "/v1/tasks", task, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := callAPI(dir, "POST", "/v1/tasks", task, nil, time.Second); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate registration: %v", err)
	}
	var status Status
	if err := callAPI(dir, "GET", "/v1/status", nil, &status, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(status.Tasks) != 1 || status.Tasks[0].Name != "dns" {
		t.Fatal(status)
	}
	// No status file exists: status must query the daemon rather than disk.
	var out, stderr bytes.Buffer
	if code := CLI([]string{"--runtime-dir", dir, "status", "--json"}, &out, &stderr, "test"); code != 0 {
		t.Fatalf("status: %s", &stderr)
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
}

func TestAPIRejectsInvalidRequests(t *testing.T) {
	d := &Daemon{status: Status{Phase: "configuring"}, states: map[string]TaskStatus{}, locks: map[string]chan struct{}{}}
	handler := d.controlServer().Handler
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/v1/tasks", "", 405},
		{"POST", "/v1/unknown", "{}", 404},
		{"POST", "/v1/tasks", "{", 400},
		{"POST", "/v1/tasks", "{}", 400},
		{"POST", "/v1/tasks", `{"unknown":true}`, 400},
		{"POST", "/v1/tasks", "{} {}", 400},
		{"POST", "/v1/tasks", `{"name":"` + strings.Repeat("a", maxAPIBody) + `"}`, 413},
		{"POST", "/v1/startup", "", 400},
		{"POST", "/v1/startup", `{"unknown":true}`, 400},
		{"POST", "/v1/main", "{}", 409},
		{"POST", "/v1/restart", "{}", 400},
	} {
		t.Run(tc.method+tc.path+tc.body[:min(len(tc.body), 20)], func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if w.Code != tc.code {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	if len(d.tasks) != 0 || d.status.Phase != "configuring" {
		t.Fatal("invalid requests mutated daemon state")
	}
}

func TestAPIConnectionDeadline(t *testing.T) {
	start := time.Now()
	err := callAPI(t.TempDir(), "GET", "/v1/status", nil, &Status{}, 50*time.Millisecond)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("connection deadline: %v (%s)", err, time.Since(start))
	}
}

func TestAPIStartupSurvivesClientTimeout(t *testing.T) {
	d := testDaemon(t)
	task := testTask("slow", "job", "sleep 0.3")
	task.Phase = "startup"
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	dir := serveTestAPI(t, d)
	if err := callAPI(dir, "POST", "/v1/startup", nil, nil, 100*time.Millisecond); err == nil {
		t.Fatal("client should time out while startup continues")
	}
	var status Status
	if err := callAPI(dir, "GET", "/v1/status", nil, &status, time.Second); err != nil || status.Phase != "startup" {
		t.Fatalf("status while startup runs: %+v, %v", status, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for d.snapshot().Phase != "awaiting-main" {
		select {
		case <-ctx.Done():
			t.Fatal("startup did not finish after client disconnected")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAPIStartupCanExceedRequestReadTimeout(t *testing.T) {
	d := testDaemon(t)
	task := testTask("slow", "job", "sleep 5.2")
	task.Phase = "startup"
	task.Timeout = 8 * time.Second
	task.Deadline = 10 * time.Second
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	dir := serveTestAPI(t, d)
	if err := callAPI(dir, "POST", "/v1/startup", nil, nil, 10*time.Second); err != nil {
		t.Fatalf("long startup response: %v", err)
	}
	if d.snapshot().Phase != "awaiting-main" {
		t.Fatal("startup did not finish")
	}
}
