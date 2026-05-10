package resolver

import (
	"context"
	"fmt"
	"strings"

	"zed-rag/internal/embedder"
	"zed-rag/internal/qdrant"
)

// LocalResolver answers local queries using only Qdrant search results.
// It performs no AI API calls.
type LocalResolver struct {
	qdrant   *qdrant.Client
	embedder *embedder.Embedder
	topK     int // Number of top results to fetch from Qdrant
}

// New creates and initializes a new LocalResolver.
func New(q *qdrant.Client, e *embedder.Embedder) *LocalResolver {
	return &LocalResolver{
		qdrant:   q,
		embedder: e,
		topK:     3, // Default to 3 top results
	}
}

// Resolve embeds the query, searches Qdrant, and formats the results.
func (r *LocalResolver) Resolve(ctx context.Context, query string) (string, error) {
	// 1. Embed the query
	queryVector, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return "", fmt.Errorf("failed to embed query: %w", err)
	}

	// 2. Search Qdrant
	searchResults, err := r.qdrant.Search(ctx, queryVector, r.topK)
	if err != nil {
		return "", fmt.Errorf("failed to search Qdrant: %w", err)
	}

	if len(searchResults) == 0 {
		return "No relevant information found in the codebase.", nil
	}

	// 3. Format results
	formattedResponse := r.formatResults(searchResults)

	return formattedResponse, nil
}

// formatResults extracts relevant information from Qdrant SearchResults
// and formats them into a readable string.
func (r *LocalResolver) formatResults(results []qdrant.SearchResult) string {
	var sb strings.Builder
	sb.WriteString("Found the following relevant code snippets:\n\n")

	for i, result := range results {
		filePath, ok := result.Payload["file_path"].(string)
		if !ok {
			filePath = "unknown file"
		}
		language, ok := result.Payload["language"].(string)
		if !ok {
			language = "unknown"
		}
		content, ok := result.Payload["content"].(string)
		if !ok {
			content = "Content not available."
		}
		startLine, ok := result.Payload["start_line"].(float64) // JSON numbers are often float64
		if !ok {
			startLine = 0
		}
		endLine, ok := result.Payload["end_line"].(float64) // JSON numbers are often float64
		if !ok {
			endLine = 0
		}

		sb.WriteString(fmt.Sprintf("--- Result %d (Score: %.2f) ---\n", i+1, result.Score))
		sb.WriteString(fmt.Sprintf("Found in: %s (Language: %s, Lines: %d-%d)\n", filePath, language, int(startLine), int(endLine)))
		sb.WriteString("```\n")
		sb.WriteString(content)
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}
