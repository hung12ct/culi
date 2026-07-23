package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/store"
)

func writeCard(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncLifecycle(t *testing.T) {
	ctx := context.Background()
	kdir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeCard(t, kdir, "rules/a.md", "---\ntitle: A\n---\nbody a")
	writeCard(t, kdir, "rules/bad.md", "no frontmatter, no title")
	writeCard(t, kdir, "lessons/b.md", "---\ntitle: B\n---\nbody b")

	res, err := Sync(ctx, s, kdir)
	if err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	if res.Upserted != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "rules/bad.md" {
		t.Fatalf("sync 1 = %+v", res)
	}

	// Idempotent: nothing changed, nothing re-upserted.
	res, err = Sync(ctx, s, kdir)
	if err != nil || res.Upserted != 0 || res.Deleted != 0 {
		t.Fatalf("sync 2 = %+v, %v", res, err)
	}

	// Modify a (bump mtime to defeat coarse timestamps), delete b.
	time.Sleep(10 * time.Millisecond)
	writeCard(t, kdir, "rules/a.md", "---\ntitle: A v2\n---\nnew body")
	if err := os.Remove(filepath.Join(kdir, "lessons/b.md")); err != nil {
		t.Fatal(err)
	}
	res, err = Sync(ctx, s, kdir)
	if err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if res.Upserted != 1 || res.Deleted != 1 {
		t.Fatalf("sync 3 = %+v", res)
	}
	got, err := s.CardByID(ctx, "rules/a")
	if err != nil || got.Title != "A v2" {
		t.Fatalf("card after modify: %+v, %v", got, err)
	}
	if _, err := s.CardByID(ctx, "lessons/b"); err == nil {
		t.Fatal("deleted card still present")
	}
}

func TestSyncSkipsDuplicateID(t *testing.T) {
	// A copied card file with the same explicit `id:` must be skipped, not
	// abort the sync — a duplicate would otherwise invisibly break every
	// SessionStart inline reindex (review fix #1).
	ctx := context.Background()
	kdir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeCard(t, kdir, "rules/a.md", "---\nid: rules/dup\ntitle: A\n---\nx")
	writeCard(t, kdir, "rules/b.md", "---\nid: rules/dup\ntitle: B copy\n---\nx")
	res, err := Sync(ctx, s, kdir)
	if err != nil {
		t.Fatalf("sync must not abort on duplicate id: %v", err)
	}
	if res.Upserted != 1 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v, want 1 upserted + 1 skipped", res)
	}
}

func TestFullRebuild(t *testing.T) {
	ctx := context.Background()
	kdir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeCard(t, kdir, "rules/a.md", "---\ntitle: A\n---\nx")
	if _, err := Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "codex:s1", "user-prompt-submit", "", "", harness.Codex, []store.InjectionRecord{
		{CardID: "rules/a", Granularity: store.GranSummary, Tokens: 12},
	}); err != nil {
		t.Fatal(err)
	}
	// Poison the DB with a card whose file no longer exists, then Full must
	// end with exactly the on-disk truth.
	res, err := Full(ctx, s, kdir)
	if err != nil || res.Upserted != 1 {
		t.Fatalf("full = %+v, %v", res, err)
	}
	all, err := s.AllCards(ctx)
	if err != nil || len(all) != 1 || all[0].ID != "rules/a" {
		t.Fatalf("after full: %v, %v", all, err)
	}
	if sessions, err := s.SessionCount(ctx); err != nil || sessions != 1 {
		t.Fatalf("sessions after full = %d, %v; want preserved", sessions, err)
	}
}
