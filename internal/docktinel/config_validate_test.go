package docktinel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidateDoesNotRunTasksOrCreateRuntime(t *testing.T) {
	dir := t.TempDir()
	path, marker, runtime := filepath.Join(dir, "config.json"), filepath.Join(dir, "ran"), filepath.Join(dir, "runtime")
	t.Setenv("VALIDATE_MARKER", marker)
	body := `{"tasks":[{"kind":"job","name":"prepare","phase":"startup","command":["touch","${VALIDATE_MARKER}"]}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := CLI([]string{"--runtime-dir", runtime, "config", "validate", "--config", path}, &out, &stderr, "test")
	if code != 0 || out.String() != "configuration valid (tasks: 1)\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, &out, &stderr)
	}
	for _, path := range []string{marker, runtime} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("validation created %s: %v", path, err)
		}
	}
}

func TestConfigValidatePathAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DOCKTINEL_CONFIG", path)
	t.Setenv("VALIDATE_UNDEFINED", "")
	if err := os.Unsetenv("VALIDATE_UNDEFINED"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"empty", `{"tasks":[]}`, 0},
		{"missing-program-allowed", `{"tasks":[{"kind":"job","name":"x","command":["/not-installed-here"]}]}`, 0},
		{"malformed", `{`, 1},
		{"unknown", `{"tasks":[],"typo":true}`, 1},
		{"duplicate", `{"tasks":[{"kind":"job","name":"x","command":["true"]},{"kind":"job","name":"x","command":["true"]}]}`, 1},
		{"invalid-duration", `{"tasks":[{"kind":"job","name":"x","command":["true"],"timeout":"wrong"}]}`, 1},
		{"missing-env", `{"tasks":[{"kind":"job","name":"x","command":["${VALIDATE_UNDEFINED}"]}]}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := CLI([]string{"config", "validate"}, &out, &stderr, "test"); code != tc.code {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, &out, &stderr)
			}
		})
	}
	var out, stderr bytes.Buffer
	if CLI([]string{"config", "validate", "--config", path + ".missing"}, &out, &stderr, "test") == 0 {
		t.Fatal("missing explicit file accepted or ENV overrode the flag")
	}
}

func TestConfigValidateHelpAndUsage(t *testing.T) {
	for _, args := range [][]string{{"config", "--help"}, {"help", "config"}, {"help", "config", "validate"}, {"config", "validate", "--help"}} {
		var out, stderr bytes.Buffer
		if CLI(args, &out, &stderr, "test") != 0 || !strings.Contains(out.String(), "Usage:") || stderr.Len() != 0 {
			t.Fatalf("%v: stdout=%q stderr=%q", args, &out, &stderr)
		}
	}
	for _, args := range [][]string{{"config", "unknown"}, {"config", "validate", "--wrong"}, {"config", "validate", "extra"}} {
		var out, stderr bytes.Buffer
		if CLI(args, &out, &stderr, "test") == 0 || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("%v: stdout=%q stderr=%q", args, &out, &stderr)
		}
	}
}
