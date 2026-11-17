# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus a JDK and Maven. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-java:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/java
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends openjdk-17-jdk-headless maven \
    && rm -rf /var/lib/apt/lists/*
ENV MAVEN_OPTS="-Dmaven.repo.local=/tmp/m2"
USER 10001:10001
