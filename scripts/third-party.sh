#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Regenerates THIRD_PARTY_LICENSES (Go modules via go-licenses, npm via license-checker).
set -eu
go install github.com/google/go-licenses@latest
out=THIRD_PARTY_LICENSES
{
  echo "Third-party licences used by Syntaxis"
  echo "====================================="
  echo
  echo "## Go modules (linked into the binary)"
  "$(go env GOPATH)/bin/go-licenses" report ./cmd/... --template scripts/licences.tpl --ignore github.com/kolonuk/syntaxis 2>/dev/null
  echo "- modernc.org/mathutil: BSD-3-Clause (verified manually) https://gitlab.com/cznic/mathutil/-/blob/master/LICENSE"
  echo
  echo "## npm packages (bundled into the frontend)"
  (cd web && npx --yes license-checker-rseidelsohn --production --excludePrivatePackages --summary)
} > "$out"
echo "wrote $out"
