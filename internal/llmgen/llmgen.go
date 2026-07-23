// Package llmgen is the multi-backend seam for one-shot structured LLM calls:
// system + user prompt in, schema-validated JSON out. Five backends share it:
// the Anthropic and OpenAI APIs, the Claude and Codex terminal clients, and
// local Ollama. Import and learning each resolve the subset they support.
//
// Never imported by hook hot-path packages (dependency contract C2): the
// API backends link gopheragent + vendor SDKs.
package llmgen

import (
	"context"
	"strings"
)

// Usage accumulates token counts for cost reporting. For retried calls it sums
// across attempts — the user pays for every attempt.
type Usage struct {
	Prompt     int
	Completion int
}

// Add folds another call's usage into the total.
func (u *Usage) Add(o Usage) { u.Prompt += o.Prompt; u.Completion += o.Completion }

// Generator is one structured-output call: decode the model's JSON reply into
// out, which must match schema. name labels the call in errors and (for the
// API backends) the structured-output operation. Implementations are safe for sequential
// reuse; none are goroutine-safe by contract.
type Generator interface {
	Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error)
	ModelName() string
}

// firstLineOf truncates noisy multi-line output for error messages.
func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
