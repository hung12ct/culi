package indexer

import (
	"context"
	"fmt"

	"github.com/hung12ct/culi/internal/embed"
	"github.com/hung12ct/culi/internal/store"
)

// embedBatch bounds one Ollama request: keeps request bodies small and lets a
// ctx deadline cut a large backlog mid-way with partial progress persisted.
const embedBatch = 64

// EmbedMissing computes vectors for every card lacking a fresh one (new card,
// changed content, or model switch). Returns the number embedded. A dead
// Ollama is an error for the CALLER to classify: `culi index` prints it,
// SessionStart logs and moves on — BM25 keeps working either way.
func EmbedMissing(ctx context.Context, s *store.Store, e embed.Embedder, model string) (int, error) {
	targets, err := s.MissingEmbeddings(ctx, model)
	if err != nil {
		return 0, fmt.Errorf("indexer: %w", err)
	}
	done := 0
	for start := 0; start < len(targets); start += embedBatch {
		batch := targets[start:min(start+embedBatch, len(targets))]
		texts := make([]string, len(batch))
		for i, t := range batch {
			texts[i] = t.Text
		}
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return done, fmt.Errorf("indexer: embedding batch: %w", err)
		}
		for i, t := range batch {
			if err := s.UpsertEmbedding(ctx, t.Rowid, model, vecs[i], t.ContentHash); err != nil {
				return done, fmt.Errorf("indexer: %w", err)
			}
			done++
		}
	}
	return done, nil
}
