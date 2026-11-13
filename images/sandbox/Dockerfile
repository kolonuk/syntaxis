# SPDX-License-Identifier: GPL-2.0-or-later
# Default sandbox image: a small dev environment. Build:
#   docker build -t syntaxis-sandbox:latest images/sandbox
# Supply your own image by setting SYNTAXIS_SANDBOX_IMAGE (it must have a
# non-root user with uid 10001, git, and a POSIX shell; ripgrep and curl are
# used by agent tools).
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client curl wget jq ripgrep build-essential pkg-config \
      python3 python3-pip python3-venv nodejs npm golang-go unzip zip less procps \
    && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 dev && useradd --uid 10001 --gid 10001 --home-dir /home/dev --shell /bin/bash dev \
    && mkdir -p /workspace && chown 10001:10001 /workspace
ENV HOME=/home/dev GOPATH=/workspace/.go GOCACHE=/tmp/go-build NPM_CONFIG_CACHE=/tmp/npm PIP_CACHE_DIR=/tmp/pip
USER 10001:10001
WORKDIR /workspace
CMD ["sleep", "infinity"]
