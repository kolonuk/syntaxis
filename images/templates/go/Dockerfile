# SPDX-License-Identifier: GPL-2.0-or-later
# Example environment: the sandbox image plus common Go tooling (gopls, delve, staticcheck,
# golangci-lint, govulncheck, goimports). The Go toolchain itself is already in the default image.
# Build it on the Docker host, then approve it in the admin app (Environments):
#   docker build -t syntaxis-env-go:1 --build-arg BASE=syntaxis-sandbox:latest images/templates/go
ARG BASE=syntaxis-sandbox:latest
FROM ${BASE}
USER 0
ENV GOBIN=/usr/local/bin GOPATH=/tmp/gopath GOCACHE=/tmp/gocache
RUN for p in golang.org/x/tools/gopls golang.org/x/tools/cmd/goimports github.com/go-delve/delve/cmd/dlv \
             honnef.co/go/tools/cmd/staticcheck golang.org/x/vuln/cmd/govulncheck; do go install "$p@latest"; done \
    && curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin \
    && rm -rf /tmp/gopath /tmp/gocache
# the caches must be writable by the sandbox user, whose home is a throwaway; the base image already
# points GOPATH at the workspace, so put it back after building
ENV GOPATH=/workspace/.go GOCACHE=/tmp/go-build
USER 10001:10001
