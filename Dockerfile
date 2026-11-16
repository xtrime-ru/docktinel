# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.4
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go test ./...
ARG TARGETOS=linux
ARG TARGETARCH
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
