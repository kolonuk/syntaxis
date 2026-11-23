#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# vmctl.sh - disposable test VMs for Syntaxis escape and destructive tests.
#
# QEMU/KVM runs upstream minimal cloud images. Each test VM is a qcow2 overlay
# on a versioned base image, so a rebuild takes seconds and never touches the
# base. Images and VM disks live outside the repository (SYX_VM_HOME, default
# ~/.local/share/syntaxis-vm). No root, no bridge, no firewall changes.
#
# Run `test/vm/vmctl.sh help` for the command list. See test/vm/README.md.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
HOME_DIR=${SYX_VM_HOME:-$HOME/.local/share/syntaxis-vm}
IMG_DIR=$HOME_DIR/images
VM_DIR=$HOME_DIR/vms
KEY_DIR=$HOME_DIR/keys
KEY=$KEY_DIR/id_ed25519
DISTROS=$HERE/distros.conf
PORT_BASE=${SYX_VM_PORT_BASE:-2200}
SSH_USER=tester
SSH_WAIT=${SYX_VM_SSH_WAIT:-240}

die() { echo "vmctl: $*" >&2; exit 1; }
log() { echo "vmctl: $*" >&2; }

usage() {
	cat <<'EOF'
usage: vmctl.sh <command> [args]

  doctor                          check KVM access, tools, disk and memory
  fetch [distro...]               download/refresh base images (default: all);
                                  keeps old versions until 'prune'
  list                            list fetched images and VMs
  create <vm> <distro> [cpus] [mem-MB] [disk]
                                  make a VM disk from the current base image
                                  (defaults: 2 cpus, 2048 MB, 20G)
  start <vm> [--net=nat]          boot VM; default is isolated networking
  stop <vm>                       ACPI-style poweroff, then kill after a timeout
  ssh <vm> [command...]           shell (or run command) as user "tester"
  put <vm> <local> <remote>       copy files into the VM
  get <vm> <remote> <local>       copy files out of the VM
  rebuild <vm>                    stop, recreate disk from current base, boot
  destroy <vm>                    stop and delete the VM and its disk
  prune                           delete base images no VM or current link uses
  timer install|remove            weekly 'fetch' via a systemd --user timer
  help                            this text

Networking:
  isolated (default) - QEMU restrict=on: the guest has no outbound route to
      the internet, the host or the LAN. SSH is a host-loopback forward only.
      Use this for every escape or destructive test.
  nat (--net=nat)    - guest gets user-mode NAT with internet access. QEMU's
      10.0.2.2 gateway reaches host loopback services and the LAN can be
      reached too. Use only to provision a VM, never during escape tests.
EOF
}

distro_line() {
	local line
	line=$(awk -F'|' -v n="$1" '$0 !~ /^#/ && NF >= 4 && $1 == n { print; exit }' "$DISTROS")
	[ -n "$line" ] || die "unknown distro '$1' (see $DISTROS)"
	printf '%s\n' "$line"
}

valid_name() {
	[[ "$1" =~ ^[a-z0-9][a-z0-9-]{0,30}$ ]] || die "VM name must match ^[a-z0-9][a-z0-9-]{0,30}\$"
}

# Pull the upstream hash for one file out of a checksum file, any common format.
upstream_hash() {
	printf '%s\n' "$1" | awk -v b="$2" '
		{
			hit = 0
			for (i = 1; i <= NF; i++) {
				t = $i; gsub(/^\*/, "", t); gsub(/^\(|\)$|:$/, "", t)
				if (t == b) hit = 1
			}
			if (hit) {
				for (i = 1; i <= NF; i++)
					if ($i ~ /^[0-9a-f]+$/ && (length($i) == 64 || length($i) == 128)) { print $i; exit }
			}
		}'
}

check_kvm() {
	[ -r /dev/kvm ] && [ -w /dev/kvm ] || die "no access to /dev/kvm: run 'sudo usermod -aG kvm \$USER', then log in again"
}

