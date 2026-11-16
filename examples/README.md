# Examples

| Example | Task configuration | Demonstrates |
| --- | --- | --- |
| [HTTP server](basic/README.md) | JSON file: [config.json](basic/config.json) | Startup jobs, readiness, runtime checks, maintenance and recovery |
| [AdGuard Home](adguard/README.md) | CLI commands in an embedded shell entrypoint | Extending an existing image using one self-contained Dockerfile |

Both examples copy the binary from `ghcr.io/xtrime-ru/docktinel:latest` by default. Override `DOCKTINEL_IMAGE` to select another version or digest.

Both examples use Docker's init, a restart policy and an independent Docker healthcheck. Application commands stay in the foreground; Docktinel coordinates checks and background tasks inside the container.
