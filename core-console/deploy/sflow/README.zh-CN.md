# 流量分析 — 流采集

> [English](README.md) · **简体中文**

控制台的 Traffic 页面（netflow.go → `/api/v1/auth/flow/top`）读取由 **goflow2** 采集器
写入的 JSON-lines 文件。该采集器由运行在每个 PoP 上的 **softflowd** 提供采样流。
（最初计划通过 hsflowd 使用 sFlow，但目标发行版未打包 hsflowd，因此改用
softflowd → NetFlow v9；它在 apt 中可用，并产生相同的 goflow2 JSON 输出。）

    each PoP: softflowd  --NetFlow v9/udp2055-->  ctrl-01: goflow2  --JSONL-->  /var/log/ncn-flows/flows.jsonl (tmpfs)
                                                                                  ^ ncn-api tails this (NCN_FLOW_FILE default)

## 为何采用 ctrl-01 + tmpfs
ncn-api 运行在 ctrl-01 上并 tail 一个本地文件，因此采集器与其同机部署。
控制节点的根文件系统可用空间有限，所以将 JSONL 放在一个 **64M tmpfs**
（`/var/log/ncn-flows`，在 fstab 中配置）上 — 由内存支持，因而无法填满控制节点的
磁盘。流数据是临时的（10 分钟窗口），因此重启后丢失是可接受的。goflow2 以
`nobody` 身份运行，MemoryMax=256M，OOMScoreAdjust=600。每小时的 `logrotate`
（copytruncate，15M）保持文件较小；tail 进程在截断时重置。

## 采集器（ctrl-01）
    GOBIN=/usr/local/bin go install github.com/netsampler/goflow2/v2/cmd/goflow2@latest
    # fstab: tmpfs /var/log/ncn-flows tmpfs size=64M,mode=0777,nosuid,nodev 0 0
    cp goflow2.service /etc/systemd/system/ && systemctl enable --now goflow2
goflow2 的默认监听已覆盖 `netflow://:2055`（以及 `sflow://:6343`）。控制节点的入站
策略为 `accept`，因此骨干网的 NetFlow 可以到达它。

## 导出器（每个 PoP）
softflowd 打包的 unit 是一个 `/bin/true` 占位符，必须用此处提供的 softflowd.service
替换它。说明：
- `-d` = 在前台运行（用于 systemd），而非 `-D`（调试）。
- softflowd 没有 `idle` 超时；有效名称为 tcp/udp/icmp/general/maxlife/**expint**。
- IPv6 采集器：`-n [2001:db8:53::1]:2055`（带方括号）可用。
- `-s 1000` = 1:1000 采样（生产）。快速测试可使用 `-s 1`（每个包）。

    apt-get install -y softflowd
    cp softflowd.service /etc/systemd/system/   # set -i <iface> (default eth0)
    systemctl daemon-reload && systemctl enable softflowd && systemctl restart softflowd

在 ctrl-01 上验证：`tail flows.jsonl | grep -o '"sampler_address":"[^"]*"' | sort -u`
应显示每个 PoP 的骨干网锚点地址（2001:db8::/32 地址）。

## ASN 构成（可选）
仅当 goflow2 富化了 AS 编号（softflowd 不会添加它们）时，`src_as`/`dst_as` 列才会
被填充 — 这需要一个 MaxMind GeoLite2-ASN 数据库。没有它时，IP/端口/协议/方向的
分解仍然可用。

## ASN 富化（Team Cymru，无需许可证）
由于 softflowd 不填充 src_as/dst_as，netflow.go 在后台 goroutine 中通过 Team Cymru
DNS（`*.origin6.asn.cymru.com` / `*.origin.asn.cymru.com`）解析头部流量 IP 的源 AS，
并缓存 6 小时。采集器主机（ctrl-01）需要出站 DNS 支持。Traffic 页面上的 AS 列大约在
1-2 分钟内填充完成。

## 状态
- softflowd 在每个 PoP（ctrl-01, pop-0N）上导出。
- `flow_agg` 数据库历史记录是后续工作；v1 是内存中的 10 分钟窗口。
