#!/usr/bin/env bash
# agent-ca-bootstrap.sh — one-time setup of the internal CA that signs
# per-node ncn-agent TLS certs.
#
# Runs on ctrl-01 (the central console). Produces:
#
#   /etc/ncn-core-console/agent-ca/ca.key   — CA private key, 0600 root
#   /etc/ncn-core-console/agent-ca/ca.crt   — CA cert (10y, self-signed)
#   /etc/ncn-core-console/agent-ca/serial   — incrementing serial counter
#
# Why a private CA instead of Let's Encrypt:
#
#   * pop-04 has no nginx (xray owns :443) — LE needs HTTP-01 or DNS-01
#     plumbing on every PoP; the latter requires distributing the
#     cloudflare API token. Avoiding that.
#   * The agent ↔ ncn-api channel is internal mesh traffic; public CA
#     trust adds no value. ncn-api pins THIS ca.crt as the only trust
#     root, so an attacker with a publicly-trusted cert can't MITM.
#   * Rotation/renewal is one cron on tyo + a re-deploy script — much
#     simpler than 4-host LE renewal.
#
# Idempotent: re-running checks the existing CA, refuses to overwrite if
# present (delete manually if you really mean to rotate the CA root).
#
# Run as root:
#
#   sudo /opt/ncn-core-console/scripts/agent-ca-bootstrap.sh

set -euo pipefail

CA_DIR="/etc/ncn-core-console/agent-ca"
CA_KEY="$CA_DIR/ca.key"
CA_CRT="$CA_DIR/ca.crt"
SERIAL="$CA_DIR/serial"

if [[ $EUID -ne 0 ]]; then
  echo "error: must run as root (CA files live in /etc/ncn-core-console/)" >&2
  exit 1
fi

if [[ -f "$CA_CRT" || -f "$CA_KEY" ]]; then
  echo "CA already exists at $CA_DIR — refusing to overwrite." >&2
  echo "  cert:   $CA_CRT"
  echo "  serial: $(cat "$SERIAL" 2>/dev/null || echo '<unset>')"
  echo "To rotate the CA root, delete $CA_DIR by hand AND be ready to re-mint every per-node cert."
  exit 1
fi

mkdir -p "$CA_DIR"
chmod 700 "$CA_DIR"

# ── Generate CA private key. Ed25519 over RSA: smaller, faster, modern
#    Go and OpenSSL both speak it. The agent's per-node certs will be
#    P-256 ECDSA (better library support across Linux distros).
openssl genpkey -algorithm Ed25519 -out "$CA_KEY"
chmod 600 "$CA_KEY"

# ── Self-sign the CA cert. CN identifies the CA; the agent and ncn-api
#    don't validate the CA's CN, they validate the CA's signature on
#    per-node certs and the per-node cert's SAN against the node ID.
openssl req -x509 -new -nodes \
  -key "$CA_KEY" \
  -days 3650 \
  -subj "/CN=NCN Agent CA/O=Acme Net/OU=Internal Mesh" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out "$CA_CRT"
chmod 644 "$CA_CRT"

echo 1000 > "$SERIAL"
chmod 600 "$SERIAL"

# ── Verify what we just produced.
echo "✓ CA bootstrap complete"
openssl x509 -in "$CA_CRT" -noout -subject -issuer -dates -ext basicConstraints
echo
echo "Next: provision each PoP with"
echo "  sudo $(dirname "$0")/agent-node-provision.sh <node-id>"
echo "(node-id ∈ { pop-03, pop-04, pop-06, pop-05, pop-01, pop-08 }; ctrl-01 only needs ncn-api, no agent)"
