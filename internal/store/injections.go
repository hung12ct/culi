package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hung12ct/culi/internal/harness"
)

// Granularity levels, ordered: hook < summary < body. Session dedup is
// monotonic — a card is only re-sent at a strictly higher level (14.43).
const (
	GranHook    = "hook"
	GranSummary = "summary"
	GranBody    = "body"
)

// GranLevel maps a granularity name to its ordering level.
func GranLevel(g string) int {
	switch g {
	case GranBody:
		return 3
	case GranSummary:
		return 2
	case GranHook:
		return 1
	default:
		return 0
	}
}

// InjectionRecord is one emitted card at one granularity.
type InjectionRecord struct {
	CardID      string
	Granularity string
	Tokens      int
}

// InjectedLevels returns card_id → highest granularity level already injected
// in this session.
func (s *Store) InjectedLevels(ctx context.Context, sessionID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT card_id, granularity FROM injections WHERE session_id = ?", sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: reading injections: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id, gran string
		if err := rows.Scan(&id, &gran); err != nil {
			return nil, fmt.Errorf("store: scanning injection: %w", err)
		}
		if lv := GranLevel(gran); lv > out[id] {
			out[id] = lv
		}
	}
	return out, rows.Err()
}

// RecordInjections logs emitted cards for dedup + stats in one transaction.
// cwd is the working directory at injection time, stored for repo attribution
// in the review console ("" when unknown — e.g. a non-git prompt). h attributes
// the injection to its source agent for the console's harness filter.
func (s *Store) RecordInjections(ctx context.Context, sessionID, event, promptHash, cwd string, h harness.Harness, recs []InjectionRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin injection log: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO injections (session_id, event, card_id, granularity, prompt_hash, tokens, cwd, harness)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store: preparing injection insert: %w", err)
	}
	defer stmt.Close()
	for _, r := range recs {
		if _, err := stmt.ExecContext(ctx, sessionID, event, r.CardID, r.Granularity, promptHash, r.Tokens, cwd, h.String()); err != nil {
			return fmt.Errorf("store: logging injection %s: %w", r.CardID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing injection log: %w", err)
	}
	return nil
}

// PruneStale deletes injection/session rows older than the retention window.
// Cheap, runs at SessionStart — keeps the disposable history bounded.
func (s *Store) PruneStale(ctx context.Context, retainDays int) error {
	cutoff := fmt.Sprintf("-%d days", retainDays)
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM injections WHERE ts < datetime('now', ?)", cutoff); err != nil {
		return fmt.Errorf("store: pruning injections: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM session_state WHERE updated_at < datetime('now', ?)", cutoff); err != nil {
		return fmt.Errorf("store: pruning session state: %w", err)
	}
	return nil
}

// ResetSession clears dedup state for a session (SessionStart with
// source=compact: the context window was rebuilt, prior injections are gone).
func (s *Store) ResetSession(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM injections WHERE session_id = ?", sessionID); err != nil {
		return fmt.Errorf("store: resetting session %s: %w", sessionID, err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM session_state WHERE session_id = ?", sessionID); err != nil {
		return fmt.Errorf("store: resetting session state %s: %w", sessionID, err)
	}
	return nil
}

// maxStoredPrompt bounds session_state growth: the novelty Jaccard is
// well-served by a 4KB prefix; storing full 1MB pastes every prompt is pure
// bloat (14.36).
const maxStoredPrompt = 4096

// SwapLastPrompt returns the previous prompt for the gate's novelty check
// ("" if none) and stores the new one, bounded to maxStoredPrompt bytes.
func (s *Store) SwapLastPrompt(ctx context.Context, sessionID, prompt string) (string, error) {
	if len(prompt) > maxStoredPrompt {
		prompt = prompt[:maxStoredPrompt]
	}
	var prev string
	err := s.db.QueryRowContext(ctx,
		"SELECT last_prompt FROM session_state WHERE session_id = ?", sessionID).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: reading session state: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO session_state (session_id, last_prompt) VALUES (?,?)
		ON CONFLICT(session_id) DO UPDATE SET
		  last_prompt = excluded.last_prompt,
		  updated_at  = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		sessionID, prompt); err != nil {
		return "", fmt.Errorf("store: writing session state: %w", err)
	}
	return prev, nil
}
