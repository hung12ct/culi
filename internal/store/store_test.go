package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func card(id, title, summary, body string, scopes ...string) knowledge.Card {
	if len(scopes) == 0 {
		scopes = []string{"global"}
	}
	return knowledge.Card{
		ID: id, Path: id + ".md", Type: "rule", Title: title,
		Summary: summary, Body: body, Scopes: scopes,
		TokSummary:  knowledge.EstimateTokens(summary),
		TokBody:     knowledge.EstimateTokens(body),
		ContentHash: "hash-" + id,
	}
}

func TestUpsertSearchDelete(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	c1 := card("rules/go-errors", "Wrap errors", "Wrap with package prefix using fmt.Errorf", "Errors are wrapped as pkg: doing X.")
	c2 := card("rules/commits", "Commit style", "Conventional commits with scope", "Use feat(scope): subject format.")
	if err := s.UpsertCard(ctx, c1, 1, 100); err != nil {
		t.Fatalf("upsert c1: %v", err)
	}
	if err := s.UpsertCard(ctx, c2, 2, 100); err != nil {
		t.Fatalf("upsert c2: %v", err)
	}

	hits, err := s.SearchBM25(ctx, `"errors" OR "wrap"`, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	got, err := s.CardsByRowid(ctx, []int64{hits[0].Rowid})
	if err != nil || len(got) != 1 || got[0].ID != "rules/go-errors" {
		t.Fatalf("hydrate: %v %v", got, err)
	}

	// Re-upsert same path with new content replaces, not duplicates.
	c1b := c1
	c1b.Title = "Wrap errors v2"
	if err := s.UpsertCard(ctx, c1b, 3, 120); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all, err := s.AllCards(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("AllCards after re-upsert = %d, want 2 (%v)", len(all), err)
	}

	if err := s.DeleteCardByPath(ctx, c1.Path); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, err = s.SearchBM25(ctx, `"errors"`, 10)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("fts row survived delete: %v", hits)
	}
}

func TestDiacriticInsensitiveSearch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	c := card("rules/vi", "Xử lý lỗi", "luôn bọc lỗi với tiền tố gói", "chi tiết về xử lý lỗi trong Go")
	if err := s.UpsertCard(ctx, c, 1, 50); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Query without diacritics must still match (remove_diacritics 2).
	hits, err := s.SearchBM25(ctx, `"xu" OR "ly" OR "loi"`, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("diacritic-folded search found %d hits, want 1", len(hits))
	}
}

func TestInjectionDedupMonotonic(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	sess := "sess-1"

	err := s.RecordInjections(ctx, sess, "user-prompt-submit", "h1", "/repo/x", []InjectionRecord{
		{CardID: "a", Granularity: GranHook, Tokens: 15},
		{CardID: "b", Granularity: GranBody, Tokens: 300},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	levels, err := s.InjectedLevels(ctx, sess)
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	if levels["a"] != GranLevel(GranHook) || levels["b"] != GranLevel(GranBody) {
		t.Fatalf("levels = %v", levels)
	}

	// Upgrade a to summary; highest level wins.
	if err := s.RecordInjections(ctx, sess, "user-prompt-submit", "h2", "/repo/x", []InjectionRecord{
		{CardID: "a", Granularity: GranSummary, Tokens: 60},
	}); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	levels, _ = s.InjectedLevels(ctx, sess)
	if levels["a"] != GranLevel(GranSummary) {
		t.Fatalf("a level = %d, want summary", levels["a"])
	}

	// Other sessions are unaffected; reset clears.
	other, _ := s.InjectedLevels(ctx, "sess-2")
	if len(other) != 0 {
		t.Fatalf("cross-session leak: %v", other)
	}
	if err := s.ResetSession(ctx, sess); err != nil {
		t.Fatalf("reset: %v", err)
	}
	levels, _ = s.InjectedLevels(ctx, sess)
	if len(levels) != 0 {
		t.Fatalf("reset left levels: %v", levels)
	}
}

func TestSwapLastPrompt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	prev, err := s.SwapLastPrompt(ctx, "s1", "first prompt")
	if err != nil || prev != "" {
		t.Fatalf("first swap = %q, %v", prev, err)
	}
	prev, err = s.SwapLastPrompt(ctx, "s1", "second prompt")
	if err != nil || prev != "first prompt" {
		t.Fatalf("second swap = %q, %v", prev, err)
	}
}

func TestShortIDStable(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	c := card("rules/x", "X", "sum", "body")
	if err := s.UpsertCard(ctx, c, 1, 10); err != nil {
		t.Fatal(err)
	}
	got, err := s.CardByID(ctx, "rules/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.ShortID != knowledge.ShortID("rules/x") {
		t.Fatalf("short id %q != derived %q", got.ShortID, knowledge.ShortID("rules/x"))
	}
	// Lookup by short ID works too.
	byShort, err := s.CardByID(ctx, got.ShortID)
	if err != nil || byShort.ID != "rules/x" {
		t.Fatalf("lookup by short id: %v %v", byShort.ID, err)
	}
}

func TestRebuildOnVersionMismatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCard(ctx, card("rules/x", "X", "s", "b"), 1, 10); err != nil {
		t.Fatal(err)
	}
	// Simulate an old schema version.
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen with version mismatch: %v", err)
	}
	defer s2.Close()
	all, err := s2.AllCards(ctx)
	if err != nil {
		t.Fatalf("AllCards after rebuild: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("rebuild kept %d cards, want 0 (index is disposable)", len(all))
	}
}