need_tools() {
	local t missing=()
	for t in qemu-system-x86_64 qemu-img cloud-localds ssh ssh-keygen scp curl ss; do
		command -v "$t" >/dev/null 2>&1 || missing+=("$t")
	done
	[ "${#missing[@]}" -eq 0 ] || die "missing tools: ${missing[*]} (Debian: apt install qemu-system-x86 qemu-utils cloud-image-utils openssh-client curl iproute2)"
}

ensure_key() {
	mkdir -p "$KEY_DIR"
	chmod 700 "$KEY_DIR"
	[ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N '' -C syntaxis-vm -f "$KEY"
}

# ---- images ---------------------------------------------------------------

fetch_one() {
	local name dir url sums base hash ver dest tmp actual sumcmd sums_txt
	IFS="|" read -r name dir base sums <<<"$(distro_line "$1")"
	url=$dir/$base
	sums=$dir/$sums
	mkdir -p "$IMG_DIR/$name"

	sums_txt=$(curl -fsSL --retry 3 "$sums") || die "$name: cannot fetch checksums from $sums"
	hash=$(upstream_hash "$sums_txt" "$base")
	[ -n "$hash" ] || die "$name: no checksum for $base in $sums"
	case ${#hash} in
		64) sumcmd=sha256sum ;;
		128) sumcmd=sha512sum ;;
		*) die "$name: unexpected checksum length ${#hash}" ;;
	esac

	ver=${hash:0:16}
	dest=$IMG_DIR/$name/$ver.qcow2
	if [ -f "$dest" ]; then
		log "$name: already have $ver"
	else
		log "$name: downloading $ver"
		tmp=$(mktemp "$IMG_DIR/$name/.partial.XXXXXX")
		if ! curl -fL -sS --retry 3 -o "$tmp" "$url"; then
			rm -f "$tmp"
			die "$name: download failed: $url"
		fi
		actual=$($sumcmd "$tmp" | cut -d' ' -f1)
		if [ "$actual" != "$hash" ]; then
			rm -f "$tmp"
			die "$name: checksum mismatch (want $hash, got $actual)"
		fi
		qemu-img info -f qcow2 "$tmp" >/dev/null || { rm -f "$tmp"; die "$name: image is not qcow2"; }
		chmod 444 "$tmp"
		mv "$tmp" "$dest"
	fi
	ln -sfn "$ver.qcow2" "$IMG_DIR/$name/current.qcow2"
	date -u +%FT%TZ >"$IMG_DIR/$name/last-checked"
}

cmd_fetch() {
	local names=("$@") n
	# One fetch at a time: two runs would download the same image twice.
	mkdir -p "$HOME_DIR"
	exec 9>"$HOME_DIR/fetch.lock"
	flock -n 9 || die "another fetch is running"
	if [ "${#names[@]}" -eq 0 ]; then
		mapfile -t names < <(awk -F'|' '$0 !~ /^#/ && NF >= 4 { print $1 }' "$DISTROS")
	fi
	for n in "${names[@]}"; do fetch_one "$n"; done
}

cmd_prune() {
	local img used f d
	[ -d "$IMG_DIR" ] || return 0
	# Partial downloads left by an interrupted fetch (older than a day, so a live fetch is safe).
	find "$IMG_DIR" -mindepth 2 -maxdepth 2 -name '.partial.*' -mmin +1440 -print -delete
	# Base images that overlay disks still point at must stay.
	used=$(mktemp)
	for f in "$VM_DIR"/*/disk.qcow2; do
		[ -f "$f" ] || continue
		# -U reads while a running VM holds the disk lock; a failure aborts before any delete.
		qemu-img info -U --output=json "$f" | grep -o '"backing-filename": "[^"]*"' | sed 's/.*: "//; s/"$//' | xargs -r -n1 basename >>"$used" || true
	done
	for d in "$IMG_DIR"/*/; do
		[ -d "$d" ] || continue
		local cur=""
		[ -L "$d/current.qcow2" ] && cur=$(readlink "$d/current.qcow2")
		for img in "$d"*.qcow2; do
			[ -e "$img" ] && [ ! -L "$img" ] || continue
			local b; b=$(basename "$img")
			if [ "$b" = "$cur" ] || grep -qxF "$b" "$used"; then continue; fi
			log "prune $b"
			rm -f "$img"
		done
	done
	rm -f "$used"
}

