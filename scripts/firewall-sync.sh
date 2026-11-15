#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Keeps the host firewall in step with Syntaxis while it runs, and forwards the firewall's
# connection log. Run as root on the Docker host (the installer sets it up as a systemd
# service). The rules themselves are applied by apply-egress.sh; this only updates their sets:
#
#   sandbox_none / sandbox_allow / sandbox_open   which running sandbox is in which egress mode
#   allowed_hosts                                 addresses that allowlisted names resolved to
#
# It polls Syntaxis (GET /internal/firewall on the admin listener, with SYNTAXIS_FIREWALL_TOKEN),
# validates every line itself and builds the nft commands locally, so Syntaxis never sends the
# host a command. If Syntaxis cannot be reached the sets are left as they are.
#
#   firewall-sync.sh            loop forever
#   firewall-sync.sh --once     one update, then exit
#
# For tests: FW_STATE_FILE reads the state from a file instead of Syntaxis, NFT_BIN replaces nft.
set -eu
cd "$(dirname "$0")/.."
envval() { [ -f .env ] || return 0; sed -n "s/^$1=//p" .env | tail -n 1; }
TABLE=${FW_TABLE:-syntaxis_sandbox}
NFT=${NFT_BIN:-nft}
TOKEN=${SYNTAXIS_FIREWALL_TOKEN:-$(envval SYNTAXIS_FIREWALL_TOKEN)}
ABIND=${SYNTAXIS_ADMIN_BIND:-$(envval SYNTAXIS_ADMIN_BIND)}
case "$ABIND" in ''|0.0.0.0) ABIND=127.0.0.1 ;; esac
APORT=${SYNTAXIS_ADMIN_PORT:-$(envval SYNTAXIS_ADMIN_PORT)}; APORT=${APORT:-8081}
URL="http://$ABIND:$APORT"
INTERVAL=${FW_INTERVAL:-1}

# Turn state lines into nft commands. Anything that is not exactly a valid line is skipped.
build() {
  awk -v T="$TABLE" '
    function ip4(s,   p, n, i) {
      n = split(s, p, ".")
      if (n != 4) return 0
      for (i = 1; i <= 4; i++) if (p[i] !~ /^[0-9]+$/ || p[i] + 0 > 255) return 0
      return 1
    }
    NF == 0 { next }
    $1 == "sandbox" && NF == 3 && ip4($2) && ($3 == "none" || $3 == "allow" || $3 == "open") { s[$3] = s[$3] (s[$3] == "" ? "" : ", ") $2; next }
    $1 == "host" && NF == 3 && ip4($2) && $3 ~ /^[0-9]+$/ { t = ($3 + 0 > 3600) ? 3600 : $3 + 0; if (t > 0) h = h (h == "" ? "" : ", ") $2 " timeout " t "s"; next }
    { bad++ }
    END {
      split("none allow open", modes, " ")
      for (i = 1; i <= 3; i++) {
        set = "sandbox_" modes[i]
        printf "flush set inet %s %s\n", T, set
        if (s[modes[i]] != "") printf "add element inet %s %s { %s }\n", T, set, s[modes[i]]
      }
      printf "flush set inet %s allowed_hosts\n", T
      if (h != "") printf "add element inet %s allowed_hosts { %s }\n", T, h
      if (bad) printf "firewall-sync: skipped %d invalid line(s)\n", bad > "/dev/stderr"
    }'
}

fetch() {
  if [ -n "${FW_STATE_FILE:-}" ]; then cat "$FW_STATE_FILE"; return; fi
  curl -fsS --max-time 5 -H "Authorization: Bearer $TOKEN" "$URL/internal/firewall"
}

sync_once() {
  state=$(fetch) || { echo "firewall-sync: cannot reach Syntaxis at $URL; leaving the firewall as it is" >&2; return 1; }
  printf '%s\n' "$state" | build | $NFT -f -
}

if [ "${1:-}" = "--once" ]; then sync_once; exit $?; fi

[ -n "$TOKEN" ] || { echo "SYNTAXIS_FIREWALL_TOKEN is not set in .env; nothing to sync" >&2; exit 1; }

# forward the firewall's connection log (kernel log lines tagged syxa:/syxd:)
BUF=$(mktemp)
trap 'kill 0 2>/dev/null; rm -f "$BUF" "$BUF.send"' EXIT INT TERM
{
  if command -v journalctl >/dev/null 2>&1; then journalctl -k -f -n0 -o cat 2>/dev/null; else dmesg --follow 2>/dev/null; fi |
    grep --line-buffered -E 'syx[ad]: ' | while IFS= read -r line; do printf '%s\n' "$line" >> "$BUF"; done
} &

while :; do
  sync_once || true
  if [ -s "$BUF" ]; then
    mv "$BUF" "$BUF.send"
    head -n 400 "$BUF.send" | curl -fsS --max-time 5 -H "Authorization: Bearer $TOKEN" --data-binary @- "$URL/internal/firewall/log" >/dev/null 2>&1 || true
    rm -f "$BUF.send"
  fi
  sleep "$INTERVAL"
done
