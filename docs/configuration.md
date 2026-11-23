# Configuration and environment

The daemon reads `/etc/docktinel/config.json` once at startup. Select another
file with `daemon --config PATH` or `DOCKTINEL_CONFIG`. The selected file must
exist and pass validation. Duplicate object members, unknown fields, trailing
JSON and duplicate task names are errors. Maximum file size is 1 MiB.

```json
{
  "tasks": [
    {
      "kind": "check",
      "name": "dns",
      "phase": "both",
      "command": ["/opt/bin/check-dns", "${DNS_SERVER}"],
      "timeout": "${DNS_TIMEOUT:-5s}",
      "interval": "5s",
      "successes": 2
    },
    {
      "kind": "job",
      "name": "refresh",
      "command": ["/opt/bin/refresh-state"],
      "interval": "30s"
    }
  ]
}
```

Each task requires `kind`, `name`, and a nonempty `command`. See
[task options and defaults](commands.md#task-options). Durations are strings
such as `500ms`, `5s`, `1m`, or `0s`.

Commands are argument arrays. Tasks inherit the daemon's environment and working
directory. Use an explicit shell for shell syntax. Executables are resolved
using the task's `PATH`, with relative entries resolved in its working directory.

## ENV settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `DOCKTINEL_CONFIG` | `/etc/docktinel/config.json` | Config file loaded by daemon |
| `DOCKTINEL_RUNTIME_DIR` | `/run/docktinel` | Socket, status, and locks |
| `DOCKTINEL_STARTUP_TIMEOUT` | `2m` | Shared startup and after-start budget |

The daemon's `--config` and `--timeout` flags override the corresponding ENV
settings. Set the same runtime directory for all commands and Docker healthcheck.
For non-root containers, use a directory writable by the container user.

## ENV substitution

All string **values** in the config support:

- `${VAR}`: required variable; missing is an error. An explicitly empty value is allowed.
- `${VAR:-default}`: use the default when the variable is absent or empty.
- `$$`: a literal dollar sign, for example `$${VAR}` yields literal `${VAR}`.

Bare `$VAR` remains unchanged, allowing explicit shell commands to read their
own environment. Variable names follow `[A-Za-z_][A-Za-z0-9_]*`. Defaults are
literal strings; nested expressions are not supported. JSON object keys are not
expanded, and substituted values are not expanded again.

Expansion happens after JSON parsing, once at daemon startup. Quotes, whitespace
and newlines from ENV remain inside their original string; they cannot inject
JSON properties or split a command argument. Numeric/boolean config properties
remain JSON numbers/booleans: substitution does not convert their types.

## Container entrypoint

```dockerfile
COPY docktinel.json /etc/docktinel/config.json
ENV DOCKTINEL_STARTUP_TIMEOUT=30s
ENTRYPOINT ["docktinel", "daemon"]
```

Enable Docker init with Compose `init: true` or `docker run --init`. Docktinel
checks that PID 1 is `docker-init` and that Docktinel is its direct child.
A shell entrypoint can prepare configuration and then replace itself with
the daemon:

```sh
exec docktinel daemon --config /custom/config.json
```

The daemon starts the application from a `process` task in phase `startup`.
List prerequisite startup jobs and checks before the process. Use after-start
checks for readiness and `on_exit: restart-container` for the main application.

To pass the application command directly, use `exec docktinel exec -- application`
in a shell entrypoint or `ENTRYPOINT ["docktinel", "exec", "--"]` with a Docker
`CMD`. Docktinel adds a startup process named `application` after the configured
startup tasks. Its exit triggers daemon shutdown. A config file is still
required. Use `{"tasks":[]}` when the application needs no additional tasks.

To migrate a legacy shell entrypoint, remove its background daemon launch and
finish with `exec docktinel exec -- COMMAND`. Existing `add` commands prepare the
file before that line. A fully configured JSON entrypoint can use
`exec docktinel daemon`.

After launch, update the config and restart the container to apply changes.
If the container already sees the updated config and has a Docker restart
policy, you can request an orderly restart with
`docktinel restart --reason "configuration updated"`. Recreate the container
when changing its environment or image.

## Configure with the CLI

```sh
docktinel add check dns --phase both --interval 5s -- /opt/bin/check-dns
docktinel add process app --phase startup --on-exit restart-container -- application --foreground
```

`add` creates the config file if necessary and appends new tasks in command
order. Use `--config` or `DOCKTINEL_CONFIG` to select the file. If the name
already exists, `add` replaces the task in place. Omitted flags use their
defaults. Repeating the same task leaves the file unchanged, so an entrypoint
can run the same `add` commands on every restart. Tasks inherit the daemon's
environment and working directory.

An entrypoint that generates the entire config can set
`DOCKTINEL_CONFIG=/dev/shm/docktinel.json` to rebuild it on each launch with
Docker's default shared memory mount.

You can use `add` with a running daemon. It updates the file for the next
launch. CLI arguments are stored literally. Existing JSON entries retain their
ENV substitutions.

## Validate before launch

```sh
docktinel config validate --config /etc/docktinel/config.json
```

Without `--config`, validation uses `DOCKTINEL_CONFIG`, then the default path.
The selected file must exist. Validation uses exactly the daemon's loader:
JSON schema, ENV expansion, task defaults, durations, task names and duplicate
names are checked. Exit status is zero for valid configuration, nonzero otherwise.
It does not start a daemon, create runtime files, execute commands, resolve
executables or contact dependencies. Run it in the intended container environment
so ENV substitutions match deployment. Output contains a task count, not expanded
configuration or environment values.
