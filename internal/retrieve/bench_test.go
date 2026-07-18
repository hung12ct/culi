package retrieve_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/pack"
	"github.com/hung12ct/culi/internal/retrieve"
	"github.com/hung12ct/culi/internal/store"
)

// BenchmarkHotPath measures the in-process retrieval+pack cost over a
// realistic corpus (500 cards). The Phase 1 exit criterion (hook p95 <100ms)
// budgets ~20ms for process start + DB open on top of this; the funnel itself
// must stay in single-digit milliseconds.
func BenchmarkHotPath(b *testing.B) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(b.TempDir(), "index.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	topics := []string{"error wrapping", "commit style", "webhook retries",
		"sqlite pragma", "context deadline", "test fixtures", "goroutine leak",
		"token budget", "yaml frontmatter", "git worktree"}
	for i := range 500 {
		topic := topics[i%len(topics)]
		c := knowledge.Card{
			ID: fmt.Sprintf("rules/card-%03d", i), Path: fmt.Sprintf("rules/card-%03d.md", i),
			Type: "rule", Title: fmt.Sprintf("Rule %d about %s", i, topic),
			Summary: fmt.Sprintf("guidance %d on %s in this codebase", i, topic),
			Body:    fmt.Sprintf("long body %d explaining %s with examples and caveats", i, topic),
			Scopes:  []string{"global"},
		}
		c.TokSummary = knowledge.EstimateTokens(c.Summary)
		c.TokBody = knowledge.EstimateTokens(c.Body)
		c.ContentHash = c.ID
		if err := s.UpsertCard(ctx, c, int64(i), 100); err != nil {
			b.Fatal(err)
		}
	}

	r := &retrieve.Retriever{Store: s}
	sc := retrieve.Scope{Allowed: map[string]bool{"global": true}}
	loader := func(rowids []int64) (map[int64]string, error) {
		cards, err := s.CardsByRowid(ctx, rowids)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]string, len(cards))
		for _, c := range cards {
			out[c.Rowid] = c.Body
		}
		return out, nil
	}

	b.ResetTimer()
	for b.Loop() {
		cands, err := r.Retrieve(ctx, "how should webhook retries handle errors here?", sc)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := pack.Pack(cands, nil, 700, loader); err != nil {
			b.Fatal(err)
		}
	}
}
