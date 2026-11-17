# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus Ruby and Bundler. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-ruby:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/ruby
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends ruby-full ruby-dev bundler \
    && rm -rf /var/lib/apt/lists/*
# gems install under the workspace-independent temp area, which is writable in the sandbox
ENV GEM_HOME=/tmp/gems BUNDLE_PATH=/tmp/gems PATH=/tmp/gems/bin:$PATH
USER 10001:10001
