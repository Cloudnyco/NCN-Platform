# Uptime Monitoring Targets

> **English** · [简体中文](uptime-targets.zh-CN.md)

This document lists the monitoring targets to paste into a dedicated uptime tracker (a SaaS service or a self-hosted Uptime Kuma instance). The goal is to use probes that are independent of the monitored network, together with anti-flap configuration, to replace or supplement the self-built alerting bot and to reduce alert noise. Targets cover the set of currently active PoPs; decommissioned nodes are not monitored.

## 1. Public anycast services (external perspective, highest priority)

These targets reflect whether end users can reach the service and should be probed from an external SaaS service using multiple geographic locations. Expected response codes are listed below.

| Name | Type | URL | Expected |
|---|---|---|---|
| Public site | HTTPS | `https://example.com/` | 200 |
| Health API | HTTPS | `https://example.com/api/v1/health` | 200 (cleanest health probe) |
| Status page | HTTPS | `https://example.com/status` | 200 |
| Looking Glass API | HTTPS | `https://example.com/api/v1/lg/sessions` | 200 |
| Admin console | HTTPS | `https://admin.example.com/login` | 200 (the root path returns 302 to the login page; probe `/login` directly) |
| TLS certificate expiry | Cert | `example.com:443` | Alert 14 days in advance |

## 2. Per-PoP reachability (internal perspective, identifies which node is down)

Each PoP has two checks: a unicast IPv4 ping and an anycast anchor IPv6 ping. Probe the unicast IP and the IPv6 anchor rather than the anycast VIP. Probing the anycast VIP resolves to the nearest node and does not reveal which specific node has failed.

Note: the IPv6 anchor ping requires the probe location to support IPv6. Some free SaaS tiers do not support IPv6; self-hosted Uptime Kuma and several SaaS providers do.

| PoP | Location | Ping v4 (unicast) | Ping v6 (anchor 2001:db8:R::N) |
|---|---|---|---|
| pop-03 | Region C | `198.51.100.3` | `2001:db8:51::1` |
| pop-04 | Region C | `198.51.100.4` | `2001:db8:51::2` |
| ctrl-01 | Region A | `198.51.100.1` | `2001:db8:53::1` |
| pop-01 | Region A | `198.51.100.2` | `2001:db8:53::2` |
| pop-02 | Region A | `198.51.100.5` | `2001:db8:53::3` |
| pop-08 | Region E | `198.51.100.6` | `2001:db8:56::1` |
| pop-06 | Region D | `198.51.100.8` | `2001:db8:54::1` |
| pop-05 | Region B | `198.51.100.7` | `2001:db8:55::1` |

## 3. Anti-flap configuration (the key to reducing alert noise, more important than tool choice)

Apply the following settings uniformly to every monitor:

- **Probe interval**: 60s.
- **Confirmation retries**: mark DOWN only after **3 consecutive** failures (`retries = 3` / confirmation period). Single or transient failures are ignored.
- **Multi-location confirmation**: anycast services alert only when **two or more probe locations** fail (SaaS multi-region).
- **Notification resend interval**: after a DOWN event, resend at most once every **30 minutes** (`resend every 30 min`) rather than every minute.
- **Recovery notifications**: send a resolved notification on UP. During a flap, the paired down/up notifications make the flap easy to identify.
- **Maintenance windows**: open a maintenance window to silence alerts before planned changes (for example, mesh or BIRD configuration changes).
- **Alert severity**: public anycast services are emergency severity (phone call / pinned). A single unreachable PoP unicast is normal severity, because the remaining PoPs continue to serve anycast and the event does not require an immediate page.

## 4. Tool selection

- **External layer** (available immediately, no host required): a SaaS uptime provider that supports multi-location confirmation, escalation policies, and IPv6 is preferred; a simpler provider without IPv6 or multi-location confirmation is also an option. Paste the public targets from section 1 into the chosen provider.
- **Internal layer** (requires a separate small host; do not run it on `ctrl-01`): Uptime Kuma (see `monitoring/docker-compose.yml`) handles the per-node pings from section 2 and can reuse the existing Telegram bot for notifications.
- Do not install the internal layer on `ctrl-01`: it has limited free memory and disk headroom, and it is itself a monitored target. Self-hosting the monitor on the node it monitors is both risky and ineffective, because a failure of that node would prevent the alert from being sent.

## 5. Reusing Telegram notifications

The existing self-built bot's Telegram configuration (bot token and chat ID) can be entered directly into the Telegram notification channel of Uptime Kuma or the chosen SaaS provider.

It is recommended to scope the self-built bot to low-frequency business events (node commission/decommission, certificate expiry, mesh apply results) and to delegate high-frequency node up/down probing to the dedicated tracker, separating responsibilities and avoiding duplicate notifications.
