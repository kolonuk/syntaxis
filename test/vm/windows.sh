# SPDX-License-Identifier: GPL-2.0-or-later
# windows.sh - Windows 11 support for vmctl.sh. Sourced by vmctl.sh, not run.
#
# A Windows VM is installed from the consumer Windows 11 ISO (Home/Pro) with an
# unattended answer file. No product key is supplied by us.
# UEFI with Secure Boot (OVMF) and a software TPM 2.0 (swtpm) satisfy the
# Windows 11 requirements without any bypass. The disk is NVMe and the network
# card is an e1000e, both with drivers built into Windows.
#
# The first boot of a new Windows VM installs OpenSSH from Windows Update, so it
# runs with nat networking. After that the VM boots isolated like the Linux ones.

OVMF_CODE=${SYX_OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.ms.fd}
OVMF_VARS=${SYX_OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.ms.fd}
WIN_ISO_DIR=$HOME_DIR/isos/windows11
WIN_EDITION=${SYX_WIN_EDITION:-Windows 11 Pro}
WIN_TPL=$IMG_DIR/windows11
WIN_INSTALL_WAIT=${SYX_WIN_INSTALL_WAIT:-3600}
WIN_BOOT_WAIT=${SYX_WIN_BOOT_WAIT:-600}

win_latest_iso() { ls -t "$WIN_ISO_DIR"/*.iso 2>/dev/null | head -1 || true; }

win_need_tools() {
	local t missing=()
	for t in swtpm genisoimage socat; do
		command -v "$t" >/dev/null 2>&1 || missing+=("$t")
	done
	[ "${#missing[@]}" -eq 0 ] || die "missing tools: ${missing[*]} (Debian: apt install swtpm genisoimage socat)"
	[ -f "$OVMF_CODE" ] && [ -f "$OVMF_VARS" ] || die "OVMF firmware not found (Debian: apt install ovmf)"
}

cmd_import_iso() {
	local src=$1 dest
	[ -f "$src" ] || die "no such file: $src"
	case $src in *.iso) ;; *) die "expected an .iso file" ;; esac
	mkdir -p "$WIN_ISO_DIR"
	dest=$WIN_ISO_DIR/$(basename "$src")
	if [ "$(readlink -f "$src")" = "$(readlink -f "$dest")" ]; then
		log "already in place: $dest"
	else
		cp "$src" "$dest"
		log "imported $dest"
	fi
}

win_make_unattend() {
	# win_make_unattend <vm name> <vm dir>
	local name=$1 dir=$2 pw pub cname staging
	if [ ! -f "$dir/win-password" ]; then
		# Only used for the one-time autologon of the local account; SSH uses the key.
		pw="$(head -c 15 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')Aa1x"
		(umask 077; printf '%s\n' "$pw" >"$dir/win-password")
	fi
	pw=$(cat "$dir/win-password")
	pub=$(cat "$KEY.pub")
	cname=$(printf '%s' "$name" | tr 'a-z' 'A-Z' | cut -c1-15)
	staging=$(mktemp -d)
	sed -e "s|@PASSWORD@|$pw|g" -e "s|@EDITION@|$WIN_EDITION|g" -e "s|@COMPUTERNAME@|$cname|g" \
		"$HERE/windows/autounattend.xml.in" >"$staging/autounattend.xml"
	sed -e "s|@PUBKEY@|$pub|g" "$HERE/windows/provision.ps1.in" >"$staging/provision.ps1"
	genisoimage -quiet -J -r -V UNATTEND -o "$dir/unattend.iso" "$staging"
	rm -rf "$staging"
}

win_create() {
	# win_create <vm> [cpus] [mem-MB] [disk]
	local vm=$1 cpus=${2:-4} mem=${3:-4096} size=${4:-64G} dir iso
	valid_name "$vm"
	win_need_tools
	dir=$VM_DIR/$vm
	[ ! -e "$dir" ] || die "VM '$vm' exists (use rebuild or destroy)"
	iso=$(win_latest_iso)
	[ -n "$iso" ] || die "no Windows ISO: download Windows 11 (x64) from Microsoft, then run 'vmctl.sh import-iso <file>'"
	ensure_key
	if win_have_template && [ "${SYX_WIN_FRESH:-0}" != 1 ]; then
		win_from_template "$dir" "$size"
		# Hold a lock while picking the port and writing vm.conf, so VMs created at
		# the same time (suite.sh runs several) cannot take the same SSH port.
		exec 8>"$HOME_DIR/alloc.lock"
		flock 8
		cat >"$dir/vm.conf" <<EOF
distro=windows11
kind=windows
cpus=$cpus
mem=$mem
disk=$size
port=$(alloc_port)
installed=1
uuid=$(cat /proc/sys/kernel/random/uuid)
EOF
		exec 8>&-
		log "created $vm from the saved template (windows11, $cpus cpus, $mem MB): boots in about a minute"
		return 0
	fi
	mkdir -p "$dir/tpm"
	qemu-img create -q -f qcow2 "$dir/disk.qcow2" "$size"
	cp "$OVMF_VARS" "$dir/OVMF_VARS.fd"
	# Hold a lock while picking the port and writing vm.conf, so VMs created at
	# the same time (suite.sh runs several) cannot take the same SSH port.
	exec 8>"$HOME_DIR/alloc.lock"
	flock 8
	cat >"$dir/vm.conf" <<EOF
distro=windows11
kind=windows
cpus=$cpus
mem=$mem
disk=$size
port=$(alloc_port)
installed=0
uuid=$(cat /proc/sys/kernel/random/uuid)
EOF
	exec 8>&-
	win_make_unattend "$vm" "$dir"
	log "created $vm (windows11, $cpus cpus, $mem MB, disk $size, install from $(basename "$iso"))"
}

win_have_template() { [ -f "$WIN_TPL/template.qcow2" ] && [ -f "$WIN_TPL/template.VARS.fd" ] && [ -d "$WIN_TPL/template.tpm" ]; }

# win_from_template <vm dir> <disk size>: overlay on the template, with its firmware
# variables and TPM state, so the clone boots as the installed machine it was copied from.
win_from_template() {
	local dir=$1 size=$2
	mkdir -p "$dir"
	qemu-img create -q -f qcow2 -F qcow2 -b "$WIN_TPL/template.qcow2" "$dir/disk.qcow2" "$size"
	cp "$WIN_TPL/template.VARS.fd" "$dir/OVMF_VARS.fd"
	cp -a "$WIN_TPL/template.tpm" "$dir/tpm"
}

cmd_win_template() {
	# win-template <vm>: save an installed Windows VM as the template new VMs start from.
	local vm=$1
	load_vm "$vm"
	[ "${kind:-linux}" = windows ] || die "$vm is not a Windows VM"
	[ "${installed:-0}" = 1 ] || die "$vm has not finished installing (start it and wait for SSH)"
	cmd_stop "$vm"
	log "saving $vm as the Windows template (this compresses the disk and takes a few minutes)"
	mkdir -p "$WIN_TPL"
	rm -rf "$WIN_TPL/template.new" "$WIN_TPL/template.tpm.new"
	qemu-img convert -O qcow2 -c "$VM/disk.qcow2" "$WIN_TPL/template.qcow2.new"
	cp "$VM/OVMF_VARS.fd" "$WIN_TPL/template.VARS.fd.new"
	cp -a "$VM/tpm" "$WIN_TPL/template.tpm.new"
	rm -rf "$WIN_TPL/template.tpm"
	mv "$WIN_TPL/template.tpm.new" "$WIN_TPL/template.tpm"
	mv "$WIN_TPL/template.VARS.fd.new" "$WIN_TPL/template.VARS.fd"
	chmod 444 "$WIN_TPL/template.qcow2.new"
	mv -f "$WIN_TPL/template.qcow2.new" "$WIN_TPL/template.qcow2"
	printf 'saved %s from %s, ISO %s\n' "$(date -u +%FT%TZ)" "$vm" "$(basename "$(win_latest_iso)")" >"$WIN_TPL/template.info"
	log "template saved ($(du -h "$WIN_TPL/template.qcow2" | cut -f1)); new Windows VMs now start from it"
}

win_send_keys() {
	# The Windows ISO asks "Press any key to boot from CD": answer it for the first 40 s.
	local i
	for ((i = 0; i < 40; i++)); do
		printf 'sendkey ret\n' | socat - UNIX-CONNECT:"$VM/monitor.sock" >/dev/null 2>&1 || true
		sleep 1
	done
}

win_start() {
	# win_start <vm> [--net=nat|--net=isolated]
	local vm=$1 net=isolated netdev installed_flag wait iso
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
	win_need_tools
	if is_running; then
		log "$vm already running"
		if [ "${installed:-0}" = 1 ]; then
			win_wait_ssh "$WIN_BOOT_WAIT"
		else
			win_wait_ssh "$WIN_INSTALL_WAIT"
			sed -i 's/^installed=.*/installed=1/' "$VM/vm.conf"
			log "$vm installed; stop it and start again to boot isolated"
		fi
		return 0
	fi

	installed_flag=${installed:-0}
	if [ "$installed_flag" != 1 ] && [ "$net" = isolated ]; then
		log "first boot of a new Windows VM installs OpenSSH from Windows Update: using nat networking"
		net=nat
	fi
	if [ "$net" = nat ]; then
		log "WARNING: nat mode: guest can reach host loopback services and the LAN; do not run escape tests"
		netdev="user,id=n0,hostfwd=tcp:127.0.0.1:$port-:22"
	else
		netdev="user,id=n0,restrict=on,hostfwd=tcp:127.0.0.1:$port-:22"
	fi

	# Software TPM 2.0. Stale sockets from a killed VM would make swtpm refuse to start.
	[ -f "$VM/swtpm.pid" ] && kill "$(cat "$VM/swtpm.pid")" 2>/dev/null || true
	rm -f "$VM/tpm.sock" "$VM/swtpm.pid" "$VM/qemu.pid" "$VM/monitor.sock"
	swtpm socket --tpmstate dir="$VM/tpm" --ctrl type=unixio,path="$VM/tpm.sock" \
		--tpm2 --flags not-need-init,startup-clear --daemon --pid file="$VM/swtpm.pid"

	local media=()
	if [ "$installed_flag" != 1 ]; then
		iso=$(win_latest_iso)
		[ -n "$iso" ] || die "no Windows ISO (vmctl.sh import-iso <file>)"
		media=(-device ahci,id=ahci
			-drive file="$iso",if=none,id=cd0,media=cdrom,readonly=on -device ide-cd,drive=cd0,bus=ahci.0,bootindex=1
			-drive file="$VM/unattend.iso",if=none,id=cd1,media=cdrom,readonly=on -device ide-cd,drive=cd1,bus=ahci.1)
	fi

	qemu-system-x86_64 \
		-name "$vm" \
		${uuid:+-uuid "$uuid"} \
		-machine q35,accel=kvm,smm=on -cpu host,hv_relaxed,hv_vapic,hv_spinlocks=0x1fff,hv_time \
		-smp "$cpus" -m "$mem" \
		-global driver=cfi.pflash01,property=secure,value=on \
		-drive if=pflash,format=raw,unit=0,readonly=on,file="$OVMF_CODE" \
		-drive if=pflash,format=raw,unit=1,file="$VM/OVMF_VARS.fd" \
		-chardev socket,id=chrtpm,path="$VM/tpm.sock" -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-crb,tpmdev=tpm0 \
		-drive file="$VM/disk.qcow2",if=none,id=sys,format=qcow2,discard=unmap -device nvme,drive=sys,serial=syxwin0,bootindex=2 \
		"${media[@]}" \
		-netdev "$netdev" -device e1000e,netdev=n0 \
		-vga std -display none -rtc base=localtime \
		-monitor unix:"$VM/monitor.sock",server,nowait \
		-pidfile "$VM/qemu.pid" -daemonize
	log "$vm starting ($net networking, ssh 127.0.0.1:$port)"

	if [ "$installed_flag" != 1 ]; then
		win_send_keys &
		wait=$WIN_INSTALL_WAIT
		log "installing Windows unattended; this takes 15 to 40 minutes (watch with: vmctl.sh screenshot $vm out.png)"
	else
		wait=$WIN_BOOT_WAIT
	fi
	win_wait_ssh "$wait"
	if [ "$installed_flag" != 1 ]; then
		sed -i 's/^installed=.*/installed=1/' "$VM/vm.conf"
		log "$vm installed; stop it and start again to boot isolated"
	fi
	log "$vm up"
}

