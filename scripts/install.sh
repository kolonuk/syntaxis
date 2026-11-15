#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# One-command install of Syntaxis on a Linux host with Docker. Run as root from a
# checkout of the repository. Safe to run again: existing settings, keys and data are
# kept, and each step is skipped when it is already done.
#
#   sudo sh scripts/install.sh --url https://syntaxis.example.com --admin-email you@example.com
#
# Options:
#   --url URL            address your users will open (required on first install)
#   --admin-email EMAIL  create the first admin and print a single-use setup link
#   --admin-name NAME    their display name (default: Admin)
#   --llm HOST_OR_CIDR   your LLM server; sandboxes are blocked from reaching it (repeatable)
#   --version vX.Y.Z     use published, version-pinned images instead of building from source
#   --https DOMAIN       add the bundled Caddy reverse proxy with automatic certificates
#   --auto-update        install a weekly timer that runs scripts/update.sh
#   --no-firewall        do not install the egress firewall units (not recommended)
#   --yes                do not ask before installing gVisor
#
# What it does: checks Docker, Compose and nftables; installs and registers gVisor
# if it is missing; writes .env with a fresh master key; builds or pulls the images;
# starts the stack; installs systemd units that keep the egress firewall applied; and
# prints a link for the first admin. Nothing is sent anywhere except package and image
# downloads.
set -eu

URL="" ADMIN_EMAIL="" ADMIN_NAME="Admin" VERSION="" HTTPS_DOMAIN="" AUTO_UPDATE=0 FIREWALL=1 YES=0 LLM=""
while [ $# -gt 0 ]; do
  case "$1" in
    --url) URL="$2"; shift 2 ;;
    --admin-email) ADMIN_EMAIL="$2"; shift 2 ;;
    --admin-name) ADMIN_NAME="$2"; shift 2 ;;
    --llm) LLM="$LLM $2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --https) HTTPS_DOMAIN="$2"; shift 2 ;;
    --auto-update) AUTO_UPDATE=1; shift ;;
    --no-firewall) FIREWALL=0; shift ;;
    --yes|-y) YES=1; shift ;;
    -h|--help) sed -n '3,26p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
cd "$(dirname "$0")/.."
ROOT=$(pwd)

# ---- prerequisites ----
say "Checking prerequisites"
[ "$(uname -s)" = Linux ] || die "Syntaxis needs Linux (gVisor does not run on other systems)"
[ "$(id -u)" = 0 ] || die "run as root (sudo sh scripts/install.sh ...)"
command -v docker >/dev/null || die "Docker Engine is not installed: https://docs.docker.com/engine/install/"
docker info >/dev/null 2>&1 || die "the Docker daemon is not running"
DV=$(docker version --format '{{.Server.Version}}' | cut -d. -f1)
[ "${DV:-0}" -ge 24 ] || die "Docker Engine 24 or newer is needed (found $(docker version --format '{{.Server.Version}}'))"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is not installed (the docker-compose-plugin package)"
command -v curl >/dev/null || die "curl is needed"
if [ "$FIREWALL" = 1 ]; then
  command -v nft >/dev/null || die "nftables (nft) is needed for the egress firewall; install the nftables package, or pass --no-firewall"
  command -v systemctl >/dev/null || die "systemd is needed to keep the firewall applied; pass --no-firewall and apply scripts/apply-egress.sh yourself"
fi
echo "Docker $(docker version --format '{{.Server.Version}}'), $(docker compose version --short 2>/dev/null || echo 'Compose v2')"

# ---- gVisor ----
if docker info --format '{{json .Runtimes}}' | grep -q '"runsc"'; then
  echo "gVisor (runsc) is registered with Docker"
else
  say "Installing gVisor"
  command -v apt-get >/dev/null || die "gVisor is not registered and this is not a Debian/Ubuntu host; install it from https://gvisor.dev/docs/user_guide/install/ and run this again"
  if [ "$YES" != 1 ]; then
    printf 'gVisor is not installed. Install it from the official repository and restart Docker now? [y/N] '
    read -r a; case "$a" in y|Y|yes) ;; *) die "gVisor is required" ;; esac
  fi
  sh scripts/install-gvisor.sh
fi

# ---- settings ----
setenv() { # KEY VALUE: set (or add) KEY in .env without touching other lines
  if grep -q "^$1=" .env; then
    awk -v k="$1" -v v="$2" 'BEGIN{FS=OFS="="} $1==k{print k"="v; next} {print}' .env > .env.tmp && mv .env.tmp .env
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}
getenv() { sed -n "s/^$1=//p" .env | tail -n 1; }
if [ ! -f .env ]; then
  say "Writing .env"
  [ -n "$URL" ] || die "--url is needed on the first install"
  umask 077
  cp .env.example .env
  setenv SYNTAXIS_MASTER_KEY "$(head -c 32 /dev/urandom | base64)"
  NEW_KEY=1
else
  echo ".env exists; keeping its settings"
  NEW_KEY=0
