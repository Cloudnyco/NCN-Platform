# ncn-debug

> **English** · [简体中文](README.zh-CN.md)

A read-only debug CLI over the NCN console REST API, intended for operators who
prefer to inspect the fleet from a terminal rather than the web console. It is
written using only the Go standard library and compiles to a single static
binary with no runtime dependencies.

## Install

```sh
cd cli/ncn-debug && go build -o ncn-debug
# or produce a cross-compiled build:
GOOS=linux  GOARCH=amd64 go build -o ncn-debug-linux-amd64
GOOS=darwin GOARCH=arm64 go build -o ncn-debug-darwin-arm64
```

Place the binary anywhere on the `$PATH`.

## Auth

Every command except `status` requires a personal API token. Create one at
**admin.example.com → Security → API Tokens** (format `ncntok_…`), then provide
it in one of the following ways:

```sh
ncn-debug token ncntok_xxxxxxxx     # saved to ~/.config/ncn-cli/token (0600)
# or:
export NCN_TOKEN=ncntok_xxxxxxxx
# or per-invocation:
ncn-debug --token ncntok_xxxxxxxx fleet
```

Resolution order: `--token` › `$NCN_TOKEN` › `~/.config/ncn-cli/token`.

The token is a bearer credential carrying the operator's role and should be
treated like a password. It can be revoked from the same Security panel if
compromised.

## Commands

```
ncn-debug whoami            verify the token; show operator, role, session TTL
ncn-debug fleet             one-line-per-PoP health table
ncn-debug node <id>         full detail for one PoP (cpu/mem/disk/bird/probes/…)
ncn-debug bgp [id]          BGP sessions across the fleet, or just one PoP
ncn-debug incidents         open + recent (30-day) incidents
ncn-debug status            public uptime summary (no token required)
ncn-debug get <path>        raw authenticated GET to any /api path, pretty JSON
ncn-debug token <ncntok_…>  save an API token
```

### Examples

```sh
ncn-debug fleet                                   # fleet-wide overview
ncn-debug node pop-05                              # one PoP in depth
ncn-debug bgp pop-04                               # only pop-04's sessions
ncn-debug get /api/v1/bird/protocol?node=ctrl-01    # arbitrary endpoint
ncn-debug --json fleet | jq '.[] | select(.ok==false)'   # script-friendly
```

`get` sends the token to any `/api/v1/…` path and pretty-prints the JSON
response, so any endpoint the web console can fetch is also reachable from the
CLI.

## Flags

```
--host URL     console base URL (default https://admin.example.com, or $NCN_HOST)
--json         raw JSON instead of formatted output (pipe into jq)
--timeout D    per-request timeout (default 20s)
```

Flags may appear before or after the command. Color output is automatically
disabled when output is not a terminal or when `NO_COLOR` is set.

## Notes

- Read-only. The CLI performs `GET` requests only; there are no mutating
  commands.
- The wire types in `main.go` mirror `core-console/backend` (fleet.go /
  bird_scrape.go / tunnel.go). If a backend JSON tag changes, the matching
  struct here must be updated. Unknown fields are ignored, so drift degrades
  gracefully — a renamed field shows blank or zero rather than breaking the
  command.
