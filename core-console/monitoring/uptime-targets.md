# NCN Uptime 监控目标清单

> 给"正经的" uptime tracker(SaaS 或自托管 Uptime Kuma)用的粘贴清单。
> 目标:用**独立于本网络**的探测 + **抗抖动配置**取代/补充自研告警 bot,
> 解决"报警太频繁"。当前活跃 PoP = 8 台(fmt-01 已下架,不监控)。

## 一、公网 anycast 服务(外部视角 · 最重要)

这些是"用户能不能用"的真相,放外部 SaaS 多地探测。已实测返回码:

| 名称 | 类型 | URL | 期望 |
|---|---|---|---|
| NCN public site | HTTPS | `https://example.com/` | 200 |
| NCN health API | HTTPS | `https://example.com/api/v1/health` | 200(最干净的健康探针) |
| Status page | HTTPS | `https://example.com/status` | 200 |
| Looking Glass API | HTTPS | `https://example.com/api/v1/lg/sessions` | 200 |
| Admin console | HTTPS | `https://admin.example.com/login` | 200(根路径会 302 跳登录,直接探 /login) |
| TLS 证书到期 | Cert | `example.com:443` | 提前 14 天告警 |

## 二、各 PoP 可达性(内部视角 · 定位哪台挂)

每台两条:unicast v4 ping + anycast anchor v6 ping。
**探 unicast IP / v6 anchor,不要探 anycast VIP**——否则探到的是"最近那台",看不出具体哪台坏。
(注:v6 anchor ping 需要探测端支持 IPv6;UptimeRobot 免费档不支持 v6,Better Stack / 自托管 Kuma 支持。)

| PoP | 位置 | Ping v4 (unicast) | Ping v6 (anchor 2001:db8:R::N) |
|---|---|---|---|
| pop-03 | Region C | `198.51.100.3` | `2001:db8:51::1` |
| pop-04 | Region C | `198.51.100.4` | `2001:db8:51::2` |
| ctrl-01 | Region A | `198.51.100.1` | `2001:db8:53::1` |
| pop-01 | Region A | `198.51.100.2` | `2001:db8:53::2` |
| pop-02 | Region A (Shibuya) | `198.51.100.5` | `2001:db8:53::3` |
| pop-08 | Region E | `198.51.100.6` | `2001:db8:56::1` |
| pop-06 | Region D | `198.51.100.8` | `2001:db8:54::1` |
| pop-05 | Region B | `198.51.100.7` | `2001:db8:55::1` |

## 三、抗抖动配置(治"报警太频繁"的关键 —— 比选哪个工具更重要)

每个监控项统一这样设:

- **探测间隔**:60s
- **确认重试**:连续 **3 次** 失败才判 DOWN(`retries = 3` / `confirmation period`)——单次/瞬时抖动直接忽略
- **多地确认**:anycast 服务要求 **≥2 个探测地点都失败** 才告警(SaaS 多 region)
- **通知重发间隔**:DOWN 后每 **30 分钟** 才重发一次(`resend every 30 min`),别每分钟刷
- **恢复也发**:UP 时发一条 resolved,flap 时成对出现 = 一眼看出是抖动
- **维护窗口**:计划内变更(比如 mesh/bird 改配)前先开 Maintenance 静默
- **告警分级**:公网 anycast 服务 = 紧急(电话/置顶);单台 PoP unicast 不可达 = 普通(其它 PoP 还在扛 anycast,不必半夜叫)

## 四、工具选择

- **外部层(立即可做,无需主机)**:Better Stack(多地确认 + 升级策略 + v6,免费档够)或 UptimeRobot(最省事,但免费档无 v6/无多地确认)。把"第一节"的公网项粘进去即可。
- **内部层(需一台独立小鸡,勿放 ctrl-01)**:Uptime Kuma(见 `monitoring/docker-compose.yml`),接管"第二节"的逐台 ping + 复用现有 Telegram bot 通知。
- ⚠️ **不要装在 ctrl-01**:它内存仅剩 ~92MB、磁盘 75% 满,且它本身就是被监控对象,自托管在此既危险又无意义(它挂了没人报)。

## 五、Telegram 通知复用

现有自研 bot 的 TG 配置(bot token + chat id)可直接填进 Kuma/Better Stack 的 Telegram 通知渠道。
建议把**自研 bot 降级**为只发低频业务事件(上下架 / 证书到期 / mesh 应用结果),
把"节点 up/down"这类高频探测交给正经 tracker —— 各司其职,不再互相刷屏。
