package llmgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewOpenAIUsesKeyFile(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "openai-key")
	if err := os.WriteFile(path, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gen, err := NewOpenAI("gpt-test", path)
	if err != nil {
		t.Fatal(err)
	}
	if got := gen.ModelName(); !strings.Contains(got, "gpt-test") || !strings.Contains(got, "openai") {
		t.Fatalf("ModelName = %q", got)
	}
}

func TestNewOpenAIRequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := NewOpenAI("gpt-test", ""); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}
