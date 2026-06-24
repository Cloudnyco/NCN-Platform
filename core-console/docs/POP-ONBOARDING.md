# Adding a new PoP to the fleet

> **English** · [简体中文](POP-ONBOARDING.zh-CN.md)

This runbook describes the procedure for bringing a new VPS into the
network as a production PoP. Typical elapsed time is around 45 minutes,
a significant portion of which is BGP session negotiation with the
upstream or IX.

A fleet typically consists of one central control node (e.g. `ctrl-01`)
and a number of PoP nodes (e.g. `pop-01`, `pop-02`, ...). The exact
roster and role assignments vary per deployment.

---

## 0. Pre-flight on the new VPS

The VPS must already be reachable as `root` via SSH key. If it ships as
a cloud-init image with a different default user (e.g. `debian`),
migration to root login is required first — see the *root SSH migration*
section below.

Run [`scripts/pop-healthcheck.sh`](../scripts/pop-healthcheck.sh) on the
new host. Several checks are expected to fail at this stage (no agent
yet), but the following must pass:

* **NTP synchronised** — install `systemd-timesyncd` if not running:
  ```
  apt install -y systemd-timesyncd && systemctl enable --now systemd-timesyncd
  ```
  (A node onboarded without NTP will have the agent HMAC timestamp skew
  reject every request until this is fixed. This is a blocking
  requirement.)

* **sudo NOPASSWD** for `birdc`, `wg`, `ip` — refer to `/etc/sudoers.d/`
  on any existing PoP for the expected contract.

* **PasswordAuthentication no** in sshd_config — see the *SSH lockdown*
  section.

* **fleet-key in `/root/.ssh/authorized_keys`** — required for the
  control node's interactive terminal WebSocket (the only remaining SSH
  out of the control node).

---

## 1. Register the node in code

The new PoP must be added in **two** places:

* `core-console/backend/fleet.go` — add to `nodes: []fleetNode{ ... }`:
  ```go
  {ID: "pop-0N", Label: "City, CC", Country: "CC",
      Address: "<public IP>", Lat: 0, Lon: 0, SSHHost: "pop-0N"},
  ```
* `core-console/scripts/agent-node-provision.sh` — add to the `case`
  statement at the top:
  ```bash
  pop-0N) SSH_USER="root"   ; ARCH="amd64" ; SAN_IP="<public IP>" ;;
  ```

Other consumers (Landing.vue facility map, WorldMap.vue popMeta,
monitor.go probe targets) either inherit the entry automatically or
require a one-line addition depending on the change pattern. Check
`core-console/src/views/Landing.vue` and
`core-console/src/components/WorldMap.vue` if the PoP should appear on
the public map.

Commit and deploy the backend. ncn-api now knows about the new ID and
will attempt to scrape it on the next 15s tick, failing with "no agent
HMAC key" until step 2 completes.

---

## 2. Provision the agent

On the control node (NOT on the new PoP):

```bash
sudo /opt/ncn-core-console/scripts/agent-node-provision.sh pop-0N
```

This script:

1. Mints a fresh 32-byte HMAC key → `/etc/ncn-core-console/agent-keys/pop-0N.key`
2. Mints a 1-year ECDSA-P-256 TLS cert signed by the internal CA at
   `/etc/ncn-core-console/agent-ca/`
3. scp's the binary, cert, key, HMAC key, config, and systemd unit to
   the new PoP (staged via `/tmp/ncn-agent-stage-$$` for non-root login,
   sudo-installed into final locations).
4. Enables and starts `ncn-agent.service`
5. curls `/v1/healthz` from the control node to verify reachability

Output should end with:
```
{"ok":true,"server_ts":..,"uptime_s":1,"version":"phase4b-..."}
✓ pop-0N provisioned
```

On failure, debug from the script output; the script is short
(~130 lines).

---

## 3. Reload ncn-api to pick up the new HMAC key

```
sudo systemctl reload ncn-api
```

`reload` (not `restart`) sends SIGHUP, which calls
`fleet.ReloadAgentKeys()` in-process. There is no 401 window for other
PoPs.

Verify:
```
journalctl -u ncn-api -n 5 | grep 'agent keys loaded'
# fleet: agent keys loaded — N/N remote keys present
```

Within 15s of the reload, `/admin/fleet` should show the new PoP
populated with load/mem/CPU/etc.

