package chunker

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Chunk represents a meaningful segment of a source file.
type Chunk struct {
	ID          string    `json:"id"`
	FilePath    string    `json:"file_path"`
	ProjectPath string    `json:"project_path"` // Base path of the project for relative paths
	Language    string    `json:"language"`
	Content     string    `json:"content"`
	StartLine   int       `json:"start_line"`
	EndLine     int       `json:"end_line"`
	Hash        string    `json:"hash"` // MD5 hash of the content for change detection
	IndexedAt   time.Time `json:"indexed_at"`
}

const patternsTTL = 30 * time.Second

// Chunker is responsible for splitting source files into Chunks.
type Chunker struct {
	maxChunkSize   int
	ignorePatterns []*regexp.Regexp
	projectPath    string
	customPatterns []string // refreshed from .zed-rag-ignore every patternsTTL
	patternsMu     sync.RWMutex
	patternsLoadedAt time.Time
}

// hardcodedIgnoreDirs are directory name substrings that are always skipped.
var hardcodedIgnoreDirs = []string{
	"node_modules", "target", "dist", "build",
	"__pycache__", "vendor", "coverage", "tmp", "temp", "logs",
	"__generated__", ".cache",
}

// New creates and initializes a new Chunker.
func New(projectPath string, maxChunkSize int) *Chunker {
	if maxChunkSize <= 0 {
		maxChunkSize = 100
	}
	c := &Chunker{
		maxChunkSize: maxChunkSize,
		projectPath:  projectPath,
	}
	c.reloadIgnoreFile() // initial load
	return c
}

// reloadIgnoreFile reads .zed-rag-ignore and updates customPatterns.
// Called under write lock.
func (c *Chunker) reloadIgnoreFile() {
	ignoreFile := filepath.Join(c.projectPath, ".zed-rag-ignore")
	data, err := ioutil.ReadFile(ignoreFile)
	var patterns []string
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}
	c.customPatterns = patterns
	c.patternsLoadedAt = time.Now()
}

// refreshPatterns reloads .zed-rag-ignore if patternsTTL has elapsed.
func (c *Chunker) refreshPatterns() {
	c.patternsMu.Lock()
	defer c.patternsMu.Unlock()
	if time.Since(c.patternsLoadedAt) >= patternsTTL {
		c.reloadIgnoreFile()
	}
}

// maxFileSizeBytes skips files larger than this — likely generated/minified, would exceed embed context.
const maxFileSizeBytes = 512 * 1024 // 512 KB

// ChunkFile reads a file and splits its content into a slice of Chunks based on language-specific rules.
func (c *Chunker) ChunkFile(filePath string) ([]Chunk, error) {
	if c.ShouldSkip(filePath) {
		return nil, nil
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", filePath, err)
	}
	if info.Size() > maxFileSizeBytes {
		return nil, nil // too large to embed — skip silently
	}

	contentBytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}
	content := string(contentBytes)
	language := c.DetectLanguage(filePath) // Corrected call

	var chunks []Chunk
	var rawChunks []string
	var startLines []int

	switch language {
	case "go":
		rawChunks, startLines = c.chunkGo(content, filePath)
	case "rust":
		rawChunks, startLines = c.chunkRust(content, filePath)
	case "python":
		rawChunks, startLines = c.chunkPython(content, filePath)
	case "javascript", "typescript", "jsx", "tsx":
		rawChunks, startLines = c.chunkJS(content, filePath)
	case "svelte":
		rawChunks, startLines = c.chunkSvelte(content, filePath)
	default:
		rawChunks, startLines = c.chunkGeneric(content, filePath)
	}

	projectRelativePath, err := filepath.Rel(c.projectPath, filePath)
	if err != nil {
		projectRelativePath = filePath // Fallback if cannot get relative path
	}

	for i, chunkContent := range rawChunks {
		chunkStartLine := startLines[i]
		chunkEndLine := chunkStartLine + strings.Count(chunkContent, "\n")
		if !strings.HasSuffix(chunkContent, "\n") {
			chunkEndLine++ // Adjust for last line if no newline
		}

		hash := c.hashContent(chunkContent)

		// Create a unique ID for the chunk (e.g., hash of filepath + chunk hash + start line)
		chunkID := c.hashContent(fmt.Sprintf("%s-%s-%d", projectRelativePath, hash, chunkStartLine))

		chunks = append(chunks, Chunk{
			ID:          chunkID,
			FilePath:    projectRelativePath,
			ProjectPath: c.projectPath,
			Language:    language,
			Content:     chunkContent,
			StartLine:   chunkStartLine,
			EndLine:     chunkEndLine,
			Hash:        hash,
			IndexedAt:   time.Now(),
		})
	}

	return chunks, nil
}

