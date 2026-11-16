package docktinel

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func startupCLI(u cliUsage, args []string, dir string) error {
	f := u.flags
	// Parse CLI before ENV so an explicit flag also overrides invalid ENV.
	timeout := f.Duration("timeout", defaultStartupTimeout, "overall startup and after-start readiness budget")
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
		value, err := envDuration("DOCKTINEL_STARTUP_TIMEOUT", defaultStartupTimeout)
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
	state, err := ensureDaemon(dir, mainPID)
	if err != nil {
		return err
	}
	// A check-only startup may be followed by startup/exec with a command.
	// Reuse its original deadline; never reset the budget on attachment.
	if state.Phase != "awaiting-main" || len(argv) == 0 {
		if err := callAPI(dir, "POST", "/v1/startup", startupRequest{Timeout: *timeout}, nil, *timeout+apiTimeout); err != nil {
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
	if err := callAPI(dir, "POST", "/v1/main", &p, nil, apiTimeout); err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, present := os.LookupEnv(name)
	if !present {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}
