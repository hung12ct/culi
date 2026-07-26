package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/knowledge"
	"time"
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

	err := s.RecordInjections(ctx, sess, "user-prompt-submit", "h1", "/repo/x", harness.Claude, []InjectionRecord{
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
	if err := s.RecordInjections(ctx, sess, "user-prompt-submit", "h2", "/repo/x", harness.Claude, []InjectionRecord{
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

func TestMigrateV2PreservesRuntimeData(t *testing.T) {
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
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO injections(session_id,event,card_id,granularity,tokens,cwd,harness)
		VALUES ('codex:s1','user-prompt-submit','rules/x','summary',12,'/repo','codex');
		INSERT INTO card_stats(card_id,injected) VALUES ('rules/x',3);
		INSERT INTO session_state(session_id,last_prompt) VALUES ('codex:s1','hello');
		INSERT INTO meta(key,value) VALUES ('cursor','kept');
		DROP INDEX idx_inj_session;
		ALTER TABLE injections RENAME TO injections_v3;
		CREATE TABLE injections (
		  id INTEGER PRIMARY KEY,
		  session_id TEXT NOT NULL,
		  ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		  event TEXT NOT NULL,
		  card_id TEXT NOT NULL,
		  granularity TEXT NOT NULL,
		  prompt_hash TEXT NOT NULL DEFAULT '',
		  tokens INTEGER NOT NULL,
		  cwd TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO injections(id,session_id,ts,event,card_id,granularity,prompt_hash,tokens,cwd)
		SELECT id,session_id,ts,event,card_id,granularity,prompt_hash,tokens,cwd FROM injections_v3;
		DROP TABLE injections_v3;
		CREATE INDEX idx_inj_session ON injections(session_id,card_id);
		ALTER TABLE session_state DROP COLUMN attributed_at;
		PRAGMA user_version = 2;
	`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen v2 store: %v", err)
	}
	defer s2.Close()
	all, err := s2.AllCards(ctx)
	if err != nil {
		t.Fatalf("AllCards after migration: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("migration kept %d cards, want 1", len(all))
	}
	var gotHarness, cursor string
	if err := s2.db.QueryRowContext(ctx, "SELECT harness FROM injections WHERE session_id = 'codex:s1'").Scan(&gotHarness); err != nil {
		t.Fatal(err)
	}
	if gotHarness != "codex" {
		t.Fatalf("migrated harness = %q, want codex", gotHarness)
	}
	if err := s2.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'cursor'").Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != "kept" {
		t.Fatalf("meta cursor = %q, want kept", cursor)
	}
	for table, want := range map[string]int{"card_stats": 1, "session_state": 1, "injections": 1} {
		var got int
		if err := s2.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestMigrateV1AddsRuntimeAttribution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO injections(session_id,event,card_id,granularity,tokens,cwd,harness)
		VALUES ('codex:s1','user-prompt-submit','rules/x','hook',4,'/repo','codex');
		DROP INDEX idx_inj_session;
		ALTER TABLE injections RENAME TO injections_v3;
		CREATE TABLE injections (
		  id INTEGER PRIMARY KEY,
		  session_id TEXT NOT NULL,
		  ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		  event TEXT NOT NULL,
		  card_id TEXT NOT NULL,
		  granularity TEXT NOT NULL,
		  prompt_hash TEXT NOT NULL DEFAULT '',
		  tokens INTEGER NOT NULL
		);
		INSERT INTO injections(id,session_id,ts,event,card_id,granularity,prompt_hash,tokens)
		SELECT id,session_id,ts,event,card_id,granularity,prompt_hash,tokens FROM injections_v3;
		DROP TABLE injections_v3;
		CREATE INDEX idx_inj_session ON injections(session_id,card_id);
		ALTER TABLE session_state DROP COLUMN attributed_at;
		PRAGMA user_version = 1;
	`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen v1 store: %v", err)
	}
	defer s2.Close()
	var cwd, gotHarness string
	if err := s2.db.QueryRowContext(ctx, "SELECT cwd,harness FROM injections WHERE session_id = 'codex:s1'").Scan(&cwd, &gotHarness); err != nil {
		t.Fatal(err)
	}
	if cwd != "" || gotHarness != "codex" {
		t.Fatalf("migrated attribution = cwd %q, harness %q; want empty cwd and codex", cwd, gotHarness)
	}
}

func TestNewerSchemaIsRejectedWithoutDataLoss(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCard(ctx, card("rules/x", "X", "s", "b"), 1, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, path); err == nil {
		t.Fatal("Open newer schema succeeded, want an error")
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cards int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cards").Scan(&cards); err != nil {
		t.Fatal(err)
	}
	if cards != 1 {
		t.Fatalf("newer-schema check kept %d cards, want 1", cards)
	}
}

// The v4 bump adds session_state.attributed_at. Schema bumps in culi preserve
// runtime history via forward migration rather than drop-and-rebuild, so the
// migration must carry existing session state, injections and card stats
// across untouched — losing them is unrecoverable (they are not in any file).
func TestMigrateV3PreservesRuntimeData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "s1", "user-prompt-submit", "h", "/repo", harness.Claude,
		[]InjectionRecord{{CardID: "rules/a", Granularity: "body", Tokens: 42}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SwapLastPrompt(ctx, "s1", "remembered prompt"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFeedback(ctx, "rules/a", FeedbackReferenced, 0.5); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-v4 database.
	if _, err := s.db.ExecContext(ctx, `
		ALTER TABLE session_state DROP COLUMN attributed_at;
		PRAGMA user_version = 3;
	`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen v3 store: %v", err)
	}
	defer s2.Close()

	prev, err := s2.SwapLastPrompt(ctx, "s1", "next")
	if err != nil {
		t.Fatal(err)
	}
	if prev != "remembered prompt" {
		t.Errorf("last_prompt = %q, want %q — migration lost session state", prev, "remembered prompt")
	}
	usage, err := s2.CardWindowUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage["rules/a"].Tokens != 42 {
		t.Errorf("injection history lost: %+v", usage["rules/a"])
	}
	stats, err := s2.AllCardStats(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if stats["rules/a"].Referenced <= 0 {
		t.Errorf("card stats lost: %+v", stats["rules/a"])
	}
	// The new column exists and starts unclaimed.
	claimed, err := s2.ClaimAttribution(ctx, "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Error("migrated row should still be claimable")
	}
}
