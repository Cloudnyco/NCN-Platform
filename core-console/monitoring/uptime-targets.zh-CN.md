# 可用性监控目标清单

> [English](uptime-targets.md) · **简体中文**

本文档列出需要粘贴到专用可用性监控工具(SaaS 服务或自托管的 Uptime Kuma 实例)中的监控目标。其目标是使用独立于被监控网络的探测,配合抗抖动配置,以替代或补充自研告警 bot,并降低告警噪声。监控目标覆盖当前活跃的 PoP;已下架的节点不予监控。

## 一、公网 anycast 服务(外部视角,优先级最高)

这些目标反映终端用户能否访问服务,应使用外部 SaaS 服务从多个地理位置进行探测。各目标的期望返回码如下。

| 名称 | 类型 | URL | 期望 |
|---|---|---|---|
| 公网站点 | HTTPS | `https://example.com/` | 200 |
| 健康 API | HTTPS | `https://example.com/api/v1/health` | 200(最干净的健康探针) |
| 状态页 | HTTPS | `https://example.com/status` | 200 |
| Looking Glass API | HTTPS | `https://example.com/api/v1/lg/sessions` | 200 |
| 管理控制台 | HTTPS | `https://admin.example.com/login` | 200(根路径会 302 跳转登录页,直接探测 `/login`) |
| TLS 证书到期 | Cert | `example.com:443` | 提前 14 天告警 |

## 二、各 PoP 可达性(内部视角,用于定位故障节点)

每台 PoP 有两条检查:unicast IPv4 ping 与 anycast anchor IPv6 ping。应探测 unicast IP 和 IPv6 anchor,而非 anycast VIP。探测 anycast VIP 会被解析到最近的节点,无法判断具体是哪台节点发生故障。

注意:IPv6 anchor ping 要求探测端支持 IPv6。部分免费 SaaS 档位不支持 IPv6;自托管的 Uptime Kuma 以及若干 SaaS 服务商支持 IPv6。

| PoP | 位置 | Ping v4 (unicast) | Ping v6 (anchor 2001:db8:R::N) |
|---|---|---|---|
| pop-03 | Region C | `198.51.100.3` | `2001:db8:51::1` |
| pop-04 | Region C | `198.51.100.4` | `2001:db8:51::2` |
| ctrl-01 | Region A | `198.51.100.1` | `2001:db8:53::1` |
| pop-01 | Region A | `198.51.100.2` | `2001:db8:53::2` |
| pop-02 | Region A | `198.51.100.5` | `2001:db8:53::3` |
| pop-08 | Region E | `198.51.100.6` | `2001:db8:56::1` |
| pop-06 | Region D | `198.51.100.8` | `2001:db8:54::1` |
| pop-05 | Region B | `198.51.100.7` | `2001:db8:55::1` |

## 三、抗抖动配置(降低告警噪声的关键,比工具选择更重要)

对每个监控项统一采用以下设置:

- **探测间隔**:60s。
- **确认重试**:连续 **3 次** 失败后才判定为 DOWN(`retries = 3` / confirmation period)。单次或瞬时失败予以忽略。
- **多地确认**:anycast 服务仅在 **2 个及以上探测地点** 均失败时才告警(SaaS 多 region)。
- **通知重发间隔**:DOWN 事件发生后,最多每 **30 分钟** 重发一次(`resend every 30 min`),而非每分钟刷屏。
- **恢复通知**:UP 时发送一条 resolved 通知。抖动时成对出现的 down/up 通知便于识别抖动。
- **维护窗口**:在计划内变更(例如 mesh 或 BIRD 配置变更)前开启维护窗口以静默告警。
- **告警分级**:公网 anycast 服务为紧急级别(电话/置顶);单台 PoP unicast 不可达为普通级别,因为其余 PoP 仍在承载 anycast,该事件无需立即呼叫。

## 四、工具选择

- **外部层**(可立即实施,无需主机):优先选择支持多地确认、升级策略与 IPv6 的 SaaS 可用性服务商;不支持 IPv6 或多地确认的更简单服务商亦可作为选项。将第一节的公网目标粘贴到所选服务商中即可。
- **内部层**(需要一台独立的小型主机;不要运行在 `ctrl-01` 上):Uptime Kuma(见 `monitoring/docker-compose.yml`)负责第二节的逐台 ping,并可复用现有 Telegram bot 进行通知。
- 不要将内部层安装在 `ctrl-01` 上:它的可用内存与磁盘余量有限,且它本身就是被监控对象。在被监控节点上自托管监控既有风险又无意义,因为该节点故障时将无法发出告警。

## 五、复用 Telegram 通知

现有自研 bot 的 Telegram 配置(bot token 与 chat ID)可直接填入 Uptime Kuma 或所选 SaaS 服务商的 Telegram 通知渠道。

建议将自研 bot 的职责收窄为只发送低频业务事件(节点上下架、证书到期、mesh 应用结果),并将“节点 up/down”这类高频探测交给专用监控工具,从而分工明确并避免重复通知。
