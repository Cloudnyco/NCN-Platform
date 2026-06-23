# NCN Webmail

Standalone webmail for `mail.example.com`. **Frontend + backend both live on
pop-03** alongside the mail server. No dependency on `core-console` or
`deploy-host`.

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

## Layout

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
│   └── nginx-mail-acme-cloud.conf
├── package.json             frontend
├── vite.config.ts
└── README.md
```

## Develop

```bash
# frontend
cd /root/ncn-workspace/webmail
npm install
npm run dev          # http://localhost:5174, /api/* proxies to prod

# backend (if you have Go locally; otherwise build on tyo as per deploy)
cd backend
go run .             # listens on 127.0.0.1:9000
```

## Deploy

We don't have a direct SSH key to pop-03's `debian` user from this
workstation; routing through `deploy-host` (which holds the fleet key) is
the canonical path.

### Backend (Go)

```bash
# 1. sync source to tyo (where Go is installed)
rsync -av /root/ncn-workspace/webmail/backend/ \
  root@deploy-host:/opt/ncn-mail-build/

# 2. build on tyo
ssh root@deploy-host "cd /opt/ncn-mail-build && go mod tidy && go build -o /tmp/ncn-mail ."

# 3. ship binary + systemd unit to pop-03 via fleet-key
rsync -av /root/ncn-workspace/webmail/deploy/ncn-mail.service root@deploy-host:/tmp/
ssh root@deploy-host "
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

### Frontend (Vue)

```bash
cd /root/ncn-workspace/webmail
npm run build           # → dist/  (~165 KB raw, ~57 KB gzip)

tar czf /tmp/wm-dist.tar.gz -C dist .
rsync /tmp/wm-dist.tar.gz root@deploy-host:/tmp/
ssh root@deploy-host "
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

### First-time setup (already done on prod)

```bash
# nginx vhost
sudo cp deploy/nginx-mail-acme-cloud.conf /etc/nginx/sites-available/mail-acme-cloud
sudo ln -sf /etc/nginx/sites-available/mail-acme-cloud /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx

# state dir for ncn-mail
sudo mkdir -p /etc/ncn-mail && sudo chmod 700 /etc/ncn-mail
# session.key auto-generates on first run

# /var/www/webmail
sudo mkdir -p /var/www/webmail
sudo chown www-data:www-data /var/www/webmail
```

## Security model

- `ncn_mail_session` cookie: HMAC-SHA256 signed, 8h TTL, HttpOnly + Secure +
  SameSite=Lax. Key derived from `/etc/ncn-mail/session.key` via HKDF with
  domain `ncn.mail.session.v1`.
- Stashed mailbox passwords: AES-256-GCM, key from same master via HKDF
  domain `ncn.mail.creds.v1` (domain-separated from session key).
- Login: requires a successful IMAP LOGIN to the local dovecot. The
  application has no separate identity store — possession of a valid
  mailbox password IS proof of identity.
- HTML mail render: sandboxed `<iframe sandbox="">` only. CSP `default-src 'self'` blocks any other XSS path through the SPA shell.
- ncn-mail systemd hardening: `NoNewPrivileges`, `ProtectSystem=strict`,
  `ProtectHome`, `PrivateTmp`, `RestrictAddressFamilies`, narrow
  `SystemCallFilter`.

## What's NOT in here yet
- Attachment upload + download (metadata is shown; payloads are stubs)
- IMAP SEARCH / THREAD
- Move-between-folders beyond Trash
- IDLE-driven push notifications (currently user-triggered refresh only)
- HTML compose / rich text
- Drafts autosave

Each is a self-contained next ticket.
