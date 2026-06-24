# ncn-agent

> **English** · [简体中文](README.zh-CN.md)

Per-PoP telemetry agent. It replaces the SSH-poll transport that
`backend/fleet.go` on the control node (`ctrl-01`) uses to scrape each
remote PoP every 15s.

It produces the same 14-segment shell-pipeline output as the previous
transport, served over HTTPS with HMAC-bearer authentication instead of
fanning out SSH connections.

## Background

Before the refactor, the control node's `fleetScraper` opens fresh SSH
connections every 15s (one per remote PoP: `pop-02`, `pop-03`, `pop-04`,
…). Each connection forks an `ssh` process, completes a full TCP+SSH
handshake, runs the pipeline, and closes. The output is parsed by the
scraper.

After the refactor, each PoP runs `ncn-agent` listening on `:9101`
(HTTPS). The control node issues `GET /v1/snapshot` with an HMAC-signed
header; the agent runs the same shell pipeline locally and returns
byte-identical output. The scraper's parser is unchanged.

## Migration

The new transport ships in parallel with SSH. A per-node `Transport`
field on `fleetNode` selects which to use:

- `""` / `"ssh"` → original SSH transport (default; no behaviour change).
- `"rest"`      → HTTPS to `ncn-agent`, with SSH fallback on any error.

Rollout proceeds one PoP at a time:

1. Run `scripts/agent-ca-bootstrap.sh` ONCE on the control node — builds
   the internal CA.
2. For each PoP, run `scripts/agent-node-provision.sh <node-id>` on the
   control node. This mints a fresh HMAC key and a per-node TLS cert
   signed by the CA, copies the binary, cert, key, and systemd unit to
   the node, and starts the service.
3. Edit `backend/fleet.go` to set that node's `Transport: "rest"`, then
   restart ncn-api on the control node.
4. Observe the fleet dashboard. Byte-equal output means the parsed result
   is identical to the SSH path — `load1`, `MemPct`, BIRD protocols, and
   probe RTTs should all read the same.
5. Once all PoPs are on `"rest"` and stable for at least 24h, Phase 3
   removes `fetchRemoteSSH` and the `Transport` field from `fleet.go`.

The terminal WebSocket in `term.go` keeps its SSH path. That is a separate
concern (an interactive command stream rather than telemetry polling) and
is not part of this refactor.

## Wire format

```
GET /v1/snapshot HTTP/1.1
Host: <node-public-ip>:9101
Authorization: NCNHMAC ts=<unix>,nonce=<base64url>,sig=<base64url>
X-NCN-Probes: name1|target1|type1,name2|target2|type2,...
```

The signature is computed as:

```
sig = HMAC-SHA256(
    /etc/ncn-agent/hmac.key,
    ts + "\n" + nonce + "\n" + METHOD + "\n" + PATH + "\n" + xprobes
)
```

The response is `text/plain` with 15 sections separated by lines
containing exactly `___SEP___` — the same shape `backend/fleet.go` already
parses from the SSH pipeline.

## Files on disk

On the control node:
- `/etc/ncn-core-console/agent-ca/ca.crt` (644) + `ca.key` (600) — internal CA.
- `/etc/ncn-core-console/agent-keys/<node>.key` (600) — per-node HMAC key.

On each PoP:
- `/usr/local/bin/ncn-agent` (755) — the binary.
- `/etc/ncn-agent/tls.crt` (644) + `tls.key` (600) — cert signed by the control node's CA.
- `/etc/ncn-agent/hmac.key` (600) — same bytes as the control node's `agent-keys/<node>.key`.
- `/etc/ncn-agent/agent.conf` — listen address and node id (informational).
- `/etc/systemd/system/ncn-agent.service` — unit file.

## Build

```
cd core-console/agent
go build -o ncn-agent
```

Building is also available through `deploy.sh`; Phase 1 and later add an
`agent` subcommand that builds and copies the binary as part of
provisioning.
