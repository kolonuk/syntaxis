# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus Elixir and Erlang. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-elixir:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/elixir
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends elixir erlang-dev erlang-xmerl \
    && rm -rf /var/lib/apt/lists/*
ENV MIX_HOME=/tmp/mix HEX_HOME=/tmp/hex
USER 10001:10001
