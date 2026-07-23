package llmgen

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexCLIGenerateIsInternalAndReadsUsage(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
args="$*"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then out="$2"; shift 2; continue; fi
  shift
done
cat >/dev/null
printf '{"value":"%s","args":"%s"}' "$CULI_INTERNAL" "$args" > "$out"
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":7}}'
`
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gen := &codexCLIGen{bin: bin, model: "gpt-test"}
	var out struct {
		Value string `json:"value"`
		Args  string `json:"args"`
	}
	usage, err := gen.Generate(context.Background(), "system", "user", "test", map[string]any{"type": "object"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Value != "1" {
		t.Fatalf("CULI_INTERNAL = %q", out.Value)
	}
	for _, want := range []string{"--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox read-only", "--output-schema", "--model gpt-test"} {
		if !strings.Contains(out.Args, want) {
			t.Errorf("codex args missing %q: %s", want, out.Args)
		}
	}
	if usage.Prompt != 12 || usage.Completion != 7 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCodexUsageIgnoresOtherEvents(t *testing.T) {
	raw := append([]byte(`{"type":"item.completed","content":"`), bytes.Repeat([]byte("x"), 70<<10)...)
	raw = append(raw, []byte("\"}\nnot-json\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}\n")...)
	if got := codexUsage(raw); got.Prompt != 4 || got.Completion != 2 {
		t.Fatalf("usage = %+v", got)
	}
}
