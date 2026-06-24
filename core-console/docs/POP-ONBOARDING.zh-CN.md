# 向集群添加新的 PoP

> [English](POP-ONBOARDING.md) · **简体中文**

本运行手册描述将一台新的 VPS 接入网络作为生产 PoP 的流程。通常耗时约
45 分钟，其中相当一部分用于与上游或 IX 进行 BGP 会话协商。

一个集群通常由一个中央控制节点（例如 `ctrl-01`）和若干 PoP 节点
（例如 `pop-01`、`pop-02` 等）组成。具体的节点清单与角色分配因部署而异。

---

## 0. 新 VPS 的预检

该 VPS 必须已可通过 SSH 密钥以 `root` 身份访问。如果它作为
cloud-init 镜像交付并使用其他默认用户（例如 `debian`），则需先迁移到
root 登录——参见下文的 *root SSH 迁移* 一节。

在新主机上运行
[`scripts/pop-healthcheck.sh`](../scripts/pop-healthcheck.sh)。在此阶段
预计会有若干检查失败（尚无 agent），但以下各项必须通过：

* **NTP 已同步**——若 `systemd-timesyncd` 未运行则安装它：
  ```
  apt install -y systemd-timesyncd && systemctl enable --now systemd-timesyncd
  ```
  （未配置 NTP 即接入的节点，会因 agent HMAC 时间戳偏移而拒绝每个请求，
  直至修复为止。这是一项阻塞性要求。）

* **针对 `birdc`、`wg`、`ip` 的 sudo NOPASSWD**——参考任一现有 PoP 上的
  `/etc/sudoers.d/` 以了解预期约定。

* **sshd_config 中的 PasswordAuthentication no**——参见 *SSH 加固* 一节。

* **`/root/.ssh/authorized_keys` 中的 fleet-key**——控制节点的交互式终端
  WebSocket 需要它（这是控制节点对外仅存的 SSH 出站途径）。

---

## 1. 在代码中注册节点

新 PoP 必须在 **两处** 添加：

* `core-console/backend/fleet.go`——添加到 `nodes: []fleetNode{ ... }`：
  ```go
  {ID: "pop-0N", Label: "City, CC", Country: "CC",
      Address: "<public IP>", Lat: 0, Lon: 0, SSHHost: "pop-0N"},
  ```
* `core-console/scripts/agent-node-provision.sh`——添加到顶部的 `case`
  语句中：
  ```bash
  pop-0N) SSH_USER="root"   ; ARCH="amd64" ; SAN_IP="<public IP>" ;;
  ```

其他使用方（Landing.vue 设施地图、WorldMap.vue popMeta、monitor.go 探测
目标）会根据变更模式自动继承该条目，或需要添加一行。若该 PoP 应出现在
公共地图上，请检查 `core-console/src/views/Landing.vue` 与
`core-console/src/components/WorldMap.vue`。

提交并部署后端。此时 ncn-api 已知晓新 ID，并将在下一个 15s 周期尝试抓取
它，在第 2 步完成之前会以 "no agent HMAC key" 失败。

---

## 2. 配置 agent

在控制节点上（不要在新 PoP 上）：

```bash
sudo /opt/ncn-core-console/scripts/agent-node-provision.sh pop-0N
```

该脚本会：

1. 生成一个新的 32 字节 HMAC 密钥 → `/etc/ncn-core-console/agent-keys/pop-0N.key`
2. 生成一个由内部 CA（位于 `/etc/ncn-core-console/agent-ca/`）签发的
   1 年期 ECDSA-P-256 TLS 证书
3. 通过 scp 将二进制文件、证书、密钥、HMAC 密钥、配置和 systemd 单元
   传输到新 PoP（对于非 root 登录经由 `/tmp/ncn-agent-stage-$$` 暂存，
   再以 sudo 安装到最终位置）。
4. 启用并启动 `ncn-agent.service`
5. 从控制节点 curl `/v1/healthz` 以验证可达性

输出应以如下内容结束：
```
{"ok":true,"server_ts":..,"uptime_s":1,"version":"phase4b-..."}
✓ pop-0N provisioned
```

如失败，请根据脚本输出排查；该脚本很短（约 130 行）。

---

## 3. 重新加载 ncn-api 以读取新的 HMAC 密钥

```
sudo systemctl reload ncn-api
```

`reload`（而非 `restart`）发送 SIGHUP，在进程内调用
`fleet.ReloadAgentKeys()`。其他 PoP 不会出现 401 窗口。

验证：
```
journalctl -u ncn-api -n 5 | grep 'agent keys loaded'
# fleet: agent keys loaded — N/N remote keys present
```

重新加载后 15s 内，`/admin/fleet` 应显示新 PoP，并填充
load/mem/CPU 等数据。

