package docktinel

import (
	"bytes"
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
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return dir
}

func TestUnixHTTPAPI(t *testing.T) {
	d := testDaemon(t)
	task := testTask("dns", "check", "true")
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	dir := serveTestAPI(t, d)
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
		{"POST", "/v1/status", "{}", 405},
		{"POST", "/v1/tasks", "{}", 404},
		{"POST", "/v1/startup", "{}", 404},
		{"POST", "/v1/main", "{}", 404},
		{"POST", "/v1/restart", "{", 400},
		{"POST", "/v1/restart", "{}", 400},
		{"POST", "/v1/restart", `{"unknown":true}`, 400},
		{"POST", "/v1/restart", "{} {}", 400},
		{"POST", "/v1/restart", `{"reason":"` + strings.Repeat("a", maxJSONSize) + `"}`, 413},
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