// chunkGo splits Go content by function and type declarations.
func (c *Chunker) chunkGo(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int
	lines := strings.Split(content, "\n")

	// Regex to find 'func' or 'type' declarations, ensuring they are at the start of a line
	// and not part of a comment or string. This is a simplification.
	// A more robust solution would involve Go's parser.
	re := regexp.MustCompile(`(?m)^(func|type)\s+([a-zA-Z0-9_]+\s*)?(\([^)]*\))?\s*({|interface|struct)`)

	lastIndex := 0
	for i, line := range lines {
		if re.MatchString(line) && i > 0 {
			// If we found a new declaration and we have accumulated lines,
			// create a chunk from the previous lines.
			chunk := strings.Join(lines[lastIndex:i], "\n")
			if strings.TrimSpace(chunk) != "" {
				chunks = append(chunks, chunk)
				startLines = append(startLines, lastIndex+1)
			}
			lastIndex = i
		}
	}
	// Add the last chunk
	if lastIndex < len(lines) {
		chunk := strings.Join(lines[lastIndex:], "\n")
		if strings.TrimSpace(chunk) != "" {
			chunks = append(chunks, chunk)
			startLines = append(startLines, lastIndex+1)
		}
	}

	// Fallback to generic if no specific chunks found or if chunks are too large
	if len(chunks) == 0 || c.anyChunkTooLarge(chunks) {
		return c.chunkGeneric(content, filePath)
	}

	return chunks, startLines
}

// chunkRust splits Rust content by fn, struct, impl, enum declarations.
func (c *Chunker) chunkRust(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int
	lines := strings.Split(content, "\n")

	// Regex to find 'fn', 'struct', 'impl', 'enum' declarations.
	re := regexp.MustCompile(`(?m)^(pub\s+)?(fn|struct|impl|enum)\s+`)

	lastIndex := 0
	for i, line := range lines {
		if re.MatchString(line) && i > 0 {
			chunk := strings.Join(lines[lastIndex:i], "\n")
			if strings.TrimSpace(chunk) != "" {
				chunks = append(chunks, chunk)
				startLines = append(startLines, lastIndex+1)
			}
			lastIndex = i
		}
	}
	if lastIndex < len(lines) {
		chunk := strings.Join(lines[lastIndex:], "\n")
		if strings.TrimSpace(chunk) != "" {
			chunks = append(chunks, chunk)
			startLines = append(startLines, lastIndex+1)
		}
	}

	if len(chunks) == 0 || c.anyChunkTooLarge(chunks) {
		return c.chunkGeneric(content, filePath)
	}
	return chunks, startLines
}

