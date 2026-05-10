package watcher

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zed-rag/internal/chunker" // To leverage chunker's ignore logic

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors a project directory for file system changes and
// sends relevant file paths to a channel for indexing.
type Watcher struct {
	projectPath string
	chunker     *chunker.Chunker       // Used to check shouldSkip logic
	fileChanges chan<- string          // Channel to send changed file paths
	fsWatcher   *fsnotify.Watcher      // fsnotify watcher instance
	debounce    map[string]*time.Timer // Debounce timers per file path
	debounceMu  sync.Mutex             // Mutex for debounce map
}

const debounceDuration = 300 * time.Millisecond

// New creates and initializes a new Watcher.
// It sets up an fsnotify watcher and recursively adds all subdirectories.
func New(projectPath string, fileChanges chan<- string) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	// Initialize a chunker to reuse its shouldSkip logic for directories/files
	// MaxChunkSize doesn't matter here as we only use shouldSkip.
	c := chunker.New(projectPath, 100)

	w := &Watcher{
		projectPath: projectPath,
		chunker:     c,
		fileChanges: fileChanges,
		fsWatcher:   fsWatcher,
		debounce:    make(map[string]*time.Timer),
	}

	// Walk the project directory and add all relevant subdirectories to the watcher
	err = filepath.WalkDir(projectPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Log the error but continue walking
			log.Printf("warning: preventing walk into %s: %v", path, err)
			return filepath.SkipDir
		}

		// Skip directories that match ignore patterns
		if d.IsDir() && w.chunker.ShouldSkip(path) && path != projectPath {
			return filepath.SkipDir
		}

		if d.IsDir() {
			if err := w.addDir(path); err != nil {
				log.Printf("warning: failed to add directory %s to watcher: %v", path, err)
			}
		}
		return nil
	})

	if err != nil {
		w.fsWatcher.Close()
		return nil, fmt.Errorf("failed to walk project directory %s: %w", projectPath, err)
	}

	return w, nil
}

// Start begins the event loop, listening for file system events.
// It blocks until the provided context is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	defer w.fsWatcher.Close()

	for {
		select {
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return nil // Watcher closed
			}
			w.handleEvent(event)
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return nil // Watcher closed
			}
			log.Printf("watcher error: %v", err)
		case <-ctx.Done():
			log.Println("File watcher stopping...")
			return ctx.Err()
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Filter out events for ignored paths or non-indexable files
	if w.chunker.ShouldSkip(event.Name) {
		return
	}

	// Special handling for directories (e.g., new directories created)
	if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
		if event.Op&fsnotify.Create == fsnotify.Create {
			// Recursively add newly created directories
			_ = filepath.WalkDir(event.Name, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					log.Printf("warning: preventing walk into %s (new dir event): %v", path, err)
					return filepath.SkipDir
				}
				if d.IsDir() && w.shouldWatch(path) { // shouldWatch for dirs means it's not ignored
					if err := w.addDir(path); err != nil {
						log.Printf("warning: failed to add new directory %s to watcher: %v", path, err)
					}
				}
				return nil
			})
		}
		return // Don't process directory events further as file changes
	}

	// Process file events
	if w.shouldWatch(event.Name) {
		switch {
		case event.Op&fsnotify.Create == fsnotify.Create ||
			event.Op&fsnotify.Write == fsnotify.Write:
			w.debounceFile(event.Name)
		case event.Op&fsnotify.Remove == fsnotify.Remove ||
			event.Op&fsnotify.Rename == fsnotify.Rename:
			// For REMOVE/RENAME, send immediately for deletion (no debounce needed)
			// The indexer will distinguish between update (via new hash) and delete
			w.fileChanges <- event.Name
		}
	}
}

// addDir adds a directory to the fsnotify watcher.
// It checks if the directory itself should be watched (i.e., not ignored).
func (w *Watcher) addDir(path string) error {
	if !w.chunker.ShouldSkip(path) { // Only add if the directory is not globally ignored
		err := w.fsWatcher.Add(path)
		if err != nil {
			return fmt.Errorf("failed to add path %s to watcher: %w", path, err)
		}
		// fmt.Printf("Watching: %s\n", path) // For debugging
	}
	return nil
}

// shouldWatch checks if a file should be actively watched and indexed.
// It combines language detection and ignore list checks.
func (w *Watcher) shouldWatch(filePath string) bool {
	// Use chunker's shouldSkip logic
	if w.chunker.ShouldSkip(filePath) {
		return false
	}

	// Additionally, only index certain file types
	language := w.chunker.DetectLanguage(filePath)
	switch language {
	case "go", "rust", "python", "javascript", "typescript", "jsx", "tsx", "svelte",
		"markdown", "html", "css", "json", "yaml", "xml":
		return true
	default:
		return false
	}
}

// debounceFile resets a timer for the given file path.
// The file path is sent to the fileChanges channel only after the debounceDuration has passed
// without further changes to that specific file.
func (w *Watcher) debounceFile(filePath string) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()

	if timer, ok := w.debounce[filePath]; ok {
		timer.Stop()
	}

	w.debounce[filePath] = time.AfterFunc(debounceDuration, func() {
		w.fileChanges <- filePath
		w.debounceMu.Lock()
		delete(w.debounce, filePath)
		w.debounceMu.Unlock()
	})
}
