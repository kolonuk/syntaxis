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

`test/vm/selftest.sh [distro...]` runs the harness against real VMs: create, boot, SSH and sudo, OS identity, network isolation, file copy in and out, rebuild, stop and destroy. The first distro also checks that `nat` differs from `isolated` and that `prune` keeps a base image a running VM uses. Run it after changing `vmctl.sh`. All five Linux distros take about 15 minutes.

`selftest.sh windows11` needs the imported ISO. It installs Windows unattended (about 15 minutes) and checks the edition, that the answer file has no product key, TPM 2.0 and Secure Boot, isolation after first boot, and a file round trip.

Run VM jobs from a copy of `test/vm` (`cp -r test/vm /tmp/vm-run`, then `SYX_REPO=<checkout> /tmp/vm-run/suite.sh`). Editing the scripts while a run is in progress breaks the run.

## Install and security suite

`test/vm/suite.sh [--keep] [distro...]` installs Syntaxis and runs the security tests, one fresh VM per distro (two at a time).

1. Boot on `nat`, copy the working tree in and run `scripts/install.sh` as a user would. Release images are pinned by `SYX_INSTALL_VERSION` (default `v0.2.0`).
2. Verify the install: health, units, nft table, `.env`, `doctor`. Re-run the installer and check the master key is kept.
3. Copy the Go toolchain in and download modules.
4. Pass A, isolated: reboot with no route out and run the sandbox, Docker proxy, egress, safehttp, doctor and policy tests against the real Docker and gVisor.
5. Pass B, nat: run `TestIntegrationEgressBlocksLANHostMetadataSMTP`, which needs the internet to prove the firewall blocks LAN, host, metadata and SMTP while the internet stays reachable.

Environment: `SYX_INSTALL_VERSION` (release to install), `SYX_GO_SDK` (a Go toolchain to copy in), `SYX_REPO` (checkout to test).

The suite waits for the guest to settle (boot finished, stack healthy, egress rules loaded, plus 30 seconds) before verifying. Testing earlier gives false failures from the egress firewall checks.

Results (logs, `go test -json`, skipped tests) go to `$SYX_VM_HOME/results/<timestamp>/<distro>/`. A VM is destroyed when everything passes and kept when something fails. Four tests skip by design: the opt-in live model test and a test that needs minutes of real load.

CI does not run the harness because GitHub-hosted runners have no KVM.

## Upgrade test

`test/vm/upgrade-test.sh [distro]` installs an old release in a VM and updates it through the admin app.

- Default run: install `SYX_UPGRADE_FROM` (default `v0.1.3`), update to `edge` through the old server and update service, check the three release streams are listed, restart the update service, switch to `preview`, check the weekly `--auto` run leaves a current install alone, then break `main` and check the update rolls back and the admin app reports the failure.
- `SYX_UPGRADE_TO=<tag>`: update to that published release and stop. Use this to check a release before announcing it.
- `SYX_KEEP=1` keeps the VM, `SYX_SUITE_MEM` sets its memory in MB (default 6144).

The test calls the GitHub API from the guest. GitHub limits unauthenticated requests per address, so a run can fail with "GitHub is limiting requests from this address". Wait and re-run.

## Windows 11

A Windows VM installs from the consumer Windows 11 ISO (Home/Pro multi-edition) with an unattended answer file. No product key is supplied; the edition is chosen by name (`Windows 11 Pro`, override with `SYX_WIN_EDITION`). UEFI with Secure Boot (OVMF) and a software TPM 2.0 (`swtpm`) meet the Windows 11 requirements. Every VM gets a random machine UUID. The disk is NVMe and the network card an e1000e, so no extra drivers are needed.

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

- The ISO is stored in `$SYX_VM_HOME/isos/windows11/`. Microsoft's download links expire, so import the file, not the link.
- The first boot uses `nat` because the answer file installs OpenSSH. After the install, `stop` and `start` boot isolated. Treat the first boot as provisioning, not a test.
- `tester` is a local administrator. The autologon password is random per VM and stored in the VM directory. SSH uses the key only.
- `rebuild` reinstalls from the ISO (about 15 minutes) unless a template exists (below).
- A Windows VM needs 4 GB of RAM and a 64 GB disk image (about 15 GB used).
- `put` and `get` use the legacy scp protocol for Windows guests, because the Win32-OpenSSH SFTP server truncates scp downloads at 200 KiB.
- OpenSSH is installed from Microsoft's Win32-OpenSSH release on GitHub, with the Windows optional feature as the fallback.
- Activation is left to Windows. Do not make a test depend on the activation state.

### Windows template

`vmctl.sh win-template <vm>` saves a VM's disk (flattened and compressed), firmware variables and TPM state to `$SYX_VM_HOME/images/windows11/`. After that, `create <vm> windows11` makes a copy-on-write clone that boots isolated in about a minute, and `rebuild` returns to the template in seconds.

- `SYX_WIN_FRESH=1` forces a fresh install.
- `prune` never deletes the template.
- Delete `template.*` to rebuild it, for example after importing a newer ISO.
- `selftest.sh windows11` saves the template on its first run and clones it afterwards.

## Networking

| Mode | Guest can reach | Use for |
|---|---|---|
| `isolated` (default) | Nothing outside the guest. SSH is a forward from `127.0.0.1:<port>` on the host. | All escape and destructive tests. |
| `nat` (`--net=nat`) | The internet, and the host's loopback services and LAN through QEMU's `10.0.2.2` gateway. | Provisioning, and the egress-firewall test in `suite.sh`. Start, install, stop, then boot isolated for tests. |

Never run escape tests in `nat` mode. SSH ports come from 2200 to 2299 and are bound to 127.0.0.1.

## Resources

- Base images are 0.3 to 0.7 GB each; all five are about 2.5 GB.
- Overlays start at a few tens of MB and grow with what the test writes. `rebuild` and `destroy` free the space; run `prune` after upgrading images.
- A VM takes its `mem` value while running (2 GB by default). Check `doctor` for available memory before starting several.

## Keeping images current

`fetch` reads each upstream checksum file and downloads an image only if its hash is new. Existing VMs keep the version they were created from; `rebuild` moves a VM to the newest base.

The image and its checksum come from the same vendor over HTTPS. The checksum file is not verified against the vendor's GPG signature.

## Other operating systems

- **Windows 11**: see above. ReactOS is not used.
- **macOS**: cannot run under KVM on x86; Apple's licence allows macOS VMs only on Apple hardware. Options:
  1. A Mac on the network as a second test host, with [Tart](https://tart.run) or UTM driven over SSH by a wrapper like `vmctl.sh`. Apple's licence allows two macOS VMs per Mac.
  2. GitHub-hosted macOS runners (`runs-on: macos-latest`) for tests that do not need a throwaway VM.
  3. A rented Mac (AWS EC2 Mac dedicated hosts have a 24-hour minimum; MacStadium rents by the month).

  The Syntaxis server needs Linux and gVisor, so on a Mac only the Go and web build, the CLI and the Docker Desktop or Colima route can be tested, not the sandbox escape tests.

## Rules for test code

- Run every escape or destructive test inside a VM, never on the host. Confirm with `vmctl.sh ssh <vm> hostname` first.
- Start from `rebuild` or a fresh `create`. Do not reuse a VM after a test that broke it.
- Keep private values (LAN addresses, keys, model endpoints) out of this directory and out of commits. Use `$SYX_VM_HOME` or `~/.config`.
- Do not put a binary image, VM disk or ISO in the repository.
