package docktinel

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

const addHelp = `Usage:  docktinel add COMMAND

Register a task before startup.

Commands:
  check     Check a dependency or application
  job       Run a one-shot or periodic command
  process   Supervise a foreground helper process

Run 'docktinel add COMMAND --help' for more information on a command.
`

type usageError struct {
	err           error
	command, args string
}

func (e *usageError) Error() string { return e.err.Error() }

// CLI usage errors print a short synopsis. Full options are only printed when
// help is requested, matching Docker's separation of errors and help output.
func printCLIError(w io.Writer, err error) {
	fmt.Fprintln(w, "docktinel:", err)
	if usage, ok := errors.AsType[*usageError](err); ok {
		fmt.Fprintf(w, "\nUsage:  %s %s\n\nSee '%s --help' for more information\n", usage.command, usage.args, usage.command)
	}
}

type cliUsage struct {
	command, args, description string
	flags                      *flag.FlagSet
	out                        io.Writer
}

func newCLIUsage(command, args, description string, out io.Writer) cliUsage {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	return cliUsage{command, args, description, f, out}
}

func cliCommandHelp(name string, out io.Writer) cliUsage {
	args, description := "[OPTIONS]", ""
	switch name {
	case "daemon":
		description = "Run the sentinel daemon in the foreground."
	case "startup", "exec":
		args = "[OPTIONS] [-- COMMAND [ARG...]]"
		description = "Connect or auto-start with config, run checks, optionally exec an application."
	case "restart":
		args = "--reason TEXT"
		description = "Request an orderly container restart."
	case "status":
		description = "Query the live daemon state."
	case "healthcheck":
		description = "Report health and stop the application if the watchdog is stuck."
	case "version":
		description = "Show build version."
	}
	return newCLIUsage("docktinel "+name, args, description, out)
}

func (u cliUsage) invalid(err error) error {
	return &usageError{err, u.command, u.args}
}

func (u cliUsage) parse(args []string, allowArgs bool) error {
	if err := u.flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			u.help()
			return err
		}
		return u.invalid(err)
	}
	if !allowArgs && u.flags.NArg() != 0 {
		return u.invalid(fmt.Errorf("unexpected argument %q", u.flags.Arg(0)))
	}
	return nil
}

func (u cliUsage) help() {
	fmt.Fprintf(u.out, "Usage:  %s %s\n\n%s\n\nOptions:\n", u.command, u.args, u.description)
	fmt.Fprintln(u.out, "  -h, --help                 Show help for this command")
	u.flags.VisitAll(func(f *flag.Flag) {
		if f.Usage == "" {
			return
		}
		name, description := flag.UnquoteUsage(f)
		label := "--" + f.Name
		if name != "" {
			label += " " + name
		}
		fmt.Fprintf(u.out, "      %-24s %s", label, description)
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(u.out, " (default %s)", f.DefValue)
		}
		fmt.Fprintln(u.out)
	})
}
