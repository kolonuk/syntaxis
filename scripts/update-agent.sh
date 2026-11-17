#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Lets an admin start an update from the admin app. Run as root on the Docker host (the installer
# sets it up as a systemd service). Only the host can pull images and restart the containers, so
# this polls Syntaxis for a request (GET /internal/update on the admin listener, with
# SYNTAXIS_FIREWALL_TOKEN), runs scripts/update.sh, and reports how it went.
#
# Syntaxis never sends the host a command. The only things accepted are "latest", "edge" or a
# version number; the command is built here from those, and anything else is ignored.
#
#   update-agent.sh            loop forever
#   update-agent.sh --once     one poll, then exit
#
# For tests: UPDATE_EXTRA_ARGS is added to the update.sh command (for example --dry-run).
#
# One compound command, so the checkout can change underneath it while an update runs.
{
set -eu
cd "$(dirname "$0")/.."
ROOT=$(pwd)
envval() { [ -f .env ] || return 0; sed -n "s/^$1=//p" .env | tail -n 1; }
TOKEN=${SYNTAXIS_FIREWALL_TOKEN:-$(envval SYNTAXIS_FIREWALL_TOKEN)}
ABIND=${SYNTAXIS_ADMIN_BIND:-$(envval SYNTAXIS_ADMIN_BIND)}
case "$ABIND" in ''|0.0.0.0) ABIND=127.0.0.1 ;; esac
APORT=${SYNTAXIS_ADMIN_PORT:-$(envval SYNTAXIS_ADMIN_PORT)}; APORT=${APORT:-8081}
URL=${UPDATE_URL:-http://$ABIND:$APORT}
INTERVAL=${UPDATE_INTERVAL:-10}
ONCE=0; [ "${1:-}" = "--once" ] && ONCE=1
[ -n "$TOKEN" ] || { echo "no SYNTAXIS_FIREWALL_TOKEN in .env" >&2; exit 1; }

call() { curl -fsS -m 20 -H "Authorization: Bearer $TOKEN" "$@"; }
report() { printf '%s' "$1" | call -X POST --data-binary @- "$URL/internal/update/status" >/dev/null 2>&1 || true; }

run_update() {
  target=$1
  case "$target" in
    edge) set -- --edge ;;
    latest) set -- --release ;;
    v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*)
      case "$target" in *[!0-9A-Za-z.-]*) report "failed refused a malformed version"; return ;; esac
      set -- --release "$target" ;;
    *) report "failed refused an unknown target"; return ;;
  esac
  report "running starting the update to $target"
  # shellcheck disable=SC2086 # UPDATE_EXTRA_ARGS is a list of flags, for tests
  if out=$(sh "$ROOT/scripts/update.sh" ${UPDATE_EXTRA_ARGS:-} "$@" 2>&1); then
    report "done $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
  else
    report "failed $(printf '%s' "$out" | tail -n 6 | tr '\n' ' ')"
  fi
}

while :; do
  if reply=$(call "$URL/internal/update" 2>/dev/null); then
    case "$reply" in
      "request "*) run_update "${reply#request }" ;;
    esac
  fi
  [ "$ONCE" = 1 ] && exit 0
  sleep "$INTERVAL"
done
}
