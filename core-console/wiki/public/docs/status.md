# 状态与事件

## 实时状态

各 PoP 的在线状态与可达性由我们的 uptime 跟踪持续监测。anycast 的特性决定了：**单个 PoP 异常通常不影响整体可用性**——流量会自动改走次近的健康 PoP。

## 事件 (Incidents)

计划内维护与故障会以 incident 形式发布，包含影响范围、时间线与处置进展。

## 报障

遇到疑似网络问题时，请提供：

- 你的来源地区 / ASN；
- 目标前缀或地址；
- 从你侧的 traceroute（v6 优先）；
- 以及（如果可能）一次 [Looking Glass](looking-glass.md) 查询结果。

这些信息能让我们快速判断是 peering、择路还是源站层面的问题。

!!! info "联系"
    紧急网络问题请通过 PeeringDB 上 AS64500 的 NOC 联系方式上报。
