# HTTP example

A small Go HTTP server demonstrates the full Docktinel lifecycle:

1. A startup job creates the page.
2. An after-start check waits for the HTTP listener.
3. A runtime check probes the server every second.
4. A maintenance job logs a message every 30 seconds.

The [Dockerfile](Dockerfile) copies the Docktinel binary from an artifact image and starts it directly with `ENTRYPOINT`. Tasks are defined in [config.json](config.json); no shell entrypoint is needed.

## Run

From the repository root, with Docker installed:

```sh
docker compose -f examples/basic/compose.yml up -d --build
curl http://127.0.0.1:8080
docker compose -f examples/basic/compose.yml exec demo docktinel status
```

The build copies Docktinel from `ghcr.io/xtrime-ru/docktinel:latest`. `make demo` builds the same demo image using that published binary by default; it does not build Docktinel from source.

| ENV | Default | Purpose |
| --- | --- | --- |
| `DEMO_MESSAGE` | `Hello from Docktinel!` | Text written by the startup job |
| `CHECK_INTERVAL` | `1s` | Runtime HTTP check interval |
| `DOCKTINEL_STARTUP_TIMEOUT` | `30s` | Shared startup and readiness budget |

Set overrides in the Compose service's `environment` section, then recreate the container. Values are applied at container startup.

## Simulate failure and recovery

```sh
docker compose -f examples/basic/compose.yml exec demo touch /srv/fail-health
docker compose -f examples/basic/compose.yml logs -f
```

Three failed runtime checks stop the server; Compose restarts the container. The marker remains in the container's writable layer, so readiness fails on subsequent starts until it is removed:

```sh
docker compose -f examples/basic/compose.yml exec demo rm -f /srv/fail-health
```

If the restart loop prevents `exec`, recreate the container to clear the marker:

```sh
docker compose -f examples/basic/compose.yml up -d --force-recreate --no-build
```

## Clean up

```sh
docker compose -f examples/basic/compose.yml down
```

`make integration` tests isolated containers without published ports, including critical failure, optional dependency recovery and shutdown signal and KILL escalation when the watchdog is frozen. See [Contributing](../../CONTRIBUTING.md) for test requirements.
