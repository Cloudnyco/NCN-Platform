# IGP Fundamentals (Interior Gateway Protocols)

> **English** · [简体中文](igp.zh-CN.md)

This document introduces the basic concepts of Interior Gateway Protocols (IGP). IGP contrasts with [BGP](bgp.md): BGP handles routing *between* autonomous systems, while IGP handles routing *within* a single autonomous system. Reading [BGP Fundamentals](bgp.md) first is recommended.

---

## IGP vs. EGP

Routing protocols are classified by scope into two categories:

| Category | Scope | Representative protocols |
|---|---|---|
| EGP (Exterior Gateway Protocol) | *Between* autonomous systems | BGP |
| IGP (Interior Gateway Protocol) | *Within* a single autonomous system | OSPF, IS-IS, RIP, etc. |

IGP allows routers within an autonomous system to learn how to reach the various networks inside that system; BGP exchanges prefixes between autonomous systems. The two serve different functions and typically operate concurrently.

## Common IGPs

| Protocol | Type | Notes |
|---|---|---|
| OSPF | Link-state | Widely deployed; supports IPv4/IPv6 (OSPFv3); divided into areas for scalability |
| IS-IS | Link-state | Common in large carrier backbones; protocol-independent and extensible |
| RIP | Distance-vector | An early protocol; simple to configure but slow to converge and limited in scale |
| EIGRP | Distance-vector (advanced) | Common within Cisco environments |

## Link-State vs. Distance-Vector

IGPs operate in two principal ways:

- **Link-state** (OSPF, IS-IS): Each router maintains the full network topology and independently computes the shortest path to each destination using an algorithm such as Dijkstra's. Convergence is fast and scalability is good.
- **Distance-vector** (RIP): A router advertises only "how many hops to a given network" to its directly connected neighbors, and information propagates hop by hop. Implementation is simple, but convergence is slower and additional mechanisms are required to prevent loops.

## How IGP and BGP Cooperate

In an autonomous system running both IGP and BGP, each protocol performs a distinct role:

- **IGP** provides internal reachability, including the internal paths to BGP neighbors and to route *next hops*.
- **BGP** carries prefixes between autonomous systems and relies on the IGP to resolve how those routes' next hops are actually reached.

In short: IGP determines how to route within the domain, and BGP determines which prefixes exist between domains.

## Internal Routing in This Network

In this network, the PoPs are interconnected over an **IPv6 backbone** (`2001:db8::/44`), and internal routes are distributed using **iBGP** (internal BGP) rather than a traditional IGP.

On a tunnel-based, limited-scale, full-mesh backbone of this kind, iBGP (with directly connected or static routes resolving next hops) is sufficient for internal reachability, so OSPF or IS-IS is not run additionally. External prefixes are still announced to upstreams and peers via eBGP (see [BGP Fundamentals](bgp.md)).

---

## Further Reading

- [BGP Fundamentals](bgp.md) — the routing protocol between autonomous systems
- [Autonomous Systems (AS) and ASN Fundamentals](asn.md) — the numbers identifying networks in routing protocols
- [Network](network.md) — the topology and anycast deployment of this network (AS64500)
