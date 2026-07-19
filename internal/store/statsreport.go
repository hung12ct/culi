package store

import (
	"context"
	"fmt"
	"time"
)

// InjectionAgg is one (event, granularity) bucket of the injection history.
// Only the retention window exists — PruneStale bounds it to ~7 days.
type InjectionAgg struct {
	Event       string
	Granularity string
	Count       int
	Tokens      int
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
