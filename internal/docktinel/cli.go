package docktinel

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const Help = `Docktinel — a lightweight sentinel for Docker containers.

Usage: docktinel [--runtime-dir DIR] COMMAND

  daemon       Run the daemon in the foreground (start with & in entrypoint)
  add          Register a check, job, or process before startup
  startup      Connect or auto-start with config, run checks, optionally exec application
  exec         Alias for startup
  restart      Request an orderly container restart with --reason TEXT
  status       Query the daemon state (--json for machine-readable output)
  healthcheck  Report health; stop the application if the watchdog is stuck
  version      Show build version

Use COMMAND --help or help COMMAND for command options. Runtime directory defaults to
DOCKTINEL_RUNTIME_DIR or /run/docktinel. Only startup/exec auto-start a daemon,
and require a config for that. Without a config, start daemon explicitly and
register tasks with add. Requires Linux and Docker --init.
`

func splitFields(s string) []string { return strings.Fields(s) }

func CLI(args []string, out, stderr io.Writer, version string) int {
	root := flag.NewFlagSet("docktinel", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	root.Usage = func() {}
	defaultDir := os.Getenv("DOCKTINEL_RUNTIME_DIR")
	if defaultDir == "" {
		defaultDir = DefaultDir
	}
	dir := root.String("runtime-dir", defaultDir, "private runtime directory shared by daemon and clients")
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, Help)
			return 0
		}
		printCLIError(stderr, &usageError{err, "docktinel", "[--runtime-dir DIR] COMMAND"})
		return 1
	}
	args = root.Args()
	if len(args) > 0 && args[0] == "help" {
		args = args[1:]
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			args = nil
		}
		if len(args) > 0 {
			args = append(args, "--help")
		}
	}
	if len(args) == 0 {
		fmt.Fprint(out, Help)
		return 0
	}
	err := command(args[0], args[1:], *dir, out, stderr, version)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		printCLIError(stderr, err)
		return 1
	}
	return 0
}

