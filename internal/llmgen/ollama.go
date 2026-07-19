package llmgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ollamaGen runs calls through a local Ollama chat model — fully free and
// private. Ollama enforces the JSON schema server-side via the `format`
// field (structured outputs), so decoding is strict. Quality depends on the
// local model; callers keep their own review/staging safety nets.
type ollamaGen struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewOllama builds a generator on a local Ollama chat model (a GENERATION
// model, e.g. qwen3 — not the embedding model).
func NewOllama(endpoint, model string) Generator {
	return &ollamaGen{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		client:   &http.Client{Timeout: 10 * time.Minute}, // local models can be slow
	}
}

// ModelName tags the model with the transport.
func (g *ollamaGen) ModelName() string { return g.model + " (ollama)" }

func (g *ollamaGen) Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model": g.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"stream":  false,
		"format":  schema, // server-side structured output
		"options": map[string]any{"temperature": 0},
	})
	if err != nil {
		return Usage{}, fmt.Errorf("llmgen: marshaling ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Usage{}, fmt.Errorf("llmgen: building ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("llmgen: ollama call %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Usage{}, fmt.Errorf("llmgen: ollama call %s: status %d: %s", name, resp.StatusCode, firstLineOf(string(msg)))
	}
	var res struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Usage{}, fmt.Errorf("llmgen: decoding ollama response: %w", err)
	}
	usage := Usage{Prompt: res.PromptEvalCount, Completion: res.EvalCount}
	if err := decodeLooseJSON(res.Message.Content, out); err != nil {
		return usage, fmt.Errorf("llmgen: ollama call %s: %w", name, err)
	}
	return usage, nil
}
