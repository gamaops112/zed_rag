package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"zed-rag/internal/metrics"
	"zed-rag/internal/resolver"
)

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      *json.RawMessage       `json:"id,omitempty"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server handles the MCP protocol over stdin/stdout.
type Server struct {
	resolver       *resolver.LocalResolver
	metricsChannel chan<- metrics.Metric
	projectPath    string
	queryCount     atomic.Int64
	scanner        *bufio.Scanner
	stdout         io.Writer
}

// New creates a new MCP Server.
func New(res *resolver.LocalResolver, metricsChan chan<- metrics.Metric, projectPath string) *Server {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	return &Server{
		resolver:       res,
		metricsChannel: metricsChan,
		projectPath:    projectPath,
		scanner:        scanner,
		stdout:         os.Stdout,
	}
}

// Start reads JSON-RPC messages from stdin until ctx is cancelled or stdin closes.
func (s *Server) Start(ctx context.Context) error {
	for s.scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := s.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, fmt.Sprintf("parse error: %v", err))
			continue
		}
		// Notifications have no id — do not respond.
		if req.ID == nil {
			continue
		}
		go func(r Request) {
			s.write(s.handle(ctx, r))
		}(req)
	}
	if err := s.scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("stdin read error: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req Request) Response {
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "zed-rag", "version": "1.0.0"},
		})

	case "tools/list":
		return s.ok(req.ID, map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "search_codebase",
					"description": "Search the indexed codebase for relevant code chunks. Returns context snippets scoped to the configured project.",
					"inputSchema": map[string]interface{}{
						"type":     "object",
						"required": []string{"query"},
						"properties": map[string]interface{}{
							"query": map[string]string{
								"type":        "string",
								"description": "Natural language query",
							},
						},
					},
				},
			},
		})

	case "tools/call":
		return s.handleToolCall(ctx, req)

	default:
		return s.err(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) handleToolCall(ctx context.Context, req Request) Response {
	name, _ := req.Params["name"].(string)
	if name != "search_codebase" {
		return s.err(req.ID, -32601, fmt.Sprintf("unknown tool: %s", name))
	}

	args, _ := req.Params["arguments"].(map[string]interface{})
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return s.err(req.ID, -32602, "query is required")
	}

	start := time.Now()
	context_, resolveErr := s.resolver.Resolve(ctx, query)
	duration := time.Since(start)

	s.queryCount.Add(1)
	s.metricsChannel <- metrics.Metric{
		Type:        "query",
		ProjectPath: s.projectPath,
		Duration:    duration,
		IntentType:  "local",
		Timestamp:   time.Now(),
		Metadata:    map[string]interface{}{"query": query},
	}

	if resolveErr != nil {
		return s.err(req.ID, 1, fmt.Sprintf("search failed: %v", resolveErr))
	}

	return s.ok(req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": context_},
		},
	})
}

func (s *Server) write(resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.stdout.Write(append(b, '\n'))
}

func (s *Server) writeError(id *json.RawMessage, code int, msg string) {
	s.write(s.err(id, code, msg))
}

func (s *Server) ok(id *json.RawMessage, result interface{}) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) err(id *json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}
