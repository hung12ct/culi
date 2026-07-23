package llmgen

import (
	"context"
	"fmt"

	"github.com/hung12ct/gopheragent/pkg/agent"
	"github.com/hung12ct/gopheragent/pkg/history"
	"github.com/hung12ct/gopheragent/pkg/llm/openai"
)

// openAIGen calls the metered OpenAI API with server-side JSON Schema output.
// It is separate from codexCLIGen: OPENAI_API_KEY usage is billed through the
// API account, while codex exec uses the user's existing Codex login.
type openAIGen struct {
	provider agent.LLMProvider
	model    string
}

// NewOpenAI builds a metered OpenAI generator. apiKeyFile is optional; when
// empty the provider reads OPENAI_API_KEY from the environment.
func NewOpenAI(model, apiKeyFile string) (Generator, error) {
	key := ""
	if apiKeyFile != "" {
		var err error
		key, err = readCredentialFile(apiKeyFile, "learn.openai_api_key_file")
		if err != nil {
			return nil, err
		}
	}
	p, err := openai.New(key, model)
	if err != nil {
		return nil, fmt.Errorf("llmgen: creating openai provider: %w", err)
	}
	return &openAIGen{provider: p, model: model}, nil
}

func (g *openAIGen) ModelName() string { return g.model + " (openai)" }

func (g *openAIGen) Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	result, err := agent.GenerateJSONInto(ctx, g.provider, agent.GenerateJSONRequest{
		Messages: []history.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Output: agent.StructuredOutput{Name: name, Schema: schema, Strict: true},
	}, out)
	usage := Usage{Prompt: result.Usage.PromptTokens, Completion: result.Usage.CompletionTokens}
	if err != nil {
		return usage, fmt.Errorf("llmgen: openai call %s: %w", name, err)
	}
	return usage, nil
}
