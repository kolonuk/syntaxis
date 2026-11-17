#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Updates a Syntaxis install made by scripts/install.sh. Run as root from the checkout.
#
#   sudo sh scripts/update.sh                   # update within the current channel
#   sudo sh scripts/update.sh --release         # switch to (or update) published releases: the latest
#   sudo sh scripts/update.sh --release v1.4.0  # ... or one version
#   sudo sh scripts/update.sh --edge            # follow the main branch (built from source)
#   sudo sh scripts/update.sh --auto            # quiet when already up to date (used by the timer)
#   sudo sh scripts/update.sh --dry-run ...     # say what would be done, change nothing
#
# Channels: "release" runs the published images of one version; "edge" builds the main branch from
# this checkout. Steps: back up the database, keep the running image for rollback, pull or build the
# new version, restart, and wait for the health endpoint to report the new version. If that fails,
# the previous image, checkout and database backup are restored.
#
# The whole script is one compound command so the shell reads it before running anything: the
# checkout can be switched to another version underneath it safely.
{
set -eu
cd "$(dirname "$0")/.."
CHANNEL=keep VERSION="" AUTO=0 DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --release) CHANNEL=release; case "${2:-}" in ''|--*) shift ;; *) VERSION="$2"; shift 2 ;; esac ;;
    --edge) CHANNEL=edge; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    --auto) AUTO=1; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) sed -n '3,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
say() { [ "$AUTO" = 1 ] || printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
getenv() { [ -f .env ] || return 0; sed -n "s/^$1=//p" .env | tail -n 1; }
setenv() {
  if grep -q "^$1=" .env; then
    awk -v k="$1" -v v="$2" 'BEGIN{FS=OFS="="} $1==k{print k"="v; next} {print}' .env > .env.tmp && mv .env.tmp .env && chmod 600 .env
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}
# only a version tag is ever used as a git ref or image tag
case "$VERSION" in
  '') ;;
  v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) case "$VERSION" in *[!0-9A-Za-z.-]*) die "bad version: $VERSION" ;; esac ;;
  *) die "bad version: $VERSION" ;;
esac

IMAGE=$(getenv SYNTAXIS_IMAGE); IMAGE=${IMAGE:-ghcr.io/kolonuk/syntaxis:latest}
OLD_IMAGE=$IMAGE
SOURCE_NOW=0
[ "$IMAGE" = syntaxis:local ] && SOURCE_NOW=1
case "$CHANNEL" in
  release) SOURCE=0 ;;
  edge) SOURCE=1 ;;
  *) SOURCE=$SOURCE_NOW ;;
esac

latest_release() {
  curl -fsS https://api.github.com/repos/kolonuk/syntaxis/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1
}

if [ "$DRY" = 1 ]; then
  if [ "$SOURCE" = 1 ]; then
    echo "plan: channel=edge target=${VERSION:-main} from=$OLD_IMAGE"
  else
    v=$VERSION
    [ -n "$v" ] || v=$(latest_release) || true
    echo "plan: channel=release target=${v:-unknown} from=$OLD_IMAGE"
  fi
  exit 0
fi

[ "$(id -u)" = 0 ] || die "run as root"
[ -f .env ] || die "no .env here; install first with scripts/install.sh"
port=$(getenv SYNTAXIS_CHAT_PORT); port=${port:-8080}
CID=$(docker compose ps -q syntaxis)
[ -n "$CID" ] || die "Syntaxis is not running"
OLD_VERSION=$(curl -fsS "http://127.0.0.1:$port/api/version" 2>/dev/null || echo unknown)

# ---- what to update to ----
OLD_REV="" OLD_REF=""
if [ -d .git ]; then
  OLD_REV=$(git rev-parse HEAD)
  OLD_REF=$(git symbolic-ref -q --short HEAD || echo "$OLD_REV")
fi
if [ "$SOURCE" = 1 ]; then
  [ -d .git ] || die "building from source needs a git checkout"
  if [ "$CHANNEL" = edge ]; then
    git fetch -q origin main || die "could not fetch the main branch"
    git checkout -q main || die "could not switch to main (local changes?)"
    git pull -q --ff-only || die "git pull failed (local changes?)"
  elif [ -n "$VERSION" ]; then
    git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null || git fetch -q --tags
    git checkout -q "$VERSION" || die "could not switch to $VERSION"
  else
    git pull -q --ff-only || die "git pull failed (local changes?)"
  fi
  NEW_REV=$(git rev-parse HEAD)
  if [ "$OLD_REV" = "$NEW_REV" ] && [ "$AUTO" = 1 ] && [ "$SOURCE_NOW" = 1 ]; then exit 0; fi
  WANT=$(git rev-parse --short HEAD)
else
  if [ -z "$VERSION" ]; then
    VERSION=$(latest_release)
    [ -n "$VERSION" ] || die "could not find the latest release"
  fi
  NEW_IMAGE="ghcr.io/kolonuk/syntaxis:${VERSION#v}"
  if [ "$NEW_IMAGE" = "$IMAGE" ]; then
    say "already on $VERSION"
    exit 0
  fi
  WANT="$VERSION"
