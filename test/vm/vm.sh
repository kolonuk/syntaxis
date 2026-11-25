#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# vm.sh <script> [args...] - run a test VM script inside the harness container.
#   ./vm.sh vmctl.sh doctor
#   ./vm.sh suite.sh debian12
#   ./vm.sh upgrade-test.sh rocky9
#   ./vm.sh selftest.sh
#   ./vm.sh shell          a shell inside the container
#   ./vm.sh build          (re)build the image
#   ./vm.sh down           stop the container (this also stops every VM)
# The host needs Docker and /dev/kvm, nothing else. VM data (images, VM disks, results, keys) is in
# the Docker volume syntaxis-vm-data. Set SYX_VM_DATA to another volume name or an absolute host
# path to keep it elsewhere.
#   SYX_ISO_DIR=<folder> ./vm.sh ...   mount a host folder holding a Windows ISO at /isos (read-only);
#                                      stop the container (./vm.sh down) after changing it
#   ./vm.sh results <dir>          copy the test results out of the volume into <dir>
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
DATA=${SYX_VM_DATA:-syntaxis-vm-data}
IMAGE=${SYX_VM_IMAGE:-syntaxis-vm-harness}
NAME=${SYX_VM_CONTAINER:-syntaxis-vm}

die() { echo "vm.sh: $*" >&2; exit 1; }
command -v docker >/dev/null || die "docker is not installed"
[ -e /dev/kvm ] || die "/dev/kvm not found: enable hardware virtualisation (KVM) on this machine"

build() { docker build -q -t "$IMAGE" "$HERE" >/dev/null; }

running() { [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = true ]; }

start() {
	docker image inspect "$IMAGE" >/dev/null 2>&1 || build
	iso=()
	[ -z "${SYX_ISO_DIR:-}" ] || iso=(-v "$SYX_ISO_DIR:/isos:ro")
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	docker run -d --init --name "$NAME" \
		--device /dev/kvm \
		-v "$REPO:/repo:ro" -v "$DATA:/data" "${iso[@]}" \
		"$IMAGE" sleep infinity >/dev/null
}

case ${1:-} in
	build) build; exit 0 ;;
	down) docker rm -f "$NAME" >/dev/null 2>&1 || true; exit 0 ;;
	"" | -h | --help | help) sed -n '3,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
esac

running || start
env=()
for v in $(compgen -e | grep -E '^SYX_' | grep -vE '^SYX_(VM_HOME|VM_IMAGE|VM_CONTAINER|VM_DATA|ISO_DIR|REPO|GO_SDK)$'); do env+=(-e "$v"); done
tty=()
[ -t 0 ] && [ -t 1 ] && tty=(-t)
case $1 in
	results)
		[ -n "${2:-}" ] || die "usage: vm.sh results <dir>"
		mkdir -p "$2"
		docker cp "$NAME:/data/results/." "$2"
		exit $?
		;;
esac
if [ "$1" = shell ]; then
	exec docker exec -i "${tty[@]}" "${env[@]}" "$NAME" bash
fi
script=$1
shift
exec docker exec -i "${tty[@]}" "${env[@]}" "$NAME" "/repo/test/vm/$script" "$@"
