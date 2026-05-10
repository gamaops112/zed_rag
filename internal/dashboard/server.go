package dashboard

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"zed-rag/internal/embedder"
	"zed-rag/internal/metrics"
	"zed-rag/internal/qdrant"
	"zed-rag/internal/resolver"
)

//go:embed static/index.html
var indexHTML []byte

// Server is the HTTP server that serves the dashboard UI.
type Server struct {
	port          int
	tracker       *metrics.Tracker
	qdrant        *qdrant.Client
	embedder      *embedder.Embedder
	mux           *http.ServeMux
	localResolver *resolver.LocalResolver
}

// StatsResponse represents the full dashboard JSON statistics.
type StatsResponse struct {
	ProjectPath string    `json:"projectPath"`
	LastUpdated time.Time `json:"lastUpdated"`
	Qdrant      struct {
		Status       string `json:"status"`
		TotalVectors int    `json:"totalVectors"`
		StorageSize  string `json:"storageSize"`
	} `json:"qdrant"`
	Ollama struct {
		Status     string  `json:"status"`
		Model      string  `json:"model"`
		AvgEmbedMS float64 `json:"avgEmbedMS"`
	} `json:"ollama"`
	MCP struct {
		TotalQueries   int     `json:"totalQueries"`
		LocalQueries   int     `json:"localQueries"`
		AIQueries      int     `json:"aiQueries"`
		AvgQueryMS     float64 `json:"avgQueryMS"`
		QueriesPerHour []int   `json:"queriesPerHour"`
	} `json:"mcp"`
	TopFiles []string `json:"topFiles"`
}

// RagQueryRequest defines the structure for an incoming RAG query.
type RagQueryRequest struct {
	Query string `json:"query"`
}

// RagQueryResponse defines the structure for a RAG query response.
type RagQueryResponse struct {
	Context string `json:"context"`
	Error   string `json:"error,omitempty"`
}

// New creates a new dashboard server.
func New(port int, tracker *metrics.Tracker, qdrant *qdrant.Client, embedder *embedder.Embedder, localRes *resolver.LocalResolver) *Server {
	mux := http.NewServeMux()
	s := &Server{
		port:          port,
		tracker:       tracker,
		qdrant:        qdrant,
		embedder:      embedder,
		mux:           mux,
		localResolver: localRes,
	}

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/rag-query", s.handleRagQuery) // New RAG query endpoint

	return s
}

// Start registers routes and starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.mux,
	}

	go func() {
		<-ctx.Done()
		log.Println("Dashboard server shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Dashboard server shutdown error: %v", err)
		}
	}()

	log.Printf("Dashboard server starting on port %d", s.port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("dashboard server failed to start: %w", err)
	}
	return nil
}

// handleIndex serves the embedded index.html.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleStats builds and returns the full dashboard JSON statistics.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := StatsResponse{}

	// General Stats
	snapshot := s.tracker.Snapshot()
	stats.ProjectPath = s.tracker.ProjectPath() // Access ProjectPath from the tracker
	stats.LastUpdated = time.Now()

	// Qdrant Stats
	if err := s.qdrant.HealthCheck(r.Context()); err != nil {
		stats.Qdrant.Status = fmt.Sprintf("Error: %v", err)
	} else {
		stats.Qdrant.Status = "Healthy"
		collectionStats, err := s.qdrant.CollectionStats(r.Context())
		if err != nil {
			log.Printf("Error getting Qdrant collection stats: %v", err)
			stats.Qdrant.StorageSize = "N/A"
		} else {
			if n, ok := collectionStats["points_count"].(int); ok {
				stats.Qdrant.TotalVectors = n
			}
			stats.Qdrant.StorageSize = "N/A"
		}
	}

	// Ollama (Embedder) Stats
	if err := s.embedder.HealthCheck(r.Context()); err != nil {
		stats.Ollama.Status = fmt.Sprintf("Error: %v", err)
	} else {
		stats.Ollama.Status = "Healthy"
		stats.Ollama.Model = s.embedder.Model()
		stats.Ollama.AvgEmbedMS = snapshot.AvgEmbedMS
	}

	// MCP and general metrics from tracker snapshot
	stats.MCP.TotalQueries = snapshot.TotalQueries
	stats.MCP.LocalQueries = snapshot.LocalQueries
	stats.MCP.AIQueries = snapshot.AIQueries
	stats.MCP.AvgQueryMS = snapshot.AvgQueryMS
	stats.MCP.QueriesPerHour = snapshot.QueriesPerHour
	stats.TopFiles = snapshot.TopFiles

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		log.Printf("Error encoding stats response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleHealth returns the service health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	qdrantHealthy := s.qdrant.HealthCheck(r.Context()) == nil
	embedderHealthy := s.embedder.HealthCheck(r.Context()) == nil

	status := struct {
		Qdrant  string `json:"qdrant"`
		Ollama  string `json:"ollama"`
		Overall string `json:"overall"`
	}{
		Qdrant:  "Unhealthy",
		Ollama:  "Unhealthy",
		Overall: "Unhealthy",
	}

	if qdrantHealthy {
		status.Qdrant = "Healthy"
	}
	if embedderHealthy {
		status.Ollama = "Healthy"
	}

	if qdrantHealthy && embedderHealthy {
		status.Overall = "Healthy"
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("Error encoding health response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleRagQuery embeds the query, searches Qdrant, and returns relevant code chunks.
func (s *Server) handleRagQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var reqBody RagQueryRequest
	bodyBytes, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		http.Error(w, "Invalid JSON request body", http.StatusBadRequest)
		return
	}
	if reqBody.Query == "" {
		http.Error(w, "Query cannot be empty", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	context_, err := s.localResolver.Resolve(ctx, reqBody.Query)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		log.Printf("RAG query error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(RagQueryResponse{Error: err.Error()})
		return
	}
	json.NewEncoder(w).Encode(RagQueryResponse{Context: context_})
}
