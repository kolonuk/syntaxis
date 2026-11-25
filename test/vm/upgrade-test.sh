#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# upgrade-test.sh [distro] - upgrade a real install through the admin app, in a fresh VM.
#
# What it does, in a VM with internet (nat), as a user would:
#   1. install an OLD release (SYX_UPGRADE_FROM, default v0.1.3) with the installer, from a git
#      checkout of that tag, and set up the admin (single-use link, password, 2FA)
#   2. through the admin app's API, ask for the development branch (edge): the OLD server, the OLD
#      host update service and the OLD update script do the update to the checkout's main branch,
#      built from source. This is the upgrade path a real instance takes.
#   3. the NEW server refuses a request that names a stream while the old host service is still
#      running, and says how to restart it; after the restart the same request is accepted
#   4. switch stream (preview), check what was recorded, and that the weekly timer's forward-only
#      rule leaves it alone
#   5. roll back: make main a commit that cannot start, ask for edge through the admin app, and
#      check that the previous version, its .env, the checkout and the database come back and the
#      admin app reports the failure
# The git checkout's origin is a local repository made from a bundle of THIS working tree's
# committed state, so what is tested is the code on disk, not what has been pushed. Results go to
# $SYX_VM_HOME/results/upgrade-<timestamp>/. The VM is destroyed when everything passes.
#
# Environment: SYX_UPGRADE_TO (a published tag: update to it through the admin app and stop, instead of the
# edge/stream/rollback run), SYX_UPGRADE_FROM (a published tag, default v0.1.3), SYX_SUITE_MEM (MB, 6144),
# SYX_KEEP=1 (keep the VM).
set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=${SYX_REPO:-$(cd "$HERE/../.." && pwd)}
VMCTL=$HERE/vmctl.sh
HOME_DIR=${SYX_VM_HOME:-$HOME/.local/share/syntaxis-vm}
FROM=${SYX_UPGRADE_FROM:-v0.1.3}
MEM=${SYX_SUITE_MEM:-6144}
distro=${1:-debian12}
name=upgrade-$distro
out=$HOME_DIR/results/upgrade-$(date +%Y%m%d-%H%M%S)
mkdir -p "$out"
steps=$out/steps.txt
# a throwaway password, made fresh for each run
PASSWORD="Syx-$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')-2026!"
fails=0

ok() { echo "PASS $*" | tee -a "$steps"; }
bad() { echo "FAIL $*" | tee -a "$steps"; fails=$((fails + 1)); }
check() { local d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi; }

gx() { "$VMCTL" ssh "$name" "$@"; }
# api <adminapi.py args>: each argument is quoted for the guest's shell
api() {
	local a="" x
	for x in "$@"; do a+=" $(printf '%q' "$x")"; done
	gx "python3 ~/adminapi.py$a"
}
info() { api get /update; }
info_is() { info 2>/dev/null | jq -e "$1" >/dev/null; }
version() { gx 'curl -fsS http://127.0.0.1:8080/api/version' 2>/dev/null | tr -d '\r'; }
envval() { gx "sudo sed -n 's/^$1=//p' /opt/syntaxis/.env | tail -n 1" 2>/dev/null | tr -d '\r'; }
branch() { gx "sudo git -c safe.directory='*' -C /opt/syntaxis rev-parse --abbrev-ref HEAD" 2>/dev/null | tr -d '\r'; }
healthy() { gx 'curl -fsS http://127.0.0.1:8080/healthz'; }
rev() { gx "sudo git -c safe.directory='*' -C /opt/syntaxis rev-parse HEAD" 2>/dev/null | tr -d '\r'; }
# request_update <json>: ask for an update, trying again while GitHub is not answering (it limits
# unauthenticated requests per address, and a test run shares its address with everything else)
request_update() {
	local out
	for _ in 1 2 3 4 5; do
		out=$(api post /update "$1" 2>/dev/null)
		if ! grep -q "could not look up the releases" <<<"$out"; then echo "$out"; return 0; fi
		sleep 70
	done
	echo "$out"
}
admin_sees_result() { api login admin@example.com "$PASSWORD" >/dev/null && info_is '.status.state == "failed" or .status.state == "unknown"'; }
status_msg() { info 2>/dev/null | jq -r '.status | "\(.state) \(.target // "") \(.message // "")"' 2>/dev/null; }

