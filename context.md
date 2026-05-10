# zed-rag — Complete Build Plan
 
## Overview
 
A Go binary that acts as an MCP server for Zed editor.
It indexes your codebase into Qdrant, watches for file changes,
resolves simple queries locally, and sends complex queries to AI
with only relevant context — reducing token usage by ~90%.
 
---
 
## How It Works
 
```
Zed opens project
      ↓
Spawns zed-rag via stdio (one process per project)
      ↓
zed-rag reads PROJECT_PATH from env
      ↓
Checks Qdrant — indexes only changed files (hash check)
      ↓
Starts 3 goroutines:
  ├── Watcher    → detects file changes → reindexes
  ├── Indexer    → chunks + embeds + stores in Qdrant
  └── Dashboard  → serves http://localhost:7702
      ↓
Main goroutine handles MCP (stdio)
  ├── Intent classifier → local or AI?
  ├── Local resolver   → Qdrant only
  └── AI resolver      → Qdrant chunks + Deepseek/Gemini
      ↓
Zed closes → stdin EOF → context.cancel() → all goroutines stop
```
 
---
 
## Project Structure
 
```
zed-rag/
├── main.go
├── go.mod
├── go.sum
├── config/
│   └── config.go
├── cmd/
│   ├── index.go
│   └── version.go
├── internal/
│   ├── chunker/
│   │   └── chunker.go
│   ├── embedder/
│   │   └── embedder.go
│   ├── qdrant/
│   │   └── qdrant.go
│   ├── watcher/
│   │   └── watcher.go
│   ├── intent/
│   │   └── classifier.go
│   ├── resolver/
│   │   ├── local.go
│   │   └── ai.go
│   ├── mcp/
│   │   └── server.go
│   ├── metrics/
│   │   ├── tracker.go
│   │   └── storage.go
│   └── dashboard/
│       ├── server.go
│       └── static/
│           └── index.html
└── .zed/
    └── settings.json  ← example Zed config
```
 
---
