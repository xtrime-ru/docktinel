package docktinel

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLIHelpAndUsage(t *testing.T) {
	t.Setenv("DOCKTINEL_STARTUP_TIMEOUT", "invalid")
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"startup", "--help"}, 0, "Usage:"},
		{[]string{"version", "--help"}, 0, "Show build version"},
		{[]string{"help", "add", "check"}, 0, "--timeout"},
		{[]string{"restart"}, 1, "required flag --reason"},
		{[]string{"add", "check"}, 1, "task NAME is required"},
		{[]string{"add", "check", "dns"}, 1, "command is required"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{"--runtime-dir", t.TempDir()}, tc.args...)
			if code := CLI(args, &out, &stderr, "test-version"); code != tc.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &stderr)
			}
			output, other := out.String(), stderr.String()
			if tc.code != 0 {
				output, other = other, output
				if !strings.Contains(output, "--help") || strings.Contains(output, "Options:") {
					t.Fatalf("expected a short usage hint: %s", output)
				}
			}
			if other != "" || !strings.Contains(output, "Usage:") || !strings.Contains(output, tc.want) {
				t.Fatalf("unexpected output: stdout=%q stderr=%q", &out, &stderr)
			}
		})
	}
}
