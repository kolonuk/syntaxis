# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus the Swift toolchain (large). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-swift:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/swift
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ARG SWIFT_VERSION=6.4.2
RUN apt-get update && apt-get install -y --no-install-recommends binutils libc6-dev libcurl4-openssl-dev libedit2 libgcc-s1 \
      libpython3-dev libsqlite3-0 libstdc++-12-dev libxml2-dev libncurses-dev libz3-dev pkg-config tzdata zlib1g-dev \
    && rm -rf /var/lib/apt/lists/* \
    && case "$(uname -m)" in x86_64) dir=debian12; file=debian12 ;; aarch64) dir=debian12-aarch64; file=debian12-aarch64 ;; esac \
    && curl -fsSL "https://download.swift.org/swift-${SWIFT_VERSION}-release/${dir}/swift-${SWIFT_VERSION}-RELEASE/swift-${SWIFT_VERSION}-RELEASE-${file}.tar.gz" -o /tmp/swift.tgz \
    && tar -xzf /tmp/swift.tgz -C / --strip-components=1 && rm /tmp/swift.tgz
ENV CLANG_MODULE_CACHE_PATH=/tmp/clang-modules SWIFTPM_CACHE_PATH=/tmp/swiftpm
USER 10001:10001