---

## 4. Run healthcheck on the new PoP

```
ssh root@<new-pop-ip> bash -s < /opt/ncn-core-console/scripts/pop-healthcheck.sh
```

All `✓` lines should pass. `?` lines are informational (e.g. no BGP
daemon installed yet, which is acceptable for a non-BGP edge).

Any `✗` line must be fixed before proceeding.

---

## 5. SSH lockdown (mandatory before exposing the PoP)

Ensure `/etc/ssh/sshd_config` (or `/etc/ssh/sshd_config.d/00-ncn.conf`)
contains:

```
PasswordAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin prohibit-password
KbdInteractiveAuthentication no
PubkeyAuthentication yes
```

Reload sshd:
```
sshd -t && systemctl reload sshd
```

Key-based login must be verified to still work BEFORE the existing
session is closed. Open a fresh terminal:
```
ssh -o BatchMode=yes -i /etc/ncn-core-console/fleet-key root@<new-pop-ip> whoami
```
This must return `root` without prompting.

---

## 6. Root SSH migration (only if cloud-init forces non-root login)

Some vendor images (Debian cloud-init in particular) ship with
`/root/.ssh/authorized_keys` containing forced-command entries that
redirect root to a non-root user:

```
no-port-forwarding,...command="echo 'Please login as the user \"debian\"...'" ssh-ed25519 ...
```

To migrate to root login (matching the rest of the fleet):

```bash
# from the control node
ssh debian@<new-pop-ip> bash <<'EOF'
sudo cp -a /root/.ssh/authorized_keys /root/.ssh/authorized_keys.cloudinit.bak
sudo cp -a /home/debian/.ssh/authorized_keys /root/.ssh/authorized_keys
sudo chown root:root /root/.ssh/authorized_keys
sudo chmod 600 /root/.ssh/authorized_keys
EOF

# verify
ssh -i /etc/ncn-core-console/fleet-key root@<new-pop-ip> whoami
```

If both succeed, the node is ready. The backup at
`/root/.ssh/authorized_keys.cloudinit.bak` is retained in case the
forced-command guards need to be restored.

---

## 7. Secrets backup

After step 2 generates the new HMAC key and cert, run:

```
sudo /opt/ncn-core-console/scripts/backup-secrets.sh
```

This bundles `/etc/ncn-core-console/agent-keys/*.key` and the agent CA
into an age-encrypted tarball under `backups/` on the control node.
(`backups/` is gitignored — these should be kept locally and replicated
offsite per the operator's own policy.)

---

## 8. Optional: BGP peering

If the PoP carries BGP, configure `/etc/bird/bird.conf` on the node
after the agent is up. Sessions are configured node-side; the fleet
snapshot picks up the protocol list automatically on the next tick. The
admin Servers page can generate the mesh and BIRD config in the standard
style and, opt-in, auto-apply it via `birdc configure soft`.

---

## Reverse direction — decommissioning a PoP

1. Stop accepting BGP sessions (drain).
2. `systemctl stop ncn-agent && systemctl disable ncn-agent` on the node.
3. Remove the entry from `fleet.go` and the `agent-node-provision.sh` map.
4. Delete `/etc/ncn-core-console/agent-keys/<node>.key` on the control node.
5. `sudo systemctl reload ncn-api`.
6. Optional: wipe the VPS or return it to the vendor.

---

## Troubleshooting cheat-sheet

| Symptom | Likely cause | Fix |
|---|---|---|
| `fleet: scrape X FAIL · agent 401: unauthorized` | HMAC key mismatch | `systemctl reload ncn-api` to re-read keys |
| `fleet: scrape X FAIL · context deadline exceeded` | PoP CPU saturated or network blip | check `pop-healthcheck.sh` on the PoP, look at the `cpu-saturated` alert |
| `fleet: scrape X FAIL · agent 400: timestamp out of skew window` | NTP not running on PoP | install systemd-timesyncd on the PoP |
| `agent-cert-expiring` alert at < 30 days | rotation due | re-run `agent-node-provision.sh <node>` and `systemctl reload ncn-api` |
| Agent up but `/v1/healthz` curl from the control node hangs | firewall blocking 9101 inbound from the control node's IP | open firewall: must allow the control node's outbound IP to reach :9101/tcp |
