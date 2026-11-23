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
`prune` keeps a base image a running VM uses. All five distros pass (105 checks,
about 15 minutes). Run it after changing `vmctl.sh`.

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

- **Windows 11**: not implemented yet. The plan is the consumer Windows 11 ISO
  (Home/Pro) from Microsoft's own download page, installed without a product key
  and left unactivated for the short test life of a VM. It needs a TPM 2.0
  emulator (`swtpm`) and UEFI firmware (`ovmf`) on the host, a blank 64 GB disk
  instead of a cloud image, and an unattended-install answer file, so it
  needs its own `vmctl.sh` path. Windows 11 Enterprise Evaluation is too heavy.
  ReactOS has no cloud image and is not a reliable stand-in for Windows.
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
