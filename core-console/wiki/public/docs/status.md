# Status and Incidents

> **English** · [简体中文](status.zh-CN.md)

## Real-time status

The online status and reachability of each PoP are continuously monitored by the uptime tracker. Because the network uses anycast, the failure of a single PoP generally does not affect overall availability: traffic is automatically rerouted to the next-nearest healthy PoP.

## Incidents

Planned maintenance and faults are published as incidents, including the affected scope, a timeline, and remediation progress.

## Reporting a problem

When reporting a suspected network issue, include the following information:

- The source region / ASN.
- The destination prefix or address.
- A traceroute from the reporting side (IPv6 preferred).
- Where possible, the result of a [Looking Glass](looking-glass.md) query.

This information helps determine whether the issue is at the peering, routing, or origin layer.

!!! info "Contact"
    Report urgent network issues through the NOC contact details for AS64500 listed on PeeringDB.
