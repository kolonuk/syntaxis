#!/bin/sh
# Runs govulncheck and fails on any finding EXCEPT advisories in the Docker
# engine module that have no fixed release (they concern the daemon, not the
# client SDK Syntaxis links; see docs/security.md).
set -eu
go install golang.org/x/vuln/cmd/govulncheck@latest
"$(go env GOPATH)/bin/govulncheck" -format json ./... > /tmp/vuln.json || true
bad=$(jq -r 'select(.finding != null) | .finding | select(.fixed_version != null or (.trace[0].module != "github.com/docker/docker")) | .osv' /tmp/vuln.json | sort -u)
if [ -n "$bad" ]; then echo "fixable or non-Docker vulnerabilities: $bad"; exit 1; fi
echo "govulncheck: only known unfixed Docker-engine advisories"
