package docktinel

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
)

const addHelp = `Usage:  docktinel add COMMAND

Add or update a task in the config file.

Commands:
  check     Check a dependency or application
  job       Run a one-shot or periodic command
  process   Supervise a foreground helper process

Run 'docktinel add COMMAND --help' for more information on a command.
`

func addCLI(args []string, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprint(out, addHelp)
		return flag.ErrHelp
	}
	kind := args[0]
	if kind != "check" && kind != "job" && kind != "process" {
		return &usageError{fmt.Errorf("unknown task kind %q", kind), "docktinel add", "COMMAND"}
	}
	name := ""
	rest := args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	t := taskDefaults(kind, name)
	u := newCLIUsage("docktinel add "+kind, "NAME [OPTIONS] -- COMMAND [ARG...]", "Add or update a "+kind+" task in the config file.", out)
	f := u.flags
	path := f.String("config", "", "config file (DOCKTINEL_CONFIG or /etc/docktinel/config.json)")
	f.StringVar(&t.Phase, "phase", t.Phase, "startup, after-start, runtime, shutdown, or both")
	f.DurationVar(&t.Interval, "interval", t.Interval, "check interval or delay after job completion; zero is one-shot")
	f.DurationVar(&t.Timeout, "timeout", t.Timeout, "execution timeout; zero is allowed only for processes")
	f.DurationVar(&t.Deadline, "deadline", t.Deadline, "overall retry budget for startup and after-start tasks")
	f.DurationVar(&t.Grace, "grace", t.Grace, "delay before first background execution")
	f.IntVar(&t.Failures, "failures", t.Failures, "consecutive failures before restarting the container")
	f.IntVar(&t.Successes, "successes", t.Successes, "consecutive successful checks required for readiness or recovery")
	f.StringVar(&t.Failure, "failure", t.Failure, "degrade or restart-container")
	f.StringVar(&t.OnExit, "on-exit", t.OnExit, "process action: restart or restart-container")
	f.StringVar(&t.Lock, "lock", t.Lock, "serialize commands sharing this lock name (not checks)")
	// Keep accepting legacy options, but do not advertise options this kind ignores.
	hidden := []string{"on-exit"}
	if kind == "job" {
		hidden = append(hidden, "successes")
	}
	if kind == "process" {
		hidden = []string{"interval", "deadline", "failures", "successes", "failure"}
		f.Lookup("phase").Usage = "startup, after-start, or runtime"
	}
	if kind == "check" {
		hidden = append(hidden, "lock")
		f.Lookup("phase").Usage = "startup, after-start, runtime, or both"
		f.Lookup("interval").Usage = "delay between checks; at least 100ms"
	}
	for _, name := range hidden {
		f.Lookup(name).Usage = ""
	}
	if err := u.parse(rest, true); err != nil {
		return err
	}
	t.Command = f.Args()
	if t.Name == "" {
		return u.invalid(errors.New("task NAME is required"))
	}
	if err := t.Validate(); err != nil {
		return u.invalid(err)
	}
	return addConfigTask(configPath(*path), t)
}

func addConfigTask(path string, task Task) error {
	// Follow an existing symlink so the update preserves the config path.
	target, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = target
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := tryFileLock(path + ".lock")
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("config %s is being updated; retry the command", path)
	}
	if err != nil {
		return err
	}
	defer lock.Close()

	doc, err := readConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		doc = &configFile{}
	} else if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	tasks, err := doc.decodeTasks()
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	taskIndex := slices.IndexFunc(tasks, func(existing Task) bool { return existing.Name == task.Name })
	if taskIndex >= 0 && reflect.DeepEqual(tasks[taskIndex], task) {
		return nil
	}

	// Store CLI arguments literally and preserve other entries' ENV expressions.
	task.Command = slices.Clone(task.Command)
	for i, arg := range task.Command {
		task.Command[i] = strings.ReplaceAll(arg, "$", "$$")
	}
	task.Lock = strings.ReplaceAll(task.Lock, "$", "$$")
	entry, err := json.Marshal(task, configMarshalers)
	if err != nil {
		return err
	}
	if taskIndex >= 0 {
		doc.Tasks[taskIndex] = entry
	} else {
		doc.Tasks = append(doc.Tasks, entry)
	}
	data, err := json.Marshal(doc, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	if len(data)+1 > maxJSONSize {
		return errors.New("JSON document exceeds 1 MiB")
	}
	return writeJSONFile(path, data)
}
