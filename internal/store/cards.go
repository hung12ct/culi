package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hung12ct/culi/internal/knowledge"
)

// FileState is the (mtime, size) fingerprint the indexer compares before
// re-hashing a file.
type FileState struct {
	Path  string
	MTime int64
	Size  int64
	Hash  string
}

// FileStates returns path → state for every indexed card.
func (s *Store) FileStates(ctx context.Context) (map[string]FileState, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT path, mtime, size, content_hash FROM cards")
	if err != nil {
		return nil, fmt.Errorf("store: listing file states: %w", err)
	}
	defer rows.Close()
	out := make(map[string]FileState)
	for rows.Next() {
		var fs FileState
		if err := rows.Scan(&fs.Path, &fs.MTime, &fs.Size, &fs.Hash); err != nil {
			return nil, fmt.Errorf("store: scanning file state: %w", err)
		}
		out[fs.Path] = fs
	}
	return out, rows.Err()
}

// UpsertCard inserts or replaces a card and its FTS row atomically.
func (s *Store) UpsertCard(ctx context.Context, c knowledge.Card, mtime, size int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin upsert: %w", err)
	}
	defer tx.Rollback()

	// Delete any existing row (and FTS row) for this path, then insert fresh.
	var oldRowid int64
	err = tx.QueryRowContext(ctx, "SELECT rowid FROM cards WHERE path = ?", c.Path).Scan(&oldRowid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("store: looking up %s: %w", c.Path, err)
	default:
		if _, err := tx.ExecContext(ctx, "DELETE FROM cards_fts WHERE rowid = ?", oldRowid); err != nil {
			return fmt.Errorf("store: deleting stale fts row: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM cards WHERE rowid = ?", oldRowid); err != nil {
			return fmt.Errorf("store: deleting stale card: %w", err)
		}
	}

	shortID, err := uniqueShortID(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO cards (id, short_id, path, type, title, summary, body, scopes, key,
		                   trig_kw, trig_glob, aliases, baseline, status,
		                   tok_summary, tok_body, content_hash, mtime, size)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, shortID, c.Path, c.Type, c.Title, c.Summary, c.Body,
		jsonArr(c.Scopes), c.Key, jsonArr(c.Triggers.Keywords), jsonArr(c.Triggers.Globs),
		jsonArr(c.Aliases), boolInt(c.Baseline), c.Status,
		c.TokSummary, c.TokBody, c.ContentHash, mtime, size)
	if err != nil {
		return fmt.Errorf("store: inserting card %s: %w", c.ID, err)
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: rowid for %s: %w", c.ID, err)
	}
	// extra column: triggers + aliases + key, so keyword-ish signals are searchable.
	extra := strings.Join(c.Triggers.Keywords, " ") + " " + strings.Join(c.Aliases, " ") + " " + c.Key
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO cards_fts (rowid, title, summary, body, extra) VALUES (?,?,?,?,?)",
		rowid, c.Title, c.Summary, c.Body, extra); err != nil {
		return fmt.Errorf("store: inserting fts row for %s: %w", c.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing upsert %s: %w", c.ID, err)
	}
	return nil
}

// DeleteCardByPath removes a vanished file's card + FTS row.
func (s *Store) DeleteCardByPath(ctx context.Context, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin delete: %w", err)
	}
	defer tx.Rollback()
	var rowid int64
	err = tx.QueryRowContext(ctx, "SELECT rowid FROM cards WHERE path = ?", path).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: looking up %s: %w", path, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM cards_fts WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("store: deleting fts row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM cards WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("store: deleting card: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing delete %s: %w", path, err)
	}
	return nil
}

// StoredCard is a card row hydrated from the DB.
type StoredCard struct {
	knowledge.Card
	Rowid   int64
	ShortID string
}

// AllCards returns every card (corpus is small; callers filter in Go).
func (s *Store) AllCards(ctx context.Context) ([]StoredCard, error) {
	return s.queryCards(ctx, "SELECT "+cardCols+" FROM cards")
}

// AllCardsMeta returns every card WITHOUT bodies — the hot-path scan.
// Bodies are hydrated per-candidate via CardsByRowid only for the few cards
// the packer actually emits.
func (s *Store) AllCardsMeta(ctx context.Context) ([]StoredCard, error) {
	return s.queryCards(ctx, "SELECT "+strings.Replace(cardCols, " body,", " '' AS body,", 1)+" FROM cards")
}

