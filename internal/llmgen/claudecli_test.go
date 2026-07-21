package llmgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
