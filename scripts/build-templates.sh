#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Builds the example environment images in images/templates on this Docker host, as
# syntaxis-env-<name>:1, on top of the sandbox image. Run from the checkout. The admin app can also
# do this for you (Environments), through the host's update service.
#
#   sh scripts/build-templates.sh              build them all (large: several gigabytes, a while)
#   sh scripts/build-templates.sh rust java    build only these
#   sh scripts/build-templates.sh --remove rust   remove a built one
#   sh scripts/build-templates.sh --dry-run rust  say what would be done
#   BASE=ghcr.io/kolonuk/syntaxis-sandbox:v1.0.0 sh scripts/build-templates.sh   build on another base
#
# Then approve the ones you want under Environments in the admin app (image syntaxis-env-<name>:1).
set -eu
cd "$(dirname "$0")/.."
envval() { [ -f .env ] || return 0; sed -n "s/^$1=//p" .env | tail -n 1; }
BASE=${BASE:-$(envval SYNTAXIS_SANDBOX_IMAGE)}
BASE=${BASE:-syntaxis-sandbox:latest}
MODE=build DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --remove) MODE=remove; shift ;;
    --dry-run) DRY=1; shift ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
if [ $# -eq 0 ]; then
  [ "$MODE" = build ] || { echo "say which one to remove" >&2; exit 2; }
  names=$(cd images/templates && ls)
  # shellcheck disable=SC2086
  set -- $names
fi
for t in "$@"; do
  case "$t" in
    *[!a-z0-9-]*|'') echo "bad name: $t" >&2; exit 1 ;;
  esac
  [ -f "images/templates/$t/Dockerfile" ] || { echo "no template named $t" >&2; exit 1; }
  if [ "$MODE" = remove ]; then
    echo "==> removing syntaxis-env-$t:1"
    [ "$DRY" = 1 ] || docker rmi "syntaxis-env-$t:1"
    continue
  fi
  echo "==> $t (on $BASE)"
  [ "$DRY" = 1 ] || docker build -t "syntaxis-env-$t:1" --build-arg "BASE=$BASE" "images/templates/$t"
done
[ "$MODE" = build ] && [ "$DRY" = 0 ] && echo "Built: $*. Approve them under Environments in the admin app (image syntaxis-env-<name>:1)."
exit 0
