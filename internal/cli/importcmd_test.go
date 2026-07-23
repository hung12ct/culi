package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMergerCodexOnlyAuto(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	m, desc, err := resolveMerger("auto", "claude-sonnet-5", "http://localhost:11434")
	if err != nil || m == nil {
		t.Fatalf("resolveMerger = %v, %q, %v", m, desc, err)
	}
	if !strings.Contains(desc, "gpt-5.6-sol") || !strings.Contains(desc, "Codex CLI") {
		t.Fatalf("description = %q", desc)
	}
}

func TestResolveMergerOpenAIAndProviderModels(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	m, desc, err := resolveMerger("openai", "claude-sonnet-5", "")
	if err != nil || m == nil {
		t.Fatalf("resolveMerger = %v, %q, %v", m, desc, err)
	}
	if !strings.Contains(desc, "gpt-5.6-terra") || !strings.Contains(desc, "OpenAI API") {
		t.Fatalf("description = %q", desc)
	}
	if got := importProviderModel("claude-cli", "gpt-5.6-sol"); got != "claude-sonnet-5" {
		t.Fatalf("Claude fallback model = %q", got)
	}
}

func TestResolveMergerExplicitOpenAIRequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, _, err := resolveMerger("openai", "gpt-5.6-terra", ""); err == nil {
		t.Fatal("OpenAI provider without a key was accepted")
	}
}
