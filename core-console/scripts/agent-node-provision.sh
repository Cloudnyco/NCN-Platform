#!/usr/bin/env bash
# agent-node-provision.sh — provision a PoP with ncn-agent.
#
# Runs on ctrl-01. Per node (pop-03 / pop-04 / pop-06 / pop-05 / pop-01 / pop-08):
#
#   1. Mints a fresh HMAC key (32B random) — stored at
#      /etc/ncn-core-console/agent-keys/<node>.key on tyo so ncn-api
#      can sign requests with it.
#   2. Mints a fresh P-256 cert signed by the agent CA, SAN includes
#      <node>.example.com + the node's public IP.
#   3. SCPs the binary, cert, key, HMAC key, systemd unit, and the
#      default config file to the node.
#   4. Enables + starts ncn-agent.service on the node.
#   5. Curls /v1/healthz from tyo to verify the listener is up.
#
# Re-running on an existing node REPLACES the HMAC key and TLS cert —
# zero downtime on the agent (systemctl reload-or-restart) but ncn-api
# on tyo must be restarted afterward to pick up the new HMAC key
# (Phase 0 loads agent keys once at startup; SIGHUP reload is a planned
# Phase 3+ ergonomic improvement).
#
# Usage:
#
#   sudo /opt/ncn-core-console/scripts/agent-node-provision.sh <node-id>
#
# Requires:
#
#   * /etc/ncn-core-console/agent-ca/{ca.crt,ca.key} (from agent-ca-bootstrap.sh)
#   * /opt/ncn-core-console/build/ncn-agent — agent binary built for the
#     target node's arch (typically linux/amd64; pop-03 is arm64, override
#     with NCN_AGENT_BINARY env var).
#   * SSH to the node working as root (fleet-key or equivalent).

set -euo pipefail

NODE="${1:-}"
if [[ -z "$NODE" ]]; then
  echo "usage: $0 <node-id>" >&2
  echo "(node ids come from the registry at /etc/ncn-core-console/nodes.json)" >&2
  exit 2
fi

# Node parameters (login user, arch, public IP for the cert SAN). Two sources,
# in priority order:
#
#   1. Environment (NCN_PROV_SAN_IP / NCN_PROV_SSH_USER / NCN_PROV_ARCH) — set
#      by ncn-api when this script is launched from the admin Servers page.
#      ncn-api already has the node registry parsed in Go, so this path needs
#      no JSON parser on the host.
#   2. The node registry JSON (/etc/ncn-core-console/nodes.json), read with jq,
#      for manual CLI runs (`sudo agent-node-provision.sh <id>`).
#
# Either way there's no hardcoded per-node case statement any more — adding a
# server through the GUI is enough to bring it online.
if [[ -n "${NCN_PROV_SAN_IP:-}" ]]; then
  SSH_USER="${NCN_PROV_SSH_USER:-root}"
  ARCH="${NCN_PROV_ARCH:-amd64}"
  SAN_IP="$NCN_PROV_SAN_IP"
  SSH_PORT="${NCN_PROV_SSH_PORT:-22}"
else
  NODES_JSON="/etc/ncn-core-console/nodes.json"
  command -v jq >/dev/null 2>&1 || { echo "error: jq required for manual runs (or invoke via the admin Servers page)" >&2; exit 1; }
  [[ -f "$NODES_JSON" ]] || { echo "error: node registry $NODES_JSON not found" >&2; exit 1; }
  REC=$(jq -c --arg id "$NODE" '.[] | select(.id==$id)' "$NODES_JSON")
  [[ -n "$REC" ]] || { echo "unknown node: $NODE (not in $NODES_JSON — add it on the admin Servers page first)" >&2; exit 2; }
  SSH_USER=$(jq -r '.ssh_user // "root"' <<<"$REC")
  ARCH=$(jq -r '.arch // "amd64"' <<<"$REC")
  SAN_IP=$(jq -r '.address' <<<"$REC")
  SSH_PORT=$(jq -r '.ssh_port // 22' <<<"$REC")
fi
[[ -n "$SSH_USER" && "$SSH_USER" != "null" ]] || SSH_USER="root"
[[ -n "$ARCH" && "$ARCH" != "null" ]] || ARCH="amd64"
[[ "$SSH_PORT" =~ ^[0-9]+$ ]] || SSH_PORT="22"
[[ -n "$SAN_IP" && "$SAN_IP" != "null" ]] || { echo "node $NODE has no address (env NCN_PROV_SAN_IP or registry)" >&2; exit 2; }

