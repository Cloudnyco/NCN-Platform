# Peering

> **English** · [简体中文](peering.zh-CN.md)

The network operates an open peering policy. Peering requests are accepted at commonly attended Internet Exchange Points (IXPs) or via Private Network Interconnect (PNI) with AS64500.

## Basic information

| | |
|---|---|
| **ASN** | AS64500 |
| **Peering policy** | Open |
| **IPv6** | Required (the network is IPv6-first) |
| **MD5** | Optional |
| **Details** | PeeringDB: AS64500 |

## Prerequisites

- Maintain an accurate AS record in PeeringDB; it is used for validation.
- Register valid **RPKI ROA** / IRR records. Origin validation is performed, and announcements that fail validation may be rejected.
- Apply appropriate prefix filtering and a max-prefix limit on both sides.

## How to apply

1. Confirm presence at a common IXP, or agree on a PNI.
2. Submit the ASN, IXP, peering IP addresses, and contact details through the peering application page.
3. The details are verified against PeeringDB and RPKI, after which the session is configured and the applicant is notified.

!!! note "RPKI"
    ROAs are published for the network's own prefixes, and origin validity is checked on received announcements. Prefixes should have valid ROAs to avoid being classified as invalid.

## What is announced

- Anycast prefixes for the network (IPv6 and IPv4).
- Routes learned from one peer are not re-advertised to another peer (no transit).
