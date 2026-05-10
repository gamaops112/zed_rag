package indexer

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"zed-rag/internal/chunker"
	"zed-rag/internal/embedder"
	"zed-rag/internal/qdrant"
)

// Indexer chunks files, embeds them, and upserts into Qdrant.
type Indexer struct {
	projectPath string
	chunker     *chunker.Chunker
	embedder    *embedder.Embedder
	qdrant      *qdrant.Client
}

// New creates a new Indexer.
func New(projectPath string, e *embedder.Embedder, q *qdrant.Client) *Indexer {
	return &Indexer{
		projectPath: projectPath,
		chunker:     chunker.New(projectPath, 100),
		embedder:    e,
		qdrant:      q,
	}
}

// EnsureCollection embeds a probe string to detect vector size, then creates the collection if absent.
func (i *Indexer) EnsureCollection(ctx context.Context) error {
	vec, err := i.embedder.Embed(ctx, "hello")
	if err != nil {
		return fmt.Errorf("failed to probe embedding dimension: %w", err)
	}
	return i.qdrant.EnsureCollection(ctx, len(vec))
}

// IndexFile chunks, embeds, and upserts a single file into Qdrant.
// Skips if the file content hash matches what's already stored.
func (i *Indexer) IndexFile(ctx context.Context, filePath string) error {
	chunks, err := i.chunker.ChunkFile(filePath)
	if err != nil {
		return fmt.Errorf("chunk %s: %w", filePath, err)
	}
	if len(chunks) == 0 {
		return nil
	}

	// Skip if already indexed with same content hash.
	existing, _ := i.qdrant.GetFileHash(ctx, filePath)
	if existing != "" && existing == chunks[0].Hash {
		return nil
	}

	texts := make([]string, len(chunks))
	for j, c := range chunks {
		texts[j] = c.Content
	}
	vectors, err := i.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed %s: %w", filePath, err)
	}
	return i.qdrant.Upsert(ctx, chunks, vectors)
}

// IndexAll walks the project directory and indexes all non-ignored source files.
func (i *Indexer) IndexAll(ctx context.Context) error {
	log.Printf("Indexer: starting full index of %s", i.projectPath)
	var indexed, skipped int
	err := filepath.WalkDir(i.projectPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if i.chunker.ShouldSkip(path) && path != i.projectPath {
				return filepath.SkipDir
			}
			return nil
		}
		if i.chunker.ShouldSkip(path) {
			return nil
		}
		if i.chunker.DetectLanguage(path) == "plain_text" {
			return nil
		}
		before := indexed
		if err := i.IndexFile(ctx, path); err != nil {
			log.Printf("Indexer: skip %s: %v", path, err)
		} else if indexed == before {
			skipped++ // hash matched, already up to date
		} else {
			indexed++
		}
		return nil
	})
	log.Printf("Indexer: full index complete — %d indexed, %d already up-to-date", indexed, skipped)
	return err
}

// Start processes file change events until ctx is cancelled or the channel closes.
func (i *Indexer) Start(ctx context.Context, fileChanges <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case filePath, ok := <-fileChanges:
			if !ok {
				return
			}
			if err := i.IndexFile(ctx, filePath); err != nil {
				log.Printf("Indexer: failed to index %s: %v", filePath, err)
			} else {
				log.Printf("Indexer: re-indexed %s", filePath)
			}
		}
	}
}
