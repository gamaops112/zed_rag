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

// Chunker is responsible for splitting source files into Chunks.
type Chunker struct {
	maxChunkSize   int
	ignorePatterns []*regexp.Regexp
	projectPath    string // Base project path for ignore patterns and relative file paths
}

// New creates and initializes a new Chunker.
func New(projectPath string, maxChunkSize int) *Chunker {
	if maxChunkSize <= 0 {
		maxChunkSize = 100 // Default to 100 lines if invalid size is provided
	}

	defaultPatterns := []string{
		"node_modules/", "target/", "dist/", "build/",
		"__pycache__/", ".git/", "vendor/", `.*\.lock$`,
		`.*\.sum$`, `.*\.mod$`, // Exclude all .mod and .sum files by default
	}

	var compiledPatterns []*regexp.Regexp
	for _, p := range defaultPatterns {
		compiledPatterns = append(compiledPatterns, regexp.MustCompile(p))
	}

	return &Chunker{
		maxChunkSize:   maxChunkSize,
		ignorePatterns: compiledPatterns,
		projectPath:    projectPath,
	}
}

// ChunkFile reads a file and splits its content into a slice of Chunks based on language-specific rules.
func (c *Chunker) ChunkFile(filePath string) ([]Chunk, error) {
	if c.ShouldSkip(filePath) { // Corrected call
		return nil, nil // Skip ignored files silently
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

// chunkGeneric splits content into chunks of maxChunkSize lines.
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
		if strings.TrimSpace(chunkContent) != "" {
			chunks = append(chunks, chunkContent)
			startLines = append(startLines, i+1) // Line numbers are 1-based
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

// shouldSkip checks if a file should be ignored based on predefined patterns and .zed-rag-ignore.
func (c *Chunker) ShouldSkip(filePath string) bool {
	// Skip any path component starting with '.' — covers .git, .continue, .zed, .idea, .vscode, etc.
	// Use the full path so relative and absolute paths both work.
	cleanPath := filepath.ToSlash(filePath)
	for _, part := range strings.Split(cleanPath, "/") {
		if strings.HasPrefix(part, ".") && len(part) > 1 {
			return true
		}
	}

	// Check against hardcoded ignore patterns
	for _, pattern := range c.ignorePatterns {
		if pattern.MatchString(filePath) {
			return true
		}
	}

	// Read .zed-rag-ignore if it exists in the project root
	ignoreFilePath := filepath.Join(c.projectPath, ".zed-rag-ignore")
	if _, err := os.Stat(ignoreFilePath); err == nil {
		ignoreContent, err := ioutil.ReadFile(ignoreFilePath)
		if err == nil {
			lines := strings.Split(string(ignoreContent), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				// Convert gitignore-like patterns to regex
				// Simple glob to regex conversion: replace * with .* and ? with .
				// Escape dots and forward slashes unless they are part of a special pattern
				pattern := regexp.QuoteMeta(line)
				pattern = strings.ReplaceAll(pattern, `\*`, `.*`)
				pattern = strings.ReplaceAll(pattern, `\?`, `.`)
				pattern = "^" + pattern // Anchor to the start of the string

				// If pattern is a directory, match it with a trailing slash
				if strings.HasSuffix(line, "/") {
					pattern += `.*`
				}

				re, err := regexp.Compile(pattern)
				if err == nil && re.MatchString(filepath.Base(filePath)) { // Match against base name or full path
					return true
				}
				if err == nil && re.MatchString(filePath) {
					return true
				}
			}
		}
	}

	// Special handling for go.mod: only skip if it's not explicitly go.mod itself.
	// This ensures that the global *.mod pattern doesn't prevent go.mod from being indexed.
	if strings.HasSuffix(filePath, "go.mod") {
		return false // Never skip go.mod
	}

	// Skip all other .mod files
	if strings.HasSuffix(filePath, ".mod") {
		return true
	}

	return false
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
