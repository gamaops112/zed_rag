package metrics

import (
	"context"
	"log"
	"sync"
	"time"
)

// Metric represents a single event or data point to be tracked in the system.
type Metric struct {
	Timestamp   time.Time              `json:"timestamp"`
	Type        string                 `json:"type"` // "query", "index", "embed", "search"
	ProjectPath string                 `json:"project_path"`
	FilePath    string                 `json:"file_path,omitempty"`
	Duration    time.Duration          `json:"duration_ns,omitempty"`
	IntentType  string                 `json:"intent_type,omitempty"` // "local" or "ai"
	ChunksFound int                    `json:"chunks_found,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Snapshot holds aggregated metrics for the dashboard.
type Snapshot struct {
	TotalQueries   int       `json:"total_queries"`
	LocalQueries   int       `json:"local_queries"`
	AIQueries      int       `json:"ai_queries"`
	TotalIndexed   int       `json:"total_indexed_files"`
	AvgEmbedMS     float64   `json:"avg_embed_ms"`
	AvgSearchMS    float64   `json:"avg_search_ms"`
	AvgQueryMS     float64   `json:"avg_query_ms"`
	QueriesPerHour []int     `json:"queries_per_hour"`
	TopFiles       []string  `json:"top_files"`
	LastUpdated    time.Time `json:"last_updated"`
}

// Tracker reads metrics from a channel, maintains an in-memory snapshot, and persists to storage.
type Tracker struct {
	storage     *Storage
	metrics     chan Metric
	snapshot    *Snapshot
	mu          sync.RWMutex
	projectPath string
	queryCount  int // running count for AvgQueryMS denominator
	embedCount  int // running count for AvgEmbedMS denominator
	searchCount int // running count for AvgSearchMS denominator
}

// NewTracker creates and initializes a new Tracker.
func NewTracker(storage *Storage, metricsChan chan Metric, projectPath string) *Tracker {
	return &Tracker{
		storage:     storage,
		metrics:     metricsChan,
		snapshot:    &Snapshot{QueriesPerHour: make([]int, 24)},
		projectPath: projectPath,
	}
}

// Start reads metrics until ctx is cancelled or the channel is closed.
func (t *Tracker) Start(ctx context.Context) {
	log.Printf("Metrics Tracker: started")
	t.refreshSnapshot()

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.refreshSnapshot()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Metrics Tracker: stopped")
			return
		case m, ok := <-t.metrics:
			if !ok {
				log.Printf("Metrics Tracker: channel closed")
				return
			}
			t.updateSnapshot(m)
			go func(metric Metric) {
				if err := t.storage.Insert(metric); err != nil {
					log.Printf("Metrics Tracker: failed to insert metric: %v", err)
				}
				if metric.FilePath != "" {
					if err := t.storage.IncrementFileHit(metric.ProjectPath, metric.FilePath); err != nil {
						log.Printf("Metrics Tracker: failed to increment file hit: %v", err)
					}
				}
			}(m)
		}
	}
}

// Record sends a Metric to the channel for processing. Drops if channel is full.
func (t *Tracker) Record(m Metric) {
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	select {
	case t.metrics <- m:
	default:
		log.Printf("Metrics Tracker: channel full, dropping metric: %+v", m)
	}
}

// Snapshot returns a thread-safe deep copy of the current aggregated snapshot.
func (t *Tracker) Snapshot() *Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	snap := *t.snapshot
	snap.QueriesPerHour = make([]int, len(t.snapshot.QueriesPerHour))
	copy(snap.QueriesPerHour, t.snapshot.QueriesPerHour)
	snap.TopFiles = make([]string, len(t.snapshot.TopFiles))
	copy(snap.TopFiles, t.snapshot.TopFiles)
	return &snap
}

// ProjectPath returns the project path associated with this tracker.
func (t *Tracker) ProjectPath() string {
	return t.projectPath
}

// updateSnapshot updates the in-memory snapshot when a new metric arrives.
func (t *Tracker) updateSnapshot(m Metric) {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.snapshot
	s.LastUpdated = time.Now()

	switch m.Type {
	case "query":
		s.TotalQueries++
		s.QueriesPerHour[time.Now().Hour()]++
		t.queryCount++
		if m.IntentType == "local" {
			s.LocalQueries++
		} else if m.IntentType == "ai" {
			s.AIQueries++
		}
		s.AvgQueryMS = (s.AvgQueryMS*float64(t.queryCount-1) + m.Duration.Seconds()*1000) / float64(t.queryCount)
	case "index":
		s.TotalIndexed++
	case "embed":
		t.embedCount++
		s.AvgEmbedMS = (s.AvgEmbedMS*float64(t.embedCount-1) + m.Duration.Seconds()*1000) / float64(t.embedCount)
	case "search":
		t.searchCount++
		s.AvgSearchMS = (s.AvgSearchMS*float64(t.searchCount-1) + m.Duration.Seconds()*1000) / float64(t.searchCount)
	}
}

// refreshSnapshot reloads storage-backed data (hourly counts, top files) into the snapshot.
// In-memory counters (TotalQueries, AvgQueryMS, etc.) are maintained by updateSnapshot only.
func (t *Tracker) refreshSnapshot() {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.snapshot
	s.LastUpdated = time.Now()

	hourlyCounts, err := t.storage.QueryPerHour(24)
	if err != nil {
		log.Printf("Metrics Tracker: failed to get hourly counts: %v", err)
	} else {
		copy(s.QueriesPerHour, hourlyCounts)
	}

	topFiles, err := t.storage.TopFiles(t.projectPath, 5)
	if err != nil {
		log.Printf("Metrics Tracker: failed to get top files: %v", err)
	} else {
		s.TopFiles = topFiles
	}
}