// chunkPython splits Python content by def and class declarations.
func (c *Chunker) chunkPython(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int
	lines := strings.Split(content, "\n")

	// Regex to find 'def' or 'class' declarations at the start of a line (after optional whitespace).
	re := regexp.MustCompile(`(?m)^\s*(def|class)\s+`)

	lastIndex := 0
	for i, line := range lines {
		if re.MatchString(line) && i > 0 {
			chunk := strings.Join(lines[lastIndex:i], "\n")
			if strings.TrimSpace(chunk) != "" {
				chunks = append(chunks, chunk)
				startLines = append(startLines, lastIndex+1)
			}
			lastIndex = i
		}
	}
	if lastIndex < len(lines) {
		chunk := strings.Join(lines[lastIndex:], "\n")
		if strings.TrimSpace(chunk) != "" {
			chunks = append(chunks, chunk)
			startLines = append(startLines, lastIndex+1)
		}
	}

	if len(chunks) == 0 || c.anyChunkTooLarge(chunks) {
		return c.chunkGeneric(content, filePath)
	}
	return chunks, startLines
}

// chunkJS splits JavaScript/TypeScript/JSX/TSX content.
func (c *Chunker) chunkJS(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int
	lines := strings.Split(content, "\n")

	// Regex to find 'function', 'const', 'let', 'var', 'class' declarations.
	// This is a simplification for basic chunking.
	re := regexp.MustCompile(`(?m)^\s*(export\s+)?(function|const|let|var|class)\s+`)

	lastIndex := 0
	for i, line := range lines {
		if re.MatchString(line) && i > 0 {
			chunk := strings.Join(lines[lastIndex:i], "\n")
			if strings.TrimSpace(chunk) != "" {
				chunks = append(chunks, chunk)
				startLines = append(startLines, lastIndex+1)
			}
			lastIndex = i
		}
	}
	if lastIndex < len(lines) {
		chunk := strings.Join(lines[lastIndex:], "\n")
		if strings.TrimSpace(chunk) != "" {
			chunks = append(chunks, chunk)
			startLines = append(startLines, lastIndex+1)
		}
	}

	if len(chunks) == 0 || c.anyChunkTooLarge(chunks) {
		return c.chunkGeneric(content, filePath)
	}
	return chunks, startLines
}

// chunkSvelte splits Svelte content, separating <script> blocks from the template.
func (c *Chunker) chunkSvelte(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int

	scriptBlockRe := regexp.MustCompile(`(?s)<script.*?>(.*?)</script>`)
	templateParts := scriptBlockRe.Split(content, -1)
	scriptMatches := scriptBlockRe.FindAllStringSubmatchIndex(content, -1)

	// Add template parts
	currentLine := 1
	for _, part := range templateParts {
		if strings.TrimSpace(part) != "" {
			chunks = append(chunks, part)
			startLines = append(startLines, currentLine)
		}
		currentLine += strings.Count(part, "\n")
	}

	// Add script blocks
	for _, match := range scriptMatches {
		scriptStartByte := match[0]
		scriptEndByte := match[1]
		scriptContent := content[scriptStartByte:scriptEndByte]

		// Find the starting line for the script block
		scriptStartLine := 1 + strings.Count(content[:scriptStartByte], "\n")

		chunks = append(chunks, scriptContent)
		startLines = append(startLines, scriptStartLine)
	}

	// Sort chunks by their start line if out of order due to splitting/finding
	type tempChunk struct {
		content   string
		startLine int
	}
	var sortedChunks []tempChunk
	for i := range chunks {
		sortedChunks = append(sortedChunks, tempChunk{content: chunks[i], startLine: startLines[i]})
	}
	// A more robust approach might be needed if script blocks can overlap or be deeply nested.
	// For simplicity, assuming distinct script blocks and template parts.

	// Now re-assemble if the chunks need to be ordered by appearance.
	// This simplified approach might require manual sorting if the templateParts and scriptMatches
	// don't inherently maintain order correctly. For now, assume a linear flow.

	if len(chunks) == 0 || c.anyChunkTooLarge(chunks) {
		return c.chunkGeneric(content, filePath)
	}
	return chunks, startLines
}

// maxChunkBytes is the hard cap per chunk — keeps content within Ollama's embed context (~8k tokens).
const maxChunkBytes = 24 * 1024 // 24 KB ≈ 6000 tokens, safe for nomic-embed-text

