# Webmail

> [English](README.md) · **简体中文**

面向 `mail.example.com` 的独立 Web 邮件应用。前端与后端均运行于邮件节点
（`pop-03`），与邮件服务器位于同一主机。该应用不依赖控制平面，也不依赖独立的
部署主机。

```
pop-03
├── nginx (443)
│   ├── /        →  /var/www/webmail        (Vue dist)
│   └── /api/*   →  127.0.0.1:9000          (ncn-mail Go service)
├── ncn-mail.service (systemd)
│   └── IMAP/SMTP loopback to dovecot/postfix on the same host
├── dovecot                                  (IMAPS :993 with LE cert)
├── postfix                                  (submission :587 STARTTLS)
└── rspamd                                   (DKIM signs outbound)
```

## 目录结构

```
webmail/
├── backend/                 Go service (ncn-mail)
│   ├── main.go
│   ├── mail.go              IMAP/SMTP client + cred store + endpoints
│   └── go.mod
├── src/                     Vue 3 frontend (standalone Vite)
│   ├── App.vue
│   ├── main.ts
│   └── i18n/                en / zh-CN / zh-TW
├── deploy/
│   ├── ncn-mail.service     systemd unit
│   └── nginx-mail.conf
├── package.json             frontend
├── vite.config.ts
└── README.md
```

## 开发

```bash
# frontend
cd webmail
npm install
npm run dev          # http://localhost:5174, /api/* proxies to the deployed backend

# backend (requires a local Go toolchain; otherwise build on the build host as per deploy)
cd backend
go run .             # listens on 127.0.0.1:9000
```

## 部署

部署经由持有 fleet SSH 密钥的构建主机进行；当工作站无法直接以 `debian` 用户
SSH 访问 `pop-03` 时，这是规范的部署路径。

### 后端（Go）

```bash
# 1. sync source to the build host (where Go is installed)
rsync -av webmail/backend/ \
  root@build-host:/opt/ncn-mail-build/

# 2. build on the build host
ssh root@build-host "cd /opt/ncn-mail-build && go mod tidy && go build -o /tmp/ncn-mail ."

# 3. ship binary + systemd unit to pop-03 via the fleet key
rsync -av webmail/deploy/ncn-mail.service root@build-host:/tmp/
ssh root@build-host "
  scp -i /etc/ncn-core-console/fleet-key /tmp/ncn-mail /tmp/ncn-mail.service \
      debian@198.51.100.3:/tmp/ &&
  ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3 '
    sudo install -m 0755 /tmp/ncn-mail /opt/ncn-mail/ncn-mail &&
    sudo install -m 0644 /tmp/ncn-mail.service /etc/systemd/system/ &&
    sudo systemctl daemon-reload &&
    sudo systemctl restart ncn-mail
  '
"
```

### 前端（Vue）

```bash
cd webmail
npm run build           # → dist/  (~165 KB raw, ~57 KB gzip)

tar czf /tmp/wm-dist.tar.gz -C dist .
rsync /tmp/wm-dist.tar.gz root@build-host:/tmp/
ssh root@build-host "
  scp -i /etc/ncn-core-console/fleet-key /tmp/wm-dist.tar.gz \
      debian@198.51.100.3:/tmp/ &&
  ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3 '
    sudo rm -rf /var/www/webmail/* &&
    sudo tar xzf /tmp/wm-dist.tar.gz -C /var/www/webmail/ &&
    sudo chown -R www-data:www-data /var/www/webmail &&
    sudo systemctl reload nginx
  '
"
```

### 首次安装

```bash
# nginx vhost
sudo cp deploy/nginx-mail.conf /etc/nginx/sites-available/mail-example
sudo ln -sf /etc/nginx/sites-available/mail-example /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx

# state dir for ncn-mail
sudo mkdir -p /etc/ncn-mail && sudo chmod 700 /etc/ncn-mail
# session.key auto-generates on first run

# /var/www/webmail
sudo mkdir -p /var/www/webmail
sudo chown www-data:www-data /var/www/webmail
```

## 安全模型

- `ncn_mail_session` cookie：使用 HMAC-SHA256 签名，TTL 为 8 小时，
  HttpOnly + Secure + SameSite=Lax。密钥经 HKDF（域 `ncn.mail.session.v1`）
  从 `/etc/ncn-mail/session.key` 派生。
- 暂存的邮箱密码：AES-256-GCM，密钥经 HKDF（域 `ncn.mail.creds.v1`）从同一
  主密钥派生（与会话密钥按域分离）。
- 登录：要求对本地 dovecot 成功执行 IMAP LOGIN。该应用没有独立的身份存储——
  持有有效的邮箱密码即为身份证明。
- HTML 邮件渲染：仅在沙箱化的 `<iframe sandbox="">` 中进行。CSP
  `default-src 'self'` 阻断经由 SPA 外壳的其他 XSS 路径。
- ncn-mail systemd 加固：`NoNewPrivileges`、`ProtectSystem=strict`、
  `ProtectHome`、`PrivateTmp`、`RestrictAddressFamilies`、收窄的
  `SystemCallFilter`。

## 尚未实现

- 附件上传与下载（仅显示元数据；负载为占位实现）
- IMAP SEARCH / THREAD
- 除回收站外的跨文件夹移动
- 基于 IDLE 的推送通知（当前仅支持用户触发的刷新）
- HTML 撰写 / 富文本
- 草稿自动保存

每一项均为独立的后续任务。
