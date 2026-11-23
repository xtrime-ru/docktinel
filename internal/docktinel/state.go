package docktinel

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func hasDockerInit() bool {
	name, err := os.ReadFile("/proc/1/comm")
	return err == nil && strings.TrimSpace(string(name)) == "docker-init"
}

// Linux boot time is shared between processes and unaffected by wall clock changes.
func uptime() (float64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("Docktinel requires Linux /proc: %w", err)
	}
	parts := strings.Fields(string(b))
	if len(parts) == 0 {
		return 0, fmt.Errorf("empty /proc/uptime")
	}
	return strconv.ParseFloat(parts[0], 64)
}

func processIdentity(pid int) (Identity, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Identity{}, err
	}
	// comm may contain spaces and parentheses; field 3 follows its final ')'.
	_, fields, found := bytes.CutLast(b, []byte(")"))
	if !found {
		return Identity{}, fmt.Errorf("invalid process stat")
	}
	f := strings.Fields(string(fields))
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return Identity{}, fmt.Errorf("process %d is not alive", pid)
	}
	return Identity{PID: pid, Start: f[19]}, nil
}

func containerIdentity() (string, error) {
	p, err := processIdentity(1)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)) + ":" + p.Start, nil
}

func sameProcess(p Identity) bool {
	if p.PID <= 1 || p.Start == "" {
		return false
	}
	current, err := processIdentity(p.PID)
	return err == nil && current == p
}

func signalProcess(p Identity, signal syscall.Signal) error {
	if !sameProcess(p) {
		return fmt.Errorf("refusing to signal missing or replaced process %d", p.PID)
	}
	return syscall.Kill(p.PID, signal)
}

func writeJSON(path string, value any) error {
	b, err := json.Marshal(value, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	return writeJSONFile(path, b)
}

func writeJSONFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// readJSON decodes a complete, bounded document and rejects trailing JSON.
func readJSON[T any](path string, opts ...json.Options) (value T, err error) {
	f, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer f.Close()
	data, err := readJSONBytes(f)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal(data, &value, opts...); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

func readStatus(dir string) (Status, error) {
	return readJSON[Status](filepath.Join(dir, "status.json"))
}
