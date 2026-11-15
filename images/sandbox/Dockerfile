# SPDX-License-Identifier: GPL-2.0-or-later
# Default sandbox image: a small dev environment. Build:
#   docker build -t syntaxis-sandbox:latest images/sandbox
# Supply your own image by setting SYNTAXIS_SANDBOX_IMAGE (it must have a
# non-root user with uid 10001, git, and a POSIX shell; ripgrep and curl are
# used by agent tools). Node and Go come from the official images so they are
# current (many tools, including modern MCP servers, need Node 20+).
FROM node:26-bookworm-slim@sha256:5df79989ed685e09f0a98019db4cebfb22fb9df1a53c0161664a011f7ff6b0f1 AS node
FROM golang:1.26-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS golang

FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client curl wget jq ripgrep build-essential pkg-config \
      python3 python3-pip python3-venv python-is-python3 unzip zip less procps \
    && pip3 install --break-system-packages --no-cache-dir uv \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/ /usr/local/
COPY --from=golang /usr/local/go /usr/local/go
RUN groupadd --gid 10001 dev && useradd --uid 10001 --gid 10001 --home-dir /home/dev --shell /bin/bash dev \
    && mkdir -p /workspace && chown 10001:10001 /workspace
ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/home/dev GOPATH=/workspace/.go GOCACHE=/tmp/go-build GOTOOLCHAIN=local \
    NPM_CONFIG_CACHE=/tmp/npm PIP_CACHE_DIR=/tmp/pip UV_CACHE_DIR=/tmp/uv PIP_BREAK_SYSTEM_PACKAGES=1
USER 10001:10001
WORKDIR /workspace
CMD ["sleep", "infinity"]
