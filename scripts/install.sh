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
#   --yes                do not ask before installing Docker, gVisor and other packages
#   --check              show what is missing and would be installed, change nothing
#
# What it does: checks the system; installs Docker, nftables and gVisor if they are missing
# (Debian, Ubuntu, Raspberry Pi OS, AlmaLinux, Rocky Linux, RHEL, Fedora; x86-64 and arm64); writes .env with a fresh master key; builds or pulls the images;
# starts the stack; installs systemd units that keep the egress firewall applied; and
# prints a link for the first admin. Nothing is sent anywhere except package and image
# downloads.
set -eu

URL="" ADMIN_EMAIL="" ADMIN_NAME="Admin" VERSION="" HTTPS_DOMAIN="" AUTO_UPDATE=0 FIREWALL=1 YES=0 LLM="" CHECK=0
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
    --check) CHECK=1; shift ;;
    -h|--help) sed -n '3,28p' "$0"; exit 0 ;;
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
# shellcheck disable=SC1091
. scripts/os.sh
echo "System: $OS_ID ($OS_FAMILY family), $CPU_ARCH"
[ "$CPU_ARCH" != unsupported ] || die "unsupported CPU ($(uname -m)); Syntaxis needs x86-64 or arm64"
MEM_MB=$(awk '/^MemTotal:/{printf "%d", $2/1024}' /proc/meminfo 2>/dev/null || echo 0)
[ "${MEM_MB:-0}" -ge 1500 ] || echo "warning: only ${MEM_MB:-?} MB of memory; sandboxes and image builds need more (2 GB or more is comfortable)"
# memory limits for sandboxes need the memory cgroup controller (Raspberry Pi OS leaves it off by default)
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  grep -qw memory /sys/fs/cgroup/cgroup.controllers || die "the memory cgroup controller is off. On Raspberry Pi OS add 'cgroup_enable=memory cgroup_memory=1' to /boot/firmware/cmdline.txt and reboot"
elif [ ! -d /sys/fs/cgroup/memory ]; then
  die "cgroups with the memory controller are required"
fi
command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = Enforcing ] && \
  echo "note: SELinux is enforcing. The Docker proxy service runs unconfined so it can reach the socket (see docker-compose.yml); report any other denial (ausearch -m avc -ts recent)."
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
  echo "note: firewalld is active. Docker manages its own rules; if users cannot reach the chat app, open its port: firewall-cmd --permanent --add-port=8080/tcp && firewall-cmd --reload"
fi

# what is missing, and so what this script will install
NEED_DOCKER=0 NEED_GVISOR=0 NEED_PKGS=""
command -v docker >/dev/null 2>&1 || NEED_DOCKER=1
command -v curl >/dev/null 2>&1 || NEED_PKGS="$NEED_PKGS curl"
if [ "$FIREWALL" = 1 ]; then
  command -v nft >/dev/null 2>&1 || NEED_PKGS="$NEED_PKGS nftables"
  command -v systemctl >/dev/null 2>&1 || die "systemd is needed to keep the firewall applied; pass --no-firewall and apply scripts/apply-egress.sh yourself"
fi
if [ "$NEED_DOCKER" = 1 ]; then
  NEED_GVISOR=1
elif docker info >/dev/null 2>&1; then
  docker info --format '{{json .Runtimes}}' | grep -q '"runsc"' || NEED_GVISOR=1
fi
if [ "$NEED_DOCKER$NEED_GVISOR${NEED_PKGS:+1}" != "00" ] && [ "$OS_FAMILY" = unknown ]; then
  die "cannot install packages on $OS_ID: install Docker Engine, Compose v2${NEED_PKGS:+, $NEED_PKGS} and gVisor yourself (docs/getting-started.md), then run this again"
fi
PLAN=$(mktemp)
{
  [ "$NEED_DOCKER" = 1 ] && echo "  - Docker Engine and Compose, from Docker's repository"
  [ -n "$NEED_PKGS" ] && echo "  -$NEED_PKGS"
  [ "$NEED_GVISOR" = 1 ] && echo "  - gVisor (runsc), registered with Docker"
  true
} > "$PLAN"
if [ -s "$PLAN" ]; then echo "To install:"; cat "$PLAN"; else echo "Everything needed is already installed."; fi
rm -f "$PLAN"
if [ "$CHECK" = 1 ]; then echo "(--check: nothing was changed)"; exit 0; fi

[ "$(id -u)" = 0 ] || die "run as root (sudo sh scripts/install.sh ...)"
if [ "$NEED_DOCKER$NEED_GVISOR${NEED_PKGS:+1}" != "00" ] && [ "$YES" != 1 ]; then
  printf 'Install the packages above now? [y/N] '
  read -r a; case "$a" in y|Y|yes) ;; *) die "cannot continue without them" ;; esac
fi
# NEED_PKGS is a space-separated list; word splitting is intended
# shellcheck disable=SC2086
[ -z "$NEED_PKGS" ] || { say "Installing$NEED_PKGS"; pkg_install $NEED_PKGS; }
if [ "$NEED_DOCKER" = 1 ]; then say "Installing Docker"; install_docker; fi
docker info >/dev/null 2>&1 || die "the Docker daemon is not running"
DV=$(docker version --format '{{.Server.Version}}' | cut -d. -f1)
[ "${DV:-0}" -ge 24 ] || die "Docker Engine 24 or newer is needed (found $(docker version --format '{{.Server.Version}}'))"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is not installed (the docker-compose-plugin package)"
echo "Docker $(docker version --format '{{.Server.Version}}'), $(docker compose version --short 2>/dev/null || echo 'Compose v2')"

# ---- gVisor ----
if docker info --format '{{json .Runtimes}}' | grep -q '"runsc"'; then
  echo "gVisor (runsc) is registered with Docker"
else
  say "Installing gVisor"
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

[ -n "$(getenv SYNTAXIS_FIREWALL_TOKEN)" ] || setenv SYNTAXIS_FIREWALL_TOKEN "$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')"

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
  SYNTAXIS_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || true)
  SYNTAXIS_BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  export SYNTAXIS_COMMIT SYNTAXIS_BUILD_DATE
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
  sed "s#@ROOT@#$ROOT#g" deploy/systemd/syntaxis-firewall-sync.service > /etc/systemd/system/syntaxis-firewall-sync.service
  systemctl daemon-reload
  systemctl enable --now syntaxis-firewall-sync.service >/dev/null
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
