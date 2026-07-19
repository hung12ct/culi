package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/store"
)

type fakeEmbedder struct {
	calls int
	fail  bool
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("ollama down")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), 1}
	}
	return out, nil
}

func writeTitledCard(t *testing.T, dir, rel, title string) {
	t.Helper()
	writeCard(t, dir, rel, "---\ntitle: "+title+"\nscope: [global]\nsummary: sum\n---\nbody\n")
}

func TestEmbedMissing(t *testing.T) {
	ctx := context.Background()
	kdir := t.TempDir()
	writeTitledCard(t, kdir, "rules/a.md", "Card A")
	writeTitledCard(t, kdir, "rules/b.md", "Card B")
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}

	fe := &fakeEmbedder{}
	n, err := EmbedMissing(ctx, s, fe, "m")
	if err != nil || n != 2 {
		t.Fatalf("embedded %d (%v), want 2", n, err)
	}
	// Idempotent: nothing to do, no Ollama call.
	before := fe.calls
	n, err = EmbedMissing(ctx, s, fe, "m")
	if err != nil || n != 0 || fe.calls != before {
		t.Fatalf("second run embedded %d, calls %d→%d — want no-op", n, before, fe.calls)
	}

	// Changed content re-embeds just that card.
	writeTitledCard(t, kdir, "rules/a.md", "Card A changed")
	touchFuture(t, filepath.Join(kdir, "rules/a.md"))
	if _, err := Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}
	n, err = EmbedMissing(ctx, s, fe, "m")
	if err != nil || n != 1 {
		t.Fatalf("after change embedded %d (%v), want 1", n, err)
	}

	// Failure surfaces as error with partial count.
	writeTitledCard(t, kdir, "rules/c.md", "Card C")
	if _, err := Sync(ctx, s, kdir); err != nil {
		t.Fatal(err)
	}
	fe.fail = true
	if _, err := EmbedMissing(ctx, s, fe, "m"); err == nil {
		t.Fatal("want error when embedder fails")
	}
}

// touchFuture bumps mtime so the cheap fingerprint sees the change even when
// the test writes within the same nanosecond bucket.
func touchFuture(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(1e9), info.ModTime().Add(1e9)); err != nil {
		t.Fatal(err)
	}
}
