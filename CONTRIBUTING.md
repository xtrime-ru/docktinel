# Contributing

Docktinel targets Linux containers and keeps service-specific behavior in external commands. Please include the failure scenario your change addresses and keep the CLI independent of individual applications.

Run `make test` on Linux with Go 1.27+. On macOS, tests that require Linux `/proc` are skipped; run the Linux suite in Docker instead:

```sh
docker run --rm --init -v "$PWD:/src" -w /src golang:1.27.4-bookworm \
    sh -ec 'go test -race ./...; go vet ./...'
```

Run `make integration` to build the artifact and example images, then test real process namespaces and signals. The tests create uniquely named containers, remove only those containers, and do not publish ports. Go 1.27+ and a running Docker Engine are required. The integration suite uses the official `github.com/moby/moby/client` SDK, pinned in `integration/go.mod`; these dependencies are not linked into Docktinel. The demo application and its probes are also written in Go.

`make integration-test` runs the suite against already-built images. The Make targets use `DOCKER_HOST` if set, otherwise derive the endpoint from the active Docker context. For a direct SDK test invocation, set `DOCKER_HOST` explicitly when using a non-default socket:

```sh
DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')" \
    go -C integration test -race -count=1 -timeout=3m -v ./...
```

The SDK honors Docker's host/TLS environment variables and negotiates the Engine API version. For contexts that use SSH transport, expose a local socket/tunnel and set `DOCKER_HOST` to that endpoint. Tests fail if Docker or the demo image is unavailable; they do not silently skip. Container cleanup uses an independent context so it still runs after test cancellation.

Format Go changes with `gofmt -w cmd internal integration examples/basic`. Keep commits focused on one behavior and update the command reference and examples when changing lifecycle semantics or defaults.

The CI runs race tests, vet, cross-compilation and container integration tests before publishing images. Pushes to `main` publish `edge`. Published GitHub prereleases publish only the exact version; stable releases publish exact and minor versions. A separate serialized job promotes the already-published digest to `latest` only if that release is currently GitHub Latest. Pushing a tag alone does not publish. To promote a release after changing GitHub's Latest selection, rerun its release workflow or promotion job. Pull requests and manually dispatched runs test without publishing. Linux binary builds and SHA256 checksums are available as workflow artifacts.
