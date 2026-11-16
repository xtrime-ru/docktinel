# Docktinel

A lightweight sentinel for Docker containers: startup checks, background jobs,
process supervision, and a watchdog-aware Docker healthcheck.

Build with Go 1.26+:

```sh
go build -o docktinel ./cmd/docktinel
```

Run under Docker's init (`--init` or Compose `init: true`). An entrypoint starts
the daemon, registers commands, waits for dependencies, then replaces itself
with the application:

```sh
#!/bin/sh
set -eu
docktinel daemon &
docktinel add check dependency --phase both -- /opt/bin/check-dependency
docktinel startup
exec docktinel exec -- application --foreground
```

Supply the dependency check and application in your own image. Docktinel does
not act as PID 1; keep Docker's init for signal forwarding and zombie reaping.
Configure `docktinel healthcheck` as the Docker healthcheck and a restart policy
such as `unless-stopped`. The independent healthcheck reads an atomic snapshot
and stops the application if the watchdog stalls. Optional failures degrade
health without creating a container restart loop.

See the [command reference](docs/commands.md) for task kinds, lifecycle phases,
timeouts and failure policies. [MIT License](LICENSE).
