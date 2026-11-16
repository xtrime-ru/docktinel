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
  startup      Run startup tasks and wait for the result
  exec         Register this entrypoint PID and replace it with the application
  restart      Request an orderly container restart with --reason TEXT
  status       Query the daemon state (--json for machine-readable output)
  healthcheck  Report health; stop the application if the watchdog is stuck
  version      Show build version

Use COMMAND --help for command options. Runtime directory defaults to
DOCKTINEL_RUNTIME_DIR or /run/docktinel. Requires Linux and Docker --init.
`

func splitFields(s string) []string { return strings.Fields(s) }

func CLI(args []string, out, stderr io.Writer, version string) int {
	root := flag.NewFlagSet("docktinel", flag.ContinueOnError)
	root.SetOutput(stderr)
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
		return 1
	}
	args = root.Args()
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(out, Help)
		return 0
	}
	err := command(args[0], args[1:], *dir, out, stderr, version)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "docktinel:", err)
		return 1
	}
	return 0
}

func command(name string, args []string, dir string, out, stderr io.Writer, version string) error {
	f := flag.NewFlagSet("docktinel "+name, flag.ContinueOnError)
	f.SetOutput(stderr)
	switch name {
	case "version":
		fmt.Fprintln(out, version)
		return nil
	case "daemon":
		config := f.String("config", "", "config file (DOCKTINEL_CONFIG or /etc/docktinel/config.json)")
		heartbeat := f.Duration("heartbeat", 5*time.Second, "state publication interval")
		startup := f.Duration("startup-timeout", 2*time.Minute, "overall deadline, including registration and after-start")
		grace := f.Duration("stop-grace", 10*time.Second, "shutdown hook budget and application termination grace")
		if err := parse(f, args); err != nil {
			return err
		}
		path, required := configPath(*config)
		return RunDaemon(Config{Dir: dir, Heartbeat: *heartbeat, StartupTimeout: *startup, StopGrace: *grace, ConfigPath: path, ConfigRequired: required})
	case "add":
		return addCLI(args, dir, stderr)
	case "startup":
		timeout := f.Duration("timeout", 15*time.Minute, "maximum time for this client to wait; daemon keeps its own deadline")
		if err := parse(f, args); err != nil {
			return err
		}
		if *timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		return callAPI(dir, "POST", "/v1/startup", nil, nil, *timeout)
	case "exec":
		if err := f.Parse(args); err != nil {
			return err
		}
		argv := f.Args()
		if len(argv) == 0 {
			return errors.New("use exec docktinel exec -- COMMAND [ARGS...]")
		}
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return err
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
		if err := parse(f, args); err != nil {
			return err
		}
		return callAPI(dir, "POST", "/v1/restart", restartRequest{Reason: *reason}, nil, 5*time.Second)
	case "status":
		asJSON := f.Bool("json", false, "print JSON status")
		if err := parse(f, args); err != nil {
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
		if err := parse(f, args); err != nil {
			return err
		}
		if *maxAge <= 0 || *grace <= 0 || *missing < 0 {
			return errors.New("max-age and stop-grace must be positive; startup-grace must be nonnegative")
		}
		if err := Healthcheck(dir, *maxAge, *grace, *missing, *noKill); err != nil {
			return err
		}
		fmt.Fprintln(out, "healthy")
		return nil
	default:
		return fmt.Errorf("unknown command %q; use docktinel --help", name)
	}
}

func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	return nil
}

func addCLI(args []string, dir string, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stderr, "Usage: docktinel add check|job|process NAME [OPTIONS] -- COMMAND [ARGS...]")
		fmt.Fprintln(stderr, "Use add check NAME --help (or job/process) to see task options.")
		return flag.ErrHelp
	}
	if len(args) < 2 {
		return errors.New("usage: docktinel add check|job|process NAME [OPTIONS] -- COMMAND [ARGS...]")
	}
	t := taskDefaults(args[0], args[1])
	f := flag.NewFlagSet("docktinel add "+t.Kind+" "+t.Name, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&t.Phase, "phase", t.Phase, "startup, after-start, runtime, shutdown, or both")
	f.DurationVar(&t.Interval, "interval", t.Interval, "check interval or delay after job completion; zero is one-shot")
	f.DurationVar(&t.Timeout, "timeout", t.Timeout, "execution timeout; zero is allowed only for processes")
	f.DurationVar(&t.Deadline, "deadline", time.Minute, "overall retry budget for startup and after-start tasks")
	f.DurationVar(&t.Grace, "grace", 0, "delay before first runtime execution")
	f.IntVar(&t.Failures, "failures", 3, "consecutive failures before restarting the container")
	f.StringVar(&t.Failure, "failure", t.Failure, "degrade or restart-container")
	f.StringVar(&t.OnExit, "on-exit", "restart", "process action: restart or restart-container")
	f.StringVar(&t.Lock, "lock", "", "serialize commands sharing this lock name (not checks)")
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	t.Command = f.Args()
	if err := t.Validate(); err != nil {
		return err
	}
	t.Env = os.Environ()
	var err error
	t.Dir, err = os.Getwd()
	if err != nil {
		return err
	}
	return callAPI(dir, "POST", "/v1/tasks", &t, nil, 5*time.Second)
}
