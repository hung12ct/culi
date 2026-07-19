package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Feedback kinds — event counters on card_stats. Weights are applied at
// multiplier time (utilityWeights), so counters stay interpretable as decayed
// event counts.
const (
	FeedbackInjected   = "injected"
	FeedbackExpanded   = "expanded"   // expand_card pull — strong positive
	FeedbackReferenced = "referenced" // cited by a saved lesson — strongest positive; recorded by the Phase 4 miner
	FeedbackDownvoted  = "downvoted"  // explicit `culi down` (weight 1.0) or abandoned pointer (0.1)
)

// utilityHalfLife is the decay half-life for all counters: feedback from a
// month ago matters ~4x less than feedback from today.
const utilityHalfLife = 14 * 24 * time.Hour

// CardStats is one card's decayed counters.
type CardStats struct {
	Injected, Expanded, Referenced, Downvoted float64
	LastDecayAt                               time.Time
}

// AddFeedback folds decay-to-now into a card's counters and bumps one of them.
// kind must be a Feedback* constant; delta is the event count (0.1 for an
// abandoned pointer, 1 otherwise).
func (s *Store) AddFeedback(ctx context.Context, cardID, kind string, delta float64) error {
	switch kind {
	case FeedbackInjected, FeedbackExpanded, FeedbackReferenced, FeedbackDownvoted:
	default:
		return fmt.Errorf("store: unknown feedback kind %q", kind)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin feedback: %w", err)
	}
	defer tx.Rollback()

	var st CardStats
	var last string
	err = tx.QueryRowContext(ctx, `
		SELECT injected, expanded, referenced, downvoted, last_decay_at
		FROM card_stats WHERE card_id = ?`, cardID).
		Scan(&st.Injected, &st.Expanded, &st.Referenced, &st.Downvoted, &last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("store: reading stats for %s: %w", cardID, err)
	default:
		f := decayFactor(parseStatsTime(last), now)
		st.Injected *= f
		st.Expanded *= f
		st.Referenced *= f
		st.Downvoted *= f
	}
	switch kind {
	case FeedbackInjected:
		st.Injected += delta
	case FeedbackExpanded:
		st.Expanded += delta
	case FeedbackReferenced:
		st.Referenced += delta
	case FeedbackDownvoted:
		st.Downvoted += delta
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO card_stats (card_id, injected, expanded, referenced, downvoted, last_decay_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(card_id) DO UPDATE SET
		  injected = excluded.injected, expanded = excluded.expanded,
		  referenced = excluded.referenced, downvoted = excluded.downvoted,
		  last_decay_at = excluded.last_decay_at`,
		cardID, st.Injected, st.Expanded, st.Referenced, st.Downvoted,
		now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("store: writing stats for %s: %w", cardID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing feedback for %s: %w", cardID, err)
	}
	return nil
}

// UtilityMultipliers returns card_id → ranking multiplier, decayed read-side
// (no writes on the hot path). Laplace-smoothed and clamped to [0.5, 1.5]:
// feedback tunes ordering but can never bury a card (plan §9). Cards without
// stats are simply absent — callers treat missing as 1.0.
func (s *Store) UtilityMultipliers(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT card_id, expanded, referenced, downvoted, last_decay_at FROM card_stats")
	if err != nil {
		return nil, fmt.Errorf("store: reading card stats: %w", err)
	}
	defer rows.Close()
	now := time.Now().UTC()
	out := make(map[string]float64)
	for rows.Next() {
		var id, last string
		var expanded, referenced, downvoted float64
		if err := rows.Scan(&id, &expanded, &referenced, &downvoted, &last); err != nil {
			return nil, fmt.Errorf("store: scanning card stats: %w", err)
		}
		f := decayFactor(parseStatsTime(last), now)
		pos := 3*expanded*f + 5*referenced*f
		neg := 5 * downvoted * f
		m := 2 * (pos + 1) / (pos + neg + 2)
		out[id] = min(1.5, max(0.5, m))
	}
	return out, rows.Err()
}

// PenalizeAbandonedPointers applies the −0.5-equivalent nudge (0.1 downvote
// events × weight 5) to cards injected at hook granularity this session and
// never expanded. Only pointers earn negative inference: pushed bodies are
// unobservable (plan §9). Runs at session end, off the hot path.
func (s *Store) PenalizeAbandonedPointers(ctx context.Context, sessionID string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT card_id FROM injections
		WHERE session_id = ? AND granularity = 'hook'
		  AND card_id NOT IN (
		    SELECT card_id FROM injections
		    WHERE session_id = ? AND granularity != 'hook')`,
		sessionID, sessionID)
	if err != nil {
		return fmt.Errorf("store: finding abandoned pointers: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("store: scanning abandoned pointer: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: iterating abandoned pointers: %w", err)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.AddFeedback(ctx, id, FeedbackDownvoted, 0.1); err != nil {
			return err
		}
	}
	return nil
}

// decayFactor returns 0.5^(elapsed/halfLife); 1.0 for zero/garbled timestamps.
func decayFactor(last, now time.Time) float64 {
	if last.IsZero() || !now.After(last) {
		return 1
	}
	return math.Pow(0.5, now.Sub(last).Hours()/utilityHalfLife.Hours())
}

// parseStatsTime tolerates both Go RFC3339Nano and SQLite strftime defaults.
func parseStatsTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
