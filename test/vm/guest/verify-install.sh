#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Runs inside a test VM after scripts/install.sh, from the repository checkout.
# Mirrors the "Verify the result" step of the CI install job, plus a re-run of
# the installer. Prints PASS or FAIL per check; exit status is the failure count.
# Usage: sh verify-install.sh <install.log> <egress-block-host>
LOG=${1:-install.log}
BLOCK=${2:-192.0.2.10}
fails=0
ok() { echo "PASS $*"; }
bad() { echo "FAIL $*"; fails=$((fails + 1)); }
check() { d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi; }

check "healthz answers" curl -fsS http://127.0.0.1:8080/healthz
check "single-use admin link printed" grep -q '#setup=' "$LOG"
for u in syntaxis-egress.service syntaxis-egress.path syntaxis-egress.timer syntaxis-firewall-sync.service syntaxis-update-agent.service; do
	check "$u enabled" systemctl is-enabled "$u"
done
check "egress nft table has an input hook" sh -c "sudo nft list table inet syntaxis_sandbox | grep -q 'hook input'"
check ".env blocks the LLM host" sudo grep -q "^SYNTAXIS_EGRESS_BLOCK=$BLOCK/32" .env
check ".env is mode 600" test "$(stat -c %a .env)" = 600
check "runsc is a registered Docker runtime" sh -c "sudo docker info --format '{{json .Runtimes}}' | grep -q runsc"

sudo docker compose exec -T syntaxis syntaxis doctor >doctor.txt 2>&1
cat doctor.txt
for c in sandbox.network-rules sandbox.socket-proxy sandbox.runsc; do
	if grep -q "$c" doctor.txt; then bad "doctor reports $c"; else ok "doctor is clear of $c"; fi
done

key=$(sudo grep '^SYNTAXIS_MASTER_KEY=' .env)
sudo sh scripts/install.sh --yes --url http://localhost:8080 --version "${SYX_INSTALL_VERSION:-v0.1.2}" >reinstall.log 2>&1
check "re-running the installer succeeds" test -s reinstall.log
check "re-run keeps the master key" test "$key" = "$(sudo grep '^SYNTAXIS_MASTER_KEY=' .env)"
check "healthz still answers after the re-run" curl -fsS http://127.0.0.1:8080/healthz

[ "$fails" -gt 100 ] && fails=100
exit "$fails"
