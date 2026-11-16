package integration

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type sentinelStatus struct {
	Phase           string  `json:"phase"`
	State           string  `json:"state"`
	Heartbeat       float64 `json:"heartbeat"`
	StartupDeadline float64 `json:"startup_deadline"`
	Daemon          struct {
		PID int `json:"pid"`
	} `json:"daemon"`
}

type fixture struct {
	t   *testing.T
	cli *client.Client
	id  string
}

func newFixture(t *testing.T, script string, env ...string) *fixture {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	image := os.Getenv("DOCKTINEL_TEST_IMAGE")
	if image == "" {
		image = "docktinel-demo:local"
	}
	cfg := &container.Config{Image: image, Env: env}
	if script != "" {
		cfg.Entrypoint = []string{"sh", "-ec", script}
		cfg.Cmd = []string{}
		// Script fixtures exercise the imperative mode without the demo config.
		cfg.Env = append(cfg.Env, "DOCKTINEL_CONFIG=/tmp/test-config.json")
		script = "printf '%s' '{\"tasks\":[]}' > /tmp/test-config.json\n" + script
		cfg.Entrypoint = []string{"sh", "-ec", script}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "docktinel-test-" + uuid.New().String(), Config: cfg,
		HostConfig: &container.HostConfig{Init: new(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, cli: cli, id: created.ID}
	t.Cleanup(f.cleanup)
	if _, err := cli.ContainerStart(ctx, f.id, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) cleanup() {
	// t.Context is canceled before cleanup; cleanup must have its own budget.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if f.t.Failed() {
		logs, err := f.cli.ContainerLogs(ctx, f.id, client.ContainerLogsOptions{
			ShowStdout: true, ShowStderr: true, Tail: "100",
		})
		if err == nil {
			var output bytes.Buffer
			_, _ = stdcopy.StdCopy(&output, &output, logs)
			_ = logs.Close()
			f.t.Logf("container %s logs:\n%s", f.id, &output)
		}
	}
	if _, err := f.cli.ContainerRemove(ctx, f.id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		f.t.Errorf("remove test container %s: %v", f.id, err)
	}
}

func (f *fixture) exec(ctx context.Context, args ...string) (string, int, error) {
	created, err := f.cli.ExecCreate(ctx, f.id, client.ExecCreateOptions{
		Cmd: args, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", 0, err
	}
	stream, err := f.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", 0, err
	}
	defer stream.Close()
	// Hijacked streams need an explicit close when the test/deadline expires.
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, stream.Reader); err != nil {
		return stdout.String(), 0, err
	}
	for {
		state, err := f.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return stdout.String(), 0, err
		}
		if !state.Running {
			if state.ExitCode != 0 {
				return stdout.String(), state.ExitCode, fmt.Errorf("exec %q exited %d: %s", args, state.ExitCode, &stderr)
			}
			return stdout.String(), 0, nil
		}
		select {
		case <-ctx.Done():
			return stdout.String(), 0, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (f *fixture) mustExec(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	output, _, err := f.exec(ctx, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return output
}

func (f *fixture) status(ctx context.Context) (sentinelStatus, error) {
	output, _, err := f.exec(ctx, "docktinel", "status", "--json")
	if err != nil {
		return sentinelStatus{}, err
	}
	var s sentinelStatus
	err = json.Unmarshal([]byte(output), &s)
	return s, err
}

// eventually returns the typed successful observation, retaining the last error
// for diagnostics. Every poll shares the same bounded context.
func eventually[T any](t *testing.T, observe func(context.Context) (T, error), ready func(T) bool) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var value T
	var err error
	for {
		value, err = observe(ctx)
		if err == nil && ready(value) {
			return value
		}
		select {
		case <-ctx.Done():
			t.Fatalf("condition timed out: last value=%+v, last error=%v", value, err)
			return value
		case <-ticker.C:
		}
	}
}

func (f *fixture) runtime() sentinelStatus {
	f.t.Helper()
	state := eventually(f.t, f.status, func(s sentinelStatus) bool { return s.Phase == "runtime" })
	// The live HTTP API can lead the independent published health snapshot.
	eventually(f.t, func(ctx context.Context) (bool, error) {
		_, code, err := f.exec(ctx, "docktinel", "healthcheck", "--no-kill")
		if code != 0 {
			return false, nil
		}
		return err == nil, err
	}, func(healthy bool) bool { return healthy })
	return state
}

func (f *fixture) waitExit() int64 {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 15*time.Second)
	defer cancel()
	wait := f.cli.ContainerWait(ctx, f.id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-wait.Result:
		if result.Error != nil {
			f.t.Fatal(result.Error.Message)
		}
		return result.StatusCode
	case err := <-wait.Error:
		f.t.Fatal(err)
	case <-ctx.Done():
		f.t.Fatal(ctx.Err())
	}
	return 0
}

func (f *fixture) expectUnhealthy(args ...string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	_, code, err := f.exec(ctx, append([]string{"docktinel", "healthcheck"}, args...)...)
	if err != nil && code != 0 {
		return
	}
	// If healthcheck terminates the main process, Docker may close the exec
	// stream before it can report a code. Require container exit in that case.
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		f.waitExit()
		return
	}
	f.t.Fatalf("expected healthcheck failure: exit=%d, error=%v", code, err)
}

func TestCriticalCheckStopsContainer(t *testing.T) {
	f := newFixture(t, "")
	f.runtime()
	f.mustExec("docktinel", "healthcheck")
	f.mustExec("touch", "/srv/fail-health")
	f.waitExit()
}

func TestRestartRequestStopsContainer(t *testing.T) {
	f := newFixture(t, "")
	f.runtime()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// The namespace may exit before Docker can report the exec's exit status.
	_, _, _ = f.exec(ctx, "docktinel", "restart", "--reason", "HTTP API integration test")
	f.waitExit()
	logs, err := f.cli.ContainerLogs(ctx, f.id, client.ContainerLogsOptions{ShowStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "HTTP API integration test") {
		t.Fatalf("restart reason missing from logs: %s", &output)
	}
}

func TestDegradedDependencyRecovers(t *testing.T) {
	f := newFixture(t, `
		docktinel daemon --heartbeat 100ms &
		docktinel add check optional --phase both --deadline 200ms --interval 100ms \
			--failure degrade -- test -f /tmp/available
		exec docktinel startup --timeout 10s -- demo wait
	`)
	f.runtime()
	eventually(t, f.status, func(s sentinelStatus) bool { return s.State == "degraded" })
	f.mustExec("docktinel", "healthcheck")
	f.mustExec("touch", "/tmp/available")
	eventually(t, f.status, func(s sentinelStatus) bool { return s.State == "healthy" })
}

func TestStartupDeadlineStopsUnfinishedEntrypoint(t *testing.T) {
	f := newFixture(t, `
		docktinel daemon --heartbeat 100ms --stop-grace 200ms &
		docktinel add job blocked --phase startup --timeout 1m -- sleep 600
		exec docktinel startup --timeout 300ms -- demo wait
	`)
	f.waitExit()
}

func TestLostSnapshotStopsMain(t *testing.T) {
	f := newFixture(t, "")
	s := f.runtime()
	f.mustExec("kill", "-STOP", strconv.Itoa(s.Daemon.PID))
	f.mustExec("rm", "/run/docktinel/status.json")
	f.expectUnhealthy()
	f.waitExit()
}

func TestFrozenWatchdogEscalatesTERMToKILL(t *testing.T) {
	f := newFixture(t, `
		docktinel daemon --heartbeat 100ms --stop-grace 200ms &
		exec docktinel startup --timeout 10s -- demo wait --ignore-term
	`)
	s := f.runtime()
	// The fixture writes this only after installing its TERM handler.
	eventually(t, func(ctx context.Context) (int, error) {
		_, code, err := f.exec(ctx, "test", "-f", "/tmp/main-ready")
		return code, err
	}, func(code int) bool { return code == 0 })
	f.mustExec("kill", "-STOP", strconv.Itoa(s.Daemon.PID))
	// Wait for the actual stale-heartbeat condition rather than sleeping an
	// arbitrary duration. The snapshot remains readable while the daemon is frozen.
	eventually(t, func(ctx context.Context) (bool, error) {
		_, code, err := f.exec(ctx, "docktinel", "healthcheck", "--max-age", "200ms", "--no-kill")
		if code != 0 {
			return true, nil
		}
		return false, err
	}, func(stale bool) bool { return stale })
	f.expectUnhealthy("--max-age", "200ms", "--stop-grace", "200ms")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	state, err := f.cli.ContainerInspect(ctx, f.id, client.ContainerInspectOptions{})
	if err != nil || !state.Container.State.Running {
		t.Fatalf("TERM grace was not respected: %v", err)
	}
	// Repeated probes exercise escalation across independent invocations.
	eventually(t, func(ctx context.Context) (bool, error) {
		_, _, _ = f.exec(ctx, "docktinel", "healthcheck", "--max-age", "200ms", "--stop-grace", "200ms")
		state, err := f.cli.ContainerInspect(ctx, f.id, client.ContainerInspectOptions{})
		return err == nil && !state.Container.State.Running, err
	}, func(stopped bool) bool { return stopped })
	if code := f.waitExit(); code != 137 {
		t.Fatalf("expected KILL exit 137, got %d", code)
	}
}

func TestDefaultConfigAndENV(t *testing.T) {
	f := newFixture(t, "", "DEMO_MESSAGE=Configured through ENV", "CHECK_INTERVAL=200ms", "DOCKTINEL_STARTUP_TIMEOUT=20s", "DOCKTINEL_RUNTIME_DIR=/tmp/docktinel-runtime")
	state := f.runtime()
	if remaining := state.StartupDeadline - state.Heartbeat; remaining <= 0 || remaining > 20.1 {
		t.Fatalf("startup timeout ENV was not applied: %+v", state)
	}
	if output := f.mustExec("cat", "/srv/index.html"); output != "Configured through ENV\n" {
		t.Fatalf("config job did not inherit ENV: %q", output)
	}
	f.mustExec("docktinel", "healthcheck")
}

func TestAutoStartExecAliasWithConfigENV(t *testing.T) {
	f := newFixture(t, `
		printf '%s' '{"tasks":[{"kind":"job","name":"env","phase":"startup","command":["touch","${MARKER}"]}]}' > "$DOCKTINEL_CONFIG"
		exec docktinel exec --timeout 10s -- demo wait
	`, "MARKER=/tmp/from-config")
	f.runtime()
	f.mustExec("test", "-f", "/tmp/from-config")
}

func TestCheckOnlyStartupThenExec(t *testing.T) {
	f := newFixture(t, `
		docktinel startup --timeout 10s
		docktinel status --json > /tmp/before-attach.json
		exec docktinel exec -- demo wait
	`)
	f.runtime()
	if output := f.mustExec("cat", "/tmp/before-attach.json"); !strings.Contains(output, "awaiting-main") {
		t.Fatalf("check-only startup did not finish: %s", output)
	}
}

func TestConfigDuplicateWithAddFails(t *testing.T) {
	f := newFixture(t, `
		printf '%s' '{"tasks":[{"kind":"job","name":"one","command":["true"]}]}' > "$DOCKTINEL_CONFIG"
		docktinel daemon &
		if docktinel add job one -- true; then exit 42; fi
		exec docktinel startup --timeout 10s -- demo wait
	`)
	f.runtime()
}

func TestConfigErrorsPreventApplicationLaunch(t *testing.T) {
	for _, config := range []string{
		`{"tasks":[{"kind":"job","name":"x","command":["${DOCKTINEL_MISSING_TEST_ENV}"]}]}`,
		`{"tasks":[],"unknown":true}`,
	} {
		t.Run(config, func(t *testing.T) {
			f := newFixture(t, "printf '%s' '"+config+"' > \"$DOCKTINEL_CONFIG\"\nexec docktinel startup -- demo wait")
			if code := f.waitExit(); code == 0 {
				t.Fatal("invalid config allowed successful startup")
			}
		})
	}
}

func TestFrozenDaemonIsNotReplacedByStartup(t *testing.T) {
	f := newFixture(t, "")
	s := f.runtime()
	f.mustExec("kill", "-STOP", strconv.Itoa(s.Daemon.PID))
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	_, code, err := f.exec(ctx, "docktinel", "startup", "--timeout", "1s")
	if code == 0 || err == nil {
		t.Fatal("startup should report a live but unresponsive daemon")
	}
	output := f.mustExec("cat", "/run/docktinel/status.json")
	var snapshot sentinelStatus
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil || snapshot.Daemon.PID != s.Daemon.PID {
		t.Fatalf("daemon identity changed: %+v, %v", snapshot, err)
	}
	// Resume before normal cleanup so it can reap its child processes.
	f.mustExec("kill", "-CONT", strconv.Itoa(s.Daemon.PID))
}

func TestConcurrentStartupLaunchesSingleDaemon(t *testing.T) {
	f := newFixture(t, `
		docktinel startup --timeout 10s > /tmp/start-one 2>&1 & one=$!
		docktinel startup --timeout 10s > /tmp/start-two 2>&1 & two=$!
		if wait "$one"; then a=0; else a=1; fi
		if wait "$two"; then b=0; else b=1; fi
		test "$((a+b))" -eq 1
		exec docktinel startup -- demo wait
	`)
	f.runtime()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	processes, err := f.cli.ContainerTop(ctx, f.id, client.ContainerTopOptions{Arguments: []string{"-eo", "pid,comm,args"}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, process := range processes.Processes {
		if len(process) == 3 && process[1] == "docktinel" && strings.Contains(process[2], " daemon ") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one daemon, got %d: %+v", count, processes)
	}
}

func TestStartupTimeoutENVAndCLIOverride(t *testing.T) {
	f := newFixture(t, `
		printf '%s' '{"tasks":[{"kind":"job","name":"slow","phase":"startup","command":["sleep","0.2"]}]}' > "$DOCKTINEL_CONFIG"
		exec docktinel startup --timeout 10s -- demo wait
	`, "DOCKTINEL_STARTUP_TIMEOUT=invalid")
	f.runtime()
}

func TestMissingExplicitConfigFailsStartup(t *testing.T) {
	f := newFixture(t, `
		export DOCKTINEL_CONFIG=/tmp/does-not-exist.json
		exec docktinel startup -- demo wait
	`)
	if code := f.waitExit(); code == 0 {
		t.Fatal("missing explicit config did not fail startup")
	}
}

func TestStartupAndExecRejectMissingDefaultConfig(t *testing.T) {
	f := newFixture(t, `
		rm -f /etc/docktinel/config.json
		unset DOCKTINEL_CONFIG
		for command in startup exec; do
			if docktinel "$command"; then exit 42; fi
			if docktinel "$command" -- demo wait; then exit 42; fi
			test ! -e /run/docktinel/control.sock
			test ! -e /run/docktinel/status.json
		done
	`)
	if code := f.waitExit(); code != 0 {
		t.Fatalf("no-config startup checks exited %d", code)
	}
}

func TestExplicitDaemonSupportsCLIWithoutConfig(t *testing.T) {
	f := newFixture(t, `
		rm -f /etc/docktinel/config.json
		unset DOCKTINEL_CONFIG
		docktinel daemon --heartbeat 100ms &
		docktinel add job marker --phase startup -- touch /tmp/registered-with-add
		docktinel startup --timeout 10s
		exec docktinel exec -- demo wait
	`)
	f.runtime()
	f.mustExec("test", "-f", "/tmp/registered-with-add")
	f.mustExec("docktinel", "healthcheck")
}

func TestClientCommandsFailWithoutDaemonEvenWithConfig(t *testing.T) {
	// The fixture creates a valid config. Ordinary clients must still never
	// auto-start a daemon simply because that file exists.
	f := newFixture(t, `exec demo wait`)
	eventually(t, func(ctx context.Context) (int, error) {
		_, code, err := f.exec(ctx, "test", "-f", "/tmp/main-ready")
		return code, err
	}, func(code int) bool { return code == 0 })
	for _, args := range [][]string{
		{"status", "--json"},
		{"add", "job", "unused", "--", "true"},
		{"restart", "--reason", "must not launch a daemon"},
		{"healthcheck", "--no-kill"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
		_, code, err := f.exec(ctx, append([]string{"docktinel"}, args...)...)
		cancel()
		if code == 0 || err == nil {
			t.Fatalf("command %v succeeded without a daemon: exit=%d error=%v", args, code, err)
		}
		f.mustExec("test", "!", "-e", "/run/docktinel/control.sock")
		f.mustExec("test", "!", "-e", "/run/docktinel/status.json")
	}
}

func TestHelperRestartsWithoutRestartingContainer(t *testing.T) {
	f := newFixture(t, `
		docktinel daemon --heartbeat 100ms &
		docktinel add process helper --failure restart-container --on-exit restart \
			-- sh -c 'echo started >> /tmp/helper-starts'
		exec docktinel startup --timeout 10s -- demo wait
	`)
	f.runtime()
	eventually(t, func(ctx context.Context) (int, error) {
		output, _, err := f.exec(ctx, "cat", "/tmp/helper-starts")
		return strings.Count(output, "started\n"), err
	}, func(starts int) bool { return starts >= 2 })
	// The irrelevant failure flag must not make a restartable helper critical.
	f.mustExec("docktinel", "healthcheck")
	if state := f.runtime(); state.State != "healthy" {
		t.Fatalf("helper restart changed container health: %+v", state)
	}
}

func TestCriticalHelperStopsContainerOnSuccessfulExit(t *testing.T) {
	f := newFixture(t, `
		docktinel daemon --heartbeat 100ms &
		docktinel add process helper --on-exit restart-container \
			-- sh -c 'while [ ! -e /tmp/release-helper ]; do sleep 0.1; done'
		exec docktinel startup --timeout 10s -- demo wait
	`)
	f.runtime()
	f.mustExec("touch", "/tmp/release-helper")
	if code := f.waitExit(); code == 0 {
		t.Fatal("critical helper exit did not terminate the main application")
	}
}
