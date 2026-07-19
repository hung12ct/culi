package retrieve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/store"
)

// fakeEmb returns a fixed vector per keyword bucket: texts mentioning "auth"
// embed identically, everything else lands orthogonal.
type fakeEmb struct {
	calls int
	fail  bool
}

func (f *fakeEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("ollama down")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if strings.Contains(strings.ToLower(t), "auth") || strings.Contains(strings.ToLower(t), "sign in") {
			out[i] = []float32{1, 0}
		} else {
			out[i] = []float32{0, 1}
		}
	}
	return out, nil
}

// embedAll runs the fake embedder over every indexed card and stores fresh
// vectors, mimicking indexer.EmbedMissing.
func embedAll(t *testing.T, s *store.Store, f *fakeEmb, model string) {
	t.Helper()
	ctx := context.Background()
	cards, err := s.AllCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		vecs, err := f.Embed(ctx, []string{c.Title + "\n" + c.Summary})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertEmbedding(ctx, c.Rowid, model, vecs[0], c.ContentHash); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHybridSemanticOnlyRecall(t *testing.T) {
	// No lexical overlap between query and card — BM25 misses, cosine ≥0.60
	// carries it over the floor.
	authCard := mkCard("rules/auth-flow", "Auth flow rules", "authorize users before granting tokens")
	dbCard := mkCard("rules/db-conn", "Database pooling", "reuse connections via a pool")
	s := testStore(t, authCard, dbCard)
	f := &fakeEmb{}
	embedAll(t, s, f, "m")

	r := &Retriever{Store: s, Embedder: f, Model: "m"}
	got, err := r.Retrieve(context.Background(), "how do users sign in safely", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Card.ID != "rules/auth-flow" {
		t.Fatalf("semantic-only recall failed: %+v", got)
	}
	if got[0].Vec == nil {
		t.Error("candidate should carry its vector for MMR")
	}
}

func TestHybridFusionBoostsAgreement(t *testing.T) {
	// Both cards match lexically; only one is semantically close — fusion must
	// rank it first even if BM25 alone preferred the other.
	a := mkCard("rules/auth-err", "Error docs", "error handling guidance for auth handling")
	b := mkCard("rules/plain-err", "Error handling", "error handling guidance for error handling paths")
	s := testStore(t, a, b)
	f := &fakeEmb{}
	embedAll(t, s, f, "m")

	r := &Retriever{Store: s, Embedder: f, Model: "m"}
	got, err := r.Retrieve(context.Background(), "auth error handling guidance", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want both cards, got %+v", got)
	}
	if got[0].Card.ID != "rules/auth-err" {
		t.Fatalf("fusion should rank the two-arm card first: %+v", got)
	}
}

func TestHybridDegradesToBM25(t *testing.T) {
	card := mkCard("rules/go-err", "Wrap Go errors", "wrap errors with package prefix")
	s := testStore(t, card)
	f := &fakeEmb{fail: true}
	r := &Retriever{Store: s, Embedder: f, Model: "m"}

	got, err := r.Retrieve(context.Background(), "how should I wrap errors here", globalScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Card.ID != "rules/go-err" {
		t.Fatalf("BM25 degrade failed: %+v", got)
	}
}

func TestBreakerOpensAfterThreeFailures(t *testing.T) {
	card := mkCard("rules/go-err", "Wrap Go errors", "wrap errors with package prefix")
	s := testStore(t, card)
	f := &fakeEmb{fail: true}
	r := &Retriever{Store: s, Embedder: f, Model: "m"}
	ctx := context.Background()

	for range 3 {
		if _, err := r.Retrieve(ctx, "how should I wrap errors here", globalScope()); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 3 {
		t.Fatalf("embedder calls = %d, want 3", f.calls)
	}
	// Breaker is now open: the 4th retrieve must not touch the embedder.
	if _, err := r.Retrieve(ctx, "how should I wrap errors here", globalScope()); err != nil {
		t.Fatal(err)
	}
	if f.calls != 3 {
		t.Fatalf("embedder called through an open breaker (calls = %d)", f.calls)
	}
	if until, _ := s.MetaGet(ctx, "embed_break_until"); until == "" {
		t.Error("break_until not persisted")
	}

	// The breaker closes on its own once the cooldown elapses.
	if breakerOpen(ctx, s, time.Now().Add(breakerCooldown+time.Second)) {
		t.Error("breaker should be closed after the cooldown window")
	}

	// A success after cooldown resets the streak counter path.
	breakerRecord(ctx, s, true, time.Now())
	if count, _ := s.MetaGet(ctx, "embed_fail_count"); count != "0" && count != "" {
		t.Errorf("fail count after success = %q", count)
	}
}

func TestUtilityReordersButNeverBuries(t *testing.T) {
	a := mkCard("rules/err-a", "Error handling A", "wrap errors with package prefix")
	b := mkCard("rules/err-b", "Error handling B", "wrap errors with package prefix too")
	s := testStore(t, a, b)
	ctx := context.Background()

	r := &Retriever{Store: s}
	got, err := r.Retrieve(ctx, "how to wrap errors with package prefix", globalScope())
	if err != nil || len(got) != 2 {
		t.Fatalf("baseline: %+v (%v)", got, err)
	}
	winner := got[0].Card.ID

	// Heavy downvotes on the winner flip the order but keep it retrievable.
	for range 5 {
		if err := s.AddFeedback(ctx, winner, store.FeedbackDownvoted, 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err = r.Retrieve(ctx, "how to wrap errors with package prefix", globalScope())
	if err != nil || len(got) != 2 {
		t.Fatalf("after downvotes: %+v (%v)", got, err)
	}
	if got[0].Card.ID == winner {
		t.Error("downvoted card should have been demoted")
	}
	if got[1].Card.ID != winner {
		t.Error("downvoted card must remain retrievable (clamped multiplier, never buried)")
	}
}
