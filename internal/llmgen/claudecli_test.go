package llmgen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/config"
)

// The headless `claude -p` call must carry config.InternalEnv into the
// subprocess so its own culi hooks no-op — otherwise learning mines its own
// mining calls. A stub claude echoes the var back through the result field.
func TestInvokeSetsInternalEnv(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`printf '{"type":"result","is_error":false,"result":"%s","usage":{"input_tokens":1,"output_tokens":1}}\n' "$` + config.InternalEnv + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	g, err := NewCLI("some-model", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.(*cliGen).invoke(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != "1" {
		t.Errorf("%s in subprocess = %q, want \"1\"", config.InternalEnv, res.Result)
	}
}

func TestReadCredentialFile(t *testing.T) {
	dir := t.TempDir()

	// Trailing newline/whitespace is stripped like a shell's $(cat …).
	good := filepath.Join(dir, "token")
	if err := os.WriteFile(good, []byte("  sk-tok-123\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readCredentialFile(good, "learn.oauth_token_file")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-tok-123" {
		t.Errorf("got %q, want trimmed token", got)
	}

	// Leading ~ expands to the home dir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "k"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readCredentialFile("~/k", "learn.anthropic_api_key_file")
	if err != nil || got != "abc" {
		t.Errorf("~ expansion: got %q err %v", got, err)
	}

	// Missing file and empty file both error, naming the config field.
	if _, err := readCredentialFile(filepath.Join(dir, "nope"), "learn.oauth_token_file"); err == nil ||
		!strings.Contains(err.Error(), "learn.oauth_token_file") {
		t.Errorf("missing file: want labeled error, got %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(empty, "learn.oauth_token_file"); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: want empty error, got %v", err)
	}
}
