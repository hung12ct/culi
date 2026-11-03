package evalrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/store"
)

// TestFixtureSuite is the retrieval regression guard: the committed fixture
// store and suite, keyword-only (no Ollama in CI). Floors sit just under the
// measured baseline (hit 29/32, abstain 6/6 on 2026-11-03), so a ranking or
// gating change that loses a case fails here instead of shipping silently.
func TestFixtureSuite(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := indexer.Full(ctx, s, filepath.Join("..", "..", "eval", "fixture", "knowledge")); err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(filepath.Join("..", "..", "eval", "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: s, Budget: 700, DefaultCWD: t.TempDir()}
	rep, err := r.Run(ctx, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range rep.Results {
		if !res.Pass {
			t.Logf("miss %s: want %v got %v (gate %q)", res.ID, res.Expect, res.Injected, res.GateSkip)
		}
	}
	if rep.HitRate < 0.90 || rep.MRR < 0.85 || rep.AbstainAccuracy < 1 {
		t.Fatalf("retrieval regressed: hit %.2f (floor 0.90), MRR %.2f (floor 0.85), abstain %.2f (floor 1.00)",
			rep.HitRate, rep.MRR, rep.AbstainAccuracy)
	}
}

func TestScore(t *testing.T) {
	for _, tc := range []struct {
		name         string
		expect, got  []string
		pass         bool
		rank         int
		recall, prec float64
	}{
		{"first hit at rank 2", []string{"a", "b"}, []string{"x", "a"}, true, 2, 0.5, 0.5},
		{"miss", []string{"a"}, []string{"x"}, false, 0, 0, 0},
		{"nothing injected", []string{"a"}, nil, false, 0, 0, 0},
		{"abstain ok", nil, nil, true, 0, 0, 0},
		{"abstain violated", nil, []string{"x"}, false, 0, 0, 0},
	} {
		res := Result{Case: Case{Expect: tc.expect}, Injected: tc.got}
		score(&res)
		if res.Pass != tc.pass || res.Rank != tc.rank || res.Recall != tc.recall || res.Precision != tc.prec {
			t.Errorf("%s: got %+v", tc.name, res)
		}
	}
}

func TestSummarize(t *testing.T) {
	rep := Report{Results: []Result{
		{Case: Case{Expect: []string{"a"}}, Pass: true, Rank: 1, Recall: 1, Precision: 1, Tokens: 100},
		{Case: Case{Expect: []string{"b"}}, Pass: true, Rank: 2, Recall: 1, Precision: 0.5, Tokens: 200},
		{Case: Case{Expect: []string{"c"}}},
		{Case: Case{}, Pass: true},
	}}
	summarize(&rep)
	if rep.Positive != 3 || rep.Abstain != 1 || rep.AbstainAccuracy != 1 {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.HitRate < 0.66 || rep.HitRate > 0.67 || rep.MRR != 0.5 || rep.MeanTokens != 75 {
		t.Fatalf("rates: hit %f MRR %f tokens %f", rep.HitRate, rep.MRR, rep.MeanTokens)
	}
}

func TestLoadSuiteRejectsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	writeFile(t, path, "name: x\ncases:\n  - {id: a, prompt: p, expect: []}\n  - {id: a, prompt: q, expect: []}\n")
	if _, err := LoadSuite(path); err == nil {
		t.Fatal("duplicate ids accepted")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
