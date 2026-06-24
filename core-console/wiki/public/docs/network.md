# Network

> **English** · [简体中文](network.zh-CN.md)

## Autonomous System

| | |
|---|---|
| **ASN** | AS64500 |
| **Protocols** | Native IPv6 and IPv4 |
| **Topology** | Multi-PoP anycast |
| **PeeringDB** | Search for AS64500 |

## PoP Distribution

Points of Presence (PoPs) are deployed across multiple regions. Each PoP announces the same anycast prefix. The current coverage is listed below and may change over time:

| Region | Code | Example node |
|---|---|---|
| Region A | `tyo` | ctrl-01 … |
| Region C | `hkg` | pop-03 … |
| Region B | `fra` | pop-05 |
| Region D | `sin` | pop-06 |
| Region E | `tpe` | pop-08 |

For the live list of nodes and their status, use the [Looking Glass](looking-glass.md) or the [status page](status.md).

## How Anycast Works

1. Each PoP announces the **same prefix** over BGP to its upstreams and IXPs.
2. Routers on the internet each select the route with the **shortest AS path** to that prefix.
3. As a result, users in different regions are directed to the **nearest** PoP.
4. When a PoP fails and withdraws its announcement, traffic shifts to the **next-nearest** PoP after BGP convergence, with minimal disruption to users.

!!! note "Latency"
    Traffic terminates at the nearest PoP rather than being routed to a single fixed server in another region.

## Resilience

- **Proximity and redundancy**: The loss of a single PoP is routed around automatically by anycast.
- **Health-based withdrawal**: An unhealthy PoP withdraws its anycast announcement, preventing traffic from being directed to a node that cannot serve it.
- **Multiple upstreams**: Each PoP connects to multiple upstreams and IXPs.
