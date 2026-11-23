# Local HTTP API

The CLI communicates with the daemon over the Unix socket
`$DOCKTINEL_RUNTIME_DIR/control.sock` (default `/run/docktinel/control.sock`).
The socket has mode `0600`. Keep the runtime directory private and run clients
as the daemon's user.

| Method | Path | JSON request | Response |
| --- | --- | --- | --- |
| `GET` | `/v1/status` | None | Current status, matching `docktinel status --json` |
| `POST` | `/v1/restart` | `{"reason":"DNS endpoints changed"}` | `{}` when shutdown is requested |

Successful requests return HTTP `200`. Malformed or invalid JSON returns `400`;
bodies above 1 MiB return `413`. Errors use `{"error":"explanation"}`. Unknown
routes and unsupported methods return standard `404` and `405` responses.

```sh
curl --unix-socket /run/docktinel/control.sock http://localhost/v1/status
```

The CLI retries connection establishment for up to five seconds. It sends each
operation once. Docker healthcheck reads the atomic `status.json` file
independently so it can detect a frozen daemon.

Check status includes the current `successes` streak and `required_successes`.
While awaiting confirmation, a check reports `confirming` and runtime container
health is degraded.
