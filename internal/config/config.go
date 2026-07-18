// Package config loads ~/.culi/config.yaml with defaults. The base directory
// is overridable via CULI_HOME so tests and sandboxes never touch the real
// user store.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds user-tunable settings. Zero values are replaced by defaults in
// Load so a partial (or absent) config.yaml always yields a usable Config.
type Config struct {
	// PushBudget is the max estimated tokens injected per UserPromptSubmit.
	PushBudget int `yaml:"push_budget"`
	// BaselineBudget is the max estimated tokens injected at SessionStart.
	BaselineBudget int `yaml:"baseline_budget"`
	// Ollama configures the local embedding endpoint (used from Phase 3).
	Ollama OllamaConfig `yaml:"ollama"`
	// ExtraAcks extends the built-in acknowledgement lexicon (the
	// inject-nothing gate) with the user's own phrases, any language.
	ExtraAcks []string `yaml:"extra_acks"`
	// ExtraStopwords extends the built-in stopword packs.
	ExtraStopwords []string `yaml:"extra_stopwords"`
}

// OllamaConfig points at the local embedding server.
type OllamaConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
}

// Defaults are deliberately conservative: the packer fills to ~90% of budget,
// so estimation error (chars/4 heuristic) cannot blow the hard cap.
const (
	defaultPushBudget     = 700
	defaultBaselineBudget = 1200
	defaultOllamaEndpoint = "http://localhost:11434"
	defaultOllamaModel    = "nomic-embed-text"
)

// BaseDir returns the culi home directory: $CULI_HOME if set, else ~/.culi.
func BaseDir() (string, error) {
	if dir := os.Getenv("CULI_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving home dir: %w", err)
	}
	return filepath.Join(home, ".culi"), nil
}

// KnowledgeDir returns the canonical card store directory under base.
func KnowledgeDir(base string) string { return filepath.Join(base, "knowledge") }

// DBPath returns the SQLite index path under base.
func DBPath(base string) string { return filepath.Join(base, "index.db") }

// LogDir returns the log directory under base.
func LogDir(base string) string { return filepath.Join(base, "logs") }

// InboxDir returns the learn-queue directory under base.
func InboxDir(base string) string { return filepath.Join(base, "inbox") }

// Load reads base/config.yaml. A missing file is not an error: defaults apply.
func Load(base string) (Config, error) {
	cfg := Config{
		PushBudget:     defaultPushBudget,
		BaselineBudget: defaultBaselineBudget,
		Ollama:         OllamaConfig{Endpoint: defaultOllamaEndpoint, Model: defaultOllamaModel},
	}
	raw, err := os.ReadFile(filepath.Join(base, "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: reading config.yaml: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parsing config.yaml: %w", err)
	}
	if cfg.PushBudget <= 0 {
		cfg.PushBudget = defaultPushBudget
	}
	if cfg.BaselineBudget <= 0 {
		cfg.BaselineBudget = defaultBaselineBudget
	}
	if cfg.Ollama.Endpoint == "" {
		cfg.Ollama.Endpoint = defaultOllamaEndpoint
	}
	if cfg.Ollama.Model == "" {
		cfg.Ollama.Model = defaultOllamaModel
	}
	return cfg, nil
}
