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
		Tasks []jsontext.Value `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if doc == nil {
		return nil, errors.New("config must be a JSON object")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(doc.Tasks))
	seen := make(map[string]bool)
	for i, entry := range doc.Tasks {
		t, err := decodeConfigTask(entry)
		if err != nil {
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

func decodeConfigTask(data []byte) (Task, error) {
	// Decode only the discriminator first so defaults are shared with add.
	// Keep raw JSON values intact: decoding through any would round integers.
	var header struct {
		Kind string         `json:"kind"`
		Env  jsontext.Value `json:"env"`
		Dir  jsontext.Value `json:"dir"`
	}
	if err := json.Unmarshal(data, &header, configUnmarshalers); err != nil {
		return Task{}, err
	}
	if header.Env != nil || header.Dir != nil {
		return Task{}, errors.New("env and dir are inherited from the daemon, not config properties")
	}
	t := taskDefaults(header.Kind, "")
	if err := json.Unmarshal(data, &t, configUnmarshalers, json.RejectUnknownMembers(true)); err != nil {
		return Task{}, err
	}
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
