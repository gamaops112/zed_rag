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

// ProgressFunc is called after each file is processed during a full index.
// done = files processed so far, total = total indexable files, path = current file.
type ProgressFunc func(done, total int, path string)

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

// IndexFile chunks, embeds, and upserts a single file.
// Returns (true, nil) if indexed, (false, nil) if skipped (hash unchanged), or (false, err).
func (i *Indexer) IndexFile(ctx context.Context, filePath string) (bool, error) {
	chunks, err := i.chunker.ChunkFile(filePath)
	if err != nil {
		return false, fmt.Errorf("chunk %s: %w", filePath, err)
	}
	if len(chunks) == 0 {
		return false, nil
	}

	existing, _ := i.qdrant.GetFileHash(ctx, filePath)
	if existing != "" && existing == chunks[0].Hash {
		return false, nil // already up-to-date
	}

	texts := make([]string, len(chunks))
	for j, c := range chunks {
		texts[j] = c.Content
	}
	vectors, err := i.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return false, fmt.Errorf("embed %s: %w", filePath, err)
	}
	return true, i.qdrant.Upsert(ctx, chunks, vectors)
}

// CountIndexable returns the number of files that would be indexed (not ignored, detectable language).
func (i *Indexer) CountIndexable(ctx context.Context) (int, error) {
	var count int
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
		if !i.chunker.ShouldSkip(path) && i.chunker.DetectLanguage(path) != "plain_text" {
			count++
		}
		return nil
	})
	return count, err
}

// IndexResult holds summary counts from a full index run.
type IndexResult struct {
	Indexed int
	UpToDate int
	Errors  []string // "path: error message" for files that failed
}

// IndexAll walks the project and indexes all non-ignored source files.
func (i *Indexer) IndexAll(ctx context.Context) error {
	_, err := i.IndexAllWithProgress(ctx, nil)
	return err
}

// IndexAllWithProgress is like IndexAll but calls fn after each file is processed.
// Returns an IndexResult summary and the walk error (if any).
func (i *Indexer) IndexAllWithProgress(ctx context.Context, fn ProgressFunc) (IndexResult, error) {
	total, err := i.CountIndexable(ctx)
	if err != nil && err != ctx.Err() {
		return IndexResult{}, err
	}

	log.Printf("Indexer: starting full index of %s (%d files)", i.projectPath, total)
	var res IndexResult
	var done int

	err = filepath.WalkDir(i.projectPath, func(path string, d os.DirEntry, err error) error {
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
		if i.chunker.ShouldSkip(path) || i.chunker.DetectLanguage(path) == "plain_text" {
			return nil
		}

		done++
		ok, indexErr := i.IndexFile(ctx, path)
		if indexErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", path, indexErr))
		} else if ok {
			res.Indexed++
		} else {
			res.UpToDate++
		}

		if fn != nil {
			fn(done, total, path)
		}
		return nil
	})

	log.Printf("Indexer: done — %d indexed, %d up-to-date, %d errors", res.Indexed, res.UpToDate, len(res.Errors))
	return res, err
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
			ok, err := i.IndexFile(ctx, filePath)
			if err != nil {
				log.Printf("Indexer: failed to index %s: %v", filePath, err)
			} else if ok {
				log.Printf("Indexer: re-indexed %s", filePath)
			}
		}
	}
}
