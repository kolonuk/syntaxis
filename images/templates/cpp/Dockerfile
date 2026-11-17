# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus C and C++ tooling (clang, CMake, Ninja, gdb, valgrind). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-cpp:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/cpp
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends clang clang-format clang-tidy cmake ninja-build gdb valgrind ccache \
    && rm -rf /var/lib/apt/lists/*
ENV CCACHE_DIR=/tmp/ccache
USER 10001:10001
