package docktinel

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const Help = `Docktinel — a lightweight sentinel for Docker containers.

Usage: docktinel [--runtime-dir DIR] COMMAND

  add          Add or update a check, job, or process in the config file
  config       Validate configuration without starting a daemon
  daemon       Run configured tasks as the main process under Docker init
  exec         Run the daemon with an application command
  restart      Request an orderly container restart with --reason TEXT
  status       Query the daemon state (--json for machine-readable output)
  healthcheck  Report health and stop the daemon if the watchdog is stuck
  version      Show build version

Use COMMAND --help or help COMMAND for command options. Runtime directory defaults to
DOCKTINEL_RUNTIME_DIR or /run/docktinel. Daemon startup requires Linux and Docker
init (Compose init: true or docker run --init).
`

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
	err := command(args[0], args[1:], *dir, out, version)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		printCLIError(stderr, err)
		return 1
	}
	return 0
}

func command(name string, args []string, dir string, out io.Writer, version string) error {
	u := cliCommandHelp(name, out)
	f := u.flags
	switch name {
	case "add":
		return addCLI(args, out)
	case "config":
		return configCLI(args, out)
	case "version":
		if err := u.parse(args, false); err != nil {
			return err
		}
		fmt.Fprintln(out, version)
		return nil
	case "daemon", "exec":
		heartbeat := f.Duration("heartbeat", 5*time.Second, "state publication interval")
		config := f.String("config", "", "config file (DOCKTINEL_CONFIG or /etc/docktinel/config.json)")
		grace := f.Duration("stop-grace", 9500*time.Millisecond, "total shutdown budget including active tasks")
		timeout := f.Duration("timeout", defaultStartupTimeout, "shared startup and after-start budget (DOCKTINEL_STARTUP_TIMEOUT)")
		if err := u.parse(args, name == "exec"); err != nil {
			return err
		}
		if name == "exec" && f.NArg() == 0 {
			return u.invalid(errors.New("a command is required after --"))
		}
		explicitTimeout := false
		f.Visit(func(option *flag.Flag) { explicitTimeout = explicitTimeout || option.Name == "timeout" })
		if !explicitTimeout {
			if value, ok := os.LookupEnv("DOCKTINEL_STARTUP_TIMEOUT"); ok {
				var err error
				*timeout, err = time.ParseDuration(value)
				if err != nil || *timeout <= 0 {
					return u.invalid(errors.New("DOCKTINEL_STARTUP_TIMEOUT must be a positive duration"))
				}
			}
		}
		path := configPath(*config)
		return RunDaemon(Config{Dir: dir, Heartbeat: *heartbeat, StartupTimeout: *timeout,
			StopGrace: *grace, ConfigPath: path, Command: f.Args()})
	case "restart":
		reason := f.String("reason", "", "required explanation for the planned restart")
		if err := u.parse(args, false); err != nil {
			return err
		}
		if *reason == "" {
			return u.invalid(errors.New("required flag --reason is missing or empty"))
		}
		return callAPI(dir, "POST", "/v1/restart", &restartRequest{Reason: *reason}, nil, apiTimeout)
	case "status":
		asJSON := f.Bool("json", false, "print JSON status")
		if err := u.parse(args, false); err != nil {
			return err
		}
		s, err := daemonStatus(dir, apiTimeout)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.MarshalEncode(jsontext.NewEncoder(out, jsontext.WithIndent("  ")), s)
		}
		now, _ := uptime()
		fmt.Fprintf(out, "%s (%s), snapshot age %.1fs\n", s.State, s.Phase, now-s.Heartbeat)
		for _, task := range s.Tasks {
			reason := task.Reason
			if task.RequiredSuccesses > 1 {
				reason = fmt.Sprintf("%d/%d successes %s", task.Successes, task.RequiredSuccesses, reason)
			}
			fmt.Fprintf(out, "%-24s %-8s %-10s %s\n", task.Name, task.Kind, task.State, strings.TrimSpace(reason))
		}
		return nil
	case "healthcheck":
		maxAge := f.Duration("max-age", 30*time.Second, "maximum heartbeat age")
		grace := f.Duration("stop-grace", 10*time.Second, "time from watchdog stop signal to KILL across invocations")
		missing := f.Duration("startup-grace", 2*time.Minute, "allow missing status during initial container startup")
		noKill := f.Bool("no-kill", false, "diagnose without signalling the daemon")
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
