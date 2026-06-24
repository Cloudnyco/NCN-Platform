# Autonomous Systems (AS) and ASN Basics
> **English** · [简体中文](asn.zh-CN.md)

This document introduces the basic concepts of Autonomous Systems and Autonomous System Numbers for readers new to network interconnection. Details specific to this network (AS64500) are described in [Network](network.md).

---

## What Is an Autonomous System (AS)

The internet is not a single unified network. It is composed of many independently administered networks interconnected with one another. A group of IP networks managed by a single organization under a common routing policy is called an Autonomous System (AS).

Common examples of Autonomous Systems include internet service providers (ISPs), cloud providers, content delivery networks (CDNs), universities, and large enterprises.

## What Is an ASN

Each Autonomous System holds a globally unique number called an Autonomous System Number (ASN). In the inter-domain routing protocol BGP, the ASN identifies which network a route originates from.

An ASN is typically written as `AS` followed by a number, for example `AS64500`.

## How ASNs Are Allocated

ASNs are administered by IANA and allocated hierarchically to five Regional Internet Registries (RIRs), which in turn allocate them to individual organizations:

| RIR | Coverage Region |
|---|---|
| RIPE NCC | Europe, the Middle East, and Central Asia |
| ARIN | North America |
| APNIC | Asia-Pacific |
| LACNIC | Latin America and the Caribbean |
| AFRINIC | Africa |

## 16-bit and 32-bit ASNs

- **16-bit ASN**: range `0`–`65535`. This is the original format, and its pool is largely exhausted.
- **32-bit ASN**: range extended to `0`–`4294967295` to meet subsequent demand. `AS64500` is a 32-bit ASN.

Both formats interoperate in BGP, and routers treat them equivalently.

## Public and Private ASNs

- **Public ASN**: globally unique and used to participate in BGP routing on the public internet.
- **Private ASN**: reserved for internal use within an organization and not advertised on the public internet. The ranges are `64512`–`65534` (16-bit) and `4200000000`–`4294967294` (32-bit).

## Relationship Between ASN and BGP

BGP (Border Gateway Protocol) is the protocol by which Autonomous Systems exchange routing information. Each BGP route carries an **AS_PATH**, the sequence of ASNs the route has traversed in order. The Autonomous System that originally advertised the prefix is called the **origin AS**.

A receiver can use this information to determine the origin and path of a route and to validate its legitimacy with mechanisms such as RPKI (see the [Peering](peering.md) requirements for this network).

## How to Look Up an ASN

Public tools can query the prefixes advertised, peering relationships, and registration details of any ASN. Examples include:

- **RIPEstat** (`stat.ripe.net`)
- **bgp.tools**
- **PeeringDB** (`peeringdb.com`) — the interconnection registry
- **whois** queries

---

## Further Reading

- [Network](network.md) — topology and anycast deployment of this network (AS64500)
- [Peering](peering.md) — how to establish peering with this network
- [Looking Glass](looking-glass.md) — real-time view of this network's routing