fi
chmod 600 .env
if [ -n "$URL" ]; then setenv SYNTAXIS_BASE_URL "$URL"; fi
[ -n "$(getenv SYNTAXIS_MASTER_KEY)" ] || [ -n "$(getenv SYNTAXIS_MASTER_KEY_FILE)" ] || setenv SYNTAXIS_MASTER_KEY "$(head -c 32 /dev/urandom | base64)"
if [ -n "$LLM" ]; then
  blocks=$(getenv SYNTAXIS_EGRESS_BLOCK)
  for h in $LLM; do
    case "$h" in
      */*) c="$h" ;;
      *:*) c="$h/128" ;;
      *[!0-9.]*) ip=$(getent ahostsv4 "$h" | awk 'NR==1{print $1}'); [ -n "$ip" ] || die "cannot resolve $h"; c="$ip/32" ;;
      *) c="$h/32" ;;
    esac
    case ",$blocks," in *",$c,"*) ;; *) blocks="${blocks:+$blocks,}$c" ;; esac
  done
  setenv SYNTAXIS_EGRESS_BLOCK "$blocks"
fi

# ---- images ----
if [ -n "$VERSION" ]; then
  say "Pulling version $VERSION"
  v=${VERSION#v}
  setenv SYNTAXIS_IMAGE "ghcr.io/kolonuk/syntaxis:$v"
  setenv SYNTAXIS_SANDBOX_IMAGE "ghcr.io/kolonuk/syntaxis-sandbox:$VERSION"
  docker compose pull
  docker pull "ghcr.io/kolonuk/syntaxis-sandbox:$VERSION"
else
  say "Building from source ($(git rev-parse --short HEAD 2>/dev/null || echo 'no git'))"
  setenv SYNTAXIS_IMAGE "syntaxis:local"
  export SYNTAXIS_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || true)"
  export SYNTAXIS_BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  docker compose build
  img=$(getenv SYNTAXIS_SANDBOX_IMAGE); img=${img:-syntaxis-sandbox:latest}
  docker build -t "$img" images/sandbox
fi

# ---- HTTPS ----
COMPOSE="docker compose"
if [ -n "$HTTPS_DOMAIN" ]; then
  setenv SYNTAXIS_DOMAIN "$HTTPS_DOMAIN"
  setenv SYNTAXIS_BASE_URL "https://$HTTPS_DOMAIN"
  setenv COMPOSE_FILE "docker-compose.yml:deploy/compose.https.yml"
  setenv SYNTAXIS_CHAT_BIND 127.0.0.1             # only Caddy is public
  setenv SYNTAXIS_PROXY_TRUSTED 172.31.250.2      # Caddy's address on the edge network
fi

# ---- start ----
say "Starting Syntaxis"
$COMPOSE up -d --remove-orphans
port=$(getenv SYNTAXIS_CHAT_PORT); port=${port:-8080}
aport=$(getenv SYNTAXIS_ADMIN_PORT); aport=${aport:-8081}
i=0
until curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -ge 60 ] && { $COMPOSE logs --tail 50 syntaxis; die "Syntaxis did not become healthy"; }
  sleep 2
done
echo "running: $(curl -fsS "http://127.0.0.1:$port/api/version")"

# ---- firewall ----
if [ "$FIREWALL" = 1 ]; then
  say "Installing the egress firewall units"
  sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-egress.service > /etc/systemd/system/syntaxis-egress.service
  sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-egress.path > /etc/systemd/system/syntaxis-egress.path
  cp deploy/systemd/syntaxis-egress.timer /etc/systemd/system/syntaxis-egress.timer
  systemctl daemon-reload
  systemctl enable --now syntaxis-egress.path syntaxis-egress.timer >/dev/null
  systemctl enable syntaxis-egress.service >/dev/null
  systemctl restart syntaxis-egress.service
  systemctl --no-pager --lines=0 status syntaxis-egress.service | head -3 || true
else
  echo "skipped (--no-firewall): apply scripts/apply-egress.sh yourself, at every boot"
fi

if [ "$AUTO_UPDATE" = 1 ]; then
  say "Installing the weekly update timer"
  sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-update.service > /etc/systemd/system/syntaxis-update.service
  cp deploy/systemd/syntaxis-update.timer /etc/systemd/system/syntaxis-update.timer
  systemctl daemon-reload
  systemctl enable --now syntaxis-update.timer >/dev/null
fi

# ---- first admin ----
if [ -n "$ADMIN_EMAIL" ]; then
  say "First admin"
  abind=$(getenv SYNTAXIS_ADMIN_BIND)
  if [ -z "$abind" ] || [ "$abind" = 0.0.0.0 ]; then abind=127.0.0.1; fi
  $COMPOSE exec -T -e SYNTAXIS_ADMIN_URL="http://$abind:$aport" syntaxis syntaxis admin setup-link "$ADMIN_EMAIL" "$ADMIN_NAME" \
    || echo "(no new link: see the message above)"
fi

# ---- check ----
say "Health check"
$COMPOSE exec -T syntaxis syntaxis doctor || true

say "Done"
echo "Chat app:  $(getenv SYNTAXIS_BASE_URL)  (local: http://127.0.0.1:$port)"
echo "Admin app: http://127.0.0.1:$aport  (kept off the public interface)"
if [ "$NEW_KEY" = 1 ]; then
  echo
  echo "IMPORTANT: back up the master key in $ROOT/.env (SYNTAXIS_MASTER_KEY) somewhere separate."
  echo "Stored secrets and 2FA settings cannot be recovered without it."
fi
echo "Next: connect your LLM server in the admin app (Providers & models), then invite users."
