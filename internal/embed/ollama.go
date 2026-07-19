package embed

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

// Ollama calls the local /api/embed endpoint. Vectors come back Normalized so
// Dot is cosine. The zero timeout on the shared client is deliberate: callers
// bound each call with ctx (100ms on the hot path, seconds for indexing).
type Ollama struct {
	Endpoint string // e.g. http://localhost:11434
	Model    string // e.g. nomic-embed-text
	Client   *http.Client
}

// NewOllama builds an embedder for endpoint/model.
func NewOllama(endpoint, model string) *Ollama {
	return &Ollama{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		// Transport-level dial timeout so a firewalled endpoint fails fast even
		// under a generous ctx; per-call deadlines still come from ctx.
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed embeds texts in one batched request. Every returned vector is unit
// length. Order matches input.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"model": o.Model, "input": texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshaling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: calling ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embed: ollama status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decoding response: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: got %d embeddings for %d texts", len(out.Embeddings), len(texts))
	}
	for i := range out.Embeddings {
		Normalize(out.Embeddings[i])
	}
	return out.Embeddings, nil
}
