#!/bin/bash
# install-pop03.sh — install Wiki.js 2.x on pop-03, on the sdb mount (root disk
# is full), running as the unprivileged ncnmon user. DB is the `wikijs` database
# on the ctrl-01 PRIMARY Postgres (reached over the v6 backbone) — so wiki content
# rides the existing streaming replication + PITR. Config (with the DB password)
# is shipped separately as config.yml; this script carries no secrets.
set -euo pipefail
D=/var/mail/vhosts/wikijs
VER=v2.5.307
id ncnmon >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d /var/mail/vhosts/ncn-mon ncnmon
install -d -o ncnmon -g ncnmon -m 755 "$D"; cd "$D"
if [ ! -f "$D/server/index.js" ]; then
  curl -fsSL -o /tmp/wiki-js.tar.gz "https://github.com/requarks/wiki/releases/download/$VER/wiki-js.tar.gz"
  tar -xzf /tmp/wiki-js.tar.gz -C "$D"; rm -f /tmp/wiki-js.tar.gz
fi
chown -R ncnmon:ncnmon "$D"
cat > /etc/systemd/system/ncn-wikijs.service <<UNIT
[Unit]
Description=NCN Wiki.js (docs, on pop-03 sdb; DB on ctrl-01 primary)
After=network-online.target
Wants=network-online.target
[Service]
User=ncnmon
Group=ncnmon
WorkingDirectory=$D
Environment=NODE_ENV=production
ExecStart=/usr/bin/node server
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
echo "Wiki.js installed. Ship config.yml (see config.sample.yml) then: systemctl enable --now ncn-wikijs"
