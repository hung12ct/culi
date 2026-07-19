package store

import (
	"context"
	"fmt"
	"time"
)

// InjectionAgg is one (event, granularity) bucket of the injection history.
// Only the retention window exists — PruneStale bounds it to ~7 days. JSON
// tags serve `culi stats --json` (snake_case like the rest of that schema).
type InjectionAgg struct {
	Event       string `json:"event"`
	Granularity string `json:"granularity"`
	Count       int    `json:"count"`
	Tokens      int    `json:"tokens"`
}

// InjectionAggs aggregates the injection log for `culi stats`.
func (s *Store) InjectionAggs(ctx context.Context) ([]InjectionAgg, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT event, granularity, COUNT(*), COALESCE(SUM(tokens), 0)
		FROM injections GROUP BY event, granularity
		ORDER BY event, granularity`)
	if err != nil {
		return nil, fmt.Errorf("store: aggregating injections: %w", err)
	}
	defer rows.Close()
	var out []InjectionAgg
	for rows.Next() {
		var a InjectionAgg
		if err := rows.Scan(&a.Event, &a.Granularity, &a.Count, &a.Tokens); err != nil {
			return nil, fmt.Errorf("store: scanning injection agg: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SessionCount returns the number of distinct sessions in the injection log.
func (s *Store) SessionCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT session_id) FROM injections").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting sessions: %w", err)
	}
	return n, nil
}

// SessionTokens sums the tokens injected into one session — the statusline's
// "what did this session cost so far" number.
func (s *Store) SessionTokens(ctx context.Context, sessionID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(tokens), 0) FROM injections WHERE session_id = ?",
		sessionID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: summing session tokens: %w", err)
	}
	return n, nil
}

// InjectedCardIDs returns the set of card IDs with at least one row in the
// injection log's retention window — the "was this card ever shown" half of
// the staleness report.
func (s *Store) InjectedCardIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT card_id FROM injections")
	if err != nil {
		return nil, fmt.Errorf("store: listing injected cards: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scanning injected card: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// AllCardStats returns every card's counters decayed to now — the raw
// material for stats' top-pulled / noisy lists. Off the hot path.
func (s *Store) AllCardStats(ctx context.Context, now time.Time) (map[string]CardStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT card_id, injected, expanded, referenced, downvoted, last_decay_at
		FROM card_stats`)
	if err != nil {
		return nil, fmt.Errorf("store: reading all card stats: %w", err)
	}
	defer rows.Close()
	out := map[string]CardStats{}
	for rows.Next() {
		var id, last string
		var st CardStats
		if err := rows.Scan(&id, &st.Injected, &st.Expanded, &st.Referenced, &st.Downvoted, &last); err != nil {
			return nil, fmt.Errorf("store: scanning card stats: %w", err)
		}
		st.LastDecayAt = parseStatsTime(last)
		f := decayFactor(st.LastDecayAt, now)
		st.Injected *= f
		st.Expanded *= f
		st.Referenced *= f
		st.Downvoted *= f
		out[id] = st
	}
	return out, rows.Err()
}
