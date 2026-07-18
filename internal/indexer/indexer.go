// Package indexer syncs the knowledge file store into the SQLite index.
// Files are truth: the sync is one-directional and idempotent.
package indexer

import (
	"context"
	"fmt"

	"github.com/hung12ct/culi/internal/knowledge"
	"github.com/hung12ct/culi/internal/store"
)

// Result summarizes one sync run.
type Result struct {
	Upserted int
	Deleted  int
	Skipped  []string // relPaths that failed to parse (logged, not fatal)
}

// Sync walks knowledgeDir and reconciles the index: changed files (by
// mtime/size, then content hash) are re-parsed and upserted; vanished files
// are deleted. A single bad card is skipped, never fatal.
func Sync(ctx context.Context, s *store.Store, knowledgeDir string) (Result, error) {
	var res Result
	files, err := knowledge.Walk(knowledgeDir)
	if err != nil {
		return res, fmt.Errorf("indexer: %w", err)
	}
	states, err := s.FileStates(ctx)
	if err != nil {
		return res, fmt.Errorf("indexer: %w", err)
	}

	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f.RelPath] = true
		prev, ok := states[f.RelPath]
		if ok && prev.MTime == f.MTime && prev.Size == f.Size {
			continue // unchanged by cheap fingerprint
		}
		card, err := knowledge.ReadCard(knowledgeDir, f.RelPath)
		if err != nil {
			res.Skipped = append(res.Skipped, f.RelPath)
			continue
		}
		// Upsert failures (e.g. a duplicate explicit `id:` from a copied file)
		// are skipped like parse errors — one bad card must never abort the
		// sync, or every SessionStart reindex breaks invisibly (§4).
		if err := s.UpsertCard(ctx, card, f.MTime, f.Size); err != nil {
			res.Skipped = append(res.Skipped, f.RelPath)
			continue
		}
		res.Upserted++
	}

	for path := range states {
		if seen[path] {
			continue
		}
		if err := s.DeleteCardByPath(ctx, path); err != nil {
			return res, fmt.Errorf("indexer: deleting %s: %w", path, err)
		}
		res.Deleted++
	}
	return res, nil
}

// Full drops the index and re-syncs everything from files.
func Full(ctx context.Context, s *store.Store, knowledgeDir string) (Result, error) {
	if err := s.Rebuild(ctx); err != nil {
		return Result{}, fmt.Errorf("indexer: rebuild: %w", err)
	}
	return Sync(ctx, s, knowledgeDir)
}
