#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Lists what a `go test -json` run could not test, and why. Tests that need something real that is
# not available (an account, a model server, gVisor) skip with "NOT TESTED: <reason>" rather than
# pretend. Usage: go test -json ./... > results.json; scripts/not-tested.sh results.json
set -eu
[ $# -eq 1 ] || { echo "usage: $0 results.json" >&2; exit 2; }
echo "## Not tested"
echo
jq -r 'select(.Action=="output") | select(.Output|test("NOT TESTED")) |
  "- `\(.Package|sub("^github.com/kolonuk/syntaxis/";""))` \(.Test // ""): \(.Output|sub("^[[:space:]]*[A-Za-z_]+\\.go:[0-9]+: ";"")|sub("^[[:space:]]*NOT TESTED: ";"")|rtrimstr("\n"))"' "$1" | sort -u
