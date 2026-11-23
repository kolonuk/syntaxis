#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# suite.sh [--keep] [distro...] - install Syntaxis and run its security tests in
# a fresh VM per distro. The default is every distro in distros.conf.
#
# For each distro:
#   1. create a VM, boot it with nat networking (provisioning), copy the working
#      tree in, run scripts/install.sh exactly as a user would (pinned release
#      images; the Docker, gVisor and nftables install is what differs by distro)
#   2. verify the install the way CI does, and re-run the installer
#   3. copy the Go toolchain in and download the modules (still nat)
#   4. security pass A, ISOLATED networking: the sandbox, Docker proxy, egress,
#      safehttp, doctor and policy packages against the real Docker and gVisor
#   5. security pass B, nat networking: only the egress firewall test, which needs
#      real internet to prove LAN, host, metadata and SMTP are blocked while the
#      internet is not. It probes addresses inside the VM and the VM's private range.
# Results go to $SYX_VM_HOME/results/<timestamp>/<distro>/. The VM is destroyed
# when everything passes and kept for inspection when something fails.
#
# Environment: SYX_INSTALL_VERSION (default v0.1.2), SYX_GO_SDK (a Go toolchain
# directory, default ~/sdk/go1.27.4), SYX_SUITE_PARALLEL (default 2), SYX_SUITE_MEM (MB, 4096).
set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=${SYX_REPO:-$(cd "$HERE/../.." && pwd)}
VMCTL=$HERE/vmctl.sh
HOME_DIR=${SYX_VM_HOME:-$HOME/.local/share/syntaxis-vm}
VERSION=${SYX_INSTALL_VERSION:-v0.1.2}
GO_SDK=${SYX_GO_SDK:-$HOME/sdk/go1.27.4}
PARALLEL=${SYX_SUITE_PARALLEL:-2}
MEM=${SYX_SUITE_MEM:-4096}
BLOCK_HOST=192.0.2.10
SEC_PKGS="./internal/sandbox/... ./internal/dockerproxy/... ./internal/egress/... ./internal/safehttp/... ./internal/doctor/... ./internal/policy/..."
KEEP=0
if [ "${1:-}" = --keep ]; then KEEP=1; shift; fi

RESULTS=$HOME_DIR/results/$(date +%Y%m%d-%H%M%S)
mkdir -p "$RESULTS"

[ -d "$GO_SDK/bin" ] || { echo "suite: no Go toolchain at $GO_SDK (set SYX_GO_SDK)" >&2; exit 2; }
GO_NAME=$(basename "$GO_SDK")

# step <steps file> <description> <command...>: run, record PASS or FAIL.
step() {
	local f=$1 d=$2
	shift 2
	if "$@"; then echo "PASS $d" | tee -a "$f"; else echo "FAIL $d" | tee -a "$f"; return 1; fi
}

