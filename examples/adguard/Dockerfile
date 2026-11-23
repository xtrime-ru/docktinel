# syntax=docker/dockerfile:1
ARG DOCKTINEL_IMAGE=ghcr.io/xtrime-ru/docktinel:latest
ARG ADGUARD_IMAGE=adguard/adguardhome:latest
FROM ${DOCKTINEL_IMAGE} AS sentinel
FROM ${ADGUARD_IMAGE}

COPY --from=sentinel /usr/local/bin/docktinel /usr/local/bin/docktinel
RUN apk add --no-cache bind-tools

# Keep the probe and task configuration in this Dockerfile.
COPY --chmod=0755 <<'SH' /usr/local/bin/check-adguard-dns
#!/bin/sh
set -eu
reply="$(dig @"$DNS_PROBE_ADDRESS" -p "$DNS_PROBE_PORT" "$DNS_PROBE_NAME" A \
    +time=2 +tries=1 +noall +comments)"
printf '%s\n' "$reply" | grep -q '^;; ->>HEADER<<- .*status: NOERROR,'
SH

RUN docktinel add check dns-ready --phase after-start --deadline 1m \
      --interval 1s --timeout 5s -- /usr/local/bin/check-adguard-dns \
    && docktinel add check dns-serving --interval 5s --timeout 5s \
      --failures 3 -- /usr/local/bin/check-adguard-dns

ENV DOCKTINEL_STARTUP_TIMEOUT=2m \
    DNS_PROBE_ADDRESS=127.0.0.1 \
    DNS_PROBE_PORT=53 \
    DNS_PROBE_NAME=example.org

HEALTHCHECK --interval=10s --timeout=5s --start-period=2m --retries=3 \
    CMD ["docktinel", "healthcheck"]
ENTRYPOINT ["docktinel", "exec", "--"]
CMD ["/opt/adguardhome/AdGuardHome", "--no-check-update", \
    "-c", "/opt/adguardhome/conf/AdGuardHome.yaml", "-w", "/opt/adguardhome/work"]
