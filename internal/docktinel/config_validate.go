package docktinel

import (
	"flag"
	"fmt"
	"io"
)

const configHelp = `Usage:  docktinel config COMMAND

Inspect configuration without starting a daemon or running tasks.

Commands:
  validate  Validate a configuration file and its ENV substitutions

Run 'docktinel config COMMAND --help' for more information on a command.
`

func configCLI(args []string, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprint(out, configHelp)
		return flag.ErrHelp
	}
	if args[0] != "validate" {
		return &usageError{fmt.Errorf("unknown config command %q", args[0]), "docktinel config", "COMMAND"}
	}
	u := newCLIUsage("docktinel config validate", "[OPTIONS]", "Validate configuration and ENV without starting a daemon or executing tasks.", out)
	pathFlag := u.flags.String("config", "", "config file (DOCKTINEL_CONFIG or /etc/docktinel/config.json)")
	if err := u.parse(args[1:], false); err != nil {
		return err
	}
	path := configPath(*pathFlag)
	// Validation always requires a file, including at the default path.
	tasks, err := loadConfig(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "configuration valid (tasks: %d)\n", len(tasks))
	return nil
}
