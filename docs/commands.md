# Command reference

```text
docktinel [--runtime-dir DIR] COMMAND
```

Global flags come before the command. Durations use Go syntax such as
`500ms`, `5s`, or `1m`.

## Daemon and lifecycle

| Command | Options and defaults |
| --- | --- |
| `add check\|job\|process NAME` | Task options and `--config PATH`, followed by `-- COMMAND ARGS...` |
| `config validate` | `--config PATH` (or `DOCKTINEL_CONFIG` / default path); no daemon required |
| `daemon` | `--config PATH`, `--heartbeat 5s`, `--stop-grace 10s`, `--timeout 2m` |
| `exec [OPTIONS] -- COMMAND ARGS...` | Same options as `daemon`. Append the application as a startup process |
| `restart --reason TEXT` | Cancel active tasks, run shutdown jobs, and exit with a nonzero status |
| `status` | `--json` for machine-readable output |
| `healthcheck` | `--max-age 30s`, `--stop-grace 10s`, `--startup-grace 2m`, `--no-kill` |
| `version` | Build version |

Run `daemon` as the direct child of Docker init. It reads the config and
runs startup tasks and readiness checks within one budget.
`DOCKTINEL_STARTUP_TIMEOUT` sets the default for `--timeout`. The minimum
heartbeat interval is 100ms.

`exec` runs the same foreground daemon, with `COMMAND` configured as a
startup process named `application` and `on_exit: restart-container`.

On Docker TERM or INT, the daemon cancels active tasks, runs shutdown jobs, and
exits successfully. A requested restart or critical failure exits with a nonzero
status. Use a Docker restart policy to start the container again.

The daemon's `--stop-grace` is the shared shutdown job budget. Allow up to four
additional seconds in the Docker stop timeout for process groups to exit after
cancellation, including a shutdown job that exceeds its deadline. Healthcheck
escalation has its own grace and runs across invocations.

`--startup-grace` only allows a missing initial snapshot; a valid snapshot carries its own startup deadline. Lost or corrupt state after initialization triggers escalation. If both status and identity files are missing, healthcheck waits out its startup grace, then attempts to identify the unique direct child of PID 1; if ambiguous, it reports an error without guessing a PID.

Exit status is zero for successful CLI operations, nonzero for errors. Healthcheck returns nonzero while starting/stopping or on a critical fault; degraded runtime returns zero. `status` queries the live daemon with a five-second deadline; success means the query completed, regardless of the reported health. Healthcheck reads the atomic snapshot independently of the daemon's [local HTTP API](api.md). Task commands have no reserved special exit codes.

## Task options

Set tasks in the [JSON configuration](configuration.md). Each task needs a
`kind` (`check`, `job`, or `process`), a unique `name`, and a `command`
argument array. Names are 1–64 characters and start with a letter or digit.
Subsequent characters may also be `_`, `-`, or `.`.

The `add` command uses equivalent flags, with `--on-exit` for `on_exit`.
Run `docktinel add KIND --help` for the options available for each kind.

| Option | Check | Job | Process |
| --- | --- | --- | --- |
| `phase` | `runtime` | `runtime` | `runtime` |
| `interval` | `10s` | `0` (one-shot) | — |
| `timeout` | `5s` | `1m` | `0` (unlimited) |
| `deadline` | `1m` | `1m` | — |
| `grace` | `0` | `0` | `0` |
| `failures` | `3` | `3` | — |
| `successes` | `1` | — | — |
| `failure` | `restart-container` | `degrade` | — |
| `on_exit` | — | — | `restart` |
| `lock` | Not allowed | Optional | Optional |

- `startup`: run in config order. Checks/jobs run sequentially and retry until successful or their deadline expires. Processes start in the background and remain supervised; add a following check if their readiness matters.
- `after-start`: run after startup tasks. Use for readiness probes that wait for a listener. Runtime tasks start after this phase completes.
- `runtime`: run in the background after readiness. Checks and jobs are sequential within each task; intervals are measured after completion, not from fixed wall-clock times.
- `both`: run the same check/job during startup and runtime. A process cannot use this phase.
- `shutdown`: jobs only, run sequentially with the shared shutdown budget; no retries.

For startup/after-start, `deadline` limits the whole task including retries; `timeout` limits an individual attempt. A failed task retries after `interval` (one second if zero). `failures` applies only at runtime. Critical startup failures stop the container when the deadline expires; optional failures allow startup to continue in degraded state. A startup-only optional task has no later retry: use `both` to recover automatically.

At runtime, `"failure": "restart-container"` stops the container after the
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

Processes use `on_exit`: `restart` relaunches the helper, while
`restart-container` stops the container on **any** exit, including zero. A
helper waiting to restart has state `restarting` and degrades container health;
a successful launch clears its error. Use a separate check for service readiness.

`grace` delays the first background execution; it does not postpone startup/after-start checks or change their deadlines. Minimum nonzero interval is 100ms. Only processes may have an unlimited timeout.

Shared locks serialize commands across tasks and are held for the command's lifetime. The timeout includes waiting for the lock. Do not give two persistent processes the same lock unless waiting is intended. A timeout cancels the task's process group; background subprocesses are also cleaned up after normal completion.

## Confirming readiness and recovery

Checks can require multiple consecutive successful attempts with the integer
`successes` (default `1`). Startup and
after-start wait for the complete streak within the existing task and global
deadlines. Runtime checks stay degraded/confirming until the streak completes;
this does not turn a runtime-only check into an application startup gate.

Any failed attempt resets the success streak. Any successful attempt resets the
consecutive failure counter, even while recovery is still being confirmed. The
previous failure reason remains visible until confirmation. A check that already
met the threshold stays confirmed on further successes until a failure occurs.
`status --json` exposes `successes` and `required_successes`; text status shows
progress for thresholds above one. Non-check tasks only accept the default `1`.

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
