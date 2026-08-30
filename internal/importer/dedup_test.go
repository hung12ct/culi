package importer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hung12ct/culi/internal/carddedup"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// stubDeduper answers by card title so a test can aim a verdict at one card.
type stubDeduper struct{ verdict map[string]carddedup.Verdict }

func (d stubDeduper) Duplicate(_ context.Context, _, title, _ string) (string, carddedup.Verdict, error) {
	if v, ok := d.verdict[title]; ok {
		return "rules/existing-twin", v, nil
	}
	return "", carddedup.New, nil
}

// A Dup never reaches staging; a Near does, because the importer's input is
// guidance the user wrote and dropping it would lose content the miner could
// simply re-derive.
func TestMergeDropsDuplicatesAndKeepsNearMatches(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	d := stubDeduper{verdict: map[string]carddedup.Verdict{
		"Use table tests": carddedup.Dup,
		"Scope escape":    carddedup.Near,
	}}
	res, err := Merge(context.Background(), kdir, rep, fakeMerger{}, MergeOpts{Dedup: d})
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(kdir, ".import", "staged")
	if _, err := knowledge.ReadCard(staged, "rules/table-tests.md"); err == nil {
		t.Error("duplicate card was staged")
	}
	if _, err := knowledge.ReadCard(staged, "rules/evil-scope.md"); err != nil {
		t.Errorf("near-duplicate must still stage (C4): %v", err)
	}
	if !hasSubstring(res.Skipped, "Use table tests") {
		t.Errorf("skipped = %v", res.Skipped)
	}
	if !hasSubstring(res.Notes, "duplicates rules/existing-twin") {
		t.Errorf("notes = %v", res.Notes)
	}
	if !hasSubstring(res.Notes, "resembles rules/existing-twin") {
		t.Errorf("near match unreported: %v", res.Notes)
	}
}

// A nil Deduper is the pre-v0.7 path and must stage everything.
func TestMergeWithoutDeduperStagesEverything(t *testing.T) {
	rep, err := Scan(fixtureRepos(t))
	if err != nil {
		t.Fatal(err)
	}
	kdir := t.TempDir()
	res, err := Merge(context.Background(), kdir, rep, fakeMerger{}, MergeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped without a deduper: %v", res.Skipped)
	}
	if _, err := knowledge.ReadCard(filepath.Join(kdir, ".import", "staged"), "rules/table-tests.md"); err != nil {
		t.Errorf("card missing: %v", err)
	}
}

func newTestStore(t *testing.T, cards ...knowledge.Card) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, c := range cards {
		if err := s.UpsertCard(ctx, c, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// The regression: a second import over the same repo re-slugged a rule it had
// already imported. Same summary, different title and path — the byte-equal
// check in Apply cannot see it, so the store check must.
func TestStoreDeduperCatchesReimportedTwin(t *testing.T) {
	const summary = "Max 50 lines changed per bugfix; scope larger refactors as separate commits."
	s := newTestStore(t, knowledge.Card{
		ID: "rules/gopheragent-line-budget", Path: "rules/gopheragent-line-budget.md",
		Type: "rule", Title: "gopheragent bugfix line budget", Summary: summary,
		Scopes: []string{"repo:gopheragent"},
	})
	d, err := NewStoreDeduper(context.Background(), s, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	id, verdict, err := d.Duplicate(context.Background(), "rule", "50-line budget per bugfix commit", summary)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != carddedup.Dup || id != "rules/gopheragent-line-budget" {
		t.Fatalf("verdict = %v, id = %q; want Dup on the existing card", verdict, id)
	}
}

// Twins can also form inside one run, before either card has been indexed or
// embedded — two repos' CLAUDE.md files saying the same thing. The run-local
// corpus is lexical-only by construction, so this is the arm that catches it.
func TestStoreDeduperCatchesWithinRunTwin(t *testing.T) {
	ctx := context.Background()
	d, err := NewStoreDeduper(ctx, newTestStore(t), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	const title, summary = "Never commit secrets", "Keep credentials out of the repo; use env vars."
	if _, v, _ := d.Duplicate(ctx, "rule", title, summary); v != carddedup.New {
		t.Fatalf("first sighting = %v, want New", v)
	}
	id, v, err := d.Duplicate(ctx, "rule", "Do not commit secrets", summary)
	if err != nil {
		t.Fatal(err)
	}
	if v != carddedup.Dup {
		t.Fatalf("second sighting = %v, want Dup", v)
	}
	if !strings.HasPrefix(id, StagedPrefix) {
		t.Errorf("id = %q, want the staged-this-run marker so the user can tell it apart", id)
	}
}

// A card ruled Dup must not join the run-local corpus, or one twin would
// shadow later cards that only resemble it.
func TestStoreDeduperDoesNotRememberDuplicates(t *testing.T) {
	ctx := context.Background()
	d, err := NewStoreDeduper(ctx, newTestStore(t), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	const summary = "Keep credentials out of the repo; use env vars."
	d.Duplicate(ctx, "rule", "Never commit secrets", summary)
	d.Duplicate(ctx, "rule", "Do not commit secrets", summary)
	if got := len(d.seen); got != 1 {
		t.Errorf("run-local corpus holds %d cards, want 1", got)
	}
}

func TestDedupSkipFailsOpen(t *testing.T) {
	skip, note := dedupSkip(context.Background(), nil, "rule", "x", "y")
	if skip || note != "" {
		t.Errorf("nil deduper: skip=%v note=%q", skip, note)
	}
	skip, note = dedupSkip(context.Background(), errDeduper{}, "rule", "Some card", "y")
	if skip {
		t.Error("a failed dedup check must not drop imported guidance")
	}
	if !strings.Contains(note, "staged unchecked") {
		t.Errorf("note = %q", note)
	}
}

type errDeduper struct{}

func (errDeduper) Duplicate(context.Context, string, string, string) (string, carddedup.Verdict, error) {
	return "", carddedup.New, context.DeadlineExceeded
}

func hasSubstring(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
