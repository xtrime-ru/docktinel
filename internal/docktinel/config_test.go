package docktinel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpandConfigENV(t *testing.T) {
	t.Setenv("DNS_HOST", "a \"quoted\" host\nwith spaces")
	t.Setenv("EMPTY", "")
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{`${DNS_HOST}`, "a \"quoted\" host\nwith spaces", false},
		{`${NOT_SET_TEST:-5s}`, "5s", false},
		{`${EMPTY:-5s}`, "5s", false},
		{`${EMPTY}`, "", false},
		{`$DNS_HOST`, `$DNS_HOST`, false},
		{`$${DNS_HOST}`, `${DNS_HOST}`, false},
		{`${NOT_SET_TEST}`, "", true},
		{`${DNS_HOST`, "", true},
		{`${1BAD}`, "", true},
		{`${}`, "", true},
		{`${NOT_SET_TEST:-${DNS_HOST}}`, "", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := expandEnv(tc.input)
			if (err != nil) != tc.invalid || (!tc.invalid && got != tc.want) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestFileConfigDefaultsAndSafeENV(t *testing.T) {
	t.Setenv("DNS_HOST", "127.0.0.1\",\"command\":[\"injected\"]")
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"tasks":[{"kind":"check","name":"dns","phase":"both","command":["check-dns","${DNS_HOST}"],"timeout":"${DNS_TIMEOUT_TEST:-5s}"},{"kind":"job","name":"refresh","command":["refresh"]}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	tasks, err := loadConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || len(tasks[0].Command) != 2 || tasks[0].Command[1] != os.Getenv("DNS_HOST") {
		t.Fatal(tasks)
	}
	if tasks[0].Timeout != 5*time.Second || tasks[0].Interval != 10*time.Second || tasks[0].Failure != "restart-container" || tasks[1].Timeout != time.Minute || tasks[1].Interval != 0 {
		t.Fatal(tasks)
	}
	if tasks[0].Dir == "" || len(tasks[0].Env) == 0 {
		t.Fatal("daemon environment/cwd not captured")
	}
}

func TestConfigValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{
		`null`, `{ "typo": true }`, `{"tasks":[],"tasks":[]}`, `{} {}`,
		`{"tasks":[{"kind":"job","name":"x","command":["true"],"timeout":5}]}`,
		`{"tasks":[{"kind":"job","name":"x","command":["true"],"interval":"wrong"}]}`,
		`{"tasks":[{"kind":"job","name":"x","command":["true"],"extra":true}]}`,
		`{"tasks":[{"kind":"job","name":"x","command":["true"],"env":[]}]}`,
		`{"tasks":[{"kind":"job","name":"x","command":["true"]},{"kind":"job","name":"x","command":["true"]}]}`,
		`{"tasks":[{"kind":"check","name":"x","command":["true"],"timeout":"0s"}]}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path, true); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxAPIBody+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path, true); err == nil {
		t.Fatal("accepted oversized config")
	}
	missing := path + ".missing"
	if _, err := loadConfig(missing, false); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(missing, true); err == nil {
		t.Fatal("explicit missing config accepted")
	}
}

func TestConfigPathPrecedence(t *testing.T) {
	t.Setenv("DOCKTINEL_CONFIG", "")
	if path, required := configPath(""); path != DefaultConfig || required {
		t.Fatal(path, required)
	}
	t.Setenv("DOCKTINEL_CONFIG", "/env/config.json")
	if path, required := configPath(""); path != "/env/config.json" || !required {
		t.Fatal(path, required)
	}
	if path, required := configPath("/flag/config.json"); path != "/flag/config.json" || !required {
		t.Fatal(path, required)
	}
}

func TestStartupBudgetResetsRegistrationDeadline(t *testing.T) {
	d := testDaemon(t)
	before := d.snapshot().StartupDeadline
	if err := d.startup(20 * time.Second); err != nil {
		t.Fatal(err)
	}
	s := d.snapshot()
	if s.StartupDeadline >= before || s.StartupDeadline-s.Heartbeat > 20.1 {
		t.Fatal(s)
	}
}

func TestGlobalStartupBudgetBoundsOptionalTask(t *testing.T) {
	d := testDaemon(t)
	task := testTask("slow", "job", "sleep 5")
	task.Phase = "startup"
	task.Timeout, task.Deadline = 10*time.Second, 10*time.Second
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := d.startup(100 * time.Millisecond); err == nil {
		t.Fatal("global deadline was ignored for optional task")
	}
	if time.Since(start) > time.Second {
		t.Fatal("global deadline did not bound execution")
	}
}

func TestStartupAliasHelpAndInvalidENV(t *testing.T) {
	t.Setenv("DOCKTINEL_STARTUP_TIMEOUT", "invalid")
	for _, name := range []string{"startup", "exec"} {
		var out, stderr bytes.Buffer
		if CLI([]string{name, "--help"}, &out, &stderr, "test") != 0 {
			t.Fatal(stderr.String())
		}
		stderr.Reset()
		if CLI([]string{name}, &out, &stderr, "test") == 0 || !strings.Contains(stderr.String(), "DOCKTINEL_STARTUP_TIMEOUT") {
			t.Fatal(stderr.String())
		}
		stderr.Reset()
		// An explicit timeout overrides invalid ENV and validates before launch.
		if CLI([]string{name, "--timeout", "0s"}, &out, &stderr, "test") == 0 || strings.Contains(stderr.String(), "DOCKTINEL_STARTUP_TIMEOUT") {
			t.Fatal(stderr.String())
		}
	}
}

func TestAfterStartSharesOriginalBudget(t *testing.T) {
	d := testDaemon(t)
	task := testTask("ready", "check", "sleep 5")
	task.Phase = "after-start"
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	if err := d.startup(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := d.runPhase("after-start", d.tasks); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}
