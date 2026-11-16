package docktinel

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// Optional values retain the distinction between omission and explicit zero.
// Decode nested values with the same JSON/ENV rules as the containing document.
type configValue[T any] struct {
	value T
	set   bool
}

func (v *configValue[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	v.set = true
	return json.UnmarshalDecode(dec, &v.value)
}

func (v configValue[T]) apply(dst *T) {
	if v.set {
		*dst = v.value
	}
}

type fileTask struct {
	Kind      string                     `json:"kind"`
	Name      string                     `json:"name"`
	Command   []string                   `json:"command"`
	Phase     configValue[string]        `json:"phase"`
	Interval  configValue[time.Duration] `json:"interval"`
	Timeout   configValue[time.Duration] `json:"timeout"`
	Deadline  configValue[time.Duration] `json:"deadline"`
	Grace     configValue[time.Duration] `json:"grace"`
	Failures  configValue[int]           `json:"failures"`
	Successes configValue[int]           `json:"successes"`
	Failure   configValue[string]        `json:"failure"`
	OnExit    configValue[string]        `json:"on_exit"`
	Lock      configValue[string]        `json:"lock"`
}

func (f fileTask) task() Task {
	t := taskDefaults(f.Kind, f.Name)
	t.Command = f.Command
	f.Phase.apply(&t.Phase)
	f.Interval.apply(&t.Interval)
	f.Timeout.apply(&t.Timeout)
	f.Deadline.apply(&t.Deadline)
	f.Grace.apply(&t.Grace)
	f.Failures.apply(&t.Failures)
	f.Successes.apply(&t.Successes)
	f.Failure.apply(&t.Failure)
	f.OnExit.apply(&t.OnExit)
	f.Lock.apply(&t.Lock)
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
	data, err := readJSONBytes(f)
	if err != nil {
		return nil, err
	}
	var doc *struct {
		Tasks []fileTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc, configUnmarshalers, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if doc == nil {
		return nil, errors.New("config must be a JSON object")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	env := os.Environ()
	tasks := make([]Task, 0, len(doc.Tasks))
	seen := make(map[string]bool, len(doc.Tasks))
	for i, entry := range doc.Tasks {
		t := entry.task()
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("config task %d: %w", i+1, err)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("duplicate config task %q", t.Name)
		}
		seen[t.Name] = true
		t.Env, t.Dir = env, cwd
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func decodeConfigTask(data []byte) (Task, error) {
	var spec fileTask
	if err := json.Unmarshal(data, &spec, configUnmarshalers, json.RejectUnknownMembers(true)); err != nil {
		return Task{}, err
	}
	t := spec.task()
	return t, t.Validate()
}

// Expand decoded string values only, preserving JSON structure and argv bounds.
func decodeConfigString(data []byte, value *string) error {
	if err := json.Unmarshal(data, value); err != nil {
		return err
	}
	expanded, err := expandEnv(*value)
	if err != nil {
		return err
	}
	if !utf8.ValidString(expanded) {
		return errors.New("ENV expansion produced invalid UTF-8")
	}
	*value = expanded
	return nil
}

var configUnmarshalers = json.WithUnmarshalers(json.JoinUnmarshalers(
	json.UnmarshalFunc(decodeConfigString),
	json.UnmarshalFunc(func(data []byte, d *time.Duration) error {
		var value string
		if err := decodeConfigString(data, &value); err != nil {
			return err
		}
		parsed, err := time.ParseDuration(value)
		if err == nil {
			*d = parsed
		}
		return err
	}),
))

// Expand only ${VAR} and ${VAR:-default}; $$ escapes a literal dollar sign.
// Bare $VAR is left alone, useful for explicitly configured shell commands.
func expandEnv(value string) (string, error) {
	if !strings.Contains(value, "$") {
		return value, nil
	}
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
