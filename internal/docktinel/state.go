package docktinel

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

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
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return Identity{}, fmt.Errorf("invalid process stat")
	}
	f := strings.Fields(string(b[i+1:]))
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
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
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

func readStatus(dir string) (Status, error) {
	f, err := os.Open(filepath.Join(dir, "status.json"))
	if err != nil {
		return Status{}, err
	}
	defer f.Close()
	var s Status
	err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&s)
	return s, err
}
