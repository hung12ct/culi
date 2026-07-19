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
		cheap, err := llmgen.NewAnthropic(lc.CheapModel)
		if err != nil {
			return nil, "", err
		}
		strong, err := llmgen.NewAnthropic(lc.StrongModel)
		if err != nil {
			return nil, "", err
		}
		return mk(cheap, strong, true, lc.CheapModel+"→"+lc.StrongModel+" via Anthropic API")
	}
	cliPair := func() (*Tier, string, error) {
		cheap, err := llmgen.NewCLI(lc.CheapModel)
		if err != nil {
			return nil, "", err
		}
		strong, err := llmgen.NewCLI(lc.StrongModel)
		if err != nil {
			return nil, "", err
		}
		return mk(cheap, strong, false, lc.CheapModel+"→"+lc.StrongModel+" via claude CLI")
	}

	switch lc.Provider {
	case "none":
		return nil, "learning LLM disabled (learn.provider: none)", nil
	case "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return nil, "", fmt.Errorf("llmtier: provider anthropic needs ANTHROPIC_API_KEY")
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
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
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
// recorded even for failed calls — the user paid for every attempt. A cap hit
// returns ErrCapped before any model call; the caller leaves its work queued.
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