# wait_update [seconds]: until the last update request has finished. 0 done, 1 failed, 2 timed out,
# 3 finished but the host service never reported how it went (an older service loses its last report
# when the server restarts, as a rollback does; this server then says "unknown" instead of "running").
wait_update() {
	local deadline=$((SECONDS + ${1:-2400})) st
	while [ "$SECONDS" -lt "$deadline" ]; do
		st=$(info 2>/dev/null | jq -r '.status.state // "unknown"' 2>/dev/null || echo unreachable)
		case $st in
			done) return 0 ;;
			failed) return 1 ;;
			unknown) return 3 ;;
		esac
		sleep 15
	done
	return 2
}

cleanup() {
	if [ "$fails" -eq 0 ] && [ "${SYX_KEEP:-0}" != 1 ]; then "$VMCTL" destroy "$name" >/dev/null 2>&1; else echo "VM $name kept for inspection ($out)"; fi
}
trap cleanup EXIT

echo "== upgrade test on $distro, from $FROM ($(date +%T))"
"$VMCTL" create "$name" "$distro" 4 "$MEM" 40G >/dev/null && ok "create VM" || { bad "create VM"; exit 1; }
"$VMCTL" start "$name" --net=nat >/dev/null && ok "boot (nat, provisioning)" || { bad "boot"; exit 1; }

# the tools the checkout needs (cloud images are minimal)
gx 'if command -v apt-get >/dev/null; then sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git curl ca-certificates python3 jq >/dev/null; else sudo dnf install -y -q git curl python3 jq >/dev/null; fi' >/dev/null 2>&1
check "git, curl, python3 and jq are installed" gx 'command -v git curl python3 jq'

# origin is a local repository made from a bundle of this working tree's committed state
git -C "$REPO" bundle create "$out/syx.bundle" --all >/dev/null 2>&1
"$VMCTL" put "$name" "$out/syx.bundle" syx.bundle >/dev/null
"$VMCTL" put "$name" "$HERE/guest/adminapi.py" adminapi.py >/dev/null
check "the origin repository and the old checkout are made" gx "sudo rm -rf /opt/syx-origin.git /opt/syntaxis && sudo git clone -q --bare ~/syx.bundle /opt/syx-origin.git && sudo git clone -q /opt/syx-origin.git /opt/syntaxis && sudo git -C /opt/syntaxis checkout -q $FROM && sudo git -C /opt/syntaxis describe --tags"

echo "-- install $FROM ($(date +%T))"
gx "cd /opt/syntaxis && sudo sh scripts/install.sh --yes --url http://localhost:8080 --admin-email admin@example.com --version $FROM" >"$out/install.log" 2>&1
check "the installer installs $FROM" grep -q '#setup=' "$out/install.log"
tok=$(grep -o '#setup=[A-Za-z0-9_-]*' "$out/install.log" | head -1 | cut -d= -f2)
check "the admin setup link works" api setup "$tok" "$PASSWORD"
check "the admin signs in and sets up 2FA" api login admin@example.com "$PASSWORD"
v0=$(version)
echo "running: $v0"
check "the old version is running" grep -q "${FROM#v}" <<<"$v0"
check "the update service is running" info_is '.agent.running == true'

