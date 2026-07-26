package mine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/store"
)

// jsonlLine renders one Claude transcript line in the shape parseLine consumes.
func jsonlLine(t *testing.T, role, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": role,
		"message": map[string]any{
			"role":    role,
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// MineSession is the only caller of attribution in production, and its guards
// (IsFinal, the WriteMu funnel, the claim, the cursor-0 entry reuse) live on
// that path rather than inside attributeUsage. Exercise it end to end.
//
// A transcript with no signal windows costs zero model calls, so this reaches
// attribution on the "clean session: free" return with Tier nil — the same
// branch a quiet real session takes.
func TestMineSessionAttributesCleanSession(t *testing.T) {
	ctx := context.Background()
	const guidance = "never tag a release without the matching changelog entry"

	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	card := knowledge.Card{
		ID: "rules/changelog", Path: "rules/changelog.md", Type: "rule",
		Title: "Changelog on tag", Summary: "Tag discipline", Body: guidance,
		Scopes: []string{"global"},
	}
	if err := s.UpsertCard(ctx, card, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "sess", "user-prompt-submit", "h", dir, harness.Claude,
		[]store.InjectionRecord{{CardID: "rules/changelog", Granularity: store.GranBody, Tokens: 20}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "session.jsonl")
	lines := jsonlLine(t, "user", "can you cut a release for me") + "\n" +
		jsonlLine(t, "assistant", "I will not tag a release without the matching changelog entry, so first I will update it") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Miner{Base: dir, Store: s} // Tier nil: a clean session must never need one
	job := queue.Job{SessionID: "sess", TranscriptPath: path, CWD: dir,
		Source: harness.Claude, Trigger: "session-end"}

	res, next, err := m.MineSession(ctx, job, queue.Cursor{})
	if err != nil {
		t.Fatalf("MineSession: %v", err)
	}
	if res.Windows != 0 {
		t.Fatalf("windows = %d, want 0 — fixture is no longer the free path", res.Windows)
	}
	if res.Attributed != 1 {
		t.Fatalf("Attributed = %d, want 1 (notes: %v)", res.Attributed, res.Notes)
	}
	if next.Offset == 0 {
		t.Error("cursor did not advance")
	}

	// The whole point of the claim: a second final job for the same session
	// (Codex rescan, or a resumed session ending again) must not re-credit.
	res2, _, err := m.MineSession(ctx, job, queue.Cursor{})
	if err != nil {
		t.Fatalf("second MineSession: %v", err)
	}
	if res2.Attributed != 0 {
		t.Errorf("second pass credited %d cards; credit is additive and must land once", res2.Attributed)
	}
}

// A non-final job is mid-session bookkeeping (Stop refreshing the pointer job)
// and must not settle attribution.
func TestMineSessionSkipsAttributionForNonFinalJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	card := knowledge.Card{
		ID: "rules/changelog", Path: "rules/changelog.md", Type: "rule",
		Title: "Changelog on tag", Summary: "Tag discipline",
		Body: "never tag a release without the matching changelog entry", Scopes: []string{"global"},
	}
	if err := s.UpsertCard(ctx, card, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInjections(ctx, "sess", "user-prompt-submit", "h", dir, harness.Claude,
		[]store.InjectionRecord{{CardID: "rules/changelog", Granularity: store.GranBody, Tokens: 20}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(
		jsonlLine(t, "user", "cut a release")+"\n"+
			jsonlLine(t, "assistant", "I will not tag a release without the matching changelog entry now")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Miner{Base: dir, Store: s}
	job := queue.Job{SessionID: "sess", TranscriptPath: path, CWD: dir,
		Source: harness.Claude, Trigger: "stop"}

	res, _, err := m.MineSession(ctx, job, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attributed != 0 {
		t.Errorf("a Stop-triggered job credited %d cards; only a final job settles the session", res.Attributed)
	}
	// And the session is still claimable afterwards, so the real SessionEnd works.
	if claimed, err := s.ClaimAttribution(ctx, "sess", time.Now()); err != nil || !claimed {
		t.Errorf("session should remain unclaimed after a non-final job (claimed=%v err=%v)", claimed, err)
	}
}
