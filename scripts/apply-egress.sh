#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Generates and applies the sandbox egress firewall (nftables). Run as root on the
# Docker host after the sandbox network exists. Usage:
#   apply-egress.sh <network-name> [resolver-ip] [llm-cidr...]
# With the provided docker-compose.yml: apply-egress.sh syntaxis-sandbox 172.30.0.2 <your-llm-cidr>
# Uses a local `syntaxis` binary if there is one, otherwise the one inside the Compose service.
set -eu
NET="${1:?network name}"; RESOLVER="${2:--}"; [ $# -ge 2 ] && shift 2 || shift 1
SUBNET=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Subnet}}')
GATEWAY=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Gateway}}')
if command -v syntaxis >/dev/null 2>&1; then
  syntaxis egress-rules "$SUBNET" "$RESOLVER" "$GATEWAY" "$@" | nft -f -
else
  docker compose exec -T syntaxis syntaxis egress-rules "$SUBNET" "$RESOLVER" "$GATEWAY" "$@" | nft -f -
fi
echo "egress rules applied for $NET ($SUBNET)"
