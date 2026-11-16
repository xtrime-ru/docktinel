# Runnable HTTP demo

From the repository root:

```sh
make demo
docker compose -f examples/basic/compose.yml up -d --no-build
curl http://127.0.0.1:8080
docker compose -f examples/basic/compose.yml exec demo docktinel status
```

The array-form Docker entrypoint runs `docktinel startup` directly. It auto-starts the daemon and loads `/etc/docktinel/config.json`, prepares a page, starts the HTTP server, waits for readiness, then checks it every second. A maintenance job logs a message every 30 seconds. Set `CHECK_INTERVAL` to change the check interval, `DEMO_MESSAGE` for the served text, and `DOCKTINEL_STARTUP_TIMEOUT` for the shared startup/readiness budget.

Simulate a critical failure:

```sh
docker compose -f examples/basic/compose.yml exec demo touch /srv/fail-health
docker compose -f examples/basic/compose.yml logs -f
```

Three failed checks terminate the server. Compose's restart policy starts it again. The marker survives a restart of the same container, so remove it to restore service:

```sh
docker compose -f examples/basic/compose.yml exec demo rm -f /srv/fail-health
# If the restart loop prevents exec, recreate the container instead:
docker compose -f examples/basic/compose.yml up -d --force-recreate --no-build
```

Clean up:

```sh
docker compose -f examples/basic/compose.yml down
```

`make integration` uses isolated containers without published ports and tests critical failures, optional dependency recovery, and a frozen watchdog with TERM/KILL escalation.
