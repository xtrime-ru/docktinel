package docktinel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteUsesTaskPathAndDirectory(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "task-helper"), []byte("#!/bin/sh\ntest -x bin/task-helper && test \"$TASK_VALUE\" = ok\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// The daemon cannot find this name; only the captured task PATH can.
	for _, path := range []string{bin, "bin", "/missing:" + bin, ":bin"} {
		t.Run(path, func(t *testing.T) {
			task := Task{Command: []string{"task-helper"}, Dir: root, Env: []string{"PATH=" + path, "TASK_VALUE=ok"}}
			if _, err := execute(t.Context(), task, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskPathDoesNotFallBackToDaemonEnvironment(t *testing.T) {
	for _, env := range [][]string{{}, {"PATH=/missing"}, {"PATH=/bin", "PATH=/missing"}} {
		if _, err := taskCommandPath(Task{Command: []string{"sh"}, Env: env}); err == nil {
			t.Fatalf("used daemon PATH for env %q", env)
		}
	}
	if _, err := taskCommandPath(Task{Command: []string{"sh"}}); err != nil {
		t.Fatalf("nil environment should inherit daemon PATH: %v", err)
	}
}
