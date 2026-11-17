# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus Haskell (GHC, Cabal, Stack). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-haskell:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/haskell
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends ghc cabal-install haskell-stack libgmp-dev zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
ENV CABAL_DIR=/tmp/cabal STACK_ROOT=/tmp/stack
USER 10001:10001
