# SPDX-License-Identifier: GPL-2.0-or-later
# Optional security-tools variant: Trivy and osv-scanner, run ON DEMAND only
# ("Scan project" button / agent tool). Not part of the base image because of
# size and vulnerability-DB downloads.
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER root
RUN curl -fsSL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sh -s -- -b /usr/local/bin \
    && curl -fsSL https://github.com/google/osv-scanner/releases/latest/download/osv-scanner_linux_amd64 -o /usr/local/bin/osv-scanner \
    && chmod +x /usr/local/bin/osv-scanner
USER 10001:10001
