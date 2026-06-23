# 互联 (Peering)

我们采取**开放 peering** 政策，欢迎在共同接入的 IXP 或通过 PNI 与 AS64500 互联。

## 基本信息

| | |
|---|---|
| **ASN** | AS64500 |
| **Peering 政策** | Open |
| **IPv6** | 必须支持（我们是 IPv6-first） |
| **MD5** | 可选 |
| **详情** | PeeringDB: AS64500 |

## 前置要求

- 在 PeeringDB 维护你的 AS 记录（我们用它校验）。
- 注册有效的 **RPKI ROA** / IRR 记录——我们做来源校验，未通过的宣告可能被拒。
- 双方各自做合理的前缀过滤与 max-prefix 限制。

## 如何申请

1. 确认我们在同一个 IXP，或商定 PNI。
2. 通过**互联申请页**提交你的 ASN、IXP、对接 IP 与联系方式。
3. 我们核对 PeeringDB / RPKI 后配置会话并通知你。

!!! note "RPKI"
    我们发布自己的 ROA 并对收到的宣告做来源有效性校验。请确保你的前缀有有效 ROA，避免被判为 invalid。

## 我们会宣告什么

- 我们自己的 anycast 前缀（IPv6 + IPv4）。
- 我们不会把从一个 peer 学到的路由再转卖给另一个 peer（非 transit）。
