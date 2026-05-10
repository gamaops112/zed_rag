package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbedRequest defines the structure for a request to the Ollama embeddings API.
type EmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// EmbedResponse defines the structure for a response from the Ollama embeddings API.
type EmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embedder is responsible for converting text chunks into vectors using Ollama.
type Embedder struct {
	ollamaURL string
	model     string
	client    *http.Client
	retries   int
}

// New creates and initializes a new Embedder.
func New(ollamaURL string, model string) *Embedder {
	return &Embedder{
		ollamaURL: ollamaURL,
		model:     model,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		retries: 3, // Default to 3 retries
	}
}

// Embed sends a single text string to Ollama for embedding and returns the vector.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	var embedding []float32
	err := e.retry(func() error {
		select {
		case <-ctx.Done():
			return ctx.Err() // Return context cancellation error
		default:
		}

		reqBody, err := json.Marshal(EmbedRequest{
			Model:  e.model,
			Prompt: text,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal embed request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/embeddings", e.ollamaURL), bytes.NewBuffer(reqBody))
		if err != nil {
			return fmt.Errorf("failed to create HTTP request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to send embedding request to Ollama: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("ollama embedding API returned non-OK status: %d, body: %s", resp.StatusCode, string(bodyBytes))
		}

		var embedResp EmbedResponse
		if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
			return fmt.Errorf("failed to decode embedding response: %w", err)
		}
		embedding = embedResp.Embedding
		return nil
	})

	if err != nil {
		return nil, err
	}
	return embedding, nil
}

// EmbedBatch embeds multiple texts by calling Embed for each one.
// This can be optimized later for actual batching if Ollama supports it, or with concurrency.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	var embeddings [][]float32
	for i, text := range texts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text batch item %d: %w", i, err)
		}
		embeddings = append(embeddings, vec)
	}
	return embeddings, nil
}

// Model returns the configured embedding model name.
func (e *Embedder) Model() string {
	return e.model
}

// HealthCheck pings Ollama to ensure it's reachable.
func (e *Embedder) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.ollamaURL, nil) // A simple GET to the base URL
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("Ollama service at %s is not reachable: %w", e.ollamaURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama service at %s returned status %d", e.ollamaURL, resp.StatusCode)
	}
	return nil
}

// retry executes a function with exponential backoff on failure.
func (e *Embedder) retry(fn func() error) error {
	var err error
	for i := 0; i < e.retries; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if ctxErr := context.Cause(context.Background()); ctxErr != nil { // Check for context cancellation
			return ctxErr
		}
		backoff := time.Duration(1<<i) * time.Second // 1s, 2s, 4s
		time.Sleep(backoff)
	}
	return err // Return the last error
}
