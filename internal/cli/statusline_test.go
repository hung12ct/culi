package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

func TestStatuslineText(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CULI_HOME", base)
	kdir := config.KnowledgeDir(base)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(kdir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/a.md", "---\ntitle: A\nsummary: s\n---\nbody body body\n")
	write("rules/b.md", "---\ntitle: B\nsummary: s\nstatus: candidate\n---\nbody\n")

	ctx := context.Background()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "sess-x", "user-prompt-submit", "h", "", harness.Claude,
		[]store.InjectionRecord{{CardID: "rules/a", Granularity: store.GranBody, Tokens: 42}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	line, err := statuslineText(ctx, "sess-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"culi 1 cards", "42 tok this session", "1 to review"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}

	// Failed jobs surface loudly.
	failed := filepath.Join(config.InboxDir(base), "failed")
	if err := os.MkdirAll(failed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failed, "j.json.f2"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	line, _ = statuslineText(ctx, "")
	if !strings.Contains(line, "failed job") {
		t.Errorf("line %q missing failed-job warning", line)
	}
}

func TestStatuslineFailsOpen(t *testing.T) {
	t.Setenv("CULI_HOME", filepath.Join(t.TempDir(), "missing", "deep"))
	// No store, garbage stdin: must not error (renders nothing).
	if err := Statusline(nil); err != nil {
		t.Fatalf("statusline must fail open: %v", err)
	}
}
