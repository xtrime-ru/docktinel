package docktinel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	tasks, err := loadConfig(path)
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
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxJSONSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("accepted oversized config")
	}
	missing := path + ".missing"
	if _, err := loadConfig(missing); err == nil {
		t.Fatal("explicit missing config accepted")
	}
}

func TestConfigPathPrecedence(t *testing.T) {
	t.Setenv("DOCKTINEL_CONFIG", "")
	if path := configPath(""); path != DefaultConfig {
		t.Fatal(path)
	}
	t.Setenv("DOCKTINEL_CONFIG", "/env/config.json")
	if path := configPath(""); path != "/env/config.json" {
		t.Fatal(path)
	}
	if path := configPath("/flag/config.json"); path != "/flag/config.json" {
		t.Fatal(path)
	}
}

func TestGlobalStartupBudgetBoundsOptionalTask(t *testing.T) {
	d := testDaemon(t)
	d.status.StartupDeadline -= (time.Minute - 100*time.Millisecond).Seconds()
	task := testTask("slow", "job", "sleep 5")
	task.Phase = "startup"
	task.Timeout, task.Deadline = 10*time.Second, 10*time.Second
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := d.startup(); err == nil {
		t.Fatal("global deadline was ignored for optional task")
	}
	if time.Since(start) > time.Second {
		t.Fatal("global deadline did not bound execution")
	}
}

func TestAfterStartSharesOriginalBudget(t *testing.T) {
	d := testDaemon(t)
	d.status.StartupDeadline -= (time.Minute - 100*time.Millisecond).Seconds()
	task := testTask("ready", "check", "sleep 5")
	task.Phase = "after-start"
	if err := d.add(&task); err != nil {
		t.Fatal(err)
	}
	if err := d.startup(); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestConfigPreservesIntegerPrecision(t *testing.T) {
	task, err := decodeConfigTask([]byte(`{"kind":"job","name":"large","command":["true"],"failures":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(task.Failures); got != "9007199254740993" {
		t.Fatalf("integer rounded to %s", got)
	}
}

func TestConfigRejectsDuplicateTaskMembers(t *testing.T) {
	_, err := decodeConfigTask([]byte(`{"kind":"job","name":"one","name":"two","command":["true"]}`))
	if err == nil {
		t.Fatal("accepted ambiguous task names")
	}
}

func TestConfigRejectsInvalidUTF8FromENV(t *testing.T) {
	t.Setenv("INVALID_UTF8", string([]byte{0xff}))
	if _, err := decodeConfigTask([]byte(`{"kind":"job","name":"bad","command":["true","${INVALID_UTF8}"]}`)); err == nil {
		t.Fatal("accepted invalid UTF-8 after ENV expansion")
	}
}

func TestTypedConfigPreservesExplicitValues(t *testing.T) {
	t.Setenv("TASK_KIND", "job")
	t.Setenv("TASK_PHASE", "startup")
	t.Setenv("TASK_TIMEOUT", "7s")
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"tasks":[{"kind":"${TASK_KIND}","name":"one","phase":"${TASK_PHASE}","command":["echo","$${LITERAL}"],"timeout":"${TASK_TIMEOUT}","interval":"0s","grace":"0s","failures":1,"failure":"restart-container","on_exit":"restart-container","lock":"state"},{"kind":"job","name":"two","command":["true"]}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	tasks, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	one := tasks[0]
	if one.Kind != "job" || one.Phase != "startup" || one.Timeout != 7*time.Second || one.Interval != 0 || one.Failures != 1 || one.Failure != "restart-container" || one.OnExit != "restart-container" || one.Lock != "state" || one.Command[1] != "${LITERAL}" {
		t.Fatalf("explicit configuration changed: %+v", one)
	}
	if &tasks[0].Env[0] != &tasks[1].Env[0] {
		t.Fatal("file tasks duplicate the immutable environment")
	}
}

func TestTypedConfigRejectsInvalidOptionalValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, extra := range []string{
		`"timeout":null`, `"timeout":"0s"`, `"failures":0`, `"failures":null`,
		`"phase":null`, `"on_exit":""`, `"deadline":"0s"`, `"grace":0`,
		`"dir":"/tmp"`, `"env":[]`,
	} {
		body := `{"tasks":[{"kind":"job","name":"x","command":["true"],` + extra + `}]}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
