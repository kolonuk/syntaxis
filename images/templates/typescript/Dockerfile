# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus a TypeScript toolchain (tsc, tsx, ts-node, ESLint, Prettier, Vitest, pnpm, Yarn). Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-typescript:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/typescript
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN npm install -g --no-audit --no-fund typescript tsx ts-node eslint prettier vitest pnpm yarn @types/node \
    && npm cache clean --force
# package managers keep their stores under /tmp, as the root filesystem is read-only
ENV PNPM_HOME=/tmp/pnpm COREPACK_HOME=/tmp/corepack YARN_CACHE_FOLDER=/tmp/yarn PATH=/tmp/pnpm:$PATH
USER 10001:10001
