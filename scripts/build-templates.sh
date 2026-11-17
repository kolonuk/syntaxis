#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Builds the example environment images in images/templates on this Docker host, as
# syntaxis-env-<name>:1, on top of the sandbox image. Then approve the ones you want in the admin
# app (Environments). Run from the checkout.
#
#   sh scripts/build-templates.sh              build them all
#   sh scripts/build-templates.sh rust java    build only these
#   BASE=ghcr.io/kolonuk/syntaxis-sandbox:v1.0.0 sh scripts/build-templates.sh   build on another base
set -eu
cd "$(dirname "$0")/.."
BASE=${BASE:-syntaxis-sandbox:latest}
if [ $# -eq 0 ]; then
  set -- $(cd images/templates && ls)
fi
for t in "$@"; do
  [ -f "images/templates/$t/Dockerfile" ] || { echo "no template named $t" >&2; exit 1; }
  echo "==> $t"
  docker build -t "syntaxis-env-$t:1" --build-arg "BASE=$BASE" "images/templates/$t"
done
echo "Built: $*. Approve them under Environments in the admin app (image syntaxis-env-<name>:1)."
