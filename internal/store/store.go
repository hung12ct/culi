// Package store owns the SQLite index. Knowledge-card search data can be
// rebuilt from files; runtime injection history is preserved by explicit
// schema migrations.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schemaVersion is compared against PRAGMA user_version at open. Every bump
// must add an in-place migration below; an unknown version fails closed rather
// than risking user activity history.
//
// v2: injections gained a `cwd` column so the review console can show which
// repo each injection happened in.
// v3: injections gained a `harness` column so attribution (claude vs codex) is
// authoritative rather than parsed out of the session_id prefix.
const schemaVersion = 3

var schemaMigrations = map[int][]string{
	1: {
		"ALTER TABLE injections ADD COLUMN cwd TEXT NOT NULL DEFAULT ''",
	},
	2: {
		"ALTER TABLE injections ADD COLUMN harness TEXT NOT NULL DEFAULT 'claude'",
		"UPDATE injections SET harness = 'codex' WHERE session_id LIKE 'codex:%'",
	},
}

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

// Rebuild recreates only the file-backed card search index. Runtime-only
// injections, card stats, session state, and metadata are preserved.
func (s *Store) Rebuild(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: beginning card-index rebuild: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS cards_fts",
		"DROP TABLE IF EXISTS embeddings",
		"DROP TABLE IF EXISTS cards",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: rebuilding card index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, cardSchemaDDL); err != nil {
		return fmt.Errorf("store: recreating card index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing card-index rebuild: %w", err)
	}
	return nil
}

func (s *Store) ensureSchema(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: reading user_version: %w", err)
	}
	switch {
	case v == schemaVersion:
		return nil
	case v == 0:
		return s.createSchema(ctx)
	case v > schemaVersion:
		return fmt.Errorf("store: database schema version %d is newer than supported version %d", v, schemaVersion)
	default:
		return s.migrateSchema(ctx)
	}
}

// migrateSchema advances the schema to schemaVersion under a single write lock.
// It pins one pooled connection and opens with BEGIN IMMEDIATE so that two
// sessions racing their first Open after a binary upgrade serialize here: the
// winner migrates and bumps user_version, the loser re-reads the bumped version
// inside the lock and no-ops rather than replaying non-idempotent ALTERs (which
// would fail with "duplicate column name"). busy_timeout(2000) makes the loser
// wait out the winner instead of erroring immediately.
func (s *Store) migrateSchema(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: acquiring migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("store: beginning schema migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Best-effort rollback; use a fresh context so a cancelled ctx
			// still releases the write lock.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var from int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&from); err != nil {
		return fmt.Errorf("store: re-reading user_version: %w", err)
	}
	for from < schemaVersion {
		stmts, ok := schemaMigrations[from]
		if !ok {
			return fmt.Errorf("store: no migration from schema version %d", from)
		}
		for _, stmt := range stmts {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("store: migrating schema version %d: %w", from, err)
			}
		}
		from++
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", from)); err != nil {
			return fmt.Errorf("store: setting migrated schema version %d: %w", from, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("store: committing schema migration: %w", err)
	}
	committed = true
	return nil
}

const cardSchemaDDL = `
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
`

const runtimeSchemaDDL = `
CREATE TABLE injections (
  id INTEGER PRIMARY KEY,
  session_id  TEXT NOT NULL,
  ts          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  event       TEXT NOT NULL,
  card_id     TEXT NOT NULL,
  granularity TEXT NOT NULL,             -- hook | summary | body
  prompt_hash TEXT NOT NULL DEFAULT '',
  tokens      INTEGER NOT NULL,
  cwd         TEXT NOT NULL DEFAULT '',  -- working dir at injection time (repo attribution)
  harness     TEXT NOT NULL DEFAULT 'claude'  -- source agent: claude | codex
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

func (s *Store) createSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, cardSchemaDDL+runtimeSchemaDDL); err != nil {
		return fmt.Errorf("store: creating schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("store: setting user_version: %w", err)
	}
	return nil
}
