package mine

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/harness"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/learn/transcript"
	"github.com/hung12ct/culi/internal/store"
)

const cardGuidance = "never tag a release without the matching changelog entry"

// attribStore builds a store holding one card, injected at the given
// granularity in session "s1".
func attribStore(t *testing.T, granularity string) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	c := knowledge.Card{
		ID: "rules/changelog", Path: "rules/changelog.md", Type: "rule",
		Title: "Changelog on tag", Summary: "Tag discipline", Body: cardGuidance,
		Scopes: []string{"global"},
	}
	if err := s.UpsertCard(ctx, c, 1, 1); err != nil {
		t.Fatal(err)
	}
	err = s.RecordInjections(ctx, "s1", "user-prompt-submit", "h", "/tmp", harness.Claude,
		[]store.InjectionRecord{{CardID: "rules/changelog", Granularity: granularity, Tokens: 10}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func referencedCount(t *testing.T, s *store.Store, id string) float64 {
	t.Helper()
	stats, err := s.AllCardStats(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return stats[id].Referenced
}

func usingCard(text string) []transcript.Entry {
	return []transcript.Entry{
		{Role: "user", Text: "can you cut a release for me please"},
		{Role: "assistant", Text: text},
	}
}

func TestAttributeUsageWritesFeedback(t *testing.T) {
	s := attribStore(t, store.GranBody)
	m := &Miner{Store: s}
	ctx := context.Background()

	n, err := m.attributeUsage(ctx, "s1", usingCard("I will not tag a release without the matching changelog entry first"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("credited %d cards, want 1", n)
	}
	// AllCardStats applies time decay on read, so compare with tolerance.
	if got := referencedCount(t, s, "rules/changelog"); math.Abs(got-attributionCredit) > 1e-6 {
		t.Errorf("referenced = %v, want ~%v", got, attributionCredit)
	}
}

// A card whose content never reached the model — only a pointer line — must
// never be credited, or one teaser would earn what a full body earns.
func TestAttributeUsageIgnoresPointerOnlyCards(t *testing.T) {
	s := attribStore(t, store.GranHook)
	m := &Miner{Store: s}

	n, err := m.attributeUsage(context.Background(), "s1",
		usingCard("I will not tag a release without the matching changelog entry first"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("credited %d pointer-only cards, want 0", n)
	}
}

func TestAttributeUsageNoMatchNoCredit(t *testing.T) {
	s := attribStore(t, store.GranBody)
	m := &Miner{Store: s}

	n, err := m.attributeUsage(context.Background(), "s1",
		usingCard("sure, I'll cut the release now and push the tag"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("credited %d cards on topic overlap alone, want 0", n)
	}
	if got := referencedCount(t, s, "rules/changelog"); got != 0 {
		t.Errorf("referenced = %v, want 0", got)
	}
}

// Cards injected in another session must not be credited by this one.
func TestAttributeUsageScopedToSession(t *testing.T) {
	s := attribStore(t, store.GranBody)
	m := &Miner{Store: s}

	n, err := m.attributeUsage(context.Background(), "other-session",
		usingCard("I will not tag a release without the matching changelog entry first"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("credited %d cards from a foreign session, want 0", n)
	}
}
