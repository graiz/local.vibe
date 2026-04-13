#!/bin/bash
set -e

# local.vibe setup — checks dependencies, builds, installs, and opens the dashboard.

RED='\033[0;31m'
GREEN='\033[0;32m'
DIM='\033[0;90m'
BOLD='\033[1m'
RESET='\033[0m'

step() { echo -e "\n${BOLD}$1${RESET}"; }
ok()   { echo -e "  ${GREEN}✓${RESET} $1"; }
info() { echo -e "  ${DIM}$1${RESET}"; }
fail() { echo -e "  ${RED}✗ $1${RESET}"; exit 1; }

echo -e "${BOLD}local.vibe${RESET} — friendly names for local dev servers"

# ── Homebrew ──────────────────────────────────────────────
step "Checking Homebrew..."
if command -v brew &>/dev/null; then
  ok "Homebrew installed"
else
  info "Installing Homebrew..."
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  # Add brew to PATH for this session
  if [[ -f /opt/homebrew/bin/brew ]]; then
    eval "$(/opt/homebrew/bin/brew shellenv)"
  elif [[ -f /usr/local/bin/brew ]]; then
    eval "$(/usr/local/bin/brew shellenv)"
  fi
  command -v brew &>/dev/null || fail "Homebrew installation failed"
  ok "Homebrew installed"
fi

# ── Go ────────────────────────────────────────────────────
step "Checking Go..."
if command -v go &>/dev/null; then
  ok "Go $(go version | awk '{print $3}' | sed 's/go//') installed"
else
  info "Installing Go via Homebrew..."
  brew install go
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
INSTALL_DIR="/opt/homebrew/bin"
if [[ ! -d "$INSTALL_DIR" ]]; then
  INSTALL_DIR="/usr/local/bin"
fi
cp vibe "$INSTALL_DIR/vibe" 2>/dev/null || sudo cp vibe "$INSTALL_DIR/vibe"
ok "Installed to $INSTALL_DIR/vibe"

# ── System setup (requires sudo) ─────────────────────────
step "Configuring DNS and port forwarding..."
info "This requires sudo to install dnsmasq, DNS resolver, and port forwarding rules."
sudo "$INSTALL_DIR/vibe" setup
ok "System configured"

# ── Start daemon ──────────────────────────────────────────
step "Starting daemon..."
"$INSTALL_DIR/vibe" daemon start 2>/dev/null || true
ok "Daemon running"

# ── Done ──────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}Setup complete!${RESET}"
echo ""
echo -e "  Dashboard:  ${BOLD}https://local.vibe${RESET}"
echo -e "  Add routes, manage services, and get per-project setup instructions there."
echo ""

# Open the dashboard
if command -v open &>/dev/null; then
  open https://local.vibe
fi
