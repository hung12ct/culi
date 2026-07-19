package style

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/llmtier"
	"github.com/hung12ct/culi/internal/llmgen"
	"github.com/hung12ct/culi/internal/store"
)

type fakeGen struct {
	payload string
	calls   int
	fail    bool
}

func (g *fakeGen) ModelName() string { return "fake-strong" }

func (g *fakeGen) Generate(_ context.Context, _, _, _ string, _ map[string]any, out any) (llmgen.Usage, error) {
	g.calls++
	if g.fail {
		return llmgen.Usage{}, errors.New("boom")
	}
	return llmgen.Usage{Prompt: 100, Completion: 40}, json.Unmarshal([]byte(g.payload), out)
}

func newSynth(t *testing.T, fake *fakeGen) *Synthesizer {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config.KnowledgeDir(base), "styles"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Synthesizer{
		Base: base, Store: s,
		Tier: llmtier.NewTier(fake, fake, config.StateDir(base), 0, 100, false),
	}
}

// seedLedger writes n observation rows for (repo, dimension) across sessions.
func seedLedger(t *testing.T, base, repo, dim string, sessions []string) {
	t.Helper()
	stateDir := config.StateDir(base)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "style_observations.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, sess := range sessions {
		raw, _ := json.Marshal(Observation{
			TS: "2026-07-19T00:00:00Z", SessionID: sess, Repo: repo,
			Dimension: dim, Observation: "prefers table tests with t.Run", Evidence: "e",
		})
		f.Write(append(raw, '\n'))
	}
}

const synthPayload = `{"cards":[{"slug":"table-tests","dimension":"tests","title":"Prefer table tests","summary":"Tests are table-driven with t.Run subtests.","markdown":"- Write table-driven tests\n- Name cases via t.Run","keywords":["tests","table"],"scope":"repo:phin","confidence":0.9}],"contradicted":[],"notes":""}`

