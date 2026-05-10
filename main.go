package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"zed-rag/config"
	"zed-rag/internal/dashboard"
	"zed-rag/internal/embedder"
	"zed-rag/internal/indexer"
	"zed-rag/internal/mcp"
	"zed-rag/internal/metrics"
	"zed-rag/internal/qdrant"
	"zed-rag/internal/resolver"
	"zed-rag/internal/watcher"
)

const usage = `zed-rag — codebase RAG context server for coding agents

Usage:
  zed-rag                      Start MCP server (spawned by continue.dev)
  zed-rag index [flags] [path] Index a project once then exit (path defaults to .)
  zed-rag watch [path]         Index then watch for file changes (run in terminal)
  zed-rag remove [path]        Remove all indexed data for a project
  zed-rag serve [path]         Full daemon: watch + dashboard (for systemd)
  zed-rag status [path]        Show indexed vector count for a project

Index flags:
  --force   Delete existing index and re-index from scratch

Environment variables (all modes):
  PROJECT_PATH       Default project path when not given as argument
  QDRANT_URL         Override qdrant URL from config
  OLLAMA_URL         Override ollama URL from config
  EMBEDDING_MODEL    Override embedding model from config
`

func main() {
	if len(os.Args) < 2 {
		runMCP()
		return
	}
	switch os.Args[1] {
	case "index":
		runIndex(os.Args[2:])
	case "watch":
		runWatch(os.Args[2:])
	case "remove":
		runRemove(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "--help", "-h", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

// runMCP is the default mode: pure MCP server over stdin/stdout, no indexing.
// Spawned by continue.dev per chat session.
func runMCP() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "zed-rag: %v\n", err)
		os.Exit(1)
	}
	setupFileLog(cfg.LogPath)
	log.Println("---")
	log.Printf("MCP: start project=%s", cfg.ProjectPath)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, cfg.ProjectPath)
	e := embedder.New(cfg.OllamaURL, cfg.EmbeddingModel)
	res := resolver.New(q, e)

	metricsChan := make(chan metrics.Metric, 100)
	go drainMetrics(metricsChan)

	srv := mcp.New(res, metricsChan, cfg.ProjectPath)
	if err := srv.Start(ctx); err != nil && err != ctx.Err() {
		log.Printf("MCP: error: %v", err)
	}
	log.Println("MCP: done")
}

// runIndex indexes a project and exits.
func runIndex(args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	force := fs.Bool("force", false, "delete existing index and re-index from scratch")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.Parse(args)

	projectPath := resolveProjectPath(fs.Args())

	cfg, err := config.LoadInfra()
	if err != nil {
		fatal(err)
	}

	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, projectPath)
	e := embedder.New(cfg.OllamaURL, cfg.EmbeddingModel)
	idx := indexer.New(projectPath, e, q)

	if err := idx.EnsureCollection(ctx); err != nil {
		fatal(fmt.Errorf("ensure collection: %w", err))
	}

	if *force {
		fmt.Printf("Clearing existing index for %s ...\n", projectPath)
		if err := q.DeleteProject(ctx); err != nil {
			fatal(fmt.Errorf("clear index: %w", err))
		}
	}

	fmt.Printf("Indexing %s ...\n", projectPath)
	if err := idx.IndexAll(ctx); err != nil && err != ctx.Err() {
		fatal(err)
	}
	fmt.Println("Done.")
}

// runRemove deletes all indexed vectors for a project and exits.
func runRemove(args []string) {
	projectPath := resolveProjectPath(args)

	cfg, err := config.LoadInfra()
	if err != nil {
		fatal(err)
	}

	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, projectPath)

	fmt.Printf("Removing index for %s ...\n", projectPath)
	if err := q.DeleteProject(ctx); err != nil {
		fatal(err)
	}
	fmt.Printf("Done. All vectors for %s removed.\n", projectPath)
}

