# syntax=docker/dockerfile:1
ARG GO_VERSION=1
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS source
WORKDIR /src
COPY go.mod ./
COPY internal ./internal

# Cache test compilation separately from execution and production compilation.
FROM source AS test-build
COPY cmd ./cmd
RUN go test -race -run '^$' ./...

FROM test-build AS test
RUN go test -race -count=1 ./... && go vet ./... && printf 'ok\n' > /test-passed

FROM source AS build
ARG TARGETOS=linux
ARG TARGETARCH
# Require successful tests without inheriting the test stage filesystem.
RUN --mount=type=bind,from=test,source=/test-passed,target=/test-passed \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath ./internal/...
COPY cmd ./cmd
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/docktinel ./cmd/docktinel

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="Docktinel" \
      org.opencontainers.image.description="A lightweight sentinel for Docker containers" \
      org.opencontainers.image.source="https://github.com/xtrime-ru/docktinel" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/docktinel /usr/local/bin/docktinel
ENTRYPOINT ["/usr/local/bin/docktinel"]
CMD ["--help"]
