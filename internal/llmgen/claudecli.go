package llmgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hung12ct/culi/internal/config"
)

// cliGen runs calls through `claude -p` (headless Claude Code) — zero API
// key, billed to the user's existing subscription. Every culi user has the
// claude binary by definition. Weaker than the API backend in exactly one
// way: no server-side schema enforcement, so output is decoded leniently and
// retried once with the decode error fed back.
type cliGen struct {
	bin       string
	model     string
	tokenFile string // optional: read CLAUDE_CODE_OAUTH_TOKEN from here (see NewCLI)
}

// NewCLI builds a generator on the claude CLI found in PATH. tokenFile is
// optional (""): when set, each headless call reads a CLAUDE_CODE_OAUTH_TOKEN
// from that file and injects it into the subprocess env — needed for headless
// background learning, since Claude Code does not hand its OAuth token to
// hook-spawned processes.
func NewCLI(model, tokenFile string) (Generator, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("llmgen: claude CLI not found in PATH: %w", err)
	}
	return &cliGen{bin: bin, model: model, tokenFile: tokenFile}, nil
}

// ModelName tags the model with the transport so provenance records how the
// output was produced.
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

func (g *cliGen) Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	prompt := system + "\n\n" + user + schemaInstruction(schema)
	var usage Usage
	var lastErr error
	for range 2 {
		res, err := g.invoke(ctx, prompt)
		usage.Prompt += res.Usage.InputTokens
		usage.Completion += res.Usage.OutputTokens
		if err != nil {
			return usage, fmt.Errorf("llmgen: claude-cli call %s: %w", name, err)
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
	return usage, fmt.Errorf("llmgen: claude-cli call %s: %w", name, lastErr)
}

// invoke runs one headless call, prompt on stdin.
func (g *cliGen) invoke(ctx context.Context, prompt string) (cliResult, error) {
	var res cliResult
	cmd := exec.CommandContext(ctx, g.bin, "-p", "--output-format", "json", "--model", g.model)
	// This headless call runs under the user's Claude Code settings, so it fires
	// culi's own hooks. Tag it internal so the hook path no-ops (see
	// config.InternalEnv) — otherwise learning mines its own mining calls.
	env := append(os.Environ(), config.InternalEnv+"=1")
	if g.tokenFile != "" {
		tok, err := readCredentialFile(g.tokenFile, "learn.oauth_token_file")
		if err != nil {
			return res, err
		}
		// Appended last so it wins over any inherited (stale) value — the
		// configured file is the authoritative account for headless learning.
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok)
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The CLI often writes its real complaint ("Not logged in", quota
		// messages) to STDOUT — surface both streams or the error is a blank.
		detail := firstLineOf(stderr.String())
		if detail == "" {
			detail = firstLineOf(stdout.String())
		}
		return res, fmt.Errorf("running claude -p: %w (%s)", err, detail)
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return res, fmt.Errorf("decoding claude -p wrapper: %w", err)
	}
	if res.IsError {
		return res, fmt.Errorf("claude -p reported an error: %s", firstLineOf(res.Result))
	}
	return res, nil
}

// readCredentialFile reads a secret (OAuth token or API key) from path (leading
// ~ expands to the home dir), trimming surrounding whitespace/newlines the way
// a shell's $(cat …) would. A missing/empty file is a clear error naming the
// config field (label), not a silent fall-through to "Not logged in". The
// secret is never logged.
func readCredentialFile(path, label string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("llmgen: resolving ~ in %s: %w", label, err)
		}
		path = filepath.Join(home, path[2:])
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("llmgen: reading %s %q: %w", label, path, err)
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", fmt.Errorf("llmgen: %s %q is empty", label, path)
	}
	return secret, nil
}
