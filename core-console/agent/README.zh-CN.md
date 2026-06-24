# ncn-agent

> [English](README.md) · **简体中文**

每个 PoP 上的遥测代理。它替代控制节点（`ctrl-01`）上 `backend/fleet.go`
所使用的 SSH 轮询传输方式——原方式每 15s 抓取一次各远端 PoP 的数据。

它产生与原传输方式相同的 14 段 shell 管道输出，但通过 HTTPS 配合
HMAC-bearer 认证提供服务，而非分散建立大量 SSH 连接。

## 背景

在重构之前，控制节点的 `fleetScraper` 每 15s 建立新的 SSH 连接（每个远端
PoP 一个：`pop-02`、`pop-03`、`pop-04`……）。每个连接都会 fork 一个
`ssh` 进程，完成一次完整的 TCP+SSH 握手，运行管道，然后关闭。其输出由
scraper 解析。

重构之后，每个 PoP 运行监听在 `:9101`（HTTPS）上的 `ncn-agent`。控制节点
发起带 HMAC 签名头部的 `GET /v1/snapshot`；代理在本地运行相同的 shell
管道并返回字节级一致的输出。scraper 的解析器保持不变。

## 迁移

新传输方式与 SSH 并行交付。`fleetNode` 上的每节点 `Transport` 字段用于
选择采用哪种方式：

- `""` / `"ssh"` → 原始 SSH 传输（默认；行为不变）。
- `"rest"`      → 通过 HTTPS 连接 `ncn-agent`，在出现任何错误时回退到 SSH。

按 PoP 逐个推进上线：

1. 在控制节点上运行一次 `scripts/agent-ca-bootstrap.sh`——构建内部 CA。
2. 对每个 PoP，在控制节点上运行 `scripts/agent-node-provision.sh <node-id>`。
   该脚本会生成一个新的 HMAC 密钥以及由 CA 签发的每节点 TLS 证书，将二进制
   文件、证书、密钥和 systemd unit 复制到节点上，并启动服务。
3. 编辑 `backend/fleet.go`，将该节点的 `Transport` 设为 `"rest"`，然后在
   控制节点上重启 ncn-api。
4. 观察 fleet 仪表盘。字节级一致的输出意味着解析结果与 SSH 路径完全相同——
   `load1`、`MemPct`、BIRD 协议以及探测 RTT 都应读数一致。
5. 当所有 PoP 都切换到 `"rest"` 并稳定运行至少 24h 后，Phase 3 将从
   `fleet.go` 中移除 `fetchRemoteSSH` 和 `Transport` 字段。

`term.go` 中的终端 WebSocket 仍保留其 SSH 路径。那是一个独立的关注点
（交互式命令流，而非遥测轮询），不属于本次重构范围。

## 报文格式

```
GET /v1/snapshot HTTP/1.1
Host: <node-public-ip>:9101
Authorization: NCNHMAC ts=<unix>,nonce=<base64url>,sig=<base64url>
X-NCN-Probes: name1|target1|type1,name2|target2|type2,...
```

签名计算方式如下：

```
sig = HMAC-SHA256(
    /etc/ncn-agent/hmac.key,
    ts + "\n" + nonce + "\n" + METHOD + "\n" + PATH + "\n" + xprobes
)
```

响应为 `text/plain`，包含 15 个分段，分段之间由恰好为 `___SEP___` 的行
分隔——与 `backend/fleet.go` 已能从 SSH 管道解析的形态相同。

## 磁盘上的文件

在控制节点上：
- `/etc/ncn-core-console/agent-ca/ca.crt`（644）+ `ca.key`（600）——内部 CA。
- `/etc/ncn-core-console/agent-keys/<node>.key`（600）——每节点 HMAC 密钥。

在每个 PoP 上：
- `/usr/local/bin/ncn-agent`（755）——二进制文件。
- `/etc/ncn-agent/tls.crt`（644）+ `tls.key`（600）——由控制节点 CA 签发的证书。
- `/etc/ncn-agent/hmac.key`（600）——与控制节点 `agent-keys/<node>.key` 字节相同。
- `/etc/ncn-agent/agent.conf`——监听地址和节点 id（仅供参考）。
- `/etc/systemd/system/ncn-agent.service`——unit 文件。

## 构建

```
cd core-console/agent
go build -o ncn-agent
```

也可通过 `deploy.sh` 构建；Phase 1 及之后会新增一个 `agent` 子命令，在
provisioning 过程中构建并复制二进制文件。
