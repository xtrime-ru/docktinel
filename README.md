# Docktinel

A lightweight sentinel for Docker containers: startup checks, background jobs, process supervision, and a watchdog-aware Docker healthcheck. Written in Go, with no external dependencies and no configuration files.

Docktinel coordinates work **inside one container**. Keep Docker's init (`--init` or Compose `init: true`) for signal forwarding and zombie reaping, and use Docker's restart policy to bring the container back.

## Quick start

Copy the static binary from the artifact image into your application's image:

```dockerfile
FROM ghcr.io/xtrime-ru/docktinel:edge AS sentinel
FROM alpine:3.22
COPY --from=sentinel /usr/local/bin/docktinel /usr/local/bin/docktinel
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENV TINI_KILL_PROCESS_GROUP=1
HEALTHCHECK --interval=10s --timeout=5s --start-period=2m --retries=3 \
  CMD ["docktinel", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["your-application", "--foreground"]
```

`entrypoint.sh`:

```sh
#!/bin/sh
set -eu

docktinel daemon --startup-timeout 2m &

# Existing commands/scripts define what ready and healthy mean.
docktinel add check dependency --phase both --deadline 1m \
    --timeout 5s --interval 5s -- /usr/local/bin/check-dependency

docktinel add job refresh --interval 1m --timeout 20s \
    -- /usr/local/bin/refresh-state

docktinel startup
exec docktinel exec -- "$@"
```

The application and helper commands above are placeholders for your image. For a complete runnable HTTP example:

```sh
make demo
# Start with the already-built image, avoiding a second Compose build.
docker run --init --rm -p 127.0.0.1:8080:8080 docktinel-demo:local
# In another terminal:
curl http://127.0.0.1:8080
```

See [the demo](examples/basic/README.md), [DNS/AdGuard integration](examples/adguard/README.md), and the [command reference](docs/commands.md).

## Lifecycle

1. The entrypoint starts `docktinel daemon &`, then registers tasks with `add`.
2. `startup` closes registration and runs startup tasks in registration order, waiting for their result. Failed commands retry up to their deadline.
3. `exec docktinel exec -- ...` registers the entrypoint's PID and replaces it with the application. The outer shell `exec` is required.
4. After-start tasks verify the running application. Once they finish, runtime checks and jobs begin. Processes started during startup remain supervised.
5. A critical failure cancels background work, runs shutdown jobs, then sends TERM to the application, escalating to KILL after the grace period. The application exits, Docker's init exits, and Docker applies its restart policy.

`exec` is a thin wrapper around the system `exec` call. Docktinel does not become PID 1, forward application signals, or implement an init. Main application stdout, stderr, signals and exit code retain Docker's normal behavior.

## Health and recovery

Checks return zero on success and nonzero on failure. Runtime critical checks tolerate three consecutive failures by default; success resets the counter. `--failure degrade` records the failure without stopping the application. A later success restores the healthy state.

Jobs run once or periodically, never overlapping with themselves. Failed jobs retry with delays of 10, 20, 30, 40, 50, then 60 seconds. Related jobs can share `--lock NAME`. Long-running helper processes restart after exit, or can trigger a container restart with `--on-exit restart-container`.

Docker healthcheck reads an atomic local snapshot rather than invoking the daemon. It verifies the container identity, monotonic heartbeat, daemon PID/start time, startup deadline, and critical task deadlines. A stuck/dead daemon or overdue critical task causes it to send TERM to the recorded main process. A later healthcheck invocation escalates to KILL after `--stop-grace`. It never sends KILL to PID 1. This mechanism requires recurring healthcheck invocations; it is not a separate host watchdog.

Starting and stopping report unhealthy while within their respective budgets, without forcing termination. Degraded runtime state reports healthy to Docker: optional maintenance failures must not create restart loops. `status` provides task details; `healthcheck --no-kill` provides a read-only diagnosis.

Docker marking a container unhealthy does **not** restart it. Docktinel terminates the application for critical failures; set `restart: unless-stopped` (or another suitable policy) to restart after exit. Docker's startup period suppresses health status penalties, not execution or side effects of the healthcheck command.

## Container setup

```yaml
services:
  app:
    image: your-application-with-docktinel
    init: true
    restart: unless-stopped
    environment:
      TINI_KILL_PROCESS_GROUP: "1"
    stop_grace_period: 30s
```

Run the daemon, CLI clients and Docker healthcheck as the same user, with permission to signal the main application. The runtime directory defaults to `/run/docktinel`; for a non-root image choose a writable private directory using `DOCKTINEL_RUNTIME_DIR`. Keep it ephemeral and local to the container. Share the same setting with all invocations. Do not expose the control socket or mount the Docker socket.

Docker's init must run the entrypoint directly. Avoid an extra supervisor or shell that launches the entrypoint as a child. Helpers must run in the foreground and must not detach into new sessions. Docktinel cancels task process groups, with a two-second TERM budget before KILL.

Shutdown hooks run before application termination when Docktinel initiates the stop. On an external `docker stop`, the init forwards signals to the application and Docktinel concurrently; hooks are **best effort** because the container exits when its main application exits. Put mandatory application cleanup in the application's own shutdown handler.

## Images and builds

The GitHub Actions workflow tests Linux behavior and publishes a minimal `scratch` artifact image to GHCR for `linux/amd64` and `linux/arm64`:

| Source | Image tag |
| --- | --- |
| Push to `main` | `ghcr.io/xtrime-ru/docktinel:edge` |
| Tag `v0.1.0` | `:0.1.0`, `:0.1`, `:latest` |

These tags become available after the workflow successfully runs; the initial implementation does not publish a release by itself. For reproducible deployments, pin a version or image digest. Ensure the GHCR package is public if public consumers need anonymous pulls.

The binary is at `/usr/local/bin/docktinel`, statically linked with `CGO_ENABLED=0`, suitable for Alpine, Debian and scratch images. No shell or CA certificates are included in the artifact image. Copy the binary into your application image; helper commands come from your application image.

```sh
make image                              # build and load docktinel:local
make build                              # native binary in bin/docktinel
GOOS=linux GOARCH=amd64 make build       # Linux binary
make test                               # race tests and go vet, on Linux
make integration                        # build both images and exercise real containers
```

Go 1.27+ is required for source builds. Docker builds use a pinned Go toolchain version. The supported runtime is Linux; process identity and heartbeat checks use `/proc`. The Docktinel binary has no external Go dependencies. Integration tests use the official Docker Go SDK in a separate module.

The implementation uses Go 1.27’s stable `encoding/json/v2`, strict JSON validation, standard-library UUIDs, and typed generic state readers.

## Design scope

Docktinel provides scheduling, retries, lifecycle coordination and escalation. Readiness checks, DNS queries, cache reinitialization and application-specific refresh logic remain ordinary commands/scripts. This keeps the binary small and avoids coupling it to AdGuard, CoreDNS or a VPN implementation.

Logs and task output go to container stdout/stderr. The state file omits task arguments and environment; helper output can still contain secrets, so design scripts accordingly. Runtime files and the control socket are private to the configured user. The CLI uses a [local HTTP API over a Unix socket](docs/api.md). `status --json` queries the live daemon and returns schema version 1; `healthcheck` reads the atomic state file independently so it can detect a frozen daemon.

[MIT License](LICENSE).
