#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# all.sh - run every VM test in one go and print one summary.
#   ./vm.sh all.sh                 the whole set
#   ./vm.sh all.sh --quick         skip the upgrade tests
#
# Order: self-test (all Linux distributions, plus Windows 11 from the saved template when there is
# one), install and security suite (all distributions), upgrade test with rollback. These need
# KVM and a developer's machine, so CI does not run them.
#
# Environment: SYX_INSTALL_VERSION (a published tag for the suite; default builds from this
# checkout), SYX_UPGRADE_DISTROS (default "debian12 rocky9"), SYX_UPGRADE_FROM, SYX_UPGRADE_TO.
set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
HOME_DIR=${SYX_VM_HOME:-$HOME/.local/share/syntaxis-vm}
quick=0
[ "${1:-}" = --quick ] && quick=1

names=()
codes=()
run() { # run <label> <command...>
	local label=$1 rc
	shift
	echo
	echo "######## $label ($(date +%T))"
	"$@"
	rc=$?
	names+=("$label")
	codes+=("$rc")
}

distros=$(awk -F'|' '$0 !~ /^#/ && NF >= 4 { printf "%s ", $1 }' "$HERE/distros.conf")

# shellcheck disable=SC2086
run "self-test: Linux distributions" "$HERE/selftest.sh" $distros
if [ -f "$HOME_DIR/images/windows11/template.qcow2" ]; then
	run "self-test: Windows 11 (saved template)" "$HERE/selftest.sh" windows11
else
	echo "Windows 11: skipped (no saved template; see README)"
fi
# shellcheck disable=SC2086
run "install and security suite: all distributions" "$HERE/suite.sh" $distros

if [ "$quick" -eq 0 ]; then
	for d in ${SYX_UPGRADE_DISTROS:-debian12 rocky9}; do
		run "upgrade and rollback on $d" "$HERE/upgrade-test.sh" "$d"
	done
fi

echo
echo "======== SUMMARY"
fail=0
for i in "${!names[@]}"; do
	if [ "${codes[$i]}" -eq 0 ]; then echo "PASS  ${names[$i]}"; else echo "FAIL  ${names[$i]} (exit ${codes[$i]})"; fail=1; fi
done
exit "$fail"
