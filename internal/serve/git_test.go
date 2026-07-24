package serve

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidSHA(t *testing.T) {
	ok := []string{"abcd", "8f7a667", "0123456789abcdef0123456789abcdef01234567"}
	bad := []string{"", "abc", "--force", "8f7a66g", "HEAD", "8f7 667",
		"01234567890123456789012345678901234567890"} // 41 chars
	for _, s := range ok {
		if !validSHA(s) {
			t.Errorf("validSHA(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validSHA(s) {
			t.Errorf("validSHA(%q) = true, want false", s)
		}
	}
}

// TestRevertCardFile drives a real git repo: a card is created, edited, then
// reverted to its first-commit content — scoped to that one file.
func TestRevertCardFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")

	card := filepath.Join(dir, "rules", "x.md")
	if err := os.MkdirAll(filepath.Dir(card), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(card, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("original\n")
	git("add", "-A")
	git("commit", "-q", "-m", "create")
	first := git("rev-parse", "--short", "HEAD")
	first = first[:len(first)-1] // strip newline
	write("edited\n")
	git("add", "-A")
	git("commit", "-q", "-m", "edit")

	if err := revertCardFile(ctx, dir, "rules/x.md", first); err != nil {
		t.Fatalf("revert: %v", err)
	}
	got, err := os.ReadFile(card)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Errorf("after revert = %q, want original", got)
	}

	// Reverting to a bogus revision must fail cleanly, not panic or wipe.
	if err := revertCardFile(ctx, dir, "rules/x.md", "deadbeef"); err == nil {
		t.Error("revert to nonexistent sha = nil, want error")
	}
	// A non-hex revision is rejected before touching git.
	if err := revertCardFile(ctx, dir, "rules/x.md", "--hard"); err == nil {
		t.Error("revert with flag-like revision = nil, want error")
	}
}
