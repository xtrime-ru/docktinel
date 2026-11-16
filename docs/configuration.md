# Configuration and environment

The daemon reads `/etc/docktinel/config.json` if present. Select another file with
`daemon --config PATH` or `DOCKTINEL_CONFIG`. A file selected by flag or ENV is
required; the absent default file is allowed for CLI-only setups. Any existing
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
to `daemon`. Set the same runtime directory for all CLI invocations and Docker
healthcheck. Start the daemon explicitly before calling `add` or `startup`. `startup --timeout` overrides the timeout ENV setting. Startup and after-start readiness share the same deadline; attaching the main process does not reset it.

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

