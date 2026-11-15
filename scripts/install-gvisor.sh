#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Installs gVisor (runsc) and registers it as a Docker runtime, on x86-64 or arm64. Run as root on
# the Docker host. Review before running.
#
# Debian, Ubuntu and Raspberry Pi OS use gVisor's signed apt repository. Other distributions
# (AlmaLinux, Rocky Linux, RHEL, Fedora, ...) take the same release package from the same bucket:
# it is downloaded over HTTPS, checked against the SHA-256 in the repository index, and the
# runsc binary is unpacked from it.
#
#   GVISOR_PREFIX=/usr/local/bin   where the binary goes in the non-apt case
#   GVISOR_FORCE_BINARY=1          use the package download even on a host with apt
#   GVISOR_SKIP_REGISTER=1         install only; do not touch Docker's configuration or restart it
set -eu
[ "$(id -u)" = 0 ] || [ -n "${GVISOR_PREFIX:-}" ] || { echo "run as root" >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "gVisor has no build for $(uname -m)" >&2; exit 1 ;;
esac
BUCKET=https://storage.googleapis.com/gvisor/releases

if command -v apt-get >/dev/null 2>&1 && [ "${GVISOR_FORCE_BINARY:-}" != 1 ]; then
  apt-get update
  apt-get install -y apt-transport-https ca-certificates curl gnupg
  curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
  echo "deb [arch=$ARCH signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] $BUCKET release main" > /etc/apt/sources.list.d/gvisor.list
  apt-get update && apt-get install -y runsc
  RUNSC=runsc
else
  command -v curl >/dev/null 2>&1 || { echo "curl is needed" >&2; exit 1; }
  if ! command -v ar >/dev/null 2>&1; then
    # ar (binutils) unpacks the .deb
    if command -v dnf >/dev/null 2>&1; then dnf install -y -q binutils
    elif command -v yum >/dev/null 2>&1; then yum install -y -q binutils
    elif command -v apt-get >/dev/null 2>&1; then apt-get update -qq && apt-get install -y -qq binutils
    else echo "ar (binutils) is needed" >&2; exit 1; fi
  fi
  PREFIX=${GVISOR_PREFIX:-/usr/local/bin}
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cd "$tmp"
  curl -fsSL "$BUCKET/dists/release/main/binary-$ARCH/Packages" -o Packages
  # the index lists Filename and SHA256 for the runsc package
  FILE=$(awk '/^Package: runsc$/{f=1} f&&/^Filename:/{print $2; exit}' Packages)
  SUM=$(awk '/^Package: runsc$/{f=1} f&&/^SHA256:/{print $2; exit}' Packages)
  [ -n "$FILE" ] && [ -n "$SUM" ] || { echo "could not find the runsc package in the gVisor index" >&2; exit 1; }
  curl -fsSL "$BUCKET/$FILE" -o runsc.deb
  echo "$SUM  runsc.deb" | sha256sum -c -
  DATA=$(ar t runsc.deb | grep '^data\.tar' | head -n 1)
  mkdir x
  case "$DATA" in
    *.xz) ar p runsc.deb "$DATA" | tar -xJ -C x ;;
    *.gz) ar p runsc.deb "$DATA" | tar -xz -C x ;;
    *.zst) ar p runsc.deb "$DATA" | zstd -dc | tar -x -C x ;;
    *) ar p runsc.deb "$DATA" | tar -x -C x ;;
  esac
  BIN=$(find x -type f -name runsc | head -n 1)
  [ -n "$BIN" ] || { echo "runsc not found in the package" >&2; exit 1; }
  mkdir -p "$PREFIX"
  install -m 0755 "$BIN" "$PREFIX/runsc"
  RUNSC="$PREFIX/runsc"
fi
echo "installed $($RUNSC --version | head -n 1)"
[ "${GVISOR_SKIP_REGISTER:-}" = 1 ] && exit 0
$RUNSC install      # adds the "runsc" runtime to /etc/docker/daemon.json
systemctl restart docker
docker run --rm --runtime=runsc hello-world >/dev/null && echo "gVisor (runsc) works with Docker"
