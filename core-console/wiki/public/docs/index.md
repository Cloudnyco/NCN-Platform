# Network Overview

> **English** · [简体中文](index.zh-CN.md)

The network operates as autonomous system **AS64500**, a multi-PoP anycast network with native IPv6 and IPv4 support.

## Anycast model

A single IP prefix is announced from multiple points of presence (PoPs) concurrently. BGP routes traffic to the topologically nearest PoP, which reduces latency and allows traffic to be diverted around a failed PoP automatically.

```
user (region A) ─┐
user (region B) ─┤   one anycast prefix
user (region C) ─┘        │
                          ├─ pop-01
       BGP nearest-exit ──┼─ pop-02
                          ├─ pop-03
                          └─ … additional PoPs
```

## Services

- **Multi-region anycast access**: a single prefix served from the nearest PoP.
- **Native IPv6 with IPv4 support**.
- **Open peering**: interconnection is available at participating IXPs. See [Peering](peering.md).
- **Looking Glass**: live route-view queries. See [Looking Glass](looking-glass.md).

## Navigation

| Topic | Page |
|---|---|
| Network structure, PoP distribution, announced prefixes | [Network](network.md) |
| Peering | [Peering](peering.md) |
| Route lookup and reachability troubleshooting | [Looking Glass](looking-glass.md) |
| Operational status and incident history | [Status and incidents](status.md) |

!!! info "NOC contact"
    For operations and interconnection inquiries, use the contact details for AS64500 on [PeeringDB](https://www.peeringdb.com/), or submit a request through the peering application page.
