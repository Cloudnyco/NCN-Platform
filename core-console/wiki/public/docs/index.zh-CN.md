# 网络概览

> [English](index.md) · **简体中文**

该网络以自治系统 **AS64500** 运行，是一个多 PoP 的 anycast 网络，原生支持 IPv6，并支持 IPv4。

## Anycast 模型

同一段 IP 前缀在多个接入点（PoP）同时进行宣告。BGP 将流量路由到拓扑上最近的 PoP，从而降低延迟，并在某个 PoP 发生故障时自动绕开。

```
user (region A) ─┐
user (region B) ─┤   one anycast prefix
user (region C) ─┘        │
                          ├─ pop-01
       BGP nearest-exit ──┼─ pop-02
                          ├─ pop-03
                          └─ … additional PoPs
```

## 服务

- **多区域 anycast 接入**：单一前缀，由最近的 PoP 提供服务。
- **原生 IPv6 并支持 IPv4**。
- **开放 Peering**：在参与的 IXP 提供互联。参见 [Peering](peering.md)。
- **Looking Glass**：实时路由视图查询。参见 [Looking Glass](looking-glass.md)。

## 导航

| 主题 | 页面 |
|---|---|
| 网络结构、PoP 分布、已宣告的前缀 | [Network](network.md) |
| Peering | [Peering](peering.md) |
| 路由查询与可达性排查 | [Looking Glass](looking-glass.md) |
| 运行状态与历史事件 | [Status and incidents](status.md) |

!!! info "NOC 联系方式"
    运营与互联事宜，请使用 [PeeringDB](https://www.peeringdb.com/) 上 AS64500 的联系方式，或通过互联申请页提交请求。
