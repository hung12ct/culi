package mine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/learn/queue"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

// fakeGen decodes a canned JSON payload into out, or fails.
type fakeGen struct {
	payload string
	fail    bool
	calls   int
}

func (g *fakeGen) ModelName() string { return "fake-model" }

func (g *fakeGen) Generate(_ context.Context, _, _, _ string, _ map[string]any, out any) (llmgen.Usage, error) {
	g.calls++
	u := llmgen.Usage{Prompt: 100, Completion: 50}
	if g.fail {
		return u, errors.New("fake decode failure")
	}
	if err := json.Unmarshal([]byte(g.payload), out); err != nil {
		return u, err
	}
	return u, nil
}

// newMiner builds a Miner over a sandbox base with a fresh store.
func newMiner(t *testing.T, cheap, strong *fakeGen) (*Miner, func()) {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config.KnowledgeDir(base), "lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	tier := llmtier.NewTier(cheap, strong, config.StateDir(base), 0, 100, false)
	m := &Miner{Base: base, Store: s, Tier: tier, Logf: t.Logf}
	return m, func() { s.Close() }
}

// correctionTranscript writes a transcript with one correction window.
func correctionTranscript(t *testing.T, dir string) string {
	t.Helper()
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"please write the fetcher"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done, panicking on errors"}]}}`,
		`{"type":"user","message":{"role":"user","content":"No, don't panic - wrap errors with the package prefix instead"}}`,
	}
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// cleanTranscript has no signal at all.
func cleanTranscript(t *testing.T, dir string) string {
	t.Helper()
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"please add a small helper function"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"added and tested"}]}}`,
	}
	path := filepath.Join(dir, "clean.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const lessonPayload = `{
  "lessons": [{
    "slug": "wrap-errors", "title": "Wrap errors with package prefix",
    "summary": "Return wrapped errors, never panic on expected failures.",
    "markdown": "Never panic on expected errors.\nWrap as fmt.Errorf with the package prefix.",
    "keywords": ["errors", "panic"], "scope": "lang:go",
    "confidence": 0.9, "evidence": "user: No, don't panic", "supersedes": ""
  }],
  "missing_rules": [],
  "style_observations": [{"dimension": "errors", "observation": "prefers wrapped errors", "evidence": "..."}],
  "notes": ""
}`

func TestCleanSessionCostsNothing(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	path := cleanTranscript(t, t.TempDir())

	res, cur, err := m.MineSession(context.Background(), queue.Job{TranscriptPath: path}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Windows != 0 || cheap.calls != 0 {
		t.Errorf("windows=%d calls=%d, want 0/0", res.Windows, cheap.calls)
	}
	st, _ := os.Stat(path)
	if cur.Offset != st.Size() {
		t.Errorf("cursor = %d, want %d", cur.Offset, st.Size())
	}
}

// selfMineSentinel is a hand-maintained copy of mineSystem's opening; if the
// prompt's first line is ever edited without updating the sentinel, backlog
// self-mining detection silently breaks. This guards the "keep in sync" comment.
func TestSelfMineSentinelMatchesPrompt(t *testing.T) {
	if !strings.HasPrefix(mineSystem, selfMineSentinel) {
		t.Fatalf("selfMineSentinel drifted from mineSystem opening:\n  sentinel: %q\n  prompt:   %q…", selfMineSentinel, mineSystem[:min(len(mineSystem), 80)])
	}
}

// A transcript that is one of culi's own `claude -p` mining calls (its user
// prompt opens with the mine system prompt) must be skipped for free: no model
// call, cursor advanced so the job drains, a note recorded.
func TestSkipsSelfMiningTranscript(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()

	dir := t.TempDir()
	content, err := json.Marshal(selfMineSentinel + " for durable knowledge...\n\nWindow 1:\nuser: do the thing")
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":` + string(content) + `}}`
	path := filepath.Join(dir, "self.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, cur, err := m.MineSession(context.Background(), queue.Job{TranscriptPath: path}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if cheap.calls != 0 {
		t.Errorf("self-mining transcript cost %d model calls, want 0", cheap.calls)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "self-ingestion") {
		t.Errorf("notes = %v, want a self-ingestion skip note", res.Notes)
	}
	st, _ := os.Stat(path)
	if cur.Offset != st.Size() {
		t.Errorf("cursor = %d, want %d (job must drain, not re-mine)", cur.Offset, st.Size())
	}
}

