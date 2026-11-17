# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus headless Chromium and the usual browser and web test
# tools (large). Build it on the Docker host, then approve it in the admin app (Environments):
#   docker build -t syntaxis-env-browser:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/browser
#
# Browser tests: Playwright (and its test runner) and Puppeteer, using the same headless Chromium;
# axe-core for accessibility checks; Jest, Vitest and Mocha for unit tests; and pytest with
# pytest-playwright for Python. Not included: Cypress and Lighthouse, which need a full Chrome or
# Electron that does not start under gVisor.
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers PUPPETEER_SKIP_DOWNLOAD=1
# Python's playwright command is kept as playwright-py so the Node one (the test runner) is "playwright"
RUN pip3 install --break-system-packages --no-cache-dir pytest pytest-playwright pytest-xdist \
    && mv /usr/local/bin/playwright /usr/local/bin/playwright-py \
    && npm install -g --no-audit --no-fund @playwright/test @axe-core/playwright puppeteer \
      jest vitest mocha \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* && npm cache clean --force
# Chromium's own sandbox cannot start inside gVisor, and gVisor is the sandbox here. Each browser
# binary is wrapped so it always starts with --no-sandbox, which lets Playwright and Puppeteer
# work unmodified. Only the headless shell runs under gVisor (full Chrome and tools that need it,
# such as Lighthouse, do not); Puppeteer needs {headless: "shell"}.
RUN for b in $(find /opt/pw-browsers -type f \( -name chrome -o -name chrome-headless-shell \)); do \
      mv "$b" "$b.real" && printf '#!/bin/sh\nexec "%s.real" --no-sandbox --disable-dev-shm-usage --disable-crash-reporter --disable-breakpad "$@"\n' "$b" > "$b" && chmod 755 "$b"; \
    done \
    && chmod -R a+rX /opt/pw-browsers \
    && ln -s "$(find /opt/pw-browsers -type f -name chrome-headless-shell | head -n 1)" /usr/local/bin/chromium
ENV NODE_PATH=/usr/local/lib/node_modules PUPPETEER_EXECUTABLE_PATH=/usr/local/bin/chromium \
    XDG_CACHE_HOME=/tmp/cache
USER 10001:10001