# settle <vm>: wait until the guest has finished booting, the Syntaxis stack answers and
# the egress rules are loaded, then give gVisor and the bridge a moment. Without this the
# firewall probe and the egress test misjudge a guest that is still starting up: both
# read a slow, failed connection from a sandbox as "reached the host".
settle() {
	"$VMCTL" ssh "$1" 'timeout 180 systemctl is-system-running --wait >/dev/null 2>&1
		i=0
		until { curl -fsS http://127.0.0.1:8080/healthz && sudo nft list table inet syntaxis_sandbox; } >/dev/null 2>&1; do
			i=$((i + 1)); [ "$i" -ge 90 ] && exit 1; sleep 2
		done
		sleep 30'
}

run_distro() {
	local distro=$1 name=suite-$1 dir=$RESULTS/$1
	local steps=$dir/steps.txt gx
	mkdir -p "$dir"
	gx() { "$VMCTL" ssh "$name" "$@"; }
	local goenv="sudo -H env PATH=/opt/$GO_NAME/bin:\$PATH GOTOOLCHAIN=local GOFLAGS=-buildvcs=false"

	{
		echo "== $distro ($(date +%T))"
		step "$steps" "$distro: create VM" "$VMCTL" create "$name" "$distro" 4 "$MEM" 40G || return 1
		step "$steps" "$distro: boot (nat, provisioning)" "$VMCTL" start "$name" --net=nat || return 1
		step "$steps" "$distro: copy the working tree" bash -c "cd '$REPO' && git ls-files -z --cached --others --exclude-standard | tar --null -T - -czf - | '$VMCTL' ssh '$name' 'rm -rf syntaxis; mkdir syntaxis && tar xzf - -C syntaxis'" || return 1

		step "$steps" "$distro: scripts/install.sh --check runs" gx "cd syntaxis && sudo sh scripts/install.sh --check --url http://localhost:8080" >"$dir/install-check.log" 2>&1
		step "$steps" "$distro: scripts/install.sh installs Docker, gVisor, firewall units and starts the stack" \
			bash -c "set -o pipefail; '$VMCTL' ssh '$name' 'cd syntaxis && sudo sh scripts/install.sh --yes --url http://localhost:8080 --admin-email admin@example.com --llm $BLOCK_HOST --version $VERSION' 2>&1 | tee '$dir/install.log' >/dev/null" || {
			echo "install failed; last lines:"; tail -15 "$dir/install.log"; return 1; }

		step "$steps" "$distro: stack settled after the install" settle "$name"
		"$VMCTL" put "$name" "$HERE/guest/verify-install.sh" syntaxis/verify-install.sh >/dev/null
		"$VMCTL" put "$name" "$dir/install.log" syntaxis/install.log >/dev/null
		gx "cd syntaxis && SYX_INSTALL_VERSION=$VERSION sh verify-install.sh install.log $BLOCK_HOST" >"$dir/verify.txt" 2>&1
		grep -E '^(PASS|FAIL) ' "$dir/verify.txt" | sed "s/^\(PASS\|FAIL\) /\1 $distro: /" | tee -a "$steps"

		step "$steps" "$distro: copy the Go toolchain in" bash -c "tar czf - -C '$(dirname "$GO_SDK")' '$GO_NAME' | '$VMCTL' ssh '$name' 'sudo tar xzf - -C /opt'" || return 1
		step "$steps" "$distro: download Go modules" gx "cd syntaxis && $goenv go mod download" || return 1
		gx "sudo docker tag ghcr.io/kolonuk/syntaxis-sandbox:$VERSION syntaxis-sandbox:latest" >/dev/null 2>&1

		# Pass A: no route out of the VM at all.
		step "$steps" "$distro: stop for the isolated pass" "$VMCTL" stop "$name"
		step "$steps" "$distro: boot isolated" "$VMCTL" start "$name" || return 1
		step "$steps" "$distro: stack settled after the isolated boot" settle "$name"
		echo "-- pass A: security tests, isolated ($(date +%T))"
		gx "cd syntaxis && $goenv go test -count=1 -timeout 40m -json $SEC_PKGS > ~/results-a.json 2> ~/results-a.err" >/dev/null 2>&1
		"$VMCTL" get "$name" results-a.json "$dir/results-a.json" >/dev/null 2>&1
		"$VMCTL" get "$name" results-a.err "$dir/results-a.err" >/dev/null 2>&1
		summarise "$dir/results-a.json" "$distro: security pass A (isolated)" "$steps"

		# Pass B: the egress firewall test, which needs the internet.
		step "$steps" "$distro: stop for the egress pass" "$VMCTL" stop "$name"
		step "$steps" "$distro: boot nat" "$VMCTL" start "$name" --net=nat || return 1
		step "$steps" "$distro: stack settled after the nat boot" settle "$name"
		echo "-- pass B: egress firewall test, nat ($(date +%T))"
		gx "cd syntaxis && $goenv SYNTAXIS_IT_EGRESS=1 go test -count=1 -timeout 20m -json -run TestIntegrationEgressBlocksLANHostMetadataSMTP ./internal/sandbox/ > ~/results-b.json 2> ~/results-b.err" >/dev/null 2>&1
		"$VMCTL" get "$name" results-b.json "$dir/results-b.json" >/dev/null 2>&1
		summarise "$dir/results-b.json" "$distro: security pass B (egress firewall)" "$steps"
		echo "== $distro finished ($(date +%T))"
	} 2>&1 | tee "$dir/run.log" >/dev/null

	if grep -q '^FAIL' "$steps"; then
		echo "$distro: FAILED (VM suite-$distro kept; see $dir)"
		return 1
	fi
	if [ "$KEEP" = 0 ]; then "$VMCTL" destroy "$name" >/dev/null 2>&1; fi
	echo "$distro: all passed"
}

# summarise <results.json> <description> <steps file>
summarise() {
	local json=$1 d=$2 f=$3 pass fail skip
	if [ ! -s "$json" ]; then echo "FAIL $d: no results" | tee -a "$f"; return; fi
	pass=$(jq -s '[.[]|select(.Action=="pass" and .Test!=null)]|length' "$json")
	fail=$(jq -s '[.[]|select(.Action=="fail")]|length' "$json")
	skip=$(jq -s '[.[]|select(.Action=="skip" and .Test!=null)]|length' "$json")
	if [ "$fail" -eq 0 ] && [ "$pass" -gt 0 ]; then echo "PASS $d: $pass passed, $skip skipped" | tee -a "$f"
	else
		echo "FAIL $d: $pass passed, $fail failed, $skip skipped" | tee -a "$f"
		jq -r 'select(.Action=="fail" and .Test!=null)|"  failed: \(.Package) \(.Test)"' "$json" | tee -a "$f"
	fi
	{ echo "  not tested ($d):"; "$REPO/scripts/not-tested.sh" "$json" 2>/dev/null | sed 's/^/    /'; } >>"${f%.txt}-skips.txt"
}

distros=("$@")
if [ "${#distros[@]}" -eq 0 ]; then
	mapfile -t distros < <(awk -F'|' '$0 !~ /^#/ && NF >= 4 { print $1 }' "$HERE/distros.conf")
fi

echo "suite: results in $RESULTS"
fails=0
running=0
for d in "${distros[@]}"; do
	run_distro "$d" &
	running=$((running + 1))
	if [ "$running" -ge "$PARALLEL" ]; then
		wait -n || fails=$((fails + 1))
		running=$((running - 1))
	fi
done
while [ "$running" -gt 0 ]; do
	wait -n || fails=$((fails + 1))
	running=$((running - 1))
done

echo
echo "SUMMARY"
for d in "${distros[@]}"; do
	p=$(grep -c '^PASS' "$RESULTS/$d/steps.txt" 2>/dev/null || echo 0)
	f=$(grep -c '^FAIL' "$RESULTS/$d/steps.txt" 2>/dev/null || echo 0)
	printf '  %-12s %3s passed %3s failed\n' "$d" "$p" "$f"
done
echo "suite: $fails distro(s) failed"
exit "$fails"
