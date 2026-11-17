# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus PHP and Composer. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-php:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/php
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends php-cli php-curl php-mbstring php-xml php-zip php-sqlite3 composer \
    && rm -rf /var/lib/apt/lists/*
ENV COMPOSER_HOME=/tmp/composer COMPOSER_CACHE_DIR=/tmp/composer-cache
USER 10001:10001
