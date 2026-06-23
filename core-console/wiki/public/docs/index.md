# Acme Net

**Acme Net (NCN)** 是自治系统 **AS64500**，一个多 PoP 的 **anycast** 网络，原生 IPv6、同时支持 IPv4。

## 一句话理解 anycast

同一段 IP 地址，**在全球多个机房（PoP）同时宣告**。用户的流量会被路由送到**网络上最近的那个 PoP**——延迟更低，单点故障时自动绕开。

```
用户 (东京)  ─┐
用户 (法兰克福)─┤   同一个 anycast 地址
用户 (香港)  ─┘        │
                       ├─ tyo  (东京)
          BGP 自动择近 ─┼─ fra  (法兰克福)
                       ├─ hkg  (香港)
                       └─ …更多 PoP
```

## 我们提供什么

- **多区域 anycast 接入**：一个地址，全球就近响应。
- **原生 IPv6 + IPv4**。
- **开放 Peering**：欢迎在各 IXP 与我们互联，详见 [互联 (Peering)](peering.md)。
- **Looking Glass**：实时查询我们的路由视图，见 [Looking Glass](looking-glass.md)。

## 快速导航

| 我想… | 去这里 |
|---|---|
| 了解网络结构、PoP 分布、我们的前缀 | [网络](network.md) |
| 和我们 peering | [互联 (Peering)](peering.md) |
| 查路由 / 排查可达性 | [Looking Glass](looking-glass.md) |
| 看运行状态、历史事件 | [状态与事件](status.md) |

!!! info "联系 NOC"
    运营 / 互联事宜请通过 [PeeringDB](https://www.peeringdb.com/) 上 AS64500 的联系方式，或互联申请页提交。
