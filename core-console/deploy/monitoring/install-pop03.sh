#!/bin/bash
# install-pop03.sh — set up Prometheus + Grafana on pop-03, entirely on the sdb
# mount (root disk is full). Run ON pop-03. Expects the config files staged in
# /tmp/ncn-mon/ (prometheus.yml, *.service, *.yaml, dashboards/) and the release
# tarballs already in /var/mail/vhosts/ncn-ha/dl/.
set -euo pipefail
# The monitoring stack lives in its OWN dir, NOT under ncn-ha (which is 0700
# postgres-only → ncnmon can't traverse it). Parent /var/mail/vhosts is o+x.
D=/var/mail/vhosts/ncn-mon
DL=/var/mail/vhosts/ncn-ha/dl   # release tarballs were staged here (root-readable)
STAGE=/tmp/ncn-mon
PROM_VER=2.53.2
GRAF_VER=11.1.4

id ncnmon >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d "$D" ncnmon
install -d -o ncnmon -g ncnmon -m 0755 "$D"
echo "[mon] user ncnmon ok"

# --- Prometheus ---
if [ ! -x "$D/prometheus/prometheus" ]; then
  tar -xzf "$DL/prometheus-$PROM_VER.linux-amd64.tar.gz" -C "$DL"
  rm -rf "$D/prometheus"
  mv "$DL/prometheus-$PROM_VER.linux-amd64" "$D/prometheus"
fi
cp "$STAGE/prometheus.yml" "$D/prometheus/prometheus.yml"
mkdir -p "$D/prometheus-data"
echo "[mon] prometheus extracted"

# --- Grafana ---
if [ ! -x "$D/grafana/bin/grafana" ]; then
  tar -xzf "$DL/grafana-$GRAF_VER.linux-amd64.tar.gz" -C "$DL"
  rm -rf "$D/grafana"
  mv "$DL/grafana-v$GRAF_VER" "$D/grafana"
fi
mkdir -p "$D/grafana/dashboards" "$D/grafana-data/log" "$D/grafana-data/plugins"
mkdir -p "$D/grafana/conf/provisioning/datasources" "$D/grafana/conf/provisioning/dashboards"
cp "$STAGE/ncn-datasource.yaml"  "$D/grafana/conf/provisioning/datasources/ncn-datasource.yaml"
cp "$STAGE/ncn-dashboards.yaml"  "$D/grafana/conf/provisioning/dashboards/ncn-dashboards.yaml"
cp "$STAGE/dashboards/ncn-overview.json" "$D/grafana/dashboards/ncn-overview.json"
echo "[mon] grafana extracted + provisioned"

chown -R ncnmon:ncnmon "$D/prometheus" "$D/prometheus-data" "$D/grafana" "$D/grafana-data"

# --- systemd ---
cp "$STAGE/ncn-prometheus.service" /etc/systemd/system/ncn-prometheus.service
cp "$STAGE/ncn-grafana.service"    /etc/systemd/system/ncn-grafana.service
systemctl daemon-reload
systemctl enable --now ncn-prometheus.service
systemctl enable --now ncn-grafana.service
echo "[mon] services enabled"
sleep 6
systemctl is-active ncn-prometheus.service ncn-grafana.service || true
rm -rf "$STAGE"
echo "[mon] done"