func command(name string, args []string, dir string, out, stderr io.Writer, version string) error {
	u := cliCommandHelp(name, out)
	f := u.flags
	switch name {
	case "version":
		if err := u.parse(args, false); err != nil {
			return err
		}
		fmt.Fprintln(out, version)
		return nil
	case "daemon":
		heartbeat := f.Duration("heartbeat", 5*time.Second, "state publication interval")
		config := f.String("config", "", "config file (DOCKTINEL_CONFIG or /etc/docktinel/config.json)")
		mainPID := f.Int("main-pid", 0, "entrypoint PID (internal auto-start option)")
		grace := f.Duration("stop-grace", 10*time.Second, "shutdown hook budget and application termination grace")
		if err := u.parse(args, false); err != nil {
			return err
		}
		path, required := configPath(*config)
		return RunDaemon(Config{Dir: dir, Heartbeat: *heartbeat, RegistrationTimeout: time.Minute,
			StopGrace: *grace, ConfigPath: path, ConfigRequired: required, MainPID: *mainPID})
	case "add":
		return addCLI(args, dir, out)
	case "startup", "exec":
		// Parse CLI before ENV so an explicit flag also overrides invalid ENV.
		timeout := f.Duration("timeout", 2*time.Minute, "overall startup and after-start readiness budget")
		if err := u.parse(args, true); err != nil {
			return err
		}
		explicit := false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "timeout" {
				explicit = true
			}
		})
		if !explicit {
			value, err := envDuration("DOCKTINEL_STARTUP_TIMEOUT", 2*time.Minute)
			if err != nil {
				return err
			}
			*timeout = value
		}
		if *timeout <= 0 {
			return u.invalid(errors.New("timeout must be positive"))
		}
		argv := f.Args()
		var path string
		var err error
		mainPID := os.Getppid()
		if mainPID == 1 {
			mainPID = os.Getpid()
		}
		if len(argv) > 0 {
			path, err = exec.LookPath(argv[0])
			if err != nil {
				return err
			}
			mainPID = os.Getpid()
		}
		if err := ensureDaemon(dir, mainPID); err != nil {
			return err
		}
		var state Status
		if err := callAPI(dir, "GET", "/v1/status", nil, &state, 5*time.Second); err != nil {
			return err
		}
		// A check-only startup may be followed by startup/exec with a command.
		// Reuse its original deadline; never reset the budget on attachment.
		if state.Phase != "awaiting-main" || len(argv) == 0 {
			if err := callAPI(dir, "POST", "/v1/startup", startupRequest{Timeout: *timeout}, nil, *timeout+5*time.Second); err != nil {
				return err
			}
		}
		if len(argv) == 0 {
			return nil
		}
		p, err := processIdentity(os.Getpid())
		if err != nil {
			return err
		}
		if err := callAPI(dir, "POST", "/v1/main", &p, nil, 5*time.Second); err != nil {
			return err
		}
		return syscall.Exec(path, argv, os.Environ())
	case "restart":
		reason := f.String("reason", "", "required explanation for the planned restart")
		if err := u.parse(args, false); err != nil {
			return err
		}
		if *reason == "" {
			return u.invalid(errors.New("required flag --reason is missing or empty"))
		}
		return callAPI(dir, "POST", "/v1/restart", restartRequest{Reason: *reason}, nil, 5*time.Second)
	case "status":
		asJSON := f.Bool("json", false, "print JSON status")
		if err := u.parse(args, false); err != nil {
			return err
		}
		var s Status
		if err := callAPI(dir, "GET", "/v1/status", nil, &s, 5*time.Second); err != nil {
			return err
		}
		if *asJSON {
			return json.MarshalEncode(jsontext.NewEncoder(out, jsontext.WithIndent("  ")), s)
		}
		now, _ := uptime()
		fmt.Fprintf(out, "%s (%s), snapshot age %.1fs\n", s.State, s.Phase, now-s.Heartbeat)
		for _, task := range s.Tasks {
			fmt.Fprintf(out, "%-24s %-8s %-10s %s\n", task.Name, task.Kind, task.State, task.Reason)
		}
		return nil
	case "healthcheck":
		maxAge := f.Duration("max-age", 30*time.Second, "maximum heartbeat age")
		grace := f.Duration("stop-grace", 10*time.Second, "time from emergency TERM to KILL across invocations")
		missing := f.Duration("startup-grace", 2*time.Minute, "allow missing status during initial container startup")
		noKill := f.Bool("no-kill", false, "diagnose without signalling the application")
		if err := u.parse(args, false); err != nil {
			return err
		}
		if *maxAge <= 0 || *grace <= 0 || *missing < 0 {
			return u.invalid(errors.New("max-age and stop-grace must be positive; startup-grace must be nonnegative"))
		}
		if err := Healthcheck(dir, *maxAge, *grace, *missing, *noKill); err != nil {
			return err
		}
		fmt.Fprintln(out, "healthy")
		return nil
	default:
		return &usageError{fmt.Errorf("unknown command %q", name), "docktinel", "[--runtime-dir DIR] COMMAND"}
	}
}

func addCLI(args []string, dir string, out io.Writer) error {
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
	u := newCLIUsage("docktinel add "+kind, "NAME [OPTIONS] -- COMMAND [ARG...]", "Register a "+kind+" task before startup.", out)
	f := u.flags
	f.StringVar(&t.Phase, "phase", t.Phase, "startup, after-start, runtime, shutdown, or both")
	f.DurationVar(&t.Interval, "interval", t.Interval, "check interval or delay after job completion; zero is one-shot")
	f.DurationVar(&t.Timeout, "timeout", t.Timeout, "execution timeout; zero is allowed only for processes")
	f.DurationVar(&t.Deadline, "deadline", time.Minute, "overall retry budget for startup and after-start tasks")
	f.DurationVar(&t.Grace, "grace", 0, "delay before first runtime execution")
	f.IntVar(&t.Failures, "failures", 3, "consecutive failures before restarting the container")
	f.StringVar(&t.Failure, "failure", t.Failure, "degrade or restart-container")
	f.StringVar(&t.OnExit, "on-exit", "restart", "process action: restart or restart-container")
	f.StringVar(&t.Lock, "lock", "", "serialize commands sharing this lock name (not checks)")
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
	t.Env = os.Environ()
	var err error
	t.Dir, err = os.Getwd()
	if err != nil {
		return err
	}
	return callAPI(dir, "POST", "/v1/tasks", &t, nil, 5*time.Second)
}
