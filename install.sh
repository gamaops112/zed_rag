#!/bin/bash
# zed-rag installer — Ubuntu 22.04 / 24.04
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${GREEN}✓${NC} $1"; }
info()  { echo -e "  → $1"; }
warn()  { echo -e "${RED}!${NC} $1"; }
error() { echo -e "${RED}✗ ERROR:${NC} $1"; exit 1; }

VERSION="__VERSION__"
REPO="__REPO__"
BINARY_NAME="zed-rag-linux-amd64"
INSTALL_PATH="/usr/local/bin/zed-rag"
CONFIG_DIR="$HOME/.zed-rag"
CONFIG_FILE="$CONFIG_DIR/config.toml"
SERVICE_DIR="$HOME/.config/systemd/user"
SERVICE_FILE="$SERVICE_DIR/zed-rag.service"

# ── OS check ─────────────────────────────────────────────────────────────────
check_os() {
  [ -f /etc/os-release ] || error "Not Ubuntu. Ubuntu 22.04/24.04 only."
  . /etc/os-release
  [ "$ID" = "ubuntu" ] || error "Not Ubuntu (detected: $ID)."
  case "$VERSION_ID" in
    22.04|24.04) log "Ubuntu $VERSION_ID detected" ;;
    *) error "Ubuntu $VERSION_ID not supported. Need 22.04 or 24.04." ;;
  esac
}

# ── Dependencies ──────────────────────────────────────────────────────────────
install_deps() {
  if ! command -v curl &>/dev/null || ! command -v jq &>/dev/null; then
    info "Installing curl, jq..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq curl jq
  fi
}

# ── Repo detection ────────────────────────────────────────────────────────────
detect_repo() {
  # REPO and VERSION are injected at release time by CI.
  # If running from source (not a release), fall back to GitHub API search.
  if [ "$REPO" != "__REPO__" ] && [ -n "$REPO" ]; then
    log "Repo: $REPO"
    return
  fi
  info "Detecting repo from GitHub API..."
  REPO=$(curl -sf \
    "https://api.github.com/search/repositories?q=zed-rag+in:name&sort=stars&per_page=1" \
    | jq -r '.items[0].full_name' 2>/dev/null || true)
  if [ -z "$REPO" ] || [ "$REPO" = "null" ]; then
    error "Could not detect repo. Set REPO=owner/zed-rag and retry:\n  REPO=owner/zed-rag bash install.sh"
  fi
  log "Detected repo: $REPO"
}

# ── Download binary ───────────────────────────────────────────────────────────
install_binary() {
  DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_NAME}"
  info "Downloading latest $BINARY_NAME ..."
  TMP=$(mktemp)
  curl -fsSL "$DOWNLOAD_URL" -o "$TMP" || error "Download failed. Check REPO and VERSION."
  chmod +x "$TMP"
  sudo mv "$TMP" "$INSTALL_PATH"
  log "Binary installed: $INSTALL_PATH"
}

# ── Config (never overwrite existing) ────────────────────────────────────────
write_config() {
  if [ -f "$CONFIG_FILE" ]; then
    log "Config exists at $CONFIG_FILE — keeping unchanged"
    return
  fi
  mkdir -p "$CONFIG_DIR"
  cat > "$CONFIG_FILE" <<'EOF'
# zed-rag configuration
# All values can also be set via environment variables.

qdrant_url        = "http://localhost:6333"
qdrant_collection = "codebase"
ollama_url        = "http://localhost:11434"
embedding_model   = "nomic-embed-text"
dashboard_port    = 7702

# project_path is set per-command (CLI arg or PROJECT_PATH env var).
# Uncomment to set a default for MCP mode:
# project_path = "/path/to/your/project"
EOF
  log "Config written: $CONFIG_FILE"
}

# ── Systemd user service ──────────────────────────────────────────────────────
install_service() {
  mkdir -p "$SERVICE_DIR"

  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=zed-rag watcher and dashboard
After=network.target

[Service]
Type=simple
ExecStart=$INSTALL_PATH serve
Restart=on-failure
RestartSec=5
# Set PROJECT_PATH to the project you want to watch.
# Override with: systemctl --user edit zed-rag
Environment=PROJECT_PATH=$HOME

[Install]
WantedBy=default.target
EOF

  systemctl --user daemon-reload 2>/dev/null || true
  log "Service installed: $SERVICE_FILE"
  info "Edit PROJECT_PATH:  systemctl --user edit zed-rag"
  info "Enable + start:    systemctl --user enable --now zed-rag"
  info "View logs:         journalctl --user -u zed-rag -f"
}

# ── Summary ───────────────────────────────────────────────────────────────────
print_summary() {
  echo ""
  echo -e "${BOLD}${GREEN}══ zed-rag installed ══${NC}"
  echo ""
  echo -e "  ${GREEN}✓${NC} Binary  : ${BOLD}$INSTALL_PATH${NC}"
  echo -e "  ${GREEN}✓${NC} Config  : ${BOLD}$CONFIG_FILE${NC}"
  echo -e "  ${GREEN}✓${NC} Service : ${BOLD}$SERVICE_FILE${NC}"
  echo ""
  echo -e "${BOLD}Quick start:${NC}"
  echo ""
  echo -e "  ${CYAN}# Index your project once:${NC}"
  echo -e "  zed-rag index /path/to/your/project"
  echo ""
  echo -e "  ${CYAN}# Start persistent watcher (re-indexes on file changes):${NC}"
  echo -e "  systemctl --user edit zed-rag   # set PROJECT_PATH"
  echo -e "  systemctl --user enable --now zed-rag"
  echo ""
  echo -e "  ${CYAN}# Remove index for a project:${NC}"
  echo -e "  zed-rag remove /path/to/your/project"
  echo ""
  echo -e "  ${CYAN}# Check what's indexed:${NC}"
  echo -e "  zed-rag status /path/to/your/project"
  echo ""
  echo -e "${BOLD}continue.dev MCP config (~/.continue/config.yaml):${NC}"
  echo ""
  echo -e "${CYAN}"
  cat <<'MCP'
mcpServers:
  - name: zed-rag
    command: zed-rag
    args: []
    env:
      PROJECT_PATH: /path/to/your/project
MCP
  echo -e "${NC}"
  echo -e "  Dashboard: ${BOLD}http://localhost:7702${NC}  (when service is running)"
  echo ""
}

# ── Main ──────────────────────────────────────────────────────────────────────
main() {
  echo -e "${BOLD}${CYAN}"
  echo "  ╔══════════════════════════════════╗"
  echo "  ║      zed-rag  installer          ║"
  echo "  ╚══════════════════════════════════╝"
  echo -e "${NC}"

  check_os
  install_deps
  detect_repo
  install_binary
  write_config
  install_service
  print_summary
}

main "$@"
