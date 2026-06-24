# 网络

> [English](network.md) · **简体中文**

## 自治系统

| | |
|---|---|
| **ASN** | AS64500 |
| **协议** | 原生 IPv6 与 IPv4 |
| **拓扑** | 多 PoP anycast |
| **PeeringDB** | 搜索 AS64500 |

## PoP 分布

PoP（接入点）部署在多个地区，每个 PoP 宣告相同的 anycast 前缀。当前覆盖如下，可能随时间变化：

| 区域 | 代号 | 示例节点 |
|---|---|---|
| Region A | `tyo` | ctrl-01 … |
| Region C | `hkg` | pop-03 … |
| Region B | `fra` | pop-05 |
| Region D | `sin` | pop-06 |
| Region E | `tpe` | pop-08 |

实时节点列表及其状态可通过 [Looking Glass](looking-glass.md) 或[状态页](status.md)查询。

## anycast 工作原理

1. 每个 PoP 通过 BGP 向其上游与 IXP 宣告**相同的前缀**。
2. 互联网上的路由器各自选择到该前缀**AS 路径最短**的路由。
3. 因此，不同地区的用户被定向到**最近**的 PoP。
4. 当某个 PoP 故障并撤回其宣告时，流量在 BGP 收敛后转移到**次近**的 PoP，对用户的影响极小。

!!! note "延迟"
    流量在最近的 PoP 终结，而非被路由到位于另一地区的某台固定服务器。

## 健壮性

- **就近与冗余**：单个 PoP 下线时，anycast 自动绕开。
- **基于健康状态的撤回**：不健康的 PoP 会撤回其 anycast 宣告，避免流量被定向到无法提供服务的节点。
- **多上游**：每个 PoP 连接多个上游与 IXP。
