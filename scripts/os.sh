#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Detects the operating system and provides package helpers for scripts/install.sh. Sourced, not run.
#
# Sets: OS_ID (debian, ubuntu, almalinux, rocky, ...), OS_FAMILY (debian | rhel | unknown),
#       OS_CODENAME (Debian/Ubuntu release name), CPU_ARCH (amd64 | arm64 | unsupported).
# Raspberry Pi OS reports itself as Debian (or raspbian on old releases) and is treated as Debian.
# CentOS Stream, Rocky Linux, AlmaLinux, RHEL and Fedora are the RHEL family.

# shellcheck disable=SC2034   # these are read by the script that sources this file
OS_ID=unknown OS_FAMILY=unknown OS_CODENAME=""
# shellcheck disable=SC1090   # os-release is read from a variable path on purpose (tests point it elsewhere)
_osr=${OS_RELEASE_FILE:-/etc/os-release}
if [ -r "$_osr" ]; then
  # os-release defines VERSION, NAME, ID and more; read it in subshells so none of that leaks
  # into the script that sources this file (it once overwrote the installer's own VERSION)
  # shellcheck disable=SC1090
  OS_ID=$( . "$_osr"; printf '%s' "${ID:-unknown}" )
  # shellcheck disable=SC1090
  OS_CODENAME=$( . "$_osr"; printf '%s' "${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}" )
  # shellcheck disable=SC1090
  _like=$( . "$_osr"; printf '%s' "${ID_LIKE:-}" )
  case " $OS_ID $_like " in
    *" debian "*|*" ubuntu "*|*" raspbian "*) OS_FAMILY=debian ;;
    *" rhel "*|*" fedora "*|*" centos "*) OS_FAMILY=rhel ;;
  esac
fi
case "${SYNTAXIS_TEST_ARCH:-$(uname -m)}" in
  x86_64|amd64) CPU_ARCH=amd64 ;;
  aarch64|arm64) CPU_ARCH=arm64 ;;
  *) CPU_ARCH=unsupported ;;
esac

# the Docker repository path for this distribution
docker_repo_id() {
  case "$OS_ID" in
    raspbian) echo debian ;;
    ubuntu|debian|fedora) echo "$OS_ID" ;;
    *) [ "$OS_FAMILY" = debian ] && echo debian || echo centos ;;
  esac
}

# pkg_install PACKAGE...
pkg_install() {
  case "$OS_FAMILY" in
    debian) apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
    rhel) if command -v dnf >/dev/null 2>&1; then dnf install -y -q "$@"; else yum install -y -q "$@"; fi ;;
    *) return 1 ;;
  esac
}

# install_docker: Docker Engine, Buildx and Compose from Docker's own repository.
install_docker() {
  repo=$(docker_repo_id)
  case "$OS_FAMILY" in
    debian)
      pkg_install ca-certificates gnupg
      command -v curl >/dev/null 2>&1 || pkg_install curl
      install -m 0755 -d /etc/apt/keyrings
      curl -fsSL "https://download.docker.com/linux/$repo/gpg" -o /etc/apt/keyrings/docker.asc
      chmod a+r /etc/apt/keyrings/docker.asc
      echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$repo $OS_CODENAME stable" > /etc/apt/sources.list.d/docker.list
      pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
      ;;
    rhel)
      pkg_install dnf-plugins-core
      command -v curl >/dev/null 2>&1 || pkg_install curl   # not when curl-minimal is already there: they conflict
      dnf config-manager --add-repo "https://download.docker.com/linux/$repo/docker-ce.repo" 2>/dev/null \
        || curl -fsSL "https://download.docker.com/linux/$repo/docker-ce.repo" -o /etc/yum.repos.d/docker-ce.repo
      pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
      ;;
    *) return 1 ;;
  esac
  systemctl enable --now docker
}
