package llmgen

// The one file in llmgen that touches gopheragent. pkg/llm/anthropic (v0.33.0
// per-provider split) links only the Anthropic SDK — keeps the binary lean.

import (
	"context"
	"fmt"

	"github.com/hung12ct/gopheragent/pkg/agent"
	"github.com/hung12ct/gopheragent/pkg/history"
	"github.com/hung12ct/gopheragent/pkg/llm/anthropic"
)

// anthropicGen calls the Anthropic API via gopheragent — the strongest
// backend: the schema is enforced server-side as a forced tool call.
type anthropicGen struct {
	provider agent.LLMProvider
	model    string
}

// NewAnthropic builds a generator on the given model. apiKeyFile is optional
// (""): when set, the ANTHROPIC_API_KEY is read from that file (headless
// learning can't rely on the env var reaching a hook-spawned process); when
// empty, gopheragent reads ANTHROPIC_API_KEY from the environment as before.
// Temperature 0: re-running the same call should produce the same output, so
// review conclusions survive a re-run (best-effort — Anthropic has no seed).
func NewAnthropic(model, apiKeyFile string) (Generator, error) {
	key := ""
	if apiKeyFile != "" {
		k, err := readCredentialFile(apiKeyFile, "learn.anthropic_api_key_file")
		if err != nil {
			return nil, err
		}
		key = k
	}
	p, err := anthropic.New(key, model, anthropic.WithMaxTokens(16384), anthropic.WithTemperature(0))
	if err != nil {
		return nil, fmt.Errorf("llmgen: creating anthropic provider: %w", err)
	}
	return &anthropicGen{provider: p, model: model}, nil
}

// ModelName reports the configured model.
func (g *anthropicGen) ModelName() string { return g.model }

// Generate runs one structured-output call and decodes into out.
func (g *anthropicGen) Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	result, err := agent.GenerateJSONInto(ctx, g.provider, agent.GenerateJSONRequest{
		Messages: []history.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Output: agent.StructuredOutput{Name: name, Schema: schema, Strict: true},
	}, out)
	usage := Usage{Prompt: result.Usage.PromptTokens, Completion: result.Usage.CompletionTokens}
	if err != nil {
		return usage, fmt.Errorf("llmgen: anthropic call %s: %w", name, err)
	}
	return usage, nil
}
