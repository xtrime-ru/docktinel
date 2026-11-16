ARG DOCKTINEL_IMAGE=ghcr.io/xtrime-ru/docktinel:latest
ARG GO_VERSION=1.27.4
FROM ${DOCKTINEL_IMAGE} AS docktinel
FROM golang:${GO_VERSION}-bookworm AS demo-build
WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/demo main.go

FROM alpine:3.22
COPY --from=docktinel /usr/local/bin/docktinel /usr/local/bin/docktinel
COPY --from=demo-build /out/demo /usr/local/bin/demo
WORKDIR /srv
COPY config.json /etc/docktinel/config.json
ENV TINI_KILL_PROCESS_GROUP=1 DOCKTINEL_STARTUP_TIMEOUT=30s DEMO_MESSAGE="Hello from Docktinel!"
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD ["docktinel", "healthcheck", "--startup-grace", "30s"]
ENTRYPOINT ["docktinel", "startup", "--"]
CMD ["demo", "serve"]
