# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus a Rust toolchain. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-rust:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/rust
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo PATH=/usr/local/cargo/bin:$PATH
RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --no-modify-path \
    && chmod -R a+rX /usr/local/rustup /usr/local/cargo
# cargo writes its cache under the home directory, which is a throwaway tmpfs in the sandbox
ENV CARGO_HOME=/tmp/cargo CARGO_INSTALL_ROOT=/tmp/cargo
USER 10001:10001