func TestMineCreatesCandidate(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	path := correctionTranscript(t, t.TempDir())

	res, _, err := m.MineSession(context.Background(), queue.Job{SessionID: "s1", TranscriptPath: path}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Windows == 0 || cheap.calls != 1 {
		t.Fatalf("windows=%d calls=%d", res.Windows, cheap.calls)
	}
	if len(res.Created) != 1 {
		t.Fatalf("created = %v", res.Created)
	}

	// File on disk: candidate, observations 1, provenance learn.
	kdir := config.KnowledgeDir(m.Base)
	card, err := knowledge.ReadCard(kdir, res.Created[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "candidate" || card.Observations != 1 {
		t.Errorf("card = status %q obs %d", card.Status, card.Observations)
	}
	if card.Provenance == nil || card.Provenance.Source != "learn" {
		t.Errorf("provenance = %+v", card.Provenance)
	}
	if !strings.Contains(card.Body, "Evidence") {
		t.Errorf("body lacks evidence: %q", card.Body)
	}

	// Indexed with candidate status (excluded from retrieval by design).
	sc, err := m.Store.CardByID(context.Background(), res.Created[0])
	if err != nil {
		t.Fatal(err)
	}
	if sc.Status != "candidate" {
		t.Errorf("indexed status = %q", sc.Status)
	}

	// Style ledger row landed.
	raw, err := os.ReadFile(filepath.Join(config.StateDir(m.Base), "style_observations.jsonl"))
	if err != nil || res.StyleObs != 1 {
		t.Fatalf("style ledger: %v (obs %d)", err, res.StyleObs)
	}
	if !strings.Contains(string(raw), "wrapped errors") {
		t.Errorf("ledger = %s", raw)
	}
}

// learn.confirm_at: 1 confirms a candidate on its first sighting — no second
// observation, no manual review. The card is born confirmed and injecting.
func TestConfirmAtOneConfirmsOnFirstSighting(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	m.ConfirmAt = 1
	path := correctionTranscript(t, t.TempDir())

	res, _, err := m.MineSession(context.Background(), queue.Job{SessionID: "s1", TranscriptPath: path}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 || len(res.Confirmed) != 1 {
		t.Fatalf("created=%v confirmed=%v, want 0 created / 1 confirmed", res.Created, res.Confirmed)
	}
	kdir := config.KnowledgeDir(m.Base)
	card, err := knowledge.ReadCard(kdir, res.Confirmed[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "confirmed" || card.Observations != 1 {
		t.Errorf("card = status %q obs %d, want confirmed/1", card.Status, card.Observations)
	}
	// Indexed as confirmed → eligible for retrieval immediately.
	sc, err := m.Store.CardByID(context.Background(), res.Confirmed[0])
	if err != nil || sc.Status != "confirmed" {
		t.Errorf("indexed status = %q (%v), want confirmed", sc.Status, err)
	}
}

// A higher threshold delays confirmation: at confirm_at: 3 a second observation
// reinforces but does not yet confirm.
func TestConfirmAtThreeDelaysConfirm(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	m.ConfirmAt = 3
	dir := t.TempDir()
	ctx := context.Background()

	p1 := correctionTranscript(t, dir)
	r1, _, err := m.MineSession(ctx, queue.Job{SessionID: "s1", TranscriptPath: p1}, queue.Cursor{})
	if err != nil || len(r1.Created) != 1 {
		t.Fatalf("first mine: created=%v err=%v", r1.Created, err)
	}
	// Second, distinct session re-observes the same lesson.
	sub := filepath.Join(dir, "second")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p2 := correctionTranscript(t, sub)
	r2, _, err := m.MineSession(ctx, queue.Job{SessionID: "s2", TranscriptPath: p2}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Reinforced) != 1 || len(r2.Confirmed) != 0 {
		t.Fatalf("second mine: reinforced=%v confirmed=%v, want reinforced / not-yet-confirmed", r2.Reinforced, r2.Confirmed)
	}
	card, err := knowledge.ReadCard(config.KnowledgeDir(m.Base), r1.Created[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "candidate" || card.Observations != 2 {
		t.Errorf("card = status %q obs %d, want candidate/2", card.Status, card.Observations)
	}
}

func TestRemineReinforcesAndConfirms(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	dir := t.TempDir()
	ctx := context.Background()

	res1, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: correctionTranscript(t, dir)}, queue.Cursor{})
	if err != nil || len(res1.Created) != 1 {
		t.Fatalf("first mine: %+v, %v", res1, err)
	}

	// Same lesson mined again (another session) → lexical dedup reinforces
	// and the 2nd observation confirms.
	path2 := filepath.Join(dir, "again.jsonl")
	lines := `{"type":"user","message":{"role":"user","content":"wrong, never panic - wrap errors with package prefix"}}` + "\n"
	if err := os.WriteFile(path2, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	res2, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: path2}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Created) != 0 || len(res2.Reinforced) != 1 || len(res2.Confirmed) != 1 {
		t.Fatalf("second mine: %+v", res2)
	}

	card, err := knowledge.ReadCard(config.KnowledgeDir(m.Base), res1.Created[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "confirmed" || card.Observations != 2 {
		t.Errorf("card = status %q obs %d", card.Status, card.Observations)
	}
	// referenced feedback recorded → utility multiplier above neutral.
	utils, err := m.Store.UtilityMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u := utils[res1.Created[0]]; u <= 1.0 {
		t.Errorf("utility = %f, want > 1 after referenced feedback", u)
	}
}

func TestEscalatesOnceOnCheapFailure(t *testing.T) {
	cheap := &fakeGen{fail: true}
	strong := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, strong)
	defer done()

	res, _, err := m.MineSession(context.Background(),
		queue.Job{TranscriptPath: correctionTranscript(t, t.TempDir())}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Escalated || cheap.calls != 1 || strong.calls != 1 {
		t.Errorf("escalated=%v cheap=%d strong=%d", res.Escalated, cheap.calls, strong.calls)
	}
	if len(res.Created) != 1 {
		t.Errorf("created = %v", res.Created)
	}
}

func TestCappedKeepsCursor(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	m.Tier = llmtier.NewTier(cheap, cheap, config.StateDir(m.Base), 0, 1, false)
	ctx := context.Background()

	// First call consumes the day's single allowed call.
	if _, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: correctionTranscript(t, t.TempDir())}, queue.Cursor{}); err != nil {
		t.Fatal(err)
	}
	path := correctionTranscript(t, t.TempDir())
	_, cur, err := m.MineSession(ctx, queue.Job{TranscriptPath: path}, queue.Cursor{})
	if !errors.Is(err, llmtier.ErrCapped) {
		t.Fatalf("err = %v, want ErrCapped", err)
	}
	if cur.Offset != 0 {
		t.Errorf("cursor advanced on capped mine: %+v", cur)
	}
}

func TestSupersedesRetiresOnConfirm(t *testing.T) {
	// Seed an old culi-authored lesson, then mine a superseding one twice.
	cheap := &fakeGen{}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	ctx := context.Background()
	kdir := config.KnowledgeDir(m.Base)

	old := knowledge.Card{
		Type: "lesson", Title: "Use panics for fatal errors", Summary: "Panic freely.",
		Body: "Panic on any error.", Scopes: []string{"lang:go"}, Status: "confirmed",
		Provenance: &knowledge.Provenance{Source: "learn"},
	}
	raw, err := knowledge.Render(old)
	if err != nil {
		t.Fatal(err)
	}
	oldRel := "lessons/use-panics.md"
	if err := os.MkdirAll(filepath.Join(kdir, "lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, oldRel), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Sync(ctx, m.Store, kdir); err != nil {
		t.Fatal(err)
	}

	payload := strings.Replace(lessonPayload, `"supersedes": ""`, `"supersedes": "lessons/use-panics"`, 1)
	cheap.payload = payload
	dir := t.TempDir()
	res1, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: correctionTranscript(t, dir)}, queue.Cursor{})
	if err != nil || len(res1.Created) != 1 {
		t.Fatalf("first mine: %+v %v", res1, err)
	}
	path2 := filepath.Join(dir, "again.jsonl")
	if err := os.WriteFile(path2, []byte(`{"type":"user","message":{"role":"user","content":"wrong, never panic - wrap errors with package prefix"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res2, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: path2}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Retired) != 1 || res2.Retired[0] != "lessons/use-panics" {
		t.Fatalf("retired = %v", res2.Retired)
	}
	oldCard, err := knowledge.ReadCard(kdir, oldRel)
	if err != nil {
		t.Fatal(err)
	}
	if oldCard.Status != "retired" {
		t.Errorf("superseded card status = %q", oldCard.Status)
	}
}

func TestValidScope(t *testing.T) {
	for _, tc := range []struct{ scope, repo, want string }{
		{"global", "phin", "global"},
		{"lang:go", "phin", "lang:go"},
		{"repo:phin", "phin", "repo:phin"},
		{"branch:x@y", "phin", "repo:phin"}, // miner may not scope to branches
		{"nonsense", "", "global"},
		{"", "phin", "repo:phin"},
	} {
		if got := validScope(tc.scope, tc.repo); got != tc.want {
			t.Errorf("validScope(%q,%q) = %q, want %q", tc.scope, tc.repo, got, tc.want)
		}
	}
}

func TestHandAuthoredNeverRewritten(t *testing.T) {
	cheap := &fakeGen{payload: lessonPayload}
	m, done := newMiner(t, cheap, cheap)
	defer done()
	ctx := context.Background()
	kdir := config.KnowledgeDir(m.Base)

	// Hand-authored card (no provenance) with the same content the miner will
	// re-derive — dedup must match it but only record feedback, never rewrite.
	hand := "---\ntitle: Wrap errors with package prefix\nsummary: Return wrapped errors, never panic on expected failures.\n---\n\nHand-written body with custom notes.\n"
	if err := os.MkdirAll(filepath.Join(kdir, "lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, "lessons", "hand.md"), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Sync(ctx, m.Store, kdir); err != nil {
		t.Fatal(err)
	}

	res, _, err := m.MineSession(ctx, queue.Job{TranscriptPath: correctionTranscript(t, t.TempDir())}, queue.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 || len(res.Reinforced) != 0 {
		t.Fatalf("res = %+v, want feedback-only note", res)
	}
	raw, err := os.ReadFile(filepath.Join(kdir, "lessons", "hand.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != hand {
		t.Error("hand-authored file was rewritten (C4 violation)")
	}
}
