# Command reference

```text
docktinel [--runtime-dir DIR] COMMAND
```

Global flags come before the command. Flags for `add` come after the task kind and name. Durations use Go syntax such as `500ms`, `5s`, `1m`. Use `--` before the executable. Commands receive argument vectors without implicit shell expansion; use `sh -c '...'` explicitly for pipes or redirection. Registration captures the calling client's environment and working directory.

## Daemon and lifecycle

| Command | Options and defaults |
| --- | --- |
| `config validate` | `--config PATH` (or `DOCKTINEL_CONFIG` / default path); no daemon required |
| `daemon` | `--config PATH`, `--heartbeat 5s`, `--stop-grace 10s` |
| `startup` | `--timeout 2m`, optional `-- COMMAND ARGS...` |
| `exec -- COMMAND ARGS...` | Full alias for `startup`, with identical flags and behavior |
| `restart --reason TEXT` | Cancel work, run shutdown jobs, terminate the application |
| `status` | `--json` for machine-readable output |
| `healthcheck` | `--max-age 30s`, `--stop-grace 10s`, `--startup-grace 2m`, `--no-kill` |
| `version` | Build version |

`daemon` stays in the foreground. Start it with `&` for CLI registration, or
let `startup` launch it from a config. `startup -- COMMAND` runs startup tasks
and replaces itself with the application; `exec` is an alias. See
[startup modes and deadlines](configuration.md#missing-daemon-or-config) for config
requirements, the shared startup budget, and separate check/attach invocations.
Minimum heartbeat is 100ms.

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
| `--interval` | `10s` | `0` (one-shot) | — |
| `--timeout` | `5s` | `1m` | `0` (unlimited) |
| `--deadline` | `1m` | `1m` | — |
| `--grace` | `0` | `0` | `0` |
| `--failures` | `3` | `3` | — |
| `--successes` | `1` | — | — |
| `--failure` | `restart-container` | `degrade` | — |
| `--on-exit` | — | — | `restart` |
| `--lock NAME` | Not allowed | Optional | Optional |

Help lists applicable options for each task kind. For compatibility, processes
still accept `--failure`, and checks/jobs accept `--on-exit`; these options do
not change their policy.

- `startup`: run before the application. Checks/jobs run sequentially and retry until successful or their deadline expires. Processes start in the background and remain supervised; add a following check if their readiness matters.
- `after-start`: run after `exec`, while the application is already running. Use for readiness probes that require its listener to exist. Runtime tasks start after this phase completes.
- `runtime`: run in the background after readiness. Checks and jobs are sequential within each task; intervals are measured after completion, not from fixed wall-clock times.
- `both`: run the same check/job during startup and runtime. A process cannot use this phase.
- `shutdown`: jobs only, run sequentially with the shared shutdown budget; no retries.

For startup/after-start, `--deadline` limits the whole task including retries; `--timeout` limits an individual attempt. A failed task retries after `--interval` (one second if zero). `--failures` applies only at runtime. Critical startup failures stop the container when the deadline expires; optional failures allow startup to continue in degraded state. A startup-only optional task has no later retry: use `both` to recover automatically.

At runtime, `--failure restart-container` stops the application after the
configured consecutive failure threshold. Optional failures remain degraded.

| Task | Retry delay |
| --- | --- |
| Check | Configured interval |
| Failed job | 5, 10, 20, 40, then 60 seconds, ±20% jitter |
| Exited process | 1, 2, 4, 8, 16, then 30 seconds, ±20% jitter |

A successful one-shot job stops; a failed one keeps retrying. Successful periodic
jobs reset backoff and resume their interval. For processes, either exit code and
launch errors use backoff. A process that ran **more than 30 seconds** restarts
immediately and resets backoff; waiting for a lock does not count as lifetime.
Jitter applies after the cap, so actual maximum delays are 72s for jobs and 36s
for processes. Shutdown interrupts waits. Startup/after-start use fixed intervals
within their deadlines.

Processes use `--on-exit`: `restart` relaunches the helper, while
`restart-container` stops the application on **any** exit, including zero. A
helper waiting to restart has state `restarting` and degrades container health;
a successful launch clears its error. Use a separate check for service readiness.

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

## Confirming readiness and recovery

```sh
docktinel add check dns --phase both --successes 2 --interval 5s -- /opt/bin/check-dns
```

Checks can require multiple consecutive successful attempts with `--successes N`
(default `1`). The JSON config property is the integer `successes`. Startup and
after-start wait for the complete streak within the existing task and global
deadlines. Runtime checks stay degraded/confirming until the streak completes;
this does not turn a runtime-only check into an application startup gate.

Any failed attempt resets the success streak. Any successful attempt resets the
consecutive failure counter, even while recovery is still being confirmed. The
previous failure reason remains visible until confirmation. A check that already
met the threshold stays confirmed on further successes until a failure occurs.
`status --json` exposes `successes` and `required_successes`; text status shows
progress for thresholds above one. Non-check tasks only accept the default `1`.
Older API callers that omit `successes` also default to `1`.

## State transition logs

Docktinel emits structured `slog` events to stderr:

- `event=container_state_changed`: `from_phase`, `phase`, `from_state`, `state`, `reason`.
- `event=task_state_changed`: `task`, `kind`, `phase`, `from_state`, `state`, `failures`, `reason`.

Recovery is logged as `task recovered`. A container returns from degraded to
healthy only when all failed tasks recover. Normal successful attempts do not
repeat health-transition events; individual failures retain their diagnostic
logs. A transient command execution does not constitute a health transition.
Events do not contain command arguments or ENV values; task stdout/stderr remain
under the helper command's control. Concurrent task events may be interleaved;
use their explicit from/to fields to interpret each transition.
