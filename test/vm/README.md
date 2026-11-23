# Syntaxis test VMs

Disposable Linux VMs for destructive and escape-route tests of Syntaxis. A test
runs inside a VM, so a broken sandbox, a bad firewall rule or a host-escape
attempt damages only the VM, which can be rebuilt in seconds.

Stack: QEMU/KVM, upstream minimal cloud images, cloud-init, and one bash script
(`vmctl.sh`). No libvirt, no root for day-to-day use, no bridges or iptables
changes. Nothing binary is stored in the repository.

## One-time host setup

```sh
sudo apt install qemu-system-x86 qemu-utils cloud-image-utils openssh-client curl iproute2
sudo usermod -aG kvm "$USER"     # then log out and back in
test/vm/vmctl.sh doctor          # every line should say ok
test/vm/vmctl.sh fetch           # download and verify all base images (about 2.5 GB)
test/vm/vmctl.sh timer install   # optional: weekly fetch of newer upstream images
```

Storage lives outside the repository, in `${SYX_VM_HOME:-~/.local/share/syntaxis-vm}`:

- `images/<distro>/<hash16>.qcow2`: base images, read-only, one file per upstream version
- `images/<distro>/current.qcow2`: symlink to the version new VMs use
- `vms/<name>/`: one VM's overlay disk, cloud-init seed, console log, pid, port
- `keys/id_ed25519`: SSH key for the `tester` account in every VM (generated, never committed)

## Commands

| Command | What it does |
|---|---|
| `vmctl.sh fetch [distro...]` | Read each upstream checksum file, download the image only if its hash is new, verify it, point `current` at it. |
| `vmctl.sh create <vm> <distro> [cpus] [mem-MB] [disk]` | Make a VM: qcow2 overlay on `current`, cloud-init seed (user `tester`, sudo, SSH key). Defaults: 2 cpus, 2048 MB, 20G. |
| `vmctl.sh start <vm> [--net=nat]` | Boot and wait for SSH. Default networking is isolated (see below). |
| `vmctl.sh ssh <vm> [cmd...]` | Interactive shell, or run one command as `tester`. |
| `vmctl.sh put <vm> <local> <remote>` / `get` | Copy files in or out (scp). |
| `vmctl.sh stop <vm>` | Power off through the guest, then kill QEMU if it does not stop. |
| `vmctl.sh rebuild <vm>` | Stop, make a fresh overlay from the current base, boot. Everything in the guest is gone. |
| `vmctl.sh destroy <vm>` | Stop and delete the VM. |
| `vmctl.sh import-iso <file>` / `screenshot <vm> <png>` | Windows only: register the ISO; take a picture of the guest screen. |
| `vmctl.sh list` | Images (versions, size) and VMs (state, port, disk use). |
| `vmctl.sh prune` | Delete base images that neither `current` nor any VM overlay uses. Safe with running VMs. |
| `vmctl.sh timer install\|remove` | Weekly `fetch` as a systemd user timer. |

Distros are in `distros.conf` (`name|image URL|checksum URL|file name`). To add one, add a line.
Current set: `debian12`, `debian13`, `ubuntu2404`, `rocky9`, `alma9`.

Example run:

```sh
test/vm/vmctl.sh create esc-1 debian12
test/vm/vmctl.sh start esc-1
test/vm/vmctl.sh put esc-1 ./build/syntaxis.tar.gz /home/tester/
test/vm/vmctl.sh ssh esc-1 'tar xzf syntaxis.tar.gz && ./run-escape-tests.sh'
test/vm/vmctl.sh rebuild esc-1   # back to a clean base for the next run
test/vm/vmctl.sh destroy esc-1
```

## Self-test

`test/vm/selftest.sh [distro...]` runs the harness against real VMs: create, boot,
SSH and sudo, OS identity, network isolation (with a real listener on the host's
loopback), file copy in and out, rebuild wiping the guest, stop and destroy. The
first distro also checks that `nat` mode really differs from `isolated` and that
`prune` keeps a base image a running VM uses. All five Linux distros pass (105
checks, about 15 minutes). Run it after changing `vmctl.sh`.

`selftest.sh windows11` is opt-in: it needs the imported ISO and does a full
unattended install (about 15 minutes), then checks Windows 11 Pro, the answer
file has no product key, TPM 2.0 and Secure Boot, isolation after the first boot,
and a file round trip.

## Windows 11

A Windows VM installs from the consumer Windows 11 ISO (Home/Pro multi-edition)
with an unattended answer file. **We supply no product key, generic or otherwise**:
the edition is chosen by name (`Windows 11 Pro`, override with `SYX_WIN_EDITION`).
Nothing is cracked or bypassed: UEFI with Secure Boot (OVMF) and a software
TPM 2.0 (`swtpm`) meet the Windows 11 requirements, and every VM gets its own
random machine UUID. The disk is NVMe and the network card an e1000e, so no
extra drivers are needed.

