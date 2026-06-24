# 互联 (Peering)

> [English](peering.md) · **简体中文**

本网络采用开放互联 (open peering) 政策。可在双方共同接入的互联网交换中心 (IXP) 或通过专线互联 (PNI) 与 AS64500 建立互联。

## 基本信息

| | |
|---|---|
| **ASN** | AS64500 |
| **互联政策** | Open |
| **IPv6** | 必须支持（本网络为 IPv6-first） |
| **MD5** | 可选 |
| **详情** | PeeringDB: AS64500 |

## 前置要求

- 在 PeeringDB 中维护准确的 AS 记录，该记录用于校验。
- 注册有效的 **RPKI ROA** / IRR 记录。本网络执行来源校验，未通过校验的宣告可能被拒绝。
- 双方均应配置合理的前缀过滤与 max-prefix 限制。

## 如何申请

1. 确认双方接入同一 IXP，或商定 PNI。
2. 通过互联申请页提交 ASN、IXP、对接 IP 地址与联系方式。
3. 提交信息将依据 PeeringDB 与 RPKI 进行核对，核对完成后配置会话并通知申请方。

!!! note "RPKI"
    本网络为自有前缀发布 ROA，并对收到的宣告执行来源有效性校验。前缀应具备有效 ROA，以避免被判定为 invalid。

## 会宣告的内容

- 本网络的 anycast 前缀（IPv6 与 IPv4）。
- 从某个 peer 学到的路由不会再宣告给另一个 peer（非 transit）。
