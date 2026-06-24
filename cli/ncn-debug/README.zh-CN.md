# ncn-debug

> [English](README.md) · **简体中文**

一个基于 NCN 控制台 REST API 的只读调试 CLI，面向更倾向于在终端而非 Web 控制台中
查看集群状态的运维人员。该工具仅使用 Go 标准库编写，编译为单个静态二进制文件，无运行时
依赖。

## 安装

```sh
cd cli/ncn-debug && go build -o ncn-debug
# 或生成交叉编译版本：
GOOS=linux  GOARCH=amd64 go build -o ncn-debug-linux-amd64
GOOS=darwin GOARCH=arm64 go build -o ncn-debug-darwin-arm64
```

将该二进制文件放置在 `$PATH` 中的任意位置。

## 鉴权

除 `status` 外的每个命令都需要个人 API 令牌。在
**admin.example.com → Security → API Tokens**（格式为 `ncntok_…`）中创建令牌，
然后通过以下任一方式提供：

```sh
ncn-debug token ncntok_xxxxxxxx     # 保存至 ~/.config/ncn-cli/token (0600)
# 或：
export NCN_TOKEN=ncntok_xxxxxxxx
# 或按调用传入：
ncn-debug --token ncntok_xxxxxxxx fleet
```

解析顺序：`--token` › `$NCN_TOKEN` › `~/.config/ncn-cli/token`。

该令牌是携带操作员角色的 bearer 凭据，应当如同密码一样妥善保管。若发生泄露，可在同一
Security 面板中吊销。

## 命令

```
ncn-debug whoami            验证令牌；显示操作员、角色、会话 TTL
ncn-debug fleet             每个 PoP 一行的健康状态表
ncn-debug node <id>         单个 PoP 的完整详情（cpu/mem/disk/bird/probes/…）
ncn-debug bgp [id]          整个集群或单个 PoP 的 BGP 会话
ncn-debug incidents         未解决的及近期（30 天）事件
ncn-debug status            公开的可用性摘要（无需令牌）
ncn-debug get <path>        对任意 /api 路径执行已鉴权的原始 GET，输出美化 JSON
ncn-debug token <ncntok_…>  保存 API 令牌
```

### 示例

```sh
ncn-debug fleet                                   # 集群整体概览
ncn-debug node pop-05                              # 单个 PoP 的详细信息
ncn-debug bgp pop-04                               # 仅 pop-04 的会话
ncn-debug get /api/v1/bird/protocol?node=ctrl-01    # 任意端点
ncn-debug --json fleet | jq '.[] | select(.ok==false)'   # 便于脚本处理
```

`get` 会将令牌发送至任意 `/api/v1/…` 路径并美化打印返回的 JSON，因此 Web 控制台能够
获取的任何端点也都可以通过该 CLI 访问。

## 标志

```
--host URL     控制台基础 URL（默认 https://admin.example.com，或 $NCN_HOST）
--json         输出原始 JSON 而非格式化结果（可管道至 jq）
--timeout D    单次请求超时时间（默认 20s）
```

标志可出现在命令之前或之后。当输出不是终端或设置了 `NO_COLOR` 时，彩色输出会自动禁用。

## 说明

- 只读。该 CLI 仅执行 `GET` 请求，没有任何会产生变更的命令。
- `main.go` 中的传输类型与 `core-console/backend`（fleet.go / bird_scrape.go /
  tunnel.go）保持一致。若后端的某个 JSON 标签发生变更，则必须同步更新此处对应的
  结构体。未知字段会被忽略，因此字段漂移会平滑降级——重命名的字段会显示为空白或零值，
  而不会导致命令失败。