# SSH options mirror backend/fleet.go fetchRemoteSSH — same identity,
# same known_hosts, same accept-new policy. Keeps deployment in sync with
# the path fleet uses for scraping; no separate ssh config alias needed.
FLEET_KEY="/etc/ncn-core-console/fleet-key"
KNOWN_HOSTS="/etc/ncn-core-console/fleet-known-hosts"
SSH_OPTS=(
  -p "$SSH_PORT"
  -i "$FLEET_KEY"
  -o "StrictHostKeyChecking=accept-new"
  -o "UserKnownHostsFile=$KNOWN_HOSTS"
  -o "BatchMode=yes"
  -o "ConnectTimeout=8"
)
SSH_TARGET="$SSH_USER@$SAN_IP"

CA_DIR="/etc/ncn-core-console/agent-ca"
CA_KEY="$CA_DIR/ca.key"
CA_CRT="$CA_DIR/ca.crt"
SERIAL_FILE="$CA_DIR/serial"
KEYS_DIR="/etc/ncn-core-console/agent-keys"
HMAC_KEY_LOCAL="$KEYS_DIR/$NODE.key"
BINARY="${NCN_AGENT_BINARY:-/opt/ncn-core-console/build/ncn-agent}"

if [[ $EUID -ne 0 ]]; then echo "error: must run as root" >&2; exit 1; fi
[[ -f "$CA_KEY" && -f "$CA_CRT" ]] || { echo "CA missing at $CA_DIR — run agent-ca-bootstrap.sh first" >&2; exit 1; }
[[ -f "$BINARY" ]] || { echo "agent binary missing at $BINARY — build first" >&2; exit 1; }

mkdir -p "$KEYS_DIR"
chmod 700 "$KEYS_DIR"

WORK=$(mktemp -d -t ncn-agent-prov.XXXXXX)
trap "rm -rf $WORK" EXIT

# ── Step 1: fresh HMAC key, 32B base64 → 44 chars. Stored raw bytes
# (no encoding) on both sides so client + server compute identical MACs.
openssl rand 32 > "$WORK/hmac.key"
install -m 0600 "$WORK/hmac.key" "$HMAC_KEY_LOCAL"
echo "✓ HMAC key minted → $HMAC_KEY_LOCAL"

# ── Step 2: mint per-node TLS cert.
#
# SAN list:
#   * DNS:  <node>.example.com (canonical name used by ncn-api when dialing)
#   * IP:   public IP of the node (fallback if DNS is broken / split)
#
# Validity 1 year. Renewal = re-run this script. ncn-api notices a cert
# mismatch on the next request and surfaces it as an alert (so silent
# expiry isn't possible).
openssl ecparam -name prime256v1 -genkey -noout -out "$WORK/tls.key"
openssl req -new -key "$WORK/tls.key" -subj "/CN=$NODE.example.com" \
  -addext "subjectAltName=DNS:$NODE.example.com,IP:$SAN_IP" \
  -addext "extendedKeyUsage=serverAuth" \
  -out "$WORK/tls.csr"

SERIAL=$(cat "$SERIAL_FILE")
echo $((SERIAL + 1)) > "$SERIAL_FILE"

cat > "$WORK/v3.ext" <<EOF
subjectAltName=DNS:$NODE.example.com,IP:$SAN_IP
extendedKeyUsage=serverAuth
basicConstraints=CA:FALSE
EOF
openssl x509 -req -in "$WORK/tls.csr" \
  -CA "$CA_CRT" -CAkey "$CA_KEY" \
  -days 365 -set_serial "$SERIAL" \
  -extfile "$WORK/v3.ext" \
  -out "$WORK/tls.crt"
echo "✓ TLS cert minted (serial=$SERIAL, valid 365d)"

# ── Step 3: systemd unit (rendered locally, scp'd to node).
cat > "$WORK/ncn-agent.service" <<'EOF'
[Unit]
Description=NCN Agent (per-PoP telemetry endpoint)
Documentation=https://github.com/your-org/your-repo/tree/main/core-console/agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ncn-agent
Restart=on-failure
RestartSec=5s
# The agent runs commands like `sudo -n birdc` — keep root for that.
# Hardening that doesn't break shell-out:
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/etc/ncn-agent
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictRealtime=true
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
EOF

# Default config — listen address only. Node id is informational, used
# in /v1/healthz output for ops sanity ("which PoP did I just hit").
cat > "$WORK/agent.conf" <<EOF
listen=:9101
node_id=$NODE
EOF

