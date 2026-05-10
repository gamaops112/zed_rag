# zed-rag — Rules for AI Editors

## Module

Module name: `zed-rag`  
Go version: 1.22  
Single `go.mod` at root. No sub-modules, no `replace` directives.

---

## Package Layout (do not move or duplicate files)

```
internal/
├── chunker/        → package chunker
├── embedder/       → package embedder
├── intent/         → package intent       ← classifier.go lives HERE
├── mcp/            → package mcp          ← server.go lives HERE
├── metrics/        → package metrics      ← tracker.go AND storage.go live HERE
├── qdrant/         → package qdrant
├── resolver/       → package resolver
└── watcher/        → package watcher
```

### Critical rule: Go package identity = module path + directory

Two `.go` files with `package metrics` in **different directories** are **two different packages** with different import paths. They cannot see each other's types without an explicit import.

- `internal/metrics/` → import path `zed-rag/internal/metrics` ✓
- Any other directory → different package, different import path ✗

Never place files in subdirectories that don't match `context.md`.  
Never copy a type into a second file as a "workaround" — fix the directory instead.

---

## metrics package API

### `storage.go` — `Storage`

Constructor: `metrics.New(dbPath string) (*Storage, error)`

Methods:
```
Insert(m Metric) error
IncrementFileHit(project, file string) error
QueryPerHour(lastHours int) ([]int, error)
TopFiles(project string, limit int) ([]string, error)
Close() error
```

Deleted (do not call or recreate):
- `SaveMetric`, `GetMetricsCountByType`, `GetAverageDurationByType`
- `GetHourlyCounts`, `GetTopFilesByQueryCount`, `NewStorage`

### `tracker.go` — `Tracker`

Constructor: `NewTracker(storage *Storage, metricsChan chan Metric, projectPath string) *Tracker`

`Metric` struct is defined **once** in `tracker.go`. Do not redeclare it in `storage.go`.

`refreshSnapshot` does **not** take a `context.Context` (storage methods don't need it).

---

## intent package API

Import path: `zed-rag/internal/intent`

```go
c := intent.New()
result := c.Classify(query)   // result.Type is intent.IntentLocal or intent.IntentAI
```

---

## mcp package

`server.go` imports:
```go
"zed-rag/internal/intent"
"zed-rag/internal/metrics"
"zed-rag/internal/resolver"
```

MCP `tools/call` param keys: `"name"` for tool name, `"arguments"` for tool args.  
Not `"tool"` and not flat params.

---

## Common mistakes to avoid

| Wrong | Correct |
|-------|---------|
| Place `tracker.go` in `internal/embedder/intent/metrics/` | Place in `internal/metrics/` |
| Place `classifier.go` in `internal/embedder/intent/` | Place in `internal/intent/` |
| Copy `Metric` struct into `storage.go` as workaround | Fix the directory |
| Call `storage.SaveMetric(ctx, m)` | Call `storage.Insert(m)` |
| Increment `TotalQueries` for all metric types | Only increment inside `case "query":` |
| Use `TotalQueries` as denominator for embed/search avg | Use separate `embedCount`/`searchCount` |
| Use `\\n` in `fmt.Fprintf` format strings | Use `\n` |

---

## Goroutine model

Four concurrent components — all share one root `context.Context`:

1. **Watcher** — `fsnotify` watches project path, sends changed file paths to channel
2. **Indexer** — receives file paths, chunks → embeds → upserts into Qdrant
3. **Dashboard** — HTTP server on `localhost:7702`
4. **MCP server** — main goroutine, reads JSON-RPC from stdin, writes to stdout

Shutdown: stdin EOF → `context.cancel()` → all goroutines return.

---

## Metrics flow

```
any component → metrics.Metric{} → chan Metric → Tracker.Start loop
                                                      ├── updateSnapshot (in-memory)
                                                      ├── storage.Insert (persist row)
                                                      └── storage.IncrementFileHit (if FilePath set)
```
