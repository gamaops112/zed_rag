package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"zed-rag/internal/chunker"
)

// Point represents a single vector point for Qdrant.
type Point struct {
	ID      string                 `json:"id"`
	Vector  []float32              `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

// SearchResult represents a single search hit from Qdrant.
type SearchResult struct {
	ID      string                 `json:"id"`
	Score   float32                `json:"score"`
	Payload map[string]interface{} `json:"payload"`
}

// Client is a Qdrant client for storing, searching, and updating vectors.
type Client struct {
	url         string
	collection  string
	projectPath string // Base path of the project for filtering
	http        *http.Client
}

// New creates and initializes a new Qdrant Client.
func New(url, collection, projectPath string) *Client {
	return &Client{
		url:         url,
		collection:  collection,
		projectPath: projectPath,
		http: &http.Client{
			Timeout: 60 * time.Second, // Increased timeout for Qdrant operations
		},
	}
}

// Upsert inserts or updates points in the Qdrant collection.
// All points are automatically tagged with the projectPath in their payload.
func (c *Client) Upsert(ctx context.Context, chunks []chunker.Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("number of chunks (%d) does not match number of vectors (%d)", len(chunks), len(vectors))
	}

	var points []Point
	for i, chunk := range chunks {
		payload := map[string]interface{}{
			"project_path": c.projectPath,
			"file_path":    chunk.FilePath,
			"language":     chunk.Language,
			"content":      chunk.Content,
			"start_line":   chunk.StartLine,
			"end_line":     chunk.EndLine,
			"hash":         chunk.Hash,
			"indexed_at":   chunk.IndexedAt.Format(time.RFC3339),
		}
		points = append(points, Point{
			ID:      chunk.ID,
			Vector:  vectors[i],
			Payload: payload,
		})
	}

	requestBody := map[string]interface{}{
		"points": points,
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal upsert request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", fmt.Sprintf("%s/collections/%s/points?wait=true", c.url, c.collection), bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create upsert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send upsert request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant upsert API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// Search queries Qdrant for similar vectors within the current project.
func (c *Client) Search(ctx context.Context, vector []float32, topK int) ([]SearchResult, error) {
	requestBody := map[string]interface{}{
		"vector":       vector,
		"limit":        topK,
		"with_payload": true,
		"filter":       c.buildProjectFilter(),
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/collections/%s/points/search", c.url, c.collection), bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send search request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant search API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var searchResponse struct {
		Result []SearchResult `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResponse); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	return searchResponse.Result, nil
}

// GetFileHash retrieves the hash of a specific file from Qdrant's payload.
// It returns the hash of the first point found for that file, or an empty string if not found.
func (c *Client) GetFileHash(ctx context.Context, filePath string) (string, error) {
	relativeFilePath, err := filepath.Rel(c.projectPath, filePath)
	if err != nil {
		relativeFilePath = filePath // Fallback if cannot get relative path
	}

	requestBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{"key": "project_path", "match": map[string]string{"value": c.projectPath}},
				{"key": "file_path", "match": map[string]string{"value": relativeFilePath}},
			},
		},
		"limit":           1,
		"with_payload":    true,
		"with_vectors":    false,
		"offset":          0,
		"points_selector": nil,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal scroll request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/collections/%s/points/scroll", c.url, c.collection), bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create scroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send scroll request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("qdrant scroll API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var scrollResponse struct {
		Result []struct {
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&scrollResponse); err != nil {
		return "", fmt.Errorf("failed to decode scroll response: %w", err)
	}

	if len(scrollResponse.Result) > 0 {
		if hash, ok := scrollResponse.Result[0].Payload["hash"].(string); ok {
			return hash, nil
		}
	}
	return "", nil // Hash not found
}

// DeleteFile deletes all points associated with a given file path in the current project.
func (c *Client) DeleteFile(ctx context.Context, filePath string) error {
	relativeFilePath, err := filepath.Rel(c.projectPath, filePath)
	if err != nil {
		relativeFilePath = filePath // Fallback if cannot get relative path
	}

	requestBody := map[string]interface{}{
		"points_selector": map[string]interface{}{
			"filter": map[string]interface{}{
				"must": []map[string]interface{}{
					{"key": "project_path", "match": map[string]string{"value": c.projectPath}},
					{"key": "file_path", "match": map[string]string{"value": relativeFilePath}},
				},
			},
		},
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/collections/%s/points/delete?wait=true", c.url, c.collection), bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send delete request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant delete API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// DeleteProject deletes ALL points for the current project from the collection.
func (c *Client) DeleteProject(ctx context.Context) error {
	requestBody := map[string]interface{}{
		"points_selector": map[string]interface{}{
			"filter": c.buildProjectFilter(),
		},
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/collections/%s/points/delete?wait=true", c.url, c.collection),
		bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant delete API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// CollectionStats returns the vector count scoped to this project.
func (c *Client) CollectionStats(ctx context.Context) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"filter": c.buildProjectFilter(),
		"exact":  true,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal count request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/collections/%s/points/count", c.url, c.collection),
		bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create count request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send count request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant count API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var countResponse struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&countResponse); err != nil {
		return nil, fmt.Errorf("failed to decode count response: %w", err)
	}

	return map[string]interface{}{
		"points_count": countResponse.Result.Count,
	}, nil
}

// EnsureCollection creates the collection if it does not already exist.
func (c *Client) EnsureCollection(ctx context.Context, vectorSize int) error {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/collections/%s", c.url, c.collection), nil)
	if err != nil {
		return fmt.Errorf("failed to check collection: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check collection: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal create collection request: %w", err)
	}
	req, err = http.NewRequestWithContext(ctx, "PUT", fmt.Sprintf("%s/collections/%s", c.url, c.collection), bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create collection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create collection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant create collection returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// HealthCheck pings the Qdrant service /healthz endpoint.
func (c *Client) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/healthz", c.url), nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant service at %s is not reachable: %w", c.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant service at %s returned status %d", c.url, resp.StatusCode)
	}
	return nil
}

// buildProjectFilter creates a Qdrant filter to scope operations to the current project.
func (c *Client) buildProjectFilter() map[string]interface{} {
	return map[string]interface{}{
		"must": []map[string]interface{}{
			{"key": "project_path", "match": map[string]string{"value": c.projectPath}},
		},
	}
}
