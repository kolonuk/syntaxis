# Licensing

Syntaxis is licensed **GPL-2.0-or-later**: the GNU General Public License, version 2, or (at your option) any later version. Source files carry `SPDX-License-Identifier: GPL-2.0-or-later`.

## Why "or later"

Most Go dependencies (including the Docker client SDK) are Apache-2.0. Apache-2.0 is **incompatible with GPL-2.0-only** but compatible with GPLv3. With "or later", the combined binary can be conveyed under GPLv3 terms, which is what the build does. If you redistribute Syntaxis, you may do so under GPL-2.0-or-later (and, for the combined work with its Apache-2.0 components, GPLv3).

## Third-party licences

[THIRD_PARTY_LICENSES](../THIRD_PARTY_LICENSES) lists the licence of every Go module linked into the binary and every npm package bundled into the frontend. Regenerate with `scripts/third-party.sh` (uses `go-licenses` and `license-checker`).

CI runs `scripts/check-licences.sh` and **fails** on any dependency licence outside this list: Apache-2.0, BSD-2/3-Clause, ISC, MIT, MPL-2.0, 0BSD, Unlicense, CC0-1.0 (plus a few permissive npm-only licences). GPL-2.0-only, AGPL, SSPL and unknown licences fail the build.

One module, `modernc.org/mathutil`, carries the standard BSD-3-Clause text but is not recognised by the classifier; it is excluded from the automated check and verified manually (see the script).

## Rule sets

The secret scanner's rules are original (provider key prefixes and shapes are public facts); no third-party rule file is copied. If you add rules from another project, check its licence first.

## Container images

Base images and the tools in the optional sandbox variants (Trivy, kubectl, Terraform/OpenTofu, cloud CLIs, …) have their own licences; they are installed at image build time and are not part of the Syntaxis binary. Check them if you redistribute the images.
