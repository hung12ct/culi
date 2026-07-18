package retrieve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

func testStore(t *testing.T, cards ...knowledge.Card) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i, c := range cards {
		if c.TokSummary == 0 {
			c.TokSummary = knowledge.EstimateTokens(c.Summary)
		}
		if c.TokBody == 0 {
			c.TokBody = knowledge.EstimateTokens(c.Body)
		}
		if c.ContentHash == "" {
			c.ContentHash = c.ID
		}
		if err := s.UpsertCard(context.Background(), c, int64(i), 100); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func mkCard(id, title, summary string, scopes ...string) knowledge.Card {
	if len(scopes) == 0 {
		scopes = []string{"global"}
	}
	return knowledge.Card{
		ID: id, Path: id + ".md", Type: "rule", Title: title,
		Summary: summary, Body: "body of " + title, Scopes: scopes,
	}
}

func globalScope() Scope {
	return Scope{Allowed: map[string]bool{"global": true}}
}

func TestRetrieveScopeFilter(t *testing.T) {
	goCard := mkCard("rules/go-err", "Wrap Go errors", "wrap errors with package prefix", "lang:go")
	jsCard := mkCard("rules/js-err", "JS error style", "wrap errors in Result types", "lang:javascript")
	s := testStore(t, goCard, jsCard)
	r := &Retriever{Store: s}

	sc := Scope{Repo: "x", Allowed: map[string]bool{"global": true, "lang:go": true, "repo:x": true}}
	got, err := r.Retrieve(context.Background(), "how should I wrap errors here?", sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Card.ID != "rules/go-err" {
		t.Fatalf("scope filter failed: %+v", got)
	}
}

func TestRetrieveKeyShadowing(t *testing.T) {
	global := mkCard("rules/commit-global", "Commit style", "conventional commits everywhere")
	global.Key = "commit-style"
	repo := mkCard("rules/commit-repo", "Repo commit style", "conventional commits with ticket prefix", "repo:x")
	repo.Key = "commit-style"
	s := testStore(t, global, repo)
	r := &Retriever{Store: s}

	sc := Scope{Repo: "x", Allowed: map[string]bool{"global": true, "repo:x": true}}
	got, err := r.Retrieve(context.Background(), "what is the commit style convention?", sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Card.ID != "rules/commit-repo" {
		t.Fatalf("shadowing failed: %+v", got)
	}

	// Outside repo:x, the global version is the one that surfaces.
	got, err = r.Retrieve(context.Background(), "what is the commit style convention?", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Card.ID != "rules/commit-global" {
		t.Fatalf("global fallback failed: %+v", got)
	}
}

func TestRetrieveTriggerPin(t *testing.T) {
	pinme := mkCard("rules/rebase", "Rebase rule", "never rebase shared branches")
	pinme.Triggers.Keywords = []string{"rebase"}
	other := mkCard("rules/other", "Other rule", "something about testing entirely")
	s := testStore(t, pinme, other)
	r := &Retriever{Store: s}

	got, err := r.Retrieve(context.Background(), "should I rebase my feature branch today?", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !got[0].Pinned || got[0].Card.ID != "rules/rebase" {
		t.Fatalf("trigger pin failed: %+v", got)
	}
}

func TestRetrieveScoreFloorInjectsNothing(t *testing.T) {
	c := mkCard("rules/deploy", "Deploy checklist", "run smoke tests before deploying to production")
	s := testStore(t, c)
	r := &Retriever{Store: s}

	// A prompt sharing zero meaningful terms with the corpus → empty result.
	got, err := r.Retrieve(context.Background(), "compose a haiku about autumn leaves", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("floor failed, injected: %+v", got)
	}
}

func TestRetrieveSkipsLifecycleStates(t *testing.T) {
	cand := mkCard("lessons/tentative", "Tentative lesson", "wrap errors carefully always")
	cand.Status = "candidate"
	ret := mkCard("lessons/old", "Retired lesson", "wrap errors carefully always")
	ret.Status = "retired"
	s := testStore(t, cand, ret)
	r := &Retriever{Store: s}
	got, err := r.Retrieve(context.Background(), "how do I wrap errors carefully?", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("lifecycle filter failed: %+v", got)
	}
}

func TestBaseline(t *testing.T) {
	base := mkCard("rules/base", "Project profile", "this repo is a Go CLI", "repo:x")
	base.Baseline = true
	nonBase := mkCard("rules/nb", "Not baseline", "irrelevant")
	s := testStore(t, base, nonBase)
	r := &Retriever{Store: s}
	sc := Scope{Repo: "x", Allowed: map[string]bool{"global": true, "repo:x": true}}
	got, err := r.Baseline(context.Background(), sc)
	if err != nil || len(got) != 1 || got[0].Card.ID != "rules/base" {
		t.Fatalf("baseline = %+v, %v", got, err)
	}
}

func TestDetectScope(t *testing.T) {
	// Fake repo: dir with .git/HEAD and go.mod.
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := DetectScope(sub)
	repo := filepath.Base(root)
	if sc.Repo != repo || sc.Branch != "main" {
		t.Fatalf("scope = %+v", sc)
	}
	for _, want := range []string{"global", "lang:go", "repo:" + repo, "branch:" + repo + "@main"} {
		if !sc.Allowed[want] {
			t.Errorf("missing allowed scope %q: %v", want, sc.Allowed)
		}
	}

	// Non-git dir: global + dir hash only.
	plain := t.TempDir()
	sc = DetectScope(plain)
	if sc.Repo != "" || len(sc.Allowed) != 2 {
		t.Fatalf("non-git scope = %+v", sc)
	}
}
