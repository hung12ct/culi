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

// injectedIn resolves the session's creditable cards the way attributeSession
// does, so the tests exercise the same selection.
func injectedIn(t *testing.T, s *store.Store, session string) []string {
	t.Helper()
	ids, err := s.SessionContentCards(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	return ids
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

	n, err := m.attributeUsage(ctx, injectedIn(t, s, "s1"), usingCard("I will not tag a release without the matching changelog entry first"))
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

	n, err := m.attributeUsage(context.Background(), injectedIn(t, s, "s1"),
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

	n, err := m.attributeUsage(context.Background(), injectedIn(t, s, "s1"),
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

	n, err := m.attributeUsage(context.Background(), injectedIn(t, s, "other-session"),
		usingCard("I will not tag a release without the matching changelog entry first"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("credited %d cards from a foreign session, want 0", n)
	}
}

// The defect this pins: a job labelled "session-end" is not once-per-session.
// The Codex rollout scanner stamps that trigger on every rescan of a growing
// rollout (cli/learn.go), and a resumed Claude session reaches SessionEnd
// twice. Since credit is additive, only a persisted claim can stop the second
// pass re-crediting the whole session from offset 0.
func TestClaimAttributionIsOncePerSession(t *testing.T) {
	s := attribStore(t, store.GranBody)
	ctx := context.Background()

	first, err := s.ClaimAttribution(ctx, "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Fatal("first claim should win")
	}
	for i := range 3 {
		again, err := s.ClaimAttribution(ctx, "s1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if again {
			t.Fatalf("claim %d re-won; session would be credited twice", i+2)
		}
	}
	// A different session is unaffected.
	other, err := s.ClaimAttribution(ctx, "s2", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !other {
		t.Error("a distinct session should still be claimable")
	}
}

// The claim must not disturb the gate's novelty state living in the same row.
func TestClaimAttributionPreservesLastPrompt(t *testing.T) {
	s := attribStore(t, store.GranBody)
	ctx := context.Background()

	if _, err := s.SwapLastPrompt(ctx, "s1", "the original prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAttribution(ctx, "s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	prev, err := s.SwapLastPrompt(ctx, "s1", "next prompt")
	if err != nil {
		t.Fatal(err)
	}
	if prev != "the original prompt" {
		t.Errorf("last_prompt = %q, want %q — claim clobbered gate state", prev, "the original prompt")
	}
}
