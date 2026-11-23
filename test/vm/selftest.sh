#!/usr/bin/env bash
# selftest.sh [distro...] - exercise vmctl.sh against real VMs.
#
# For each distro: create, boot, SSH and sudo, OS identity, network isolation
# (including a real listener on the host's loopback), file copy in and out,
# rebuild wiping the guest, stop, destroy. The first distro also checks nat
# mode really differs from isolated mode, and prune keeping a base in use.
# Needs the base images (vmctl.sh fetch) and /dev/kvm. Takes about 3 minutes
# per distro. Exit status is the number of failed checks (capped at 100).
set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
VMCTL=$HERE/vmctl.sh
HOME_DIR=${SYX_VM_HOME:-$HOME/.local/share/syntaxis-vm}
fails=0
workdir=$(mktemp -d)
listener_pid=""

ok() { echo "PASS $*"; }
bad() { echo "FAIL $*"; fails=$((fails + 1)); }
check() { local d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi; }
check_not() { local d=$1; shift; if "$@" >/dev/null 2>&1; then bad "$d"; else ok "$d"; fi; }

vm() { "$VMCTL" "$@"; }
gx() { local n=$1; shift; "$VMCTL" ssh "$n" "$@"; }

expect_for() {
	case $1 in
		debian12) echo 'VERSION_ID="12"' ;;
		debian13) echo 'VERSION_ID="13"' ;;
		ubuntu2404) echo 'VERSION_ID="24.04"' ;;
		rocky9) echo 'ID="rocky"' ;;
		alma9) echo 'ID="almalinux"' ;;
		*) echo 'ID=' ;;
	esac
}

cleanup() {
	[ -n "$listener_pid" ] && kill "$listener_pid" 2>/dev/null
	for d in "$HOME_DIR"/vms/selftest-*/; do
		[ -d "$d" ] && vm destroy "$(basename "$d")" >/dev/null 2>&1
	done
	rm -rf "$workdir"
}
trap cleanup EXIT

# A real service on the host's loopback: reachable from a guest only if the
# guest can get out of its network.
host_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
(cd "$workdir" && exec python3 -m http.server --bind 127.0.0.1 "$host_port" >/dev/null 2>&1) &
listener_pid=$!

reach() { # reach <vm> <host> <port>: can the guest open a TCP connection?
	gx "$1" "timeout 5 bash -c 'echo > /dev/tcp/$2/$3'"
}

test_distro() {
	local distro=$1 name=selftest-$1 first=$2
	echo "== $distro"
	check "$distro: create" vm create "$name" "$distro"
	check "$distro: start (isolated)" vm start "$name"
	check "$distro: ssh runs a command" gx "$name" true
	check "$distro: OS is $distro" gx "$name" "grep -q '$(expect_for "$distro")' /etc/os-release"
	check "$distro: tester has sudo" gx "$name" 'sudo -n true'
	check_not "$distro: isolated: no outbound TCP to the internet" reach "$name" 1.1.1.1 443
	check_not "$distro: isolated: no route to a host loopback service" reach "$name" 10.0.2.2 "$host_port"
	check_not "$distro: isolated: DNS does not resolve" gx "$name" 'getent hosts example.com'

	head -c 1048576 /dev/urandom >"$workdir/blob"
	check "$distro: put" vm put "$name" "$workdir/blob" /home/tester/blob
	check "$distro: get" vm get "$name" /home/tester/blob "$workdir/blob.back"
	check "$distro: copy round trip is identical" cmp "$workdir/blob" "$workdir/blob.back"

	gx "$name" 'echo marker > /home/tester/marker; sudo touch /etc/selftest-marker' >/dev/null 2>&1
	check "$distro: rebuild" vm rebuild "$name"
	check_not "$distro: rebuild wiped home files" gx "$name" 'test -e /home/tester/marker'
	check_not "$distro: rebuild wiped system files" gx "$name" 'test -e /etc/selftest-marker'
	check_not "$distro: rebuild is still isolated" reach "$name" 10.0.2.2 "$host_port"

	if [ "$first" = 1 ]; then
		check "$distro: stop" vm stop "$name"
		check "$distro: start nat" vm start "$name" --net=nat
		check "$distro: nat: outbound TCP works" reach "$name" 1.1.1.1 443
		check "$distro: nat: host loopback IS reachable (why escape tests avoid nat)" reach "$name" 10.0.2.2 "$host_port"
		check "$distro: stop after nat" vm stop "$name"
		check "$distro: restart isolated" vm start "$name"
		check_not "$distro: back to isolated after nat" reach "$name" 10.0.2.2 "$host_port"

		local base
		base=$(readlink -f "$HOME_DIR/images/$distro/current.qcow2")
		check "$distro: prune runs with a VM running" vm prune
		check "$distro: prune kept the base image in use" test -f "$base"
	fi

	check "$distro: stop" vm stop "$name"
	check "$distro: list shows it stopped" bash -c "'$VMCTL' list | grep -E '^ +$name +.*stopped'"
	check "$distro: destroy" vm destroy "$name"
	check_not "$distro: destroy removed the VM directory" test -e "$HOME_DIR/vms/$name"
}

distros=("$@")
if [ "${#distros[@]}" -eq 0 ]; then
	mapfile -t distros < <(awk -F'|' '$0 !~ /^#/ && NF >= 4 { print $1 }' "$HERE/distros.conf")
fi

first=1
for d in "${distros[@]}"; do
	test_distro "$d" "$first"
	first=0
done

echo
if [ "$fails" -eq 0 ]; then echo "ALL PASSED"; else echo "$fails FAILED"; fi
[ "$fails" -gt 100 ] && fails=100
exit "$fails"
