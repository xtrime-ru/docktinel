# Contributing

Docktinel targets Linux containers and keeps service-specific behavior in external commands. Please include the failure scenario your change addresses and keep the CLI independent of individual applications.

Run `make test` on Linux with Go 1.27+. On macOS, tests that require Linux `/proc` are skipped; run the Linux suite in Docker instead:

```sh
docker run --rm --init -v "$PWD:/src" -w /src golang:1.27.4-bookworm \
    sh -ec 'go test -race ./...; go vet ./...'
```

Run `make integration` to build the artifact and example images, then test real process namespaces and signals. The tests create uniquely named containers, remove only those containers, and do not publish ports. Python 3 is required on the host. Docker must be running.

Format Go changes with `gofmt -w cmd internal`. Keep commits focused on one behavior and update the command reference and examples when changing lifecycle semantics or defaults.

The CI runs race tests, vet, cross-compilation and container integration tests before publishing images. Pushes to `main` publish `edge`; version tags such as `v0.1.0` publish versioned tags and `latest`. Pull requests and manually dispatched runs test without publishing. Linux binary builds and SHA256 checksums are available as workflow artifacts.
