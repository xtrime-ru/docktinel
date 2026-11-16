# Local HTTP API

The CLI communicates with the daemon using HTTP and JSON over the Unix socket
`$DOCKTINEL_RUNTIME_DIR/control.sock` (default `/run/docktinel/control.sock`).
There is no TCP listener. The socket has mode `0600`; clients run as the daemon's
user. Access to this socket allows registering commands and stopping the
application. Keep the runtime directory private and inside the container.

| Method | Path | JSON request | Response |
| --- | --- | --- | --- |
| `GET` | `/v1/status` | None | Current status object, matching `docktinel status --json` |
| `POST` | `/v1/tasks` | Task object | `{}` after registration |
| `POST` | `/v1/startup` | `{"timeout":30000000000}` (optional; default 2m) | `{}` when startup finishes |
| `POST` | `/v1/main` | `{"pid":123,"start":"456"}` | `{}` after attaching the entrypoint |
| `POST` | `/v1/restart` | `{"reason":"DNS endpoints changed"}` | `{}` when shutdown is requested |

Successful operations return HTTP `200`. JSON is decoded with Go 1.27’s `encoding/json/v2`: duplicate object names, invalid UTF-8 and trailing values are rejected. Malformed JSON, unknown fields and invalid
task definitions return `400`; requests exceeding 1 MiB return `413`. Operations
rejected by the lifecycle (including duplicate registration or startup failure)
return `409`. These errors use `{"error":"explanation"}`. Unknown routes and
unsupported methods return standard HTTP `404` and `405` responses.

Task fields follow [the command reference](commands.md); the wire representation
is defined by `Task` in `internal/docktinel/types.go`. All duration fields are
integer nanoseconds. API callers provide the complete task, including environment
and working directory; CLI defaults are applied by the CLI. Main process identity
must match the entrypoint recorded by the daemon, using Linux `/proc/PID/stat`
start time. Prefer `docktinel startup -- COMMAND` (or its `exec` alias) for registering and replacing that process.

```sh
curl --unix-socket /run/docktinel/control.sock http://localhost/v1/status

curl --unix-socket /run/docktinel/control.sock \
    -H 'Content-Type: application/json' -d '{}' http://localhost/v1/startup
```

Startup requests wait for the daemon's startup work. The server limits request
reading to five seconds, but allows the operation itself to run until its lifecycle
deadline. The request timeout is the daemon-enforced budget shared with after-start readiness; it does not include the earlier one-minute registration window. Disconnecting or timing out the client does not cancel startup. Other
requests, including status, can run concurrently. State transitions to runtime and stopping publish the independent snapshot immediately; a failed task remains degraded until an attempt succeeds, including while a retry is running. The CLI retries connection
establishment while the daemon starts; it never retries a submitted operation.

`status` queries the daemon with a five-second client deadline. For postmortem
inspection, read the last atomic `status.json` snapshot directly. Docker
`healthcheck` always reads this file independently of the API: a frozen daemon
must still be detected and its application stopped. Live API status does not
replace or refresh that file.

HTTP API clients never auto-start a daemon. The CLI only provides automatic
startup through `startup` / `exec`, and requires an existing config file when
no daemon is running. `add`, `status`, and `restart` fail on an unavailable
socket even if a config exists. See [missing daemon or config](configuration.md#missing-daemon-or-config).

Check tasks accept `successes` (default `1` when omitted). Status includes the current `successes` streak and `required_successes` for checks. While awaiting confirmation, task state is `confirming` and runtime container health is degraded.
