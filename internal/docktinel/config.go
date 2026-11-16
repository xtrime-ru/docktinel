package docktinel

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultConfig = "/etc/docktinel/config.json"

// Config paths from flags or ENV are explicit: a missing file is an error.
func configPath(flagPath string) (string, bool) {
	if flagPath != "" {
		return flagPath, true
	}
	if path := os.Getenv("DOCKTINEL_CONFIG"); path != "" {
		return path, true
	}
	return DefaultConfig, false
}

func taskDefaults(kind, name string) Task {
	t := Task{Name: name, Kind: kind, Phase: "runtime", Timeout: time.Minute, Deadline: time.Minute,
		Failures: 3, Failure: "degrade", OnExit: "restart"}
	switch kind {
	case "check":
		t.Interval, t.Timeout, t.Failure = 10*time.Second, 5*time.Second, "restart-container"
	case "process":
		t.Timeout = 0
	}
	return t
}

func loadConfig(path string, required bool) ([]Task, error) {
	f, err := os.Open(path)
	if !required && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAPIBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAPIBody {
		return nil, errors.New("config exceeds 1 MiB")
	}
	// Expand parsed strings, never raw JSON: quotes/newlines in ENV cannot
	// introduce JSON members or split a command argument.
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	value, err = expandConfig(value)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	data, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if value == nil {
		return nil, errors.New("config must be a JSON object")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(doc.Tasks))
	seen := make(map[string]bool)
	for i, entry := range doc.Tasks {
		kind, _ := entry["kind"].(string)
		name, _ := entry["name"].(string)
		t := taskDefaults(kind, name)
		// Task Env/Dir belong to the registering process, not the file schema.
		if _, exists := entry["env"]; exists {
			return nil, fmt.Errorf("config task %d: env is inherited from the daemon", i+1)
		}
		if _, exists := entry["dir"]; exists {
			return nil, fmt.Errorf("config task %d: working directory is inherited from the daemon", i+1)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &t, configDurations, json.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("config task %d: %w", i+1, err)
		}
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("config task %d: %w", i+1, err)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("duplicate config task %q", t.Name)
		}
		seen[t.Name] = true
		t.Env, t.Dir = os.Environ(), cwd
		tasks = append(tasks, t)
	}
	return tasks, nil
}

var configDurations = json.WithUnmarshalers(json.UnmarshalFunc(func(b []byte, d *time.Duration) error {
	var value string
	if err := json.Unmarshal(b, &value); err != nil {
		return errors.New("config durations must be strings such as 5s or 1m")
	}
	parsed, err := time.ParseDuration(value)
	if err == nil {
		*d = parsed
	}
	return err
}))

func expandConfig(value any) (any, error) {
	switch v := value.(type) {
	case string:
		return expandEnv(v)
	case []any:
		for i, item := range v {
			expanded, err := expandConfig(item)
			if err != nil {
				return nil, err
			}
			v[i] = expanded
		}
	case map[string]any:
		for key, item := range v {
			expanded, err := expandConfig(item)
			if err != nil {
				return nil, err
			}
			v[key] = expanded
		}
	}
	return value, nil
}

// Expand only ${VAR} and ${VAR:-default}; $$ escapes a literal dollar sign.
// Bare $VAR is left alone, useful for explicitly configured shell commands.
func expandEnv(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if strings.HasPrefix(value[i:], "$$") {
			out.WriteByte('$')
			i += 2
			continue
		}
		if !strings.HasPrefix(value[i:], "${") {
			out.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			return "", errors.New("unterminated ENV substitution")
		}
		expr := value[i+2 : i+2+end]
		name, fallback, hasDefault := strings.Cut(expr, ":-")
		if !validEnvName(name) || strings.Contains(fallback, "${") {
			return "", fmt.Errorf("invalid ENV substitution %s", strconv.Quote(expr))
		}
		replacement, exists := os.LookupEnv(name)
		if hasDefault && (!exists || replacement == "") {
			replacement = fallback
		} else if !exists {
			return "", fmt.Errorf("required ENV variable %s is not set", name)
		}
		out.WriteString(replacement)
		i += 3 + end
	}
	return out.String(), nil
}

func validEnvName(name string) bool {
	for i, c := range name {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return name != ""
}
