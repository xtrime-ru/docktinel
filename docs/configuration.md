# Configuration and environment

The daemon reads `/etc/docktinel/config.json` if present. Select another file with
`daemon --config PATH` or `DOCKTINEL_CONFIG`. A file selected by flag or ENV is
required. An absent default file is allowed only when you explicitly start
`docktinel daemon` for a CLI-only setup. Automatic startup requires an existing
config file, including when using the default path. Any existing
file that cannot be read, parsed or validated fails startup. Duplicate object
members, unknown fields, trailing JSON and duplicate task names are errors.
Maximum file size is 1 MiB. The file is read once, before the API accepts tasks;
there is no automatic reload.

```json
{
  "tasks": [
    {
      "kind": "check",
      "name": "dns",
      "phase": "both",
      "command": ["/opt/bin/check-dns", "${DNS_SERVER}"],
      "timeout": "${DNS_TIMEOUT:-5s}",
      "interval": "5s"
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

Task properties match `add` options: `kind`, `name`, `phase`, `command`, `interval`,
`timeout`, `deadline`, `grace`, `failures`, `failure`, `on_exit`, and `lock`.
`kind`, `name`, and a nonempty `command` are required. Omitted options use the
same [defaults as the CLI](commands.md). Durations are strings such as `500ms`,
`5s`, `1m`, or `0s`; numeric durations are not accepted in config files. The local
HTTP API continues to use integer nanoseconds.

Commands are argument arrays, without implicit shell interpretation. Tasks
inherit the daemon's environment and working directory. `env` and `dir` are not
config properties. Use an explicit shell only if your command needs shell syntax.
CLI-registered tasks continue to inherit their registering client's environment
and working directory. You may add tasks after config loading, but all names
must remain unique; registration closes when startup begins.

## ENV settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `DOCKTINEL_CONFIG` | `/etc/docktinel/config.json` | Config file loaded by daemon |
| `DOCKTINEL_RUNTIME_DIR` | `/run/docktinel` | Socket, status, and locks |
| `DOCKTINEL_STARTUP_TIMEOUT` | `2m` | Shared startup and after-start budget |

Explicit CLI flags override ENV, which overrides defaults. `--config` belongs
to `daemon`; an automatically launched daemon inherits `DOCKTINEL_CONFIG`.
`--timeout` belongs to `startup` and its `exec` alias. Set the same runtime
directory for all commands and Docker healthcheck. A non-root runtime directory
must be writable by the container user.

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

## Missing daemon or config

| Command | Daemon running | Daemon absent |
| --- | --- | --- |
| `startup` / `exec` | Use registered tasks; no file required | Auto-start only with an existing config; otherwise error |
| `add`, `status`, `restart` | Connect to daemon | Error, even if a config exists; never auto-start |
| `healthcheck` | Evaluate independent status snapshot | Report failure; never auto-start |
| `daemon` | Refuse a second instance | Explicitly start; absent default config is allowed |
| `help`, `version` | Local operation | Local operation |

For a setup without a config, start the daemon explicitly and register tasks:

```sh
#!/bin/sh
set -eu

docktinel daemon &
docktinel add check dns --phase both -- /opt/bin/check-dns
exec docktinel startup --timeout 30s -- application --foreground
```

No config file is needed in this flow. Once started, the daemon owns its task
registrations; clients do not need to reread a config. In contrast, a direct
`docktinel startup` or `docktinel exec -- application` with no daemon and no
config fails without launching an application or background daemon. A valid
empty config (`{"tasks":[]}`) is still an explicit configuration and is allowed.

Help/version are intentionally independent of daemon availability. Healthcheck
retains its independent watchdog behavior: a dead/stuck daemon is reported as
unhealthy and may cause application termination according to its existing
TERM/KILL policy; it never attempts to repair this by starting a daemon.

## One-command Docker entrypoint

```dockerfile
COPY docktinel.json /etc/docktinel/config.json
ENV DOCKTINEL_STARTUP_TIMEOUT=30s
ENTRYPOINT ["docktinel", "startup", "--"]
CMD ["application", "--foreground"]
```

Run the container with `--init` / Compose `init: true` and a restart policy.
Startup connects to a running daemon or starts one if absent **only with an
existing config file**, then waits for startup checks and replaces itself with
the application. The auto-started daemon treats that path as required: if the
file disappears before it is read, startup fails rather than launching with
no tasks. Automatic startup accepts regular files, including symlinks to
regular files. After-start checks run
in the background within the remaining budget. No shell entrypoint is required.

A shell-based setup can select a different config explicitly:

```sh
docktinel daemon --config /custom/config.json &
# Optional additional registrations here.
exec docktinel startup --timeout 30s -- application --foreground
```

Without a command, `docktinel startup` performs startup checks and returns.
The daemon awaits main attachment within the same startup budget. A subsequent
`exec docktinel startup -- application` or `exec docktinel exec -- application`
attaches without repeating checks or resetting that deadline. Both spellings
have identical behavior.

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
