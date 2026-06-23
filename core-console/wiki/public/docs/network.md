# 网络

## 自治系统

| | |
|---|---|
| **ASN** | AS64500 |
| **协议** | 原生 IPv6 + IPv4 |
| **类型** | 多 PoP anycast |
| **PeeringDB** | 搜索 AS64500 |

## PoP 分布

我们在多个地区部署 PoP（接入点），每个 PoP 都宣告同一段 anycast 地址。当前覆盖（持续扩展中）：

| 区域 | 代号 | 示例节点 |
|---|---|---|
| 东京 Region A | `tyo` | ctrl-01 … |
| 香港 Region C | `hkg` | pop-03 … |
| 法兰克福 Region B | `fra` | pop-05 |
| 新加坡 Region D | `sin` | pop-06 |
| 台北 Region E | `tpe` | pop-08 |

实时节点与状态请用 [Looking Glass](looking-glass.md) 查询，或看 [状态页](status.md)。

## anycast 是怎么工作的

1. 每个 PoP 通过 BGP 向上游/IXP 宣告**相同的前缀**。
2. 互联网上的路由器各自选择**到该前缀 AS 路径最短**的那条。
3. 于是不同地区的用户被**就近**送达不同 PoP。
4. 某个 PoP 故障并撤回宣告时，流量在 BGP 收敛后**自动改走次近的 PoP**——对用户基本无感。

!!! tip "为什么延迟低"
    因为你的流量不必绕到地球另一端的某台固定服务器——它在离你最近的 PoP 就被接住了。

## 健壮性

- **就近 + 冗余**：单 PoP 下线由 anycast 自动绕开。
- **健康撤回**：PoP 不健康时会从 anycast 主动撤回宣告，避免把流量吸进无法服务的节点。
- **多上游**：每个 PoP 接多个上游/IXP。
