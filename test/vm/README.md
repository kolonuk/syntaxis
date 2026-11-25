# Testing Syntaxis in virtual machines

## What this is for

Some Syntaxis tests are dangerous or change the whole machine: installing Docker and a firewall, trying to escape a sandbox, updating a running install, breaking an update on purpose. They must not run on your own computer.

These scripts create a throwaway virtual machine (VM), run such a test inside it, and delete the VM afterwards. A VM that goes wrong costs nothing; it is rebuilt in seconds.

The VM tooling (QEMU and its helpers) runs inside a Docker container, so the only things your machine needs are Docker and KVM.

The scripts run three tests:

| Test | Script | What it proves |
|---|---|---|
| Install and security suite | `suite.sh` | Syntaxis installs on each supported Linux distribution, and sandboxes cannot reach your network or the host. |
| Upgrade test | `upgrade-test.sh` | An older install updates to a newer release through the admin app, and a failed update rolls back. |
| Self-test | `selftest.sh` | The VM scripts themselves work. Only needed after changing them. |

## What you need

- A Linux machine with Docker and hardware virtualisation (KVM). `ls /dev/kvm` must show a file.
- About 30 GB of free disk and 8 GB of free memory for one test; more for several at once.
- An internet connection.
- A checkout of this repository.

## Set up (once)

1. Build the harness image (about a minute):

   ```sh
   test/vm/vm.sh build
   ```

2. Check the container can use KVM. Every line must say `ok`:

   ```sh
   test/vm/vm.sh vmctl.sh doctor
   ```

3. Download the base VM images (about 2.5 GB, one per distribution):

   ```sh
   test/vm/vm.sh vmctl.sh fetch
   ```

### How `vm.sh` works

`test/vm/vm.sh <script> [arguments]` runs one of the scripts in this folder inside a container named `syntaxis-vm`, starting it if needed. The container mounts this checkout read-only at `/repo`. Every `SYX_*` environment variable you set is passed through.

- `vm.sh shell` opens a shell in the container.
- `vm.sh down` stops the container. This also stops every running VM.
- `vm.sh build` rebuilds the image.

Everything the scripts create (base images, VM disks, SSH key, results) is stored in the Docker volume `syntaxis-vm-data`, mounted at `/data` in the container. The volume keeps the downloaded images, kept VMs and the Windows template across container restarts, so none of them is downloaded or built again. Nothing is stored in the repository. To use a different volume, or a folder on the host, set `SYX_VM_DATA` to a volume name or an absolute path.

| Path in the volume | Contents |
|---|---|
| `images/` | Downloaded base images. |
| `vms/` | One folder per VM: its disk, console log and settings. |
| `keys/` | The SSH key used to log in to the VMs. Generated, never committed. |
| `results/` | Logs from each test run. |

## Run the install and security suite

```sh
test/vm/vm.sh suite.sh                 # every distribution, about 45 minutes
test/vm/vm.sh suite.sh debian12        # one distribution
test/vm/vm.sh suite.sh --keep debian12 # keep the VM afterwards
```

Distributions: `debian12`, `debian13`, `ubuntu2404`, `rocky9`, `alma9` (listed in `distros.conf`).

For each distribution the suite:

1. Creates a fresh VM with internet access, copies the repository in and runs `scripts/install.sh`, as a user would.
2. Checks the install: health, services, firewall, `.env`, `syntaxis doctor`. Runs the installer again and checks the master key is kept.
3. Reboots the VM with no network and runs the sandbox, Docker proxy, egress, doctor and policy tests against the real Docker and gVisor.
4. Reboots with internet access and runs the egress test, which proves sandboxes cannot reach the LAN, the host, cloud metadata or SMTP.

To test a published release instead of building from source, set the release tag:

```sh
SYX_INSTALL_VERSION=v0.2.0 test/vm/vm.sh suite.sh debian12
```

Other settings: `SYX_SUITE_PARALLEL` (VMs at once, default 2), `SYX_SUITE_MEM` (MB per VM, default 4096).

## Run the upgrade test

```sh
test/vm/vm.sh upgrade-test.sh              # default distribution
test/vm/vm.sh upgrade-test.sh rocky9       # a named distribution
```

It installs an old release, then updates it through the admin app:

1. Installs `v0.1.3` and sets up the admin account.
2. Updates to `edge` (the development branch) with the old server and old update service.
3. Checks the new version lists the three release streams, and that a stream request is refused with instructions while the old update service is running.
4. Restarts the update service, switches to `preview`, and checks the weekly automatic update leaves a current install alone.
5. Breaks `main` on purpose, asks for an update, and checks the previous version, `.env`, checkout and database come back and the admin app reports the failure.

To check a published release before announcing it, update to that release and stop:

```sh
SYX_UPGRADE_TO=v0.2.0 test/vm/vm.sh upgrade-test.sh debian12
```

Settings: `SYX_UPGRADE_FROM` (release to start from, default `v0.1.3`), `SYX_KEEP=1` (keep the VM), `SYX_SUITE_MEM` (MB, default 6144).

The guest calls the GitHub API. GitHub limits unauthenticated requests per address, so a run can fail with "GitHub is limiting requests from this address". Wait a few minutes and run it again.

## Read the results

Each run prints `PASS` or `FAIL` for every step and ends with a summary. Copy the logs out of the volume with:

```sh
test/vm/vm.sh results ./results
```