---

## 4. 在新 PoP 上运行健康检查

```
ssh root@<new-pop-ip> bash -s < /opt/ncn-core-console/scripts/pop-healthcheck.sh
```

所有 `✓` 行应通过。`?` 行为信息性提示（例如尚未安装 BGP 守护进程，对于
非 BGP 边缘节点这是可接受的）。

任何 `✗` 行都必须在继续之前修复。

---

## 5. SSH 加固（在暴露 PoP 之前为强制项）

确保 `/etc/ssh/sshd_config`（或
`/etc/ssh/sshd_config.d/00-ncn.conf`）包含：

```
PasswordAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin prohibit-password
KbdInteractiveAuthentication no
PubkeyAuthentication yes
```

重新加载 sshd：
```
sshd -t && systemctl reload sshd
```

在关闭现有会话之前，必须验证基于密钥的登录仍然有效。打开一个新终端：
```
ssh -o BatchMode=yes -i /etc/ncn-core-console/fleet-key root@<new-pop-ip> whoami
```
该命令必须无提示地返回 `root`。

---

## 6. root SSH 迁移（仅当 cloud-init 强制非 root 登录时）

某些厂商镜像（尤其是 Debian cloud-init）交付时
`/root/.ssh/authorized_keys` 中包含将 root 重定向到非 root 用户的
forced-command 条目：

```
no-port-forwarding,...command="echo 'Please login as the user \"debian\"...'" ssh-ed25519 ...
```

迁移到 root 登录（与集群其余部分一致）：

```bash
# 从控制节点执行
ssh debian@<new-pop-ip> bash <<'EOF'
sudo cp -a /root/.ssh/authorized_keys /root/.ssh/authorized_keys.cloudinit.bak
sudo cp -a /home/debian/.ssh/authorized_keys /root/.ssh/authorized_keys
sudo chown root:root /root/.ssh/authorized_keys
sudo chmod 600 /root/.ssh/authorized_keys
EOF

# 验证
ssh -i /etc/ncn-core-console/fleet-key root@<new-pop-ip> whoami
```

若两者均成功，则该节点就绪。保留
`/root/.ssh/authorized_keys.cloudinit.bak` 处的备份，以便在需要时恢复
forced-command 保护。

---

## 7. 密钥备份

在第 2 步生成新的 HMAC 密钥和证书后，运行：

```
sudo /opt/ncn-core-console/scripts/backup-secrets.sh
```

这会将 `/etc/ncn-core-console/agent-keys/*.key` 和 agent CA 打包为控制
节点上 `backups/` 目录下的 age 加密 tarball。（`backups/` 已被
gitignore——应在本地保留，并按运营方自己的策略复制到异地。）

---

## 8. 可选：BGP peering

如果该 PoP 承载 BGP，请在 agent 启动后于该节点上配置
`/etc/bird/bird.conf`。会话在节点侧配置；fleet 快照会在下一个周期自动
获取协议列表。管理端的 Servers 页面可以按标准样式生成 mesh 和 BIRD
配置，并（可选启用）通过 `birdc configure soft` 自动应用。

---

## 反向操作——下线一个 PoP

1. 停止接受 BGP 会话（排空）。
2. 在该节点上执行
   `systemctl stop ncn-agent && systemctl disable ncn-agent`。
3. 从 `fleet.go` 和 `agent-node-provision.sh` 映射中移除该条目。
4. 在控制节点上删除 `/etc/ncn-core-console/agent-keys/<node>.key`。
5. `sudo systemctl reload ncn-api`。
6. 可选：擦除该 VPS 或将其归还给厂商。

---

## 故障排查速查表

| 现象 | 可能原因 | 处理 |
|---|---|---|
| `fleet: scrape X FAIL · agent 401: unauthorized` | HMAC 密钥不匹配 | `systemctl reload ncn-api` 以重新读取密钥 |
| `fleet: scrape X FAIL · context deadline exceeded` | PoP CPU 饱和或网络抖动 | 在该 PoP 上检查 `pop-healthcheck.sh`，查看 `cpu-saturated` 告警 |
| `fleet: scrape X FAIL · agent 400: timestamp out of skew window` | PoP 上未运行 NTP | 在该 PoP 上安装 systemd-timesyncd |
| `agent-cert-expiring` 告警且剩余 < 30 天 | 需要轮换 | 重新运行 `agent-node-provision.sh <node>` 并 `systemctl reload ncn-api` |
| agent 已启动但从控制节点 curl `/v1/healthz` 挂起 | 防火墙阻止来自控制节点 IP 的 9101 入站 | 开放防火墙：必须允许控制节点的出站 IP 访问 :9101/tcp |