# --- 1b. SYX_UPGRADE_TO=<published tag>: update to that release through the admin app and stop ---
if [ -n "${SYX_UPGRADE_TO:-}" ]; then
	TO=$SYX_UPGRADE_TO
	echo "-- upgrade to the published release $TO ($(date +%T))"
	sleep 20
	request_update "{\"target\":\"$TO\"}" >"$out/release-request.json" 2>&1
	check "the old server accepts a request for $TO" jq -e ".target == \"$TO\"" "$out/release-request.json"
	wait_update 3000
	rc=$?
	if [ "$rc" -eq 0 ]; then ok "the update to $TO finished"; else bad "the update to $TO finished ($rc): $(status_msg)"; fi
	sleep 25
	v1=$(version)
	echo "running: $v1"
	check "the running version is $TO" grep -q "${TO#v}" <<<"$v1"
	check "the stack is healthy after the update" healthy
	check "the admin can sign in on the new version" api login admin@example.com "$PASSWORD"
	info >"$out/update-info.json" 2>&1
	check "the new version lists the three release streams" jq -e '(.streams | map(.id)) == ["stable","preview","edge"]' "$out/update-info.json"
	check "the update is recorded as done" jq -e '.status.state == "done"' "$out/update-info.json"
	check "the old host service has not announced a protocol" jq -e '.agent.protocol == 0' "$out/update-info.json"
	gx 'sudo systemctl restart syntaxis-update-agent' >/dev/null 2>&1
	sleep 25
	check "the restarted host service announces protocol 2" info_is '.agent.protocol == 2'
	check "the data survived: the admin account still exists" api login admin@example.com "$PASSWORD"
	echo
	if [ "$fails" -eq 0 ]; then echo "ALL PASSED"; else echo "$fails FAILED"; exit 1; fi
	exit 0
fi

# --- 2. upgrade to the development branch with the old server, service and script ---
echo "-- upgrade to edge through the admin app ($(date +%T))"
sleep 20
api post /update '{"target":"edge"}' >"$out/edge-request.json" 2>&1
check "the old server accepts a request for edge" jq -e '.target == "edge"' "$out/edge-request.json"
wait_update 3000
rc=$?
if [ "$rc" -eq 0 ]; then ok "the update to edge finished"; else bad "the update to edge finished ($rc): $(status_msg)"; fi
sleep 25
v1=$(version)
echo "running: $v1"
check "the version changed" test "$v0" != "$v1" -a -n "$v1"
check "the checkout is on main" test "$(branch)" = main
check "the stream is recorded as edge" test "$(envval SYNTAXIS_CHANNEL)" = edge
check "the stack is healthy after the update" healthy
check "the admin can sign in on the new version" api login admin@example.com "$PASSWORD"
info >"$out/update-info.json" 2>&1
check "the new version lists the three release streams" jq -e '(.streams | map(.id)) == ["stable","preview","edge"]' "$out/update-info.json"
check "the new version knows it follows edge" jq -e '.stream == "edge"' "$out/update-info.json"
check "the update is recorded as done" jq -e '.status.state == "done"' "$out/update-info.json"

# --- 3. the old host service cannot be told a stream ---
echo "-- a stream request against the old host service"
check "the old host service has not announced a protocol" jq -e '.agent.protocol == 0' "$out/update-info.json"
msg=$(request_update '{"target":"preview"}' | jq -r '.error // ""')
if grep -q "systemctl restart syntaxis-update-agent" <<<"$msg"; then ok "a stream request is refused with the way out"; else bad "a stream request is refused with the way out: $msg"; fi
gx 'sudo systemctl restart syntaxis-update-agent' >/dev/null 2>&1
sleep 25
check "the restarted host service announces protocol 2" info_is '.agent.protocol == 2'

