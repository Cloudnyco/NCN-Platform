# ncn-agent

Per-PoP telemetry agent. Replaces the SSH-poll transport that
`backend/fleet.go` on ctrl-01 uses to scrape each remote PoP every 15s.

Same 14-segment shell-pipeline output as before, served over HTTPS with
HMAC-bearer auth instead of fanning out ssh connections.

## Why this exists

Pre-refactor: ctrl-01's `fleetScraper` opens 4 fresh SSH connections every
15s (one per remote PoP: pop-03, pop-04, pop-06, pop-05, …). Each connection
forks an `ssh` process, does a full TCP+SSH handshake, runs the pipeline,
closes. The output is parsed by the scraper.

Post-refactor: each PoP runs `ncn-agent` listening on `:9101` (HTTPS). tyo
issues `GET /v1/snapshot` with an HMAC-signed header; the agent runs the
same shell pipeline locally and returns **byte-identical** output. The
scraper's parser doesn't change.

## Migration story

The new transport ships in parallel with SSH. Per-node `Transport` field
on `fleetNode` selects which to use:

- `""` / `"ssh"` → original SSH transport (default; no behaviour change)
- `"rest"`      → HTTPS to `ncn-agent`, with SSH fallback on any error

Rollout (one PoP at a time):

1. Run `scripts/agent-ca-bootstrap.sh` ONCE on tyo — builds internal CA.
2. For each PoP, run `scripts/agent-node-provision.sh <node-id>` on tyo —
   mints a fresh HMAC key + per-node TLS cert signed by the CA, scp's the
   binary + cert + key + systemd unit to the node, starts the service.
3. Edit `backend/fleet.go` to flip that node's `Transport: "rest"`. Restart
   ncn-api on tyo.
4. Watch the fleet dashboard. Byte-equal output means the parsed result is
   identical to the SSH path — load1, MemPct, BIRD protocols, probe RTTs
   should all read the same.
5. Once all 4 PoPs are on `"rest"` and stable for ≥24h, Phase 3 removes
   `fetchRemoteSSH` and the `Transport` field from `fleet.go`.

The terminal WebSocket in `term.go` keeps its SSH path — that's a separate
concern (interactive command stream, not telemetry polling) and not part
of this refactor.

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

Response is `text/plain` with 15 sections separated by lines containing
exactly `___SEP___` — the same shape `backend/fleet.go` already knows how
to parse from the SSH pipeline.

## Files on disk

On tyo:
- `/etc/ncn-core-console/agent-ca/ca.crt` (644) + `ca.key` (600) — internal CA.
- `/etc/ncn-core-console/agent-keys/<node>.key` (600) — per-node HMAC key.

On each PoP:
- `/usr/local/bin/ncn-agent` (755) — the binary.
- `/etc/ncn-agent/tls.crt` (644) + `tls.key` (600) — cert signed by tyo's CA.
- `/etc/ncn-agent/hmac.key` (600) — SAME bytes as tyo's `agent-keys/<node>.key`.
- `/etc/ncn-agent/agent.conf` — listen addr, node id (informational).
- `/etc/systemd/system/ncn-agent.service` — unit file.

## Build

```
cd core-console/agent
go build -o ncn-agent
```

Or from `deploy.sh` (Phase 1+ will add an `agent` subcommand that builds
+ scp's the binary as part of provisioning).
