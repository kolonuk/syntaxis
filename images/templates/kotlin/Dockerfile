# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus Kotlin and a JDK. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-kotlin:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/kotlin
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ARG KOTLIN_VERSION=2.4.21
RUN apt-get update && apt-get install -y --no-install-recommends openjdk-17-jdk-headless maven \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL "https://github.com/JetBrains/kotlin/releases/download/v${KOTLIN_VERSION}/kotlin-compiler-${KOTLIN_VERSION}.zip" -o /tmp/kotlin.zip \
    && unzip -q /tmp/kotlin.zip -d /opt && rm /tmp/kotlin.zip \
    && ln -s /opt/kotlinc/bin/kotlinc /usr/local/bin/kotlinc && ln -s /opt/kotlinc/bin/kotlin /usr/local/bin/kotlin
# Gradle projects bring their own wrapper; its downloads and caches go under /tmp
ENV GRADLE_USER_HOME=/tmp/gradle MAVEN_OPTS="-Dmaven.repo.local=/tmp/m2"
USER 10001:10001
