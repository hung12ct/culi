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
		pos := wExpanded*expanded*f + wReferenced*referenced*f
		neg := wDownvoted * downvoted * f
		m := 2 * (pos + 1) / (pos + neg + 2)
		out[id] = min(1.5, max(0.5, m))
	}
	return out, rows.Err()
}

// SessionContentCards lists the cards whose actual content reached the model
// this session — summary or body granularity, never bare pointers. It is the
// candidate set for usage attribution: only a card the model could read can be
// credited for having been used. Pointers keep the opposite contract (expand
// or be penalized, see PenalizeAbandonedPointers), so excluding them here
// prevents one line of teaser text earning the same credit as a full body.
func (s *Store) SessionContentCards(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT card_id FROM injections
		WHERE session_id = ? AND granularity != 'hook'`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: listing session content cards: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scanning session content card: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimAttribution reserves the right to credit this session's card usage,
// returning true exactly once per session. Usage credit is additive, so it must
// not be applied twice — and the job label cannot be trusted for that: the
// Codex rollout scanner stamps trigger "session-end" on every rescan of a
// growing rollout, and a resumed Claude session reaches SessionEnd more than
// once. Both would otherwise re-credit the whole session from offset 0.
//
// One statement, so two workers racing the same session cannot both win: the
// conflicting UPDATE is skipped when the row was already claimed, leaving
// RowsAffected at 0. The marker rides in session_state, which PruneStale
// already bounds to the retention window.
func (s *Store) ClaimAttribution(ctx context.Context, sessionID string, now time.Time) (bool, error) {
	return s.claimSessionOnce(ctx, sessionID, colAttributedAt, now)
}

// Once-per-session marker columns on session_state. Attribution and the
// pointer penalty need separate markers: they are applied by different
// processes at different times (the learn worker after mining vs. the hook at
// session end), so a shared marker would let whichever ran first silently
// cancel the other.
const (
	colAttributedAt = "attributed_at"
	colPenalizedAt  = "penalized_at"
)

// claimSessionOnce sets a session_state marker column, returning true only for
// the caller that set it. column comes from the constants above and is never
// caller-supplied — SQLite cannot parameterize identifiers, so this must stay
// closed to outside input.
func (s *Store) claimSessionOnce(ctx context.Context, sessionID, column string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO session_state (session_id, %[1]s) VALUES (?, ?)
		ON CONFLICT(session_id) DO UPDATE SET %[1]s = excluded.%[1]s
		WHERE session_state.%[1]s = ''`, column),
		sessionID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("store: claiming %s for %s: %w", column, sessionID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claiming %s for %s: %w", column, sessionID, err)
	}
	return n > 0, nil
}

// PenalizeAbandonedPointers applies the −0.5-equivalent nudge (0.1 downvote
// events × weight 5) to cards injected at hook granularity this session and
// never expanded. Only pointers earn negative inference: pushed bodies are
// unobservable (plan §9). Runs at session end, off the per-prompt hot path.
//
// Claims the session first so the penalty lands at most once. SessionEnd is
// not once-per-session — a resumed Claude session reaches it again on the same
// session_id — and the penalty is additive, so a second pass would push the
// same pointers toward "noisy" twice as fast as their evidence warrants.
// Claiming inside keeps the invariant with the operation rather than relying
// on every caller to remember it.
//
// The trade this makes: pointers injected *after* a resumed session's first
// end are never penalized. That is the deliberate direction — negative
// inference here is already conservative (see ClassifyEffectiveness: silence
// alone never marks a card noisy), so under-penalizing costs a slower verdict
// while over-penalizing manufactures one the evidence does not support.
func (s *Store) PenalizeAbandonedPointers(ctx context.Context, sessionID string) error {
	claimed, err := s.claimSessionOnce(ctx, sessionID, colPenalizedAt, time.Now())
	if err != nil {
		return err
	}
	if !claimed {
		return nil // already settled by an earlier session end
	}
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
