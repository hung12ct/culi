// Package llmtier gives the learning pipelines their model access: an
// explicit Cheap/Strong generator pair (plan §cost control) behind the
// multi-backend llmgen seam, gated by a persistent daily spend ledger. Every
// pipeline call goes through Tier.Generate, so no learning code can bypass
// the caps.
package llmtier

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/llmgen"
)

// Tier is the resolved Cheap/Strong pair plus its spend gate.
type Tier struct {
	Cheap  llmgen.Generator
	Strong llmgen.Generator

	ledger  *Ledger
	usdCap  float64
	callCap int
	priced  bool // anthropic API: estimate real dollars; CLI/ollama cost $0
}

// NewTier assembles a tier directly. Resolve is the config-driven path; this
// exists for tests and pipelines with custom backends.
func NewTier(cheap, strong llmgen.Generator, stateDir string, usdCap float64, callCap int, priced bool) *Tier {
	return &Tier{
		Cheap: cheap, Strong: strong, ledger: LoadLedger(stateDir),
		usdCap: usdCap, callCap: callCap, priced: priced,
	}
}

// Resolve builds the tier from config, mirroring the import backend policy:
// "auto" prefers the strongest thing the user already has and never errors —
// a nil Tier with a reason means learning quietly cannot run. Explicit
// providers DO error when their prerequisite is missing.
func Resolve(lc config.LearnConfig, ollamaEndpoint, stateDir string) (*Tier, string, error) {
	if !lc.Enabled {
		return nil, "learning disabled (learn.enabled: false)", nil
	}
	mk := func(cheap, strong llmgen.Generator, priced bool, desc string) (*Tier, string, error) {
		return NewTier(cheap, strong, stateDir, lc.DailyUSDCap, lc.DailyCallCap, priced), desc, nil
	}
	anthropicPair := func() (*Tier, string, error) {
		cheap, err := llmgen.NewAnthropic(lc.CheapModel, lc.AnthropicAPIKeyFile)
		if err != nil {
			return nil, "", err
		}
		strong, err := llmgen.NewAnthropic(lc.StrongModel, lc.AnthropicAPIKeyFile)
		if err != nil {
			return nil, "", err
		}
		return mk(cheap, strong, true, lc.CheapModel+"→"+lc.StrongModel+" via Anthropic API")
	}
	cliPair := func() (*Tier, string, error) {
		cheap, err := llmgen.NewCLI(lc.CheapModel, lc.OAuthTokenFile)
		if err != nil {
			return nil, "", err
		}
		strong, err := llmgen.NewCLI(lc.StrongModel, lc.OAuthTokenFile)
		if err != nil {
			return nil, "", err
		}
		return mk(cheap, strong, false, lc.CheapModel+"→"+lc.StrongModel+" via claude CLI")
	}

	// A configured API-key file counts as "have a key" everywhere the env var
	// does — headless learning reads the key from the file (see NewAnthropic).
	haveAPIKey := os.Getenv("ANTHROPIC_API_KEY") != "" || lc.AnthropicAPIKeyFile != ""
	switch lc.Provider {
	case "none":
		return nil, "learning LLM disabled (learn.provider: none)", nil
	case "anthropic":
		if !haveAPIKey {
			return nil, "", fmt.Errorf("llmtier: provider anthropic needs ANTHROPIC_API_KEY (env or learn.anthropic_api_key_file)")
		}
		return anthropicPair()
	case "claude-cli":
		return cliPair()
	case "ollama":
		if strings.HasPrefix(lc.CheapModel, "claude") || strings.HasPrefix(lc.StrongModel, "claude") {
			return nil, "", fmt.Errorf("llmtier: provider ollama needs local models — set learn.cheap_model / learn.strong_model (e.g. qwen3)")
		}
		return mk(llmgen.NewOllama(ollamaEndpoint, lc.CheapModel),
			llmgen.NewOllama(ollamaEndpoint, lc.StrongModel), false,
			lc.CheapModel+"→"+lc.StrongModel+" via local Ollama")
	case "auto", "":
		if haveAPIKey {
			if t, desc, err := anthropicPair(); err == nil {
				return t, desc, nil
			}
		}
		if t, desc, err := cliPair(); err == nil {
			return t, desc, nil
		}
		return nil, "no learning backend — set ANTHROPIC_API_KEY, install the claude CLI, " +
			"or set learn.provider: ollama with local models", nil
	default:
		return nil, "", fmt.Errorf("llmtier: unknown learn provider %q (want auto|anthropic|claude-cli|ollama|none)", lc.Provider)
	}
}

// Generate runs one capped structured call on the chosen tier. Usage is
// recorded even for failed calls — the user paid for every attempt — EXCEPT
// deterministic backend-unavailable failures (auth/config), which spent nothing
// and would otherwise let identical retries drain the cap (see
// isBackendUnavailable / ErrBackendUnavailable). A cap hit returns ErrCapped
// before any model call; the caller leaves its work queued.
func (t *Tier) Generate(ctx context.Context, strong bool, system, user, name string, schema map[string]any, out any) (llmgen.Usage, error) {
	gen := t.Cheap
	if strong {
		gen = t.Strong
	}
	now := time.Now().UTC()
	if err := t.ledger.Allow(now, t.usdCap, t.callCap); err != nil {
		return llmgen.Usage{}, err
	}
	usage, err := gen.Generate(ctx, system, user, name, schema, out)
	if err != nil && isBackendUnavailable(err) {
		// Deterministic env failure (CLI logged out / bad API key): nothing was
		// spent and a retry this run fails identically. Do NOT record the call —
		// otherwise 40 identical login failures exhaust daily_call_cap and stall
		// learning even after the user re-authenticates. Surface a typed error so
		// the runner stops the batch and keeps jobs queued instead of parking
		// good transcripts as "failed".
		return usage, fmt.Errorf("llmtier: %w: %v", ErrBackendUnavailable, err)
	}
	usd := 0.0
	if t.priced {
		usd = estimateUSD(gen.ModelName(), usage)
	}
	t.ledger.Record(now, usage, usd)
	if serr := t.ledger.Save(); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return usage, fmt.Errorf("llmtier: %w", err)
	}
	return usage, nil
}

// isBackendUnavailable spots deterministic auth/config failures that retrying
// this run cannot fix — a logged-out claude CLI or a missing/invalid API key.
// String-matched as a local workaround: gopheragent's llmgen returns these as
// opaque wrapped errors today. Replace with a typed llmgen auth/config error
// once upstream exposes one (logged to gopheragent BACKLOG.md).
func isBackendUnavailable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"not logged in",        // claude CLI, signed out
		"/login",               // claude CLI, "Please run /login"
		"invalid x-api-key",    // Anthropic API, bad key
		"authentication_error", // Anthropic API error type
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