func TestDuePolicy(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		st    synthState
		total int
		want  bool
	}{
		{"nothing new", synthState{Consumed: 5, LastAt: now.Add(-30 * 24 * time.Hour)}, 5, false},
		{"burst of new obs", synthState{Consumed: 0, LastAt: now}, synthNewObs, true},
		{"few new, recent", synthState{Consumed: 0, LastAt: now.Add(-time.Hour)}, 3, false},
		{"few new, week old", synthState{Consumed: 0, LastAt: now.Add(-8 * 24 * time.Hour)}, 3, true},
	} {
		if got := due(tc.st, tc.total, now); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestQualifyGroups(t *testing.T) {
	rows := []Observation{
		{SessionID: "a", Repo: "r", Dimension: "tests", Observation: "x"},
		{SessionID: "a", Repo: "r", Dimension: "tests", Observation: "y"},
		{SessionID: "b", Repo: "r", Dimension: "tests", Observation: "z"},
		// only one session: not qualifying
		{SessionID: "a", Repo: "r", Dimension: "errors", Observation: "p"},
		{SessionID: "a", Repo: "r", Dimension: "errors", Observation: "q"},
		{SessionID: "a", Repo: "r", Dimension: "errors", Observation: "s"},
		// too few observations
		{SessionID: "a", Repo: "r2", Dimension: "naming", Observation: "n"},
		{SessionID: "b", Repo: "r2", Dimension: "naming", Observation: "m"},
	}
	groups := qualifyGroups(rows)
	if len(groups) != 1 || groups[0].Dimension != "tests" || groups[0].Sessions != 2 {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestSynthesisCreatesReinforcesConfirms(t *testing.T) {
	fake := &fakeGen{payload: synthPayload}
	sy := newSynth(t, fake)
	ctx := context.Background()
	seedLedger(t, sy.Base, "phin", "tests", []string{"s1", "s2", "s3"})

	res, err := sy.Run(ctx, false, time.Now().UTC().Add(-0))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || len(res.Created) != 1 {
		t.Fatalf("res = %+v", res)
	}
	card, err := knowledge.ReadCard(config.KnowledgeDir(sy.Base), res.Created[0]+".md")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != "candidate" || card.Observations != 1 || card.Scopes[0] != "repo:phin" {
		t.Errorf("card = %+v", card)
	}

	// Second synthesis (new rows re-arm the trigger): same slug → reinforce
	// and confirm.
	seedLedger(t, sy.Base, "phin", "tests", []string{"s4", "s5", "s6"})
	res2, err := sy.Run(ctx, false, time.Now().UTC().Add(10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Reinforced) != 1 || len(res2.Confirmed) != 1 {
		t.Fatalf("res2 = %+v", res2)
	}
	card2, _ := knowledge.ReadCard(config.KnowledgeDir(sy.Base), res.Created[0]+".md")
	if card2.Status != "confirmed" || card2.Observations != 2 {
		t.Errorf("card2 = status %q obs %d", card2.Status, card2.Observations)
	}
}

func TestSynthesisTriggerAndGroupGates(t *testing.T) {
	fake := &fakeGen{payload: synthPayload}
	sy := newSynth(t, fake)
	ctx := context.Background()
	now := time.Now().UTC()

	// Empty ledger: not due, no call.
	if res, err := sy.Run(ctx, false, now); err != nil || res.Ran || fake.calls != 0 {
		t.Fatalf("empty: %+v calls=%d err=%v", res, fake.calls, err)
	}
	// Rows exist but no qualifying group (single session): trigger consumed,
	// still zero calls.
	seedLedger(t, sy.Base, "phin", "tests", []string{"s1", "s1", "s1"})
	res, err := sy.Run(ctx, true, now)
	if err != nil || res.Ran || fake.calls != 0 {
		t.Fatalf("unqualified: %+v calls=%d err=%v", res, fake.calls, err)
	}
	// Re-running without new rows: not due (consumed), even after the gate.
	if res, err := sy.Run(ctx, false, now.Add(30*24*time.Hour)); err != nil || res.Ran {
		t.Fatalf("consumed rows re-fired: %+v err=%v", res, err)
	}
}

func TestSynthesisRejectsGlobalScopeAndContradiction(t *testing.T) {
	payload := `{"cards":[{"slug":"bad","dimension":"x","title":"T","summary":"S","markdown":"body","keywords":[],"scope":"global","confidence":0.9}],"contradicted":["styles/old-pref"],"notes":""}`
	fake := &fakeGen{payload: payload}
	sy := newSynth(t, fake)
	ctx := context.Background()
	kdir := config.KnowledgeDir(sy.Base)

	// Seed a culi-authored style card that the synthesis contradicts.
	old := knowledge.Card{
		Type: "style", Title: "Old preference", Summary: "s", Body: "b",
		Scopes: []string{"repo:phin"}, Status: "confirmed",
		Provenance: &knowledge.Provenance{Source: "learn"},
	}
	raw, _ := knowledge.Render(old)
	if err := os.WriteFile(filepath.Join(kdir, "styles", "old-pref.md"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Sync(ctx, sy.Store, kdir); err != nil {
		t.Fatal(err)
	}

	seedLedger(t, sy.Base, "phin", "tests", []string{"s1", "s2", "s3"})
	res, err := sy.Run(ctx, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 {
		t.Errorf("global-scoped card was written: %v", res.Created)
	}
	if len(res.Retired) != 1 || res.Retired[0] != "styles/old-pref" {
		t.Errorf("retired = %v", res.Retired)
	}
	got, _ := knowledge.ReadCard(kdir, "styles/old-pref.md")
	if got.Status != "retired" {
		t.Errorf("contradicted card status = %q", got.Status)
	}
}

func TestSynthesisNeverClobbersForeignCard(t *testing.T) {
	fake := &fakeGen{payload: synthPayload}
	sy := newSynth(t, fake)
	kdir := config.KnowledgeDir(sy.Base)
	hand := "---\ntitle: My table tests note\n---\n\nhand content\n"
	if err := os.WriteFile(filepath.Join(kdir, "styles", "table-tests.md"), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, sy.Base, "phin", "tests", []string{"s1", "s2", "s3"})

	res, err := sy.Run(context.Background(), true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created)+len(res.Reinforced) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(kdir, "styles", "table-tests.md"))
	if string(raw) != hand {
		t.Error("hand-authored style card clobbered (C4)")
	}
}

func TestLedgerTailCap(t *testing.T) {
	base := t.TempDir()
	sessions := make([]string, 0, maxLedgerRows+50)
	for i := 0; i < maxLedgerRows+50; i++ {
		sessions = append(sessions, "s")
	}
	seedLedger(t, base, "r", "d", sessions)
	rows, total, err := loadLedger(config.StateDir(base))
	if err != nil {
		t.Fatal(err)
	}
	if total != maxLedgerRows+50 || len(rows) != maxLedgerRows {
		t.Errorf("total=%d rows=%d", total, len(rows))
	}
	if !strings.Contains(rows[0].Observation, "table tests") {
		t.Errorf("row = %+v", rows[0])
	}
}