Activation: Windows decides this by itself. Both VMs we installed reported
"permanently activated" after their nat first boot, with no key from us and each
with its own random machine UUID. We do not know why and make no use of it. Do not
rely on that, or on the opposite, in any test.

```sh
sudo apt install swtpm genisoimage socat ovmf
# Download the ISO in a browser from Microsoft (Windows 11, x64, multi-edition).
# Microsoft's download service refuses scripted requests, so there is no fetch step.
test/vm/vmctl.sh import-iso ~/Downloads/Win11_*.iso
test/vm/vmctl.sh create win-1 windows11      # 4 cpus, 4096 MB, 64G by default
test/vm/vmctl.sh start win-1                 # installs unattended, 15 to 40 minutes
test/vm/vmctl.sh screenshot win-1 /tmp/win.png   # watch the install
test/vm/vmctl.sh stop win-1
test/vm/vmctl.sh start win-1                 # now isolated, like the Linux VMs
test/vm/vmctl.sh ssh win-1 'ver'             # PowerShell over OpenSSH, user "tester"
```

- The ISO lives in `$SYX_VM_HOME/isos/windows11/`, outside the repository. The
  browser download link Microsoft gives you expires, so import the file, not the link.
- The first boot of a new Windows VM uses `nat` networking, because the answer file
  installs the OpenSSH server from Windows Update. After the install, `stop` and
  `start` boot it isolated. Treat that first boot as provisioning, not a test.
- `tester` is a local administrator. Its autologon password is random per VM and
  stored in the VM directory; SSH uses the key only.
- `rebuild` on a Windows VM reinstalls it from the ISO (about 15 minutes), so keep
  a Windows VM for a whole test session rather than rebuilding between steps.
- A Windows VM needs about 4 GB of RAM and a 64 GB disk image (it uses about 15 GB once installed).
- `put` and `get` use the legacy scp protocol for Windows guests: Win32-OpenSSH's SFTP server
  truncates scp downloads at 200 KiB.
- OpenSSH comes from Microsoft's Win32-OpenSSH release on GitHub; the Windows optional
  feature (Windows Update) is the fallback, because it hung for over 30 minutes in testing.

## Networking

| Mode | Guest can reach | Use for |
|---|---|---|
| `isolated` (default) | Nothing outside the guest. SSH is a forward from `127.0.0.1:<port>` on the host. | All escape and destructive tests. |
| `nat` (`--net=nat`) | Internet, and the host's loopback services and LAN through QEMU's `10.0.2.2` gateway. | Provisioning only (installing packages). Start, install, stop, then boot isolated for tests. |

Checked on this host: an isolated guest gets connection refused or timeouts to
the host's Ollama and LiteLLM ports, to host SSH, to a LAN address and to the
internet; DNS does not resolve. Only the gateway answers ping, and QEMU answers
that itself.

SSH ports come from 2200 to 2299 and are bound to 127.0.0.1 only.

## Resource budget

- Base images: about 0.3 to 0.7 GB each; all five are about 2.5 GB.
- VM overlays start at a few tens of MB and grow with what the test writes.
- Memory: a VM takes its `mem` value (2 GB by default) while running. Check `doctor` for available memory before starting several.
- `rebuild` and `destroy` free the overlay space. Run `prune` after upgrading images.

## Keeping images current

`fetch` checks each upstream checksum file, so a moving "latest" image is picked up
without changing VM names. Existing VMs keep the version they were created from, so
a fetch never changes a VM under a running test. `rebuild` moves a VM to the newest base.

Trust: the image and its checksum come from the same vendor over HTTPS. The
checksum file is not checked against the vendor's GPG signature. That is a
known gap; it is acceptable for disposable test guests.

## Other operating systems

- **Windows 11**: see "Windows 11" below. ReactOS has no cloud image and is not
  a reliable stand-in for Windows, so it is not used.
- **macOS**: not supported. Apple does not license macOS for non-Apple hardware, so
  there is no legitimate KVM image path. Test macOS on real hardware or in a hosted
  macOS CI runner.

## Rules for test code

- Run every escape or destructive test inside a VM, never on the host. Confirm with
  `vmctl.sh ssh <vm> hostname` before running anything.
- Start from `rebuild` or a fresh `create`. Do not reuse a VM after a test that
  broke it.
- Keep private values (host LAN addresses, keys, model endpoints) out of this
  directory and out of commits. Use `$SYX_VM_HOME` or `~/.config`.
- Do not put a binary image, VM disk or ISO in the repository.