- All steps passed: the VM is deleted.
- Any step failed: the VM is kept. Log in with `test/vm/vm.sh vmctl.sh ssh <vm-name>`, look around, then delete it with `test/vm/vm.sh vmctl.sh destroy <vm-name>`. The VM name is in the output.

## Run the self-test

Run this after changing `vmctl.sh`:

```sh
test/vm/vm.sh selftest.sh              # all distributions, about 15 minutes
test/vm/vm.sh selftest.sh debian12     # one
```

## Commands

`vmctl.sh` manages the VMs directly. Use it to try something by hand, as `test/vm/vm.sh vmctl.sh <command>`.

| Command | What it does |
|---|---|
| `doctor` | Check KVM and the tools are available. |
| `fetch [distro...]` | Download base images, only when upstream has a new one. |
| `create <vm> <distro> [cpus] [mem-MB] [disk]` | Make a VM. Defaults: 2 cpus, 2048 MB, 20G. |
| `start <vm> [--net=nat]` | Boot it and wait for SSH. |
| `ssh <vm> [command...]` | Open a shell, or run one command, as user `tester`. |
| `put <vm> <path-in-container> <remote>` / `get` | Copy a file in or out. Paths are inside the container; `/data` is the volume. |
| `stop <vm>` | Shut it down. |
| `rebuild <vm>` | Wipe it back to a clean base image and boot. |
| `destroy <vm>` | Delete it. |
| `list` | Show images and VMs. |
| `prune` | Delete base images no VM uses. |

Example:

```sh
test/vm/vm.sh vmctl.sh create esc-1 debian12
test/vm/vm.sh vmctl.sh start esc-1
test/vm/vm.sh vmctl.sh put esc-1 /repo/test/vm/guest/verify-install.sh /home/tester/
test/vm/vm.sh vmctl.sh ssh esc-1 'ls /home/tester'
test/vm/vm.sh vmctl.sh destroy esc-1
```

### Networking

| Mode | The VM can reach | Use for |
|---|---|---|
| `isolated` (default) | Nothing outside itself. | Every escape or destructive test. |
| `nat` (`--net=nat`) | The internet, and the container's network. | Installing packages and the egress test. |

Start a VM in `nat` mode to install, stop it, then start it isolated to test. Never run an escape test in `nat` mode.

### Adding a distribution

Add a line to `distros.conf`: `name|image URL|checksum URL|file name`, then run `vm.sh vmctl.sh fetch <name>`.

### Updating images

`fetch` downloads a base image only when its upstream checksum changed. Existing VMs keep the image they were made from; `rebuild` moves a VM to the newest one. Run `prune` afterwards to free disk.

## Windows 11

A Windows VM is only needed to test Windows support. It installs from a Windows 11 ISO with an unattended answer file and no product key.

1. Download the Windows 11 ISO (x64, multi-edition) from Microsoft in a browser.
2. Put it in a folder and set `SYX_ISO_DIR` to that folder. It is mounted read-only at `/isos` in the container, so the ISO is never copied. Stop the container if it is running, so it starts with the folder mounted:

   ```sh
   test/vm/vm.sh down
   export SYX_ISO_DIR=~/isos
   ```

3. Create and start the VM (the install takes 15 to 40 minutes):

   ```sh
   test/vm/vm.sh vmctl.sh create win-1 windows11
   test/vm/vm.sh vmctl.sh start win-1
   test/vm/vm.sh vmctl.sh screenshot win-1 /data/win.png
   docker cp syntaxis-vm:/data/win.png .     # to watch progress
   ```

4. When the install finishes, stop it and start it again. It now boots isolated:

   ```sh
   test/vm/vm.sh vmctl.sh stop win-1
   test/vm/vm.sh vmctl.sh start win-1
   test/vm/vm.sh vmctl.sh ssh win-1 'ver'
   ```

Notes:

- The VM needs 4 GB of memory and a 64 GB disk image (about 15 GB used). Defaults: 4 cpus, 4096 MB, 64G.
- The first boot uses `nat` because the install downloads OpenSSH. Treat it as setup, not a test.
- Edition: `Windows 11 Pro`. Set `SYX_WIN_EDITION` to change it.
- `tester` is a local administrator. SSH uses the key only.
- `put` and `get` use the legacy scp protocol for Windows, because the Windows SFTP server truncates downloads at 200 KiB.
- `rebuild` reinstalls from the ISO (15 minutes) unless you save a template.

### Template

A template makes new Windows VMs start in about a minute instead of installing.

```sh
test/vm/vm.sh vmctl.sh win-template win-1     # save the installed VM as the template
test/vm/vm.sh vmctl.sh create win-2 windows11 # now a clone of the template
```

`rebuild` returns a VM to the template in seconds. `SYX_WIN_FRESH=1` forces a fresh install. Delete `/data/images/windows11/template.*` in the container (`vm.sh shell`) to rebuild the template, for example after importing a newer ISO.

`test/vm/vm.sh selftest.sh windows11` runs the Windows checks (edition, no product key in the answer file, TPM 2.0, Secure Boot, isolation, file copy). It saves the template on its first run.

## Rules for test code

- Run every escape or destructive test inside a VM, never in the container. Run `vm.sh vmctl.sh ssh <vm> hostname` first to confirm where you are.
- Start from `rebuild` or a fresh `create`. Do not reuse a VM after a test that broke it.
- Do not edit `test/vm` while a run is in progress.
- Keep private values (LAN addresses, keys, model endpoints) out of this directory and out of commits.
- Do not put a binary image, VM disk or ISO in the repository.
