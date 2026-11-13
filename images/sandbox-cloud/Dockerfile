# SPDX-License-Identifier: GPL-2.0-or-later
# Cloud-tools variant (separate tag so the default image stays small):
#   docker build -t syntaxis-sandbox:cloud --build-arg BASE=syntaxis-sandbox:latest images/sandbox-cloud
# Adds: gh, kubectl, helm, terraform/opentofu, flyctl, wrangler, gcloud, aws, az.
# Versions are installed from vendor repositories at build time; pin them in
# your own fork if you need reproducible builds. Credentials are never baked in:
# users supply their own via the secrets vault.
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends gnupg apt-transport-https lsb-release \
    && install -d -m 0755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/github.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/github.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github.list \
    && curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /etc/apt/keyrings/google.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" > /etc/apt/sources.list.d/google-cloud-sdk.list \
    && apt-get update && apt-get install -y --no-install-recommends gh google-cloud-cli \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g wrangler @cloudflare/cli 2>/dev/null || npm install -g wrangler
RUN pip3 install --break-system-packages --no-cache-dir awscli azure-cli
RUN curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl && chmod +x /usr/local/bin/kubectl \
    && curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash \
    && curl -fsSL https://get.opentofu.org/install-opentofu.sh | sh -s -- --install-method standalone \
    && curl -fsSL https://fly.io/install.sh | FLYCTL_INSTALL=/usr/local sh
USER 10001:10001
