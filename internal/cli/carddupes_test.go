package cli

import (
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

func liveCard(rowid int64, id, typ, title, summary string) store.StoredCard {
	return store.StoredCard{
		Card:  knowledge.Card{ID: id, Type: typ, Title: title, Summary: summary},
		Rowid: rowid,
	}
}

// The real pair from the store that motivated this: identical summaries,
// different slugs. The keeper is the one that has actually been injected.
func TestFindDupePairsKeepsTheCardWithATrackRecord(t *testing.T) {
	const summary = "Max 50 lines changed per bugfix; scope larger refactors as separate commits."
	live := []store.StoredCard{
		liveCard(1, "rules/gopheragent-line-budget-rule", "rule", "50-line budget per bugfix commit", summary),
		liveCard(2, "rules/gopheragent-line-budget", "rule", "gopheragent bugfix line budget", summary),
		liveCard(3, "rules/unrelated", "rule", "Use table tests", "Prefer table tests for Go units."),
	}
	rank := map[string]float64{"rules/gopheragent-line-budget-rule": 4}
	dups, _ := findDupePairs(live, nil, rank)
	if len(dups) != 1 {
		t.Fatalf("got %d pairs, want 1: %+v", len(dups), dups)
	}
	if dups[0].keep.ID != "rules/gopheragent-line-budget-rule" {
		t.Errorf("kept %s, want the injected card", dups[0].keep.ID)
	}
	if dups[0].drop.ID != "rules/gopheragent-line-budget" {
		t.Errorf("dropped %s", dups[0].drop.ID)
	}
}

// Different types never pair, however similar the prose: a rule and a pattern
// saying the same thing play different roles in retrieval.
func TestFindDupePairsIgnoresCrossTypeMatches(t *testing.T) {
	const s = "Keep credentials out of the repo; use env vars."
	live := []store.StoredCard{
		liveCard(1, "rules/secrets", "rule", "Never commit secrets", s),
		liveCard(2, "patterns/secrets", "pattern", "Never commit secrets", s),
	}
	if dups, _ := findDupePairs(live, nil, nil); len(dups) != 0 {
		t.Errorf("paired across types: %+v", dups)
	}
}

// Three copies of one rule must collapse to one keeper and two drops, not
// cascade into a chain where the survivor is itself dropped by a later pair.
func TestFindDupePairsCollapsesAClusterToOneKeeper(t *testing.T) {
	const s = "Never declare work done without running the full test suite."
	live := []store.StoredCard{
		liveCard(1, "rules/verify-a", "rule", "Verify before completion", s),
		liveCard(2, "rules/verify-b", "rule", "Verification gates", s),
		liveCard(3, "rules/verify-c", "rule", "Verify gates always", s),
	}
	rank := map[string]float64{"rules/verify-a": 10}
	dups, _ := findDupePairs(live, nil, rank)
	if len(dups) != 2 {
		t.Fatalf("got %d pairs, want 2: %+v", len(dups), dups)
	}
	dropped := map[string]bool{}
	for _, p := range dups {
		if p.keep.ID != "rules/verify-a" {
			t.Errorf("keeper = %s, want the ranked card in every pair", p.keep.ID)
		}
		dropped[p.drop.ID] = true
	}
	if dropped["rules/verify-a"] {
		t.Error("the keeper was also dropped — retiring would leave the rule with no live copy")
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %v", dropped)
	}
}

// Orientation must not depend on map iteration order, or a dry run and the
// --retire run that follows could disagree about which twin dies.
func TestOrientIsDeterministicOnTies(t *testing.T) {
	a := liveCard(1, "rules/aaa", "rule", "t", "s")
	b := liveCard(2, "rules/aaa-extended", "rule", "t", "s")
	for i := 0; i < 50; i++ {
		keep, drop := orient(a, b, map[string]float64{})
		if keep.ID != "rules/aaa" || drop.ID != "rules/aaa-extended" {
			t.Fatalf("iteration %d: keep=%s drop=%s", i, keep.ID, drop.ID)
		}
		if k2, _ := orient(b, a, map[string]float64{}); k2.ID != keep.ID {
			t.Fatalf("argument order changed the keeper: %s", k2.ID)
		}
	}
}

// A near match is reported but never queued for retirement — that band is for
// human review, and dropping it automatically would lose real guidance.
//
// Driven through the vector arm on purpose: the pairs that actually land here
// on the real corpus (0.93-0.96 cosine, 0.48-0.55 Jaccard) are rephrasings
// that share meaning without sharing terms, so the lexical arm calls them New.
func TestNearMatchesAreReportedNotRetired(t *testing.T) {
	live := []store.StoredCard{
		liveCard(1, "rules/uv-only", "rule", "uv only for python tooling", "Use uv to run and install python tools."),
		liveCard(2, "rules/uv-wrapper", "rule", "uv tool wrapper required", "Always wrap python tool invocations with uv run."),
	}
	// cos = 0.85: inside the Near band, short of Dup.
	vecs := map[int64][]float32{1: {1, 0}, 2: {0.85, 0.5268}}
	dups, nears := findDupePairs(live, vecs, nil)
	if len(dups) != 0 {
		t.Errorf("near match queued for retirement: %+v", dups)
	}
	if len(nears) != 1 {
		t.Fatalf("near match unreported: %+v", nears)
	}
	if nears[0].arm != "cosine" {
		t.Errorf("arm = %s; the lexical arm cannot see this pair", nears[0].arm)
	}
}

// The vector arm must be able to promote a pair the lexical arm calls New —
// that asymmetry is the whole reason both run.
func TestCosineCatchesWhatJaccardMisses(t *testing.T) {
	live := []store.StoredCard{
		liveCard(1, "rules/no-orm", "rule", "No migration frameworks", "Write manual SQL for schema changes."),
		liveCard(2, "rules/manual-sql", "rule", "Hand-rolled schema edits", "Avoid Alembic; hand the DDL over yourself."),
	}
	if dups, nears := findDupePairs(live, nil, nil); len(dups)+len(nears) != 0 {
		t.Fatalf("lexical arm should see nothing here: dups=%+v nears=%+v", dups, nears)
	}
	vecs := map[int64][]float32{1: {1, 0}, 2: {0.97, 0.2431}}
	dups, _ := findDupePairs(live, vecs, nil)
	if len(dups) != 1 || dups[0].arm != "cosine" {
		t.Fatalf("dups = %+v", dups)
	}
}

// Identical prose at different scopes is not a duplicate to retire: dropping
// the global copy would leave every repo but one without the rule.
func TestScopeMismatchIsDemotedToReview(t *testing.T) {
	const sum = "Never declare work done without running the full test suite."
	global := liveCard(1, "rules/verify-global", "rule", "Verify before completion", sum)
	global.Scopes = []string{"global"}
	scoped := liveCard(2, "rules/verify-cutpilot", "rule", "Verify before completion", sum)
	scoped.Scopes = []string{"repo:cutpilot"}

	dups, nears := findDupePairs([]store.StoredCard{global, scoped}, nil, nil)
	if len(dups) != 0 {
		t.Fatalf("cross-scope pair queued for retirement: %+v", dups)
	}
	if len(nears) != 1 {
		t.Fatalf("cross-scope pair unreported: %+v", nears)
	}

	// Same scope on both sides and it is a duplicate again.
	scoped.Scopes = []string{"global"}
	if dups, _ := findDupePairs([]store.StoredCard{global, scoped}, nil, nil); len(dups) != 1 {
		t.Errorf("same-scope twins not paired: %+v", dups)
	}
}

func TestSameScopeIgnoresOrdering(t *testing.T) {
	a := liveCard(1, "a", "rule", "t", "s")
	a.Scopes = []string{"lang:go", "global"}
	b := liveCard(2, "b", "rule", "t", "s")
	b.Scopes = []string{"global", "lang:go"}
	if !sameScope(a, b) {
		t.Error("scope order changed the verdict")
	}
	b.Scopes = []string{"global"}
	if sameScope(a, b) {
		t.Error("a narrower card matched a wider one")
	}
}
