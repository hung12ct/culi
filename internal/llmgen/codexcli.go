package llmgen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hung12ct/culi/internal/config"
)

// codexCLIGen uses `codex exec` and the user's existing Codex login. Calls are
// ephemeral, read-only, and schema-constrained; they do not need OPENAI_API_KEY.
type codexCLIGen struct {
	bin   string
	model string
}

func NewCodexCLI(model string) (Generator, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("llmgen: codex CLI not found in PATH: %w", err)
	}
	return &codexCLIGen{bin: bin, model: model}, nil
}

func (g *codexCLIGen) ModelName() string { return g.model + " (codex-cli)" }

func (g *codexCLIGen) Generate(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	prompt := system + "\n\n" + user + schemaInstruction(schema)
	var total Usage
	var lastErr error
	for range 2 {
		raw, usage, err := g.invoke(ctx, prompt, schema)
		total.Add(usage)
		if err != nil {
			return total, fmt.Errorf("llmgen: codex-cli call %s: %w", name, err)
		}
		if err := decodeLooseJSON(raw, out); err != nil {
			lastErr = err
			prompt += "\n\nYour previous response failed to parse: " + err.Error() + "\nRe-emit the complete JSON object only."
			continue
		}
		return total, nil
	}
	return total, fmt.Errorf("llmgen: codex-cli call %s: %w", name, lastErr)
}

func (g *codexCLIGen) invoke(ctx context.Context, prompt string, schema map[string]any) (string, Usage, error) {
	var usage Usage
	schemaFile, err := os.CreateTemp("", "culi-codex-schema-*.json")
	if err != nil {
		return "", usage, fmt.Errorf("creating output schema: %w", err)
	}
	schemaPath := schemaFile.Name()
	defer os.Remove(schemaPath)
	rawSchema, err := json.Marshal(schema)
	if err == nil {
		_, err = schemaFile.Write(rawSchema)
	}
	if closeErr := schemaFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", usage, fmt.Errorf("writing output schema: %w", err)
	}

	outFile, err := os.CreateTemp("", "culi-codex-output-*.json")
	if err != nil {
		return "", usage, fmt.Errorf("creating output file: %w", err)
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	args := []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
		"--sandbox", "read-only", "--color", "never", "--json", "--output-schema", schemaPath,
		"--output-last-message", outPath, "--model", g.model, "-"}
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Env = append(os.Environ(), config.InternalEnv+"=1")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := firstLineOf(stderr.String())
		if detail == "" {
			detail = firstLineOf(stdout.String())
		}
		return "", usage, fmt.Errorf("running codex exec: %w (%s)", err, detail)
	}
	usage = codexUsage(stdout.Bytes())
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return "", usage, fmt.Errorf("reading codex final output: %w", err)
	}
	return string(raw), usage, nil
}

func codexUsage(jsonl []byte) Usage {
	var total Usage
	scanner := bufio.NewScanner(bytes.NewReader(jsonl))
	// item.completed can include the full structured response. Keep scanning to
	// turn.completed (where usage lives) even when that response exceeds the
	// Scanner default 64 KiB token limit.
	scanner.Buffer(make([]byte, 4096), 10<<20)
	for scanner.Scan() {
		var ev struct {
			Type  string `json:"type"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		// One `codex exec` emits a single turn.completed today; summing rather
		// than overwriting keeps the count correct if a call ever spans turns.
		if json.Unmarshal(scanner.Bytes(), &ev) == nil && ev.Type == "turn.completed" {
			total.Prompt += ev.Usage.Input
			total.Completion += ev.Usage.Output
		}
	}
	return total
}
