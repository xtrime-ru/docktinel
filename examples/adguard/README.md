# AdGuard Home

[Dockerfile](Dockerfile) extends the official AdGuard Home image with Docktinel.
It embeds a DNS probe and uses `docktinel add` at image build time to configure
its checks. Docker init starts `docktinel exec`, which runs AdGuard Home.

From the repository root:

```sh
docker build -t adguard-docktinel examples/adguard
```

The build copies Docktinel from `ghcr.io/xtrime-ru/docktinel:latest`. Override `DOCKTINEL_IMAGE` to select another version or digest.

Start with **existing, configured AdGuard Home directories**:

```sh
docker run -d --name adguard-docktinel --init --restart unless-stopped \
    --stop-timeout 30 \
    -p 53:53/tcp -p 53:53/udp -p 3000:3000/tcp \
    -v "$PWD/adguard/conf:/opt/adguardhome/conf" \
    -v "$PWD/adguard/work:/opt/adguardhome/work" \
    adguard-docktinel
```

Keep the web port mapping appropriate to your AdGuard configuration. For a new installation, finish the setup wizard using the official image first: DNS readiness cannot pass before DNS is configured.

## Checks and recovery

- After launch, Docktinel waits up to one minute for a successful DNS query.
- Runtime checks wait five seconds between attempts. Three consecutive failures stop the application; Docker's restart policy starts the container again.
- The independent Docker healthcheck detects a dead or frozen Docktinel daemon.

The probe uses `dig` with a two-second query timeout and checks the DNS response code, not just the command's exit status. `SERVFAIL`, `REFUSED`, `NXDOMAIN` and timeouts fail. Select a stable, allowed probe name that returns `NOERROR` through your serving configuration; a cached answer is sufficient to confirm serving health.

| ENV | Default |
| --- | --- |
| `DNS_PROBE_NAME` | `example.org` |
| `DNS_PROBE_ADDRESS` | `127.0.0.1` |
| `DNS_PROBE_PORT` | `53` |

For example, add `-e DNS_PROBE_NAME=your-allowed-domain.example` to the run command. The address and port must match the local DNS listener. Inspect state with `docker exec adguard-docktinel docktinel status`.

AdGuard keeps its own filter update schedule and configuration. Configure fallback upstreams, upstream timeouts and DNS cache reinitialization in your application setup; this example does not add another filter updater or DNS endpoint refresh loop. Pin `ADGUARD_IMAGE` and `DOCKTINEL_IMAGE` build arguments to versions or digests for reproducible builds.
