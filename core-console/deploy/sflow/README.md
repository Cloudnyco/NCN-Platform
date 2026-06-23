# Traffic analytics — flow collection (AS-DEPLOYED 2026-06-23)

The console's Traffic page (netflow.go → `/api/v1/auth/flow/top`) reads a JSON-lines
file written by a **goflow2** collector, fed sampled flows by **softflowd** on each
PoP. (We planned hsflowd/sFlow but it isn't packaged for trixie, so we use
softflowd → NetFlow v9, which is in apt — same goflow2 JSON out.)

    each PoP: softflowd  --NetFlow v9/udp2055-->  ctrl-01: goflow2  --JSONL-->  /var/log/ncn-flows/flows.jsonl (tmpfs)
                                                                                  ^ ncn-api tails this (NCN_FLOW_FILE default)

## Why ctrl-01 + tmpfs
ncn-api runs on ctrl-01 and tails a LOCAL file, so the collector lives there too.
ctrl-01's root has only ~1G free, so the JSONL is on a **64M tmpfs** (`/var/log/
ncn-flows`, fstab) — RAM-backed, can NOT fill the control node's disk; flow data
is ephemeral (10-min window) so losing it on reboot is fine. goflow2 runs as
`nobody`, MemoryMax=256M, OOMScoreAdjust=600. Hourly `logrotate` (copytruncate,
15M) keeps it small; the tailer resets on truncation.

## Collector (ctrl-01) — DONE
    GOBIN=/usr/local/bin go install github.com/netsampler/goflow2/v2/cmd/goflow2@latest
    # fstab: tmpfs /var/log/ncn-flows tmpfs size=64M,mode=0777,nosuid,nodev 0 0
    cp goflow2.service /etc/systemd/system/ && systemctl enable --now goflow2
goflow2's default listen already covers `netflow://:2055` (+ `sflow://:6343`).
ctrl-01 input policy is `accept`, so backbone NetFlow reaches it.

## Exporter (each PoP) — DONE on ctrl-01/02/03, pop-03/02, pop-08
softflowd's packaged unit is a `/bin/true` placeholder — REPLACE it with
softflowd.service here. Notes learned the hard way:
- `-d` = run in foreground (for systemd), NOT `-D` (debug).
- softflowd has NO `idle` timeout; valid names: tcp/udp/icmp/general/maxlife/**expint**.
- IPv6 collector: `-n [2001:db8:53::1]:2055` (bracketed) works.
- `-s 1000` = 1:1000 sampling (prod). For a quick test use `-s 1` (every packet).

    apt-get install -y softflowd
    cp softflowd.service /etc/systemd/system/   # set -i <iface> (default eth0)
    systemctl daemon-reload && systemctl enable softflowd && systemctl restart softflowd

Verify on ctrl-01: `tail flows.jsonl | grep -o '"sampler_address":"[^"]*"' | sort -u`
should show each PoP's backbone anchor (2001:db8:5X::Y).

## ASN composition (optional)
`src_as`/`dst_as` columns light up only if goflow2 enriches AS numbers (softflowd
doesn't add them) — give goflow2 a MaxMind GeoLite2-ASN DB. Without it the
IP/port/proto/direction breakdowns still work.

## ASN enrichment — DONE (Team Cymru, no license)
softflowd doesn't fill src_as/dst_as, so netflow.go resolves the top-talker IPs'
origin AS via Team Cymru DNS (`*.origin6.asn.cymru.com` / `*.origin.asn.cymru.com`)
in a background goroutine, cached 6h. The collector host (ctrl-01) needs outbound
DNS for this. The AS columns on the Traffic page fill in within ~1-2 min.

## Status
- softflowd exporting on ALL 8 PoPs (ctrl-01/02/03, pop-03/02, pop-08, pop-05, pop-06).
- `flow_agg` DB history is a follow-up; v1 is the in-memory 10-min window.
