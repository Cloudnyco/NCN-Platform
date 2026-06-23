# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities. Instead,
email the maintainers (see the repository owner's profile) or use GitHub's
**private vulnerability reporting** ("Report a vulnerability" under the Security
tab). We aim to acknowledge within a few days.

When reporting, include: affected component, version/commit, reproduction steps,
and impact.

## Running this safely

This platform controls real network infrastructure. If you deploy it:

- **Secrets** live only in `/etc/<service>/` env files and runtime key files —
  never in the repo. Keep `oauth.env`, `tg.env`, `fleet-key`, agent CA/keys,
  `turnstile.secret`, recovery keys, and your `NCN_DATABASE_URL` password out of
  version control (`.gitignore` already excludes `*.env`, `*.key`, `*.pem`, `*.age`).
- **Never commit an encrypted secrets backup** to a repo that is (or may become)
  public — even age-encrypted blobs should be treated as compromised once exposed.
- The API is auth-gated; the public site/looking-glass paths are explicitly
  separated from `/admin/*` (host separation in nginx + the router).
- Production-touching actions (config rollback, mesh apply, DDoS mitigation,
  failover) are **confirm-gated, audited, and reversible** by design — keep them so.
- Per-PoP agents use mTLS-pinned HTTPS + HMAC; rotate keys with the helpers in
  `scripts/` if exposure is suspected.

## Sensitive-by-design modules

`core-console/backend/{auth*,oauth,passkey,recover_bootstrap,audit}.go`,
`lb/failover.sh`, `scripts/{backup,restore}-secrets.sh`, and anything under
`scripts/pitr/` handle credentials or destructive operations — review changes
there with extra care.
