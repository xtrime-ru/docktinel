package docktinel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoStartRequiresConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("DOCKTINEL_CONFIG", path)
	if _, err := autoStartConfig(); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "start docktinel daemon explicitly") {
		t.Fatalf("missing config: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"tasks":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := autoStartConfig(); err != nil || got != path {
		t.Fatalf("existing config: %q, %v", got, err)
	}
	t.Setenv("DOCKTINEL_CONFIG", filepath.Dir(path))
	if _, err := autoStartConfig(); err == nil {
		t.Fatal("accepted directory as auto-start config")
	}
}

func TestMissingConfigNeverLaunchesDaemon(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKTINEL_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if err := ensureDaemon(dir, os.Getpid()); err == nil || !strings.Contains(err.Error(), "cannot auto-start daemon") {
		t.Fatalf("got %v", err)
	}
	for _, file := range []string{"control.sock", "status.json", "identity.json"} {
		if _, err := os.Stat(filepath.Join(dir, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected daemon artifact %s: %v", file, err)
		}
	}
}

func TestExistingDaemonDoesNotRequireConfig(t *testing.T) {
	d := &Daemon{status: Status{Version: 1, Phase: "configuring", State: "starting"}}
	dir := serveTestAPI(t, d)
	t.Setenv("DOCKTINEL_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if err := ensureDaemon(dir, os.Getpid()); err != nil {
		t.Fatal(err)
	}
}
