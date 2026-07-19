package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// cliGen runs merges through `claude -p` (headless Claude Code) — zero API
// key, billed to the user's existing subscription. Every culi user has the
// claude binary by definition. Weaker than the API backend in exactly one
// way: no server-side schema enforcement, so output is decoded leniently and
// retried once with the decode error fed back.
type cliGen struct {
	bin   string
	model string
}

// NewCLIMerger builds a merger on the claude CLI found in PATH.
func NewCLIMerger(model string) (*GenMerger, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("importer: claude CLI not found in PATH: %w", err)
	}
	return &GenMerger{gen: &cliGen{bin: bin, model: model}}, nil
}

// ModelName tags provenance with the transport so a reviewed merge records
// how it was produced.
func (g *cliGen) ModelName() string { return g.model + " (claude-cli)" }

// cliResult is the subset of `claude -p --output-format json` we consume.
// Shape verified against the real CLI: result text often arrives fenced even
// when the prompt forbids it.
type cliResult struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (g *cliGen) generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	prompt := system + "\n\n" + user + schemaInstruction(schema)
	var usage Usage
	var lastErr error
	for range 2 {
		res, err := g.invoke(ctx, prompt)
		usage.Prompt += res.Usage.InputTokens
		usage.Completion += res.Usage.OutputTokens
		if err != nil {
			return usage, fmt.Errorf("importer: claude-cli call %s: %w", name, err)
		}
		if err := decodeLooseJSON(res.Result, out); err != nil {
			// One corrective round: feed the decode error back (plan §upstream
			// candidate 5, implemented locally first).
			lastErr = err
			prompt = prompt + "\n\nYour previous response failed to parse: " + err.Error() +
				"\nRe-emit the complete JSON object only."
			continue
		}
		return usage, nil
	}
	return usage, fmt.Errorf("importer: claude-cli call %s: %w", name, lastErr)
}

// invoke runs one headless call, prompt on stdin.
func (g *cliGen) invoke(ctx context.Context, prompt string) (cliResult, error) {
	var res cliResult
	cmd := exec.CommandContext(ctx, g.bin, "-p", "--output-format", "json", "--model", g.model)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return res, fmt.Errorf("running claude -p: %w (%s)", err, firstLineOf(stderr.String()))
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return res, fmt.Errorf("decoding claude -p wrapper: %w", err)
	}
	if res.IsError {
		return res, fmt.Errorf("claude -p reported an error: %s", firstLineOf(res.Result))
	}
	return res, nil
}

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
