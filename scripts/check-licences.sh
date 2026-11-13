#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Fails on any dependency licence that cannot be combined into a GPLv3-conveyed
# binary. Syntaxis is GPL-2.0-or-later, so Apache-2.0, MIT, BSD, ISC, MPL-2.0 and
# GPL-2.0-or-later/3.0 dependencies are fine; GPL-2.0-only, AGPL, SSPL and
# proprietary/unknown licences are not.
set -eu
ALLOWED="Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0,0BSD,Unlicense,CC0-1.0,Python-2.0,BlueOak-1.0.0,CC-BY-4.0"
go install github.com/google/go-licenses@latest
# Ignored: our own module (GPL-2.0-or-later) and modernc.org/mathutil, whose
# LICENSE is the standard BSD-3-Clause text but is not recognised by the
# classifier (manually verified; see docs/licensing.md).
"$(go env GOPATH)/bin/go-licenses" check ./cmd/... --allowed_licenses="$ALLOWED" \
  --ignore github.com/kolonuk/syntaxis --ignore modernc.org/mathutil
(cd web && npm ci --no-audit --no-fund >/dev/null && npx --yes license-checker-rseidelsohn --production --onlyAllow "MIT;ISC;BSD-2-Clause;BSD-3-Clause;Apache-2.0;0BSD;BlueOak-1.0.0;CC0-1.0;Python-2.0" --excludePrivatePackages)
echo "licence check passed"
