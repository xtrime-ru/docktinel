# DNS and AdGuard integration recipe

This is a registration recipe for an existing image, not a standalone AdGuard distribution. Supply the named helper scripts using your application's supported APIs and DNS tools.

```sh
#!/bin/sh
set -eu

docktinel daemon &

# Resolve dynamic upstream endpoints before launching AdGuard. Retry for at
# most a minute; do not block startup indefinitely on optional upstreams.
docktinel add job dns-initial --phase startup --deadline 1m \
    --timeout 5s --failure degrade -- /opt/bin/update-dns-endpoints

# A real local DNS query, not merely an open port or a live PID.
docktinel add check dns-ready --phase after-start --deadline 30s \
    --timeout 5s --interval 1s -- /opt/bin/check-local-dns

docktinel add check dns-serving --interval 5s --timeout 5s --failures 3 \
    -- /opt/bin/check-local-dns

# Detect endpoint changes and perform application-specific DNS reinitialization.
# No restart on a temporary resolver/API failure; retry and report degraded.
docktinel add job dns-refresh --interval 30s --timeout 10s --lock dns-state \
    -- /opt/bin/update-dns-endpoints

exec docktinel startup --timeout 2m -- /opt/AdGuardHome/AdGuardHome --no-check-update \
    -c /opt/adguardhome/conf/AdGuardHome.yaml -w /opt/adguardhome/work
```

Adapt the AdGuard path and arguments to your existing image. Use `init: true`, the Docker healthcheck from the main README, and a restart policy.

## Helper contracts

`check-local-dns` should issue an actual DNS query through the local serving path with a bounded timeout, inspect the response code, and return zero only for an acceptable answer. Choose a stable domain allowed by your filters. Avoid coupling serving health to a single optional upstream; the query must be able to use your configured fallback. A readiness check can retry startup failures for its configured deadline.

`update-dns-endpoints` should resolve upstream names through a bootstrap resolver independent of the DNS service it is maintaining; compare normalized endpoint sets with the last successfully applied state. On a temporary error, return nonzero and keep the last known working configuration. On change, apply the supported reinitialization/reload operation and clear the relevant application resolver caches; record the new state only after success. Serialize related changes using the same Docktinel lock.

If the application requires a full restart to reinitialize its DNS resolver, the helper can call `docktinel restart --reason 'upstream addresses changed'`. Ensure initialization persists or derives the new endpoints before the next launch, so a restart does not repeat indefinitely. A helper cannot reset another application's private resolver cache just by changing `/etc/hosts` or flushing the operating system cache.

Keep AdGuard's built-in filter update schedule. A failed filter download should not by itself fail the serving check: existing filters and DNS service may remain usable. Configure fallback upstreams and upstream request timeouts in AdGuard itself. Docktinel does not alter AdGuard configuration or add another filter downloader.

For a VPN container, register the DNS dependency as a startup/runtime check with a failure policy appropriate to the protocol. Stopping a WireGuard server does not automatically remove a client's tunnel or its routes; service restart is recovery, not a guarantee that clients switch to a direct connection.
