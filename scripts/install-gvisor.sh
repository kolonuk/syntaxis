#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Installs gVisor (runsc) from the official apt repository and registers it as a
# Docker runtime. Run as root on the Docker host. Review before running.
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
apt-get update
apt-get install -y apt-transport-https ca-certificates curl gnupg
curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" > /etc/apt/sources.list.d/gvisor.list
apt-get update && apt-get install -y runsc
runsc install                 # adds the "runsc" runtime to /etc/docker/daemon.json
systemctl restart docker
docker run --rm --runtime=runsc hello-world >/dev/null && echo "gVisor (runsc) works with Docker"
