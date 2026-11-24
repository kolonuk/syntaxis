#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Lets an admin start an update from the admin app. Run as root on the Docker host (the installer
# sets it up as a systemd service). Only the host can pull images and restart the containers, so
# this polls Syntaxis for a request (GET /internal/update on the admin listener, with
# SYNTAXIS_FIREWALL_TOKEN), runs scripts/update.sh, and reports how it went.
#
# The same service builds and removes the example environments the admin chooses (Environments in
# the admin app): scripts/build-templates.sh, one image at a time.
#
# Syntaxis never sends the host a command. The only things accepted are a release stream ("stable",
# "preview" or "edge", "latest" being the old name for stable) or a version number, with an optional
# stream after it, and the name of an example environment that has a Dockerfile in images/templates;
# the command is built here from those, and anything else is ignored.
#
#   update-agent.sh            loop forever
#   update-agent.sh --once     one poll, then exit
#
# For tests: UPDATE_EXTRA_ARGS is added to the update.sh command (for example --dry-run), and
# BUILD_EXTRA_ARGS to the build-templates.sh command.
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

# the protocol header tells the server which request format this service understands (2: a stream may follow the target)
call() { curl -fsS -m 20 -H "Authorization: Bearer $TOKEN" -H "X-Syntaxis-Agent: 2" "$@"; }
# An update restarts Syntaxis, and a rollback does too, so the server is often not listening at the
# moment the result is ready. Keep trying for a couple of minutes instead of losing the result.
send_status() { # <path> <text>
  tries=0
  while [ "$tries" -lt "${REPORT_TRIES:-30}" ]; do
    printf '%s' "$2" | call -X POST --data-binary @- "$URL$1" >/dev/null 2>&1 && return 0
    tries=$((tries + 1)); sleep "${REPORT_WAIT:-4}"
  done
  return 0
}
report() { send_status /internal/update/status "$1"; }

# "<target> [stream]": a version or a stream name, and the stream an install follows from now on
run_update() {
  target=$1 stream=${2:-}
  case "$stream" in ''|stable|preview|edge) ;; *) report "failed refused an unknown stream"; return ;; esac
  case "$target" in
    edge) set -- --edge ;;
    latest) set -- --stream "${stream:-stable}" ;;
    stable|preview) set -- --stream "$target" ;;
    v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*)
      case "$target" in *[!0-9A-Za-z.-]*) report "failed refused a malformed version"; return ;; esac
      if [ -n "$stream" ]; then set -- --release "$target" --stream "$stream"; else set -- --release "$target"; fi ;;
    *) report "failed refused an unknown target"; return ;;
  esac
  report "running starting the update to $target"
  RELOAD=1
  # shellcheck disable=SC2086 # UPDATE_EXTRA_ARGS is a list of flags, for tests
  if out=$(sh "$ROOT/scripts/update.sh" ${UPDATE_EXTRA_ARGS:-} "$@" 2>&1); then
    report "done $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
  else
    report "failed $(printf '%s' "$out" | tail -n 6 | tr '\n' ' ')"
  fi
}

# one environment task: "<build|remove> <name>"
run_env() {
  action=$1 name=$2
  case "$name" in *[!a-z0-9-]*|'') report_env "$action $name failed refused a malformed name"; return ;; esac
  [ -f "$ROOT/images/templates/$name/Dockerfile" ] || { report_env "$action $name failed no such environment"; return; }
  report_env "$action $name running"
  case "$action" in
    build) set -- "$name" ;;
    remove) set -- --remove "$name" ;;
  esac
  # shellcheck disable=SC2086 # BUILD_EXTRA_ARGS is a list of flags, for tests
  if out=$(sh "$ROOT/scripts/build-templates.sh" ${BUILD_EXTRA_ARGS:-} "$@" 2>&1); then
    report_env "$action $name done $(printf '%s' "$out" | tail -n 2 | tr '\n' ' ')"
  else
    report_env "$action $name failed $(printf '%s' "$out" | tail -n 6 | tr '\n' ' ')"
  fi
}
report_env() { send_status /internal/environments/status "$1"; }

while :; do
  if reply=$(call "$URL/internal/update" 2>/dev/null); then
    # the words are split on purpose: a target and an optional stream; run_update checks both
    # shellcheck disable=SC2086
    case "$reply" in
      "request "*) run_update ${reply#request } ;;
    esac
  fi
  if reply=$(call "$URL/internal/environments" 2>/dev/null); then
    case "$reply" in
      "build "*) run_env build "${reply#build }" ;;
      "remove "*) run_env remove "${reply#remove }" ;;
    esac
  fi
  [ "$ONCE" = 1 ] && exit 0
  # an update replaces this script with the new version's: start again from the file so the next
  # request is read by the version that is running (a loop already in memory would keep the old one)
  [ -n "${RELOAD:-}" ] && { RELOAD=""; exec sh "$ROOT/scripts/update-agent.sh"; }
  sleep "$INTERVAL"
done
}
