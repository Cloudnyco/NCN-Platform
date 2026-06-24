# Gatus uptime monitoring on pop-0N (deployed)

> **English** · [简体中文](gatus-pop03.zh-CN.md)

Gatus provides a config-as-code uptime tracker that takes over node up/down detection and public-service probing previously handled by a self-built bot.

An earlier evaluation of Uptime Kuma was discontinued: Kuma 2.x has a fragile installation path and requires interactive database/admin setup. Gatus ships as a single binary driven by YAML, which is more reliable and can be fully automated.

## Deployment

- **Host**: pop-0N (`198.51.100.3`). Runs as a native binary rather than under Docker, because the node also acts as a BGP router and Docker would modify `iptables`/`ip_forward`.
- **Binary**: Gatus v5.36.0. Gatus does not publish precompiled binaries, so a linux-amd64 binary is built on a build host with `go install github.com/TwiN/gatus/v5@v5.36.0` and copied to pop-0N at `/usr/local/bin/gatus`.
- **Configuration**: `/etc/gatus/config.yaml` (tracked in the repository as `monitoring/gatus-config.yaml`). The SQLite data store resides under `/var/lib/monitoring/gatus/`.
- **Service**: systemd unit `gatus.service`, bound to `127.0.0.1:8080` (external access is via an SSH tunnel). Telegram credentials are supplied through `EnvironmentFile=/etc/gatus/gatus.env` (`TG_BOT_TOKEN` / `TG_CHAT_ID`). The configuration references these with `${...}` placeholders so secrets are not committed.
- **Monitored endpoints**: public HTTP endpoints (site/health/status/looking-glass/admin, asserting `[STATUS]==200`) plus per-PoP IPv6 anchor ICMP checks (`2001:db8:R::N`, asserting `[CONNECTED]==true`).
- **Flap suppression**: `default-alert` requires 3 consecutive failures before alerting and 2 consecutive successes before resolving, with `send-on-resolved` enabled.

## Why only IPv6 anchors are probed, not IPv4 unicast

From the vantage point of a single node within one mesh, inter-PoP IPv4 cross-carrier reachability is unreliable (a PoP may be unreachable over IPv4 from one region while clearly online over IPv6), which produces false alerts. The IPv6 anchor traverses the backbone and provides a reliable per-PoP signal (consistent with the `ncnProbeV6` logic in the codebase). Public IPv4 reachability is delegated to an external multi-region SaaS layer (see the first section of `uptime-targets.md`).

## Access

- **Public (recommended)**: https://monitor.example.com, served through a Cloudflare Tunnel (`cloudflared-gatus.service` on pop-0N, an outbound connection that opens no inbound ports) to Gatus on port 8080. Authentication is enforced by Cloudflare Access (a Zero Trust organization with a policy that admits operators, e.g. `admin@example.com`, via email OTP).
  - The tunnel connection token is stored on pop-0N at `/etc/cloudflared/tunnel.env` (mode `0600`).
  - DNS: a `monitor` CNAME points to `<tunnel-id>.cfargotunnel.com` (proxied).
- **Emergency**: `ssh -L 8080:127.0.0.1:8080 root@198.51.100.3`, then open http://localhost:8080.

## Changing the configuration

```
# Edit monitoring/gatus-config.yaml in the repository
# scp it to pop-0N at /etc/gatus/config.yaml
# systemctl restart gatus
```

## Follow-up

The self-built bot's node up/down Telegram notifications (`node-unreachable` / `probe-down`) are silenced in `alerts.go` once Gatus takes over. Other critical alerts (CPU/memory/disk, `bgp-peer-down`, `bird-unreachable`) are retained. An independent external fallback layer is to be added per the first section of `uptime-targets.md` (pending).
