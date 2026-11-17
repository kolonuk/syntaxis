# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus Zig. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-zig:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/zig
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ARG ZIG_VERSION=0.17.1
# the download is checked against the checksum the Zig project publishes for it
RUN arch=$(uname -m) \
    && curl -fsSL https://ziglang.org/download/index.json -o /tmp/zig.json \
    && url=$(jq -r --arg v "$ZIG_VERSION" --arg a "$arch-linux" '.[$v][$a].tarball' /tmp/zig.json) \
    && sum=$(jq -r --arg v "$ZIG_VERSION" --arg a "$arch-linux" '.[$v][$a].shasum' /tmp/zig.json) \
    && curl -fsSL "$url" -o /tmp/zig.tar.xz \
    && echo "$sum  /tmp/zig.tar.xz" | sha256sum -c - \
    && mkdir /opt/zig && tar -xJf /tmp/zig.tar.xz -C /opt/zig --strip-components=1 \
    && ln -s /opt/zig/zig /usr/local/bin/zig && rm /tmp/zig.tar.xz /tmp/zig.json
ENV ZIG_GLOBAL_CACHE_DIR=/tmp/zig-global ZIG_LOCAL_CACHE_DIR=/tmp/zig-local
USER 10001:10001
