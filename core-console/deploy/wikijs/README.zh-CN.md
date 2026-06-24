> [English](README.md) · **简体中文**

# Wiki.js — 可编辑文档

本部署使用 Wiki.js 2.x 替换静态 MkDocs wiki，提供浏览器内编辑器、版本历史、
全文检索以及可配置主题，并复用现有的 PostgreSQL 数据库。

## 拓扑

```
operator browser
  ├─ setup:  ssh -L 3002:127.0.0.1:3002 deploy-host  →  http://localhost:3002
  └─ prod:   https://wiki.example.com  (ctrl-01 nginx → ctrl-01:3002 tunnel → pop-03 Wiki.js)
                                       │
       Wiki.js (pop-03, localhost:3002, dedicated service user, on a data volume at /var/lib/wikijs)
                                       │
       DB: wikijs @ ctrl-01 primary Postgres (2001:db8:53::1) over the backbone
           → inherits streaming replication + PITR
```

## 组成部分

| 文件 | 位置 | 说明 |
|---|---|---|
| `install-pop03.sh` | pop-03 | 下载并解压 Wiki.js，安装 systemd 单元 |
| `config.sample.yml` | pop-03 `/var/lib/wikijs/config.yml` | 数据库 DSN（密码取自 `wikijs.pass`） |
| `ncn-wikijs.service` | pop-03 | Wiki.js 服务 |
| `ncn-wikijs-tunnel.service` | ctrl-01 | `ssh -L` ctrl-01:3002 → pop-03 Wiki.js |

`wikijs` 数据库与角色位于 ctrl-01 主库。`pg_hba` 允许来自 `2001:db8:50::/44`
的 `wikijs` 连接。密码存储在 ctrl-01 的 `/etc/ncn-core-console/wikijs.pass`
（权限 0600）。

## 安装（一次性，通过隧道在浏览器中完成）

1. `ssh -L 3002:127.0.0.1:3002 deploy-host`，打开 `http://localhost:3002`。
2. 完成向导：管理员邮箱、强密码，以及站点 URL `https://wiki.example.com`。
3. 安装后配置（在 Admin 区域，或通过 GraphQL API 脚本化）：
   - **Storage → Git**：指向本仓库（部署密钥），使编辑内容同步至 git。
   - **Groups**：设置一个 `Guests`（匿名）组，仅对公开路径具有读权限；
     已认证用户可读取所有页面；管理员可写。
   - **Theme / Locale**：深色主题，按需设置语言区域。
   - 将既有 Markdown（来自先前的 `wiki/` 目录）导入为初始页面。

## SSO — 通过控制台登录（控制台作为 IdP）

Wiki.js 通过控制台自身的登录页面进行认证，而非外部提供方：

```
Wiki.js account login  →  admin.example.com/api/v1/auth/idp/authorize
   (no console session? → 302 /login → operator logs in → full-page return)
   → code → wiki.example.com/login/oauth2/callback
   → Wiki.js exchanges at /idp/token → reads /idp/userinfo → signs in
```

- IdP 是 ncn-api 中的一个 OAuth2 授权码提供方
  （`backend/idp_provider.go`，`/api/v1/auth/idp/{authorize,token,userinfo}`），
  受现有控制台会话约束。客户端凭据存储于 `oauth.env`
  （`NCN_WIKI_OAUTH_CLIENT_ID/_SECRET/_REDIRECT`）。
- 只有已认证的控制台操作员（已绑定并获批准）才能获取授权码，因此 Wiki.js
  会将每个 SSO 用户自动加入 Operators 组（id 3 → 读取 `ops/*`）。在
  `authentication` 表中策略为 `oauth2`。
- `Login.vue` 将所有登录后重定向都经由 `navigateAfterLogin()` 处理，因此
  指向 `/api/` 的 next 目标（即 authorize URL）会执行整页重定向，而非 SPA 路由。

## 品牌设置

品牌相关值存储在 `settings` 表中（value 列为 `json` 而非 jsonb——需使用
`pg_read_file(...)::json` 并在之后重启）：`title` 与 `company` 设置站点名称；
`theming.darkMode=true`；`theming.injectHead` 定义 SVG favicon；
`theming.injectCSS` 通过 `.nav-header-inner::before` 在页头前置同一图标。
也可在 Admin → General → Site Logo 中上传页头 logo 图片（设置 `logo.hasLogo`）。
分组：Guests(2) 仅可读取 `home` 与 `public/*`；Operators(3) 可读取所有页面。

## 切换上线

完成配置与访问规则后，将 `wiki.example.com` 指向 Wiki.js
（nginx 反向代理 → :3002 隧道），并下线静态 MkDocs vhost。Wiki.js 自行管理
认证，因此公开页面匿名可读、内部页面需登录，二者由同一主机提供服务。
