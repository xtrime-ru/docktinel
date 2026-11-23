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

func configPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if path := os.Getenv("DOCKTINEL_CONFIG"); path != "" {
		return path
	}
	return DefaultConfig
}

type configFile struct {
	Tasks []jsontext.Value `json:"tasks"`
}

func readConfig(path string) (*configFile, error) {
	return readJSON[*configFile](path, json.RejectUnknownMembers(true))
}

func loadConfig(path string) ([]Task, error) {
	doc, err := readConfig(path)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	tasks, err := doc.decodeTasks()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	env := os.Environ()
	for i := range tasks {
		tasks[i].Env, tasks[i].Dir = env, cwd
	}
	return tasks, nil
}

func (doc *configFile) decodeTasks() ([]Task, error) {
	if doc == nil {
		return nil, errors.New("config must be a JSON object")
	}
	tasks := make([]Task, 0, len(doc.Tasks))
	seen := make(map[string]bool, len(doc.Tasks))
	for i, entry := range doc.Tasks {
		task, err := decodeConfigTask(entry)
		if err != nil {
			return nil, fmt.Errorf("config task %d: %w", i+1, err)
		}
		if seen[task.Name] {
			return nil, fmt.Errorf("duplicate config task %q", task.Name)
		}
		seen[task.Name] = true
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func decodeConfigTask(data []byte) (Task, error) {
	// Seed defaults from the kind before decoding any overrides.
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Task{}, err
	}
	kind, err := expandEnv(header.Kind)
	if err != nil {
		return Task{}, err
	}
	task := taskDefaults(kind, "")
	if err := json.Unmarshal(data, &task, configUnmarshalers, json.RejectUnknownMembers(true)); err != nil {
		return Task{}, err
	}
	return task, task.Validate()
}

var configMarshalers = json.WithMarshalers(json.MarshalFunc(func(d time.Duration) ([]byte, error) {
	return json.Marshal(d.String())
}))

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
