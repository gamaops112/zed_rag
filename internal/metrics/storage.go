package metrics

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

// Storage handles persisting metrics to an SQLite database.
type Storage struct {
	db *sql.DB
	mu sync.Mutex // Protects database access
}

// New creates and initializes a new Storage instance.
// It opens the SQLite database and ensures the metrics and file_stats tables exist.
func New(dbPath string) (*Storage, error) {
	// Ensure the directory for the database path exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database at %s: %w", dbPath, err)
	}

	s := &Storage{
		db: db,
	}

	if err := s.initSchema(); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to initialize database schema: %w", err)
	}

	return s, nil
}

// initSchema creates the metrics and file_stats tables if they don't already exist.
func (s *Storage) initSchema() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	createMetricsTableSQL := `
	CREATE TABLE IF NOT EXISTS metrics (
	  id          INTEGER PRIMARY KEY AUTOINCREMENT,
	  timestamp   DATETIME,
	  type        TEXT,
	  project     TEXT,
	  file        TEXT,
	  duration_ms INTEGER,
	  intent      TEXT,
	  chunks      INTEGER,
	  error       TEXT
	);`

	createFileStatsTableSQL := `
	CREATE TABLE IF NOT EXISTS file_stats (
	  file        TEXT,
	  project     TEXT,
	  hit_count   INTEGER,
	  PRIMARY KEY (file, project)
	);`

	if _, err := s.db.Exec(createMetricsTableSQL); err != nil {
		return fmt.Errorf("failed to create metrics table: %w", err)
	}
	if _, err := s.db.Exec(createFileStatsTableSQL); err != nil {
		return fmt.Errorf("failed to create file_stats table: %w", err)
	}
	return nil
}

// Insert inserts one metric row into the metrics table.
func (s *Storage) Insert(m Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	insertSQL := `
	INSERT INTO metrics (timestamp, type, project, file, duration_ms, intent, chunks, error)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?);`

	_, err := s.db.Exec(
		insertSQL,
		m.Timestamp.Format(time.RFC3339),
		m.Type,
		m.ProjectPath, // project
		m.FilePath,    // file
		m.Duration.Milliseconds(),
		m.IntentType,  // intent
		m.ChunksFound, // chunks
		m.Error,
	)
	if err != nil {
		return fmt.Errorf("failed to insert metric: %w", err)
	}
	return nil
}

// IncrementFileHit upserts (inserts or updates) the hit count for a file in file_stats.
func (s *Storage) IncrementFileHit(project, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	upsertSQL := `
	INSERT INTO file_stats (file, project, hit_count)
	VALUES (?, ?, 1)
	ON CONFLICT(file, project) DO UPDATE SET hit_count = hit_count + 1;`

	_, err := s.db.Exec(upsertSQL, file, project)
	if err != nil {
		return fmt.Errorf("failed to increment file hit count for %s/%s: %w", project, file, err)
	}
	return nil
}

// QueryPerHour returns query counts per hour for the last N hours.
func (s *Storage) QueryPerHour(lastHours int) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := make([]int, lastHours)
	now := time.Now()

	for i := 0; i < lastHours; i++ {
		// Calculate the start and end of the current hour slot
		endTime := now.Add(-time.Duration(i) * time.Hour)
		startTime := now.Add(-time.Duration(i+1) * time.Hour)

		query := `
		SELECT COUNT(*) FROM metrics
		WHERE type = 'query' AND timestamp >= ? AND timestamp < ?;`

		var count int
		err := s.db.QueryRow(query, startTime.Format(time.RFC3339), endTime.Format(time.RFC3339)).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("failed to get hourly query count for hour %d: %w", i, err)
		}
		counts[lastHours-1-i] = count // Store in reverse order to have oldest first
	}

	return counts, nil
}

// TopFiles returns the most hit files for a given project, up to a specified limit.
func (s *Storage) TopFiles(project string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	SELECT file
	FROM file_stats
	WHERE project = ?
	ORDER BY hit_count DESC
	LIMIT ?;`

	rows, err := s.db.Query(query, project, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get top files for project %s: %w", project, err)
	}
	defer rows.Close()

	var topFiles []string
	for rows.Next() {
		var file string
		if err := rows.Scan(&file); err != nil {
			return nil, fmt.Errorf("failed to scan top files row: %w", err)
		}
		topFiles = append(topFiles, file)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating top files rows: %w", err)
	}

	return topFiles, nil
}

// Close closes the database connection.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
