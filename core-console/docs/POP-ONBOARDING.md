# Adding a new PoP to the NCN fleet

This is the runbook to bring a brand-new VPS into the network as a
production PoP. Estimated time: **~45 minutes**, half of which is BGP
session negotiation with the upstream / IX.

The fleet today (2026-05-28): **ctrl-01** (central), **pop-03**, **pop-04**,
**pop-06**, **pop-05**.

---

## 0. Pre-flight on the new VPS

The VPS must already be reachable as `root` via SSH key. If it ships as
a cloud-init image with a different default user (e.g. `debian`), you
MUST migrate to root login first — see the *root SSH migration* section
below.

Run [`scripts/pop-healthcheck.sh`](../scripts/pop-healthcheck.sh) on the
new box. It should fail several checks at this stage (no agent yet) but
must pass:

* **NTP synchronised** — install `systemd-timesyncd` if not running:
  ```
  apt install -y systemd-timesyncd && systemctl enable --now systemd-timesyncd
  ```
  (fmt-01 onboarded without NTP and the agent HMAC ts skew rejected
  every request until this was fixed. Block on this.)

* **sudo NOPASSWD** for `birdc`, `wg`, `ip` — see `/etc/sudoers.d/` on
  any existing PoP for the contract.

* **PasswordAuthentication no** in sshd_config — see *SSH lockdown*
  section.

* **fleet-key in `/root/.ssh/authorized_keys`** — needed for tyo's
  interactive terminal WebSocket (the ONLY remaining SSH out of tyo).

---

## 1. Register the node in code

Add the new PoP to **two** places:

* `core-console/backend/fleet.go` — add to `nodes: []fleetNode{ ... }`:
  ```go
  {ID: "xxx-01", Label: "City, CC", Country: "CC",
      Address: "<public IP>", Lat: 0, Lon: 0, SSHHost: "xxx-01"},
  ```
* `core-console/scripts/agent-node-provision.sh` — add to the `case`
  statement at the top:
  ```bash
  xxx-01) SSH_USER="root"   ; ARCH="amd64" ; SAN_IP="<public IP>" ;;
  ```

Other consumers (Landing.vue facility map, WorldMap.vue popMeta,
monitor.go probe targets) will inherit automatically OR need a one-line
add depending on the change pattern. Check `core-console/src/views/Landing.vue`
and `core-console/src/components/WorldMap.vue` if the PoP should appear
on the public map.

**Commit + deploy backend.** ncn-api now knows about the new ID; it
will try to scrape it on the next 15s tick and fail with "no agent HMAC
key" until step 2.

---

## 2. Provision the agent

On **ctrl-01** (NOT on the new PoP):

```bash
sudo /opt/ncn-core-console/scripts/agent-node-provision.sh xxx-01
```

This script:

1. Mints a fresh 32-byte HMAC key → `/etc/ncn-core-console/agent-keys/xxx-01.key`
2. Mints a 1-year ECDSA-P-256 TLS cert signed by the internal CA at
   `/etc/ncn-core-console/agent-ca/`
3. scp's the binary, cert, key, HMAC key, config, and systemd unit to
   the new PoP (staged via `/tmp/ncn-agent-stage-$$` for non-root login,
   sudo-installed into final locations).
4. Enables + starts `ncn-agent.service`
5. curls `/v1/healthz` from tyo to verify reachability

Output should end with:
```
{"ok":true,"server_ts":..,"uptime_s":1,"version":"phase4b-..."}
✓ xxx-01 provisioned
```

If it fails, debug from the output — the script is short, ~130 lines.

---

## 3. Reload ncn-api to pick up the new HMAC key

```
sudo systemctl reload ncn-api
```

`reload` (not `restart`) sends SIGHUP which calls `fleet.ReloadAgentKeys()`
in-process. No 401 window for other PoPs.

Verify:
```
journalctl -u ncn-api -n 5 | grep 'agent keys loaded'
# fleet: agent keys loaded — 5/5 remote keys present
```

Within 15s of the reload, `/admin/fleet` should show the new PoP
populated with load/mem/CPU/etc.

---

## 4. Run healthcheck on the new PoP

```
ssh root@<new-pop-ip> bash -s < /opt/ncn-core-console/scripts/pop-healthcheck.sh
```

All `✓` lines should pass. `?` lines are informational (e.g. no BGP
daemon installed yet — fine for a non-BGP edge).

If anything is `✗`, fix BEFORE moving on.

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

**Verify key-based login still works BEFORE closing the existing
session** — open a fresh terminal:
```
ssh -o BatchMode=yes -i /etc/ncn-core-console/fleet-key root@<new-pop-ip> whoami
```
Must return `root` without prompting.

---

## 6. Root SSH migration (only if cloud-init forces non-root login)

Some vendor images (Debian cloud-init in particular) ship with
`/root/.ssh/authorized_keys` containing forced-command entries that
redirect root → debian:

```
no-port-forwarding,...command="echo 'Please login as the user \"debian\"...'" ssh-ed25519 ...
```

To migrate to root login (matches the rest of the fleet):

```bash
# from tyo
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
`/root/.ssh/authorized_keys.cloudinit.bak` is kept in case you ever
need the forced-command guards back.

---

## 7. Secrets backup

After step 2 generates the new HMAC key + cert, run:

```
sudo /opt/ncn-core-console/scripts/backup-secrets.sh
```

This bundles `/etc/ncn-core-console/agent-keys/*.key` + the agent CA
into an age-encrypted tarball under `backups/` on tyo. (`backups/` is
gitignored — keep these locally + replicate offsite per your own
policy.)

---

## 8. Optional: BGP peering

If the PoP carries BGP, configure `/etc/bird/bird.conf` on the node
AFTER the agent is up. Sessions are set up node-side; the fleet
snapshot picks up the protocol list automatically on the next tick.
The admin Servers page can generate the mesh + BIRD config in the house
style and (opt-in) auto-apply it via `birdc configure soft`.

---

## Reverse direction — decommissioning a PoP

1. Stop accepting BGP sessions (drain).
2. `systemctl stop ncn-agent && systemctl disable ncn-agent` on the node.
3. Remove the entry from `fleet.go` + `agent-node-provision.sh` map.
4. Delete `/etc/ncn-core-console/agent-keys/<node>.key` on tyo.
5. `sudo systemctl reload ncn-api`.
6. Optional: wipe the VPS / hand back to vendor.

---

## Troubleshooting cheat-sheet

| Symptom | Likely cause | Fix |
|---|---|---|
| `fleet: scrape X FAIL · agent 401: unauthorized` | HMAC key mismatch | `systemctl reload ncn-api` to re-read keys |
| `fleet: scrape X FAIL · context deadline exceeded` | PoP CPU saturated OR network blip | check `pop-healthcheck.sh` on the PoP, look at `cpu-saturated` alert |
| `fleet: scrape X FAIL · agent 400: timestamp out of skew window` | NTP not running on PoP | install systemd-timesyncd on the PoP |
| `agent-cert-expiring` alert at < 30 days | rotation due | re-run `agent-node-provision.sh <node>` + `systemctl reload ncn-api` |
| Agent up but `/v1/healthz` curl from tyo hangs | firewall blocking 9101 inbound from tyo's IP | open firewall: must allow ctrl-01's outbound IP to reach :9101/tcp |
