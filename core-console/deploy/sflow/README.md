# Traffic analytics — flow collection

> **English** · [简体中文](README.zh-CN.md)

The console's Traffic page (netflow.go → `/api/v1/auth/flow/top`) reads a JSON-lines
file written by a **goflow2** collector. The collector is fed sampled flows by
**softflowd** running on each PoP. (sFlow via hsflowd was the original plan, but
hsflowd is not packaged for the target distribution, so softflowd → NetFlow v9 is
used instead; it is available in apt and produces the same goflow2 JSON output.)

    each PoP: softflowd  --NetFlow v9/udp2055-->  ctrl-01: goflow2  --JSONL-->  /var/log/ncn-flows/flows.jsonl (tmpfs)
                                                                                  ^ ncn-api tails this (NCN_FLOW_FILE default)

## Why ctrl-01 + tmpfs
ncn-api runs on ctrl-01 and tails a LOCAL file, so the collector is colocated there.
The control node's root filesystem has limited free space, so the JSONL is placed on
a **64M tmpfs** (`/var/log/ncn-flows`, configured in fstab) — RAM-backed, so it
cannot fill the control node's disk. Flow data is ephemeral (a 10-minute window), so
loss on reboot is acceptable. goflow2 runs as `nobody`, with MemoryMax=256M and
OOMScoreAdjust=600. Hourly `logrotate` (copytruncate, 15M) keeps the file small; the
tailer resets on truncation.

## Collector (ctrl-01)
    GOBIN=/usr/local/bin go install github.com/netsampler/goflow2/v2/cmd/goflow2@latest
    # fstab: tmpfs /var/log/ncn-flows tmpfs size=64M,mode=0777,nosuid,nodev 0 0
    cp goflow2.service /etc/systemd/system/ && systemctl enable --now goflow2
goflow2's default listen already covers `netflow://:2055` (+ `sflow://:6343`). The
control node's input policy is `accept`, so backbone NetFlow reaches it.

## Exporter (each PoP)
softflowd's packaged unit is a `/bin/true` placeholder and must be REPLACED with the
softflowd.service provided here. Notes:
- `-d` = run in foreground (for systemd), NOT `-D` (debug).
- softflowd has NO `idle` timeout; valid names are tcp/udp/icmp/general/maxlife/**expint**.
- IPv6 collector: `-n [2001:db8:53::1]:2055` (bracketed) works.
- `-s 1000` = 1:1000 sampling (production). For a quick test use `-s 1` (every packet).

    apt-get install -y softflowd
    cp softflowd.service /etc/systemd/system/   # set -i <iface> (default eth0)
    systemctl daemon-reload && systemctl enable softflowd && systemctl restart softflowd

Verification on ctrl-01: `tail flows.jsonl | grep -o '"sampler_address":"[^"]*"' | sort -u`
should show each PoP's backbone anchor (2001:db8::/32 addresses).

## ASN composition (optional)
The `src_as`/`dst_as` columns are populated only if goflow2 enriches AS numbers
(softflowd does not add them) — this requires a MaxMind GeoLite2-ASN database. Without
it, the IP/port/proto/direction breakdowns still work.

## ASN enrichment (Team Cymru, no license)
Because softflowd does not fill src_as/dst_as, netflow.go resolves the top-talker IPs'
origin AS via Team Cymru DNS (`*.origin6.asn.cymru.com` / `*.origin.asn.cymru.com`) in
a background goroutine, cached for 6h. The collector host (ctrl-01) requires outbound
DNS for this. The AS columns on the Traffic page populate within roughly 1-2 minutes.

## Status
- softflowd exports from each PoP (ctrl-01, pop-0N).
- `flow_agg` DB history is a follow-up; v1 is the in-memory 10-minute window.
