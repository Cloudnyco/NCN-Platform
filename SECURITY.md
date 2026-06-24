# Security Policy

> **English** · [简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Do not open a public issue for security vulnerabilities. Report them by email to
the maintainers (see the repository owner's profile) or through GitHub's
private vulnerability reporting ("Report a vulnerability" under the Security
tab). Acknowledgement is typically sent within a few days.

Reports should include: the affected component, version or commit, reproduction
steps, and impact.

## Operational guidance

This platform controls network infrastructure. The following practices apply to
any deployment:

- Secrets reside only in `/etc/<service>/` env files and runtime key files, never
  in the repository. Keep `oauth.env`, `tg.env`, `fleet-key`, agent CA/keys,
  `turnstile.secret`, recovery keys, and the `NCN_DATABASE_URL` password out of
  version control (`.gitignore` already excludes `*.env`, `*.key`, `*.pem`, `*.age`).
- Encrypted secrets backups must not be committed to a repository that is, or may
  become, public. An age-encrypted blob should be treated as compromised once exposed.
- The API is authentication-gated. The public site and looking-glass paths are
  separated from `/admin/*` (host separation in nginx and the router).
- Production-touching actions (config rollback, mesh apply, DDoS mitigation,
  failover) are confirm-gated, audited, and reversible by design; this property
  should be preserved.
- Per-PoP agents use mTLS-pinned HTTPS with HMAC. Rotate keys with the helpers in
  `scripts/` if exposure is suspected.

## Sensitive-by-design modules

The following handle credentials or destructive operations and warrant additional
review for any change:

- `core-console/backend/{auth*,oauth,passkey,recover_bootstrap,audit}.go`
- `lb/failover.sh`
- `scripts/{backup,restore}-secrets.sh`
- anything under `scripts/pitr/`
