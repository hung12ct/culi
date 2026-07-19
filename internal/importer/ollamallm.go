package importer

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

// ollamaGen runs merges through a local Ollama chat model — fully free and
// private. Ollama enforces the JSON schema server-side via the `format`
// field (structured outputs), so decoding is strict. Quality depends on the
// local model; the staging + review gate is the safety net either way.
type ollamaGen struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewOllamaMerger builds a merger on a local Ollama chat model (this is a
// GENERATION model, e.g. qwen3 — not the embedding model).
func NewOllamaMerger(endpoint, model string) *GenMerger {
	return &GenMerger{gen: &ollamaGen{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		client:   &http.Client{Timeout: 10 * time.Minute}, // local models can be slow
	}}
}

// ModelName tags provenance with the transport.
func (g *ollamaGen) ModelName() string { return g.model + " (ollama)" }

func (g *ollamaGen) generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
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
		return Usage{}, fmt.Errorf("importer: marshaling ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Usage{}, fmt.Errorf("importer: building ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("importer: ollama call %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Usage{}, fmt.Errorf("importer: ollama call %s: status %d: %s", name, resp.StatusCode, firstLineOf(string(msg)))
	}
	var res struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Usage{}, fmt.Errorf("importer: decoding ollama response: %w", err)
	}
	usage := Usage{Prompt: res.PromptEvalCount, Completion: res.EvalCount}
	if err := decodeLooseJSON(res.Message.Content, out); err != nil {
		return usage, fmt.Errorf("importer: ollama call %s: %w", name, err)
	}
	return usage, nil
}