// CardsByRowid hydrates specific rows (e.g. FTS hits), preserving no order.
func (s *Store) CardsByRowid(ctx context.Context, rowids []int64) ([]StoredCard, error) {
	if len(rowids) == 0 {
		return nil, nil
	}
	ph := strings.Repeat("?,", len(rowids))
	args := make([]any, len(rowids))
	for i, r := range rowids {
		args[i] = r
	}
	return s.queryCards(ctx,
		"SELECT "+cardCols+" FROM cards WHERE rowid IN ("+ph[:len(ph)-1]+")", args...)
}

// CardByID fetches one card by full ID or 4-hex short ID.
func (s *Store) CardByID(ctx context.Context, id string) (StoredCard, error) {
	cards, err := s.queryCards(ctx,
		"SELECT "+cardCols+" FROM cards WHERE id = ? OR short_id = ?", id, id)
	if err != nil {
		return StoredCard{}, err
	}
	if len(cards) == 0 {
		return StoredCard{}, fmt.Errorf("store: card %q not found", id)
	}
	return cards[0], nil
}

const cardCols = `rowid, id, short_id, path, type, title, summary, body, scopes, key,
	trig_kw, trig_glob, aliases, baseline, status, tok_summary, tok_body, content_hash`

func (s *Store) queryCards(ctx context.Context, q string, args ...any) ([]StoredCard, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: querying cards: %w", err)
	}
	defer rows.Close()
	var out []StoredCard
	for rows.Next() {
		var c StoredCard
		var scopes, kw, glob, aliases string
		var baseline int
		if err := rows.Scan(&c.Rowid, &c.ID, &c.ShortID, &c.Path, &c.Type, &c.Title,
			&c.Summary, &c.Body, &scopes, &c.Key, &kw, &glob, &aliases,
			&baseline, &c.Status, &c.TokSummary, &c.TokBody, &c.ContentHash); err != nil {
			return nil, fmt.Errorf("store: scanning card: %w", err)
		}
		c.Scopes = fromJSONArr(scopes)
		c.Triggers.Keywords = fromJSONArr(kw)
		c.Triggers.Globs = fromJSONArr(glob)
		c.Aliases = fromJSONArr(aliases)
		c.Baseline = baseline != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// SearchBM25 runs an FTS5 MATCH over the corpus and returns (rowid, rank)
// pairs, best first. matchExpr MUST be pre-sanitized by the caller
// (internal/retrieve owns the sanitizer) — raw prompt text here is a bug.
type BM25Hit struct {
	Rowid int64
	Rank  float64 // bm25(): lower (more negative) = better
}

func (s *Store) SearchBM25(ctx context.Context, matchExpr string, limit int) ([]BM25Hit, error) {
	// Column weights: title 4x, summary 2x, body 1x, extra (triggers/aliases) 3x.
	rows, err := s.db.QueryContext(ctx, `
		SELECT rowid, bm25(cards_fts, 4.0, 2.0, 1.0, 3.0) AS rank
		FROM cards_fts WHERE cards_fts MATCH ?
		ORDER BY rank LIMIT ?`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("store: bm25 search: %w", err)
	}
	defer rows.Close()
	var out []BM25Hit
	for rows.Next() {
		var h BM25Hit
		if err := rows.Scan(&h.Rowid, &h.Rank); err != nil {
			return nil, fmt.Errorf("store: scanning bm25 hit: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// uniqueShortID derives a 4-hex short ID, extending on collision (5, 6 … hex
// chars of the same hash) so pointers stay stable and unique.
func uniqueShortID(ctx context.Context, tx *sql.Tx, cardID string) (string, error) {
	full := knowledge.ShortID(cardID) // 4 hex chars from sha256 prefix
	candidate := full
	for n := 0; ; n++ {
		var exists int
		err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM cards WHERE short_id = ? AND id != ?", candidate, cardID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("store: short-id collision check: %w", err)
		}
		// Collision with a different card: disambiguate deterministically.
		candidate = fmt.Sprintf("%s%d", full, n)
	}
}

func jsonArr(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func fromJSONArr(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
