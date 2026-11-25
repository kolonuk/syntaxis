# The test VM harness: QEMU, the firmware, TPM emulator, SSH and the Go toolchain the suite copies
# into guests. Build and run it with ./vm.sh; nothing here is installed on the host.
FROM golang:1.27 AS go

FROM debian:12-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		bash ca-certificates curl git iproute2 jq openssh-client procps python3 util-linux \
		qemu-system-x86 qemu-utils cloud-image-utils genisoimage ovmf swtpm socat \
	&& rm -rf /var/lib/apt/lists/*
RUN git config --system safe.directory /repo
COPY --from=go /usr/local/go /usr/local/go
ENV SYX_GO_SDK=/usr/local/go \
	SYX_VM_HOME=/data \
	SYX_REPO=/repo \
	HOME=/data/home
WORKDIR /repo
