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
	// Repos lists absolute paths of repositories whose .claude directories
	// and CLAUDE.md files `culi import` reconciles into the canonical store.
	Repos []string `yaml:"repos"`
	// Import configures the drift-reconcile pipeline.
	Import ImportConfig `yaml:"import"`
	// Learn configures the background learning pipelines.
	Learn LearnConfig `yaml:"learn"`
}

// LearnConfig tunes transcript mining (Phase 4). Both caps must pass for a
// model call to run; hooks always enqueue regardless (the queue drains when
// caps reset). To spend nothing, set enabled: false or provider: none.
type LearnConfig struct {
	// Enabled is the master switch (default true).
	Enabled bool `yaml:"enabled"`
	// Provider selects the mining backend like import.provider: auto (default),
	// anthropic, claude-cli, ollama, or none.
	Provider string `yaml:"provider"`
	// CheapModel handles routine mining; StrongModel is the one-step
	// escalation on schema failures. For ollama both MUST be local models.
	CheapModel  string `yaml:"cheap_model"`
	StrongModel string `yaml:"strong_model"`
	// DailyUSDCap bounds estimated API spend per day (anthropic backend only;
	// claude-cli and ollama cost $0). 0 = default.
	DailyUSDCap float64 `yaml:"daily_usd_cap"`
	// DailyCallCap bounds model calls per day on every backend — the
	// subscription-quota guard for claude-cli. 0 = default.
	DailyCallCap int `yaml:"daily_call_cap"`
	// CandidateTTLDays auto-retires mined candidate cards left unreinforced for
	// this many days (file mtime is the clock). 0 = default (30); a negative
	// value disables the janitor. Confirmed cards are NEVER auto-retired —
	// dormant ones are only reported by `culi stats` (C4).
	CandidateTTLDays int `yaml:"candidate_ttl_days"`
	// OAuthTokenFile points the claude-cli backend at a file holding a
	// CLAUDE_CODE_OAUTH_TOKEN. Empty (default) = off. Set it when background
	// learning runs headless: Claude Code does not propagate its OAuth token to
	// hook-spawned processes, so `claude -p` has no auth; culi reads the token
	// from this file and injects it into the subprocess env. Leading ~ expands.
	OAuthTokenFile string `yaml:"oauth_token_file"`
	// AnthropicAPIKeyFile points the anthropic backend at a file holding an
	// ANTHROPIC_API_KEY, for users who prefer the metered API over the
	// subscription CLI. Empty (default) = off; the env var is used when set.
	// Its presence also makes provider:auto prefer the Anthropic API. ~ expands.
	AnthropicAPIKeyFile string `yaml:"anthropic_api_key_file"`
}

// ImportConfig tunes `culi import merge`.
type ImportConfig struct {
	// Provider selects the merge backend: auto (default — anthropic when
	// ANTHROPIC_API_KEY is set, else claude-cli when the claude binary is in
	// PATH), anthropic, claude-cli, ollama, or none (mechanical only).
	Provider string `yaml:"provider"`
	// MergeModel is the model used to reconcile diverged clusters and
	// decompose CLAUDE.md files. For anthropic/claude-cli this is a Claude
	// model ID; for ollama it MUST be a local generation model (e.g. qwen3).
	MergeModel string `yaml:"merge_model"`
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
	defaultMergeModel     = "claude-sonnet-5"
	defaultCheapModel     = "claude-haiku-4-5"
	defaultStrongModel    = "claude-sonnet-5"
	defaultDailyUSDCap    = 0.50
	defaultDailyCallCap   = 40
	defaultCandidateTTL   = 30 // days a candidate may sit unreinforced
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

// StateDir returns the directory for culi-internal state (export manifest,
// learning ledgers) under base.
func StateDir(base string) string { return filepath.Join(base, "state") }

// Load reads base/config.yaml. A missing file is not an error: defaults apply.
func Load(base string) (Config, error) {
	cfg := Config{
		PushBudget:     defaultPushBudget,
		BaselineBudget: defaultBaselineBudget,
		Ollama:         OllamaConfig{Endpoint: defaultOllamaEndpoint, Model: defaultOllamaModel},
		Import:         ImportConfig{Provider: "auto", MergeModel: defaultMergeModel},
		Learn: LearnConfig{
			Enabled: true, Provider: "auto",
			CheapModel: defaultCheapModel, StrongModel: defaultStrongModel,
			DailyUSDCap: defaultDailyUSDCap, DailyCallCap: defaultDailyCallCap,
			CandidateTTLDays: defaultCandidateTTL,
		},
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
	if cfg.Import.Provider == "" {
		cfg.Import.Provider = "auto"
	}
	if cfg.Import.MergeModel == "" {
		cfg.Import.MergeModel = defaultMergeModel
	}
	if cfg.Learn.Provider == "" {
		cfg.Learn.Provider = "auto"
	}
	if cfg.Learn.CheapModel == "" {
		cfg.Learn.CheapModel = defaultCheapModel
	}
	if cfg.Learn.StrongModel == "" {
		cfg.Learn.StrongModel = defaultStrongModel
	}
	if cfg.Learn.DailyUSDCap <= 0 {
		cfg.Learn.DailyUSDCap = defaultDailyUSDCap
	}
	if cfg.Learn.DailyCallCap <= 0 {
		cfg.Learn.DailyCallCap = defaultDailyCallCap
	}
	// Only the zero value defaults — a negative value is the explicit
	// "disable the janitor" signal and must survive.
	if cfg.Learn.CandidateTTLDays == 0 {
		cfg.Learn.CandidateTTLDays = defaultCandidateTTL
	}
	return cfg, nil
}