// runWatch indexes a project then watches for file changes, re-indexing on saves.
// Logs to stderr — intended for interactive terminal use.
func runWatch(args []string) {
	projectPath := resolveProjectPath(args)

	cfg, err := config.LoadInfra()
	if err != nil {
		fatal(err)
	}

	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, projectPath)
	e := embedder.New(cfg.OllamaURL, cfg.EmbeddingModel)
	idx := indexer.New(projectPath, e, q)

	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if err := idx.EnsureCollection(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "EnsureCollection attempt %d: %v — retry in 10s\n", attempt, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		break
	}

	fmt.Fprintf(os.Stderr, "Indexing %s ...\n", projectPath)
	if err := idx.IndexAll(ctx); err != nil && err != ctx.Err() {
		fmt.Fprintf(os.Stderr, "IndexAll error: %v\n", err)
	}

	fileChanges := make(chan string, 200)
	fw, err := watcher.New(projectPath, fileChanges)
	if err != nil {
		fatal(err)
	}

	fmt.Fprintf(os.Stderr, "Watching %s for changes (Ctrl+C to stop) ...\n", projectPath)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := fw.Start(ctx); err != nil && err != ctx.Err() {
			log.Printf("watcher: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.Start(ctx, fileChanges)
	}()

	wg.Wait()
	fmt.Fprintln(os.Stderr, "Watch stopped.")
}

// runStatus prints indexed vector count for a project and exits.
func runStatus(args []string) {
	projectPath := resolveProjectPath(args)

	cfg, err := config.LoadInfra()
	if err != nil {
		fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, projectPath)
	stats, err := q.CollectionStats(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("Project : %s\n", projectPath)
	fmt.Printf("Vectors : %v\n", stats["points_count"])
}

// runServe runs the persistent daemon: initial index + watcher + dashboard.
// Intended to be run by systemd. Does NOT start an MCP server.
func runServe(args []string) {
	projectPath := resolveProjectPath(args)

	cfg, err := config.LoadInfra()
	if err != nil {
		fatal(err)
	}
	cfg.ProjectPath = projectPath

	setupFileLog(cfg.LogPath)
	log.Println("---")
	log.Printf("Serve: start project=%s port=%d", projectPath, cfg.DashboardPort)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	q := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, projectPath)
	e := embedder.New(cfg.OllamaURL, cfg.EmbeddingModel)

	homeDir, _ := os.UserHomeDir()
	store, err := metrics.New(filepath.Join(homeDir, ".zed-rag", "metrics.db"))
	if err != nil {
		fatal(err)
	}
	defer store.Close()

	metricsChan := make(chan metrics.Metric, 100)
	tracker := metrics.NewTracker(store, metricsChan, projectPath)
	res := resolver.New(q, e)
	idx := indexer.New(projectPath, e, q)

	fileChanges := make(chan string, 200)
	fw, err := watcher.New(projectPath, fileChanges)
	if err != nil {
		fatal(err)
	}

	dash := dashboard.New(cfg.DashboardPort, tracker, q, e, res)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		tracker.Start(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := fw.Start(ctx); err != nil && err != ctx.Err() {
			log.Printf("watcher: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		idx.Start(ctx, fileChanges)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := dash.Start(ctx); err != nil && err != http.ErrServerClosed {
			log.Printf("dashboard: %v", err)
		}
	}()

	// Initial index with retry on Qdrant unavailability.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for attempt := 1; ; attempt++ {
			if ctx.Err() != nil {
				return
			}
			if err := idx.EnsureCollection(ctx); err != nil {
				log.Printf("EnsureCollection attempt %d: %v — retry in 10s", attempt, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}
			break
		}
		if err := idx.IndexAll(ctx); err != nil && err != ctx.Err() {
			log.Printf("IndexAll: %v", err)
		}
	}()

	wg.Wait()
	close(metricsChan)
	log.Println("Serve: stopped")
}

// resolveProjectPath returns the absolute path from CLI args, PROJECT_PATH env, or cwd.
func resolveProjectPath(args []string) string {
	p := "."
	if len(args) > 0 {
		p = args[0]
	} else if env := os.Getenv("PROJECT_PATH"); env != "" {
		p = env
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		fatal(fmt.Errorf("resolve path: %w", err))
	}
	return abs
}

// setupFileLog redirects log output to a file. Stdout is reserved for MCP JSON-RPC.
func setupFileLog(logDir string) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "create log dir: %v\n", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "zed-rag.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

func drainMetrics(ch <-chan metrics.Metric) {
	for range ch {
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
