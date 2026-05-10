#!/bin/bash
# setup.sh — Install Ollama + Qdrant prerequisites for zed-rag
# Supports: Ubuntu 22.04/24.04, WSL2 Ubuntu
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${GREEN}✓${NC} $1"; }
info()  { echo -e "  → $1"; }
warn()  { echo -e "${YELLOW}!${NC} $1"; }
error() { echo -e "${RED}✗ ERROR:${NC} $1"; exit 1; }

EMBEDDING_MODEL="${EMBEDDING_MODEL:-nomic-embed-text}"
QDRANT_STORAGE="$HOME/.zed-rag/qdrant_storage"

is_wsl()      { grep -qis "microsoft\|wsl" /proc/version 2>/dev/null; }
has_systemd() { [ -d /run/systemd/system ]; }

# ── Bootstrap deps (needed before anything else) ──────────────────────────────
install_bootstrap_deps() {
  local missing=()
  command -v curl &>/dev/null || missing+=(curl)
  command -v zstd &>/dev/null || missing+=(zstd)    # needed for Docker image pulls
  dpkg -s gnupg &>/dev/null   || missing+=(gnupg)   # needed for Docker GPG key
  if [ ${#missing[@]} -gt 0 ]; then
    info "Installing bootstrap deps: ${missing[*]}"
    sudo apt-get update -qq
    sudo apt-get install -y -qq "${missing[@]}"
  fi
}

# ── OS check ──────────────────────────────────────────────────────────────────
check_os() {
  [ -f /etc/os-release ] || error "Unsupported OS. Ubuntu 22.04/24.04 only."
  . /etc/os-release
  [ "$ID" = "ubuntu" ] || error "Not Ubuntu (detected: $ID)."
  case "$VERSION_ID" in
    22.04|24.04) log "Ubuntu $VERSION_ID detected" ;;
    *) error "Ubuntu $VERSION_ID not supported. Need 22.04 or 24.04." ;;
  esac
  if is_wsl; then warn "WSL detected"; fi
}

# ── Docker ────────────────────────────────────────────────────────────────────
install_docker() {
  if command -v docker &>/dev/null; then
    log "Docker already installed: $(docker --version)"
    return
  fi

  if is_wsl; then
    error "Docker not found in WSL. Install Docker Desktop for Windows and enable WSL2 integration:
  https://docs.docker.com/desktop/wsl/
Then re-run this script."
  fi

  info "Installing Docker Engine..."
  sudo apt-get update -qq
  sudo apt-get install -y -qq ca-certificates curl gnupg
  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | \
    sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  sudo chmod a+r /etc/apt/keyrings/docker.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
    https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
    sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
  sudo apt-get update -qq
  sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io
  sudo usermod -aG docker "$USER"
  log "Docker Engine installed"
  warn "Run 'newgrp docker' or log out/in for Docker group to take effect"
}

# ── Qdrant ────────────────────────────────────────────────────────────────────
install_qdrant() {
  mkdir -p "$QDRANT_STORAGE"

  # Already running
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^qdrant$'; then
    log "Qdrant already running (port 6333)"
    return
  fi

  # Container exists but stopped — just start it
  if docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q '^qdrant$'; then
    info "Starting existing Qdrant container..."
    docker start qdrant
    log "Qdrant started (port 6333)"
    return
  fi

  info "Pulling and starting Qdrant..."
  docker run -d \
    --name qdrant \
    --restart unless-stopped \
    -p 6333:6333 \
    -p 6334:6334 \
    -v "$QDRANT_STORAGE:/qdrant/storage:z" \
    qdrant/qdrant

  log "Qdrant started (port 6333, storage: $QDRANT_STORAGE)"

  info "Waiting for Qdrant to be ready..."
  for i in $(seq 1 30); do
    if curl -sf http://localhost:6333/healthz &>/dev/null; then
      log "Qdrant is ready"
      return
    fi
    sleep 1
  done
  warn "Qdrant may still be starting. Check: docker logs qdrant"
}

# ── Ollama ────────────────────────────────────────────────────────────────────
install_ollama() {
  if ! command -v ollama &>/dev/null; then
    info "Installing Ollama..."
    curl -fsSL https://ollama.com/install.sh | sh
    log "Ollama installed"
  else
    log "Ollama already installed"
  fi

  if has_systemd; then
    if ! systemctl is-active --quiet ollama 2>/dev/null; then
      sudo systemctl enable --now ollama
    fi
    log "Ollama service running (systemd)"
  else
    if ! pgrep -x ollama &>/dev/null; then
      info "Starting Ollama in background..."
      nohup ollama serve > /tmp/ollama.log 2>&1 &
      sleep 3
    fi
    log "Ollama running (background)"
    warn "WSL without systemd — add to ~/.bashrc for auto-start:"
    warn "  pgrep ollama &>/dev/null || nohup ollama serve > /dev/null 2>&1 &"
  fi

  info "Waiting for Ollama to be ready..."
  for i in $(seq 1 30); do
    if curl -sf http://localhost:11434/ &>/dev/null; then break; fi
    sleep 1
  done

  info "Pulling $EMBEDDING_MODEL (may take a few minutes on first run)..."
  ollama pull "$EMBEDDING_MODEL"
  log "Model $EMBEDDING_MODEL ready"
}

# ── Summary ───────────────────────────────────────────────────────────────────
print_summary() {
  echo ""
  echo -e "${BOLD}${GREEN}══ Prerequisites ready ══${NC}"
  echo ""
  echo -e "  ${GREEN}✓${NC} Qdrant  : http://localhost:6333  (storage: $QDRANT_STORAGE)"
  echo -e "  ${GREEN}✓${NC} Ollama  : http://localhost:11434  (model: $EMBEDDING_MODEL)"
  echo ""
  echo -e "${BOLD}Next steps:${NC}"
  echo -e "  Install zed-rag:  bash install.sh"
  echo -e "  Index project:    zed-rag index /path/to/your/project"
  echo -e "  Watch project:    zed-rag watch /path/to/your/project"
  echo ""
}

# ── Main ──────────────────────────────────────────────────────────────────────
main() {
  echo -e "${BOLD}${CYAN}"
  echo "  ╔══════════════════════════════════╗"
  echo "  ║   zed-rag  prerequisites         ║"
  echo "  ╚══════════════════════════════════╝"
  echo -e "${NC}"

  check_os
  install_bootstrap_deps
  install_docker
  install_qdrant
  install_ollama
  print_summary
}

main "$@"
