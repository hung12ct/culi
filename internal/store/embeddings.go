package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hung12ct/culi/internal/embed"
)

// UpsertEmbedding stores one card's vector, replacing any prior row. The
// content hash ties the vector to the card revision it was computed from.
func (s *Store) UpsertEmbedding(ctx context.Context, rowid int64, model string, vec []float32, contentHash string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO embeddings (card_rowid, model, dim, vec, content_hash)
		VALUES (?,?,?,?,?)
		ON CONFLICT(card_rowid) DO UPDATE SET
		  model = excluded.model, dim = excluded.dim,
		  vec = excluded.vec, content_hash = excluded.content_hash`,
		rowid, model, len(vec), embed.EncodeVec(vec), contentHash); err != nil {
		return fmt.Errorf("store: upserting embedding for rowid %d: %w", rowid, err)
	}
	return nil
}

// Embeddings returns rowid → vector for every card whose stored vector is
// FRESH: same model and same content hash as the current card row. Stale
// vectors are simply absent — the funnel degrades per-card, never errors.
func (s *Store) Embeddings(ctx context.Context, model string) (map[int64][]float32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.card_rowid, e.vec
		FROM embeddings e JOIN cards c ON c.rowid = e.card_rowid
		WHERE e.model = ? AND e.content_hash = c.content_hash`, model)
	if err != nil {
		return nil, fmt.Errorf("store: reading embeddings: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]float32)
	for rows.Next() {
		var rowid int64
		var blob []byte
		if err := rows.Scan(&rowid, &blob); err != nil {
			return nil, fmt.Errorf("store: scanning embedding: %w", err)
		}
		if v := embed.DecodeVec(blob); v != nil {
			out[rowid] = v
		}
	}
	return out, rows.Err()
}

// EmbedTarget is a card needing (re-)embedding.
type EmbedTarget struct {
	Rowid       int64
	ContentHash string
	Text        string // title + summary — what gets embedded
}

// MissingEmbeddings lists cards with no fresh vector for model.
func (s *Store) MissingEmbeddings(ctx context.Context, model string) ([]EmbedTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.rowid, c.content_hash, c.title, c.summary
		FROM cards c LEFT JOIN embeddings e ON e.card_rowid = c.rowid
		WHERE e.card_rowid IS NULL OR e.model != ? OR e.content_hash != c.content_hash`, model)
	if err != nil {
		return nil, fmt.Errorf("store: listing missing embeddings: %w", err)
	}
	defer rows.Close()
	var out []EmbedTarget
	for rows.Next() {
		var t EmbedTarget
		var title, summary string
		if err := rows.Scan(&t.Rowid, &t.ContentHash, &title, &summary); err != nil {
			return nil, fmt.Errorf("store: scanning embed target: %w", err)
		}
		t.Text = title + "\n" + summary
		out = append(out, t)
	}
	return out, rows.Err()
}

// MetaGet reads one meta key ("" when absent).
func (s *Store) MetaGet(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: reading meta %s: %w", key, err)
	}
	return v, nil
}

// MetaSet writes one meta key.
func (s *Store) MetaSet(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("store: setting meta %s: %w", key, err)
	}
	return nil
}
