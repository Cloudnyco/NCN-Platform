# BGP Basics
> **English** · [简体中文](bgp.zh-CN.md)

This document introduces the basic concepts of the Border Gateway Protocol (BGP) for readers new to inter-domain routing. Reading [Autonomous Systems (AS) and ASN Basics](asn.md) first is recommended.

---

## What Is BGP

BGP (Border Gateway Protocol) is the inter-domain routing protocol of the Internet. It exchanges reachability information between different [autonomous systems (AS)](asn.md) and determines how data traverses multiple networks to reach its destination.

## The Problem BGP Solves

Each autonomous system has direct knowledge only of its own internal networks. BGP allows adjacent autonomous systems to inform one another which IP prefixes they can reach and by what path. This information is propagated hop by hop across the network, eventually establishing reachability between arbitrary endpoints.

## Prefixes and Route Announcements

An autonomous system uses BGP to announce the IP prefixes it is responsible for (for example, `2001:db8:50::/44`). Other autonomous systems that receive the announcement then know where to forward traffic destined for those addresses.

The core information of a BGP route is a destination prefix plus the path to reach it.

## AS_PATH

Every BGP route carries an AS_PATH, which records the sequence of ASNs it has traversed. It serves two main purposes:

- Path selection: among multiple reachable paths, a shorter AS_PATH is generally preferred.
- Loop prevention: if an autonomous system sees its own ASN in the AS_PATH, it discards the route, avoiding routing loops.

The autonomous system at the end of the path, which originally announced the prefix, is called the origin AS.

## eBGP and iBGP

| Type | Purpose |
|---|---|
| eBGP (External BGP) | Exchanges routes between different autonomous systems |
| iBGP (Internal BGP) | Synchronizes routes among routers within the same autonomous system |

## Path Selection Overview

BGP path selection considers more than hop count. It decides according to a series of attributes evaluated in a fixed priority order, commonly including Local Preference, AS_PATH length, and MED. As a result, BGP outcomes are policy-driven and are not necessarily the physically shortest path.

## Interconnection Models: Transit and Peering

| Model | Description |
|---|---|
| Transit | A paid relationship with an upstream provider that provides reachability to the entire Internet |
| Peering | Two networks directly exchange their own routes and those of their customers, usually without payment, often at an IXP |

## Related Concepts

- IXP (Internet Exchange Point): a facility where multiple networks interconnect at a shared physical location.
- Full table: the complete set of Internet routes an autonomous system receives from its connections.
- Route origin validation: see the following section.

## Security: Route Origin Validation

BGP itself does not verify the authenticity of an announcement, which creates a risk of prefix mis-origination and route hijacking. RPKI uses signed ROAs to declare which autonomous system is authorized to announce a given prefix. Routers use this information to perform Route Origin Validation (ROV) on received routes, classifying them as valid, invalid, or unknown. A network publishes its own ROAs and performs route origin validation on routes received over its interconnections (see the requirements for [Peering](peering.md)).

---

## Further Reading

- [Autonomous Systems (AS) and ASN Basics](asn.md) — the numbers that identify networks in BGP
- [Network](network.md) — the topology and anycast deployment of the network (AS64500)
- [Peering](peering.md) — how to establish interconnection with the network
- [Looking Glass](looking-glass.md) — view routes and AS_PATH in real time