fi
say "updating from $OLD_VERSION to $WANT"

# ---- backup and rollback point ----
TS=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP="/data/pre-update-$TS.db"
docker compose exec -T syntaxis syntaxis backup "$BACKUP" >/dev/null
say "database backed up to $BACKUP (inside the data volume)"
docker tag "$(docker inspect -f '{{.Image}}' "$CID")" syntaxis:rollback
cp -p .env ".env.pre-update-$TS"
VOL=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$CID")

# ---- apply ----
# installs from before the firewall sync service get its token
[ -n "$(getenv SYNTAXIS_FIREWALL_TOKEN)" ] || setenv SYNTAXIS_FIREWALL_TOKEN "$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')"
if [ "$SOURCE" = 1 ]; then
  setenv SYNTAXIS_IMAGE syntaxis:local
  case "$(getenv SYNTAXIS_SANDBOX_IMAGE)" in ghcr.io/*) setenv SYNTAXIS_SANDBOX_IMAGE syntaxis-sandbox:latest ;; esac
  SYNTAXIS_COMMIT="$WANT"
  SYNTAXIS_BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  export SYNTAXIS_COMMIT SYNTAXIS_BUILD_DATE
  docker compose build -q
  img=$(getenv SYNTAXIS_SANDBOX_IMAGE); img=${img:-syntaxis-sandbox:latest}
  docker build -q -t "$img" images/sandbox >/dev/null
  setenv SYNTAXIS_CHANNEL edge
else
  setenv SYNTAXIS_IMAGE "$NEW_IMAGE"
  setenv SYNTAXIS_SANDBOX_IMAGE "ghcr.io/kolonuk/syntaxis-sandbox:$VERSION"
  setenv SYNTAXIS_CHANNEL release
  docker compose pull -q
  docker pull -q "ghcr.io/kolonuk/syntaxis-sandbox:$VERSION" >/dev/null
  # keep the Compose file and scripts in step with the release (best effort)
  if [ -d .git ]; then
    git fetch -q --tags origin 2>/dev/null || true
    git checkout -q "$VERSION" 2>/dev/null || echo "note: could not move the checkout to $VERSION; the images are still $VERSION" >&2
  fi
fi
docker compose up -d --remove-orphans

# ---- verify ----
healthy() {
  i=0
  while [ "$i" -lt 60 ]; do
    v=$(curl -fsS "http://127.0.0.1:$port/api/version" 2>/dev/null || true)
    case "$v" in *"$WANT"*) return 0 ;; esac
    i=$((i + 1)); sleep 2
  done
  return 1
}
if healthy; then
  say "updated: $(curl -fsS "http://127.0.0.1:$port/api/version")"
  if [ -f /etc/systemd/system/syntaxis-egress.service ]; then
    # keep the host's units in step with the checkout
    ROOT=$(pwd)
    sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-firewall-sync.service > /etc/systemd/system/syntaxis-firewall-sync.service
    if [ -f /etc/systemd/system/syntaxis-update-agent.service ]; then
      sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-update-agent.service > /etc/systemd/system/syntaxis-update-agent.service
    fi
    systemctl daemon-reload
    systemctl enable syntaxis-firewall-sync.service >/dev/null 2>&1 || true
    systemctl restart syntaxis-egress.service syntaxis-firewall-sync.service || true
  fi
  exit 0
fi

# ---- roll back ----
echo "the new version did not become healthy; rolling back" >&2
docker compose logs --tail 40 syntaxis >&2 || true
docker compose stop syntaxis
SANDBOX_IMG=$(getenv SYNTAXIS_SANDBOX_IMAGE); SANDBOX_IMG=${SANDBOX_IMG:-syntaxis-sandbox:latest}
# restore the database as it was before any migration ran (the server image has no shell)
docker run --rm --user 0 --network none -v "$VOL:/data" --entrypoint sh "$SANDBOX_IMG" -c \
  "cp /data/syntaxis.db /data/failed-update-$TS.db 2>/dev/null; rm -f /data/syntaxis.db-wal /data/syntaxis.db-shm; cp $BACKUP /data/syntaxis.db && chown --reference=$BACKUP /data/syntaxis.db"
cp -p ".env.pre-update-$TS" .env
docker tag syntaxis:rollback "$OLD_IMAGE"
# the Compose file and scripts come from the checkout too: put it back where it was
if [ -n "$OLD_REF" ]; then
  git checkout -q "$OLD_REF" || echo "could not return the checkout to $OLD_REF; do it by hand" >&2
  if [ "$OLD_REF" != "$OLD_REV" ]; then
    git reset -q --keep "$OLD_REV" || echo "could not return the checkout to $OLD_REV; do it by hand" >&2
  fi
fi
docker compose up -d --remove-orphans
die "update failed; rolled back to $OLD_VERSION. The failed database is kept as /data/failed-update-$TS.db"
}
