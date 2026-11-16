ARG DOCKTINEL_IMAGE=docktinel:local
ARG GO_VERSION=1.27.4
FROM ${DOCKTINEL_IMAGE} AS sentinel
FROM golang:${GO_VERSION}-bookworm AS demo-build
WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/demo main.go

FROM alpine:3.22
COPY --from=sentinel /usr/local/bin/docktinel /usr/local/bin/docktinel
COPY --from=demo-build /out/demo /usr/local/bin/demo
WORKDIR /srv
COPY entrypoint.sh /opt/demo/entrypoint.sh
RUN chmod +x /opt/demo/entrypoint.sh
ENV TINI_KILL_PROCESS_GROUP=1
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD ["docktinel", "healthcheck", "--startup-grace", "30s"]
ENTRYPOINT ["/opt/demo/entrypoint.sh"]
