package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/knowledge"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testCard(id string) knowledge.Card {
	return knowledge.Card{
		ID: id, Path: id + ".md", Type: "rule", Title: "t " + id, Summary: "s",
		Body: "b", Scopes: []string{"global"}, ContentHash: "hash-" + id,
		TokSummary: 5, TokBody: 5,
	}
}

func TestEmbeddingsFreshnessAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertCard(ctx, testCard("rules/a"), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCard(ctx, testCard("rules/b"), 1, 1); err != nil {
		t.Fatal(err)
	}
	cards, _ := s.AllCards(ctx)
	byID := map[string]int64{}
	for _, c := range cards {
		byID[c.ID] = c.Rowid
	}

	// Both need embedding initially.
	missing, err := s.MissingEmbeddings(ctx, "m1")
	if err != nil || len(missing) != 2 {
		t.Fatalf("missing = %d (%v), want 2", len(missing), err)
	}

	if err := s.UpsertEmbedding(ctx, byID["rules/a"], "m1", []float32{1, 0}, "hash-rules/a"); err != nil {
		t.Fatal(err)
	}
	// Stale hash: vector exists but for older content.
	if err := s.UpsertEmbedding(ctx, byID["rules/b"], "m1", []float32{0, 1}, "old-hash"); err != nil {
		t.Fatal(err)
	}

	vecs, err := s.Embeddings(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 {
		t.Fatalf("fresh vectors = %d, want 1 (stale hash excluded)", len(vecs))
	}
	if v := vecs[byID["rules/a"]]; len(v) != 2 || v[0] != 1 {
		t.Errorf("vector round-trip broken: %v", v)
	}

	missing, err = s.MissingEmbeddings(ctx, "m1")
	if err != nil || len(missing) != 1 || missing[0].Rowid != byID["rules/b"] {
		t.Fatalf("missing after upsert = %+v (%v), want only stale rules/b", missing, err)
	}
	// Different model: everything is missing again.
	missing, _ = s.MissingEmbeddings(ctx, "m2")
	if len(missing) != 2 {
		t.Fatalf("missing for other model = %d, want 2", len(missing))
	}
}

func TestMeta(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if v, err := s.MetaGet(ctx, "nope"); err != nil || v != "" {
		t.Fatalf("absent key = %q, %v", v, err)
	}
	if err := s.MetaSet(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MetaSet(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.MetaGet(ctx, "k"); v != "v2" {
		t.Fatalf("meta = %q, want v2", v)
	}
}

func TestFeedbackAndUtility(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// No stats: map is empty (callers default to 1.0).
	m, err := s.UtilityMultipliers(ctx)
	if err != nil || len(m) != 0 {
		t.Fatalf("empty stats = %v (%v)", m, err)
	}

	if err := s.AddFeedback(ctx, "c1", FeedbackExpanded, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFeedback(ctx, "c2", FeedbackDownvoted, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFeedback(ctx, "c1", FeedbackInjected, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFeedback(ctx, "bad", "nope", 1); err == nil {
		t.Fatal("unknown kind must error")
	}

	m, err = s.UtilityMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// c1: pos=3, neg=0 → 2*4/5 = 1.6 → clamped 1.5.
	if math.Abs(m["c1"]-1.5) > 1e-9 {
		t.Errorf("c1 multiplier = %v, want 1.5 (clamped)", m["c1"])
	}
	// c2: pos=0, neg=5 → 2*1/7 ≈ 0.286 → clamped 0.5.
	if math.Abs(m["c2"]-0.5) > 1e-9 {
		t.Errorf("c2 multiplier = %v, want 0.5 (clamped)", m["c2"])
	}
}

func TestDecayFactor(t *testing.T) {
	now := time.Now().UTC()
	if f := decayFactor(now.Add(-utilityHalfLife), now); math.Abs(f-0.5) > 1e-9 {
		t.Errorf("one half-life = %v, want 0.5", f)
	}
	if f := decayFactor(time.Time{}, now); f != 1 {
		t.Errorf("zero time = %v, want 1", f)
	}
	if f := decayFactor(now.Add(time.Hour), now); f != 1 {
		t.Errorf("future time = %v, want 1", f)
	}
}

func TestPenalizeAbandonedPointers(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	recs := []InjectionRecord{
		{CardID: "seen", Granularity: GranHook, Tokens: 10},
		{CardID: "expanded", Granularity: GranHook, Tokens: 10},
	}
	if err := s.RecordInjections(ctx, "sess", "user-prompt-submit", "h", recs); err != nil {
		t.Fatal(err)
	}
	// "expanded" was later pulled at body granularity.
	if err := s.RecordInjections(ctx, "sess", "mcp", "", []InjectionRecord{
		{CardID: "expanded", Granularity: GranBody, Tokens: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PenalizeAbandonedPointers(ctx, "sess"); err != nil {
		t.Fatal(err)
	}
	m, err := s.UtilityMultipliers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// seen: neg = 5*0.1 = 0.5 → 2*1/2.5 = 0.8.
	if math.Abs(m["seen"]-0.8) > 1e-6 {
		t.Errorf("abandoned pointer multiplier = %v, want 0.8", m["seen"])
	}
	if _, ok := m["expanded"]; ok {
		t.Error("expanded card must not be penalized")
	}
}
