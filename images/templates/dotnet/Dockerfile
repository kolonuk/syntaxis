# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus the .NET SDK. Build it on the Docker host, then
# approve it in the admin app (Environments):
#   docker build -t syntaxis-env-dotnet:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/dotnet
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
RUN apt-get update && apt-get install -y --no-install-recommends libicu72 libssl3 \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh \
    && bash /tmp/dotnet-install.sh --channel 8.0 --install-dir /usr/share/dotnet \
    && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet && rm /tmp/dotnet-install.sh
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_CLI_HOME=/tmp/dotnet NUGET_PACKAGES=/tmp/nuget
USER 10001:10001
