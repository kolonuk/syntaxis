# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus a headless Chromium for browser testing (Playwright; large). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-browser:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/browser
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers
RUN npm install -g --no-audit --no-fund playwright \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* && npm cache clean --force && chmod -R a+rX /opt/pw-browsers
# Chromium's own sandbox does not start inside gVisor; gVisor is the sandbox here, so launch with
# --no-sandbox (Playwright: chromium.launch({ args: ["--no-sandbox"] })).
ENV NODE_PATH=/usr/local/lib/node_modules
USER 10001:10001