# ---- VMs -------------------------------------------------------------------

load_vm() {
	valid_name "$1"
	VM_NAME=$1
	VM=$VM_DIR/$1
	[ -f "$VM/vm.conf" ] || die "no VM '$1' (see: vmctl.sh list)"
	# shellcheck disable=SC1091
	. "$VM/vm.conf"
	SSH=(ssh -i "$KEY" -p "$port" -o BatchMode=yes -o ConnectTimeout=5
		-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$VM/known_hosts"
		-o LogLevel=ERROR "$SSH_USER@127.0.0.1")
	SCP=(scp -i "$KEY" -P "$port" -o BatchMode=yes -o StrictHostKeyChecking=accept-new
		-o UserKnownHostsFile="$VM/known_hosts" -o LogLevel=ERROR -r)
}

is_running() {
	local pid
	[ -f "$VM/qemu.pid" ] || return 1
	pid=$(cat "$VM/qemu.pid")
	kill -0 "$pid" 2>/dev/null
}

alloc_port() {
	local p
	for ((p = PORT_BASE; p < PORT_BASE + 100; p++)); do
		grep -qs "^port=$p\$" "$VM_DIR"/*/vm.conf && continue
		ss -ltnH "( sport = :$p )" | grep -q . && continue
		echo "$p"
		return 0
	done
	die "no free port in $PORT_BASE..$((PORT_BASE + 99))"
}

make_disk() {
	# make_disk <vm dir> <distro> <disk size>
	local dir=$1 distro=$2 size=$3 base
	base=$(readlink -f "$IMG_DIR/$distro/current.qcow2" 2>/dev/null) || true
	[ -n "$base" ] && [ -f "$base" ] || die "no base image for $distro: run 'vmctl.sh fetch $distro'"
	rm -f "$dir/disk.qcow2"
	qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$dir/disk.qcow2" "$size"
}

make_seed() {
	# make_seed <vm name> <vm dir>
	local name=$1 dir=$2 pub
	pub=$(cat "$KEY.pub")
	cat >"$dir/user-data" <<EOF
#cloud-config
hostname: $name
users:
  - name: $SSH_USER
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - $pub
ssh_pwauth: false
package_update: false
package_upgrade: false
EOF
	printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$name" "$(date +%s)" "$name" >"$dir/meta-data"
	cloud-localds "$dir/seed.iso" "$dir/user-data" "$dir/meta-data"
	rm -f "$dir/user-data" "$dir/meta-data"
}

cmd_create() {
	local vm=$1 distro=$2 cpus=${3:-2} mem=${4:-2048} size=${5:-20G} dir
	valid_name "$vm"
	distro_line "$distro" >/dev/null
	dir=$VM_DIR/$vm
	[ ! -e "$dir" ] || die "VM '$vm' exists (use rebuild or destroy)"
	# Check the base before creating anything, so a failure leaves no half-made VM.
	[ -f "$(readlink -f "$IMG_DIR/$distro/current.qcow2" 2>/dev/null)" ] || die "no base image for $distro: run 'vmctl.sh fetch $distro'"
	mkdir -p "$dir"
	ensure_key
	make_disk "$dir" "$distro" "$size"
	cat >"$dir/vm.conf" <<EOF
distro=$distro
cpus=$cpus
mem=$mem
disk=$size
port=$(alloc_port)
EOF
	make_seed "$vm" "$dir"
	log "created $vm ($distro, ${cpus} cpus, ${mem} MB, disk $size)"
}

wait_ssh() {
	local deadline=$((SECONDS + SSH_WAIT))
	until "${SSH[@]}" true >/dev/null 2>&1; do
		[ "$SECONDS" -lt "$deadline" ] || die "$VM_NAME: no SSH after ${SSH_WAIT}s (see $VM/console.log)"
		sleep 3
	done
}

cmd_start() {
	local vm=$1 net=isolated netdev
	shift
	while [ $# -gt 0 ]; do
		case $1 in
			--net=nat) net=nat ;;
			--net=isolated) net=isolated ;;
			*) die "unknown option $1" ;;
		esac
		shift
	done
	load_vm "$vm"
	check_kvm
	if is_running; then log "$vm already running"; wait_ssh; return 0; fi
	if [ "$net" = nat ]; then
		log "WARNING: nat mode: guest can reach host loopback services and the LAN; do not run escape tests"
		netdev="user,id=n0,hostfwd=tcp:127.0.0.1:$port-:22"
	else
		netdev="user,id=n0,restrict=on,hostfwd=tcp:127.0.0.1:$port-:22"
	fi
	rm -f "$VM/qemu.pid"
	qemu-system-x86_64 \
		-name "$vm" \
		-machine q35,accel=kvm -cpu host \
		-smp "$cpus" -m "$mem" \
		-display none -monitor none \
		-drive file="$VM/disk.qcow2",if=virtio,format=qcow2 \
		-drive file="$VM/seed.iso",if=virtio,media=cdrom,format=raw,readonly=on \
		-netdev "$netdev" -device virtio-net-pci,netdev=n0 \
		-device virtio-rng-pci \
		-serial file:"$VM/console.log" \
		-pidfile "$VM/qemu.pid" -daemonize
	log "$vm starting ($net networking, ssh 127.0.0.1:$port)"
	wait_ssh
	log "$vm up"
}

cmd_stop() {
	local vm=$1 deadline
	load_vm "$vm"
	is_running || { log "$vm not running"; return 0; }
	timeout 20 "${SSH[@]}" sudo systemctl poweroff >/dev/null 2>&1 || true
	deadline=$((SECONDS + 60))
	while is_running && [ "$SECONDS" -lt "$deadline" ]; do sleep 2; done
	if is_running; then
		log "$vm did not power off; killing qemu"
		kill -TERM "$(cat "$VM/qemu.pid")" 2>/dev/null || true
		sleep 5
		is_running && kill -KILL "$(cat "$VM/qemu.pid")" 2>/dev/null || true
	fi
	rm -f "$VM/qemu.pid"
	log "$vm stopped"
}

cmd_rebuild() {
	local vm=$1 net=${2:-isolated}
	load_vm "$vm"
	cmd_stop "$vm"
	ensure_key
	make_disk "$VM" "$distro" "$disk"
	rm -f "$VM/known_hosts"
	make_seed "$vm" "$VM"
	log "rebuilt $vm from current $distro base"
	cmd_start "$vm" "--net=$net"
}

cmd_destroy() {
	local vm=$1 dir
	valid_name "$vm"
	dir=$VM_DIR/$vm
	[ -d "$dir" ] || die "no VM '$vm'"
	cmd_stop "$vm"
	case $dir in
		"$VM_DIR"/*) rm -rf "$dir" ;;
		*) die "refusing to delete $dir" ;;
	esac
	log "destroyed $vm"
}

cmd_list() {
	local d vm distro port state size
	echo "IMAGES"
	for d in "$IMG_DIR"/*/; do
		[ -d "$d" ] || continue
		local cur=""
		[ -L "$d/current.qcow2" ] && cur=$(readlink "$d/current.qcow2")
		printf '  %-12s current=%-22s versions=%s size=%s\n' "$(basename "$d")" "${cur:--}" \
			"$(find "$d" -maxdepth 1 -name "*.qcow2" ! -type l | wc -l)" "$(du -sh "$d" 2>/dev/null | cut -f1)"
	done
	echo "VMS"
	for d in "$VM_DIR"/*/; do
		[ -d "$d" ] || continue
		vm=$(basename "$d")
		distro=$(sed -n 's/^distro=//p' "$d/vm.conf")
		port=$(sed -n 's/^port=//p' "$d/vm.conf")
		if [ -f "$d/qemu.pid" ] && kill -0 "$(cat "$d/qemu.pid")" 2>/dev/null; then state=running; else state=stopped; fi
		size=$(du -sh "$d/disk.qcow2" 2>/dev/null | cut -f1)
		printf '  %-16s %-10s %-8s port=%-5s disk=%s\n' "$vm" "$distro" "$state" "$port" "${size:--}"
	done
}

cmd_doctor() {
	local ok=0
	if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then echo "ok    /dev/kvm accessible"; else echo "FAIL  /dev/kvm not accessible (kvm group)"; ok=1; fi
	if command -v qemu-system-x86_64 >/dev/null; then echo "ok    $(qemu-system-x86_64 --version | head -1)"; else echo "FAIL  qemu-system-x86_64 missing"; ok=1; fi
	for t in qemu-img cloud-localds ssh ssh-keygen scp curl ss; do
		if command -v "$t" >/dev/null; then echo "ok    $t"; else echo "FAIL  $t missing"; ok=1; fi
	done
	echo "info  free disk on $HOME_DIR's filesystem: $(df -h "$(dirname "$HOME_DIR")" 2>/dev/null | awk 'NR==2{print $4}')"
	echo "info  available memory: $(free -h | awk '/^Mem:/{print $7}')"
	return $ok
}

cmd_put() { load_vm "$1"; "${SCP[@]}" "$2" "$SSH_USER@127.0.0.1:$3"; }
cmd_get() { load_vm "$1"; "${SCP[@]}" "$SSH_USER@127.0.0.1:$2" "$3"; }

cmd_timer() {
	local unit_dir=$HOME/.config/systemd/user
	case ${1:-} in
		install)
			mkdir -p "$unit_dir"
			cat >"$unit_dir/syntaxis-vm-fetch.service" <<EOF
[Unit]
Description=Refresh Syntaxis test VM base images

[Service]
Type=oneshot
ExecStart=$HERE/vmctl.sh fetch
EOF
			cat >"$unit_dir/syntaxis-vm-fetch.timer" <<'EOF'
[Unit]
Description=Weekly refresh of Syntaxis test VM base images

[Timer]
OnCalendar=weekly
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
EOF
			systemctl --user daemon-reload
			systemctl --user enable --now syntaxis-vm-fetch.timer
			log "timer installed (runs while you are logged in; 'loginctl enable-linger' keeps it running after logout)"
			;;
		remove)
			systemctl --user disable --now syntaxis-vm-fetch.timer 2>/dev/null || true
			rm -f "$unit_dir/syntaxis-vm-fetch.service" "$unit_dir/syntaxis-vm-fetch.timer"
			systemctl --user daemon-reload
			log "timer removed"
			;;
		*) die "usage: vmctl.sh timer install|remove" ;;
	esac
}

# ---- dispatch --------------------------------------------------------------

cmd=${1:-help}
shift || true
case $cmd in
	help|-h|--help) usage ;;
	doctor) cmd_doctor ;;
	fetch) need_tools; mkdir -p "$IMG_DIR"; cmd_fetch "$@" ;;
	list) cmd_list ;;
	create) need_tools; [ $# -ge 2 ] || die "usage: create <vm> <distro> [cpus] [mem-MB] [disk]"; cmd_create "$@" ;;
	start) need_tools; [ $# -ge 1 ] || die "usage: start <vm> [--net=nat]"; cmd_start "$@" ;;
	stop) [ $# -eq 1 ] || die "usage: stop <vm>"; cmd_stop "$1" ;;
	ssh) [ $# -ge 1 ] || die "usage: ssh <vm> [command...]"; vm=$1; shift; load_vm "$vm"
		if [ $# -eq 0 ]; then exec ssh -t -i "$KEY" -p "$port" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$VM/known_hosts" -o LogLevel=ERROR "$SSH_USER@127.0.0.1"; fi
		"${SSH[@]}" "$@" ;;
	put) [ $# -eq 3 ] || die "usage: put <vm> <local> <remote>"; cmd_put "$@" ;;
	get) [ $# -eq 3 ] || die "usage: get <vm> <remote> <local>"; cmd_get "$@" ;;
	rebuild) need_tools; [ $# -ge 1 ] || die "usage: rebuild <vm>"; cmd_rebuild "$@" ;;
	destroy) [ $# -eq 1 ] || die "usage: destroy <vm>"; cmd_destroy "$1" ;;
	prune) cmd_prune ;;
	timer) cmd_timer "$@" ;;
	*) die "unknown command '$cmd' (try: vmctl.sh help)" ;;
esac
