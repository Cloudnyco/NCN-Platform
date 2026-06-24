# Looking Glass

> **English** · [简体中文](looking-glass.zh-CN.md)

The Looking Glass provides a read-only view of routing state from the network's perspective. It is used to investigate reachability, confirm whether a prefix is being received or advertised, and inspect AS paths.

## Capabilities

- **Route lookup**: the best path, next hop, and AS path for a given prefix.
- **BGP neighbor status**: the state of BGP sessions.
- **Basic reachability probes**: reachability tests originating from a selected PoP.

## Usage

1. Open the **Looking Glass** entry from the site home page.
2. Select a **PoP**, which determines the regional vantage point used for the query.
3. Enter a prefix or address and run the query.

!!! tip "Investigating anycast"
    When access appears abnormal from a particular region, query the prefix path using the PoP closest to that region. This helps distinguish between peering, path-selection, and origin-server issues.

## Read-only

The Looking Glass is read-only. It displays routing information only and does not modify any configuration.
