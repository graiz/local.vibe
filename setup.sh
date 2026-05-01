#!/usr/bin/env bash
set -euo pipefail

# local.vibe setup — checks dependencies, builds, installs, and opens the dashboard.

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
DIM='\033[0;90m'
BOLD='\033[1m'
RESET='\033[0m'

step() { echo -e "\n${BOLD}$1${RESET}"; }
ok()   { echo -e "  ${GREEN}✓${RESET} $1"; }
info() { echo -e "  ${DIM}$1${RESET}"; }
warn() { echo -e "  ${YELLOW}!${RESET} $1"; }
fail() { echo -e "  ${RED}✗ $1${RESET}"; exit 1; }

require_cmd() {
  command -v "$1" &>/dev/null || fail "Missing required command: $1"
}

SUDO=""
if [[ "${EUID}" -ne 0 ]]; then
  require_cmd sudo
  SUDO="sudo"
fi

OS="$(uname -s)"
PKG_MANAGER=""
INSTALL_DIR="/usr/local/bin"

setup_platform() {
  case "$OS" in
    Darwin)
      PKG_MANAGER="brew"
      if [[ -f /opt/homebrew/bin/brew ]]; then
        eval "$(/opt/homebrew/bin/brew shellenv)"
        INSTALL_DIR="/opt/homebrew/bin"
      elif [[ -f /usr/local/bin/brew ]]; then
        eval "$(/usr/local/bin/brew shellenv)"
        INSTALL_DIR="/usr/local/bin"
      fi
      ;;
    Linux)
      if command -v apt-get &>/dev/null; then
        PKG_MANAGER="apt"
      elif command -v dnf &>/dev/null; then
        PKG_MANAGER="dnf"
      elif command -v pacman &>/dev/null; then
        PKG_MANAGER="pacman"
      else
        fail "Unsupported Linux distro: expected apt, dnf, or pacman"
      fi
      ;;
    *)
      fail "Unsupported OS: $OS"
      ;;
  esac
}

pkg_install() {
  case "$PKG_MANAGER" in
    brew)
      brew install "$@"
      ;;
    apt)
      ${SUDO} apt-get update -y
      ${SUDO} apt-get install -y "$@"
      ;;
    dnf)
      ${SUDO} dnf install -y "$@"
      ;;
    pacman)
      ${SUDO} pacman -Syu --noconfirm --needed "$@"
      ;;
  esac
}

install_go() {
  case "$PKG_MANAGER" in
    brew) pkg_install go ;;
    apt) pkg_install golang-go ;;
    dnf) pkg_install golang ;;
    pacman) pkg_install go ;;
  esac
}

open_url() {
  if command -v open &>/dev/null; then
    open "$1" >/dev/null 2>&1 || true
  elif command -v xdg-open &>/dev/null; then
    xdg-open "$1" >/dev/null 2>&1 || true
  elif command -v gio &>/dev/null; then
    gio open "$1" >/dev/null 2>&1 || true
  fi
}

echo -e "${BOLD}local.vibe${RESET} — friendly names for local dev servers"
setup_platform

# ── System dependencies ──────────────────────────────────
step "Installing system dependencies..."
if [[ "$OS" == "Darwin" ]] && ! command -v brew &>/dev/null; then
  info "Installing Homebrew..."
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  setup_platform
fi
info "Installing dnsmasq..."
pkg_install dnsmasq
ok "dnsmasq installed"

# ── Go ────────────────────────────────────────────────────
step "Checking Go..."
if command -v go &>/dev/null; then
  ok "Go $(go version | awk '{print $3}' | sed 's/go//') installed"
else
  info "Installing Go via $PKG_MANAGER..."
  install_go
  command -v go &>/dev/null || fail "Go installation failed"
  ok "Go installed"
fi

# ── Build ─────────────────────────────────────────────────
step "Building vibe..."
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"
go build -o vibe .
ok "Built successfully"

# ── Install binary ────────────────────────────────────────
step "Installing binary..."
${SUDO} install -m 0755 vibe "$INSTALL_DIR/vibe"
ok "Installed to $INSTALL_DIR/vibe"

# ── System setup (requires sudo) ─────────────────────────
step "Configuring DNS and port forwarding..."
info "This requires root to configure dnsmasq, DNS resolver, and forwarding rules."
${SUDO} "$INSTALL_DIR/vibe" setup
ok "System configured"

# ── Start daemon ──────────────────────────────────────────
step "Starting daemon..."
"$INSTALL_DIR/vibe" daemon start 2>/dev/null || warn "Could not start daemon automatically"
ok "Daemon setup complete"

# ── Done ──────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}Setup complete!${RESET}"
echo ""
echo -e "  Dashboard:  ${BOLD}https://local.vibe${RESET}"
echo -e "  Add routes, manage services, and get per-project setup instructions there."
echo ""

open_url "https://local.vibe"