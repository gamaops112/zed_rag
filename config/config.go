package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/pelletier/go-toml/v2"
)

// Config holds the application's configuration.
type Config struct {
	ProjectPath      string `toml:"project_path"`
	QdrantURL        string `toml:"qdrant_url"`
	QdrantCollection string `toml:"qdrant_collection"`
	OllamaURL        string `toml:"ollama_url"`
	EmbeddingModel   string `toml:"embedding_model"`
	DashboardPort    int    `toml:"dashboard_port"`
	LogPath          string `toml:"log_path"`
}

// Load loads config and requires ProjectPath to be set. Used by MCP mode.
func Load() (*Config, error) {
	cfg, err := loadFile()
	if err != nil {
		return nil, err
	}
	if cfg.ProjectPath == "" {
		return nil, fmt.Errorf("PROJECT_PATH not set; add to config.toml or set env var")
	}
	return cfg, nil
}

// LoadInfra loads config without requiring ProjectPath. Used by index/remove/serve/status commands.
func LoadInfra() (*Config, error) {
	return loadFile()
}

// loadFile reads ~/.zed-rag/config.toml, applies env var overrides, fills defaults.
func loadFile() (*Config, error) {
	cfg := Default()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	configPath := filepath.Join(homeDir, ".zed-rag", "config.toml")

	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", configPath, err)
		}
		if err := toml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
	}

	_ = godotenv.Load()

	cfg.ProjectPath = getEnv("PROJECT_PATH", cfg.ProjectPath)
	cfg.QdrantURL = getEnv("QDRANT_URL", cfg.QdrantURL)
	cfg.QdrantCollection = getEnv("QDRANT_COLLECTION", cfg.QdrantCollection)
	cfg.OllamaURL = getEnv("OLLAMA_URL", cfg.OllamaURL)
	cfg.EmbeddingModel = getEnv("EMBEDDING_MODEL", cfg.EmbeddingModel)
	cfg.DashboardPort = getEnvAsInt("DASHBOARD_PORT", cfg.DashboardPort)
	cfg.LogPath = getEnv("LOG_PATH", cfg.LogPath)

	if cfg.LogPath == "" {
		cfg.LogPath = filepath.Join(homeDir, ".zed-rag", "logs")
	}

	return cfg, nil
}

// Default returns a Config with all default values.
func Default() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		ProjectPath:      "",
		QdrantURL:        "http://localhost:6333",
		QdrantCollection: "codebase",
		OllamaURL:        "http://localhost:11434",
		EmbeddingModel:   "nomic-embed-text",
		DashboardPort:    7702,
		LogPath:          filepath.Join(homeDir, ".zed-rag", "logs"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}
