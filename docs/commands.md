# Command reference

```text
docktinel [--runtime-dir DIR] COMMAND
```

Global flags come before the command. Flags for `add` come after the task kind and name. Durations use Go syntax such as `500ms`, `5s`, `1m`. Use `--` before the executable. Commands receive argument vectors without implicit shell expansion; use `sh -c '...'` explicitly for pipes or redirection. Registration captures the calling client's environment and working directory.

## Daemon and lifecycle

| Command | Options and defaults |
| --- | --- |
| `daemon` | `--config PATH`, `--heartbeat 5s`, `--stop-grace 10s` |
| `startup` | `--timeout 2m`, optional `-- COMMAND ARGS...` |
| `exec -- COMMAND ARGS...` | Full alias for `startup`, with identical flags and behavior |
| `restart --reason TEXT` | Cancel work, run shutdown jobs, terminate the application |
| `status` | `--json` for machine-readable output |
| `healthcheck` | `--max-age 30s`, `--stop-grace 10s`, `--startup-grace 2m`, `--no-kill` |
| `version` | Build version |

`daemon` stays in the foreground; start it with `&` in a shell entrypoint, or let `startup` launch it automatically when a config file exists. Explicit daemon startup loads the optional default config `/etc/docktinel/config.json`. `--config PATH` or `DOCKTINEL_CONFIG` selects a required file. See [configuration and ENV](configuration.md).

The daemon allows one minute to request startup. `startup --timeout` then sets a new, shared budget for startup tasks, waiting for main attachment, and after-start readiness. It is enforced by the daemon even if the client disconnects. `DOCKTINEL_STARTUP_TIMEOUT` supplies the default budget; an explicit CLI flag takes precedence. Minimum heartbeat is 100ms.

`startup` without a command runs checks and returns, leaving the daemon awaiting main attachment. A later `startup -- COMMAND` or `exec -- COMMAND` reuses that startup's deadline. Supplying a command on the first invocation performs both operations. Use `exec docktinel startup -- COMMAND` inside a shell entrypoint to preserve the main PID. With Docker array-form `ENTRYPOINT`, no outer `exec` is needed. If no daemon is running, startup requires an existing config file to launch one; without a daemon and config, both `startup` and `exec` fail without starting an application or daemon. If a daemon is already running, both commands work without a config. If its lock is held but the API is unresponsive, they fail without starting another daemon.

`add`, `status`, and `restart` require a running daemon and never auto-start it, regardless of whether a config exists. They return a nonzero exit code if connection establishment fails within their five-second client deadline. The bounded connection retry also allows `add` to race an explicitly started `daemon &`. `healthcheck` is independent of the API, never starts a daemon, and returns nonzero when the daemon is missing. `help` and `version` do not require a daemon. `daemon` is the explicit start command; it may be used without a default config to register tasks through `add`.

The daemon's stop grace is the budget for shutdown jobs together, followed by a separate application TERM grace of the same duration. Task cancellation may take another two seconds. Choose Docker's `stop_grace_period` to match your shutdown requirements. Docker healthcheck escalation has its own grace and runs across invocations, without blocking a single probe.

`--startup-grace` only allows a missing initial snapshot; a valid snapshot carries its own startup deadline. Lost or corrupt state after initialization triggers escalation. If both status and identity files are missing, healthcheck waits out its startup grace, then attempts to identify the unique direct child of PID 1; if ambiguous, it reports an error without guessing a PID.

Exit status is zero for successful CLI operations, nonzero for errors. Healthcheck returns nonzero while starting/stopping or on a critical fault; degraded runtime returns zero. `status` queries the live daemon with a five-second deadline; success means the query completed, regardless of the reported health. Healthcheck reads the atomic snapshot independently of the daemon's [local HTTP API](api.md). Task commands have no reserved special exit codes.

## Register a task

```text
docktinel add check|job|process NAME [OPTIONS] -- COMMAND [ARGS...]
```

Names must be unique, 1–64 characters, starting with a letter or digit, followed by letters, digits, `_`, `-` or `.`. All tasks must be registered before `startup`; there is no runtime mutation or configuration reload.

| Option | Check | Job | Process |
| --- | --- | --- | --- |
| `--phase` | `runtime` | `runtime` | `runtime` |
| `--interval` | `10s` | `0` (one-shot) | Not used for normal restarts |
| `--timeout` | `5s` | `1m` | `0` (unlimited) |
| `--deadline` | `1m` | `1m` | Not used |
| `--grace` | `0` | `0` | `0` |
| `--failures` | `3` | `3` | Not used |
| `--failure` | `restart-container` | `degrade` | Use `--on-exit` |
| `--on-exit` | Not used | Not used | `restart` |
| `--lock NAME` | Not allowed | Optional | Optional |

- `startup`: run before the application. Checks/jobs run sequentially and retry until successful or their deadline expires. Processes start in the background and remain supervised; add a following check if their readiness matters.
- `after-start`: run after `exec`, while the application is already running. Use for readiness probes that require its listener to exist. Runtime tasks start after this phase completes.
- `runtime`: run in the background after readiness. Checks and jobs are sequential within each task; intervals are measured after completion, not from fixed wall-clock times.
- `both`: run the same check/job during startup and runtime. A process cannot use this phase.
- `shutdown`: jobs only, run sequentially with the shared shutdown budget; no retries.

For startup/after-start, `--deadline` limits the whole task including retries; `--timeout` limits an individual attempt. A failed task retries after `--interval` (one second if zero). `--failures` applies only at runtime. Critical startup failures stop the container when the deadline expires; optional failures allow startup to continue in degraded state. A startup-only optional task has no later retry: use `both` to recover automatically.

At runtime, failed checks retry at their configured interval. Failed jobs and processes use a 10-second increasing backoff capped at one minute. A successful one-shot job stops; a failed one keeps retrying. Periodic jobs resume their configured interval after success. `--failure restart-container` restarts after the configured consecutive failure threshold. Process `--on-exit restart-container` stops the container on **any** exit, including zero. Default process policy restarts after any exit; a successful exit uses a 100ms delay.

`--grace` delays the first background execution; it does not postpone startup/after-start checks or change their deadlines. Minimum nonzero interval is 100ms. Only processes may have an unlimited timeout.

Shared locks serialize commands across tasks and are held for the command's lifetime. The timeout includes waiting for the lock. Do not give two persistent processes the same lock unless waiting is intended. A timeout cancels the task's process group; background subprocesses are also cleaned up after normal completion.

## Examples

```sh
# Wait for dependency, then keep checking it. A one-minute startup retry budget.
docktinel add check dns --phase both --deadline 1m --timeout 5s \
    --interval 5s -- /opt/bin/check-dns

# Readiness after application launch.
docktinel add check ready --phase after-start --deadline 30s \
    --interval 1s -- /opt/bin/check-local-service

# Optional refresh, serialized with other configuration updates.
docktinel add job refresh --interval 1m --lock config \
    --timeout 20s -- /opt/bin/refresh-state

# Helper process: exits cause only the helper to restart.
docktinel add process helper --phase startup -- /opt/bin/helper --foreground

# Controlled cleanup before a Docktinel-initiated restart.
docktinel add job cleanup --phase shutdown --timeout 5s -- /opt/bin/cleanup

# A maintenance script requests a restart explicitly if reinitialization needs it.
docktinel restart --reason 'DNS endpoints changed; reload unavailable'
```
