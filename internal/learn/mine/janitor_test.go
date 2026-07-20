package mine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hung12ct/culi/internal/config"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// seedCard writes a card, backdates its mtime by ageDays, and indexes it.
func seedCard(t *testing.T, kdir, rel string, c knowledge.Card, ageDays int) {
	t.Helper()
	raw, err := knowledge.Render(c)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(kdir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
	if err := os.Chtimes(abs, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestRetireStaleCandidates(t *testing.T) {
	base := t.TempDir()
	kdir := config.KnowledgeDir(base)
	ctx := context.Background()
	s, err := store.Open(ctx, config.DBPath(base))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	learned := func() *knowledge.Provenance { return &knowledge.Provenance{Source: "learn"} }

	// Old unreinforced candidate — should retire.
	seedCard(t, kdir, "lessons/stale-old.md", knowledge.Card{
		Type: "lesson", Title: "Stale old candidate", Summary: "x", Body: "b",
		Scopes: []string{"lang:go"}, Status: "candidate", Provenance: learned(),
	}, 45)
	// Fresh candidate — within TTL, must survive.
	seedCard(t, kdir, "lessons/fresh.md", knowledge.Card{
		Type: "lesson", Title: "Fresh candidate", Summary: "x", Body: "b",
		Scopes: []string{"lang:go"}, Status: "candidate", Provenance: learned(),
	}, 3)
	// Old but CONFIRMED — never auto-retired.
	seedCard(t, kdir, "lessons/old-confirmed.md", knowledge.Card{
		Type: "lesson", Title: "Old confirmed", Summary: "x", Body: "b",
		Scopes: []string{"lang:go"}, Status: "confirmed", Provenance: learned(),
	}, 200)
	// Old candidate but HAND-AUTHORED (no culi provenance) — never touched.
	seedCard(t, kdir, "lessons/hand.md", knowledge.Card{
		Type: "lesson", Title: "Hand authored", Summary: "x", Body: "b",
		Scopes: []string{"lang:go"}, Status: "candidate",
	}, 90)

	if _, err := indexer.Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}

	ttl := 30 * 24 * time.Hour
	retired, err := RetireStaleCandidates(ctx, s, kdir, ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 1 || retired[0] != "lessons/stale-old" {
		t.Fatalf("retired = %v, want [lessons/stale-old]", retired)
	}

	// The stale one is flipped on disk; the others keep their status.
	assertStatus(t, kdir, "lessons/stale-old.md", "retired")
	assertStatus(t, kdir, "lessons/fresh.md", "candidate")
	assertStatus(t, kdir, "lessons/old-confirmed.md", "confirmed")
	assertStatus(t, kdir, "lessons/hand.md", "candidate")

	// Disabled TTL is a no-op.
	if got, _ := RetireStaleCandidates(ctx, s, kdir, 0, time.Now()); got != nil {
		t.Errorf("ttl<=0 should retire nothing, got %v", got)
	}
}

func assertStatus(t *testing.T, kdir, rel, want string) {
	t.Helper()
	c, err := knowledge.ReadCard(kdir, rel)
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	if c.Status != want {
		t.Errorf("%s status = %q, want %q", rel, c.Status, want)
	}
}