# --- 4. switch stream ---
echo "-- switch to the preview stream ($(date +%T))"
request_update '{"target":"preview"}' >"$out/preview-request.json" 2>&1
check "the preview request is accepted" jq -e '.stream == "preview"' "$out/preview-request.json"
wait_update 2400
rc=$?
if [ "$rc" -eq 0 ]; then ok "the switch to preview finished: $(status_msg)"; else bad "the switch to preview finished ($rc): $(status_msg)"; fi
sleep 25
check "the stream is recorded as preview" test "$(envval SYNTAXIS_CHANNEL)" = preview
# (the app running now is the published release the stream pointed at, which has no streams of its own)
check "the stack is healthy after the switch" healthy
before=$(version)
gx 'cd /opt/syntaxis && sudo sh scripts/update.sh --auto' >"$out/auto.log" 2>&1
check "the weekly timer leaves a current install alone" test "$(version)" = "$before"
check "the host service was restarted by the update (a transient unit ran)" gx 'sudo journalctl -u syntaxis-update-agent-restart --no-pager 2>/dev/null | grep -q . || systemctl is-active syntaxis-update-agent'

# --- 5. a failed update rolls back, and the admin app says so ---
echo "-- a failed update rolls back ($(date +%T))"
api post /update '{"target":"edge"}' >/dev/null 2>&1
wait_update 3000
check "back on edge" healthy
# a published release's update script restarts the host service some seconds after an update; wait for that
# to finish so it cannot land in the update that follows
gx 'sleep 8; while systemctl is-active -q syntaxis-update-agent-restart.timer || systemctl is-active -q syntaxis-update-agent-restart.service; do sleep 2; done; sleep 10' >/dev/null 2>&1
good=$(version)
good_rev=$(rev)
good_env=$(gx 'sudo cat /opt/syntaxis/.env | grep -v "^#" | sort | md5sum' 2>/dev/null)
gx "cd /tmp && sudo rm -rf work && sudo git -c safe.directory='*' clone -q /opt/syx-origin.git work && cd work && sudo sed -i 's#SYNTAXIS_SANDBOX_NETWORK: \${SYNTAXIS_SANDBOX_NETWORK:-syntaxis-sandbox}#SYNTAXIS_SANDBOX_NETWORK: \${SYNTAXIS_SANDBOX_NETWORK:-syntaxis-sandbox}\n      SYNTAXIS_EGRESS_MODE: bogus#' docker-compose.yml && sudo git -c safe.directory='*' -c user.name=t -c user.email=t@example.com commit -q -am 'a change that cannot start' && sudo git -c safe.directory='*' push -q origin main" >"$out/break.log" 2>&1
check "main now holds a commit that cannot start" gx "sudo git -c safe.directory='*' -C /opt/syx-origin.git show main:docker-compose.yml | grep -q 'EGRESS_MODE: bogus'"
api post /update '{"target":"edge"}' >"$out/rollback-request.json" 2>&1
check "the request for the broken update is accepted" jq -e '.target == "edge"' "$out/rollback-request.json"
wait_update 3000
rc=$?
check "the update is not shown as done or as still running" test "$rc" -eq 1 -o "$rc" -eq 3
sleep 25
check "the previous version is running again" test "$(version)" = "$good"
check "the stack is healthy after the rollback" healthy
check "the .env is as it was before the update" test "$(gx 'sudo cat /opt/syntaxis/.env | grep -v "^#" | sort | md5sum' 2>/dev/null)" = "$good_env"
check "the checkout is back on the commit it was on" test "$(rev)" = "$good_rev" -a "$(branch)" = main
check "the admin can still sign in and the app says it failed, or that the result was lost" admin_sees_result
check "the failed database was kept for inspection" gx 'sudo docker run --rm -v "$(sudo docker inspect -f "{{range .Mounts}}{{if eq .Destination \"/data\"}}{{.Name}}{{end}}{{end}}" "$(cd /opt/syntaxis && sudo docker compose ps -q syntaxis)")":/d busybox ls /d | grep -q failed-update'

echo
if [ "$fails" -eq 0 ]; then echo "ALL PASSED"; else echo "$fails FAILED"; fi
[ "$fails" -gt 100 ] && fails=100
exit "$fails"
