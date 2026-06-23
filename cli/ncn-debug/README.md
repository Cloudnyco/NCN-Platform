# ncn-debug

A small, read-only debug CLI over the NCN console REST API — for ops members
who'd rather check the fleet from a terminal than the web console. Pure Go
stdlib, single static binary, no runtime deps.

## Install

```sh
cd cli/ncn-debug && go build -o ncn-debug
# or grab a cross-compiled build:
GOOS=linux  GOARCH=amd64 go build -o ncn-debug-linux-amd64
GOOS=darwin GOARCH=arm64 go build -o ncn-debug-darwin-arm64
```

Drop the binary anywhere on your `$PATH`.

## Auth

Everything except `status` needs a personal API token. Mint one at
**admin.example.com → Security → API Tokens** (format `ncntok_…`), then either:

```sh
ncn-debug token ncntok_xxxxxxxx     # saved to ~/.config/ncn-cli/token (0600)
# or:
export NCN_TOKEN=ncntok_xxxxxxxx
# or per-invocation:
ncn-debug --token ncntok_xxxxxxxx fleet
```

Resolution order: `--token` › `$NCN_TOKEN` › `~/.config/ncn-cli/token`.

The token is a bearer credential with your operator's role — treat it like a
password. Revoke it from the same Security panel if it leaks.

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
ncn-debug fleet                                   # the everyday overview
ncn-debug node pop-05                              # one PoP in depth
ncn-debug bgp pop-04                               # just pop-04's sessions
ncn-debug get /api/v1/bird/protocol?node=ctrl-01    # escape hatch: any endpoint
ncn-debug --json fleet | jq '.[] | select(.ok==false)'   # script-friendly
```

`get` is the escape hatch — it sends your token to any `/api/v1/…` path and
pretty-prints the JSON, so anything the web console can fetch, you can too.

## Flags

```
--host URL     console base URL (default https://admin.example.com, or $NCN_HOST)
--json         raw JSON instead of formatted output (pipe into jq)
--timeout D    per-request timeout (default 20s)
```

Flags may appear before or after the command. Color auto-disables when output
isn't a terminal or when `NO_COLOR` is set.

## Notes

- Read-only. It performs `GET`s only; there are no mutating commands.
- The wire types in `main.go` mirror `core-console/backend` (fleet.go /
  bird_scrape.go / tunnel.go). If a backend JSON tag changes, update the
  matching struct here — unknown fields are ignored, so drift degrades
  gracefully (a renamed field just shows blank/zero) rather than breaking.