win_wait_ssh() {
	local limit=$1 deadline
	deadline=$((SECONDS + limit))
	until "${SSH[@]}" 'exit 0' >/dev/null 2>&1; do
		is_running || die "$VM_NAME: qemu exited before SSH came up (see $VM)"
		[ "$SECONDS" -lt "$deadline" ] || die "$VM_NAME: no SSH after ${limit}s (vmctl.sh screenshot $VM_NAME out.png)"
		sleep 10
	done
}

win_stop() {
	local deadline
	timeout 30 "${SSH[@]}" 'shutdown /s /t 0' >/dev/null 2>&1 || true
	deadline=$((SECONDS + 120))
	while is_running && [ "$SECONDS" -lt "$deadline" ]; do sleep 3; done
	if is_running; then
		log "$VM_NAME did not shut down; killing qemu"
		kill -TERM "$(cat "$VM/qemu.pid")" 2>/dev/null || true
		sleep 5
		is_running && kill -KILL "$(cat "$VM/qemu.pid")" 2>/dev/null || true
	fi
	[ -f "$VM/swtpm.pid" ] && kill "$(cat "$VM/swtpm.pid")" 2>/dev/null || true
	rm -f "$VM/qemu.pid" "$VM/swtpm.pid" "$VM/tpm.sock" "$VM/monitor.sock"
}

