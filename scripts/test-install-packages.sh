#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Run INSIDE a distribution container with the repository mounted at /repo (read-only), for example:
#   docker run --rm -v "$PWD":/repo:ro almalinux:9 sh /repo/scripts/test-install-packages.sh
# Checks that the installer recognises the system and that Docker (with Compose), nftables and gVisor
# can be installed from their repositories there. systemd is not running in a container, so
# service start-up is stubbed out.
set -eu
printf '#!/bin/sh\nexit 0\n' > /usr/local/bin/systemctl; chmod +x /usr/local/bin/systemctl
# shellcheck disable=SC1091
. /etc/os-release
echo "== $PRETTY_NAME $(uname -m)"
sh /repo/scripts/install.sh --check
cd /repo
if sh -c '. ./scripts/os.sh; pkg_install nftables && install_docker' >/tmp/docker-install.log 2>&1; then
  echo "docker install: OK ($(docker --version 2>&1 | head -1); $(docker compose version 2>&1 | head -1))"
else
  echo "docker install: FAILED"; tail -12 /tmp/docker-install.log; exit 1
fi
GVISOR_FORCE_BINARY=1 GVISOR_PREFIX=/opt/gv GVISOR_SKIP_REGISTER=1 sh /repo/scripts/install-gvisor.sh
/opt/gv/runsc --version | head -n 1
nft --version
