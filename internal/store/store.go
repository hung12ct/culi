// Package store owns the SQLite index. The DB is a disposable cache of the
// knowledge file store: schema changes bump schemaVersion and trigger a full
// drop-and-rebuild — never an ALTER migration.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schemaVersion is compared against PRAGMA user_version at open; a mismatch
// drops every table and recreates. Only disposable history is lost.
//
// v2: injections gained a `cwd` column so the review console can show which
// repo each injection happened in.
const schemaVersion = 2

// Store wraps the SQLite handle. Safe for concurrent use (database/sql pools;
// WAL allows one writer + many readers).
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the index at path. WAL + busy_timeout are
// set per-connection via the DSN so every pooled connection gets them.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: creating db dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.ensureSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying pool.
func (s *Store) Close() error { return s.db.Close() }

// Rebuild drops all tables and recreates the schema. Callers re-index from
// the file store afterwards.
func (s *Store) Rebuild(ctx context.Context) error {
	if err := s.dropAll(ctx); err != nil {
		return err
	}
	return s.createSchema(ctx)
}

func (s *Store) ensureSchema(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: reading user_version: %w", err)
	}
	if v == schemaVersion {
		return nil
	}
	if v != 0 {
		// Version mismatch: drop and rebuild rather than migrate.
		if err := s.dropAll(ctx); err != nil {
			return err
		}
	}
	return s.createSchema(ctx)
}

func (s *Store) dropAll(ctx context.Context) error {
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS cards_fts",
		"DROP TABLE IF EXISTS embeddings",
		"DROP TABLE IF EXISTS injections",
		"DROP TABLE IF EXISTS session_state",
		"DROP TABLE IF EXISTS card_stats",
		"DROP TABLE IF EXISTS cards",
		"DROP TABLE IF EXISTS meta",
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: dropping tables: %w", err)
		}
	}
	return nil
}

func (s *Store) createSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE cards (
  rowid        INTEGER PRIMARY KEY,
  id           TEXT NOT NULL UNIQUE,
  short_id     TEXT NOT NULL UNIQUE,
  path         TEXT NOT NULL UNIQUE,
  type         TEXT NOT NULL,
  title        TEXT NOT NULL,
  summary      TEXT NOT NULL,
  body         TEXT NOT NULL,
  scopes       TEXT NOT NULL,            -- JSON array
  key          TEXT NOT NULL DEFAULT '',
  trig_kw      TEXT NOT NULL DEFAULT '[]',
  trig_glob    TEXT NOT NULL DEFAULT '[]',
  aliases      TEXT NOT NULL DEFAULT '[]',
  baseline     INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT '',
  tok_summary  INTEGER NOT NULL,
  tok_body     INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  mtime        INTEGER NOT NULL,
  size         INTEGER NOT NULL
);

-- Standalone FTS5 (own storage): corpus is small, duplication is cheaper than
-- external-content sync bugs. porter for stemming; remove_diacritics 2 so
-- Vietnamese matches with or without diacritics.
CREATE VIRTUAL TABLE cards_fts USING fts5(
  title, summary, body, extra,
  tokenize='porter unicode61 remove_diacritics 2'
);

-- Reserved for Phase 3 (Ollama vectors).
CREATE TABLE embeddings (
  card_rowid   INTEGER PRIMARY KEY REFERENCES cards(rowid) ON DELETE CASCADE,
  model        TEXT NOT NULL,
  dim          INTEGER NOT NULL,
  vec          BLOB NOT NULL,
  content_hash TEXT NOT NULL
);

CREATE TABLE injections (
  id INTEGER PRIMARY KEY,
  session_id  TEXT NOT NULL,
  ts          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  event       TEXT NOT NULL,
  card_id     TEXT NOT NULL,
  granularity TEXT NOT NULL,             -- hook | summary | body
  prompt_hash TEXT NOT NULL DEFAULT '',
  tokens      INTEGER NOT NULL,
  cwd         TEXT NOT NULL DEFAULT ''   -- working dir at injection time (repo attribution)
);
CREATE INDEX idx_inj_session ON injections(session_id, card_id);

-- Small per-session scratch used by the gate (novelty check).
CREATE TABLE session_state (
  session_id  TEXT PRIMARY KEY,
  last_prompt TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE card_stats (
  card_id       TEXT PRIMARY KEY,
  injected      REAL NOT NULL DEFAULT 0,
  expanded      REAL NOT NULL DEFAULT 0,
  referenced    REAL NOT NULL DEFAULT 0,
  downvoted     REAL NOT NULL DEFAULT 0,
  last_decay_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("store: creating schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("store: setting user_version: %w", err)
	}
	return nil
}
