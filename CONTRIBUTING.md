# Contributing

Docktinel targets Linux containers and keeps service-specific behavior in external commands. Please include the failure scenario your change addresses and keep the CLI independent of individual applications.

CI and Docker builds use the latest stable Go 1.x; CI resolves one version for all jobs and Docker stages. The minimum supported version is Go 1.27. Docker builds compile unit tests in `test-build` and execute them in `test`; CI always reruns the latter. Test files and caches are excluded from the final `scratch` image.

Run `make test` on Linux with Go 1.27+. On macOS, tests that require Linux `/proc` are skipped; run the Linux suite in Docker instead:

```sh
docker run --rm --init -v "$PWD:/src" -w /src golang:1-bookworm \
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

The [CI workflow](.github/workflows/ci.yml) runs race tests, vet, cross-compilation and Docker integration tests. Linux binaries and SHA256 checksums are available as workflow artifacts.