# ── Step 4: ship to node.
#
# Non-root login (e.g. pop-03's cloud-init `debian` user) can't scp
# directly to /etc or /usr/local — we stage in /tmp first then sudo-mv
# into place. The `sudo` invocation is `sudo -n` (non-interactive); the
# fleet contract is that the login user has passwordless sudo, same
# guarantee fleet.go.buildRemoteScript relies on for `sudo -n birdc`.
echo "→ shipping to $NODE ($SSH_TARGET) …"

SUDO=""
if [[ "$SSH_USER" != "root" ]]; then
  SUDO="sudo -n"
fi

# scp uses the same key/options. -O forces legacy SCP protocol on
# OpenSSH ≥ 9 (which defaults to SFTP). Some PoPs run older sshd that
# doesn't speak the new protocol — -O keeps us portable across the
# fleet without per-node detection.
# scp takes the port as -P (uppercase), unlike ssh's -p.
SCP_OPTS=(-O -P "$SSH_PORT" -i "$FLEET_KEY" -o "StrictHostKeyChecking=accept-new"
          -o "UserKnownHostsFile=$KNOWN_HOSTS" -o "BatchMode=yes")

# Stage everything under /tmp/ncn-agent-stage on the remote; debian
# (or any non-root user) can write here without sudo. The remote
# install step then sudo-moves each file to its final location.
STAGE="/tmp/ncn-agent-stage-$$"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "mkdir -p $STAGE && chmod 700 $STAGE"
scp "${SCP_OPTS[@]}" -q "$BINARY"                  "$SSH_TARGET":$STAGE/ncn-agent
scp "${SCP_OPTS[@]}" -q "$WORK/tls.crt"            "$SSH_TARGET":$STAGE/tls.crt
scp "${SCP_OPTS[@]}" -q "$WORK/tls.key"            "$SSH_TARGET":$STAGE/tls.key
scp "${SCP_OPTS[@]}" -q "$WORK/hmac.key"           "$SSH_TARGET":$STAGE/hmac.key
scp "${SCP_OPTS[@]}" -q "$WORK/agent.conf"         "$SSH_TARGET":$STAGE/agent.conf
scp "${SCP_OPTS[@]}" -q "$WORK/ncn-agent.service"  "$SSH_TARGET":$STAGE/ncn-agent.service

ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "STAGE=$STAGE SUDO='$SUDO' bash -s" <<'REMOTE'
set -euo pipefail
# Files were uploaded to $STAGE under the login user; sudo-move them
# into the canonical locations. mkdir + chmod first so even a fresh
# /etc/ncn-agent is set up correctly before the key files land.
$SUDO mkdir -p /etc/ncn-agent
$SUDO chmod 700 /etc/ncn-agent

$SUDO install -m 0755 "$STAGE/ncn-agent"          /usr/local/bin/ncn-agent
$SUDO install -m 0644 "$STAGE/tls.crt"            /etc/ncn-agent/tls.crt
$SUDO install -m 0600 "$STAGE/tls.key"            /etc/ncn-agent/tls.key
$SUDO install -m 0600 "$STAGE/hmac.key"           /etc/ncn-agent/hmac.key
$SUDO install -m 0644 "$STAGE/agent.conf"         /etc/ncn-agent/agent.conf
$SUDO install -m 0644 "$STAGE/ncn-agent.service"  /etc/systemd/system/ncn-agent.service

# Clean staging dir — secrets shouldn't linger in /tmp.
rm -rf "$STAGE"

$SUDO systemctl daemon-reload
$SUDO systemctl enable --now ncn-agent.service
$SUDO systemctl restart ncn-agent.service

# Wait for listener
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ss -tlnp 2>/dev/null | grep -q ':9101'; then echo "listener up"; exit 0; fi
  sleep 1
done
echo "WARN: listener didn't come up within 10s; check journalctl -u ncn-agent -n 50" >&2
exit 1
REMOTE

# ── Step 5: cross-validate from tyo using the agent's TLS cert.
echo "→ verifying TLS reachability from tyo …"
curl --silent --show-error \
  --cacert "$CA_CRT" \
  --resolve "$NODE.example.com:9101:$SAN_IP" \
  --max-time 8 \
  "https://$NODE.example.com:9101/v1/healthz" | head -c 400
echo
echo "✓ $NODE provisioned"
echo
echo "Next: in ncn-api config, flip the node's Transport to \"rest\" and"
echo "watch /var/log/ncn-core-console for parity between REST and SSH"
echo "snapshots before disabling SSH for this node."
