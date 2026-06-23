# Monitoring — Prometheus + Grafana

A self-hosted Prometheus + Grafana stack on **pop-03** that scrapes the ncn-api
`/metrics` endpoint. pop-03 is the observability host (it survives a ctrl-01
death and is where Gatus already runs); everything lives on its roomy **sdb**
(`/var/mail/vhosts/ncn-mon/`) because the root disk is full, and runs as the
unprivileged `ncnmon` user. Both services bind **localhost only** — reach them
through an SSH tunnel, the same pattern as Gatus (:8080).

## Layout (on pop-03)

```
/var/mail/vhosts/ncn-mon/
  prometheus/            # release binary + prometheus.yml
  prometheus-data/       # TSDB (90d retention)
  grafana/               # release tree + conf/provisioning + dashboards/
  grafana-data/          # grafana.db, logs, plugins
```
Release tarballs are cached in `/var/mail/vhosts/ncn-ha/dl/` for re-install.

## Scrape path

Prometheus → `https://[2001:db8:53::1]/metrics` (ctrl-01 primary, over the
private v6 backbone). ctrl-01's nginx has a `location = /metrics` restricted to
`2001:db8:50::/44` + localhost (`deny all` otherwise), so only the backbone can
scrape it — never the public internet. TLS is by IP so the scrape uses
`insecure_skip_verify` (fine on the trusted backbone).

After a failover that promotes pop-03, repoint `prometheus.yml` at the new
primary (or list both — the warm standby's ncn-api is stopped, so it just reads
down until promoted).

## Access (embedded in the console — primary)

The console embeds Grafana at **admin.example.com/grafana** (nav item 监控 /
Monitoring → `Observability.vue`, an iframe of the dashboard in kiosk mode):

```
browser ──> admin.example.com/grafana  (nginx /grafana → ncn_api)
        ──> ncn-api  requireRole("admin")  reverse-proxy            (grafana_proxy.go)
        ──> tyo 127.0.0.1:3001  (ncn-grafana-tunnel.service, ssh -L over the backbone)
        ──> pop-03 127.0.0.1:3000  Grafana (anonymous Viewer, serve_from_sub_path=/grafana)
```
Grafana never leaves pop-03 localhost; the only way in is the admin-session-gated
proxy. It's anonymous **Viewer** (read-only) so operators get no second login;
`GF_SECURITY_ALLOW_EMBEDDING=true` + an nginx `X-Frame-Options: SAMEORIGIN`
override on `/grafana/` let the same-origin iframe render. For admin edits, use
the tunnel below (full login).

## Access (SSH tunnel — direct admin)

```bash
# Grafana
ssh -L 3000:127.0.0.1:3000 root@<pop-03>     # → http://localhost:3000  (admin/admin, change on first login)
# Prometheus (optional, for ad-hoc PromQL)
ssh -L 9090:127.0.0.1:9090 root@<pop-03>     # → http://localhost:9090
```
Dashboard **NCN Control Plane** (uid `ncn-overview`) is auto-provisioned in the
**NCN** folder: DB up, replication standbys + lag, fleet up/total, open
op-failures, WAL archive failures + last-archive age, active alerts by severity,
AI token rate.

## Install / re-install

`install-pop03.sh` is idempotent. Stage the config files in `/tmp/ncn-mon/` and
ensure the release tarballs are in `/var/mail/vhosts/ncn-ha/dl/`, then run it on
pop-03. (It is **not** wired into `deploy/deploy.sh` — hand-deployed, like the
PITR scripts.)

## PITR base-backup freshness (textfile)

The weekly basebackup (`scripts/pitr/ncn-pitr-basebackup`) writes
`/var/mail/vhosts/ncn-mon/pitr.prom` with the last-success timestamp + retained
base count. `ncn-textfile.nginx.conf` (installed to `/etc/nginx/conf.d/` on
pop-03) serves it on **127.0.0.1:9102** (9101 is taken by ncn-agent), and the
`pitr` scrape job picks it up — so Grafana graphs "PITR base backup age" and can
alert if the weekly job silently stops.

## Metrics exposed

See `backend/metrics.go`. Gauges: `ncn_db_up`,
`ncn_replication_streaming_standbys`, `ncn_replication_lag_seconds`,
`ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
`ncn_wal_last_archive_age_seconds`, `ncn_fleet_nodes_{total,up}`,
`ncn_alerts_active{severity}`, `ncn_op_failures_open`,
`ncn_ai_tokens_total{model,kind}`, `ncn_ai_calls_total{model}`,
`ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
`ncn_wal_last_archive_age_seconds`, `ncn_anycast_upstreams_up{node}`,
`ncn_anycast_drained{node}`. Plus (textfile job `pitr`)
`ncn_pitr_last_basebackup_timestamp_seconds`, `ncn_pitr_bases_count`.