// chunkGeneric splits content into chunks of maxChunkSize lines, then further splits by byte size.
func (c *Chunker) chunkGeneric(content string, filePath string) ([]string, []int) {
	var chunks []string
	var startLines []int
	lines := strings.Split(content, "\n")

	for i := 0; i < len(lines); i += c.maxChunkSize {
		end := i + c.maxChunkSize
		if end > len(lines) {
			end = len(lines)
		}
		chunkContent := strings.Join(lines[i:end], "\n")
		if strings.TrimSpace(chunkContent) == "" {
			continue
		}
		// If chunk still exceeds byte limit (e.g. minified/long lines), split by bytes.
		if len(chunkContent) > maxChunkBytes {
			for off := 0; off < len(chunkContent); off += maxChunkBytes {
				end := off + maxChunkBytes
				if end > len(chunkContent) {
					end = len(chunkContent)
				}
				chunks = append(chunks, chunkContent[off:end])
				startLines = append(startLines, i+1)
			}
		} else {
			chunks = append(chunks, chunkContent)
			startLines = append(startLines, i+1)
		}
	}
	return chunks, startLines
}

// detectLanguage determines the programming language from the file extension.
func (c *Chunker) DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".svelte":
		return "svelte"
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".xml":
		return "xml"
	// Add more languages as needed
	default:
		return "plain_text"
	}
}

// ShouldSkip reports whether filePath should be excluded from indexing.
func (c *Chunker) ShouldSkip(filePath string) bool {
	// 1. Skip hidden dirs/files (.git, .continue, .idea, .env, etc.)
	for _, part := range strings.Split(filepath.ToSlash(filePath), "/") {
		if strings.HasPrefix(part, ".") && len(part) > 1 {
			return true
		}
	}

	// 2. Skip hardcoded noisy dirs by name component.
	base := filepath.Base(filePath)
	for _, dir := range hardcodedIgnoreDirs {
		if base == dir {
			return true
		}
	}

	// 3. Skip lock/sum files.
	if strings.HasSuffix(filePath, ".lock") || strings.HasSuffix(filePath, ".sum") {
		return true
	}

	// 4. .zed-rag-ignore — refresh if stale, then match under read lock.
	c.refreshPatterns()
	c.patternsMu.RLock()
	patterns := c.customPatterns
	c.patternsMu.RUnlock()

	if len(patterns) > 0 {
		rel, err := filepath.Rel(c.projectPath, filePath)
		if err != nil {
			rel = base
		}
		rel = filepath.ToSlash(rel)
		for _, pat := range patterns {
			if matchIgnorePattern(pat, rel, base) {
				return true
			}
		}
	}

	return false
}

// matchIgnorePattern applies a single gitignore-style pattern.
// rel is the path relative to project root (forward slashes), base is filepath.Base.
func matchIgnorePattern(pattern, rel, base string) bool {
	// Directory pattern: ends with /
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		return rel == dir || strings.HasPrefix(rel, dir+"/")
	}
	// Pattern with slash → match against full relative path
	if strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, rel)
		return ok
	}
	// No slash → match against basename only (like gitignore)
	ok, _ := filepath.Match(pattern, base)
	return ok
}

// hashContent returns the MD5 hash of the given content as a hex string.
func (c *Chunker) hashContent(content string) string {
	hasher := md5.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}

// anyChunkTooLarge checks if any of the generated chunks are significantly larger than maxChunkSize.
// This acts as a heuristic to fall back to generic chunking if language-specific chunking isn't effective.
func (c *Chunker) anyChunkTooLarge(chunks []string) bool {
	for _, chunk := range chunks {
		if strings.Count(chunk, "\n") > c.maxChunkSize*2 { // Allow some leeway (e.g., twice the maxChunkSize)
			return true
		}
	}
	return false
}
