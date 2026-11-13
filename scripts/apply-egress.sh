#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Generates and applies the sandbox egress firewall (nftables). Run as root on
# the Docker host after the sandbox network exists. Usage:
#   apply-egress.sh <network-name> [resolver-ip] [llm-cidr...]
set -eu
NET="${1:?network name}"; RESOLVER="${2:--}"; [ $# -ge 2 ] && shift 2 || shift 1
SUBNET=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Subnet}}')
GATEWAY=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Gateway}}')
syntaxis egress-rules "$SUBNET" "$RESOLVER" "$GATEWAY" "$@" | nft -f -
echo "egress rules applied for $NET ($SUBNET)"
