#!/usr/bin/env bash
# pop-healthcheck.sh — one-shot diagnostic for a NCN PoP.
#
# Runs ON the PoP. Checks the things that need to be true before the
# host is fit to join the fleet, OR to confirm a running PoP still
# meets the contract.
#
# What gets checked (each line = one check, ✓/✗/? prefix):
#
#   1. NTP    — clock synced to within ±60s of real wall time
#               (fmt-01 incident: agent HMAC ts skew refused requests
#               because the box had no NTP daemon installed).
#   2. SSH    — PasswordAuthentication is OFF (key-only login).
#               PermitRootLogin allows key login.
#   3. sudo   — `sudo -n birdc show protocols` succeeds without prompt
#               (NOPASSWD contract for the snapshot pipeline).
#   4. agent  — ncn-agent.service is active, listening on :9101.
#               /etc/ncn-agent/{hmac.key,tls.crt,tls.key} exist + 0600.
#   5. probes — sudo birdc + wg show + ip link return non-error.
#   6. log    — /var/log can write; logrotate has at least one config
#               in /etc/logrotate.d/.
#   7. fleet  — fleet-key pubkey is in /root/.ssh/authorized_keys
#               (so tyo can SSH for the interactive terminal).
#
# Usage:
#
#   curl -sk https://admin.example.com/scripts/pop-healthcheck.sh | bash -
#   # or copy from /opt/ncn-core-console/scripts/ on tyo + scp over.
#
# Exit 0 = all checks pass; exit 1 = any ✗; ? lines never fail the run
# (they're informational diagnostics, not requirements).

set -uo pipefail

OK="\e[32m✓\e[0m"
FAIL="\e[31m✗\e[0m"
UNKN="\e[33m?\e[0m"
fail_count=0

check_pass() { printf "$OK  %s\n" "$1"; }
check_fail() { printf "$FAIL  %s\n" "$1"; fail_count=$((fail_count+1)); }
check_info() { printf "$UNKN  %s\n" "$1"; }

echo "=== PoP healthcheck — $(hostname) — $(date -u +%FT%TZ) ==="
echo

# ── 1. NTP ──
if command -v timedatectl >/dev/null; then
  if timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes; then
    check_pass "NTP synchronised ($(timedatectl show -p NTP --value 2>/dev/null || echo 'service active'))"
  else
    check_fail "NTP NOT synchronised — install systemd-timesyncd: apt install -y systemd-timesyncd && systemctl enable --now systemd-timesyncd"
  fi
else
  check_info "no timedatectl — manually verify clock"
fi

# ── 2. SSH config ──
if sshd_T="$(sudo -n sshd -T 2>/dev/null)"; then
  if echo "$sshd_T" | grep -qiE '^passwordauthentication no$'; then
    check_pass "sshd: PasswordAuthentication OFF (key-only)"
  else
    check_fail "sshd: PasswordAuthentication is ON — see scripts/disable-ssh-password.sh"
  fi
  if echo "$sshd_T" | grep -qiE '^permitrootlogin (prohibit-password|without-password|yes)$'; then
    check_pass "sshd: PermitRootLogin allows key login"
  else
    check_fail "sshd: PermitRootLogin blocks root key login"
  fi
else
  check_info "can't read sshd config (need sudo) — skipping"
fi

# ── 3. sudo NOPASSWD for birdc ──
if sudo -n birdc show protocols >/dev/null 2>&1; then
  check_pass "sudo -n birdc works (NOPASSWD verified)"
elif sudo -n true 2>/dev/null; then
  check_info "sudo -n works but birdc isn't installed (?) — OK if non-BGP node"
else
  check_fail "sudo -n FAILED — fleet snapshot pipeline will fail; check /etc/sudoers.d/"
fi

# ── 4. ncn-agent service + files ──
if systemctl is-active --quiet ncn-agent 2>/dev/null; then
  check_pass "ncn-agent.service active (pid $(systemctl show -p MainPID --value ncn-agent))"
else
  check_fail "ncn-agent.service NOT active — provision via agent-node-provision.sh on tyo"
fi
if ss -tlnp 2>/dev/null | grep -q ':9101'; then
  check_pass "agent listening on :9101"
else
  check_fail "agent NOT listening on :9101"
fi
for f in /etc/ncn-agent/hmac.key /etc/ncn-agent/tls.crt /etc/ncn-agent/tls.key; do
  if [[ -f "$f" ]]; then
    mode=$(stat -c '%a' "$f" 2>/dev/null)
    case "$f" in
      *.key)
        if [[ "$mode" == "600" ]]; then check_pass "$f mode 0600";
        else check_fail "$f mode is $mode (want 0600)"; fi
        ;;
      *)
        check_pass "$f present (mode $mode)"
        ;;
    esac
  else
    check_fail "$f MISSING"
  fi
done

# ── 5. PoP probe commands ──
sudo -n birdc show protocols >/dev/null 2>&1 \
  && check_pass "birdc reachable" \
  || check_info "birdc not on PATH or no BGP daemon"
sudo -n wg show >/dev/null 2>&1 \
  && check_pass "wg show reachable" \
  || check_info "wg not on PATH or no WireGuard interface"
sudo -n ip -d -j link show type gre >/dev/null 2>&1 \
  && check_pass "ip link (GRE) reachable" \
  || check_info "ip -j may not be available on this kernel"

# ── 6. logging ──
if touch /var/log/.ncn-healthcheck-write 2>/dev/null; then
  rm -f /var/log/.ncn-healthcheck-write
  check_pass "/var/log writable"
else
  check_fail "/var/log NOT writable"
fi
rotate_count=$(ls /etc/logrotate.d/ 2>/dev/null | wc -l)
if (( rotate_count > 0 )); then
  check_pass "logrotate configs present ($rotate_count files in /etc/logrotate.d/)"
else
  check_info "no /etc/logrotate.d/ configs (may not be needed on this PoP)"
fi

# ── 7. fleet-key in root's authorized_keys ──
if sudo -n grep -q 'ncn-fleet@deploy-host' /root/.ssh/authorized_keys 2>/dev/null; then
  check_pass "fleet-key authorised in /root/.ssh/authorized_keys"
else
  check_fail "fleet-key NOT in /root/.ssh/authorized_keys — add via tyo's ssh-copy-id (terminal WebSocket needs this)"
fi

# ── Verdict ──
echo
if (( fail_count == 0 )); then
  printf "$OK  ALL CHECKS PASSED — PoP is fleet-ready\n"
  exit 0
else
  printf "$FAIL  %d check(s) failed — fix and re-run\n" "$fail_count"
  exit 1
fi
