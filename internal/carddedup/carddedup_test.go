package carddedup

import (
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

func card(id, typ, status, title, summary string) store.StoredCard {
	return store.StoredCard{Card: knowledge.Card{
		ID: id, Type: typ, Status: status, Title: title, Summary: summary,
	}}
}

func TestClassifyBands(t *testing.T) {
	for _, tc := range []struct {
		name string
		cos  float64
		want Verdict
	}{
		{"far apart", 0.10, New},
		{"just under near", CosNear - 0.01, New},
		{"at near", CosNear, Near},
		{"just under dup", CosDup - 0.01, Near},
		{"at dup", CosDup, Dup},
		{"identical", 1.0, Dup},
	} {
		if got := Classify(tc.cos); got != tc.want {
			t.Errorf("%s: Classify(%v) = %v, want %v", tc.name, tc.cos, got, tc.want)
		}
	}
	if got := ClassifyLexical(JacDup); got != Dup {
		t.Errorf("ClassifyLexical(JacDup) = %v", got)
	}
	if got := ClassifyLexical(JacNear - 0.01); got != New {
		t.Errorf("ClassifyLexical below near = %v", got)
	}
}

// The pair that motivated this package: two imports of the same repo produced
// byte-identical summaries under different slugs. The lexical arm alone must
// catch it, since an import can run with Ollama down.
func TestRealWorldTwinIsLexicallyADuplicate(t *testing.T) {
	const summary = "Max 50 lines changed per bugfix; scope larger refactors as separate commits."
	existing := []store.StoredCard{
		card("rules/gopheragent-line-budget", "rule", "", "gopheragent bugfix line budget", summary),
		card("rules/unrelated", "rule", "", "Use table-driven tests", "Prefer table tests for Go units."),
	}
	best, sim := BestLexical("50-line budget per bugfix commit", summary, existing)
	if v := ClassifyLexical(sim); v != Dup {
		t.Fatalf("verdict = %v (jaccard %.2f), want Dup", v, sim)
	}
	if best.ID != "rules/gopheragent-line-budget" {
		t.Errorf("matched %s", best.ID)
	}
}

func TestBestLexicalPicksTheClosest(t *testing.T) {
	existing := []store.StoredCard{
		card("a", "rule", "", "Totally different", "About deployment pipelines."),
		card("b", "rule", "", "Use table tests", "Prefer table tests for Go units."),
	}
	best, sim := BestLexical("Use table tests", "Prefer table tests for Go units.", existing)
	if best.ID != "b" || sim < JacDup {
		t.Errorf("best = %s sim = %.2f", best.ID, sim)
	}
}

func TestBestVectorReportsWhenNothingHasAVector(t *testing.T) {
	existing := []store.StoredCard{card("a", "rule", "", "x", "y")}
	if _, _, ok := BestVector([]float32{1, 0}, existing, map[int64][]float32{}); ok {
		t.Fatal("ok = true with no vectors; caller would read New into the silence")
	}
	// A card that does have a vector flips ok, and unvectored peers are skipped
	// rather than scored as distant.
	existing[0].Rowid = 7
	best, sim, ok := BestVector([]float32{1, 0}, existing, map[int64][]float32{7: {1, 0}})
	if !ok || best.ID != "a" || sim < 0.99 {
		t.Errorf("ok=%v best=%s sim=%.3f", ok, best.ID, sim)
	}
}

func TestEligibleExcludesRetiredButKeepsCandidates(t *testing.T) {
	cards := []store.StoredCard{
		card("live", "rule", "", "a", "a"),
		card("queued", "rule", "candidate", "b", "b"),
		card("dead", "rule", "retired", "c", "c"),
		card("other-type", "lesson", "", "d", "d"),
	}
	got := Eligible(cards, "rule")
	if len(got) != 2 {
		t.Fatalf("got %d cards: %+v", len(got), got)
	}
	for _, sc := range got {
		if sc.ID == "dead" {
			t.Error("retired card would swallow its successor")
		}
	}
}