cmd_screenshot() {
	# screenshot <vm> <out.png>: what the guest's screen shows, for watching an install.
	local out=$2 ppm
	load_vm "$1"
	is_running || die "$1 is not running"
	[ -S "$VM/monitor.sock" ] || die "$1 has no monitor socket (only Windows VMs do)"
	ppm=$(mktemp --suffix=.ppm)
	printf 'screendump %s\n' "$ppm" | socat - UNIX-CONNECT:"$VM/monitor.sock" >/dev/null
	sleep 1
	python3 - "$ppm" "$out" <<'PY'
import sys, zlib, struct
src, dst = sys.argv[1], sys.argv[2]
data = open(src, "rb").read()
parts = data.split(b"\n", 3)
w, h = map(int, parts[1].split())
px = parts[3]
rows = b"".join(b"\x00" + px[y * w * 3:(y + 1) * w * 3] for y in range(h))
def chunk(t, d):
    c = struct.pack(">I", len(d)) + t + d
    return c + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) \
    + chunk(b"IDAT", zlib.compress(rows, 6)) + chunk(b"IEND", b"")
open(dst, "wb").write(png)
PY
	rm -f "$ppm"
}

win_rebuild() {
	# From the saved template this takes seconds; without one it is a fresh install.
	local vm=$1 net=$2
	rm -rf "$VM/tpm" "$VM/disk.qcow2" "$VM/OVMF_VARS.fd" "$VM/known_hosts" "$VM/win-password" "$VM/unattend.iso"
	if win_have_template && [ "${SYX_WIN_FRESH:-0}" != 1 ]; then
		win_from_template "$VM" "$disk"
		sed -i 's/^installed=.*/installed=1/' "$VM/vm.conf"
		log "rebuilt $vm from the saved template"
		win_start "$vm" "--net=$net"
		return
	fi
	mkdir -p "$VM/tpm"
	qemu-img create -q -f qcow2 "$VM/disk.qcow2" "$disk"
	cp "$OVMF_VARS" "$VM/OVMF_VARS.fd"
	sed -i 's/^installed=.*/installed=0/' "$VM/vm.conf"
	win_make_unattend "$vm" "$VM"
	log "reinstalling $vm from the imported ISO"
	win_start "$vm" "--net=$net"
}
