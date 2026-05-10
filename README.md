# zed-rag

> Local codebase RAG for coding agents. Indexes your code into a vector DB and serves it as context via MCP — so your AI assistant actually understands your codebase.

![Build](https://github.com/gamaops112/zed_rag/actions/workflows/release.yml/badge.svg)
![License](https://img.shields.io/badge/license-MIT-blue)
![Platform](https://img.shields.io/badge/platform-Ubuntu%20%7C%20WSL2-informational)
![Go](https://img.shields.io/badge/go-1.22+-00ADD8)

---

## What it does

When you ask your coding agent a question, it has no idea what's in your codebase unless you paste it in manually. zed-rag fixes that.

It chunks your source files, embeds them with a local model via Ollama, stores vectors in Qdrant, and exposes a single MCP tool — `search_codebase` — that continue.dev calls automatically on every query. Everything runs on your machine. No API keys. No data leaves.

```
your files → Ollama (embeddings) → Qdrant (vector store) → zed-rag MCP → continue.dev
```

---

## Prerequisites

- Ubuntu 22.04 / 24.04 or WSL2 Ubuntu
- Docker (for Qdrant) — Docker Desktop on Windows if using WSL2

Install everything in one shot:

```bash
curl -fsSL https://github.com/gamaops112/zed_rag/releases/latest/download/setup.sh | bash
```

This installs Docker Engine (native Ubuntu), starts Qdrant as a Docker container with persistent storage, installs Ollama as a systemd service, and pulls the `nomic-embed-text` embedding model.

---

## Installation

```bash
curl -fsSL https://github.com/gamaops112/zed_rag/releases/latest/download/install.sh | bash
```

Installs the `zed-rag` binary to `/usr/local/bin`, writes default config to `~/.zed-rag/config.toml`, and sets up a systemd user service.

---

## Quick start

```bash
# 1. Index your project
zed-rag index /path/to/your/project

# 2. Watch for file changes (re-indexes on save)
zed-rag watch /path/to/your/project

# 3. Configure continue.dev (see below) — done
```

---

## CLI

| Command | Description |
|---|---|
| `zed-rag index [path]` | Index a project once then exit. `--force` clears and re-indexes from scratch. |
| `zed-rag watch [path]` | Index then watch for file changes. Re-indexes saved files automatically. |
| `zed-rag remove [path]` | Delete all indexed vectors for a project from Qdrant. |
| `zed-rag serve [path]` | Full daemon: watch + dashboard on `:7702`. Used by the systemd service. |
| `zed-rag status [path]` | Show indexed vector count for a project. |
| `zed-rag` *(no args)* | MCP server over stdin/stdout. Spawned automatically by continue.dev. |

All path arguments default to `PROJECT_PATH` env var, then current directory.

```bash
# Remove a stale or test project
zed-rag remove /path/to/old-project

# Force re-index (e.g. after changing embedding model)
zed-rag index --force /path/to/project
```

---

## continue.dev MCP config

### WSL2 (Windows + VS Code Remote WSL)

continue.dev runs on the Windows side. Edit `C:\Users\<you>\.continue\config.yaml`:

```yaml
name: AI Models Configuration
version: 1.0.0
schema: v1

mcpServers:
  - name: zed-rag
    command: wsl
    args:
      - /usr/local/bin/zed-rag
    env:
      PROJECT_PATH: /home/user/your-project
```

### Native Ubuntu / VS Code Remote SSH

Edit `~/.continue/config.yaml`:

```yaml
name: AI Models Configuration
version: 1.0.0
schema: v1

mcpServers:
  - name: zed-rag
    command: /usr/local/bin/zed-rag
    args: []
    env:
      PROJECT_PATH: /home/user/your-project
```

**Multiple projects:** add multiple `mcpServers` entries with different `PROJECT_PATH` values. All share a single Qdrant collection — data is scoped per project path.

---

## Configuration

Default config is written to `~/.zed-rag/config.toml` on install:

```toml
qdrant_url        = "http://localhost:6333"
qdrant_collection = "codebase"
ollama_url        = "http://localhost:11434"
embedding_model   = "nomic-embed-text"
dashboard_port    = 7702

# project_path is set per-command (CLI arg or PROJECT_PATH env var)
# Uncomment to set a default for MCP mode:
# project_path = "/path/to/your/project"
```

All values can be overridden with environment variables:

```bash
QDRANT_URL=http://localhost:6333
OLLAMA_URL=http://localhost:11434
EMBEDDING_MODEL=nomic-embed-text
PROJECT_PATH=/path/to/project
```

---

## Dashboard

Run `zed-rag serve` (or enable the systemd service) to get a dashboard at `http://localhost:7702` showing indexed vector count, top retrieved files, and query history.

```bash
# Enable the systemd user service
systemctl --user edit zed-rag        # set PROJECT_PATH
systemctl --user enable --now zed-rag

# View logs
journalctl --user -u zed-rag -f
```

---

## How indexing works

- Files are chunked by language-aware rules (functions, classes, blocks)
- Each chunk is embedded via Ollama (`nomic-embed-text`, 768 dimensions)
- Chunks are stored in Qdrant tagged with their `project_path` and a content hash
- On re-index, unchanged files are skipped via hash comparison — only modified files are re-embedded
- `zed-rag` (MCP mode) never indexes — it only queries. Indexing is explicit via `index` / `watch`

---

## Build from source

```bash
git clone https://github.com/gamaops112/zed_rag.git
cd zed-rag
go build -o zed-rag .
```

Requires Go 1.22+.

---

## License

MIT
